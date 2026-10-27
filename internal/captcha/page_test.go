package captcha

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAPIMethod(t *testing.T) {
	cases := map[string]string{
		"https://api.vk.ru/method/captchaNotRobot.check?v=5.131":        "captchaNotRobot.check",
		"https://api.vk.com/method/captchaNotRobot.initSession?v=5.131": "captchaNotRobot.initSession",
		"/method/captchaNotRobot.endSession":                            "captchaNotRobot.endSession",
		"https://st.vk.ru/entry.js":                                     "",
		"https://api.vk.ru/method/calls.getAnonymousToken":              "",
	}
	for url, want := range cases {
		got, ok := apiMethod(url)
		if got != want || ok != (want != "") {
			t.Errorf("apiMethod(%q) = %q, %v", url, got, ok)
		}
	}
}

// Ответы — в том виде, в каком их отдавал VK в живой записи csqtt-cli (без
// значений).
func TestPageWatch(t *testing.T) {
	const token = "выдуманный-success-token"
	cases := []struct {
		name    string
		method  string
		body    string
		token   string
		err     error
		logPart string
	}{
		{"initSession", methodInit, `{"response":{"app_id":1,"content_settings":[{"settings_key":"x","type":"slider"}],"show_captcha_type":"checkbox","status":"OK"}}`, "", nil, `"checkbox"`},
		{"settings", "captchaNotRobot.settings", `{"response":{"sensors_delay":200,"status":"OK"}}`, "", nil, "status=OK"},
		{"check токен без статуса", methodCheck, `{"response":{"success_token":"` + token + `"}}`, token, nil, ""},
		{"check OK", methodCheck, `{"response":{"redirect":"","show_captcha_type":"","status":"OK","success_token":"` + token + `"}}`, token, nil, "токен получен"},
		{"check ok строчными", methodCheck, `{"response":{"status":"ok","success_token":"` + token + `"}}`, token, nil, ""},
		{"check ERROR_LIMIT", methodCheck, `{"response":{"status":"ERROR_LIMIT"}}`, "", ErrLimit, "ERROR_LIMIT"},
		{"check BOT", methodCheck, `{"response":{"status":"BOT","show_captcha_type":"slider"}}`, "", nil, `"slider"`},
		{"check OK без токена", methodCheck, `{"response":{"status":"OK"}}`, "", nil, "ждём"},
		{"initSession ошибка VK", methodInit, `{"error":{"error_code":10,"error_msg":"Internal server error"}}`, "", ErrPageInit, "ошибка VK 10"},
		{"check ошибка VK", methodCheck, `{"error":{"error_code":100,"error_msg":"выдуманная"}}`, "", errVKRefused, "100"},
		{"другой метод, ошибка VK", "captchaNotRobot.settings", `{"error":{"error_code":100,"error_msg":"выдуманная"}}`, "", nil, "100"},
		{"мусор", methodCheck, `не JSON`, "", nil, "не разобран"},
		{"пустой JSON", methodCheck, `{}`, "", nil, "не разобран"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logged []string
			w := pageWatch{logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}
			out := w.observe(tc.method, []byte(tc.body))
			switch {
			case tc.token != "":
				if out == nil || out.token != tc.token || out.err != nil {
					t.Errorf("исход %+v, ожидался токен", out)
				}
			case tc.err == errVKRefused:
				if out == nil || out.err == nil || !strings.Contains(out.err.Error(), "VK отказал") {
					t.Errorf("исход %+v, ожидался отказ VK", out)
				}
			case tc.err != nil:
				if out == nil || !errors.Is(out.err, tc.err) {
					t.Errorf("исход %+v, ожидалась %v", out, tc.err)
				}
			default:
				if out != nil {
					t.Errorf("исход %+v, ожидалось ждать дальше", out)
				}
			}
			all := strings.Join(logged, "\n")
			if !strings.Contains(all, tc.logPart) {
				t.Errorf("в журнале нет %q: %q", tc.logPart, all)
			}
			if strings.Contains(all, token) {
				t.Errorf("токен в журнале: %q", all)
			}
		})
	}
}

// errVKRefused — в TestPageWatch: ожидается отказ VK («VK отказал: …»).
var errVKRefused = errors.New("отказ VK")

// В автофазе ошибка VK на check окно не закрывает: она — причина отдать
// капчу человеку, и её разбирает автоклик.
func TestPageWatchAutoPhase(t *testing.T) {
	auto := true
	var got []checkReply
	w := pageWatch{auto: func() bool { return auto }, checked: func(r checkReply) { got = append(got, r) }}
	body := []byte(`{"error":{"error_code":100,"error_msg":"выдуманная"}}`)
	if out := w.observe(methodCheck, body); out != nil {
		t.Errorf("в автофазе: исход %+v", out)
	}
	auto = false
	if out := w.observe(methodCheck, body); out == nil || out.err == nil {
		t.Errorf("после автофазы: исход %+v, ожидался отказ VK", out)
	}
	if len(got) != 2 {
		t.Errorf("checked: %+v", got)
	}
	// ERROR_LIMIT и ошибка initSession кончают окно и в автофазе.
	auto = true
	if out := w.observe(methodCheck, []byte(`{"response":{"status":"ERROR_LIMIT"}}`)); out == nil ||
		!errors.Is(out.err, ErrLimit) {
		t.Errorf("ERROR_LIMIT в автофазе: %+v", out)
	}
	if out := w.observe(methodInit, []byte(`{"error":{"error_code":10,"error_msg":"Internal server error"}}`)); out == nil ||
		!errors.Is(out.err, ErrPageInit) {
		t.Errorf("ошибка initSession в автофазе: %+v", out)
	}
}

// pageWatch.checked получает каждый ответ на check — им автоклик узнаёт исход
// своего клика; ответы других методов его не трогают.
func TestPageWatchChecked(t *testing.T) {
	cases := []struct {
		name   string
		method string
		body   string
		want   *checkReply
	}{
		{"OK с токеном", methodCheck, `{"response":{"status":"OK","success_token":"выдуманный"}}`,
			&checkReply{status: "OK", token: true}},
		{"BOT", methodCheck, `{"response":{"status":"BOT","show_captcha_type":"slider"}}`,
			&checkReply{status: "BOT", showType: "slider"}},
		{"ERROR_LIMIT", methodCheck, `{"response":{"status":"ERROR_LIMIT"}}`, &checkReply{status: "ERROR_LIMIT"}},
		{"ошибка VK", methodCheck, `{"error":{"error_code":100,"error_msg":"выдуманная"}}`,
			&checkReply{vkError: 100, vkMsg: "выдуманная"}},
		{"initSession", methodInit, `{"response":{"status":"OK","show_captcha_type":"checkbox"}}`, nil},
		{"мусор", methodCheck, `не JSON`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []checkReply
			w := pageWatch{checked: func(r checkReply) { got = append(got, r) }}
			w.observe(tc.method, []byte(tc.body))
			switch {
			case tc.want == nil && len(got) != 0:
				t.Errorf("checked позван: %+v", got)
			case tc.want != nil && (len(got) != 1 || got[0] != *tc.want):
				t.Errorf("checked: %+v, ожидалось %+v", got, *tc.want)
			}
		})
	}
}
