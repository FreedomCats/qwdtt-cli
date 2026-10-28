//go:build windows

// webview2_windows.go — окно капчи на WebView2 (задача E3).
//
// Перенос того, что делал Android: своё окно со своим движком и перехватчик
// JS (interceptor.go), который отдаёт в Go ответы API страницы капчи; их
// разбирает pageWatch (page.go), тот же, что у браузера по CDP. От
// пользователя остаётся одно действие — решить капчу; токен уходит ядру сам,
// консоль не участвует. Проект и разбор подводных камней — doc/e2-captcha.md
// §3. На selected окно сначала прячется за край экрана и пробует пройти
// капчу само (autoclick.go, hiddenwindow_windows.go,
// webview2_devtools_windows.go); перенесено из csqtt-cli.
//
// Библиотека — github.com/jchv/go-webview2: без cgo, загрузчик WebView2Loader.dll
// встроен. Три её особенности определили устройство решателя, и в эскизе §3
// они учтены не были:
//
//   - Terminate() — это PostQuitMessage, то есть выход для очереди вызывающей
//     нити. Из чужой горутины он окно не закрывает. Закрываем через Destroy():
//     это PostMessage(WM_CLOSE) самому окну, и его можно звать откуда угодно —
//     дальше окно разрушается и выходит из Run() штатным путём, тем же, что
//     при нажатии на крестик.
//   - COM библиотека инициализирует в init() пакета, то есть на той нити,
//     где этот init выполнился. Нашей нити это не касается: WebView2 требует
//     однопоточный апартамент, и заводить его приходится самим.
//   - GetInstalledVersion без Runtime возвращает пустую версию без ошибки.
package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/webviewloader"
	"golang.org/x/sys/windows"
)

// bindingName — глобальная функция, через которую перехватчик отдаёт ответы
// API в Go. Заводится Bind.
const bindingName = "__qwdttCliCaptchaReply"

// WebView2Options — настройки решателя с окном.
type WebView2Options struct {
	// Out — куда уходит сообщение, что окно открыто. nil — os.Stderr.
	// Как у консольного решателя, это stderr: stdout у CLI занят результатом.
	Out io.Writer

	// Dial, если задан, — окно ходит в сеть только через локальный прокси
	// qwdtt-cli (proxy.go), а он соединяется этой функцией. Так при kill-switch
	// страница грузится сокетами qwdtt-cli, которые он пропускает (plan-e3.md, W2).
	// Прокси не поднялся — Solve отказывает с ErrProxy, окна не открыв.
	Dial DialFunc

	// Logf — строки прокси (отказы), страницы капчи (pageWatch) и автоклика;
	// nil — молчать.
	Logf func(format string, args ...any)
}

// browserArgsEnv — переменная окружения с аргументами браузера WebView2.
//
// Прокси окну передаётся опцией окружения AdditionalBrowserArguments
// (ICoreWebView2EnvironmentOptions; в go-webview2 её нет — она в форке, см.
// replace в go.mod). Одной переменной мало: рантайм 155 со встроенным
// загрузчиком go-webview2 её не применяет вовсе — окно шло мимо прокси
// (R2 Э3). Но документация обещает, что переменная, если её применят, важнее
// опции, поэтому на время создания окна в ней то же значение, что в опции:
// какой путь ни сработает, прокси один. Своё значение пользователя
// сохраняется в обоих. Действует только на это окно: у окна свой профиль,
// значит, и свой экземпляр msedgewebview2.exe.
const browserArgsEnv = "WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS"

// WebView2 — решатель с окном WebView2.
type WebView2 struct {
	opts WebView2Options

	// opened — зовётся с ручкой окна, как только оно создано. Только для
	// тестов: через неё они закрывают окно так же, как крестик.
	opened func(webview2.WebView)
	// Только для тестов автоклика: hidden получает скрытое окно сразу после
	// создания; revealed зовётся на нити окна, когда оно показано человеку;
	// quietReveal — показать его без переднего плана и без активации, чтобы
	// не отнимать фокус у того, кто за машиной; autoTiming — свои сроки
	// автофазы вместо defaultAutoTiming.
	hidden      func(*hiddenWindow)
	revealed    func(webview2.WebView)
	quietReveal bool
	autoTiming  *autoTiming
}

// NewWebView2 проверяет, что WebView2 Runtime установлен, и собирает решатель.
// Окно до первой капчи не открывается.
//
// Ошибка означает, что окна не будет; CLI в этом случае берёт консольный
// решатель.
func NewWebView2(opts WebView2Options) (*WebView2, error) {
	version, err := webviewloader.GetInstalledVersion()
	if err != nil {
		return nil, fmt.Errorf("WebView2 Runtime не проверить: %w", err)
	}
	if version == "" {
		return nil, errors.New("WebView2 Runtime не установлен")
	}
	if opts.Out == nil {
		opts.Out = os.Stderr
	}

	sweepStaleProfiles()
	return &WebView2{opts: opts}, nil
}

// NeedsHuman: да — окно может уйти человеку, и срок нужен человеческий, даже
// когда капчу сначала пробует автоклик.
func (*WebView2) NeedsHuman() bool { return true }

// Solve открывает окно со страницей капчи и ждёт токена из перехватчика.
//
// На selected окно создаётся за краем экрана и сначала пробует пройти капчу
// само (autoclick.go); показывается человеку, только если не вышло. На
// manual — сразу человеку: эту просьбу ядро шлёт только в цепочке auto,
// которой оболочка не включает (solver.go).
//
// Возвращается сразу, как только исход известен: токен ядру нужен сейчас,
// а окно закрывается и профиль убирается уже без нас.
func (s *WebView2) Solve(ctx context.Context, req Request) (string, error) {
	var proxy *Proxy
	if s.opts.Dial != nil {
		var err error
		if proxy, err = StartProxy(s.opts.Dial, s.opts.Logf); err != nil {
			return "", err
		}
		if s.opts.Logf != nil {
			s.opts.Logf("[CAPTCHA] окно капчи идёт в сеть через прокси qwdtt-cli %s", proxy.Addr())
		}
	}

	profile, err := os.MkdirTemp("", profilePrefix+"*")
	if err != nil {
		if proxy != nil {
			proxy.Close()
		}
		return "", fmt.Errorf("каталог профиля WebView2: %w", err)
	}

	result := make(chan outcome, 1)
	report := func(o outcome) {
		select {
		case result <- o:
		default:
		}
	}

	win := &windowHandle{}
	done := make(chan struct{})

	// Автофаза: её ctx кончается вместе с Solve, а начинается она, когда окно
	// создано и страница открывается (started).
	actx, stopAuto := context.WithCancel(ctx)
	defer stopAuto()
	var auto *autoRun
	var hidden *hiddenWindow // пишется и читается на нити окна
	checks := make(chan checkReply, 8)
	started := make(chan *webviewDriver, 1)
	autoDone := make(chan struct{})
	if req.Mode == ModeSelected {
		auto = newAutoRun(s.opts.Out, s.opts.Logf, req, func() {
			win.dispatch(func(w webview2.WebView) {
				if hidden != nil {
					hidden.reveal()
				}
				if s.revealed != nil {
					s.revealed(w)
				}
			})
		})
		if s.autoTiming != nil {
			auto.timing = *s.autoTiming
		}
	} else {
		close(autoDone)
	}

	go func() {
		// Окно, его насос сообщений и COM живут на одной прибитой нити.
		// Отпускать её не будем: горутина кончится, и нить уйдёт вместе с ней,
		// а с нитью — её апартамент COM и всё, что WebView2 на ней оставил.
		runtime.LockOSThread()
		defer close(done)

		if err := initCOM(); err != nil {
			report(outcome{err: err})
			return
		}
		// До перехватчика скрытого окна: место за краем экрана он берёт в
		// физических пикселях (dpi_windows.go).
		scale := dpiAwareThread()

		var extra []string
		if proxy != nil {
			extra = append(extra, "--proxy-server=http://"+proxy.Addr())
		}
		if auto != nil {
			var err error
			if hidden, err = hideNextWindow(s.quietReveal); err != nil {
				// Спрятать не вышло — окно сразу человеку, как на manual.
				s.logf("[CAPTCHA] окно капчи не спрятать: %v", err)
			} else {
				defer hidden.unhook()
				extra = append(extra, hiddenBrowserArgs)
				// Размер — под монитор, за которым окно будет числиться за
				// краем; при показе reveal пересчитает его под основной.
				scale = hidden.scale(scale)
			}
		}
		var browserArgs string
		var restoreArgs func()
		if len(extra) > 0 {
			browserArgs, restoreArgs = setBrowserArgs(strings.Join(extra, " "))
		}
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:                   profile,
			AdditionalBrowserArguments: browserArgs,
			WindowOptions: webview2.WindowOptions{
				Title:  windowTitle(req),
				Width:  scaledSize(500, scale),
				Height: scaledSize(680, scale),
				Center: true,
			},
		})
		// Браузер окна к этому моменту запущен со своей строкой: переменная
		// больше не нужна, и детям qwdtt-cli её видеть незачем.
		if restoreArgs != nil {
			restoreArgs()
		}
		if w == nil {
			report(outcome{err: errors.New("окно WebView2 не открылось")})
			return
		}
		win.set(w)
		defer win.clear()
		var driver *webviewDriver
		if hidden != nil {
			if s.hidden != nil {
				s.hidden(hidden)
			}
			// Перехватчик не узнал окно (в другой версии форка у него может быть
			// другой класс): оно уже на экране, и автоклик в нём шёл бы на виду.
			if hidden.hwnd == 0 {
				s.logf("[CAPTCHA] автоклик: окно не спряталось — перехватчик его не узнал")
			} else if dt, err := newDevtools(w); err != nil {
				s.logf("[CAPTCHA] автоклик: нет DevTools окна: %v", err)
			} else {
				defer dt.release()
				driver = &webviewDriver{win: win, dt: dt}
			}
		}

		// Bind и перехватчик ставятся до Navigate: оба — скрипты, которые
		// WebView2 выполняет на каждой новой странице до её собственного кода.
		// Приёмник перехватчик ищет лениво, при вызове, поэтому порядок двух
		// скриптов между собой не важен.
		watch := pageWatch{logf: s.opts.Logf}
		if auto != nil {
			watch.checked = func(r checkReply) {
				select {
				case checks <- r:
				default:
				}
			}
			watch.auto = auto.active
		}
		_ = w.Bind(bindingName, func(method, body string) {
			// Ответ без исхода (слайдер, BOT, initSession) — окно остаётся
			// открытым: человек решает дальше, токен придёт следующим check.
			// Так же и на Android: ручное окно слайдер не ловило вовсе
			// (ManlCaptchaWebViewManager.kt).
			if out := watch.observe(method, []byte(body)); out != nil {
				report(*out)
				w.Destroy()
			}
		})
		w.Init(pageInterceptorJS(bindingName))
		w.Navigate(req.RedirectURI)

		if s.opened != nil {
			s.opened(w)
		}
		if auto != nil {
			started <- driver
		}
		w.Run()
	}()

	// Профиль убирается, когда окно закрыто, и не раньше: до этого его файлы
	// держит msedgewebview2.exe. Прокси — тогда же: окну он нужен, пока оно
	// открыто, а не пока ждём токен.
	go func() {
		<-done
		if proxy != nil {
			proxy.Close()
		}
		removeProfile(profile)
	}()

	stop := context.AfterFunc(ctx, win.close)
	defer stop()

	if auto != nil {
		auto.announce()
		go func() {
			defer close(autoDone)
			var driver *webviewDriver
			select {
			case driver = <-started:
			case <-actx.Done():
				return
			}
			if driver == nil {
				// Окно не спряталось или без DevTools: сразу человеку.
				auto.fail(errAutoUnavailable)
				return
			}
			auto.run(actx, driver, checks)
		}()
	} else {
		fmt.Fprintf(s.opts.Out, "\nVK просит решить капчу — открыто окно, время на это %s.\n%s\n",
			req.Left(time.Now()).Round(time.Second), humanHint)
	}
	// endAuto кончает автофазу раньше, чем окно: автоклик, получивший «окно
	// закрыто» при живом ctx, счёл бы это неудачей и сказал бы о ней
	// человеку, хотя исход уже есть.
	endAuto := func() {
		stopAuto()
		<-autoDone
	}

	select {
	case got := <-result:
		endAuto()
		win.close()
		if got.err == nil && strings.TrimSpace(got.token) == "" {
			got.err = ErrNoToken
		}
		if got.err == nil && auto != nil {
			auto.solved()
		}
		return got.token, got.err
	case <-done:
		endAuto()
		// Токен и окно могли кончиться одновременно: токен важнее.
		select {
		case got := <-result:
			return got.token, got.err
		default:
		}
		return "", ErrWindowClosed
	case <-ctx.Done():
		endAuto()
		return "", ctx.Err()
	}
}

// setBrowserArgs добавляет args к аргументам браузера WebView2 в переменной
// browserArgsEnv и возвращает итоговую строку — её же окно получает опцией —
// и чем вернуть прежнее значение. Своё значение пользователя сохраняется:
// наши аргументы идут после, а из повторов Chromium берёт последний.
func setBrowserArgs(args string) (value string, restore func()) {
	before, had := os.LookupEnv(browserArgsEnv)
	value = args
	if strings.TrimSpace(before) != "" {
		value = before + " " + args
	}
	_ = os.Setenv(browserArgsEnv, value)
	return value, func() {
		if had {
			_ = os.Setenv(browserArgsEnv, before)
		} else {
			_ = os.Unsetenv(browserArgsEnv)
		}
	}
}

// windowHandle — ручка окна для тех, кто живёт не на его нити: отмены по ctx
// и закрытия после токена. Окно может ещё не появиться, когда его просят
// закрыть, — тогда оно закроется сразу, как появится.
type windowHandle struct {
	mu     sync.Mutex
	w      webview2.WebView
	closed bool
}

func (h *windowHandle) set(w webview2.WebView) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.w = w
	if h.closed {
		w.Destroy()
	}
}

// clear забывает окно, когда оно уже разрушено: слать WM_CLOSE мёртвому окну
// бесполезно, а его номер система может отдать другому.
func (h *windowHandle) clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.w = nil
}

// close закрывает окно с любой нити. Повторный вызов безвреден.
func (h *windowHandle) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	if h.w != nil {
		h.w.Destroy()
	}
}

// dispatch выполняет f на нити окна, если окно ещё живо. Для тех, кто живёт
// на чужой нити: автофазы и показа окна человеку. false — окна уже нет, f не
// позовётся.
func (h *windowHandle) dispatch(f func(w webview2.WebView)) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.w == nil {
		return false
	}
	w := h.w
	w.Dispatch(func() { f(w) })
	return true
}

// initCOM заводит однопоточный апартамент COM на текущей нити. S_FALSE —
// апартамент уже есть, и это не ошибка.
func initCOM() error {
	err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED)
	if err == nil || errors.Is(err, windows.Errno(1)) { // S_FALSE
		return nil
	}
	return fmt.Errorf("COM для окна WebView2: %w", err)
}

// windowTitle — заголовок окна. Срок в нём — время на часах, а не обратный
// отсчёт: так он верен, не перерисовываясь.
func windowTitle(req Request) string {
	if req.Deadline.IsZero() {
		return "Капча VK — qwdtt-cli"
	}
	return "Капча VK — ответить до " + req.Deadline.Format("15:04:05")
}

// webviewDriver — страница окна WebView2 для автоклика: методы DevTools
// зовутся на нити окна, ответ ждётся на своей.
type webviewDriver struct {
	win *windowHandle
	dt  *devtools
}

func (d *webviewDriver) devtools(ctx context.Context, method string, params any) (json.RawMessage, error) {
	reply := make(chan devtoolsReply, 1)
	ok := d.win.dispatch(func(webview2.WebView) {
		if err := d.dt.call(method, params, func(r devtoolsReply) { reply <- r }); err != nil {
			reply <- devtoolsReply{err: err}
		}
	})
	if !ok {
		return nil, ErrWindowClosed
	}
	select {
	case r := <-reply:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *WebView2) logf(format string, args ...any) {
	if s.opts.Logf != nil {
		s.opts.Logf(format, args...)
	}
}
