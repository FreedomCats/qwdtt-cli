package datapath

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/app"
)

// DefaultTunnelName — имя туннеля у штатного WireGuard и файла конфига
// (qwdtt-cli.conf → служба WireGuardTunnel$qwdtt-cli, адаптер «qwdtt-cli»).
const DefaultTunnelName = app.Name

// DefaultProgramTimeout — сколько ждать wireguard.exe на установку и снятие
// службы. Штатно это секунды; дольше значит, что программа встала (например,
// показала окно с ошибкой), и ждать её дальше незачем.
const DefaultProgramTimeout = 30 * time.Second

// tunnelNameRe — правило wireguard-windows на имя туннеля: оно становится
// именем службы и адаптера.
var tunnelNameRe = regexp.MustCompile(`^[a-zA-Z0-9_=+.-]{1,32}$`)

// ExternalOptions — настройки datapath external.
type ExternalOptions struct {
	// Program — путь к wireguard.exe. Пусто — искать самим: реестр, каталог
	// Program Files, затем PATH (см. locateWireGuard).
	Program string
	// Name — имя туннеля. Пусто — DefaultTunnelName.
	Name string
	// Dir — каталог для файла конфига. Пусто — каталог настроек пользователя
	// (qwdtt-cli в os.UserConfigDir). Рядом с бинарём файл не кладётся ни
	// при каких настройках: в нём приватный ключ.
	Dir string
	// Timeout — сколько ждать wireguard.exe. Ноль — DefaultProgramTimeout.
	Timeout time.Duration

	// Logf — куда писать; nil означает молчать. Datapath сам в stdout не
	// пишет — вывод принадлежит CLI.
	Logf func(format string, args ...any)

	// DNSGuard — защита от утечки DNS: включается, когда служба туннеля
	// работает, выключается в Close. Нужна и здесь: с половинами /1 вместо
	// /0 (splitDefaultRoutes) WireGuard свой запрет DNS не включает
	// (plan-e2.md, решение 5). nil — без неё.
	DNSGuard DNSGuard
}

// External — datapath поверх штатного WireGuard для Windows: конфиг от сервера
// записывается в файл, wireguard.exe ставит по нему службу туннеля, при
// остановке служба снимается.
//
// Файл с приватным ключом живёт столько же, сколько служба: служба хранит
// путь к нему и читает его при каждом запуске, копии у себя не делает.
// Удаляется файл в Close, после снятия службы, и при любом отказе Start.
// (Первая версия D3 удаляла файл сразу после установки — служба падала
// с «файл не найден», а туннель числился работающим; найдено живой
// проверкой H2.)
type External struct {
	opts ExternalOptions

	// program — найденный wireguard.exe; programErr — почему не найден.
	// Ищется в конструкторе, чтобы Requires мог назвать точный путь.
	program    string
	programErr error

	// serviceRunning ждёт, пока служба туннеля дойдёт до «работает», и
	// отвечает ошибкой, если она остановилась. /installtunnelservice
	// возвращается сразу после запуска службы, не дожидаясь её: без этой
	// проверки упавшая служба выглядит успешной установкой. Поле — ради
	// тестов, где настоящей службы нет.
	serviceRunning func(ctx context.Context, name string) error

	mu        sync.Mutex
	started   bool
	closed    bool
	installed bool
	endpoint  netip.AddrPort

	// done закрывается в Close и снимает сторожа отмены ctx.
	done chan struct{}

	// closeOnce и closeErr — снятие один раз, ожидание всеми (см. Close).
	closeOnce sync.Once
	closeErr  error
}

// NewExternal готовит datapath. Программа ищется сразу, сеть и служба
// не трогаются до Start.
func NewExternal(opts ExternalOptions) *External {
	if strings.TrimSpace(opts.Name) == "" {
		opts.Name = DefaultTunnelName
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultProgramTimeout
	}
	e := &External{opts: opts, done: make(chan struct{}), serviceRunning: waitTunnelService}
	if opts.Program != "" {
		e.program = opts.Program
	} else {
		e.program, e.programErr = locateWireGuard()
	}
	return e
}

// Requires: права администратора (службу ставит только он), wireguard.exe,
// netguard — туннель забирает весь трафик системы — и защита DNS. Programs называет
// найденный путь, а если найти не вышло — голое имя, чтобы вызывающий
// сказал «не найдена программа». Вне Windows — Unsupported: служб WireGuard
// для Windows там нет.
func (e *External) Requires() Requirements {
	program := "wireguard.exe"
	if e.program != "" {
		program = e.program
	}
	return Requirements{
		Unsupported: externalUnsupported,
		Admin:       true,
		Programs:    []string{program},
		Netguard:    true,
		DNSGuard:    true,
	}
}

// Endpoint — адрес из конфига, на который штатный WireGuard шлёт UDP; на нём
// слушает ядро.
func (e *External) Endpoint() netip.AddrPort {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.endpoint
}

// Start записывает конфиг, ставит службу туннеля, ждёт, пока она заработает,
// и включает защиту DNS. При любом отказе служба снимается, а файл
// удаляется: следов не остаётся.
func (e *External) Start(ctx context.Context, conf *WGConfig) error {
	if conf == nil {
		return errors.New("external: пустой конфиг WireGuard")
	}
	if e.programErr != nil {
		return fmt.Errorf("external: %w", e.programErr)
	}
	if !tunnelNameRe.MatchString(e.opts.Name) {
		return fmt.Errorf("external: имя туннеля %q: допустимы латиница, цифры и _=+.-, не длиннее 32 знаков", e.opts.Name)
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return errors.New("external: datapath уже остановлен")
	}
	if e.started {
		e.mu.Unlock()
		return errors.New("external: datapath уже запущен")
	}
	e.started = true
	e.mu.Unlock()

	raw, split := splitDefaultRoutes(conf.Raw)
	raw, blocked6 := blockIPv6(raw)
	raw, addedDNS := addDefaultDNS(raw, conf.DNS)
	path, err := e.writeConfig(raw)
	if err != nil {
		e.startFailed()
		return err
	}

	if err := e.run(ctx, "/installtunnelservice", path); err != nil {
		_ = os.Remove(path)
		e.startFailed()
		return fmt.Errorf("external: установка службы туннеля %q: %w", e.opts.Name, err)
	}

	if err := e.serviceRunning(ctx, e.opts.Name); err != nil {
		// Служба стоит, но не работает: снять её и убрать файл, иначе
		// следующая попытка наткнётся на занятое имя, а ключ останется
		// на диске.
		_ = e.uninstall()
		_ = os.Remove(path)
		e.startFailed()
		return fmt.Errorf("external: служба туннеля %q не запустилась: %w; подробности — wireguard.exe /dumplog",
			e.opts.Name, err)
	}

	if e.opts.DNSGuard != nil {
		if err := e.opts.DNSGuard.Enable(conf.DNS); err != nil {
			_ = e.uninstall()
			_ = os.Remove(path)
			e.startFailed()
			return fmt.Errorf("external: %w", err)
		}
	}

	endpoint, ok := conf.EndpointAddrPort()
	if !ok {
		e.logf("[WG-EXT] endpoint %q — не адрес с портом", conf.Endpoint)
	}

	e.mu.Lock()
	e.installed = true
	e.endpoint = endpoint
	e.mu.Unlock()

	allowed, _ := splitAllowedIPs(conf.AllowedIPs)
	e.logf("[WG-EXT] служба туннеля %q установлена (addr=%v endpoint=%s allowed=%s)",
		e.opts.Name, conf.Addresses, conf.Endpoint, strings.Join(allowed, ","))
	if split {
		e.logf("[WG-EXT] маршрут по умолчанию в AllowedIPs записан двумя половинами /1: " +
			"с /0 WireGuard включил бы свой kill-switch и отрезал соединения qwdtt-cli с VK и TURN")
	}
	if blocked6 {
		e.logf("[WG-EXT] сервер не выдал маршрута IPv6: в AllowedIPs дописаны ::/1, 8000::/1 — " +
			"IPv6 машины уходит в туннель и там отбрасывается, а не идёт мимо него")
	}
	if addedDNS {
		e.logf("[WG-EXT] сервер не прислал DNS, в конфиг записан DNS = %s", joinAddrs(conf.DNS))
	}

	go func() {
		select {
		case <-ctx.Done():
			_ = e.Close()
		case <-e.done:
		}
	}()
	return nil
}

// Close снимает службу туннеля и удаляет файл конфига. Безопасен до Start
// и при повторном вызове: супервизор зовёт его из defer.
//
// Служба обязана быть снята, иначе после выхода qwdtt-cli в системе останется
// туннель в никуда; поэтому снятие идёт со своим сроком (см. uninstall).
//
// Снятие делается один раз, а все вызовы ждут его конца и возвращают его
// ошибку. Иначе была гонка (найдена живым прогоном N4 Э2): по отмене ctx
// первым в Close входит сторож из Start, defer супервизора получал nil
// сразу, процесс выходил, а горутина сторожа гибла между снятием службы
// и удалением файла — служба снята, а файл с приватным ключом остался.
// Ошибку Close пишет в лог сам: супервизор её отбрасывает, а человеку
// нужна подсказка, что снимать вручную.
func (e *External) Close() error {
	e.closeOnce.Do(func() {
		e.closeErr = e.close()
		if e.closeErr != nil {
			e.logf("[WG-EXT] ошибка остановки: %v", e.closeErr)
		}
	})
	return e.closeErr
}

func (e *External) close() error {
	e.mu.Lock()
	e.closed = true
	installed := e.installed
	e.installed = false
	close(e.done)
	e.mu.Unlock()

	if !installed {
		// Файл мог уцелеть, если прошлый процесс умер, не дойдя до Close.
		_ = os.Remove(e.confPath())
		return nil
	}

	// Защита — раньше службы: с kill-switch её Disable закрывает трафик
	// системы (plan-e3.md, K3), и мига, когда службы уже нет, а закрыть ещё
	// не успели, быть не должно. Без kill-switch DNS без запрета на этот миг
	// уходит в туннель. Потом служба, потом файл: служба читает его, пока
	// работает.
	var err error
	if e.opts.DNSGuard != nil {
		if derr := e.opts.DNSGuard.Disable(); derr != nil {
			err = fmt.Errorf("external: %w", derr)
		}
	}
	err = errors.Join(err, e.uninstall())
	if rerr := os.Remove(e.confPath()); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		err = errors.Join(err, fmt.Errorf("external: удаление конфига %s: %w", e.confPath(), rerr))
	}
	if err != nil {
		return err
	}
	e.logf("[WG-EXT] служба туннеля %q снята, конфиг удалён", e.opts.Name)
	return nil
}

// uninstall снимает службу туннеля со своим сроком, а не с ctx туннеля: к
// этому моменту ctx обычно уже отменён, а служба обязана быть снята.
func (e *External) uninstall() error {
	ctx, cancel := context.WithTimeout(context.Background(), e.opts.Timeout)
	defer cancel()
	if err := e.run(ctx, "/uninstalltunnelservice", e.opts.Name); err != nil {
		return fmt.Errorf("external: снятие службы туннеля %q: %w; снимите её вручную: wireguard.exe /uninstalltunnelservice %s",
			e.opts.Name, err, e.opts.Name)
	}
	return nil
}

// confPath — путь к файлу конфига: <каталог>/<имя>.conf. Расширение задаёт
// wireguard.exe, имя файла становится именем туннеля.
func (e *External) confPath() string {
	return filepath.Join(e.dir(), e.opts.Name+".conf")
}

func (e *External) dir() string {
	if e.opts.Dir != "" {
		return e.opts.Dir
	}
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, app.Name)
	}
	return os.TempDir()
}

// writeConfig пишет текст конфига в файл, доступный только пользователю.
// Текст — исходный (WGConfig.Raw) с одной правкой, splitDefaultRoutes:
// читать его будет чужая программа, и пересобирать из полей значило бы
// терять то, чего разбор не понял (D1).
func (e *External) writeConfig(raw string) (string, error) {
	dir := e.dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("external: каталог для конфига %s: %w", dir, err)
	}
	path := e.confPath()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("external: файл конфига %s: %w", path, err)
	}
	if !strings.HasSuffix(raw, "\n") {
		raw += "\n"
	}
	_, werr := f.WriteString(raw)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("external: запись конфига %s: %w", path, errors.Join(werr, cerr))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("external: путь к конфигу %s: %w", path, err)
	}
	return abs, nil
}

// run зовёт wireguard.exe с аргументами и ждёт не дольше Timeout. Вывод
// программы попадает в ошибку: своих сообщений о причинах у неё нет, а
// человеку нужно знать, чем она недовольна.
func (e *External) run(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, e.opts.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.program, args...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = fmt.Errorf("wireguard.exe не ответил за %s", e.opts.Timeout)
	case msg != "":
		err = fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

// startFailed возвращает datapath в исходное состояние: неудачный Start не
// должен закрывать дорогу повторной попытке.
func (e *External) startFailed() {
	e.mu.Lock()
	e.started = false
	e.mu.Unlock()
}

func (e *External) logf(format string, args ...any) {
	if e.opts.Logf == nil {
		return
	}
	e.opts.Logf(format, args...)
}

// splitDefaultRoutes заменяет в тексте конфига маршруты по умолчанию в
// AllowedIPs парами половин: 0.0.0.0/0 → 0.0.0.0/1, 128.0.0.0/1 и
// ::/0 → ::/1, 8000::/1 (решение 5 Э2). Второе значение — была ли замена.
//
// Зачем: WireGuard для Windows при одном пире с префиксом /0 в AllowedIPs
// включает свой kill-switch (wireguard/windows v1.1.1,
// tunnel/addressconfig.go:165, enableFirewall) и режет всё мимо туннеля,
// в том числе сокеты qwdtt-cli, которые netguard отправил через физический
// интерфейс. Половины покрывают то же пространство и выигрывают у маршрута
// по умолчанию длиной префикса, а kill-switch не включают.
//
// Правятся только значения строк AllowedIPs в секциях [Peer]; ключ, пробелы
// до «=», комментарий в конце строки и конец строки (\r\n) сохраняются,
// остальные строки не трогаются. Если строки AllowedIPs нет вовсе,
// ParseWgQuick читает это как 0.0.0.0/0 (так туннель строит socks), а
// wireguard.exe не поставил бы ни одного маршрута; поэтому в первую секцию
// [Peer] дописывается AllowedIPs = 0.0.0.0/1, 128.0.0.0/1.
func splitDefaultRoutes(raw string) (string, bool) {
	lines := strings.Split(raw, "\n")
	section := ""
	firstPeer := -1
	sawAllowed := false
	changed := false
	for i, line := range lines {
		body, cr := strings.CutSuffix(line, "\r")
		trimmed := strings.TrimSpace(body)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			if section == "peer" && firstPeer < 0 {
				firstPeer = i
			}
			continue
		}
		eq := strings.IndexByte(body, '=')
		if section != "peer" || eq < 0 || strings.ToLower(strings.TrimSpace(body[:eq])) != "allowedips" {
			continue
		}
		sawAllowed = true
		val, comment, hasComment := strings.Cut(body[eq+1:], "#")
		parts, split := splitAllowedIPs(strings.Split(val, ","))
		if !split {
			continue
		}
		changed = true
		rebuilt := body[:eq+1] + " " + strings.Join(parts, ", ")
		if hasComment {
			rebuilt += " #" + comment
		}
		if cr {
			rebuilt += "\r"
		}
		lines[i] = rebuilt
	}
	if !sawAllowed && firstPeer >= 0 {
		added := "AllowedIPs = 0.0.0.0/1, 128.0.0.0/1"
		if strings.HasSuffix(lines[firstPeer], "\r") {
			added += "\r"
		}
		lines = append(lines[:firstPeer+1], append([]string{added}, lines[firstPeer+1:]...)...)
		changed = true
	}
	return strings.Join(lines, "\n"), changed
}

// blockIPv6 дописывает ::/1, 8000::/1 в AllowedIPs полного туннеля IPv4, если
// IPv6 в нём не покрыт. Второе значение — была ли правка. Звать после
// splitDefaultRoutes: полный туннель IPv4 к этому времени уже записан
// половинами 0.0.0.0/1, 128.0.0.0/1.
//
// Зачем (решение 2 Э2, то же, что у tun): маршруты WireGuard для Windows
// ставит из AllowedIPs. Если сервер выдал только 0.0.0.0/0, маршрутов IPv6
// в туннель нет, и у кого провайдер даёт IPv6, весь IPv6 машины идёт мимо
// туннеля с настоящим адресом (сайты с AAAA — мимо VPN). С половинами IPv6
// уходит в туннель; адреса IPv6 у туннеля нет, сервер такие пакеты
// отбрасывает, и программы откатываются на IPv4 (Happy Eyeballs).
//
// Половины, а не ::/0: с /0 WireGuard включил бы kill-switch (см.
// splitDefaultRoutes). Сплит-конфиг (без обеих половин IPv4) не трогается.
// Половины дописываются в ту строку AllowedIPs секции [Peer], где стоит
// 0.0.0.0/1, с сохранением комментария и \r.
func blockIPv6(raw string) (string, bool) {
	lines := strings.Split(raw, "\n")
	type peerAllowed struct{ prefixes map[string]bool }
	var peers []peerAllowed
	target := map[int]int{} // номер секции [Peer] → строка с 0.0.0.0/1
	section := ""
	for i, line := range lines {
		body := strings.TrimSuffix(line, "\r")
		trimmed := strings.TrimSpace(body)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			if section == "peer" {
				peers = append(peers, peerAllowed{prefixes: map[string]bool{}})
			}
			continue
		}
		eq := strings.IndexByte(body, '=')
		if section != "peer" || eq < 0 || strings.ToLower(strings.TrimSpace(body[:eq])) != "allowedips" {
			continue
		}
		val, _, _ := strings.Cut(body[eq+1:], "#")
		for _, a := range strings.Split(val, ",") {
			p, err := netip.ParsePrefix(strings.TrimSpace(a))
			if err != nil {
				continue
			}
			p = p.Masked()
			peers[len(peers)-1].prefixes[p.String()] = true
			if p.String() == "0.0.0.0/1" {
				target[len(peers)-1] = i
			}
		}
	}
	changed := false
	for n, peer := range peers {
		i, ok := target[n]
		if !ok || !peer.prefixes["128.0.0.0/1"] || (peer.prefixes["::/1"] && peer.prefixes["8000::/1"]) {
			continue
		}
		body, cr := strings.CutSuffix(lines[i], "\r")
		eq := strings.IndexByte(body, '=')
		val, comment, hasComment := strings.Cut(body[eq+1:], "#")
		var parts []string
		for _, a := range strings.Split(val, ",") {
			if a = strings.TrimSpace(a); a != "" {
				parts = append(parts, a)
			}
		}
		for _, h := range []string{"::/1", "8000::/1"} {
			if !peer.prefixes[h] {
				parts = append(parts, h)
			}
		}
		rebuilt := body[:eq+1] + " " + strings.Join(parts, ", ")
		if hasComment {
			rebuilt += " #" + comment
		}
		if cr {
			rebuilt += "\r"
		}
		lines[i] = rebuilt
		changed = true
	}
	return strings.Join(lines, "\n"), changed
}

// addDefaultDNS дописывает в секцию [Interface] строку DNS с резолверами
// dns, если своей строки DNS там нет. Второе значение — была ли правка.
//
// Зачем: без строки DNS штатный WireGuard не даёт адаптеру резолверов, и
// Windows спрашивает резолвер провайдера, а его запрещает защита DNS
// (DNSGuard пускает только к WGConfig.DNS) — DNS системы пропал бы целиком.
// ParseWgQuick в этом случае подставляет 1.1.1.1; та же подстановка нужна
// и в файле.
func addDefaultDNS(raw string, dns []netip.Addr) (string, bool) {
	if len(dns) == 0 {
		return raw, false
	}
	lines := strings.Split(raw, "\n")
	section := ""
	iface := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSpace(trimmed[1 : len(trimmed)-1]))
			if section == "interface" && iface < 0 {
				iface = i
			}
			continue
		}
		key, _, ok := strings.Cut(trimmed, "=")
		if ok && section == "interface" && strings.ToLower(strings.TrimSpace(key)) == "dns" {
			return raw, false
		}
	}
	if iface < 0 {
		return raw, false
	}
	added := "DNS = " + joinAddrs(dns)
	if strings.HasSuffix(lines[iface], "\r") {
		added += "\r"
	}
	lines = append(lines[:iface+1], append([]string{added}, lines[iface+1:]...)...)
	return strings.Join(lines, "\n"), true
}

func joinAddrs(addrs []netip.Addr) string {
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}

// splitAllowedIPs — та же замена над списком префиксов: каждый /0 становится
// двумя половинами своего семейства, повторы убираются, пустые элементы
// выбрасываются, остальное остаётся как было (без пробелов по краям).
// Второе значение — был ли среди префиксов хоть один /0.
func splitAllowedIPs(allowed []string) ([]string, bool) {
	out := make([]string, 0, len(allowed)+2)
	seen := make(map[string]bool, len(allowed)+2)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	split := false
	for _, a := range allowed {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		p, err := netip.ParsePrefix(a)
		switch {
		case err != nil || p.Bits() != 0:
			add(a)
		case p.Addr().Is4():
			split = true
			add("0.0.0.0/1")
			add("128.0.0.0/1")
		default:
			split = true
			add("::/1")
			add("8000::/1")
		}
	}
	return out, split
}
