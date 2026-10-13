package datapath

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"

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

// wintunTunnelType — тип адаптера у драйвера, виден в диспетчере устройств
// («tt Tunnel»). Умолчание wireguard-go — «WireGuard», его легко спутать
// с адаптером штатного клиента.
const wintunTunnelType = "tt"

// wintunLockName — именованный мьютекс одного адаптера на машину. Два tt
// с datapath wintun делили бы один GUID, а что драйвер делает со вторым
// адаптером с тем же GUID, не документировано. Мьютекс умирает вместе с
// процессом, так что упавший запуск следующему не мешает.
const wintunLockName = `Global\qwdtt-cli-wintun`

// openWintunAdapter создаёт адаптер и задаёт ему адреса, MTU и DNS из конфига;
// маршруты ставит и снимает вызывающий (addRoutes, removeRoutes).
// На отказе за собой убирает всё.
//
// Адаптер от упавшего прошлого запуска убирать не нужно: он исчезает, как
// только система закрыла дескрипторы умершего процесса, а его остатки драйвер
// вычищает сам при следующем создании (plan-e2.md, врезка N1, п. 3).
func openWintunAdapter(name string, conf *WGConfig, logf func(string, ...any)) (*wintunAdapter, error) {
	unlock, err := acquireWintunLock()
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

	if err := winsys.ConfigureAdapter(luid, conf.Addresses, conf.MTU, logf); err != nil {
		tunDev.Close()
		release()
		return nil, fmt.Errorf("адаптер %q: %w", name, err)
	}
	// DNS — свойство адаптера и держится, пока он жив. На время
	// переподключения (S1) снимаются маршруты /1 и запрет утечки (DNSGuard),
	// а не DNS: весь трафик и так идёт мимо туннеля (решение 3).
	if err := winsys.SetAdapterDNS(luid, conf.DNS); err != nil {
		tunDev.Close()
		release()
		return nil, fmt.Errorf("адаптер %q: %w", name, err)
	}
	return &wintunAdapter{
		tun:  tunDev,
		luid: luid,
		addRoutes: func() error {
			return winsys.SetTunnelRoutes(luid, fullTunnelRoutes, logf)
		},
		removeRoutes: func() error {
			return winsys.ClearTunnelRoutes(luid, fullTunnelRoutes)
		},
		release: release,
	}, nil
}

// acquireWintunLock занимает мьютекс одного адаптера на машину и возвращает
// функцию, которая его отпустит. Занят другим процессом — значит, на машине
// уже работает tt с datapath wintun.
//
// Внутри процесса захваты считаются: CLI держит мьютекс весь tt connect
// (Wintun.Reserve), а Start каждой попытки берёт его ещё раз. Повторный
// CreateMutex с тем же именем вернул бы ERROR_ALREADY_EXISTS и своему же
// процессу, поэтому дескриптор один и закрывается с последним захватом.
func acquireWintunLock() (release func(), err error) {
	wintunLock.mu.Lock()
	defer wintunLock.mu.Unlock()

	if wintunLock.refs == 0 {
		name, err := windows.UTF16PtrFromString(wintunLockName)
		if err != nil {
			return nil, err
		}
		h, err := windows.CreateMutex(nil, false, name)
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			windows.CloseHandle(h)
			return nil, errors.New("адаптер уже занят: на этой машине работает другой tt с datapath wintun; " +
				"остановите его или подключитесь с -datapath socks")
		}
		if err != nil {
			return nil, fmt.Errorf("мьютекс адаптера: %w", err)
		}
		wintunLock.handle = h
	}
	wintunLock.refs++

	var once sync.Once
	return func() {
		once.Do(func() {
			wintunLock.mu.Lock()
			defer wintunLock.mu.Unlock()
			wintunLock.refs--
			if wintunLock.refs == 0 {
				windows.CloseHandle(wintunLock.handle)
				wintunLock.handle = 0
			}
		})
	}, nil
}

// wintunLock — дескриптор мьютекса и число захватов в этом процессе.
var wintunLock struct {
	mu     sync.Mutex
	handle windows.Handle
	refs   int
}

// captureStdLog направляет стандартный log в logf и возвращает функцию,
// которая вернёт прежний вывод.
//
// Нужно ради wintun.dll: golang.zx2c4.com/wintun передаёт сообщения драйвера
// в log.Default() (wintun.go, logMessage), а тот по умолчанию пишет прямо
// в stderr — поверх статусной строки CLI. Перехват глобальный и живёт, пока
// жив адаптер. Ядро в log не пишет (у него свой logf), но изредка пишут
// библиотеки под ним — fhttp и x/net/http2 об ошибках протокола; их строки
// на это время тоже идут в logf, поэтому тег у всех общий — [LOG], а строки
// драйвера узнаются по тексту.
func captureStdLog(logf func(string, ...any)) (restore func()) {
	out, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(stdLogWriter{logf: logf})
	log.SetFlags(0)
	log.SetPrefix("")
	return func() {
		log.SetOutput(out)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	}
}

type stdLogWriter struct {
	logf func(string, ...any)
}

func (w stdLogWriter) Write(p []byte) (int, error) {
	w.logf("[LOG] %s", strings.TrimRight(string(p), "\r\n"))
	return len(p), nil
}
