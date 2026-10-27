// page.go — что делает страница капчи VK и как по её ответам понять исход.
//
// Страница id.vk.com/not_robot_captcha ходит в API запросами
// POST …/method/captchaNotRobot.<метод> (живая запись csqtt-cli):
//
//   - initSession — status=OK и show_captcha_type: какую капчу показать
//     (обычно checkbox). Бывает и ошибка VK (живьём — 10, Internal server
//     error): капчи тогда нет, страница показывает ошибку, и окно
//     закрывается сразу (ErrPageInit);
//   - settings, componentDone — подготовка;
//   - check — ответ человека или автоклика. Токен — response.success_token.
//     ERROR_LIMIT — попытки на эту сессию кончились, страница пишет «Лимит
//     попыток исчерпан», и окно закрывается (ErrLimit). BOT — VK не поверил
//     галочке, и страница переходит к слайдеру: ждём дальше;
//   - endSession — после токена;
//   - refreshSession — через несколько минут без действий.
//
// Здесь разбираются ответы, которые видит окно CDP (cdp.go) сетью браузера, —
// всё, что нужно автоклику (autoclick.go). Ручной режим и окно WebView2 берут
// из того же ответа только токен, слайдер и ошибку (successTokenFrom и
// перехватчик JS в token.go); форма ответа — одна, checkResponse.
package captcha

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// API-методы страницы капчи и статусы check, которые что-то значат для исхода.
const (
	methodInit  = "captchaNotRobot.initSession"
	methodCheck = "captchaNotRobot.check"
	statusLimit = "ERROR_LIMIT"
)

var (
	// ErrLimit — VK ответил на check ERROR_LIMIT: попытки на эту сессию
	// кончились, решать в окне больше нечего.
	ErrLimit = errors.New("VK: лимит попыток капчи исчерпан")
	// ErrPageInit — VK ответил ошибкой на initSession: капчи на странице не
	// будет, человеку в окне делать нечего.
	ErrPageInit = errors.New("VK не открыл капчу")
)

// apiMethodRE — метод API капчи в адресе запроса.
var apiMethodRE = regexp.MustCompile(`/method/(captchaNotRobot\.[A-Za-z]+)`)

// apiMethod — метод API капчи по адресу запроса; ok=false — запрос не к нему.
func apiMethod(url string) (method string, ok bool) {
	m := apiMethodRE.FindStringSubmatch(url)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// vkError — ошибка VK в ответе API.
type vkError struct {
	Code    int    `json:"error_code"`
	Message string `json:"error_msg"`
}

// parseReply разбирает ответ API капчи; ok=false — это не ответ VK.
func parseReply(body []byte) (r checkResponse, vkErr *vkError, ok bool) {
	if json.Unmarshal(body, &r) != nil || (r.Response == nil && len(r.Error) == 0) {
		return checkResponse{}, nil, false
	}
	if len(r.Error) > 0 {
		vkErr = &vkError{}
		if json.Unmarshal(r.Error, vkErr) != nil {
			vkErr = &vkError{Message: strings.TrimSpace(string(r.Error))}
		}
	}
	return r, vkErr, true
}

// pageWatch разбирает ответы API, которые окно увидело на странице капчи, и
// пишет в журнал, что происходит: какой метод, статус и тип капчи. Токен в
// журнал не попадает.
type pageWatch struct {
	logf func(string, ...any)
	// checked, если задан, получает каждый ответ на check — и с исходом, и
	// без: по нему автоклик узнаёт, поверил ли VK галочке (autoclick.go).
	checked func(checkReply)
	// auto, если задан и отвечает «да», — идёт автофаза: ошибка VK на check
	// окно не закрывает, а уходит автоклику причиной отдать капчу человеку.
	// Без автофазы такая ошибка кончает окно, как в перехватчике JS.
	auto func() bool
}

// checkReply — что VK ответил на check, без токена.
type checkReply struct {
	status   string
	showType string
	token    bool   // в ответе есть токен
	vkError  int    // код ошибки VK; 0 — ответ без ошибки
	vkMsg    string // её текст
}

func (w pageWatch) notify(r checkReply) {
	if w.checked != nil {
		w.checked(r)
	}
}

// observe — один ответ API. Исход не nil — окно можно закрывать: токен,
// ErrLimit, ErrPageInit или отказ VK на check вне автофазы.
func (w pageWatch) observe(method string, body []byte) *outcome {
	logf := w.logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	r, vkErr, ok := parseReply(body)
	if !ok {
		logf("[CAPTCHA] страница: %s — ответ не разобран", method)
		return nil
	}
	if vkErr != nil {
		logf("[CAPTCHA] страница: %s — ошибка VK %d: %s", method, vkErr.Code, vkErr.Message)
		switch method {
		case methodInit:
			return &outcome{err: fmt.Errorf("%w: initSession — ошибка VK %d: %s", ErrPageInit,
				vkErr.Code, vkErr.Message)}
		case methodCheck:
			w.notify(checkReply{vkError: vkErr.Code, vkMsg: vkErr.Message})
			if w.auto != nil && w.auto() {
				return nil
			}
			return &outcome{err: fmt.Errorf("VK отказал: ошибка %d: %s", vkErr.Code, vkErr.Message)}
		}
		return nil
	}
	if r.Response == nil {
		return nil
	}
	status, showType := r.Response.Status, r.Response.ShowCaptchaType
	token := strings.TrimSpace(r.Response.SuccessToken)
	switch method {
	case methodInit:
		logf("[CAPTCHA] страница: %s — status=%s, тип капчи %q", method, status, showType)
	case methodCheck:
		w.notify(checkReply{status: status, showType: showType, token: token != ""})
		switch {
		case token != "":
			logf("[CAPTCHA] страница: %s — status=%s, токен получен (%d зн.)", method, status, len(token))
			return &outcome{token: token}
		case strings.EqualFold(status, statusLimit):
			logf("[CAPTCHA] страница: %s — status=%s: %v", method, status, ErrLimit)
			return &outcome{err: ErrLimit}
		default:
			logf("[CAPTCHA] страница: %s — status=%s, тип капчи %q: ждём решения в окне", method, status, showType)
		}
	default:
		logf("[CAPTCHA] страница: %s — status=%s", method, status)
	}
	return nil
}
