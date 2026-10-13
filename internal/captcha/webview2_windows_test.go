//go:build windows

package captcha

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	webview2 "github.com/jchv/go-webview2"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// Окну нужен рабочий стол, а WebView2 Runtime на машине сборки может и не
// стоять, поэтому стенд включается руками:
//
//	TT_WEBVIEW2=1 go test ./internal/captcha -run WebView2 -v
//
// Он проверяет всё, кроме самой VK: окно, Bind и перехватчик на настоящем
// движке, оба пути страницы (fetch и XMLHttpRequest), слайдер, отказ VK,
// закрытие крестиком, отмену по ctx и уборку профиля. Страницы делают запрос
// сами, без человека, так что окна мелькают и закрываются.

// stand — страницы, изображающие капчу VK. Путь запроса содержит
// captchaNotRobot.check, ровно по нему перехватчик и узнаёт проверку.
func stand(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(standHandler())
	t.Cleanup(srv.Close)
	return srv
}

func standHandler() http.Handler {
	page := func(script string) string {
		return `<!doctype html><meta charset="utf-8"><title>стенд</title>` +
			`<p>Стенд капчи tt</p><script>` + script + `</script>`
	}
	check := `/method/captchaNotRobot.check`

	mux := http.NewServeMux()
	mux.HandleFunc(check, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("kind") {
		case "slider":
			io.WriteString(w, `{"response":{"show_captcha_type":"slider"}}`)
		case "error":
			io.WriteString(w, `{"error":{"error_code":14,"error_msg":"Captcha needed"}}`)
		default:
			io.WriteString(w, `{"response":{"success_token":"token-`+r.URL.Query().Get("via")+`"}}`)
		}
	})
	mux.HandleFunc("/fetch", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, page(`fetch('`+check+`?via=fetch',{method:'POST'});`))
	})
	mux.HandleFunc("/xhr", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, page(`var x=new XMLHttpRequest();x.open('POST','`+check+`?via=xhr');x.send();`))
	})
	mux.HandleFunc("/slider", func(w http.ResponseWriter, r *http.Request) {
		// Сначала слайдер, потом — как будто человек его решил — токен.
		io.WriteString(w, page(`fetch('`+check+`?kind=slider').then(function(){`+
			`setTimeout(function(){fetch('`+check+`?via=slider');},500);});`))
	})
	mux.HandleFunc("/error", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, page(`fetch('`+check+`?kind=error');`))
	})
	mux.HandleFunc("/idle", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, page(``))
	})

	return mux
}

func newStandSolver(t *testing.T) *WebView2 {
	t.Helper()
	if os.Getenv("TT_WEBVIEW2") == "" {
		t.Skip("стенд окна капчи включается переменной TT_WEBVIEW2=1")
	}
	s, err := NewWebView2(WebView2Options{Out: io.Discard})
	if err != nil {
		t.Skipf("окна не будет: %v", err)
	}
	return s
}

// profilesNow — каталоги профилей, которые сейчас лежат в %TEMP%.
func profilesNow() map[string]bool {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), profilePrefix+"*"))
	set := make(map[string]bool, len(matches))
	for _, m := range matches {
		set[m] = true
	}
	return set
}

// waitProfilesGone ждёт, пока профили, появившиеся после before, будут убраны.
func waitProfilesGone(t *testing.T, before map[string]bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var left []string
		for dir := range profilesNow() {
			if !before[dir] {
				left = append(left, dir)
			}
		}
		if len(left) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("профиль WebView2 не убран за 15 с: %s", strings.Join(left, ", "))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func standRequest(url string, budget time.Duration) Request {
	return Request{Session: 1, Mode: ModeManual, RedirectURI: url, Deadline: time.Now().Add(budget)}
}

func TestWebView2Token(t *testing.T) {
	s := newStandSolver(t)
	srv := stand(t)

	for _, via := range []string{"fetch", "xhr", "slider"} {
		t.Run(via, func(t *testing.T) {
			before := profilesNow()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			start := time.Now()
			token, err := s.Solve(ctx, standRequest(srv.URL+"/"+via, 30*time.Second))
			if err != nil {
				t.Fatalf("Solve: %v", err)
			}
			if want := "token-" + via; token != want {
				t.Fatalf("токен %q, ожидался %q", token, want)
			}
			t.Logf("токен за %s", time.Since(start).Round(time.Millisecond))
			waitProfilesGone(t, before)
		})
	}
}

func TestWebView2VKError(t *testing.T) {
	s := newStandSolver(t)
	srv := stand(t)

	_, err := s.Solve(context.Background(), standRequest(srv.URL+"/error", 30*time.Second))
	if err == nil || !strings.Contains(err.Error(), "VK отказал") || !strings.Contains(err.Error(), "14") {
		t.Fatalf("ошибка %v, ожидался отказ VK с кодом 14", err)
	}
}

func TestWebView2Deadline(t *testing.T) {
	s := newStandSolver(t)
	srv := stand(t)
	before := profilesNow()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	_, err := s.Solve(ctx, standRequest(srv.URL+"/idle", 3*time.Second))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ошибка %v, ожидался истёкший срок", err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("Solve вернулся через %s после срока в 3 с", took)
	}
	// Окно закрыто по ctx — иначе профиль никогда не освободится.
	waitProfilesGone(t, before)
}

func TestWebView2ClosedByUser(t *testing.T) {
	s := newStandSolver(t)
	srv := stand(t)
	before := profilesNow()

	// Destroy — это WM_CLOSE окну, ровно то, что шлёт крестик.
	s.opened = func(w webview2.WebView) {
		time.AfterFunc(time.Second, w.Destroy)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := s.Solve(ctx, standRequest(srv.URL+"/idle", 30*time.Second))
	if err == nil || !strings.Contains(err.Error(), "закрыто") {
		t.Fatalf("ошибка %v, ожидалось «окно капчи закрыто»", err)
	}
	if ctx.Err() != nil {
		t.Fatal("Solve дождался срока, а не закрытия окна")
	}
	waitProfilesGone(t, before)
}

func TestWindowTitle(t *testing.T) {
	deadline := time.Date(2026, 10, 11, 14, 5, 9, 0, time.Local)
	if got := windowTitle(Request{Deadline: deadline}); got != "Капча VK — ответить до 14:05:09" {
		t.Errorf("заголовок %q", got)
	}
	if got := windowTitle(Request{}); got != "Капча VK — tt" {
		t.Errorf("заголовок без срока %q", got)
	}
}

// Сквозной путь целиком, как в tt connect: событие ядра → диспетчер → окно →
// токен в канале ядра. Консоль не участвует.
func TestWebView2ThroughDispatcher(t *testing.T) {
	s := newStandSolver(t)
	srv := stand(t)
	d := newDispatcher(t, Options{Solver: s, Control: core.NewControl()}, time.Minute)

	ev := required("manual")
	ev.RedirectURI = srv.URL + "/fetch"
	d.Observe(ev)

	if got := answer(t, 15*time.Second); got != "token-fetch" {
		t.Fatalf("ядро получило %q, ожидался токен со стенда", got)
	}
}

// Окно через прокси tt (plan-e3.md, W2): страница грузится только через
// него. Диалер уводит id.vk.com:443 на стенд с TLS — токен со стенда
// значит, что окно ходило через прокси, а не к настоящей VK. Сертификат
// стенда браузеру разрешается своим аргументом в той же переменной, что и
// прокси, — заодно видно, что значение пользователя не затирается.
func TestWebView2Proxy(t *testing.T) {
	s := newStandSolver(t)
	srv := httptest.NewTLSServer(standHandler())
	t.Cleanup(srv.Close)
	t.Setenv(browserArgsEnv, "--ignore-certificate-errors")

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
	token, err := s.Solve(ctx, standRequest("https://id.vk.com/fetch", 30*time.Second))
	if err != nil {
		t.Fatalf("Solve: %v", err)
	}
	if token != "token-fetch" {
		t.Fatalf("токен %q, ожидался со стенда", token)
	}
	mu.Lock()
	if len(asked) == 0 || asked[0] != "id.vk.com:443" {
		t.Errorf("через прокси: %q", asked)
	}
	mu.Unlock()
	if got := os.Getenv(browserArgsEnv); got != "--ignore-certificate-errors" {
		t.Errorf("%s после окна = %q", browserArgsEnv, got)
	}
	waitProfilesGone(t, before)
}

func TestSetBrowserArgs(t *testing.T) {
	t.Setenv(browserArgsEnv, "")
	os.Unsetenv(browserArgsEnv)
	restore := setBrowserArgs("--proxy-server=http://127.0.0.1:1")
	if got := os.Getenv(browserArgsEnv); got != "--proxy-server=http://127.0.0.1:1" {
		t.Errorf("без своего значения: %q", got)
	}
	restore()
	if _, ok := os.LookupEnv(browserArgsEnv); ok {
		t.Error("переменная осталась после restore")
	}

	t.Setenv(browserArgsEnv, "--lang=ru")
	restore = setBrowserArgs("--proxy-server=http://127.0.0.1:1")
	if got := os.Getenv(browserArgsEnv); got != "--lang=ru --proxy-server=http://127.0.0.1:1" {
		t.Errorf("со своим значением: %q", got)
	}
	restore()
	if got := os.Getenv(browserArgsEnv); got != "--lang=ru" {
		t.Errorf("своё значение после restore: %q", got)
	}
}
