package captcha

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// Браузеру нужен рабочий стол и установленный Chromium, поэтому живой стенд
// включается руками:
//
//	QWDTT_BROWSER=1 go test ./internal/captcha -run Browser -v
//
// Он проверяет всё, кроме самой VK: запуск браузера, разговор по трубе,
// оба пути страницы (fetch и XMLHttpRequest), фрейм другого сайта, слайдер,
// отказ VK, закрытие окна, отмену по ctx, прокси и уборку профиля. Страницы
// делают запрос сами, без человека, так что окна мелькают и закрываются.

func TestFindBrowser(t *testing.T) {
	have := map[string]string{
		"chromium":           "/usr/bin/chromium",
		"yandex-browser":     "/usr/bin/yandex-browser",
		"/snap/bin/chromium": "/snap/bin/chromium",
	}
	look := func(name string) (string, error) {
		if path, ok := have[name]; ok {
			return path, nil
		}
		return "", errors.New("нет")
	}
	if got, err := findBrowser(look); err != nil || got != "/usr/bin/chromium" {
		t.Errorf("findBrowser = %q, %v; ожидался первый по порядку", got, err)
	}

	delete(have, "chromium")
	delete(have, "yandex-browser")
	if got, err := findBrowser(look); err != nil || got != "/snap/bin/chromium" {
		t.Errorf("findBrowser = %q, %v; ожидался chromium из snap", got, err)
	}

	clear(have)
	if _, err := findBrowser(look); !errors.Is(err, ErrNoBrowser) {
		t.Errorf("без браузеров: %v", err)
	}
}

func TestBrowserArgs(t *testing.T) {
	args := browserArgs("/tmp/profile", "")
	for _, want := range []string{"--remote-debugging-pipe", "--disable-blink-features=AutomationControlled",
		"--test-type", "--user-data-dir=/tmp/profile"} {
		if !slices.Contains(args, want) {
			t.Errorf("нет %s: %q", want, args)
		}
	}
	for _, arg := range args {
		// Первый выставляет navigator.webdriver, второй не покажет окна.
		if strings.HasPrefix(arg, "--enable-automation") || strings.HasPrefix(arg, "--headless") ||
			strings.HasPrefix(arg, "--remote-debugging-port") || strings.HasPrefix(arg, "--proxy-server") {
			t.Errorf("лишний аргумент %s", arg)
		}
	}
	if args[len(args)-1] != "about:blank" {
		t.Errorf("последним должен идти адрес: %q", args)
	}

	args = browserArgs("/tmp/profile", "127.0.0.1:8080")
	if !slices.Contains(args, "--proxy-server=http://127.0.0.1:8080") {
		t.Errorf("нет прокси: %q", args)
	}
}

func newBrowserStand(t *testing.T) *Browser {
	t.Helper()
	if os.Getenv("QWDTT_BROWSER") == "" {
		t.Skip("стенд браузера капчи включается переменной QWDTT_BROWSER=1")
	}
	b, err := NewBrowser(BrowserOptions{Out: io.Discard, Logf: t.Logf})
	if err != nil {
		t.Skipf("браузера не будет: %v", err)
	}
	return b
}

func TestBrowserToken(t *testing.T) {
	b := newBrowserStand(t)
	srv := stand(t)

	cases := []struct{ path, want string }{
		{"fetch", "token-fetch"},
		{"xhr", "token-xhr"},
		{"slider", "token-slider"},
		{"frame", "token-fetch"},
		{"webdriver", "token-wd-false"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			before := profilesNow()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			start := time.Now()
			token, err := b.Solve(ctx, standRequest(srv.URL+"/"+tc.path, 30*time.Second))
			if err != nil {
				t.Fatalf("Solve: %v", err)
			}
			if token != tc.want {
				t.Fatalf("токен %q, ожидался %q", token, tc.want)
			}
			t.Logf("токен за %s", time.Since(start).Round(time.Millisecond))
			waitProfilesGone(t, before)
		})
	}
}

func TestBrowserVKError(t *testing.T) {
	b := newBrowserStand(t)
	srv := stand(t)

	_, err := b.Solve(context.Background(), standRequest(srv.URL+"/error", 30*time.Second))
	if err == nil || !strings.Contains(err.Error(), "VK отказал") || !strings.Contains(err.Error(), "14") {
		t.Fatalf("ошибка %v, ожидался отказ VK с кодом 14", err)
	}
}

// По сроку браузер закрывается, и Solve дожидается уборки: окно не должно
// пережить qwdtt-cli.
func TestBrowserDeadline(t *testing.T) {
	b := newBrowserStand(t)
	srv := stand(t)
	before := profilesNow()
	var pid int
	b.started = func(p int) { pid = p }

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	start := time.Now()
	_, err := b.Solve(ctx, standRequest(srv.URL+"/idle", 4*time.Second))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ошибка %v, ожидался истёкший срок", err)
	}
	if took := time.Since(start); took > 4*time.Second+browserStop+time.Second {
		t.Fatalf("Solve вернулся через %s", took)
	}
	if pid != 0 && syscall.Kill(pid, 0) == nil {
		t.Errorf("браузер %d жив после Solve", pid)
	}
	waitProfilesGone(t, before)
}

// Человек закрыл окно — Chromium выходит с последним окном. Здесь его
// закрывает SIGTERM: выход тот же, штатный.
func TestBrowserClosedByUser(t *testing.T) {
	b := newBrowserStand(t)
	srv := stand(t)
	before := profilesNow()
	b.started = func(pid int) {
		time.AfterFunc(3*time.Second, func() { _ = syscall.Kill(pid, syscall.SIGTERM) })
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := b.Solve(ctx, standRequest(srv.URL+"/idle", 30*time.Second))
	if !errors.Is(err, ErrWindowClosed) {
		t.Fatalf("ошибка %v, ожидалось «окно капчи закрыто»", err)
	}
	if ctx.Err() != nil {
		t.Fatal("Solve дождался срока, а не закрытия окна")
	}
	waitProfilesGone(t, before)
}

// Браузер не запустился — ErrBrowser, и быстро: по нему CLI откроет ручной
// режим, пока у человека есть время.
func TestBrowserStartFailure(t *testing.T) {
	b := newBrowserStand(t)
	b.exe = "/bin/false"
	before := profilesNow()

	start := time.Now()
	_, err := b.Solve(context.Background(), standRequest("https://id.vk.com/", 30*time.Second))
	if !errors.Is(err, ErrBrowser) {
		t.Fatalf("ошибка %v, ожидалась ErrBrowser", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("отказ через %s", took)
	}
	waitProfilesGone(t, before)
}

// Сквозной путь, как в qwdtt-cli connect: событие ядра → диспетчер → браузер →
// токен в канале ядра.
func TestBrowserThroughDispatcher(t *testing.T) {
	b := newBrowserStand(t)
	srv := stand(t)
	d := newDispatcher(t, Options{Solver: b, Control: core.NewControl()}, time.Minute)

	ev := required("manual")
	ev.RedirectURI = srv.URL + "/fetch"
	d.Observe(ev)

	if got := answer(t, 20*time.Second); got != "token-fetch" {
		t.Fatalf("ядро получило %q, ожидался токен со стенда", got)
	}
}

// Браузер через прокси qwdtt-cli: страница грузится только через него.
// Диалер уводит id.vk.com:443 на стенд с TLS — токен со стенда значит, что
// браузер ходил через прокси, а не к настоящей VK.
func TestBrowserProxy(t *testing.T) {
	b := newBrowserStand(t)
	srv := httptest.NewTLSServer(standHandler())
	t.Cleanup(srv.Close)
	b.extraArgs = []string{"--ignore-certificate-errors"}

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
	token, err := b.Solve(ctx, standRequest("https://id.vk.com/fetch", 30*time.Second))
	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		t.Fatalf("Solve: %v; через прокси: %q", err, asked)
	}
	if token != "token-fetch" {
		t.Fatalf("токен %q, ожидался со стенда", token)
	}
	if len(asked) == 0 || asked[0] != "id.vk.com:443" {
		t.Errorf("через прокси: %q", asked)
	}
	waitProfilesGone(t, before)
}
