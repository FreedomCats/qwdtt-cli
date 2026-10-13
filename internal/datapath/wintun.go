package datapath

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// WintunDLL — библиотека драйвера Wintun. Ищется только рядом с бинарём
// и в System32 (LoadLibraryEx в golang.zx2c4.com/wintun), поэтому в
// Requires она стоит как файл рядом с tt, а не как программа из PATH.
const WintunDLL = "wintun.dll"

// WintunOptions — настройки datapath wintun.
type WintunOptions struct {
	// Name — имя адаптера, как его покажет Get-NetAdapter. Пусто —
	// DefaultTunnelName.
	Name string

	// Logf — куда писать; nil означает молчать. Сюда же уходит вывод
	// wireguard-go и самого wintun.dll: datapath сам в stdout не пишет.
	Logf func(format string, args ...any)
}

// Wintun — datapath с собственным адаптером: wireguard-go в процессе поверх
// TUN от драйвера Wintun. От socks отличается только TUN (architecture.md
// §04), общая часть — startDevice.
//
// Адаптер живёт, пока открыт его дескриптор: Close убирает его штатно, а при
// падении процесса систему чистит сам драйвер (plan-e2.md, врезка N1, п. 3).
// Адреса и маршруты висят на адаптере и уходят вместе с ним.
type Wintun struct {
	opts WintunOptions

	// openAdapter создаёт и настраивает адаптер. Поле — ради тестов: настоящий
	// адаптер требует прав администратора и wintun.dll, а логика Start и Close
	// от этого не зависит.
	openAdapter func(name string, conf *WGConfig, logf func(string, ...any)) (*wintunAdapter, error)

	mu       sync.Mutex
	started  bool
	closed   bool
	dev      *device.Device
	adapter  *wintunAdapter
	endpoint netip.AddrPort

	// done закрывается в Close и снимает сторожа отмены ctx.
	done chan struct{}

	// closeOnce и closeErr — уборка один раз, ожидание всеми (см. Close).
	closeOnce sync.Once
	closeErr  error
}

// wintunAdapter — то, что платформа отдаёт datapath: TUN для wireguard-go
// и уборка того, что вместе с TUN не уходит.
type wintunAdapter struct {
	tun  tun.Device
	luid uint64
	// addRoutes и removeRoutes ставят и снимают fullTunnelRoutes через
	// адаптер; оба безвредны повторно. Сам адаптер при этом остаётся.
	addRoutes    func() error
	removeRoutes func() error
	// release зовётся после закрытия TUN, то есть когда адаптера уже нет:
	// снимает блокировку второго запуска и возвращает перехваченный вывод.
	release func()
}

// NewWintun готовит datapath. Ни драйвер, ни сеть не трогаются до Start:
// CLI строит datapath и ради одного Requires.
func NewWintun(opts WintunOptions) *Wintun {
	if strings.TrimSpace(opts.Name) == "" {
		opts.Name = DefaultTunnelName
	}
	return &Wintun{opts: opts, openAdapter: openWintunAdapter, done: make(chan struct{})}
}

// Requires: права администратора (адаптер создаёт драйвер), wintun.dll рядом
// с бинарём и netguard — туннель забирает весь трафик системы.
func (w *Wintun) Requires() Requirements {
	return Requirements{
		Admin:    true,
		Files:    []string{WintunDLL},
		Netguard: true,
	}
}

// Reserve занимает адаптер на машине до подключения (Reserver): второй tt
// с datapath wintun узнаёт о первом сразу, а не после похода в VK за кредами,
// и не уходит в переподключения (найдено живым прогоном W2). Держать
// резерв — весь сеанс; Start каждой попытки берёт ту же блокировку ещё раз,
// внутри процесса это не конфликт.
func (w *Wintun) Reserve() (release func(), err error) {
	release, err = acquireWintunLock()
	if err != nil {
		return nil, fmt.Errorf("wintun: %w", err)
	}
	return release, nil
}

// Endpoint — адрес из конфига, на который wireguard-go шлёт UDP; на нём
// слушает ядро.
func (w *Wintun) Endpoint() netip.AddrPort {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.endpoint
}

// LUID — LUID адаптера; 0 до Start и после Close. Нужен тем, кто исключает
// свой адаптер из выбора физического интерфейса (netguard, смена сети).
func (w *Wintun) LUID() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.adapter == nil {
		return 0
	}
	return w.adapter.luid
}

// Start создаёт адаптер, задаёт ему адреса и MTU, поднимает над ним
// wireguard-go и ставит маршруты fullTunnelRoutes — с этого момента через
// туннель идёт весь трафик системы. Возвращается, когда всё это сделано.
func (w *Wintun) Start(ctx context.Context, conf *WGConfig) error {
	if conf == nil {
		return errors.New("wintun: пустой конфиг WireGuard")
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errors.New("wintun: datapath уже остановлен")
	}
	if w.started {
		w.mu.Unlock()
		return errors.New("wintun: datapath уже запущен")
	}
	w.started = true
	w.mu.Unlock()

	adapter, err := w.openAdapter(w.opts.Name, conf, w.logf)
	if err != nil {
		w.startFailed()
		return fmt.Errorf("wintun: %w", err)
	}

	dev, err := startDevice(adapter.tun, conf, "[WG-WINTUN]", w.logf)
	if err != nil {
		// startDevice уже закрыл TUN, а с ним и адаптер.
		adapter.release()
		w.startFailed()
		return fmt.Errorf("wintun: %w", err)
	}

	endpoint, ok := conf.EndpointAddrPort()
	if !ok {
		w.logf("[WINTUN] endpoint %q — не адрес с портом", conf.Endpoint)
	}

	for _, warning := range allowedIPsWarnings(conf) {
		w.logf("[WINTUN] Внимание: %s", warning)
	}
	if err := adapter.addRoutes(); err != nil {
		dev.Close()
		adapter.release()
		w.startFailed()
		return fmt.Errorf("wintun: маршруты: %w", err)
	}

	w.mu.Lock()
	if w.closed {
		// Close пришёл, пока адаптер создавался, и убирать ему было нечего:
		// убираем сами, иначе адаптер пережил бы datapath.
		w.mu.Unlock()
		dev.Close()
		adapter.release()
		return errors.New("wintun: datapath остановлен во время запуска")
	}
	w.dev, w.adapter, w.endpoint = dev, adapter, endpoint
	w.mu.Unlock()

	w.logf("[WINTUN] адаптер %q поднят (addr=%v mtu=%d endpoint=%s routes=%v)",
		w.opts.Name, conf.Addresses, conf.MTU, conf.Endpoint, fullTunnelRoutes)

	go func() {
		select {
		case <-ctx.Done():
			_ = w.Close()
		case <-w.done:
		}
	}()
	return nil
}

// AddRoutes возвращает маршруты fullTunnelRoutes через адаптер после
// RemoveRoutes. Start ставит их сам; отдельный вызов нужен супервизору,
// который снимает маршруты на время переподключения (plan-e2.md, S1).
func (w *Wintun) AddRoutes() error {
	adapter, err := w.runningAdapter()
	if err != nil {
		return err
	}
	if err := adapter.addRoutes(); err != nil {
		return fmt.Errorf("wintun: маршруты: %w", err)
	}
	return nil
}

// RemoveRoutes снимает маршруты, оставляя адаптер: трафик системы идёт мимо
// туннеля, пока их не вернёт AddRoutes.
func (w *Wintun) RemoveRoutes() error {
	adapter, err := w.runningAdapter()
	if err != nil {
		return err
	}
	if err := adapter.removeRoutes(); err != nil {
		return fmt.Errorf("wintun: снятие маршрутов: %w", err)
	}
	return nil
}

func (w *Wintun) runningAdapter() (*wintunAdapter, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.adapter == nil {
		return nil, errors.New("wintun: адаптер не поднят")
	}
	return w.adapter, nil
}

// Close останавливает wireguard-go и убирает адаптер. Безопасен до Start
// и при повторном вызове; одновременный вызов ждёт конца первого (контракт
// Datapath.Close).
func (w *Wintun) Close() error {
	w.closeOnce.Do(func() { w.closeErr = w.close() })
	return w.closeErr
}

func (w *Wintun) close() error {
	w.mu.Lock()
	w.closed = true
	dev, adapter := w.dev, w.adapter
	w.dev, w.adapter = nil, nil
	close(w.done)
	w.mu.Unlock()

	if dev == nil {
		return nil
	}
	// Close устройства закрывает TUN, а закрытие TUN убирает адаптер вместе
	// с его адресами. Ошибки wireguard-go здесь не возвращает.
	dev.Close()
	adapter.release()
	w.logf("[WINTUN] адаптер %q снят", w.opts.Name)
	return nil
}

// startFailed возвращает datapath в исходное состояние: неудачный Start не
// должен закрывать дорогу повторной попытке.
func (w *Wintun) startFailed() {
	w.mu.Lock()
	w.started = false
	w.mu.Unlock()
}

func (w *Wintun) logf(format string, args ...any) {
	if w.opts.Logf == nil {
		return
	}
	w.opts.Logf(format, args...)
}
