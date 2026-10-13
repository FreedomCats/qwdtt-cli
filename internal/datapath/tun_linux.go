package datapath

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"syscall"

	"golang.zx2c4.com/wireguard/tun"

	"github.com/FreedomCats/qwdtt-cli/internal/linuxsys"
)

// tunUnsupported — на Linux tun работает (см. tun_other.go).
var tunUnsupported error

// tunFiles — на Linux рядом с tt ничего не нужно: TUN даёт ядро
// (/dev/net/tun).
var tunFiles []string

// minMTU6 — наименьший MTU, при котором ядро держит IPv6 на интерфейсе
// (RFC 8200); с меньшим маршрут через tt для IPv6 не поставить.
const minMTU6 = 1280

// tunLockName — абстрактный unix-сокет одного адаптера на машину: файла не
// оставляет и умирает вместе с процессом, так что упавший запуск следующему
// не мешает. Абстрактные имена у каждого сетевого пространства свои, как и
// имена интерфейсов: tt в контейнере с хостовым не конфликтует.
const tunLockName = "@qwdtt-cli-tun"

// openTunAdapter создаёт адаптер и задаёт ему адреса, MTU и DNS из конфига;
// перенастройку и маршруты делает вызывающий (configure, addRoutes,
// removeRoutes). На отказе за собой убирает всё.
//
// DNS на Linux — не свойство адаптера, а настройка системы (plan-e4.md, D2):
// способ выбирает linuxsys.SystemDNS по тому, кто ведёт /etc/resolv.conf.
// Снимается он в release, когда TUN уже закрыт, — на любом пути закрытия.
//
// TUN не постоянный: интерфейс живёт, пока открыт его дескриптор, и после
// падения процесса исчезает вместе с адресами и маршрутами (plan-e4.md,
// врезка N1, п. 7). Поэтому чужой интерфейс с тем же именем — не остаток
// прошлого tt, а чей-то ещё (например, wg-quick up tt), и он не удаляется,
// а называется в ошибке.
func openTunAdapter(name string, conf *WGConfig, logf func(string, ...any)) (*tunAdapter, error) {
	unlock, err := acquireTunLock()
	if err != nil {
		return nil, err
	}
	restoreLog := captureStdLog(logf)
	var (
		index int
		dns   *linuxsys.SystemDNS
	)
	release := func() {
		if dns != nil && dns.Applied() {
			if err := dns.Revert(index); err != nil {
				logf("[TUN] %v", err)
			} else {
				logf("[TUN] DNS системы возвращён (%s)", dns.Method())
			}
		}
		restoreLog()
		unlock()
	}

	kind, exists, err := linuxsys.LinkKind(name)
	if err != nil {
		release()
		return nil, fmt.Errorf("адаптер %q: %w", name, err)
	}
	if exists {
		release()
		return nil, fmt.Errorf("адаптер %q: в системе уже есть интерфейс с этим именем (тип %s), и создал его не tt; "+
			"если он не нужен, удалите его: sudo ip link del %s", name, kind, name)
	}

	tunDev, err := tun.CreateTUN(name, conf.MTU)
	if err != nil {
		release()
		return nil, fmt.Errorf("адаптер %q: %w", name, err)
	}
	if index, err = linuxsys.LinkIndex(name); err == nil {
		dns, err = linuxsys.DetectSystemDNS(name)
	}
	if err != nil {
		tunDev.Close()
		release()
		return nil, fmt.Errorf("адаптер %q: %w", name, err)
	}

	// mtu — MTU последней настройки: от него зависит, ставятся ли маршруты
	// IPv6. configure и addRoutes зовёт одна горутина Start, по очереди.
	var mtu int
	configure := func(conf *WGConfig) error {
		if err := linuxsys.ConfigureAdapter(index, conf.Addresses, conf.MTU, logf); err != nil {
			return fmt.Errorf("адаптер %q: %w", name, err)
		}
		mtu = conf.MTU
		// DNS держится и на время переподключения, как на Windows: маршруты
		// сняты, и запросы к нему идут мимо туннеля (решение 3 Э2).
		servers := conf.DNS
		if !ipv6Routed(mtu) {
			// Без маршрутов IPv6 резолвер IPv6 туннеля недостижим.
			servers = ipv4Addrs(servers)
		}
		if err := dns.Set(index, servers); err != nil {
			return fmt.Errorf("адаптер %q: %w", name, err)
		}
		if len(servers) == 0 {
			logf("[TUN] сервер не выдал DNS — DNS системы tt не трогает")
		} else {
			logf("[TUN] DNS системы (%s): %v", dns.Method(), servers)
		}
		return nil
	}
	if err := configure(conf); err != nil {
		tunDev.Close()
		release()
		return nil, err
	}
	return &tunAdapter{
		tun:       tunDev,
		id:        uint64(index),
		configure: configure,
		addRoutes: func() error {
			routes := fullTunnelRoutes
			if !ipv6Routed(mtu) {
				routes = ipv4Only(routes)
				logf("[TUN] при MTU %d ядро не держит IPv6 на %s — маршруты IPv6 не ставятся", mtu, name)
			}
			return linuxsys.SetTunnelRoutes(index, routes)
		},
		removeRoutes: func() error {
			return linuxsys.ClearTunnelRoutes(index, fullTunnelRoutes)
		},
		ipv6Routed: func() bool { return ipv6Routed(mtu) },
		// flush не нужен: адреса и маршруты уходят вместе с интерфейсом,
		// когда закрывается TUN.
		release: release,
	}, nil
}

// ipv6Routed — ставятся ли при таком MTU маршруты IPv6 в туннель; 0 и
// меньше — MTU не задан, ядро берёт своё.
func ipv6Routed(mtu int) bool { return mtu <= 0 || mtu >= minMTU6 }

// ipv4Addrs — адреса только IPv4.
func ipv4Addrs(addrs []netip.Addr) []netip.Addr {
	var out []netip.Addr
	for _, a := range addrs {
		if a.Unmap().Is4() {
			out = append(out, a)
		}
	}
	return out
}

// ipv4Only — маршруты только IPv4.
func ipv4Only(routes []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, r := range routes {
		if r.Addr().Is4() {
			out = append(out, r)
		}
	}
	return out
}

// acquireTunLock занимает абстрактный сокет одного адаптера на машину
// и возвращает функцию, которая его отпустит. Занят другим процессом —
// значит, на машине уже работает tt с datapath tun.
//
// Внутри процесса захваты считаются, как на Windows: CLI держит сокет весь
// tt connect (Tun.Reserve), а первый Start берёт его ещё раз.
func acquireTunLock() (release func(), err error) {
	tunLock.mu.Lock()
	defer tunLock.mu.Unlock()

	if tunLock.refs == 0 {
		l, err := net.Listen("unix", tunLockName)
		if errors.Is(err, syscall.EADDRINUSE) {
			return nil, errTunBusy
		}
		if err != nil {
			return nil, fmt.Errorf("блокировка адаптера: %w", err)
		}
		tunLock.listener = l
	}
	tunLock.refs++

	var once sync.Once
	return func() {
		once.Do(func() {
			tunLock.mu.Lock()
			defer tunLock.mu.Unlock()
			tunLock.refs--
			if tunLock.refs == 0 {
				tunLock.listener.Close()
				tunLock.listener = nil
			}
		})
	}, nil
}

// tunLock — сокет блокировки и число захватов в этом процессе.
var tunLock struct {
	mu       sync.Mutex
	listener net.Listener
	refs     int
}
