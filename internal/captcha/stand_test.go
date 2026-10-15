package captcha

// Стенд для живых тестов окна WebView2 и браузера под управлением: страницы,
// изображающие капчу VK, и проверка уборки временных профилей.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
			`<p>Стенд капчи qwdtt-cli</p><script>` + script + `</script>`
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
	mux.HandleFunc("/webdriver", func(w http.ResponseWriter, r *http.Request) {
		// Видна ли автоматика странице: токен «token-wd-false» — нет.
		io.WriteString(w, page(`fetch('`+check+`?via=wd-'+navigator.webdriver);`))
	})
	mux.HandleFunc("/frame", func(w http.ResponseWriter, r *http.Request) {
		// Проверка из фрейма другого сайта: 127.0.0.1 и localhost — разные
		// сайты, и Chromium держит такой фрейм в своём процессе.
		frame := "http://" + strings.Replace(r.Host, "127.0.0.1", "localhost", 1) + "/fetch"
		io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>стенд</title>`+
			`<p>Стенд капчи qwdtt-cli: фрейм</p><iframe src="`+frame+`"></iframe>`)
	})
	mux.HandleFunc("/idle", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, page(``))
	})

	return mux
}

// profilesNow — каталоги профилей, которые сейчас лежат во временном каталоге.
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
			t.Errorf("профиль капчи не убран за 15 с: %s", strings.Join(left, ", "))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func standRequest(url string, budget time.Duration) Request {
	return Request{Session: 1, Mode: ModeManual, RedirectURI: url, Deadline: time.Now().Add(budget)}
}
