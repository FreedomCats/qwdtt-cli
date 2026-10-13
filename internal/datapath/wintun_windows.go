package datapath

import (
	"errors"
	"fmt"
	"log"
	"strings"

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

// openWintunAdapter создаёт адаптер и задаёт ему адреса и MTU из конфига.
// На отказе за собой убирает всё.
//
// Адаптер от упавшего прошлого запуска убирать не нужно: он исчезает, как
// только система закрыла дескрипторы умершего процесса, а его остатки драйвер
// вычищает сам при следующем создании (plan-e2.md, врезка N1, п. 3).
func openWintunAdapter(name string, conf *WGConfig, logf func(string, ...any)) (*wintunAdapter, error) {
	lock, err := acquireWintunLock()
	if err != nil {
		return nil, err
	}
	restoreLog := captureStdLog(logf)
	release := func() {
		restoreLog()
		windows.CloseHandle(lock)
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
	return &wintunAdapter{tun: tunDev, luid: luid, release: release}, nil
}

// acquireWintunLock берёт мьютекс одного адаптера. Занят — значит, на машине
// уже работает tt с datapath wintun.
func acquireWintunLock() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(wintunLockName)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return 0, errors.New("адаптер уже занят: на этой машине работает другой tt с datapath wintun; " +
			"остановите его или подключитесь с -datapath socks")
	}
	if err != nil {
		return 0, fmt.Errorf("мьютекс адаптера: %w", err)
	}
	return h, nil
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
