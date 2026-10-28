// interceptor.go — перехватчик ответов API капчи для окна WebView2.
//
// В WebView2 через go-webview2 нет доступа к сети окна, как у CDP, поэтому
// ответы API страница отдаёт сама: скрипт, который окно выполняет на каждой
// новой странице до её собственного кода, оборачивает fetch и
// XMLHttpRequest. Ответ на запрос к …/method/captchaNotRobot.<метод> он
// передаёт в Go целиком, текстом, через функцию Bind — а что в нём значит,
// решает pageWatch (page.go), тот же, что у браузера по CDP. Скрипт ничего не
// решает и не трогает ни запрос, ни ответ: страница получает их как были.
//
// Перенесён из csqtt-cli. У ручного режима свой перехватчик (token.go): он
// печатает в консоль браузера только токен, слайдер и ошибку.
//
// Сам файл не зависит от Windows, чтобы скрипт проверялся в движке JS на
// любой системе (interceptor_test.go).
package captcha

import (
	"encoding/json"
	"strings"
)

// pageInterceptorSource — текст перехватчика; BIND — имя глобальной функции,
// через которую ответы уходят в Go: BIND(метод, тело).
const pageInterceptorSource = `(function () {
	var bind = BIND;
	var re = /\/method\/(captchaNotRobot\.[A-Za-z]+)/;
	function report(url, body) {
		var m = re.exec(String(url || ''));
		if (!m) { return; }
		try {
			var f = window[bind];
			if (typeof f === 'function') { f(m[1], String(body)); }
		} catch (e) {}
	}
	var origFetch = window.fetch;
	if (typeof origFetch === 'function') {
		window.fetch = function (input, init) {
			var url = (input && typeof input === 'object' && 'url' in input) ? input.url : input;
			return origFetch.apply(this, arguments).then(function (resp) {
				var where = (resp && resp.url) || url;
				if (re.test(String(where))) {
					try {
						resp.clone().text().then(function (t) { report(where, t); }, function () {});
					} catch (e) {}
				}
				return resp;
			});
		};
	}
	var X = window.XMLHttpRequest;
	if (X && X.prototype) {
		var origOpen = X.prototype.open, origSend = X.prototype.send;
		X.prototype.open = function (method, url) {
			this.__qwdttCliURL = url;
			return origOpen.apply(this, arguments);
		};
		X.prototype.send = function () {
			var xhr = this;
			if (re.test(String(xhr.__qwdttCliURL))) {
				xhr.addEventListener('load', function () {
					try {
						var t = xhr.responseType === '' || xhr.responseType === 'text' ?
							xhr.responseText : JSON.stringify(xhr.response);
						report(xhr.responseURL || xhr.__qwdttCliURL, t);
					} catch (e) {}
				});
			}
			return origSend.apply(this, arguments);
		};
	}
})();`

// pageInterceptorJS — перехватчик, отдающий ответы функции binding.
func pageInterceptorJS(binding string) string {
	name, _ := json.Marshal(binding) // строка маршалится всегда
	return strings.Replace(pageInterceptorSource, "BIND", string(name), 1)
}
