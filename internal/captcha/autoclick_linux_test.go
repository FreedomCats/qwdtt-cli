package captcha

import (
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// Автоклик в браузере по CDP на стенде (перенесён из csqtt-cli): окно за
// краем экрана, доверенный клик с движениями мыши, страница видна и
// рисуется. Тесты, где окно выходит человеку, выводят его без переднего
// плана (quietReveal). Включается, как и стенд браузера, переменной
// QWDTT_BROWSER=1 — и только под Xvfb, чтобы окна не отбирали фокус у
// человека за машиной:
//
//	QWDTT_BROWSER=1 xvfb-run -a -s "-screen 0 1280x1024x24 -noreset" \
//		go test ./internal/captcha -run BrowserAuto -v
//
// Под Xvfb оконного менеджера нет, место окну даёт сам браузер. Без
// графического сеанса (headless) проверки места окна пропускаются.

// headless — графического сеанса нет.
func headless() bool { return os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" }

// newAutoStand — браузер стенда для автоклика: сеанс считается X11, даже
// если тесты идут под Wayland.
func newAutoStand(t *testing.T) *Browser {
	t.Helper()
	b := newBrowserStand(t)
	b.wayland = func() bool { return false }
	return b
}

// watchHiddenCDP следит, чтобы окно автоклика открылось за краем экрана.
func watchHiddenCDP(t *testing.T, b *Browser) { watchWindowCDP(t, b, false) }

// watchWindowCDP следит, чтобы окно автоклика открылось на экране (visible)
// или за его краем.
func watchWindowCDP(t *testing.T, b *Browser, visible bool) {
	b.hidden = func(d autoDriver) {
		if headless() {
			return
		}
		g, err := geometry(context.Background(), d)
		if err != nil {
			t.Errorf("место окна автоклика не узнать: %v", err)
			return
		}
		if g.onScreen() != visible {
			t.Errorf("окно автоклика на экране: %v, ожидалось %v; окно %+v, экран %+v", g.onScreen(), visible,
				g.window, g.screen)
		}
	}
}

// Окно за краем экрана: доверенный клик с движениями мыши, страница видна и
// рисуется — и сразу, и спустя десять секунд за краем (slow). Галочка
// находится и без признаков VK: спрятанный флажок в label, роль в теневом
// корне, флажок во фрейме, один текст.
func TestBrowserAutoClick(t *testing.T) {
	b := newAutoStand(t)
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
			b.opts.Out = &out
			b.opts.Logf = func(format string, args ...any) {
				log.logf(format, args...)
				outer.Logf(format, args...)
			}
			watchHiddenCDP(t, b)
			b.revealed = func(autoDriver) {
				t.Error("окно показано человеку, хотя автоклик должен был пройти")
			}
			before := profilesNow()

			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			start := time.Now()
			token, err := b.Solve(ctx, standAutoRequest(srv.page(tc.scenario, "fetch")+tc.extra, 40*time.Second))
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
			t.Logf("check: trusted=%s moves=%s vis=%s fps=%s focus=%s", q.Get("trusted"), q.Get("moves"),
				q.Get("vis"), q.Get("fps"), q.Get("focus"))
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
// запроса адреса.
//
// visible — то же в окне, которое и не пряталось (сеанс Wayland): автоклик
// шёл на виду, при неудаче окно — на передний план (здесь — без него,
// quietReveal) и та же строка.
func TestBrowserAutoFallback(t *testing.T) {
	b := newAutoStand(t)
	srv := newCaptchaStand(t)
	outer := t
	b.quietReveal = true
	b.autoTiming = &autoTiming{find: 6 * time.Second, click: defaultAutoTiming.click, poll: defaultAutoTiming.poll,
		reply: 5 * time.Second}
	baseArgs := b.extraArgs

	for _, tc := range []struct {
		name, scenario, reason string
		visible                bool
	}{
		{"autobot", "autobot", "слайдер", false},
		{"autonone", "autonone", "нет галочки", false},
		{"autovkerror", "autovkerror", "ошибкой 100", false},
		{"visible", "autobot", "слайдер", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b.extraArgs = baseArgs
			b.wayland = func() bool { return tc.visible }
			watchWindowCDP(t, b, tc.visible)
			var out syncBuffer
			var log logLines
			b.opts.Out = &out
			b.opts.Logf = func(format string, args ...any) {
				log.logf(format, args...)
				outer.Logf(format, args...)
			}
			var revealed sync.WaitGroup
			revealed.Add(1)
			b.revealed = func(d autoDriver) {
				defer revealed.Done()
				if !headless() {
					if g, err := geometry(context.Background(), d); err != nil || !g.onScreen() {
						t.Errorf("окно показано человеку, но не на экране: окно %+v, экран %+v, %v", g.window,
							g.screen, err)
					}
				}
				standHumanCDP(t, d)
			}
			before := profilesNow()

			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			token, err := b.Solve(ctx, standAutoRequest(srv.page(tc.scenario, "fetch"), 40*time.Second))
			if err != nil {
				t.Fatalf("Solve: %v\nвывод:%s", err, out.String())
			}
			if want := standToken(tc.scenario, "fetch"); token != want {
				t.Fatalf("токен %q, ожидался %q", token, want)
			}
			revealed.Wait()
			text := out.String()
			if !strings.Contains(text, "Автоматически пройти капчу не удалось: ") ||
				!strings.Contains(text, tc.reason) || strings.Contains(text, "пройдена автоматически") ||
				strings.Contains(text, visibleLine) != tc.visible {
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

// standHumanCDP — человек решает капчу в показанном окне; страница стенда
// могла ещё не дойти до своего скрипта — ждать его.
func standHumanCDP(t *testing.T, d autoDriver) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var err error
	for ctx.Err() == nil {
		var done bool
		if err = evaluate(ctx, d, "typeof standHuman === 'function' && (standHuman(), true)", &done); err == nil && done {
			return
		}
		_ = sleep(ctx, 100*time.Millisecond)
	}
	t.Errorf("человек не решил капчу: %v", err)
}

// Срок вышел, пока идёт автофаза: браузер закрывается, человеку окно не
// показывается, Solve дожидается уборки.
func TestBrowserAutoDeadline(t *testing.T) {
	b := newAutoStand(t)
	srv := newCaptchaStand(t)
	watchHiddenCDP(t, b)
	b.revealed = func(autoDriver) { t.Error("окно показано человеку после срока") }
	before := profilesNow()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	start := time.Now()
	_, err := b.Solve(ctx, standAutoRequest(srv.page("autonone", "fetch"), 4*time.Second))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ошибка %v, ожидался истёкший срок", err)
	}
	if took := time.Since(start); took > 4*time.Second+browserStop+time.Second {
		t.Fatalf("Solve вернулся через %s", took)
	}
	waitProfilesGone(t, before)
}

// Автоклик через прокси qwdtt-cli (kill-switch): аргументы прокси и
// скрытого окна уходят вместе.
func TestBrowserAutoProxy(t *testing.T) {
	b := newAutoStand(t)
	srv := newCaptchaTLSStand(t)
	b.extraArgs = append(b.extraArgs, "--ignore-certificate-errors")
	watchHiddenCDP(t, b)
	b.revealed = func(autoDriver) { t.Error("окно показано человеку") }
	var mu sync.Mutex
	var asked []string
	b.opts.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
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
	token, err := b.Solve(ctx, standAutoRequest(page, 30*time.Second))
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
	waitProfilesGone(t, before)
}

// visibleLine — начало строки об автоклике в видимом окне
// (autoRun.announceVisible).
const visibleLine = "открыто окно, в нём сейчас пробую пройти её автоматически"

// Окно не спрятать — автоклик идёт на виду, строка просит окно не трогать,
// и проходит так же: доверенный клик, страница видна и в фокусе, человеку
// окно не выводится.
//
// pinned — оконный менеджер прижал окно к экрану (здесь его изображает
// свой --window-position: он идёт после нашего и побеждает); автоклик это
// видит по месту окна. wayland — сеанс Wayland: окно сразу открывается без
// места за краем. Под Xvfb браузер всё равно идёт через X11, и окно — на
// экране.
func TestBrowserAutoVisible(t *testing.T) {
	b := newAutoStand(t)
	srv := newCaptchaStand(t)
	outer := t
	baseArgs := b.extraArgs

	for _, tc := range []struct{ name, journal string }{
		{"pinned", "окно не спряталось за край экрана"},
		{"wayland", "сеанс Wayland"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b.extraArgs = baseArgs
			b.wayland = func() bool { return tc.name == "wayland" }
			b.hidden = nil
			if tc.name == "pinned" {
				if headless() {
					t.Skip("без окна места на экране нет")
				}
				b.extraArgs = append(append([]string(nil), baseArgs...), "--window-position=40,40")
			} else {
				watchWindowCDP(t, b, true)
			}
			var out syncBuffer
			var log logLines
			b.opts.Out = &out
			b.opts.Logf = func(format string, args ...any) {
				log.logf(format, args...)
				outer.Logf(format, args...)
			}
			b.revealed = func(autoDriver) {
				t.Error("окно показано человеку, хотя автоклик должен был пройти")
			}
			before := profilesNow()

			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			token, err := b.Solve(ctx, standAutoRequest(srv.page("auto", "fetch"), 40*time.Second))
			if err != nil {
				t.Fatalf("Solve: %v\nвывод:%s", err, out.String())
			}
			if want := standToken("auto", "fetch"); token != want {
				t.Fatalf("токен %q, ожидался %q", token, want)
			}
			if !strings.Contains(log.text(), tc.journal) {
				t.Errorf("в журнале нет «%s»:\n%s", tc.journal, log.text())
			}

			checks := srv.checkQueries()
			q := checks[len(checks)-1]
			t.Logf("check: trusted=%s moves=%s vis=%s fps=%s focus=%s", q.Get("trusted"), q.Get("moves"),
				q.Get("vis"), q.Get("fps"), q.Get("focus"))
			if q.Get("trusted") != "true" {
				t.Errorf("клик недоверенный: isTrusted=%s", q.Get("trusted"))
			}
			if q.Get("vis") != "visible" {
				t.Errorf("страница в видимом окне считает себя %q", q.Get("vis"))
			}
			if q.Get("focus") != "true" {
				t.Errorf("страница не в фокусе: hasFocus=%s", q.Get("focus"))
			}
			if text := out.String(); !strings.Contains(text, visibleLine) ||
				!strings.Contains(text, "пройдена автоматически") || strings.Contains(text, "не удалось") ||
				strings.Contains(text, "капчу — пробую") {
				t.Errorf("вывод:%s", text)
			}
			waitProfilesGone(t, before)
		})
	}
}

// Человек закрыл показанное окно. Браузер с окном автоклика запущен без
// окна при старте и с последним окном не выходит — закрытие видно по
// отключению от вкладки: ErrWindowClosed сразу, а не по сроку.
func TestBrowserAutoClosedByUser(t *testing.T) {
	b := newAutoStand(t)
	srv := newCaptchaStand(t)
	b.quietReveal = true
	b.autoTiming = &autoTiming{find: 3 * time.Second, click: defaultAutoTiming.click, poll: defaultAutoTiming.poll,
		reply: 3 * time.Second}
	watchHiddenCDP(t, b)
	b.revealed = func(d autoDriver) {
		if _, err := d.devtools(context.Background(), "Page.close", nil); err != nil {
			t.Errorf("Page.close: %v", err)
		}
	}
	before := profilesNow()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := b.Solve(ctx, standAutoRequest(srv.page("autonone", "fetch"), 30*time.Second))
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
func TestBrowserAutoPageOutcomes(t *testing.T) {
	b := newAutoStand(t)
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
			b.opts.Out = &out
			watchHiddenCDP(t, b)
			b.revealed = func(autoDriver) { t.Error("окно показано человеку") }
			before := profilesNow()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := time.Now()
			_, err := b.Solve(ctx, standAutoRequest(srv.page(tc.scenario, "fetch"), 30*time.Second))
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

// Сквозной путь, как в qwdtt-cli connect: событие ядра selected → диспетчер →
// браузер с автокликом → токен в канале ядра, окно человеку не показано.
func TestBrowserAutoThroughDispatcher(t *testing.T) {
	b := newAutoStand(t)
	srv := newCaptchaStand(t)
	var out syncBuffer
	b.opts.Out = &out
	watchHiddenCDP(t, b)
	b.revealed = func(autoDriver) { t.Error("окно показано человеку") }
	d := newDispatcher(t, Options{Solver: b, Control: core.NewControl(), Logf: t.Logf}, time.Minute)

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
}

// Ctrl+C (Options.Stop) останавливает капчу сразу — и посреди автофазы, и
// когда окно уже у человека: ядро получает отказ, браузер закрыт, профиль
// убран, не дожидаясь срока окна.
func TestBrowserAutoStop(t *testing.T) {
	b := newAutoStand(t)
	srv := newCaptchaStand(t)
	b.quietReveal = true

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
			b.autoTiming = tc.timing
			var out syncBuffer
			b.opts.Out = &out
			watchHiddenCDP(t, b)
			var pid int
			b.started = func(p int) { pid = p }
			stop, cancel := context.WithCancel(context.Background())
			defer cancel()
			var stopped time.Time
			halt := func() { stopped = time.Now(); cancel() }
			b.revealed = func(autoDriver) {
				if !tc.human {
					t.Error("окно показано человеку")
					return
				}
				halt()
			}
			before := profilesNow()
			d := newDispatcher(t, Options{Solver: b, Control: core.NewControl(), Logf: t.Logf, Stop: stop}, time.Minute)

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
			if took := time.Since(stopped); took > browserStop+time.Second {
				t.Errorf("отказ через %s после остановки", took)
			}
			if pid != 0 && syscall.Kill(pid, 0) == nil {
				t.Errorf("браузер %d жив после остановки", pid)
			}
			if text := out.String(); strings.Contains(text, "не удалось") != tc.human {
				t.Errorf("вывод:%s", text)
			}
			waitProfilesGone(t, before)
		})
	}
}

// То же в окне у человека (manual, без автоклика): ERROR_LIMIT и ошибка VK на
// initSession закрывают окно сразу, а не по сроку; браузер и профиль убраны.
func TestBrowserHumanPageOutcomes(t *testing.T) {
	b := newBrowserStand(t)
	srv := newCaptchaStand(t)

	for _, tc := range []struct {
		scenario string
		want     error
	}{
		{"limit", ErrLimit},
		{"initerror", ErrPageInit},
	} {
		t.Run(tc.scenario, func(t *testing.T) {
			var pid int
			b.started = func(p int) { pid = p }
			before := profilesNow()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			start := time.Now()
			_, err := b.Solve(ctx, standRequest(srv.page(tc.scenario, "fetch"), 30*time.Second))
			if !errors.Is(err, tc.want) {
				t.Fatalf("ошибка %v, ожидалась %v", err, tc.want)
			}
			if took := time.Since(start); took > 15*time.Second {
				t.Errorf("окно закрылось через %s", took)
			}
			// Уборка после исхода идёт без Solve: ждём её.
			deadline := time.Now().Add(browserStop + 5*time.Second)
			for pid != 0 && syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			if pid == 0 {
				t.Error("браузер не запускался")
			} else if syscall.Kill(pid, 0) == nil {
				t.Errorf("браузер %d жив после закрытия окна", pid)
			}
			waitProfilesGone(t, before)
		})
	}
}
