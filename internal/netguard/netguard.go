// Package netguard уводит собственные сокеты ядра мимо туннеля
// (architecture.md §05, дорожка 2).
//
// Когда datapath забирает весь трафик системы (tun, external), сокеты
// ядра по общей таблице маршрутов попали бы в тот же туннель, и туннель
// зависел бы от самого себя. Guard.Control ставится в поле Control у
// net.Dialer и привязывает сокет к физическому интерфейсу: на Windows —
// опциями IP_UNICAST_IF / IPV6_UNICAST_IF, на Linux — SO_BINDTOIFINDEX.
// На других системах Control пока ничего не делает (macOS — этап Э4).
package netguard

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
)

// Guard хранит индексы физического интерфейса для IPv4 и IPv6. Нулевое
// значение готово к работе, но сокеты не пропускает: ни один интерфейс ещё
// не выбран. Индексы меняются на ходу (смена сети): новые сокеты берут новый
// индекс, уже открытые доживают со старым.
type Guard struct {
	index4 atomic.Uint32
	index6 atomic.Uint32

	// mu и choice — последний выбор интерфейса (SelectInterfaces, Watch):
	// с ним Watch сравнивает новый, чтобы понять, сменилась ли сеть.
	mu     sync.Mutex
	choice Choice

	// Logf, если задан, получает строку на каждый привязанный сокет:
	// «[NETGUARD] tcp4 203.0.113.80:443 → интерфейс 3». Главный инструмент
	// отладки петли; CLI ставит его при -v. Задаётся до первого сокета.
	// Имени хоста в строке нет: в Control адрес приходит уже разрешённым.
	Logf func(format string, args ...any)

	// Mark, если не 0, ставится сокетам опцией SO_MARK (только Linux): по
	// ней защита DNS узнаёт сокеты tt и пропускает их запросы к DNS для
	// доменов VK (plan-e4.md, D1). Метке нужен CAP_NET_ADMIN, без него
	// сокет не создаётся (EPERM), поэтому CLI ставит её, только когда
	// защита DNS включена. Задаётся до первого сокета. На Windows не
	// используется: там защита DNS пропускает весь процесс tt.
	Mark uint32

	// NoIPv6 — сокетам ядра IPv6 не давать: выбор интерфейса IPv6
	// отбрасывается, и сокет IPv6 не создаётся, а Go уходит на IPv4. Нужно,
	// когда ответы IPv6 на физический интерфейс отбросит обратный фильтр
	// (firewalld, plan-e4.md, T3). Задаётся до SelectInterfaces и Watch.
	NoIPv6 bool
}

// SocketMark — значение Mark для сокетов tt. Свободно от известных
// пользователей меток: wg-quick (0xca6c), kube-proxy (0x4000, 0x8000),
// Tailscale (маска 0xff0000), Calico (маска 0xffff0000), Cilium (маска
// 0xf00). Маршрутизации по метке tt не заводит — только опознание.
const SocketMark uint32 = 0x1074

// SelectInterfaces выбирает физический интерфейс маршрута по умолчанию для
// IPv4 и IPv6 и ставит их индексы. exclude — свой адаптер (AdapterID
// datapath: на Windows — LUID, на Linux — индекс), 0 — исключать нечего.
// Возвращает выбор словами для вывода.
//
// Сети нет вовсе — не ошибка: индексы обнуляются, и сокеты ядра откажут
// с понятным текстом, а не уйдут в туннель. Ошибка — только отказ системы
// отдать таблицу маршрутов.
func (g *Guard) SelectInterfaces(exclude uint64) (string, error) {
	if !supported {
		return "не нужен на этой системе", nil
	}
	c, err := choose(exclude)
	if err != nil {
		return "", err
	}
	g.commit(c)
	return c.String(), nil
}

// SetInterfaces задаёт индексы физического интерфейса. 0 — для этого
// семейства интерфейса нет, и сокеты этого семейства не создаются.
func (g *Guard) SetInterfaces(index4, index6 uint32) {
	g.index4.Store(index4)
	g.index6.Store(index6)
}

// Interfaces возвращает текущие индексы для IPv4 и IPv6.
func (g *Guard) Interfaces() (index4, index6 uint32) {
	return g.index4.Load(), g.index6.Load()
}

// Choice — выбор физического интерфейса для обоих семейств. Индекс 0 —
// маршрута по умолчанию для семейства нет.
type Choice struct {
	Index4, Index6 uint32
	// Alias4, Alias6 — имена интерфейсов для вывода («Ethernet 3», «Wi-Fi»,
	// «enp7s0»).
	Alias4, Alias6 string
}

// Same — тот же выбор: индексы совпадают. Имена не сравниваются, их
// переименование сети не меняет.
func (c Choice) Same(o Choice) bool {
	return c.Index4 == o.Index4 && c.Index6 == o.Index6
}

// Empty — сети нет: маршрута по умолчанию нет ни для одного семейства.
func (c Choice) Empty() bool { return c.Index4 == 0 && c.Index6 == 0 }

// String — выбор словами: «IPv4 — Ethernet 3 (индекс 3), IPv6 — нет маршрута».
func (c Choice) String() string {
	return "IPv4 — " + describe(c.Index4, c.Alias4) + ", IPv6 — " + describe(c.Index6, c.Alias6)
}

func describe(index uint32, alias string) string {
	if index == 0 {
		return "нет маршрута"
	}
	return fmt.Sprintf("%s (индекс %d)", alias, index)
}

// Choice — последний выбор, который поставил SelectInterfaces или Watch.
func (g *Guard) Choice() Choice {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.choice
}

// allowed — выбор без того, что сокетам ядра давать нельзя (NoIPv6).
func (g *Guard) allowed(c Choice) Choice {
	if g.NoIPv6 {
		c.Index6, c.Alias6 = 0, ""
	}
	return c
}

// commit ставит выбор: индексы для новых сокетов и запись для сравнения.
// Возвращает прежний выбор.
func (g *Guard) commit(c Choice) (before Choice) {
	c = g.allowed(c)
	g.mu.Lock()
	defer g.mu.Unlock()
	before, g.choice = g.choice, c
	g.SetInterfaces(c.Index4, c.Index6)
	return before
}

// Control — хук под поле Control у net.Dialer и net.ListenConfig.
func (g *Guard) Control(network, address string, c syscall.RawConn) error {
	if !supported {
		return nil
	}
	return g.apply(network, address, c, g.setter())
}

// setter привязывает сокет fd к интерфейсу index для семейства. На
// Windows это setUnicastIf, на Linux — привязка и метка, в тестах — подмена.
type setter func(fd uintptr, ipv6 bool, index uint32) error

// apply — платформенно-независимая часть Control: решает, какие семейства
// привязывать, и проверяет, что индекс для них известен.
func (g *Guard) apply(network, address string, c syscall.RawConn, set setter) error {
	v4, v6, err := families(network, address)
	if err != nil {
		return err
	}
	if !v4 && !v6 {
		return nil
	}
	index4, index6 := g.Interfaces()

	// Двухстековый сокет привязывается к тому, что известно: на машине без
	// IPv6 это только IPv4. В шести точках ядра таких сокетов нет — там
	// только диалы, и Control получает адрес назначения.
	if v4 && v6 {
		if index4 == 0 && index6 == 0 {
			return noInterface("IPv4 и IPv6", network, address)
		}
		v4, v6 = index4 != 0, index6 != 0
		// На Linux привязка одна на сокет, а не на семейство. При разных
		// интерфейсах семейств сокет привязывается к интерфейсу IPv4: IPv6
		// через него получит отказ маршрута, но в туннель не уйдёт.
		if v4 && v6 && !bindPerFamily {
			v6 = false
		}
	}
	// Индекс не известен — сокет не создаётся. Молча пустить его по общей
	// таблице значит получить петлю через туннель, которую не видно.
	if v4 && index4 == 0 {
		return noInterface("IPv4", network, address)
	}
	if v6 && index6 == 0 {
		return noInterface("IPv6", network, address)
	}

	var setErr error
	err = c.Control(func(fd uintptr) {
		if v4 {
			if setErr = set(fd, false, index4); setErr != nil {
				setErr = fmt.Errorf("netguard: %s=%d для %s %s: %w", option4, index4, network, address, setErr)
				return
			}
		}
		if v6 {
			if setErr = set(fd, true, index6); setErr != nil {
				setErr = fmt.Errorf("netguard: %s=%d для %s %s: %w", option6, index6, network, address, setErr)
			}
		}
	})
	if err != nil {
		return err
	}
	if setErr != nil {
		return setErr
	}
	if g.Logf != nil {
		switch {
		case v4 && v6:
			g.Logf("[NETGUARD] %s %s → интерфейс %d (IPv4), %d (IPv6)", network, address, index4, index6)
		case v4:
			g.Logf("[NETGUARD] %s %s → интерфейс %d", network, address, index4)
		default:
			g.Logf("[NETGUARD] %s %s → интерфейс %d", network, address, index6)
		}
	}
	return nil
}

func noInterface(family, network, address string) error {
	return fmt.Errorf("netguard: нет физического интерфейса для %s (сеть не выбрана или в ней нет %s), сокет %s %s не создаётся",
		family, family, network, address)
}

// families решает, какие опции ставить сокету. Loopback пропускается без
// изменений: это страховка, хук и так не ставят в listen.go.
//
// Семейство задаёт суффикс сети: пакет net всегда передаёт в Control tcp4,
// udp6 и т. п. (net/sock_posix.go, ctrlNetwork), голых tcp/udp там не
// бывает. Особый случай — слушающий сокет на неуказанном адресе (":0",
// "0.0.0.0:0", "[::]:0") с сетью *6: так выглядит двухстековый сокет, через
// который можно слать и на IPv4, поэтому ставятся обе опции.
func families(network, address string) (v4, v6 bool, err error) {
	host, _, splitErr := net.SplitHostPort(address)
	if splitErr != nil {
		return false, false, fmt.Errorf("netguard: не разобрать адрес %q для %s: %w", address, network, splitErr)
	}
	addr, _ := netip.ParseAddr(host)
	if addr.IsValid() && addr.Unmap().IsLoopback() {
		return false, false, nil
	}
	unspecified := host == "" || addr.IsUnspecified()

	switch {
	case strings.HasSuffix(network, "4"):
		return true, false, nil
	case strings.HasSuffix(network, "6") && unspecified:
		return true, true, nil
	case strings.HasSuffix(network, "6"):
		return false, true, nil
	default:
		return false, false, fmt.Errorf("netguard: не понять семейство сокета %s %s", network, address)
	}
}
