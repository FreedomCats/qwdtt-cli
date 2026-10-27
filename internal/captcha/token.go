// token.go — то, что знают про капчу все решатели сразу: как выглядит ответ VK
// и как заставить страницу отдать токен.
//
// Файл общий намеренно. Токен добывается из одного и того же места тремя
// способами — консольным (manual.go), окном WebView2 и браузером под
// управлением по CDP (doc/e2-captcha.md), — и знание об ответе VK должно жить
// в одном экземпляре, иначе оно разъедется.
package captcha

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrSlider — VK показал слайдер вместо чекбокса. Отдельная ошибка, потому что
// у неё другой смысл: решать можно и нужно, просто дольше. Ядро на Android
// по этому признаку переключало стратегию (CaptchaWebViewManager.kt,
// ERROR_SLIDER_DETECTED).
var ErrSlider = errors.New("VK показал слайдер")

// ErrNoToken — в ответе не нашлось ни токена, ни внятной ошибки.
var ErrNoToken = errors.New("в ответе VK нет success_token")

// ErrWindowClosed — человек закрыл окно капчи, не решив её. Ядро получит отказ
// сразу и пойдёт дальше, а не выстоит весь таймаут.
var ErrWindowClosed = errors.New("окно капчи закрыто")

// outcome — чем кончилось окно капчи: токеном или ошибкой.
type outcome struct {
	token string
	err   error
}

// checkResponse — ответ VK на captchaNotRobot.check (и другие методы API
// капчи, page.go). Разобраны только те поля, которые что-то значат для
// решателя; остальное VK волен менять. Response nil — его в ответе нет.
type checkResponse struct {
	Response *struct {
		Status          string `json:"status"`
		SuccessToken    string `json:"success_token"`
		ShowCaptchaType string `json:"show_captcha_type"`
	} `json:"response"`
	Error json.RawMessage `json:"error"`
}

// successTokenFrom достаёт токен из тела ответа на captchaNotRobot.check.
//
// Три исхода, ровно как у перехватчика на Android: токен, слайдер, ошибка VK.
func successTokenFrom(body []byte) (string, error) {
	var parsed checkResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("ответ VK не разбирается как JSON: %w", err)
	}

	resp := parsed.Response
	switch {
	case resp != nil && resp.SuccessToken != "":
		return resp.SuccessToken, nil
	case resp != nil && resp.ShowCaptchaType == "slider":
		return "", ErrSlider
	case len(parsed.Error) > 0:
		return "", fmt.Errorf("VK отказал: %s", strings.TrimSpace(string(parsed.Error)))
	default:
		return "", ErrNoToken
	}
}

// interceptorJS — перехватчик ответа captchaNotRobot.check.
//
// Тот же приём, что на Android (CaptchaWebViewManager.kt:70–125): страница
// ходит за проверкой через fetch или XMLHttpRequest, и подменённые обёртки
// заглядывают в ответ по дороге. Другого способа нет — токен приходит в теле,
// а не в адресе, и страница его нигде не показывает.
//
// %s — имя глобальной функции, которой отдаётся результат; она зовётся как
// f(kind, value), где kind это "token", "slider" или "error". Кто её заводит,
// решает вызывающий: окно WebView2 — через Bind, консоль — определением рядом.
//
// Формат менять с оглядкой: oneLine превращает этот текст в одну строку,
// разбивая по пробелам, поэтому здесь не должно быть ни комментариев «//»,
// ни строковых литералов с пробелами внутри.
const interceptorJS = `
(function () {
	if (window.__qwdttCliCaptchaInstalled) { return; }
	window.__qwdttCliCaptchaInstalled = true;
	var sink = %s;
	var matches = function (target) {
		var url = (typeof target === 'string') ? target : (target && target.url);
		return typeof url === 'string' && url.indexOf('captchaNotRobot.check') >= 0;
	};
	var report = function (text) {
		try {
			var data = JSON.parse(text);
			if (data.response && data.response.success_token) {
				sink('token', data.response.success_token);
			} else if (data.response && data.response.show_captcha_type === 'slider') {
				sink('slider', '');
			} else if (data.error) {
				sink('error', JSON.stringify(data.error));
			}
		} catch (e) {}
	};
	var origFetch = window.fetch;
	window.fetch = function () {
		var args = arguments;
		var promise = origFetch.apply(this, args);
		if (matches(args[0])) {
			promise.then(function (resp) {
				resp.clone().text().then(report);
			});
		}
		return promise;
	};
	var origOpen = XMLHttpRequest.prototype.open;
	var origSend = XMLHttpRequest.prototype.send;
	XMLHttpRequest.prototype.open = function (method, url) {
		this.__qwdttCliURL = url;
		return origOpen.apply(this, arguments);
	};
	XMLHttpRequest.prototype.send = function () {
		var xhr = this;
		if (matches(xhr.__qwdttCliURL)) {
			xhr.addEventListener('load', function () { report(xhr.responseText); });
		}
		return origSend.apply(this, arguments);
	};
})();
`

// oneLine собирает из перехватчика строку, которую можно вставить в консоль
// браузера одним куском.
//
// Схлопывание по пробелам законно ровно потому, что в interceptorJS нет ни
// комментариев «//», которые съели бы остаток строки, ни литералов с пробелами.
func oneLine(js string) string {
	return strings.Join(strings.Fields(js), " ")
}

// consoleSnippet — перехватчик для консоли браузера: сам заводит приёмник,
// который печатает найденное строкой вида «qwdtt-cli-captcha token <токен>».
//
// Пробелов внутри строковых литералов здесь нет ни одного, и это не
// придирчивость: их бы схлопнул oneLine.
func consoleSnippet() string {
	const sink = `window.__qwdttCliCaptchaSink`
	prologue := sink + `=function(k,v){console.log('qwdtt-cli-captcha',k,v)};`
	return oneLine(prologue + fmt.Sprintf(interceptorJS, sink))
}
