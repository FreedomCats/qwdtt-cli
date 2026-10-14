package datapath

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"

	"github.com/FreedomCats/qwdtt-cli/internal/app"
	"github.com/FreedomCats/qwdtt-cli/internal/winsys"
)

// wintunGUID — постоянный GUID адаптера. С ним Windows узнаёт адаптер при
// каждом запуске и не заводит «Сеть 2», «Сеть 3» (документация
// WintunCreateAdapter: GUID определяет запись NLA).
var wintunGUID = windows.GUID{
	Data1: 0xe46af63b,
	Data2: 0x12e0,
	Data3: 0x4377,
	Data4: [8]byte{0x97, 0xdb, 0x29, 0xf1, 0x24, 0xa7, 0x0f, 0x40},
}

// tunUnsupported — на Windows tun работает (см. tun_other.go).
var tunUnsupported error

// tunFiles — драйвер Wintun рядом с бинарём (см. WintunDLL).
var tunFiles = []string{WintunDLL}

// wintunTunnelType — тип адаптера у драйвера, виден в диспетчере устройств
// («qwdtt-cli Tunnel»). Умолчание wireguard-go — «WireGuard», его легко спутать
// с адаптером штатного клиента.
const wintunTunnelType = app.Name

// tunLockName — именованный мьютекс одного адаптера на машину. Два qwdtt-cli
// с datapath tun делили бы один GUID, а что драйвер делает со вторым
// адаптером с тем же GUID, не документировано. Мьютекс умирает вместе с
// процессом, так что упавший запуск следующему не мешает.
const tunLockName = `Global\` + app.Name + "-wintun"

// openTunAdapter создаёт адаптер и задаёт ему адреса, MTU и DNS из конфига;
// перенастройку и маршруты делает вызывающий (configure, addRoutes,
// removeRoutes).
// На отказе за собой убирает всё.
//
// Адаптер от упавшего прошлого запуска убирать не нужно: он исчезает, как
// только система закрыла дескрипторы умершего процесса, а его остатки драйвер
// вычищает сам при следующем создании (plan-e2.md, врезка N1, п. 3).
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

	tun.WintunTunnelType = wintunTunnelType
	guid := wintunGUID
	tunDev, err := tun.CreateTUNWithRequestedGUID(name, &guid, conf.MTU)
	if err != nil {
		release()
		return nil, fmt.Errorf("адаптер %q: %w", name, err)
	}
	native, ok := tunDev.(*tun.NativeTun)
	if !ok {
		tunDev.Close()
		release()
		return nil, fmt.Errorf("адаптер %q: TUN неожиданного типа %T", name, tunDev)
	}
	luid := native.LUID()

	configure := func(conf *WGConfig) error {
		if err := winsys.ConfigureAdapter(luid, conf.Addresses, conf.MTU, logf); err != nil {
			return fmt.Errorf("адаптер %q: %w", name, err)
		}
		// DNS — свойство адаптера и держится, пока он жив. На время
		// переподключения (S1) снимаются маршруты /1 и запрет утечки (DNSGuard),
		// а не DNS: весь трафик и так идёт мимо туннеля (решение 3).
		if err := winsys.SetAdapterDNS(luid, conf.DNS); err != nil {
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
		id:        luid,
		configure: configure,
		addRoutes: func() error {
			return winsys.SetTunnelRoutes(luid, fullTunnelRoutes, logf)
		},
		removeRoutes: func() error {
			return winsys.ClearTunnelRoutes(luid, fullTunnelRoutes)
		},
		flush: func() error {
			return winsys.FlushAdapter(luid)
		},
		release: release,
	}, nil
}

// acquireTunLock занимает мьютекс одного адаптера на машину и возвращает
// функцию, которая его отпустит. Занят другим процессом — значит, на машине
// уже работает qwdtt-cli с datapath tun.
//
// Внутри процесса захваты считаются: CLI держит мьютекс весь qwdtt-cli connect
// (Tun.Reserve), а первый Start берёт его ещё раз. Повторный
// CreateMutex с тем же именем вернул бы ERROR_ALREADY_EXISTS и своему же
// процессу, поэтому дескриптор один и закрывается с последним захватом.
func acquireTunLock() (release func(), err error) {
	tunLock.mu.Lock()
	defer tunLock.mu.Unlock()

	if tunLock.refs == 0 {
		name, err := windows.UTF16PtrFromString(tunLockName)
		if err != nil {
			return nil, err
		}
		h, err := windows.CreateMutex(nil, false, name)
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			windows.CloseHandle(h)
			return nil, errTunBusy
		}
		if err != nil {
			return nil, fmt.Errorf("мьютекс адаптера: %w", err)
		}
		tunLock.handle = h
	}
	tunLock.refs++

	var once sync.Once
	return func() {
		once.Do(func() {
			tunLock.mu.Lock()
			defer tunLock.mu.Unlock()
			tunLock.refs--
			if tunLock.refs == 0 {
				windows.CloseHandle(tunLock.handle)
				tunLock.handle = 0
			}
		})
	}, nil
}

// tunLock — дескриптор мьютекса и число захватов в этом процессе.
var tunLock struct {
	mu     sync.Mutex
	handle windows.Handle
	refs   int
}
