package datapath

import (
	"errors"
	"fmt"
	"net"
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

// tunLockName — абстрактный unix-сокет одного адаптера на машину: файла не
// оставляет и умирает вместе с процессом, так что упавший запуск следующему
// не мешает. Абстрактные имена у каждого сетевого пространства свои, как и
// имена интерфейсов: tt в контейнере с хостовым не конфликтует.
const tunLockName = "@qwdtt-cli-tun"

// openTunAdapter создаёт адаптер и задаёт ему адреса и MTU из конфига;
// перенастройку и маршруты делает вызывающий (configure, addRoutes,
// removeRoutes). На отказе за собой убирает всё.
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
	release := func() {
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
	index, err := linuxsys.LinkIndex(name)
	if err != nil {
		tunDev.Close()
		release()
		return nil, fmt.Errorf("адаптер %q: %w", name, err)
	}

	configure := func(conf *WGConfig) error {
		// DNS адаптера — D2 (plan-e4.md).
		if err := linuxsys.ConfigureAdapter(index, conf.Addresses, conf.MTU, logf); err != nil {
			return fmt.Errorf("адаптер %q: %w", name, err)
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
		// Маршруты fullTunnelRoutes — T3 (plan-e4.md): до неё адаптер
		// поднимается без них и трафик системы не забирает.
		addRoutes:    func() error { return nil },
		removeRoutes: func() error { return nil },
		// flush не нужен: адреса и маршруты уходят вместе с интерфейсом,
		// когда закрывается TUN.
		release: release,
	}, nil
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
