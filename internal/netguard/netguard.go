// Package netguard уводит собственные сокеты ядра мимо туннеля
// (architecture.md §05, дорожка 2).
//
// Когда datapath забирает весь трафик системы (wintun, external), сокеты
// ядра по общей таблице маршрутов попали бы в тот же туннель, и туннель
// зависел бы от самого себя. Guard.Control ставится в поле Control у
// net.Dialer и привязывает сокет к физическому интерфейсу: на Windows —
// опциями IP_UNICAST_IF / IPV6_UNICAST_IF. На других системах Control пока
// ничего не делает (Linux и macOS — этап Э4).
package netguard

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
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

// Control — хук под поле Control у net.Dialer и net.ListenConfig.
func (g *Guard) Control(network, address string, c syscall.RawConn) error {
	if !supported {
		return nil
	}
	return g.apply(network, address, c, setUnicastIf)
}

// setter привязывает сокет fd к интерфейсу index. На Windows это
// setUnicastIf, в тестах — подмена.
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
				setErr = fmt.Errorf("netguard: IP_UNICAST_IF=%d для %s %s: %w", index4, network, address, setErr)
				return
			}
		}
		if v6 {
			if setErr = set(fd, true, index6); setErr != nil {
				setErr = fmt.Errorf("netguard: IPV6_UNICAST_IF=%d для %s %s: %w", index6, network, address, setErr)
			}
		}
	})
	if err != nil {
		return err
	}
	return setErr
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
