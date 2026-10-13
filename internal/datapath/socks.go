package datapath

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"sync"

	"github.com/armon/go-socks5"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// DefaultSocksListen — адрес SOCKS5 по умолчанию, тот же, что был у флага ядра.
const DefaultSocksListen = "127.0.0.1:1080"

// SocksOptions — настройки datapath socks.
type SocksOptions struct {
	// Listen — адрес SOCKS5-сервера. Пусто — DefaultSocksListen.
	Listen string
	// Auth включает проверку логина и пароля (метод 0x02).
	Auth     bool
	Username string
	Password string

	// Logf — куда писать; nil означает молчать. Через него же проходит вывод
	// wireguard-go и библиотеки SOCKS5: datapath, как и ядро, сам в stdout
	// ничего не пишет — вывод принадлежит CLI.
	Logf func(format string, args ...any)
}

// Socks — datapath без прав администратора: userspace WireGuard поверх gVisor
// netstack и локальный SOCKS5-сервер над ним.
//
// Охват у него уже, чем у системного туннеля: только TCP CONNECT и только те
// приложения, которые умеют ходить через прокси. IPv6 отклоняется намеренно —
// маршрутов на v6 в туннеле нет, и попытка клиента сходить по AAAA должна
// заканчиваться отказом сразу, а не таймаутом (architecture.md §04).
type Socks struct {
	opts SocksOptions

	mu       sync.Mutex
	started  bool
	closed   bool
	dev      *device.Device
	ln       net.Listener
	endpoint netip.AddrPort

	// done закрывается в Close и снимает сторожа отмены ctx: без него горутина
	// сторожа жила бы до конца процесса, если ctx не отменяют вовсе.
	done chan struct{}

	// closeOnce и closeErr — уборка один раз, ожидание всеми (см. Close).
	closeOnce sync.Once
	closeErr  error
}

// NewSocks готовит datapath. Сеть не трогается до Start.
func NewSocks(opts SocksOptions) *Socks {
	if strings.TrimSpace(opts.Listen) == "" {
		opts.Listen = DefaultSocksListen
	}
	return &Socks{opts: opts, done: make(chan struct{})}
}

// Requires: ничего. В этом весь смысл режима — он работает от обычного
// пользователя и ничего не требует установленным.
func (s *Socks) Requires() Requirements { return Requirements{} }

// Endpoint — адрес, на который userspace WireGuard шлёт UDP; на нём слушает ядро.
func (s *Socks) Endpoint() netip.AddrPort {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endpoint
}

// ListenAddr — фактический адрес SOCKS5-сервера. Отличается от заказанного,
// если в настройках стоял нулевой порт; до Start — nil.
func (s *Socks) ListenAddr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Start поднимает WireGuard в userspace и SOCKS5 над ним.
//
// Возвращается, когда сервер уже слушает: занятый порт обязан выясниться здесь,
// а не в фоне через секунду после того, как CLI сказал «готово». Обслуживание
// продолжается в фоне до Close или отмены ctx.
func (s *Socks) Start(ctx context.Context, conf *WGConfig) error {
	if conf == nil {
		return errors.New("socks: пустой конфиг WireGuard")
	}
	// Проверка переехала из core.Validate вместе с самим SOCKS5. Пустые логин
	// и пароль библиотека приняла бы как валидные учётные данные — то есть
	// «авторизация включена» означало бы «пускаем любого, кто спросит».
	if s.opts.Auth {
		switch {
		case s.opts.Username == "" || s.opts.Password == "":
			return errors.New("socks: для авторизации SOCKS5 нужны логин и пароль")
		case len(s.opts.Username) > 255 || len(s.opts.Password) > 255:
			return errors.New("socks: логин и пароль SOCKS5 должны быть не длиннее 255 байт")
		}
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("socks: datapath уже остановлен")
	}
	if s.started {
		s.mu.Unlock()
		return errors.New("socks: datapath уже запущен")
	}
	s.started = true
	s.mu.Unlock()

	dev, tnet, err := s.startWireGuard(conf)
	if err != nil {
		s.startFailed()
		return err
	}

	server, err := s.newServer(tnet)
	if err != nil {
		dev.Close()
		s.startFailed()
		return fmt.Errorf("socks: %w", err)
	}

	ln, err := net.Listen("tcp", s.opts.Listen)
	if err != nil {
		dev.Close()
		s.startFailed()
		return fmt.Errorf("socks: listen %s: %w", s.opts.Listen, err)
	}

	endpoint, ok := conf.EndpointAddrPort()
	if !ok {
		s.logf("[SOCKS] endpoint %q — не адрес с портом", conf.Endpoint)
	}

	s.mu.Lock()
	s.dev, s.ln, s.endpoint = dev, ln, endpoint
	s.mu.Unlock()

	s.logf("[SOCKS] слушает %s", ln.Addr())

	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-s.done:
		}
	}()

	go func() {
		err := server.Serve(ln)
		if err != nil && !isClosedListener(err) {
			s.logf("[SOCKS] сервер остановлен: %v", err)
		}
	}()

	return nil
}

// Close останавливает SOCKS5 и WireGuard. Безопасен до Start и при повторном
// вызове: супервизор зовёт его из defer. Одновременный вызов ждёт конца
// первого (контракт Datapath.Close).
func (s *Socks) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.close() })
	return s.closeErr
}

func (s *Socks) close() error {
	s.mu.Lock()
	s.closed = true
	ln, dev := s.ln, s.dev
	s.ln, s.dev = nil, nil
	close(s.done)
	s.mu.Unlock()

	var err error
	if ln != nil {
		if cerr := ln.Close(); cerr != nil && !isClosedListener(cerr) {
			err = cerr
		}
	}
	if dev != nil {
		// Close у wireguard-go не возвращает ошибку и закрывает в том числе
		// netstack-интерфейс: отдельно закрывать tunDev не нужно.
		dev.Close()
	}
	return err
}

// startWireGuard поднимает WG через netstack — без прав, без TUN-адаптера,
// целиком в адресном пространстве процесса.
func (s *Socks) startWireGuard(conf *WGConfig) (*device.Device, *netstack.Net, error) {
	tunDev, tnet, err := netstack.CreateNetTUN(conf.Addresses, conf.DNS, conf.MTU)
	if err != nil {
		return nil, nil, fmt.Errorf("socks: CreateNetTUN: %w", err)
	}
	dev, err := startDevice(tunDev, conf, "[WG-SOCKS]", s.logf)
	if err != nil {
		return nil, nil, fmt.Errorf("socks: %w", err)
	}
	s.logf("[SOCKS] userspace WireGuard поднят (addr=%v endpoint=%s)", conf.Addresses, conf.Endpoint)
	return dev, tnet, nil
}

// newServer собирает SOCKS5-сервер поверх netstack: свой резолвер, свой dial
// и правило, отсекающее IPv6.
func (s *Socks) newServer(tnet *netstack.Net) (*socks5.Server, error) {
	conf := &socks5.Config{
		Dial:     dialIPv4Only(tnet),
		Resolver: &netstackNameResolver{tnet: tnet},
		Rules:    ipv4OnlyRule{},
		Logger:   log.New(io.Writer(socksLogFilter{logf: s.logf}), "", 0),
	}
	if s.opts.Auth {
		conf.Credentials = socks5.StaticCredentials{s.opts.Username: s.opts.Password}
	}
	return socks5.New(conf)
}

// startFailed возвращает datapath в исходное состояние: неудачный Start не
// должен закрывать дорогу повторной попытке — перезапуск транспорта это делает.
func (s *Socks) startFailed() {
	s.mu.Lock()
	s.started = false
	s.mu.Unlock()
}

func (s *Socks) logf(format string, args ...any) {
	if s.opts.Logf == nil {
		return
	}
	s.opts.Logf(format, args...)
}

// isClosedListener отличает штатное закрытие слушателя от настоящей ошибки.
func isClosedListener(err error) bool {
	return errors.Is(err, net.ErrClosed) ||
		strings.Contains(err.Error(), "use of closed network connection")
}

// netstackNameResolver резолвит имена внутри туннеля: DNS-сервер берётся
// из конфига WireGuard, запрос уходит через туннель, а не мимо него.
type netstackNameResolver struct {
	tnet *netstack.Net
}

func (r *netstackNameResolver) Resolve(ctx context.Context, name string) (context.Context, net.IP, error) {
	addrs, err := r.tnet.LookupContextHost(ctx, name)
	if err != nil {
		return ctx, nil, err
	}
	// Только IPv4: туннель без IPv6-маршрутов.
	for _, a := range addrs {
		ip := net.ParseIP(a)
		if ip == nil {
			continue
		}
		if v4 := ip.To4(); v4 != nil {
			return ctx, v4, nil
		}
	}
	return ctx, nil, fmt.Errorf("no IPv4 addresses for %s", name)
}

// ipv4OnlyRule отклоняет CONNECT на IPv6 (клиент часто пробует AAAA первым).
type ipv4OnlyRule struct{}

func (ipv4OnlyRule) Allow(ctx context.Context, req *socks5.Request) (context.Context, bool) {
	if req == nil || req.DestAddr == nil {
		return ctx, true
	}
	ip := req.DestAddr.IP
	if ip != nil && ip.To4() == nil {
		return ctx, false
	}
	return ctx, true
}

// socksLogFilter превращает вывод чужой библиотеки SOCKS5 в наши записи
// и глушит шум, которого при отключённом IPv6 будет по строке на запрос.
type socksLogFilter struct {
	logf func(format string, args ...any)
}

func (f socksLogFilter) Write(p []byte) (n int, err error) {
	msg := string(p)
	if isBenignSocksNoise(msg) {
		return len(p), nil
	}
	f.logf("%s", strings.TrimRight(msg, "\r\n"))
	return len(p), nil
}

// bracketedIPv6 — адрес IPv6 в квадратных скобках, как его печатает
// net.JoinHostPort: [2001:db8::1]:443.
var bracketedIPv6 = regexp.MustCompile(`\[[0-9A-Fa-f]*:[0-9A-Fa-f:.%a-z]*\]`)

func isBenignSocksNoise(msg string) bool {
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "blocked by rules") {
		return true
	}
	if strings.Contains(lower, "ipv6") {
		return true
	}
	// Connect to [2001:…] / no route на v6. Адрес ищется именно IPv6 в
	// скобках: прежняя проверка «есть [, ] и :» срабатывала на префикс
	// самой библиотеки «[ERR] socks:» и глушила любой отказ подключиться,
	// в том числе к IPv4 (найдено первым прогоном на Linux).
	if bracketedIPv6.MatchString(msg) {
		if strings.Contains(lower, "no route") || strings.Contains(lower, "connect to") {
			return true
		}
	}
	if strings.Contains(msg, "::") && (strings.Contains(lower, "no route") || strings.Contains(lower, "failed")) {
		return true
	}
	// Соединение закрыл или сбросил клиент: браузер бросил вкладку, curl
	// дочитал ответ. Это штатно, а печаталось строкой [ERR] на каждое такое
	// соединение (найдено первым прогоном на Linux). Отказ подключиться
	// к цели («Connect to … failed») сюда не попадает и печатается.
	if strings.Contains(lower, "failed to get version byte: eof") {
		return true
	}
	if !strings.Contains(lower, "connect to") &&
		(strings.Contains(lower, "connection reset by peer") || strings.Contains(lower, "broken pipe")) {
		return true
	}
	return false
}

func dialIPv4Only(tnet *netstack.Net) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err == nil {
			if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
				return nil, fmt.Errorf("IPv6 skipped")
			}
		}
		return tnet.DialContext(ctx, network, addr)
	}
}
