// browser_linux.go — капча в браузере под управлением по CDP (Linux).
//
// WebView2 на Linux нет, а встроенный движок (WebKitGTK, Qt WebEngine, CEF)
// требует cgo и системных библиотек. Поэтому qwdtt-cli запускает установленный
// браузер на Chromium со своим временным профилем, открывает в нём страницу
// капчи и забирает токен по протоколу DevTools (cdp.go). Человек только
// решает капчу — как в окне WebView2 на Windows.
//
// Управление — по трубе (--remote-debugging-pipe), дескрипторы 3 и 4 через
// ExtraFiles: порта нет, и чужой процесс к браузеру не подключится. Нет
// и чтения DevToolsActivePort из профиля — а у Chromium из snap свой /tmp,
// и профиль, созданный нами, он бы там не увидел.
package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/sudo"
)

// ErrNoBrowser — браузера на Chromium нет. CLI по нему берёт ручной режим.
var ErrNoBrowser = errors.New("браузер на Chromium не найден")

// browserCandidates — браузеры на Chromium в порядке предпочтения: имена в
// PATH, затем пути, которых в PATH под sudo может не оказаться. Firefox по
// CDP больше не управляется, его здесь нет.
var browserCandidates = []string{
	"google-chrome-stable",
	"google-chrome",
	"chromium",
	"chromium-browser",
	"yandex-browser-stable",
	"yandex-browser",
	"microsoft-edge-stable",
	"microsoft-edge",
	"brave-browser",
	"vivaldi-stable",
	"vivaldi",
	"/snap/bin/chromium",
}

const (
	// browserStartup — сколько ждать, пока браузер начнёт открывать страницу.
	// Холодный старт с чистым профилем — одна-три секунды.
	browserStartup = 20 * time.Second
	// browserStop — сколько ждать выхода браузера после Browser.close,
	// прежде чем убить его группу процессов.
	browserStop = 5 * time.Second
)

// BrowserOptions — настройки решателя с браузером.
type BrowserOptions struct {
	// Out — куда уходит сообщение, что браузер открыт. nil — os.Stderr.
	Out io.Writer

	// Dial, если задан, — браузер ходит в сеть только через локальный прокси
	// qwdtt-cli (proxy.go), а он соединяется этой функцией: при kill-switch
	// страница грузится сокетами qwdtt-cli. Прокси не поднялся — Solve
	// отказывает с ErrProxy, браузера не запуская.
	Dial DialFunc

	// Logf — строки прокси и разговора с браузером; nil — молчать.
	Logf func(format string, args ...any)
}

// Browser — решатель в браузере под управлением.
type Browser struct {
	opts BrowserOptions
	exe  string

	// Только для тестов: extraArgs дописываются к командной строке браузера,
	// started зовётся с PID запущенного браузера.
	extraArgs []string
	started   func(pid int)
}

// NewBrowser находит браузер на Chromium и собирает решатель. Браузер до
// первой капчи не запускается. Ошибка — ErrNoBrowser.
func NewBrowser(opts BrowserOptions) (*Browser, error) {
	exe, err := findBrowser(exec.LookPath)
	if err != nil {
		return nil, err
	}
	if opts.Out == nil {
		opts.Out = os.Stderr
	}
	sweepStaleProfiles()
	return &Browser{opts: opts, exe: exe}, nil
}

// findBrowser — первый найденный из browserCandidates.
func findBrowser(lookPath func(string) (string, error)) (string, error) {
	for _, name := range browserCandidates {
		if path, err := lookPath(name); err == nil {
			return path, nil
		}
	}
	return "", ErrNoBrowser
}

// NeedsHuman: да — решает человек, браузер только показывает страницу.
func (*Browser) NeedsHuman() bool { return true }

// browserArgs — командная строка браузера.
//
// navigator.webdriver, по которому автоматику видно странице, выставляют
// --enable-automation, --headless и — проверено живьём — любое
// управление по DevTools, и трубой, и портом. Первых двух здесь нет (окно к
// тому же нужно человеку), а признак от трубы снимает --disable-blink-features=
// AutomationControlled: капчу решает человек, и странице браузер должен
// казаться обычным, как окно WebView2. Emulation.setAutomationOverride
// признак только включает, выключить им нельзя.
//
// --test-type убирает плашку «Вы используете неподдерживаемый флаг командной
// строки», которую Chromium показывает на --disable-blink-features; так же
// делает ChromeDriver. Странице этот флаг не виден.
//
// --disable-background-networking — чтобы свой экземпляр не ходил за
// обновлениями и подсказками: через прокси капчи им всё равно отказ.
func browserArgs(profile, proxy string) []string {
	args := []string{
		"--remote-debugging-pipe",
		"--disable-blink-features=AutomationControlled",
		"--test-type",
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--window-size=520,760",
	}
	if proxy != "" {
		args = append(args, "--proxy-server=http://"+proxy)
	}
	return append(args, "about:blank")
}

// Solve запускает браузер со страницей капчи и ждёт токена.
//
// Возвращается, как только исход известен: браузер закрывается и профиль
// убирается уже без нас. Кроме отмены ctx — тогда уборка дожидается, чтобы
// окно не пережило qwdtt-cli.
func (b *Browser) Solve(ctx context.Context, req Request) (string, error) {
	var proxy *Proxy
	if b.opts.Dial != nil {
		var err error
		if proxy, err = StartProxy(b.opts.Dial, b.opts.Logf); err != nil {
			return "", err
		}
		b.logf("[CAPTCHA] браузер капчи идёт в сеть через прокси qwdtt-cli %s", proxy.Addr())
	}
	closeProxy := func() {
		if proxy != nil {
			proxy.Close()
		}
	}

	profile, err := newProfile()
	if err != nil {
		closeProxy()
		return "", fmt.Errorf("%w: каталог профиля: %v", ErrBrowser, err)
	}

	var proxyAddr string
	if proxy != nil {
		proxyAddr = proxy.Addr()
	}
	browser, err := b.start(profile, proxyAddr)
	if err != nil {
		closeProxy()
		removeProfile(profile)
		return "", fmt.Errorf("%w: %v", ErrBrowser, err)
	}

	fmt.Fprintf(b.opts.Out, "\nVK просит решить капчу — открыто окно браузера %s, время на это %s.\n",
		filepath.Base(b.exe), req.Left(time.Now()).Round(time.Second))

	token, err := captureToken(ctx, browser.out, browser.in, req.RedirectURI, browserStartup, b.opts.Logf)

	cleanup := func() {
		browser.stop()
		closeProxy()
		removeProfile(profile)
	}
	if ctx.Err() != nil {
		cleanup()
	} else {
		go cleanup()
	}
	return token, err
}

// runningBrowser — запущенный браузер и концы трубы к нему.
type runningBrowser struct {
	cmd    *exec.Cmd
	in     *os.File // пишем мы, читает браузер (его дескриптор 3)
	out    *os.File // пишет браузер (дескриптор 4), читаем мы
	exited chan struct{}
}

// start запускает браузер. Под sudo — от имени вызвавшего пользователя с
// окружением его сеанса (prepareCommand), в своей группе процессов: Ctrl+C
// в терминале её не задевает, а при уборке её можно убить целиком.
func (b *Browser) start(profile, proxy string) (*runningBrowser, error) {
	cmdR, cmdW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	resR, resW, err := os.Pipe()
	if err != nil {
		cmdR.Close()
		cmdW.Close()
		return nil, err
	}

	args := browserArgs(profile, proxy)
	if len(b.extraArgs) > 0 {
		// Перед about:blank: после адреса Chromium флагов уже не ждёт.
		args = append(append(args[:len(args)-1:len(args)-1], b.extraArgs...), args[len(args)-1])
	}
	cmd := exec.Command(b.exe, args...)
	cmd.ExtraFiles = []*os.File{cmdR, resW}
	err = prepareCommand(cmd)
	if err == nil {
		err = cmd.Start()
	}
	// Концы браузера у нас больше не нужны: без этого чтение не увидело бы
	// конца трубы, когда браузер выйдет.
	cmdR.Close()
	resW.Close()
	if err != nil {
		cmdW.Close()
		resR.Close()
		return nil, err
	}

	if b.started != nil {
		b.started(cmd.Process.Pid)
	}
	r := &runningBrowser{cmd: cmd, in: cmdW, out: resR, exited: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(r.exited)
	}()
	return r, nil
}

// stop закрывает браузер: просит сам (Browser.close), не вышел — убивает
// его группу процессов.
func (r *runningBrowser) stop() {
	// Срок записи — на случай зависшего браузера, который трубу не читает.
	closeCmd, _ := json.Marshal(cdpCommand{ID: 1 << 30, Method: "Browser.close"})
	_ = r.in.SetWriteDeadline(time.Now().Add(browserStop))
	_, _ = r.in.Write(append(closeCmd, 0))

	select {
	case <-r.exited:
	case <-time.After(browserStop):
		_ = syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL)
		<-r.exited
	}
	r.in.Close()
	r.out.Close()
}

// newProfile заводит пустой временный профиль. Под sudo он отдаётся
// вызвавшему пользователю: браузер работает от его имени, и в каталог root'а
// писать не смог бы.
func newProfile() (string, error) {
	dir, err := os.MkdirTemp("", profilePrefix+"*")
	if err != nil {
		return "", err
	}
	u, err := sudo.Invoker()
	if err == nil && u != nil {
		err = os.Chown(dir, int(u.Uid), int(u.Gid))
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func (b *Browser) logf(format string, args ...any) {
	if b.opts.Logf != nil {
		b.opts.Logf(format, args...)
	}
}
