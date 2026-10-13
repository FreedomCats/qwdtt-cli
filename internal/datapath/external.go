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
)

// DefaultTunnelName — имя туннеля у штатного WireGuard и файла конфига
// (tt.conf → служба WireGuardTunnel$tt, адаптер «tt»).
const DefaultTunnelName = "tt"

// DefaultProgramTimeout — сколько ждать wireguard.exe на установку и снятие
// службы. Штатно это секунды; дольше значит, что программа встала (например,
// показала окно с ошибкой), и ждать её дальше незачем.
const DefaultProgramTimeout = 30 * time.Second

// ExternalWarning — ограничение Э1, о котором CLI предупреждает при запуске:
// без netguard (architecture.md §05) полный туннель забирает и собственные
// соединения ядра с VK, и подключение зацикливается.
const ExternalWarning = "штатный WireGuard заберёт весь трафик системы, включая соединения tt с VK и TURN; " +
	"до появления netguard (Э2) туннель поднимется только со сплит-конфигом, " +
	"в котором AllowedIPs не покрывает адреса VK"

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
}

// External — datapath поверх штатного WireGuard для Windows: конфиг от сервера
// записывается в файл, wireguard.exe ставит по нему службу туннеля, при
// остановке служба снимается.
//
// Файл с приватным ключом живёт ровно столько, сколько идёт установка:
// wireguard.exe копирует конфиг в своё хранилище, и после возврата наш файл
// удаляется. Снятие службы удаляет и копию.
type External struct {
	opts ExternalOptions

	// program — найденный wireguard.exe; programErr — почему не найден.
	// Ищется в конструкторе, чтобы Requires мог назвать точный путь.
	program    string
	programErr error

	mu        sync.Mutex
	started   bool
	closed    bool
	installed bool
	endpoint  netip.AddrPort

	// done закрывается в Close и снимает сторожа отмены ctx.
	done chan struct{}
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
	e := &External{opts: opts, done: make(chan struct{})}
	if opts.Program != "" {
		e.program = opts.Program
	} else {
		e.program, e.programErr = locateWireGuard()
	}
	return e
}

// Requires: права администратора (службу ставит только он), wireguard.exe
// и предупреждение о петле. Programs называет найденный путь, а если найти
// не вышло — голое имя, чтобы вызывающий сказал «не найдена программа».
func (e *External) Requires() Requirements {
	program := "wireguard.exe"
	if e.program != "" {
		program = e.program
	}
	return Requirements{
		Admin:    true,
		Programs: []string{program},
		Warning:  ExternalWarning,
	}
}

// Endpoint — адрес из конфига, на который штатный WireGuard шлёт UDP; на нём
// слушает ядро.
func (e *External) Endpoint() netip.AddrPort {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.endpoint
}

// Start записывает конфиг и ставит службу туннеля. Возвращается, когда
// wireguard.exe отчитался об установке; файл конфига к этому моменту удалён.
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

	path, err := e.writeConfig(conf)
	if err != nil {
		e.startFailed()
		return err
	}
	// Файл нужен только на время установки: wireguard.exe забирает конфиг
	// в своё хранилище. Удаляется при любом исходе.
	defer func() { _ = os.Remove(path) }()

	if err := e.run(ctx, "/installtunnelservice", path); err != nil {
		e.startFailed()
		return fmt.Errorf("external: установка службы туннеля %q: %w", e.opts.Name, err)
	}

	endpoint, ok := conf.EndpointAddrPort()
	if !ok {
		e.logf("[WG-EXT] endpoint %q — не адрес с портом", conf.Endpoint)
	}

	e.mu.Lock()
	e.installed = true
	e.endpoint = endpoint
	e.mu.Unlock()

	e.logf("[WG-EXT] служба туннеля %q установлена (addr=%v endpoint=%s allowed=%s)",
		e.opts.Name, conf.Addresses, conf.Endpoint, strings.Join(conf.AllowedIPs, ","))
	if coversEverything(conf.AllowedIPs) {
		e.logf("[WG-EXT] предупреждение: AllowedIPs покрывает всё; %s", ExternalWarning)
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

// Close снимает службу туннеля. Безопасен до Start и при повторном вызове:
// супервизор зовёт его из defer.
//
// Снятие идёт со своим сроком, а не с ctx туннеля: к этому моменту ctx обычно
// уже отменён, а служба обязана быть снята — иначе после выхода tt в системе
// останется туннель в никуда.
func (e *External) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	installed := e.installed
	e.installed = false
	close(e.done)
	e.mu.Unlock()

	// Файл мог уцелеть, если процесс умер между записью и удалением.
	_ = os.Remove(e.confPath())

	if !installed {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), e.opts.Timeout)
	defer cancel()
	if err := e.run(ctx, "/uninstalltunnelservice", e.opts.Name); err != nil {
		return fmt.Errorf("external: снятие службы туннеля %q: %w; снимите её вручную: wireguard.exe /uninstalltunnelservice %s",
			e.opts.Name, err, e.opts.Name)
	}
	e.logf("[WG-EXT] служба туннеля %q снята", e.opts.Name)
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
		return filepath.Join(base, "qwdtt-cli")
	}
	return os.TempDir()
}

// writeConfig пишет исходный текст конфига в файл, доступный только
// пользователю. Именно исходный (WGConfig.Raw): читать его будет чужая
// программа, и пересобирать из полей значило бы терять то, чего разбор
// не понял (D1).
func (e *External) writeConfig(conf *WGConfig) (string, error) {
	dir := e.dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("external: каталог для конфига %s: %w", dir, err)
	}
	path := e.confPath()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("external: файл конфига %s: %w", path, err)
	}
	raw := conf.Raw
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

// coversEverything — есть ли среди AllowedIPs маршрут по умолчанию: именно
// он утаскивает в туннель собственные соединения ядра.
func coversEverything(allowed []string) bool {
	for _, a := range allowed {
		switch strings.TrimSpace(a) {
		case "0.0.0.0/0", "::/0":
			return true
		}
	}
	return false
}
