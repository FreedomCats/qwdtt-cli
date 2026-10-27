package captcha

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// Окно с автокликом: вкладка — в новом окне, открытом в фоне; команды
// автоклика уходят в сессию вкладки, когда она подготовлена, а ответы на
// них — автоклику, отказы тоже, и разговор они не кончают.
func TestCDPCaptureHidden(t *testing.T) {
	c := newCDPCapture(testCaptchaURL, nil)
	c.hidden = true
	readied := 0
	c.onReady = func() { readied++ }

	start := c.start()
	if len(start) != 1 || start[0].Method != "Target.createTarget" || start[0].SessionID != "" ||
		params(t, start[0]) != `{"background":true,"newWindow":true,"url":"about:blank"}` {
		t.Fatalf("начало: %+v", start)
	}

	early := cdpCall{method: "Runtime.evaluate", reply: make(chan cdpReply, 1)}
	if cmds := c.drive(early); len(cmds) != 0 {
		t.Fatalf("до вкладки команда автоклика ушла: %v", methods(cmds))
	}
	if r := <-early.reply; r.err == nil {
		t.Fatal("до вкладки команда автоклика без отказа")
	}

	cmds := mustStep(t, c, replyTo(start[0], `{"targetId":"PAGE1"}`))
	if len(cmds) != 1 || cmds[0].Method != "Target.attachToTarget" {
		t.Fatalf("подключение к вкладке: %+v", cmds)
	}
	nav := setupPage(t, c, cmds[0])
	if readied != 1 {
		t.Fatalf("onReady позвана %d раз", readied)
	}

	call := cdpCall{method: "Input.dispatchMouseEvent", params: map[string]any{"type": "mouseMoved"},
		reply: make(chan cdpReply, 1)}
	cmds = c.drive(call)
	if len(cmds) != 1 || cmds[0].Method != call.method || cmds[0].SessionID != "S1" ||
		params(t, cmds[0]) != `{"type":"mouseMoved"}` {
		t.Fatalf("команда автоклика: %+v", cmds)
	}
	// Ответ на Page.navigate может прийти и после команд автоклика.
	mustStep(t, c, replyTo(nav, `{"frameId":"F1"}`))
	if got := mustStep(t, c, replyTo(cmds[0], `{"ok":1}`)); len(got) != 0 {
		t.Fatalf("после ответа автоклику отправлено %v", methods(got))
	}
	if r := <-call.reply; r.err != nil || string(r.result) != `{"ok":1}` {
		t.Fatalf("ответ автоклику: %s, %v", r.result, r.err)
	}

	bad := cdpCall{method: "Runtime.evaluate", reply: make(chan cdpReply, 1)}
	cmds = c.drive(bad)
	mustStep(t, c, cdpMessage{ID: cmds[0].ID, SessionID: "S1", Error: &cdpError{Code: -32000, Message: "выдуманная"}})
	if r := <-bad.reply; r.err == nil || !strings.Contains(r.err.Error(), "выдуманная") {
		t.Fatalf("отказ автоклику: %v", r.err)
	}
	if readied != 1 {
		t.Fatalf("onReady позвана %d раз", readied)
	}
}

// Команды автоклика по трубе: идут через converse, ответы приходят; разговор
// кончился — cdpDriver отвечает «окно закрыто», а не висит.
func TestCDPDriver(t *testing.T) {
	cmdR, cmdW := io.Pipe()
	resR, resW := io.Pipe()
	go fakeBrowser{}.run(t, cmdR, resW)
	defer cmdW.Close()
	defer resR.Close()

	calls := make(chan cdpCall)
	done := make(chan struct{})
	ready := make(chan struct{})
	c := newCDPCapture(testCaptchaURL, nil)
	c.hidden = true
	c.calls = calls
	c.onReady = sync.OnceFunc(func() { close(ready) })
	d := &cdpDriver{calls: calls, done: done}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conv, stop := context.WithCancel(ctx)
	ended := make(chan error, 1)
	go func() {
		_, err := runCapture(conv, resR, cmdW, c, 5*time.Second)
		close(done)
		ended <- err
	}()

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("вкладка не подготовлена")
	}
	if raw, err := d.devtools(ctx, "Runtime.evaluate", map[string]any{"expression": "1"}); err != nil ||
		string(raw) != `{}` {
		t.Fatalf("devtools: %s, %v", raw, err)
	}
	stop()
	if err := <-ended; !errors.Is(err, context.Canceled) {
		t.Fatalf("разговор: %v", err)
	}
	if _, err := d.devtools(ctx, "Runtime.evaluate", nil); !errors.Is(err, ErrWindowClosed) {
		t.Fatalf("после разговора: %v, ожидалось «окно закрыто»", err)
	}
}

// geomPage — страница для windowHidden и revealWindow: окно и экран, вызовы
// записываются.
type geomPage struct {
	mu     sync.Mutex
	window screenRect
	screen screenRect
	fail   int // столько первых вызовов отказывают (страница открывается)
	// Рендерер не отвечает, браузер — да: evalFail — Runtime.evaluate
	// отказывает, evalHang — молчит до конца ctx.
	evalFail bool
	evalHang bool
	// moveFail — Browser.setWindowBounds отказывает (окно развёрнуто).
	moveFail bool
	calls    []string
	params   []any
}

func (g *geomPage) devtools(ctx context.Context, method string, params any) (json.RawMessage, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, method)
	g.params = append(g.params, params)
	if method == "Runtime.evaluate" && g.evalHang {
		g.mu.Unlock()
		<-ctx.Done()
		g.mu.Lock()
		return nil, ctx.Err()
	}
	if method == "Runtime.evaluate" && g.evalFail {
		return nil, errors.New("выдуманная: страница не отвечает")
	}
	if method == "Browser.setWindowBounds" && g.moveFail {
		return nil, errors.New("выдуманная: restore it to normal state first")
	}
	if g.fail > 0 {
		g.fail--
		return nil, errors.New("выдуманная: нет контекста")
	}
	switch method {
	case "Browser.getWindowForTarget":
		return json.Marshal(map[string]any{"windowId": 7, "bounds": g.window})
	case "Runtime.evaluate":
		s := g.screen
		return json.Marshal(map[string]any{"result": map[string]any{"value": map[string]any{"l": s.Left, "t": s.Top,
			"w": s.Width, "h": s.Height}}})
	}
	return json.RawMessage(`{}`), nil
}

func TestWindowHidden(t *testing.T) {
	ctx := context.Background()
	screen := screenRect{0, 0, 1920, 1050}
	for _, tc := range []struct {
		name   string
		window screenRect
		hidden bool
	}{
		{"за нижним краем", screenRect{0, 10000, 520, 760}, true},
		{"за правым краем", screenRect{1920, 0, 520, 760}, true},
		{"левее экрана", screenRect{-600, 10, 520, 760}, true},
		{"прижато к экрану", screenRect{6, 33, 520, 760}, false},
		{"краем на экране", screenRect{1900, 1000, 520, 760}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := &geomPage{window: tc.window, screen: screen, fail: 2}
			_, hidden, err := windowHidden(ctx, page, time.Millisecond)
			if err != nil || hidden != tc.hidden {
				t.Fatalf("windowHidden = %v, %v; ожидалось %v", hidden, err, tc.hidden)
			}
		})
	}

	// Страница не отвечает — ошибка по сроку, а не вечное ожидание.
	page := &geomPage{fail: 1 << 30}
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if _, _, err := windowHidden(short, page, 10*time.Millisecond); err == nil {
		t.Fatal("windowHidden без ответа страницы — без ошибки")
	}
}

func TestRevealWindow(t *testing.T) {
	for _, front := range []bool{false, true} {
		page := &geomPage{window: screenRect{0, 10000, 520, 760}, screen: screenRect{1920, 24, 2560, 1416}}
		if err := revealWindow(context.Background(), page, front, t.Logf); err != nil {
			t.Fatal(err)
		}
		want := "Browser.getWindowForTarget,Runtime.evaluate,Browser.setWindowBounds"
		if front {
			want += ",Page.bringToFront"
		}
		if got := strings.Join(page.calls, ","); got != want {
			t.Errorf("front=%v: вызовы %s, ожидалось %s", front, got, want)
		}
		data, _ := json.Marshal(page.params[2])
		if got := string(data); got != `{"bounds":{"left":2940,"top":352},"windowId":7}` {
			t.Errorf("Browser.setWindowBounds: %s", got)
		}
	}
}

// Страница не отвечает (зависший рендерер, диалог JS): экрана не узнать, но
// окно всё равно уходит с края — в левый верхний угол — и, если front, на
// передний план, в пределах revealTimeout. Молчащей странице — свой срок
// revealScreenTimeout, перестановка окна идёт после него.
func TestRevealWindowPageDead(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hang  bool
		front bool
	}{
		{"отказ", false, false},
		{"отказ, на передний план", false, true},
		{"молчит, на передний план", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := &geomPage{window: screenRect{0, 10000, 520, 760}, evalFail: !tc.hang, evalHang: tc.hang}
			var log logLines
			ctx, cancel := context.WithTimeout(context.Background(), revealTimeout)
			defer cancel()
			start := time.Now()
			if err := revealWindow(ctx, page, tc.front, log.logf); err != nil {
				t.Fatalf("revealWindow: %v", err)
			}
			took := time.Since(start)
			page.mu.Lock()
			defer page.mu.Unlock()
			want := "Browser.getWindowForTarget,Runtime.evaluate,Browser.setWindowBounds"
			if tc.front {
				want += ",Page.bringToFront"
			}
			if got := strings.Join(page.calls, ","); got != want {
				t.Fatalf("вызовы %s, ожидалось %s", got, want)
			}
			data, _ := json.Marshal(page.params[2])
			if got := string(data); got != `{"bounds":{"left":0,"top":0},"windowId":7}` {
				t.Errorf("Browser.setWindowBounds: %s", got)
			}
			if !strings.Contains(log.text(), "левый верхний угол") {
				t.Errorf("журнал:\n%s", log.text())
			}
			if tc.hang && (took < revealScreenTimeout-100*time.Millisecond || took >= revealTimeout) {
				t.Errorf("окно переставлено через %s, ожидалось около %s", took, revealScreenTimeout)
			}
			if !tc.hang && took > time.Second {
				t.Errorf("revealWindow шёл %s", took)
			}
		})
	}
}

// Окно развёрнуто — Browser.setWindowBounds отказывает, а на передний план
// окно всё равно выходит; ошибка перестановки возвращается.
func TestRevealWindowMoveRefused(t *testing.T) {
	for _, front := range []bool{false, true} {
		page := &geomPage{window: screenRect{0, 0, 1280, 1024}, screen: screenRect{0, 0, 1280, 1024}, moveFail: true}
		err := revealWindow(context.Background(), page, front, t.Logf)
		if err == nil || !strings.Contains(err.Error(), "normal state") {
			t.Errorf("front=%v: ошибка %v, ожидался отказ перестановки", front, err)
		}
		want := "Browser.getWindowForTarget,Runtime.evaluate,Browser.setWindowBounds"
		if front {
			want += ",Page.bringToFront"
		}
		if got := strings.Join(page.calls, ","); got != want {
			t.Errorf("front=%v: вызовы %s, ожидалось %s", front, got, want)
		}
	}
}
