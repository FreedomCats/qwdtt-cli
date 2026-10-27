// browser_linux.go — капча в браузере под управлением по CDP (Linux).
//
// WebView2 на Linux нет, а встроенный движок (WebKitGTK, Qt WebEngine, CEF)
// требует cgo и системных библиотек. Поэтому qwdtt-cli запускает установленный
// браузер на Chromium со своим временным профилем, открывает в нём страницу
// капчи и забирает токен по протоколу DevTools (cdp.go). На selected окно
// сначала прячется за край экрана и пробует пройти капчу само — автоклик
// (autoclick.go, autoclick_cdp.go; не спрятать — пробует на виду); человек
// решает капчу в том же окне, если автоклик не прошёл, и сразу — на manual.
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
	"sync"
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
	// browserStartup — сколько ждать, пока браузер начнёт открывать страницу:
	// до отправки Page.navigate. Загрузку самой страницы он не ограничивает.
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

	// Logf — строки прокси, разговора с браузером, страницы капчи
	// (pageWatch) и автоклика; nil — молчать.
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
	// Только для тестов автоклика: hidden получает страницу скрытого окна,
	// как только вкладка подготовлена; revealed — после того, как окно
	// выведено человеку; quietReveal — выводить без переднего плана и
	// фокуса; autoTiming — свои сроки автофазы; wayland заменяет
	// waylandSession.
	hidden      func(autoDriver)
	revealed    func(autoDriver)
	quietReveal bool
	autoTiming  *autoTiming
	wayland     func() bool
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
//
// --disable-extensions — расширения, которые система ставит всем профилям
// браузера (политики, каталог внешних расширений дистрибутива), в пустой
// профиль приходят всё равно. В csqtt-cli живая запись поймала такое:
// расширение перезагружало страницу капчи и меняло User-Agent.
//
// --password-store=basic — без него Chromium на Linux лезет в связку ключей
// сеанса и может спросить у человека её пароль поверх капчи; паролей в
// профиле капчи нет и не будет.
func browserArgs(profile, proxy string) []string {
	args := []string{
		"--remote-debugging-pipe",
		"--disable-blink-features=AutomationControlled",
		"--test-type",
		"--user-data-dir=" + profile,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-background-networking",
		"--disable-extensions",
		"--password-store=basic",
		"--window-size=520,760",
	}
	if proxy != "" {
		args = append(args, "--proxy-server=http://"+proxy)
	}
	return append(args, "about:blank")
}

// insertArgs дописывает extra к командной строке browserArgs — перед
// about:blank: после адреса Chromium флагов уже не ждёт.
func insertArgs(args []string, extra ...string) []string {
	if len(extra) == 0 {
		return args
	}
	return append(append(args[:len(args)-1:len(args)-1], extra...), args[len(args)-1])
}

// autoWindowArgs — браузер окна с автокликом. Окна при запуске нет: окно
// капчи откроет в фоне, без фокуса, Target.createTarget (cdpCapture.hidden).
// Окно при запуске (about:blank) Chromium активирует, а с этим флагом
// адрес в командной строке он не открывает вовсе.
//
// Окно за краем экрана Chromium на X11 считает перекрытым: страница —
// hidden, кадры не рисуются (проверено на стенде в csqtt-cli).
// --disable-backgrounding-occluded-windows это снимает;
// --disable-renderer-backgrounding — страховка. Окну на виду (Wayland) они
// не мешают.
var autoWindowArgs = []string{
	"--no-startup-window",
	"--disable-backgrounding-occluded-windows",
	"--disable-renderer-backgrounding",
}

// offscreenArg ставит новые окна за нижний край экрана. Не влево и не
// вверх: отрицательные координаты оконный менеджер может заменить нулём
// (так делает, например, Mutter), и окно окажется на экране, а за нижний
// край окно обычно пускают. Не пустили — окно на экране, автоклик это видит
// (windowHidden). 10000 — ниже любого разумного набора мониторов и в
// пределах 16-битных координат X11 и при масштабе 3.
const offscreenArg = "--window-position=0,10000"

// waylandSession — сеанс Wayland: браузер (Chromium 140 и новее сам выбирает
// Wayland, если он есть) не может поставить окно за край экрана — у клиента
// Wayland нет координат на экране, место окну выбирает композитор. Автоклик
// там идёт в видимом окне. Окружение браузера под sudo — то же, что у
// qwdtt-cli (sudo.SessionEnv WAYLAND_DISPLAY не трогает): нет её здесь — нет
// и у браузера, и он идёт через XWayland, где окно прячется как на X11.
func waylandSession() bool { return os.Getenv("WAYLAND_DISPLAY") != "" }

// Solve запускает браузер со страницей капчи и ждёт токена.
//
// На selected окно открывается в фоне за краем экрана и сначала пробует
// пройти капчу само (autoclick.go); человеку выводится, только если не
// вышло. В сеансе Wayland и когда оконный менеджер прижал окно к экрану,
// автоклик идёт в видимом окне. На manual окно сразу у человека: эту просьбу
// ядро шлёт только в цепочке auto, которой оболочка не включает (solver.go),
// после попыток решателя ядра на той же сессии капчи.
//
// Возвращается, как только исход известен: браузер закрывается и профиль
// убирается уже без нас. Кроме отмены ctx — тогда уборка дожидается, чтобы
// окно не пережило qwdtt-cli: и посреди автофазы, и при окне у человека.
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
	auto := req.Mode == ModeSelected
	visible := auto && b.waylandSession()
	if visible {
		b.logf("[CAPTCHA] сеанс Wayland: окно браузера за край экрана не спрятать — автоклик в видимом окне")
	}
	args := browserArgs(profile, proxyAddr)
	if auto {
		args = insertArgs(args, autoWindowArgs...)
		if !visible {
			args = insertArgs(args, offscreenArg)
		}
	}
	browser, err := b.start(args)
	if err != nil {
		closeProxy()
		removeProfile(profile)
		return "", fmt.Errorf("%w: %v", ErrBrowser, err)
	}

	capture := newCDPCapture(req.RedirectURI, b.opts.Logf)
	var finish func(token string, err error)
	if auto {
		finish = b.startAuto(ctx, req, capture, visible)
	} else {
		fmt.Fprintf(b.opts.Out, "\nVK просит решить капчу — открыто окно браузера %s, время на это %s.\n",
			filepath.Base(b.exe), req.Left(time.Now()).Round(time.Second))
	}

	token, err := runCapture(ctx, browser.out, browser.in, capture, browserStartup)
	if finish != nil {
		finish(token, err)
	}

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

// startAuto готовит автофазу окна capture: окно — в фоне за краем экрана
// (visible — на экране: Wayland), команды автоклика — через разговор с
// браузером. Автофаза начинается, когда вкладка подготовлена; тогда же —
// строка о ней: спряталось ли окно, известно только к этому времени.
// finish зовётся с исходом разговора: автофаза кончается, и, если токен
// получен без человека, — строка об этом.
func (b *Browser) startAuto(ctx context.Context, req Request, capture *cdpCapture, visible bool) (finish func(string, error)) {
	calls := make(chan cdpCall)
	done := make(chan struct{})
	ready := make(chan struct{})
	checks := make(chan checkReply, 8)
	capture.hidden = true
	capture.calls = calls
	capture.onReady = sync.OnceFunc(func() { close(ready) })
	capture.watch.checked = func(r checkReply) {
		select {
		case checks <- r:
		default:
		}
	}
	driver := &cdpDriver{calls: calls, done: done}

	actx, stopAuto := context.WithCancel(ctx)
	auto := newAutoRun(b.opts.Out, b.opts.Logf, req, func() {
		rctx, cancel := context.WithTimeout(actx, revealTimeout)
		defer cancel()
		if err := revealWindow(rctx, driver, !b.quietReveal, b.logf); err != nil {
			b.logf("[CAPTCHA] окно капчи не вывести на экран: %v", err)
		}
		if b.revealed != nil {
			b.revealed(driver)
		}
	})
	if b.autoTiming != nil {
		auto.timing = *b.autoTiming
	}
	capture.watch.auto = auto.active

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-ready:
		case <-actx.Done():
			return
		}
		if b.hidden != nil {
			b.hidden(driver)
		}
		// Оконный менеджер мог прижать окно к экрану: тогда автоклик идёт у
		// человека на глазах, и строка просит окно не трогать.
		if !visible {
			switch g, hidden, err := windowHidden(actx, driver, auto.timing.poll); {
			case actx.Err() != nil:
				return
			case err != nil:
				b.logf("[CAPTCHA] автоклик: не узнать, спряталось ли окно: %v", err)
			case !hidden:
				b.logf("[CAPTCHA] автоклик: окно не спряталось за край экрана (%.0f,%.0f %.0f×%.0f) — "+
					"автоклик в видимом окне", g.window.Left, g.window.Top, g.window.Width, g.window.Height)
				visible = true
			}
		}
		if visible {
			auto.announceVisible()
		} else {
			auto.announce()
		}
		auto.run(actx, driver, checks)
	}()

	return func(token string, err error) {
		// Сначала отмена, потом done: автоклик, получивший «окно закрыто» при
		// живом ctx, счёл бы это неудачей и сказал бы о ней человеку.
		stopAuto()
		close(done)
		wg.Wait()
		if err == nil && token != "" {
			auto.solved()
		}
	}
}

// waylandSession — сеанс Wayland (b.wayland у тестов).
func (b *Browser) waylandSession() bool {
	if b.wayland != nil {
		return b.wayland()
	}
	return waylandSession()
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
func (b *Browser) start(args []string) (*runningBrowser, error) {
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

	cmd := exec.Command(b.exe, insertArgs(args, b.extraArgs...)...)
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
	r.killGroup()
	r.in.Close()
	r.out.Close()
}

// killGroup добивает процессы браузера, которые пережили главный: он вышел, а
// его дочерние (сеть, отрисовка) в той же группе ещё доделывают своё. В
// csqtt-cli на занятой машине такой процесс после закрытия окна успевал
// записать кэш и создать каталог профиля заново — уже после того, как
// профиль убрали, и профиль оставался во временном каталоге. Профиль убирают
// после stop, поэтому stop ждёт, пока в группе никого не останется.
func (r *runningBrowser) killGroup() {
	pgid := r.cmd.Process.Pid
	if syscall.Kill(-pgid, syscall.SIGKILL) != nil {
		return // в группе никого
	}
	for deadline := time.Now().Add(browserStop); time.Now().Before(deadline); {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
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
