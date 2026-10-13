package datapath

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// WintunDLL — библиотека драйвера Wintun. Ищется только рядом с бинарём
// и в System32 (LoadLibraryEx в golang.zx2c4.com/wintun), поэтому в
// Requires она стоит как файл рядом с tt, а не как программа из PATH.
const WintunDLL = "wintun.dll"

// TunOptions — настройки datapath tun.
type TunOptions struct {
	// Name — имя адаптера, как его покажет Get-NetAdapter. Пусто —
	// DefaultTunnelName.
	Name string

	// Logf — куда писать; nil означает молчать. Сюда же уходит вывод
	// wireguard-go и самого wintun.dll: datapath сам в stdout не пишет.
	Logf func(format string, args ...any)

	// DNSGuard — защита от утечки DNS: включается, когда маршруты стоят,
	// выключается в Suspend и Close. nil — без неё (CLI с -allow-leak, тесты).
	DNSGuard DNSGuard
}

// Tun — datapath с собственным адаптером: wireguard-go в процессе поверх
// TUN системы (на Windows — от драйвера Wintun). От socks отличается только TUN (architecture.md
// §04), общая часть — startDevice.
//
// Tun — Persistent: адаптер живёт весь tt connect. Первый Start создаёт
// его, следующие перенастраивают устройство конфигом новой попытки, Suspend
// между попытками снимает маршруты и запрет утечки DNS (plan-e2.md, решение 3).
//
// Адаптер живёт, пока открыт его дескриптор: Close убирает его штатно, а при
// падении процесса систему чистит сам драйвер (plan-e2.md, врезка N1, п. 3).
// Адреса и маршруты висят на адаптере и уходят вместе с ним.
type Tun struct {
	opts TunOptions

	// openAdapter создаёт и настраивает адаптер. Поле — ради тестов: настоящий
	// адаптер требует прав администратора и wintun.dll, а логика Start и Close
	// от этого не зависит.
	openAdapter func(name string, conf *WGConfig, logf func(string, ...any)) (*tunAdapter, error)

	mu       sync.Mutex
	starting bool
	closed   bool
	dev      *device.Device
	adapter  *tunAdapter
	// conf — конфиг, которым настроен адаптер: по нему перенастройка решает,
	// трогать ли адреса, MTU и DNS адаптера.
	conf     *WGConfig
	endpoint netip.AddrPort

	// pipe — канал к ядру. Живёт, как и адаптер, весь сеанс: устройство
	// wireguard-go держит его через попытки, а сторона ядра у каждой попытки
	// своя (Piped).
	pipe *Pipe

	// closeOnce и closeErr — уборка один раз, ожидание всеми (см. Close).
	closeOnce sync.Once
	closeErr  error
}

// tunAdapter — то, что платформа отдаёт datapath: TUN для wireguard-go
// и уборка того, что вместе с TUN не уходит.
type tunAdapter struct {
	tun tun.Device
	// id — идентификатор адаптера в системе: на Windows — LUID, на Linux —
	// индекс интерфейса.
	id uint64
	// configure заново задаёт адаптеру адреса, MTU и DNS из конфига —
	// когда сервер на новой попытке выдал другие.
	configure func(conf *WGConfig) error
	// addRoutes и removeRoutes ставят и снимают fullTunnelRoutes через
	// адаптер; оба безвредны повторно. Сам адаптер при этом остаётся.
	addRoutes    func() error
	removeRoutes func() error
	// ipv6Routed — везёт ли туннель IPv6 после addRoutes: на Linux при MTU
	// меньше 1280 маршрутов IPv6 нет. nil — везёт всегда (Windows).
	ipv6Routed func() bool
	// flush снимает с адаптера маршруты, адреса и DNS перед закрытием;
	// nil — нечего снимать (подделки в тестах).
	flush func() error
	// release зовётся после закрытия TUN, то есть когда адаптера уже нет:
	// снимает блокировку второго запуска и возвращает перехваченный вывод.
	release func()
}

// NewTun готовит datapath. Ни драйвер, ни сеть не трогаются до Start:
// CLI строит datapath и ради одного Requires.
func NewTun(opts TunOptions) *Tun {
	if strings.TrimSpace(opts.Name) == "" {
		opts.Name = DefaultTunnelName
	}
	return &Tun{opts: opts, openAdapter: openTunAdapter, pipe: NewPipe()}
}

// CoreConn — Piped: сторона ядра канала в памяти на эту попытку.
func (w *Tun) CoreConn() net.PacketConn { return w.pipe.CoreConn() }

// errTunBusy — отказ второму tt с datapath tun на той же машине.
var errTunBusy = errors.New("адаптер уже занят: на этой машине работает другой tt с datapath tun; " +
	"остановите его или подключитесь с -datapath socks")

// Requires: права администратора (root на Linux), файлы платформы (на
// Windows — wintun.dll рядом с бинарём), netguard — туннель забирает весь
// трафик системы — и защита DNS. Где своего адаптера нет — Unsupported.
func (w *Tun) Requires() Requirements {
	return Requirements{
		Unsupported: tunUnsupported,
		Admin:       true,
		Files:       tunFiles,
		Netguard:    true,
		DNSGuard:    true,
	}
}

// Reserve занимает адаптер на машине до подключения (Reserver): второй tt
// с datapath tun узнаёт о первом сразу, а не после похода в VK за кредами,
// и не уходит в переподключения (найдено живым прогоном W2). Держать
// резерв — весь сеанс; первый Start берёт ту же блокировку ещё раз, внутри
// процесса это не конфликт.
func (w *Tun) Reserve() (release func(), err error) {
	release, err = acquireTunLock()
	if err != nil {
		return nil, fmt.Errorf("tun: %w", err)
	}
	return release, nil
}

// Endpoint — адрес из конфига; номинальный: пакеты ядру идут каналом в
// памяти.
func (w *Tun) Endpoint() netip.AddrPort {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.endpoint
}

// AdapterID — идентификатор адаптера в системе (на Windows — LUID, на
// Linux — индекс интерфейса); 0 до Start и после Close. Нужен тем, кто
// исключает свой адаптер из выбора физического интерфейса (netguard, смена
// сети).
func (w *Tun) AdapterID() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.adapter == nil {
		return 0
	}
	return w.adapter.id
}

// Start поднимает туннель по конфигу попытки и возвращается, когда через
// него идёт весь трафик системы: стоят маршруты fullTunnelRoutes и включена
// защита DNS.
//
// Первый вызов создаёт адаптер (адреса, MTU, DNS) и wireguard-go над ним.
// Следующие — перенастройка (Persistent): устройство получает ключ, пира и
// endpoint новой попытки, адаптер — новые адреса и DNS, если сервер их
// сменил. Отказ перенастройки адаптер не снимает: это отказ попытки, а
// следующая попробует снова.
//
// ctx Start не хранит: адаптер переживает попытку и снимается только Close.
func (w *Tun) Start(_ context.Context, conf *WGConfig) error {
	if conf == nil {
		return errors.New("tun: пустой конфиг WireGuard")
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errors.New("tun: datapath уже остановлен")
	}
	if w.starting {
		// Попытки идут по одной, и супервизор не зовёт Start дважды разом.
		w.mu.Unlock()
		return errors.New("tun: Start уже идёт")
	}
	w.starting = true
	dev, adapter, prev := w.dev, w.adapter, w.conf
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		w.starting = false
		w.mu.Unlock()
	}()

	if dev == nil {
		return w.open(conf)
	}
	return w.reconfigure(dev, adapter, prev, conf)
}

// open — первый Start: адаптер, устройство, маршруты, защита DNS. На отказе
// адаптер снимается: следующей попытке ничего не мешает.
func (w *Tun) open(conf *WGConfig) error {
	adapter, err := w.openAdapter(w.opts.Name, conf, w.logf)
	if err != nil {
		return fmt.Errorf("tun: %w", err)
	}

	dev, err := startDevice(adapter.tun, w.pipe, conf, "[WG-TUN]", w.logf)
	if err != nil {
		// startDevice уже закрыл TUN, а с ним и адаптер.
		adapter.release()
		return fmt.Errorf("tun: %w", err)
	}

	if err := w.route(adapter, conf); err != nil {
		dev.Close()
		adapter.release()
		return err
	}

	endpoint := w.endpointOf(conf)
	w.mu.Lock()
	if w.closed {
		// Close пришёл, пока адаптер создавался, и убирать ему было нечего:
		// убираем сами, иначе адаптер пережил бы datapath.
		w.mu.Unlock()
		dev.Close()
		adapter.release()
		w.disableDNSGuard()
		return errors.New("tun: datapath остановлен во время запуска")
	}
	w.dev, w.adapter, w.conf = dev, adapter, conf
	w.endpoint = endpoint
	w.mu.Unlock()

	w.logf("[TUN] адаптер %q поднят (addr=%v mtu=%d endpoint=%s routes=%v dns=%v)",
		w.opts.Name, conf.Addresses, conf.MTU, conf.Endpoint, fullTunnelRoutes, conf.DNS)
	return nil
}

// reconfigure — повторный Start: тот же адаптер и то же устройство, конфиг
// новой попытки.
func (w *Tun) reconfigure(dev *device.Device, adapter *tunAdapter, prev, conf *WGConfig) error {
	if adapterSettingsChanged(prev, conf) {
		if err := adapter.configure(conf); err != nil {
			return fmt.Errorf("tun: %w", err)
		}
		w.mu.Lock()
		w.conf = conf
		w.mu.Unlock()
		w.logf("[TUN] сервер выдал другие адрес, MTU или DNS — адаптер перенастроен (addr=%v mtu=%d dns=%v)",
			conf.Addresses, conf.MTU, conf.DNS)
	}

	if err := reconfigureDevice(dev, conf); err != nil {
		return fmt.Errorf("tun: %w", err)
	}
	if err := w.route(adapter, conf); err != nil {
		return err
	}

	endpoint := w.endpointOf(conf)
	w.mu.Lock()
	if w.closed {
		// Close уже снял адаптер и защиту DNS, а route включил её снова.
		w.mu.Unlock()
		w.disableDNSGuard()
		return errors.New("tun: datapath остановлен во время запуска")
	}
	w.endpoint = endpoint
	w.mu.Unlock()

	w.logf("[TUN] адаптер %q перенастроен (endpoint=%s)", w.opts.Name, conf.Endpoint)
	return nil
}

// route пускает трафик системы в туннель: маршруты fullTunnelRoutes и
// защита DNS. На отказе защиты маршруты снимаются, чтобы не оставить туннель
// без неё.
func (w *Tun) route(adapter *tunAdapter, conf *WGConfig) error {
	for _, warning := range allowedIPsWarnings(conf) {
		w.logf("[TUN] Внимание: %s", warning)
	}
	if err := adapter.addRoutes(); err != nil {
		return fmt.Errorf("tun: маршруты: %w", err)
	}
	w.blockIPv6(adapter.ipv6Routed != nil && !adapter.ipv6Routed())
	if err := w.enableDNSGuard(conf.DNS); err != nil {
		_ = adapter.removeRoutes()
		return fmt.Errorf("tun: %w", err)
	}
	return nil
}

// Suspend снимает защиту DNS и маршруты, оставляя адаптер (Persistent):
// трафик системы идёт мимо туннеля, пока следующий Start их не вернёт.
// Заодно у устройства убирается пир — иначе wireguard-go слал бы
// рукопожатия старым ключом, пока ядра между попытками нет.
//
// Защита — раньше маршрутов: с kill-switch её Disable закрывает трафик
// системы (plan-e3.md, K3), и мига, когда маршрутов уже нет, а закрыть
// ещё не успели, быть не должно. Без kill-switch порядок безвреден: DNS
// без запрета на этот миг уходит в туннель.
func (w *Tun) Suspend() error {
	w.mu.Lock()
	dev, adapter := w.dev, w.adapter
	w.mu.Unlock()
	if adapter == nil {
		return nil
	}

	dev.RemoveAllPeers()
	var err error
	if w.opts.DNSGuard != nil {
		if derr := w.opts.DNSGuard.Disable(); derr != nil {
			err = fmt.Errorf("tun: %w", derr)
		}
	}
	if rerr := adapter.removeRoutes(); rerr != nil {
		err = errors.Join(err, fmt.Errorf("tun: снятие маршрутов: %w", rerr))
	}
	return err
}

// Close останавливает wireguard-go и убирает адаптер. Безопасен до Start
// и при повторном вызове; одновременный вызов ждёт конца первого (контракт
// Datapath.Close).
func (w *Tun) Close() error {
	w.closeOnce.Do(func() { w.closeErr = w.close() })
	return w.closeErr
}

func (w *Tun) close() error {
	w.mu.Lock()
	w.closed = true
	dev, adapter := w.dev, w.adapter
	w.dev, w.adapter, w.conf = nil, nil, nil
	w.mu.Unlock()

	if dev == nil {
		return nil
	}
	// Защита — раньше адаптера, по той же причине, что в Suspend.
	w.disableDNSGuard()
	if adapter.flush != nil {
		if err := adapter.flush(); err != nil {
			w.logf("[TUN] сброс настроек адаптера перед закрытием: %v", err)
		}
	}
	// Close устройства закрывает TUN, а закрытие TUN убирает адаптер.
	// Ошибки wireguard-go здесь не возвращает.
	dev.Close()
	adapter.release()
	w.logf("[TUN] адаптер %q снят", w.opts.Name)
	return nil
}

// adapterSettingsChanged — выдал ли сервер на новой попытке другие адреса,
// MTU или DNS, то есть нужно ли перенастраивать адаптер.
func adapterSettingsChanged(prev, conf *WGConfig) bool {
	return prev == nil || prev.MTU != conf.MTU ||
		!slices.Equal(prev.Addresses, conf.Addresses) || !slices.Equal(prev.DNS, conf.DNS)
}

// endpointOf — Endpoint конфига адресом с портом; не адрес — строка в лог
// и нулевое значение.
func (w *Tun) endpointOf(conf *WGConfig) netip.AddrPort {
	endpoint, ok := conf.EndpointAddrPort()
	if !ok {
		w.logf("[TUN] endpoint %q — не адрес с портом", conf.Endpoint)
	}
	return endpoint
}

// enableDNSGuard включает защиту DNS, если она дана: DNS системы — только
// к резолверам туннеля.
func (w *Tun) enableDNSGuard(dns []netip.Addr) error {
	if w.opts.DNSGuard == nil {
		return nil
	}
	return w.opts.DNSGuard.Enable(dns)
}

// blockIPv6 передаёт защите DNS, закрывать ли IPv6 системы: туннель его не
// везёт, и без запрета он шёл бы мимо туннеля (plan-e4.md, D1).
func (w *Tun) blockIPv6(block bool) {
	blocker, ok := w.opts.DNSGuard.(IPv6Blocker)
	if ok {
		blocker.BlockIPv6(block)
	}
	switch {
	case !block:
	case ok:
		w.logf("[TUN] IPv6 системы, кроме loopback и сокетов tt, закрыт защитой DNS: туннель его не везёт")
	default:
		w.logf("[TUN] Внимание: туннель не везёт IPv6, а закрыть его без защиты DNS нечем — " +
			"IPv6 системы идёт мимо туннеля")
	}
}

// disableDNSGuard снимает защиту DNS. Отказ только пишется: адаптер всё
// равно снимается, а правила уйдут вместе с сессией, когда tt выйдет.
func (w *Tun) disableDNSGuard() {
	if w.opts.DNSGuard == nil {
		return
	}
	if err := w.opts.DNSGuard.Disable(); err != nil {
		w.logf("[TUN] %v", err)
	}
}

func (w *Tun) logf(format string, args ...any) {
	if w.opts.Logf == nil {
		return
	}
	w.opts.Logf(format, args...)
}
