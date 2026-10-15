//go:build windows

// webview2_windows.go — окно капчи на WebView2 (задача E3).
//
// Точный перенос того, что делал Android: своё окно со своим движком, тот же
// перехватчик JS из token.go. От пользователя остаётся одно действие — решить
// капчу; токен уходит ядру сам, консоль не участвует. Проект и разбор
// подводных камней — doc/e2-captcha.md §3.
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

// bindingName — глобальная функция, через которую перехватчик отдаёт
// результат в Go. Заводится Bind.
const bindingName = "__qwdttCliCaptchaResult"

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

	// Logf — строки прокси (отказы); nil — молчать.
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

// NeedsHuman: да — решает человек, окно только показывает страницу.
func (*WebView2) NeedsHuman() bool { return true }

// Solve открывает окно со страницей капчи и ждёт токена из перехватчика.
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

		var browserArgs string
		var restoreArgs func()
		if proxy != nil {
			browserArgs, restoreArgs = setBrowserArgs("--proxy-server=http://" + proxy.Addr())
		}
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:                   profile,
			AdditionalBrowserArguments: browserArgs,
			WindowOptions: webview2.WindowOptions{
				Title:  windowTitle(req),
				Width:  500,
				Height: 680,
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

		// Bind и перехватчик ставятся до Navigate: оба — скрипты, которые
		// WebView2 выполняет на каждой новой странице до её собственного кода.
		// Приёмник перехватчик ищет лениво, при вызове, поэтому порядок двух
		// скриптов между собой не важен.
		_ = w.Bind(bindingName, func(kind, value string) {
			switch kind {
			case "token":
				report(outcome{token: value})
			case "error":
				report(outcome{err: fmt.Errorf("VK отказал: %s", value)})
			default:
				// "slider": VK показал слайдер вместо чекбокса. Человек
				// решит и его — окно остаётся открытым, токен придёт
				// следующим ответом. Так же и на Android: ручное окно
				// слайдер не ловило вовсе (ManlCaptchaWebViewManager.kt).
				return
			}
			w.Destroy()
		})
		w.Init(fmt.Sprintf(interceptorJS, "function(k,v){return window."+bindingName+"(k,v);}"))
		w.Navigate(req.RedirectURI)

		if s.opened != nil {
			s.opened(w)
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

	fmt.Fprintf(s.opts.Out, "\nVK просит решить капчу — открыто окно, время на это %s.\n"+
		"Если окна не видно, оно может оказаться за консолью: поищите его на панели задач.\n",
		req.Left(time.Now()).Round(time.Second))

	select {
	case got := <-result:
		win.close()
		if got.err == nil && strings.TrimSpace(got.token) == "" {
			got.err = ErrNoToken
		}
		return got.token, got.err
	case <-done:
		// Токен и окно могли кончиться одновременно: токен важнее.
		select {
		case got := <-result:
			return got.token, got.err
		default:
		}
		return "", ErrWindowClosed
	case <-ctx.Done():
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
