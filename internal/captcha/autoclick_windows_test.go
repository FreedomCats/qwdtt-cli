//go:build windows

package captcha

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	webview2 "github.com/jchv/go-webview2"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// Автоклик в окне WebView2 на стенде (перенесён из csqtt-cli; QWDTT_WEBVIEW2=1,
// как и весь стенд окна). Окно с автокликом стоит за краем экрана; тесты, где
// оно выходит человеку, показывают его без переднего плана (quietReveal) —
// фокус у того, кто за машиной, не пропадает. Страница стенда —
// autostand_test.go.
//
//	QWDTT_WEBVIEW2=1 go test ./internal/captcha -run 'WebView2Auto|WebView2DPI' -v

// watchHidden следит, чтобы окно создалось за краем экрана и без кнопки на
// панели задач.
func watchHidden(t *testing.T, s *WebView2) {
	s.hidden = func(hw *hiddenWindow) {
		hiddenMu.Lock()
		where := hw.where
		hiddenMu.Unlock()
		if hw.hwnd == 0 || onScreen(where) || onScreen(windowRect(hw.hwnd)) {
			t.Errorf("окно автоклика на экране: создано в %+v, сейчас %+v", where, windowRect(hw.hwnd))
		}
		if ex, _, _ := procGetWindowLongPtrW.Call(hw.hwnd, uintptr(gwlExStyle)); ex&wsExToolWindow == 0 {
			t.Errorf("у скрытого окна будет кнопка на панели задач: стиль %#x", ex)
		}
	}
}

// Окно за краем экрана: доверенный клик с движениями мыши, страница видна,
// рисуется и в фокусе — и сразу, и спустя десять секунд за краем (slow:
// галочка появляется позже; Chromium считает перекрытие окон не сразу).
// Галочка находится и без признаков VK: спрятанный флажок в label, роль в
// теневом корне, флажок во фрейме, один текст.
func TestWebView2AutoClick(t *testing.T) {
	s := newStandSolver(t)
	srv := newCaptchaStand(t)
	outer := t

	for _, tc := range []struct{ name, scenario, extra, found string }{
		{"fast", "auto", "", "признак vk, document"},
		{"slow", "auto", "&boxdelay=10000", "признак vk, document"},
		{"plain", "autoplain", "", "признак input, document"},
		{"shadow", "autoshadow", "", "признак role, shadow"},
		{"frame", "autoframe", "", "признак input, frame"},
		{"text", "autotext", "", "признак text, document"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out syncBuffer
			var log logLines
			s.opts.Out = &out
			s.opts.Logf = func(format string, args ...any) {
				log.logf(format, args...)
				outer.Logf(format, args...)
			}
			watchHidden(t, s)
			s.revealed = func(webview2.WebView) {
				t.Error("окно показано человеку, хотя автоклик должен был пройти")
			}
			before := profilesNow()

			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			start := time.Now()
			token, err := s.Solve(ctx, standAutoRequest(srv.page(tc.scenario, "fetch")+tc.extra, 40*time.Second))
			if err != nil {
				t.Fatalf("Solve: %v\nвывод:%s", err, out.String())
			}
			if want := standToken(tc.scenario, "fetch"); token != want {
				t.Fatalf("токен %q, ожидался %q", token, want)
			}
			if !strings.Contains(log.text(), tc.found) {
				t.Errorf("в журнале нет «%s»:\n%s", tc.found, log.text())
			}
			t.Logf("токен за %s", time.Since(start).Round(time.Millisecond))

			checks := srv.checkQueries()
			q := checks[len(checks)-1]
			t.Logf("check: trusted=%s moves=%s vis=%s fps=%s focus=%s dpr=%s", q.Get("trusted"), q.Get("moves"),
				q.Get("vis"), q.Get("fps"), q.Get("focus"), q.Get("dpr"))
			if q.Get("trusted") != "true" {
				t.Errorf("клик недоверенный: isTrusted=%s", q.Get("trusted"))
			}
			if n, _ := strconv.Atoi(q.Get("moves")); n < 10 {
				t.Errorf("движений мыши до клика %d, ожидалось не меньше 10", n)
			}
			if q.Get("vis") != "visible" {
				t.Errorf("страница за краем экрана считает себя %q", q.Get("vis"))
			}
			if n, _ := strconv.Atoi(q.Get("fps")); n < 10 {
				t.Errorf("кадров за секунду до клика %d: страница не рисуется", n)
			}
			if q.Get("focus") != "true" {
				t.Errorf("страница не в фокусе: hasFocus=%s", q.Get("focus"))
			}
			if text := out.String(); !strings.Contains(text, "пройдена автоматически") ||
				strings.Contains(text, "не удалось") {
				t.Errorf("вывод:%s", text)
			}
			waitProfilesGone(t, before)
		})
	}
}

// Автоклик не прошёл — окно выходит человеку со строкой в выводе, человек
// решает ту же капчу в том же окне. Описание страницы — в журнал, без
// запроса адреса. Ошибка VK на клик окно не закрывает, а отдаёт человеку.
func TestWebView2AutoFallback(t *testing.T) {
	s := newStandSolver(t)
	srv := newCaptchaStand(t)
	outer := t
	s.quietReveal = true
	s.autoTiming = &autoTiming{find: 6 * time.Second, click: defaultAutoTiming.click, poll: defaultAutoTiming.poll,
		reply: 5 * time.Second}

	for _, tc := range []struct{ scenario, reason string }{
		{"autobot", "слайдер"},
		{"autonone", "нет галочки"},
		{"autovkerror", "ошибкой 100"},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			var out syncBuffer
			var log logLines
			s.opts.Out = &out
			s.opts.Logf = func(format string, args ...any) {
				log.logf(format, args...)
				outer.Logf(format, args...)
			}
			watchHidden(t, s)
			var revealed sync.WaitGroup
			revealed.Add(1)
			s.revealed = func(w webview2.WebView) {
				defer revealed.Done()
				if r := windowRect(uintptr(w.Window())); !onScreen(r) {
					t.Errorf("окно показано человеку, но не на экране: %+v", r)
				}
				w.Eval("standHuman()")
			}
			before := profilesNow()

			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			token, err := s.Solve(ctx, standAutoRequest(srv.page(tc.scenario, "fetch"), 40*time.Second))
			if err != nil {
				t.Fatalf("Solve: %v\nвывод:%s", err, out.String())
			}
			if want := standToken(tc.scenario, "fetch"); token != want {
				t.Fatalf("токен %q, ожидался %q", token, want)
			}
			revealed.Wait()
			text := out.String()
			if !strings.Contains(text, "Автоматически пройти капчу не удалось: ") ||
				!strings.Contains(text, tc.reason) || strings.Contains(text, "пройдена автоматически") {
				t.Errorf("вывод:%s", text)
			}
			if got := srv.checkQueries(); len(got) == 0 || got[len(got)-1].Get("human") != "1" {
				t.Errorf("последний check — не от человека: %v", got)
			}
			journal := log.text()
			if !strings.Contains(journal, "страница при неудаче автоклика: "+srv.URL+"/not_robot_captcha,") {
				t.Errorf("в журнале нет описания страницы:\n%s", journal)
			}
			for _, bad := range []string{"session_token", "scenario=", "decoy"} {
				if strings.Contains(journal, bad) {
					t.Errorf("в журнале %q:\n%s", bad, journal)
				}
			}
			if tc.scenario == "autonone" {
				foreign := strings.Replace(srv.URL, "127.0.0.1", "localhost", 1) + "/empty "
				for _, want := range []string{"теневых корней 1",
					"фрейм чужого происхождения — внутрь не заглянуть: " + foreign, "button/shadow: button «Обновить»"} {
					if !strings.Contains(journal, want) {
						t.Errorf("в журнале нет %q:\n%s", want, journal)
					}
				}
			}
			waitProfilesGone(t, before)
		})
	}
}

// Срок вышел, пока идёт автофаза: окно закрывается, человеку не показывается.
func TestWebView2AutoDeadline(t *testing.T) {
	s := newStandSolver(t)
	srv := newCaptchaStand(t)
	var out syncBuffer
	s.opts.Out = &out
	watchHidden(t, s)
	s.revealed = func(webview2.WebView) { t.Error("окно показано человеку после срока") }
	before := profilesNow()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := s.Solve(ctx, standAutoRequest(srv.page("autonone", "fetch"), 3*time.Second))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ошибка %v, ожидался истёкший срок", err)
	}
	if text := out.String(); strings.Contains(text, "не удалось") {
		t.Errorf("вывод:%s", text)
	}
	waitProfilesGone(t, before)
}

// Человек закрыл показанное окно: ErrWindowClosed сразу, а не по сроку.
func TestWebView2AutoClosedByUser(t *testing.T) {
	s := newStandSolver(t)
	srv := newCaptchaStand(t)
	s.quietReveal = true
	s.autoTiming = &autoTiming{find: 3 * time.Second, click: defaultAutoTiming.click, poll: defaultAutoTiming.poll,
		reply: 3 * time.Second}
	watchHidden(t, s)
	// Destroy — это WM_CLOSE окну, ровно то, что шлёт крестик.
	s.revealed = func(w webview2.WebView) { w.Destroy() }
	before := profilesNow()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.Solve(ctx, standAutoRequest(srv.page("autonone", "fetch"), 30*time.Second))
	if !errors.Is(err, ErrWindowClosed) {
		t.Fatalf("ошибка %v, ожидалось «окно капчи закрыто»", err)
	}
	if ctx.Err() != nil {
		t.Fatal("Solve дождался срока, а не закрытия окна")
	}
	waitProfilesGone(t, before)
}

// ERROR_LIMIT на автоклик — окно человеку не показывается и закрывается
// сразу: решать уже нечего. Ошибка VK на initSession — то же: капчи на
// странице не будет.
func TestWebView2AutoPageOutcomes(t *testing.T) {
	s := newStandSolver(t)
	srv := newCaptchaStand(t)

	for _, tc := range []struct {
		scenario string
		want     error
	}{
		{"autolimit", ErrLimit},
		{"autoiniterror", ErrPageInit},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			var out syncBuffer
			s.opts.Out = &out
			watchHidden(t, s)
			s.revealed = func(webview2.WebView) { t.Error("окно показано человеку") }
			before := profilesNow()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := time.Now()
			_, err := s.Solve(ctx, standAutoRequest(srv.page(tc.scenario, "fetch"), 30*time.Second))
			if !errors.Is(err, tc.want) {
				t.Fatalf("ошибка %v, ожидалась %v", err, tc.want)
			}
			if took := time.Since(start); took > 15*time.Second {
				t.Errorf("окно закрылось через %s", took)
			}
			if text := out.String(); strings.Contains(text, "не удалось") || strings.Contains(text, "пройдена") {
				t.Errorf("вывод:%s", text)
			}
			waitProfilesGone(t, before)
		})
	}
}

// Автоклик через прокси qwdtt-cli (kill-switch): аргументы браузера прокси и
// скрытого окна уходят вместе.
func TestWebView2AutoProxy(t *testing.T) {
	s := newStandSolver(t)
	srv := newCaptchaTLSStand(t)
	t.Setenv(browserArgsEnv, "--ignore-certificate-errors")
	watchHidden(t, s)
	s.revealed = func(webview2.WebView) { t.Error("окно показано человеку") }
	var mu sync.Mutex
	var asked []string
	s.opts.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		asked = append(asked, address)
		mu.Unlock()
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}
	before := profilesNow()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	page := "https://id.vk.com/not_robot_captcha?session_token=stand&scenario=auto&via=fetch"
	token, err := s.Solve(ctx, standAutoRequest(page, 30*time.Second))
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if token != standToken("auto", "fetch") {
		t.Fatalf("токен %q, ожидался со стенда", token)
	}
	if q := srv.checkQueries(); len(q) != 1 || q[0].Get("trusted") != "true" || q[0].Get("vis") != "visible" {
		t.Errorf("check: %v", q)
	}
	mu.Lock()
	if len(asked) == 0 || asked[0] != "id.vk.com:443" {
		t.Errorf("через прокси: %q", asked)
	}
	mu.Unlock()
	if got := os.Getenv(browserArgsEnv); got != "--ignore-certificate-errors" {
		t.Errorf("%s после окна = %q", browserArgsEnv, got)
	}
	// Окно и его прокси живут чуть дольше Solve (endSession): дождаться, пока
	// закроются, — иначе прокси пишет в журнал теста, которого уже нет.
	waitProfilesGone(t, before)
}

// Сквозной путь, как в qwdtt-cli connect: событие ядра selected → диспетчер →
// окно с автокликом → токен в канале ядра, окно человеку не показано.
func TestWebView2AutoThroughDispatcher(t *testing.T) {
	s := newStandSolver(t)
	srv := newCaptchaStand(t)
	var out syncBuffer
	s.opts.Out = &out
	watchHidden(t, s)
	s.revealed = func(webview2.WebView) { t.Error("окно показано человеку") }
	before := profilesNow()
	d := newDispatcher(t, Options{Solver: s, Control: core.NewControl(), Logf: t.Logf}, time.Minute)

	ev := required("selected")
	ev.RedirectURI = srv.page("auto", "fetch")
	d.Observe(ev)

	if got := answer(t, 30*time.Second); got != standToken("auto", "fetch") {
		t.Fatalf("ядро получило %q, ожидался токен со стенда", got)
	}
	if q := srv.checkQueries(); len(q) != 1 || q[0].Get("trusted") != "true" {
		t.Errorf("check: %v", q)
	}
	if text := out.String(); !strings.Contains(text, "пробую пройти её автоматически") ||
		!strings.Contains(text, "пройдена автоматически") {
		t.Errorf("вывод:%s", text)
	}
	waitProfilesGone(t, before)
}

// Ctrl+C (Options.Stop) останавливает капчу сразу — и посреди автофазы, и
// когда окно уже у человека: ядро получает отказ, окно закрыто, профиль
// убран, не дожидаясь срока окна.
func TestWebView2AutoStop(t *testing.T) {
	s := newStandSolver(t)
	srv := newCaptchaStand(t)
	s.quietReveal = true

	for _, tc := range []struct {
		name   string
		timing *autoTiming
		human  bool // остановить, когда окно показано человеку
	}{
		// Галочки нет, и автофаза ищет её все 30 с: остановка — посреди неё.
		{"автофаза", nil, false},
		{"окно у человека", &autoTiming{find: 3 * time.Second, click: defaultAutoTiming.click,
			poll: defaultAutoTiming.poll, reply: 3 * time.Second}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.autoTiming = tc.timing
			var out syncBuffer
			s.opts.Out = &out
			watchHidden(t, s)
			stop, cancel := context.WithCancel(context.Background())
			defer cancel()
			var mu sync.Mutex
			var stopped time.Time
			halt := func() {
				mu.Lock()
				stopped = time.Now()
				mu.Unlock()
				cancel()
			}
			s.revealed = func(webview2.WebView) {
				if !tc.human {
					t.Error("окно показано человеку")
					return
				}
				halt()
			}
			before := profilesNow()
			d := newDispatcher(t, Options{Solver: s, Control: core.NewControl(), Logf: t.Logf, Stop: stop}, time.Minute)

			ev := required("selected")
			ev.RedirectURI = srv.page("autonone", "fetch")
			d.Observe(ev)
			if !tc.human {
				time.AfterFunc(4*time.Second, halt)
			}

			got := answer(t, 30*time.Second)
			if got == "" || strings.Contains(got, "stand-token") {
				t.Fatalf("ядро получило %q, ожидался отказ", got)
			}
			mu.Lock()
			took := time.Since(stopped)
			mu.Unlock()
			if took > 2*time.Second {
				t.Errorf("отказ через %s после остановки", took)
			}
			if text := out.String(); strings.Contains(text, "не удалось") != tc.human {
				t.Errorf("вывод:%s", text)
			}
			waitProfilesGone(t, before)
		})
	}
}
