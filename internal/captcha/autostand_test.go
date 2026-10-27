package captcha

// Стенд автоклика (перенесён из csqtt-cli): страница, изображающая капчу VK
// так, как её видит окно с автокликом, и её API. Поведение — по живой записи
// (page.go): страница зовёт initSession, settings, componentDone, потом
// «человек ставит галочку» — check, после токена — endSession. Человека нет:
// страница делает всё сама или по зову теста (standHuman), поэтому окна
// мелькают и закрываются. Простой стенд окон — stand_test.go.
//
// Варианты (параметр scenario):
//
//	checkbox — галочка → OK с токеном;
//	slider   — галочка → BOT (тип slider), «слайдер» → OK с токеном;
//	limit    — галочка → ERROR_LIMIT;
//	initerror — initSession отвечает ошибкой VK 10, как было живьём;
//	idle     — до галочки дело не доходит (закрытие окна, срок);
//	webdriver — токен говорит, видит ли страница navigator.webdriver;
//	auto     — галочка VK для автоклика: check — по первому клику, с тем,
//	           доверенный ли он и сколько было движений мыши → OK с токеном;
//	autobot  — то же, но check → BOT (слайдер), страница показывает слайдер;
//	           человек (standHuman) → OK с токеном;
//	autonone — галочки нет вовсе (только приманки для описания страницы:
//	           кнопка в теневом корне, фрейм чужого происхождения с запросом
//	           в адресе); человек (standHuman) → OK с токеном;
//	autoplain  — как auto, но галочка без признаков VK: спрятанный
//	             input[type=checkbox] в label с текстом «Я не робот»;
//	autoshadow — галочка [role=checkbox] в открытом теневом корне;
//	autoframe  — галочка input[type=checkbox] во фрейме того же
//	             происхождения, фрейм сдвинут в окне;
//	autotext   — ни флажка, ни роли: только элемент с текстом «Я не робот»;
//	autolimit  — как auto, но check → ERROR_LIMIT;
//	autovkerror — как auto, но check по клику → ошибка VK; человек → OK;
//	autoiniterror — как auto, но initSession → ошибка VK 10.
//
// via=xhr — те же запросы через XMLHttpRequest вместо fetch; delay=<срок> —
// страница отвечает через столько (медленная сеть).

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// standPage — страница капчи стенда. API она зовёт по относительному адресу
// /method/…: стенд — один сервер, а через прокси — «id.vk.com».
const standPage = `<!doctype html><meta charset="utf-8"><title>стенд капчи</title>
<p>Стенд капчи qwdtt-cli</p>
<script>
var q = new URLSearchParams(location.search);
var scenario = q.get('scenario') || 'checkbox', via = q.get('via') || 'fetch';
function api(method, extra) {
	var url = '/method/captchaNotRobot.' + method + '?v=5.131&scenario=' + scenario + '&via=' + via + (extra || '');
	if (via === 'xhr') {
		return new Promise(function (resolve) {
			var x = new XMLHttpRequest();
			x.open('POST', url);
			x.onload = function () { resolve(JSON.parse(x.responseText)); };
			x.send('session_token=stand');
		});
	}
	return fetch(url, {method: 'POST', body: 'session_token=stand'}).then(function (r) { return r.json(); });
}
function later(f) { setTimeout(f, 300); }
if (scenario.indexOf('auto') === 0) { autoStand(); } else {
api('initSession').then(function () { return api('settings'); }).then(function () {
	return api('componentDone');
}).then(function () {
	if (scenario === 'idle') { return; }
	later(function () {
		api('check', '&attempt=1&wd=' + navigator.webdriver + '&dpr=' + devicePixelRatio).then(function (r) {
			var st = r.response && r.response.status;
			if (st === 'BOT') {
				later(function () {
					api('check', '&attempt=2').then(function (r2) {
						if (r2.response && r2.response.status === 'OK') { api('endSession'); }
					});
				});
			} else if (st === 'OK') {
				api('endSession');
			}
		});
	});
});
}
// Галочка для автоклика — с классами и атрибутами VK (или без них, см.
// autoBox). check уходит по первому клику и несёт, был ли клик доверенным,
// сколько доверенных движений мыши было до него, видна ли страница и сколько
// кадров она нарисовала за последнюю секунду. Человека изображает
// standHuman(): тест зовёт его, когда окно показано человеку.
function autoStand() {
	var moves = 0, frames = [], sent = false, made = null;
	function onMove(e) { if (e.isTrusted) { moves++; } }
	function onClick(e) {
		if (sent) { return; }
		sent = true;
		api('check', '&attempt=1&trusted=' + e.isTrusted + '&moves=' + moves + '&vis=' + document.visibilityState +
			'&fps=' + fps() + '&focus=' + document.hasFocus() + '&dpr=' + devicePixelRatio).then(function (r) {
			var st = r.response && r.response.status;
			if (st === 'OK') { api('endSession'); return; }
			if (st === 'ERROR_LIMIT' || r.error) { return; }
			made.el.remove();
			var slider = document.createElement('div');
			slider.className = 'vkc__SliderCaptcha-module__description';
			slider.textContent = 'Сдвиньте ползунок';
			document.body.appendChild(slider);
		});
	}
	function watch(doc) {
		doc.addEventListener('mousemove', onMove, true);
		doc.addEventListener('click', onClick, true);
	}
	watch(document);
	(function frame(t) { frames.push(performance.now()); requestAnimationFrame(frame); })();
	function fps() {
		var now = performance.now();
		return frames.filter(function (t) { return now - t < 1000; }).length;
	}
	window.standHuman = function () {
		api('check', '&attempt=2&human=1&dpr=' + devicePixelRatio).then(function (r) {
			if (r.response && r.response.status === 'OK') { api('endSession'); }
		});
	};
	api('initSession').then(function () { return api('settings'); }).then(function () {
		return api('componentDone');
	}).then(function () {
		if (scenario === 'autonone') { decoys(); return; }
		setTimeout(function () {
			made = autoBox();
			document.body.appendChild(made.el);
			if (made.ready) { made.ready(watch); }
		}, +(q.get('boxdelay') || 0));
	});
}
var boxStyle = 'display:inline-block;margin:120px 40px;padding:14px 18px;border:1px solid #888';
// autoBox — галочка варианта: el — что вставить в страницу; ready(watch) —
// после вставки, для фрейма: следить и за его документом.
function autoBox() {
	var el;
	switch (scenario) {
	case 'autoplain':
		el = document.createElement('label');
		el.style.cssText = boxStyle;
		el.innerHTML = '<input type="checkbox" style="position:absolute;opacity:0;width:1px;height:1px">' +
			'<span>Я не робот</span>';
		return {el: el};
	case 'autoshadow':
		el = document.createElement('div');
		el.attachShadow({mode: 'open'}).innerHTML = '<div role="checkbox" aria-checked="false" tabindex="0" ' +
			'style="' + boxStyle + '">Я не робот</div>';
		return {el: el};
	case 'autotext':
		el = document.createElement('div');
		el.style.cssText = boxStyle;
		el.innerHTML = '<span style="display:inline-block;width:16px;height:16px;border:1px solid #888"></span> ' +
			'<span>Я не робот</span>';
		return {el: el};
	case 'autoframe':
		el = document.createElement('iframe');
		el.style.cssText = 'display:block;margin:90px 0 0 60px;width:340px;height:200px;border:2px solid #888';
		return {el: el, ready: function (watch) {
			var doc = el.contentDocument;
			doc.open();
			doc.write('<!doctype html><meta charset="utf-8"><body style="margin:0">' +
				'<label style="display:inline-block;margin:70px 0 0 90px;padding:14px 18px">' +
				'<input type="checkbox"> Я не робот</label></body>');
			doc.close();
			watch(doc);
		}};
	}
	el = document.createElement('label');
	el.className = 'vkc__Checkbox-module__Checkbox';
	el.htmlFor = 'not-robot-captcha-checkbox';
	el.style.cssText = boxStyle;
	el.innerHTML = '<input type="checkbox" id="not-robot-captcha-checkbox"> Я не робот';
	return {el: el};
}
// decoys — не галочки: кнопка в теневом корне и фрейм чужого происхождения
// (127.0.0.1 и localhost — разные происхождения) с запросом в адресе.
function decoys() {
	var host = document.createElement('div');
	host.attachShadow({mode: 'open'}).innerHTML = '<button>Обновить</button>';
	document.body.appendChild(host);
	var f = document.createElement('iframe');
	f.src = location.protocol + '//' + location.host.replace('127.0.0.1', 'localhost') + '/empty?session_token=decoy';
	document.body.appendChild(f);
}
</script>`

// captchaStand — сервер стенда; calls — какие методы API звала страница.
type captchaStand struct {
	*httptest.Server

	mu     sync.Mutex
	calls  []string
	checks []url.Values // запросы check: что страница сообщила о клике
}

func newCaptchaStand(t *testing.T) *captchaStand {
	t.Helper()
	s := &captchaStand{}
	s.Server = httptest.NewServer(s.handler())
	t.Cleanup(s.Close)
	return s
}

// newCaptchaTLSStand — то же по HTTPS: для проверки прокси.
func newCaptchaTLSStand(t *testing.T) *captchaStand {
	t.Helper()
	s := &captchaStand{}
	s.Server = httptest.NewTLSServer(s.handler())
	t.Cleanup(s.Close)
	return s
}

// standToken — токен, который стенд отдаёт в варианте scenario.
func standToken(scenario, via string) string { return "stand-token-" + scenario + "-" + via }

func (s *captchaStand) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/not_robot_captcha", func(w http.ResponseWriter, r *http.Request) {
		if d, err := time.ParseDuration(r.URL.Query().Get("delay")); err == nil {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, standPage)
	})
	mux.HandleFunc("/frame", func(w http.ResponseWriter, r *http.Request) {
		// Капча во фрейме другого сайта: 127.0.0.1 и localhost — разные
		// сайты, и Chromium держит такой фрейм в своём процессе.
		frame := "http://" + strings.Replace(r.Host, "127.0.0.1", "localhost", 1) +
			"/not_robot_captcha?" + r.URL.RawQuery
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><meta charset="utf-8"><title>стенд</title>`+
			`<p>Стенд капчи qwdtt-cli: фрейм</p><iframe src="`+frame+`"></iframe>`)
	})
	mux.HandleFunc("/empty", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><meta charset="utf-8"><p>пустой фрейм</p>`)
	})
	mux.HandleFunc("/method/", func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/method/captchaNotRobot.")
		q := r.URL.Query()
		s.mu.Lock()
		s.calls = append(s.calls, method)
		if method == "check" {
			s.checks = append(s.checks, q)
		}
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		switch scenario := q.Get("scenario"); {
		case method == "initSession" && (scenario == "initerror" || scenario == "autoiniterror"):
			io.WriteString(w, `{"error":{"error_code":10,"error_msg":"Internal server error"}}`)
			return
		case method == "check" && scenario == "autovkerror" && q.Get("attempt") == "1":
			io.WriteString(w, `{"error":{"error_code":100,"error_msg":"выдуманная ошибка стенда"}}`)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"response": standReply(method, q)})
	})
	return mux
}

// standReply — ответ API стенда в том виде, в каком его отдаёт VK.
func standReply(method string, q url.Values) map[string]any {
	scenario, via := q.Get("scenario"), q.Get("via")
	switch method {
	case "initSession":
		return map[string]any{"status": "OK", "show_captcha_type": "checkbox",
			"content_settings": []any{map[string]any{"type": "slider", "settings_key": "stand"}}}
	case "settings":
		return map[string]any{"status": "OK", "sensors_delay": 200}
	case "check":
		ok := func(token string) map[string]any {
			return map[string]any{"status": "OK", "success_token": token, "redirect": "", "show_captcha_type": ""}
		}
		switch scenario {
		case "slider":
			if q.Get("attempt") == "1" {
				return map[string]any{"status": "BOT", "show_captcha_type": "slider"}
			}
			return ok(standToken(scenario, via))
		case "limit", "autolimit":
			return map[string]any{"status": "ERROR_LIMIT"}
		case "autobot":
			if q.Get("attempt") == "1" {
				return map[string]any{"status": "BOT", "show_captcha_type": "slider"}
			}
		case "webdriver":
			return ok("stand-token-wd-" + q.Get("wd"))
		}
		return ok(standToken(scenario, via))
	}
	return map[string]any{"status": "OK"}
}

// methods — какие методы API звала страница.
func (s *captchaStand) methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// checkQueries — запросы check, которые слала страница.
func (s *captchaStand) checkQueries() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]url.Values(nil), s.checks...)
}

// called — сколько раз страница звала метод API.
func (s *captchaStand) called(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.calls {
		if m == method {
			n++
		}
	}
	return n
}

// page — адрес страницы капчи стенда с вариантом.
func (s *captchaStand) page(scenario, via string) string {
	return s.URL + "/not_robot_captcha?domain=vk.com&session_token=stand&variant=popup&blank=1&scenario=" +
		scenario + "&via=" + via
}

// standAutoRequest — запрос режима wv: окно сначала пробует автоклик.
func standAutoRequest(url string, budget time.Duration) Request {
	r := standRequest(url, budget)
	r.Mode = ModeSelected
	return r
}

// syncBuffer — вывод, в который пишут горутины окна.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
