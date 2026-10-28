package captcha

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Перехватчик окна WebView2 проверяется там, где ему предстоит работать, — в
// движке JS: разбор ответов в Go может быть безупречен, а скрипт — не
// выполняться вовсе, и узнать об этом на живой капче под таймером было бы
// поздно. Окружение браузера здесь — заглушки fetch и XMLHttpRequest ровно
// настолько, насколько их трогает перехватчик. Без node тест пропускается.
func TestPageInterceptorRunsInJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node не найден — проверка перехватчика пропущена")
	}

	harness := `
const bodies = {
	'https://api.example.test/method/captchaNotRobot.initSession?v=5.131': '{"response":{"status":"OK","show_captcha_type":"checkbox"}}',
	'https://api.example.test/method/captchaNotRobot.check?v=5.131': '{"response":{"status":"OK","success_token":"js-token"}}',
	'https://static.example.test/other.json': '{"x":1}',
};
const seen = [];
const pageGot = [];
globalThis.window = globalThis;
window.__bind = (method, body) => seen.push(method + ' ' + body);
globalThis.fetch = async (input) => {
	const url = typeof input === 'object' ? input.url : input;
	const text = bodies[url];
	return {url, clone: () => ({text: async () => text}), text: async () => text};
};
// open и send обязаны лежать на прототипе: перехватчик подменяет именно его.
globalThis.XMLHttpRequest = function () { this.listeners = {}; this.responseType = ''; };
XMLHttpRequest.prototype.addEventListener = function (name, fn) { this.listeners[name] = fn; };
XMLHttpRequest.prototype.open = function (m, url) { this.url = url; };
XMLHttpRequest.prototype.send = function () {
	this.responseText = bodies[this.url];
	this.responseURL = this.url;
	if (this.listeners.load) { this.listeners.load(); }
	if (this.onload) { this.onload(); }
};
` + pageInterceptorJS("__bind") + `
(async () => {
	const r = await fetch('https://api.example.test/method/captchaNotRobot.initSession?v=5.131', {method: 'POST'});
	pageGot.push(await r.text());
	await fetch({url: 'https://api.example.test/method/captchaNotRobot.check?v=5.131'});
	await fetch('https://static.example.test/other.json');
	await new Promise(r => setTimeout(r, 10));
	const xhr = new XMLHttpRequest();
	xhr.onload = () => pageGot.push(xhr.responseText);
	xhr.open('POST', 'https://api.example.test/method/captchaNotRobot.check?v=5.131');
	xhr.send('x=1');
	process.stdout.write(seen.join('\n') + '\n--\n' + pageGot.join('\n'));
})();
`
	path := filepath.Join(t.TempDir(), "harness.js")
	if err := os.WriteFile(path, []byte(harness), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, path).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}

	got, page, _ := strings.Cut(string(out), "\n--\n")
	want := strings.Join([]string{
		`captchaNotRobot.initSession {"response":{"status":"OK","show_captcha_type":"checkbox"}}`,
		`captchaNotRobot.check {"response":{"status":"OK","success_token":"js-token"}}`,
		`captchaNotRobot.check {"response":{"status":"OK","success_token":"js-token"}}`,
	}, "\n")
	if got != want {
		t.Errorf("перехватчик отдал:\n%s\nожидалось:\n%s", got, want)
	}
	// Страница получает свои ответы нетронутыми.
	if !strings.Contains(page, `"show_captcha_type":"checkbox"`) || !strings.Contains(page, `"js-token"`) {
		t.Errorf("страница получила:\n%s", page)
	}
}

func TestPageInterceptorBindingName(t *testing.T) {
	const name = "__qwdttCliCaptchaReply"
	js := pageInterceptorJS(name)
	if !strings.Contains(js, `var bind = "`+name+`";`) || strings.Contains(js, "BIND") {
		t.Errorf("имя функции не подставлено:\n%s", js)
	}
}
