//go:build windows

package captcha

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
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
//	QWDTT_WEBVIEW2=1 go test ./internal/captcha -run WebView2 -v
//
// Он проверяет всё, кроме самой VK: окно, Bind и перехватчик на настоящем
// движке, оба пути страницы (fetch и XMLHttpRequest), слайдер, отказ VK,
// закрытие крестиком, отмену по ctx и уборку профиля. Страницы делают запрос
// сами, без человека, так что окна мелькают и закрываются.

func newStandSolver(t *testing.T) *WebView2 {
	t.Helper()
	if os.Getenv("QWDTT_WEBVIEW2") == "" {
		t.Skip("стенд окна капчи включается переменной QWDTT_WEBVIEW2=1")
	}
	s, err := NewWebView2(WebView2Options{Out: io.Discard})
	if err != nil {
		t.Skipf("окна не будет: %v", err)
	}
	return s
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
	if got := windowTitle(Request{}); got != "Капча VK — qwdtt-cli" {
		t.Errorf("заголовок без срока %q", got)
	}
}

// Сквозной путь целиком, как в qwdtt-cli connect: событие ядра → диспетчер → окно →
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

// Окно через прокси qwdtt-cli (plan-e3.md, W2): страница грузится только через
// него. Диалер уводит id.vk.com:443 на стенд с TLS — токен со стенда
// значит, что окно ходило через прокси, а не к настоящей VK. Сертификат
// стенда браузеру разрешается своим аргументом в той же переменной, что и
// прокси, — заодно видно, что значение пользователя не затирается.
func TestWebView2Proxy(t *testing.T) {
	s := newStandSolver(t)
	srv := httptest.NewTLSServer(standHandler())
	t.Cleanup(srv.Close)
	t.Setenv(browserArgsEnv, "--ignore-certificate-errors")

	s.opts.Logf = t.Logf
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
		// Пустой список — окно в прокси не пришло: браузер не получил
		// --proxy-server (так было, пока аргументы шли одной переменной).
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("Solve: %v; через прокси: %q", err, asked)
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
	value, restore := setBrowserArgs("--proxy-server=http://127.0.0.1:1")
	if got := os.Getenv(browserArgsEnv); got != "--proxy-server=http://127.0.0.1:1" || got != value {
		t.Errorf("без своего значения: %q, опцией %q", got, value)
	}
	restore()
	if _, ok := os.LookupEnv(browserArgsEnv); ok {
		t.Error("переменная осталась после restore")
	}

	t.Setenv(browserArgsEnv, "--lang=ru")
	value, restore = setBrowserArgs("--proxy-server=http://127.0.0.1:1")
	if got := os.Getenv(browserArgsEnv); got != "--lang=ru --proxy-server=http://127.0.0.1:1" || got != value {
		t.Errorf("со своим значением: %q, опцией %q", got, value)
	}
	restore()
	if got := os.Getenv(browserArgsEnv); got != "--lang=ru" {
		t.Errorf("своё значение после restore: %q", got)
	}
}
