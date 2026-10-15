package captcha

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

const (
	testCaptchaURL = "https://id.vk.com/not_robot_captcha?session_token=выдуманный"
	testCheckURL   = "https://api.vk.com/method/captchaNotRobot.check?v=5.207"
	testToken      = "выдуманный-success-token-0123456789"
)

// tokenBody — ответ VK с токеном.
var tokenBody = `{"response":{"status":"OK","success_token":"` + testToken + `"}}`

// msg собирает сообщение браузера из JSON.
func msg(t *testing.T, raw string) cdpMessage {
	t.Helper()
	var m cdpMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("сообщение %s: %v", raw, err)
	}
	return m
}

// replyTo — ответ браузера на команду.
func replyTo(cmd cdpCommand, result string) cdpMessage {
	return cdpMessage{ID: cmd.ID, SessionID: cmd.SessionID, Result: json.RawMessage(result)}
}

// params — параметры команды как JSON, чтобы сверять их строкой.
func params(t *testing.T, cmd cdpCommand) string {
	t.Helper()
	data, err := json.Marshal(cmd.Params)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func methods(cmds []cdpCommand) []string {
	var out []string
	for _, c := range cmds {
		out = append(out, c.Method)
	}
	return out
}

// mustStep отдаёт автомату сообщение и требует, чтобы разговор продолжился.
func mustStep(t *testing.T, c *cdpCapture, m cdpMessage) []cdpCommand {
	t.Helper()
	cmds, out := c.handle(m)
	if out != nil {
		t.Fatalf("разговор кончился раньше времени: %+v", *out)
	}
	return cmds
}

// openPage проводит автомат до открытой страницы капчи и возвращает сессию
// вкладки.
func openPage(t *testing.T, c *cdpCapture) string {
	t.Helper()
	start := c.start()
	if got := methods(start); len(got) != 1 || got[0] != "Target.getTargets" || start[0].SessionID != "" {
		t.Fatalf("начало: %v", got)
	}

	cmds := mustStep(t, c, replyTo(start[0], `{"targetInfos":[
		{"targetId":"BROWSER","type":"browser"},
		{"targetId":"PAGE1","type":"page","url":"about:blank"}]}`))
	if len(cmds) != 1 || cmds[0].Method != "Target.attachToTarget" ||
		params(t, cmds[0]) != `{"flatten":true,"targetId":"PAGE1"}` {
		t.Fatalf("подключение к вкладке: %+v", cmds)
	}

	setup := mustStep(t, c, replyTo(cmds[0], `{"sessionId":"S1"}`))
	want := []string{"Network.enable", "Target.setAutoAttach", "Page.navigate"}
	if got := methods(setup); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("подготовка вкладки: %v, ожидалось %v", got, want)
	}
	for _, cmd := range setup {
		if cmd.SessionID != "S1" {
			t.Errorf("%s ушла не в сессию вкладки: %q", cmd.Method, cmd.SessionID)
		}
	}
	if p := params(t, setup[2]); !strings.Contains(p, `"url":"https://id.vk.com/not_robot_captcha`) {
		t.Errorf("Page.navigate: %s", p)
	}

	for _, cmd := range setup[:2] {
		mustStep(t, c, replyTo(cmd, `{}`))
	}
	mustStep(t, c, replyTo(setup[2], `{"frameId":"F1","loaderId":"L1"}`))
	if !c.ready {
		t.Fatal("после Page.navigate страница не считается открытой")
	}
	return "S1"
}

// checkFinished проводит один ответ VK: responseReceived, loadingFinished,
// getResponseBody. Возвращает команду за телом.
func checkFinished(t *testing.T, c *cdpCapture, session, request string) cdpCommand {
	t.Helper()
	mustStep(t, c, msg(t, fmt.Sprintf(`{"method":"Network.responseReceived","sessionId":%q,
		"params":{"requestId":%q,"type":"Fetch","response":{"url":%q,"status":200}}}`, session, request, testCheckURL)))
	cmds := mustStep(t, c, msg(t, fmt.Sprintf(`{"method":"Network.loadingFinished","sessionId":%q,
		"params":{"requestId":%q}}`, session, request)))
	if len(cmds) != 1 || cmds[0].Method != "Network.getResponseBody" || cmds[0].SessionID != session ||
		params(t, cmds[0]) != fmt.Sprintf(`{"requestId":%q}`, request) {
		t.Fatalf("за телом ответа: %+v", cmds)
	}
	return cmds[0]
}

func bodyReply(cmd cdpCommand, body string, b64 bool) cdpMessage {
	if b64 {
		body = base64.StdEncoding.EncodeToString([]byte(body))
	}
	data, _ := json.Marshal(map[string]any{"body": body, "base64Encoded": b64})
	return replyTo(cmd, string(data))
}

func TestCDPCaptureToken(t *testing.T) {
	for _, b64 := range []bool{false, true} {
		t.Run(fmt.Sprintf("base64=%v", b64), func(t *testing.T) {
			c := newCDPCapture(testCaptchaURL, nil)
			s := openPage(t, c)
			get := checkFinished(t, c, s, "R1")
			_, out := c.handle(bodyReply(get, tokenBody, b64))
			if out == nil || out.err != nil || out.token != testToken {
				t.Fatalf("исход: %+v", out)
			}
		})
	}
}

// Слайдер, пустой ответ и мусор разговор не кончают: человек решает дальше
// в том же окне. Отказ VK — кончает.
func TestCDPCaptureBodies(t *testing.T) {
	c := newCDPCapture(testCaptchaURL, nil)
	s := openPage(t, c)

	for i, body := range []string{
		`{"response":{"show_captcha_type":"slider"}}`,
		`{"response":{"status":"BOT"}}`,
		`не JSON`,
		``,
	} {
		get := checkFinished(t, c, s, fmt.Sprintf("R%d", i))
		mustStep(t, c, bodyReply(get, body, false))
	}

	// Браузер не отдал тело — тоже не конец.
	get := checkFinished(t, c, s, "R-lost")
	mustStep(t, c, cdpMessage{ID: get.ID, SessionID: s, Error: &cdpError{Code: -32000, Message: "No resource with given identifier found"}})

	get = checkFinished(t, c, s, "R-error")
	_, out := c.handle(bodyReply(get, `{"error":{"error_code":100,"error_msg":"выдуманная ошибка"}}`, false))
	if out == nil || out.err == nil || !strings.Contains(out.err.Error(), "VK отказал") {
		t.Fatalf("отказ VK: %+v", out)
	}
}

// Чужие запросы, preflight и оборванные ответы за телом не ходят.
func TestCDPCaptureIgnores(t *testing.T) {
	c := newCDPCapture(testCaptchaURL, nil)
	openPage(t, c)

	steps := []string{
		`{"method":"Network.responseReceived","sessionId":"S1","params":{"requestId":"A","type":"Script","response":{"url":"https://st.vk.com/captcha.js"}}}`,
		`{"method":"Network.loadingFinished","sessionId":"S1","params":{"requestId":"A"}}`,
		`{"method":"Network.responseReceived","sessionId":"S1","params":{"requestId":"P","type":"Preflight","response":{"url":"` + testCheckURL + `"}}}`,
		`{"method":"Network.loadingFinished","sessionId":"S1","params":{"requestId":"P"}}`,
		`{"method":"Network.responseReceived","sessionId":"S1","params":{"requestId":"F","type":"XHR","response":{"url":"` + testCheckURL + `"}}}`,
		`{"method":"Network.loadingFailed","sessionId":"S1","params":{"requestId":"F","errorText":"net::ERR_ABORTED"}}`,
		`{"method":"Network.loadingFinished","sessionId":"S1","params":{"requestId":"F"}}`,
		// Тот же requestId в другой сессии — другой запрос.
		`{"method":"Network.responseReceived","sessionId":"S1","params":{"requestId":"X","type":"XHR","response":{"url":"` + testCheckURL + `"}}}`,
		`{"method":"Network.loadingFinished","sessionId":"OTHER","params":{"requestId":"X"}}`,
		// Ответ на команду, которой не было.
		`{"id":999,"result":{}}`,
	}
	for _, raw := range steps {
		if cmds := mustStep(t, c, msg(t, raw)); len(cmds) != 0 {
			t.Errorf("на %s отправлено %v", raw, methods(cmds))
		}
	}
}

// Фрейм на другом сайте живёт в своём процессе и подключается отдельной
// сессией: в ней включается Network, а сам фрейм отпускается.
func TestCDPCaptureChildFrame(t *testing.T) {
	c := newCDPCapture(testCaptchaURL, nil)
	openPage(t, c)

	cmds := mustStep(t, c, msg(t, `{"method":"Target.attachedToTarget","sessionId":"S1","params":{
		"sessionId":"S2","targetInfo":{"targetId":"FRAME","type":"iframe"},"waitingForDebugger":true}}`))
	want := "Network.enable,Target.setAutoAttach,Runtime.runIfWaitingForDebugger"
	if got := strings.Join(methods(cmds), ","); got != want {
		t.Fatalf("подготовка фрейма: %s, ожидалось %s", got, want)
	}
	for _, cmd := range cmds {
		if cmd.SessionID != "S2" {
			t.Errorf("%s ушла не в сессию фрейма: %q", cmd.Method, cmd.SessionID)
		}
	}
	// Отказы в сессии фрейма разговор не кончают.
	mustStep(t, c, cdpMessage{ID: cmds[1].ID, SessionID: "S2", Error: &cdpError{Code: -32601, Message: "not supported"}})

	// Воркеру автоподключение не шлём — у него нет фреймов.
	worker := mustStep(t, c, msg(t, `{"method":"Target.attachedToTarget","sessionId":"S1","params":{
		"sessionId":"S3","targetInfo":{"targetId":"W","type":"worker"},"waitingForDebugger":true}}`))
	if got := strings.Join(methods(worker), ","); got != "Network.enable,Runtime.runIfWaitingForDebugger" {
		t.Errorf("подготовка воркера: %s", got)
	}

	get := checkFinished(t, c, "S2", "R1")
	if _, out := c.handle(bodyReply(get, tokenBody, false)); out == nil || out.token != testToken {
		t.Fatalf("исход из фрейма: %+v", out)
	}
}

// Вкладки в браузере не нашлось — открываем свою.
func TestCDPCaptureCreatesTarget(t *testing.T) {
	c := newCDPCapture(testCaptchaURL, nil)
	start := c.start()
	cmds := mustStep(t, c, replyTo(start[0], `{"targetInfos":[{"targetId":"B","type":"browser"}]}`))
	if len(cmds) != 1 || cmds[0].Method != "Target.createTarget" {
		t.Fatalf("без вкладки: %v", methods(cmds))
	}
	cmds = mustStep(t, c, replyTo(cmds[0], `{"targetId":"NEW"}`))
	if len(cmds) != 1 || cmds[0].Method != "Target.attachToTarget" || !strings.Contains(params(t, cmds[0]), `"NEW"`) {
		t.Fatalf("подключение к новой вкладке: %+v", cmds)
	}
}

func TestCDPCaptureEnds(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "вкладку закрыли", raw: `{"method":"Target.detachedFromTarget","params":{"sessionId":"S1","targetId":"PAGE1"}}`, want: "окно капчи закрыто"},
		{name: "вкладка упала", raw: `{"method":"Target.targetCrashed","params":{"targetId":"PAGE1","status":"crashed"}}`, want: "упала"},
		{name: "отладчик отключён", raw: `{"method":"Inspector.detached","sessionId":"S1","params":{"reason":"target_closed"}}`, want: "окно капчи закрыто"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newCDPCapture(testCaptchaURL, nil)
			openPage(t, c)
			// Закрытие чужой сессии — не наше дело.
			mustStep(t, c, msg(t, `{"method":"Target.detachedFromTarget","sessionId":"S1","params":{"sessionId":"S9"}}`))
			_, out := c.handle(msg(t, tc.raw))
			if out == nil || out.err == nil || !strings.Contains(out.err.Error(), tc.want) {
				t.Fatalf("исход: %+v, ожидалось %q", out, tc.want)
			}
		})
	}
}

// Отказ на пути к странице — ErrBrowser: человек окна с капчей не видел,
// и CLI решит ту же капчу вручную. Страница не загрузилась — просто ошибка.
func TestCDPCaptureSetupFailures(t *testing.T) {
	t.Run("подключение", func(t *testing.T) {
		c := newCDPCapture(testCaptchaURL, nil)
		start := c.start()
		cmds := mustStep(t, c, replyTo(start[0], `{"targetInfos":[{"targetId":"P","type":"page"}]}`))
		_, out := c.handle(cdpMessage{ID: cmds[0].ID, Error: &cdpError{Code: -32602, Message: "No target with given id found"}})
		if out == nil || !errors.Is(out.err, ErrBrowser) {
			t.Fatalf("исход: %+v", out)
		}
	})
	t.Run("страница", func(t *testing.T) {
		c := newCDPCapture(testCaptchaURL, nil)
		start := c.start()
		cmds := mustStep(t, c, replyTo(start[0], `{"targetInfos":[{"targetId":"P","type":"page"}]}`))
		setup := mustStep(t, c, replyTo(cmds[0], `{"sessionId":"S1"}`))
		_, out := c.handle(replyTo(setup[2], `{"frameId":"F","errorText":"net::ERR_TUNNEL_CONNECTION_FAILED"}`))
		if out == nil || out.err == nil || errors.Is(out.err, ErrBrowser) ||
			!strings.Contains(out.err.Error(), "ERR_TUNNEL_CONNECTION_FAILED") {
			t.Fatalf("исход: %+v", out)
		}
	})
}

// ─────────────── captureToken: разговор по трубе ───────────────

// fakeBrowser отвечает на команды по трубе, как браузер: вкладка, сессия,
// страница. after — что прислать после Page.navigate; закрыть — значит
// выйти, как браузер с последним окном.
type fakeBrowser struct {
	after      []string // сообщения после Page.navigate
	exitAfter  bool     // после них — закрыть трубу
	silent     bool     // не отвечать вовсе
	exitAtOnce bool     // закрыть трубу сразу
	body       string   // тело на Network.getResponseBody
}

func (f fakeBrowser) run(t *testing.T, cmds io.Reader, out io.WriteCloser) {
	defer out.Close()
	if f.exitAtOnce {
		return
	}
	write := func(raw string) {
		_, _ = out.Write(append([]byte(raw), 0))
	}
	br := bufio.NewReader(cmds)
	for {
		raw, err := br.ReadBytes(0)
		if err != nil {
			return
		}
		if f.silent {
			continue
		}
		var cmd cdpCommand
		if err := json.Unmarshal(raw[:len(raw)-1], &cmd); err != nil {
			t.Errorf("команда не JSON: %q", raw)
			return
		}
		reply := func(result string) {
			write(fmt.Sprintf(`{"id":%d,"sessionId":%q,"result":%s}`, cmd.ID, cmd.SessionID, result))
		}
		switch cmd.Method {
		case "Target.getTargets":
			reply(`{"targetInfos":[{"targetId":"P","type":"page"}]}`)
		case "Target.attachToTarget":
			reply(`{"sessionId":"S1"}`)
		case "Page.navigate":
			reply(`{"frameId":"F"}`)
			for _, m := range f.after {
				write(m)
			}
			if f.exitAfter {
				return
			}
		case "Network.getResponseBody":
			data, _ := json.Marshal(map[string]any{"body": f.body})
			reply(string(data))
		default:
			reply(`{}`)
		}
	}
}

func runFake(t *testing.T, ctx context.Context, f fakeBrowser, startup time.Duration) (string, error) {
	t.Helper()
	cmdR, cmdW := io.Pipe()
	resR, resW := io.Pipe()
	go f.run(t, cmdR, resW)
	defer cmdW.Close()
	defer resR.Close()
	return captureToken(ctx, resR, cmdW, testCaptchaURL, startup, nil)
}

var checkEvents = []string{
	`{"method":"Network.responseReceived","sessionId":"S1","params":{"requestId":"R","type":"Fetch","response":{"url":"` + testCheckURL + `"}}}`,
	`{"method":"Network.loadingFinished","sessionId":"S1","params":{"requestId":"R"}}`,
}

func TestCaptureToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	t.Run("токен", func(t *testing.T) {
		got, err := runFake(t, ctx, fakeBrowser{after: checkEvents, body: tokenBody}, 5*time.Second)
		if err != nil || got != testToken {
			t.Fatalf("captureToken = %q, %v", got, err)
		}
	})
	t.Run("окно закрыто", func(t *testing.T) {
		_, err := runFake(t, ctx, fakeBrowser{exitAfter: true}, 5*time.Second)
		if !errors.Is(err, ErrWindowClosed) || errors.Is(err, ErrBrowser) {
			t.Fatalf("ошибка %v, ожидалось «окно капчи закрыто»", err)
		}
	})
	t.Run("вкладку закрыли до страницы", func(t *testing.T) {
		// Браузер вышел, не ответив: окна с капчей человек не видел.
		_, err := runFake(t, ctx, fakeBrowser{exitAtOnce: true}, 5*time.Second)
		if !errors.Is(err, ErrBrowser) {
			t.Fatalf("ошибка %v, ожидалась ErrBrowser", err)
		}
	})
	t.Run("браузер молчит", func(t *testing.T) {
		start := time.Now()
		_, err := runFake(t, ctx, fakeBrowser{silent: true}, 200*time.Millisecond)
		if !errors.Is(err, ErrBrowser) || time.Since(start) > 5*time.Second {
			t.Fatalf("ошибка %v за %s, ожидалась ErrBrowser по сроку запуска", err, time.Since(start))
		}
	})
	t.Run("срок запуска после открытой страницы не действует", func(t *testing.T) {
		short, stop := context.WithTimeout(ctx, 500*time.Millisecond)
		defer stop()
		_, err := runFake(t, short, fakeBrowser{}, 50*time.Millisecond)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ошибка %v, ожидался срок ctx", err)
		}
	})
}
