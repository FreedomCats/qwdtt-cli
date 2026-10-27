package captcha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"
)

func testRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)) }

func TestClickPoint(t *testing.T) {
	b := box{X: 40, Y: 120, W: 200, H: 50}
	for seed := range uint64(500) {
		p := clickPoint(b, testRand(seed))
		if p.X < b.X+0.15*b.W || p.X > b.X+0.85*b.W || p.Y < b.Y+0.25*b.H || p.Y > b.Y+0.75*b.H {
			t.Fatalf("точка %+v вне середины галочки %+v", p, b)
		}
	}
}

func TestStartPoint(t *testing.T) {
	to := point{200, 150}
	for seed := range uint64(500) {
		p := startPoint(to, 480, 640, testRand(seed))
		if p.X < 0 || p.X > 480 || p.Y < 0 || p.Y > 640 {
			t.Fatalf("начало %+v вне окна", p)
		}
		if math.Hypot(p.X-to.X, p.Y-to.Y) < 120 {
			t.Fatalf("начало %+v ближе 120 px к цели %+v", p, to)
		}
	}
	// Окно меньше расстояния — точка всё равно в окне.
	if p := startPoint(point{10, 10}, 50, 50, testRand(1)); p.X < 0 || p.X > 50 || p.Y < 0 || p.Y > 50 {
		t.Errorf("начало %+v вне маленького окна", p)
	}
}

func TestMousePath(t *testing.T) {
	from, to := point{30, 500}, point{180, 140}
	straight := 0
	for seed := range uint64(300) {
		path := mousePath(from, to, testRand(seed))
		if n := len(path); n < 10 || n > 25 {
			t.Fatalf("точек %d, ожидалось 10–25", n)
		}
		last := path[len(path)-1]
		if last.x != to.X || last.y != to.Y {
			t.Fatalf("путь кончается в (%v, %v), а не в цели %+v", last.x, last.y, to)
		}
		if last.at < 300*time.Millisecond || last.at > 800*time.Millisecond {
			t.Fatalf("путь занимает %s, ожидалось 0,3–0,8 с", last.at)
		}
		var prev time.Duration
		maxOff := 0.0
		for _, s := range path {
			if s.at <= prev {
				t.Fatalf("время пути не растёт: %s после %s", s.at, prev)
			}
			prev = s.at
			// Отклонение от прямой from→to.
			dx, dy := to.X-from.X, to.Y-from.Y
			off := math.Abs(dy*(s.x-from.X)-dx*(s.y-from.Y)) / math.Hypot(dx, dy)
			maxOff = max(maxOff, off)
		}
		if maxOff < 3 {
			straight++
		}
	}
	// Прогиб случайный и бывает почти нулевым, но не у большинства путей.
	if straight > 30 {
		t.Errorf("прямых путей %d из 300", straight)
	}
}

func TestCheckVerdict(t *testing.T) {
	cases := []struct {
		r    checkReply
		want error
		text string
	}{
		{checkReply{status: "OK", token: true}, nil, ""},
		{checkReply{status: "BOT", showType: "slider"}, errAutoSlider, ""},
		{checkReply{status: "BOT"}, nil, "не поверил галочке (BOT)"},
		{checkReply{status: "ERROR_LIMIT"}, errAutoLimit, ""},
		{checkReply{vkError: 100, vkMsg: "выдуманная"}, nil, "ошибкой 100: выдуманная"},
		{checkReply{}, nil, "без статуса"},
	}
	for _, tc := range cases {
		err := checkVerdict(tc.r)
		switch {
		case tc.want == nil && tc.text == "":
			if err != nil {
				t.Errorf("%+v: %v, ожидался успех", tc.r, err)
			}
		case tc.want != nil:
			if !errors.Is(err, tc.want) {
				t.Errorf("%+v: %v, ожидалась %v", tc.r, err, tc.want)
			}
		default:
			if err == nil || !strings.Contains(err.Error(), tc.text) {
				t.Errorf("%+v: %v, ожидалось «%s»", tc.r, err, tc.text)
			}
		}
	}
}

// fakePage — страница капчи для автофазы без окна: probe отвечает по
// состоянию, события мыши записываются.
type fakePage struct {
	mu     sync.Mutex
	state  func(clicked bool) pageProbe // что на странице; clicked — после отпускания кнопки
	broken bool                         // Runtime.evaluate не проходит (страница не грузится)
	// hang — вызов не отвечает до конца ctx (диалог JS, зависший рендерер);
	// clicked — после отпускания кнопки.
	hang   func(method string, clicked bool) bool
	events []mouseEvent
	onUp   func() // зовётся после отпускания кнопки
	// Ответ на описание страницы (describeJS).
	dom *domReport
}

type mouseEvent struct {
	kind string
	p    point
}

func (f *fakePage) devtools(ctx context.Context, method string, params any) (json.RawMessage, error) {
	f.mu.Lock()
	clicked := len(f.events) > 0 && f.events[len(f.events)-1].kind == "mouseReleased"
	if f.hang != nil && f.hang(method, clicked) {
		f.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	defer f.mu.Unlock()
	switch method {
	case "Runtime.evaluate":
		if f.broken {
			return nil, errors.New("выдуманная: нет контекста страницы")
		}
		var value any
		switch params.(map[string]any)["expression"] {
		case describeJS:
			value = f.dom
		default:
			value = f.state(clicked)
		}
		b, _ := json.Marshal(map[string]any{"result": map[string]any{"type": "object", "value": value}})
		return b, nil
	case "Input.dispatchMouseEvent":
		m := params.(map[string]any)
		ev := mouseEvent{kind: m["type"].(string), p: point{m["x"].(float64), m["y"].(float64)}}
		f.events = append(f.events, ev)
		if ev.kind == "mouseReleased" && f.onUp != nil {
			go f.onUp()
		}
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func (f *fakePage) mouse() []mouseEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mouseEvent(nil), f.events...)
}

var testBox = box{X: 40, Y: 120, W: 180, H: 48}

func withCheckbox(bool) pageProbe {
	return pageProbe{State: "checkbox", Ready: "complete", Visibility: "visible", VW: 480, VH: 640, Box: &testBox}
}

// fastTiming — короткие сроки; click — с запасом на паузы человека (~2,5 с).
var fastTiming = autoTiming{find: 600 * time.Millisecond, click: 5 * time.Second, poll: 20 * time.Millisecond,
	reply: 400 * time.Millisecond}

func runAuto(t *testing.T, page *fakePage, checks chan checkReply) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return autoSolve(ctx, page, checks, fastTiming, testRand(7), t.Logf)
}

// Галочка есть, VK принял клик: путь курсора, нажатие и отпускание в
// пределах галочки.
func TestAutoSolveAccepted(t *testing.T) {
	t.Parallel()
	checks := make(chan checkReply, 1)
	page := &fakePage{state: withCheckbox, onUp: func() { checks <- checkReply{status: "OK", token: true} }}
	if err := runAuto(t, page, checks); err != nil {
		t.Fatalf("autoSolve: %v", err)
	}
	ev := page.mouse()
	n := len(ev)
	if n < 12 || ev[n-2].kind != "mousePressed" || ev[n-1].kind != "mouseReleased" {
		t.Fatalf("события мыши: %+v", ev)
	}
	for _, e := range ev[:n-2] {
		if e.kind != "mouseMoved" {
			t.Fatalf("до нажатия — %s", e.kind)
		}
	}
	if moves := n - 2; moves < 10 || moves > 25 {
		t.Errorf("движений %d, ожидалось 10–25", moves)
	}
	in := func(p point) bool {
		return p.X >= testBox.X && p.X <= testBox.X+testBox.W && p.Y >= testBox.Y && p.Y <= testBox.Y+testBox.H
	}
	if p := ev[n-2].p; !in(p) || ev[n-3].p != p {
		t.Errorf("нажатие в %+v: вне галочки или не там, где кончился путь (%+v)", p, ev[n-3].p)
	}
	if up, down := ev[n-1].p, ev[n-2].p; !in(up) || math.Abs(up.X-down.X) > 1 || math.Abs(up.Y-down.Y) > 1 {
		t.Errorf("отпускание в %+v, нажатие в %+v", up, down)
	}
}

func TestAutoSolveFailures(t *testing.T) {
	t.Parallel()
	slider := func(bool) pageProbe { return pageProbe{State: "slider", Ready: "complete"} }
	empty := func(bool) pageProbe { return pageProbe{State: "none", Ready: "complete"} }
	loading := func(bool) pageProbe { return pageProbe{State: "none", Ready: "loading"} }
	sliderAfter := func(clicked bool) pageProbe {
		if clicked {
			return slider(true)
		}
		return withCheckbox(false)
	}
	cases := []struct {
		name    string
		page    *fakePage
		reply   *checkReply // ответ VK после клика; nil — молчит
		want    error
		text    string
		clicked bool
	}{
		{name: "слайдер сразу", page: &fakePage{state: slider}, want: errAutoSlider},
		{name: "нет галочки", page: &fakePage{state: empty}, want: errAutoNoCheckbox},
		{name: "страница грузится", page: &fakePage{state: loading}, want: errAutoNotLoaded},
		{name: "страница не отвечает", page: &fakePage{state: empty, broken: true}, want: errAutoNotLoaded},
		{name: "BOT со слайдером", page: &fakePage{state: withCheckbox},
			reply: &checkReply{status: "BOT", showType: "slider"}, want: errAutoSlider, clicked: true},
		{name: "ошибка VK", page: &fakePage{state: withCheckbox},
			reply: &checkReply{vkError: 100, vkMsg: "выдуманная"}, text: "ошибкой 100", clicked: true},
		{name: "VK молчит", page: &fakePage{state: withCheckbox}, want: errAutoNoReply, clicked: true},
		{name: "слайдер после клика без ответа", page: &fakePage{state: sliderAfter}, want: errAutoSlider,
			clicked: true},
		{name: "ERROR_LIMIT", page: &fakePage{state: withCheckbox}, reply: &checkReply{status: "ERROR_LIMIT"},
			want: errAutoLimit, clicked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checks := make(chan checkReply, 1)
			if tc.reply != nil {
				r := *tc.reply
				tc.page.onUp = func() { checks <- r }
			}
			err := runAuto(t, tc.page, checks)
			switch {
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Errorf("ошибка %v, ожидалась %v", err, tc.want)
			case tc.text != "" && (err == nil || !strings.Contains(err.Error(), tc.text)):
				t.Errorf("ошибка %v, ожидалось «%s»", err, tc.text)
			}
			if clicked := len(tc.page.mouse()) > 0; clicked != tc.clicked {
				t.Errorf("клик был: %v, ожидалось %v", clicked, tc.clicked)
			}
		})
	}
}

// Страница перестала отвечать: каждый этап кончается своим сроком, а не
// концом всего окна, и капча уходит человеку с причиной этапа.
func TestAutoSolveHangs(t *testing.T) {
	t.Parallel()
	timing := fastTiming
	timing.click = 3 * time.Second
	cases := []struct {
		name  string
		state func(bool) pageProbe
		hang  func(method string, clicked bool) bool
		want  error
		text  string
		limit time.Duration // за сколько автофаза должна кончиться
	}{
		{name: "поиск галочки", state: withCheckbox,
			hang: func(string, bool) bool { return true }, want: errAutoNotLoaded, limit: 2 * time.Second},
		{name: "клик", state: withCheckbox,
			hang: func(m string, _ bool) bool { return m == "Input.dispatchMouseEvent" }, text: "клик не прошёл",
			limit: 4 * time.Second},
		{name: "ответ VK", state: withCheckbox,
			hang: func(m string, clicked bool) bool { return clicked && m == "Runtime.evaluate" },
			want: errAutoNoReply, limit: 5 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			page := &fakePage{state: tc.state, hang: tc.hang}
			start := time.Now()
			err := autoSolve(ctx, page, make(chan checkReply), timing, testRand(3), t.Logf)
			took := time.Since(start)
			switch {
			case tc.want != nil && !errors.Is(err, tc.want):
				t.Errorf("ошибка %v, ожидалась %v", err, tc.want)
			case tc.text != "" && (err == nil || !strings.Contains(err.Error(), tc.text)):
				t.Errorf("ошибка %v, ожидалось «%s»", err, tc.text)
			}
			if took > tc.limit {
				t.Errorf("автофаза кончилась через %s, ожидалось не дольше %s", took, tc.limit)
			}
		})
	}
}

func TestAutoSolveCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	page := &fakePage{state: func(bool) pageProbe { return pageProbe{State: "none", Ready: "loading"} }}
	time.AfterFunc(100*time.Millisecond, cancel)
	slow := autoTiming{find: time.Minute, poll: 10 * time.Millisecond, reply: time.Minute}
	if err := autoSolve(ctx, page, nil, slow, testRand(1), t.Logf); !errors.Is(err, context.Canceled) {
		t.Fatalf("ошибка %v, ожидалась отмена", err)
	}
}

// autoRun: неудача — строка с причиной и окно человеку, один раз; успех без
// человека — строка, что капча пройдена сама; после показа окна — молча.
func TestAutoRun(t *testing.T) {
	t.Parallel()
	req := Request{Mode: ModeSelected, Deadline: time.Now().Add(3 * time.Minute)}

	t.Run("неудача", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		reveals := 0
		a := newAutoRun(&out, t.Logf, req, func() { reveals++ })
		a.timing = fastTiming
		a.run(context.Background(), &fakePage{state: func(bool) pageProbe {
			return pageProbe{State: "none", Ready: "complete"}
		}}, nil)
		a.fail(errors.New("второй раз"))
		a.solved()
		text := out.String()
		if reveals != 1 {
			t.Errorf("окно показано %d раз", reveals)
		}
		if strings.Count(text, "Автоматически пройти капчу не удалось: на странице нет галочки — решите её в окне") != 1 ||
			strings.Contains(text, "пройдена автоматически") || strings.Contains(text, "второй раз") {
			t.Errorf("вывод:%s", text)
		}
	})

	t.Run("успех", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		a := newAutoRun(&out, t.Logf, req, func() { t.Error("окно показано") })
		a.timing = fastTiming
		checks := make(chan checkReply, 1)
		page := &fakePage{state: withCheckbox, onUp: func() { checks <- checkReply{status: "OK", token: true} }}
		a.run(context.Background(), page, checks)
		a.solved()
		if text := out.String(); strings.TrimSpace(text) != "Капча пройдена автоматически." {
			t.Errorf("вывод:%s", text)
		}
	})

	t.Run("ERROR_LIMIT", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		a := newAutoRun(&out, t.Logf, req, func() { t.Error("окно показано на ERROR_LIMIT") })
		a.timing = fastTiming
		checks := make(chan checkReply, 1)
		page := &fakePage{state: withCheckbox, onUp: func() { checks <- checkReply{status: "ERROR_LIMIT"} }}
		a.run(context.Background(), page, checks)
		if out.Len() != 0 {
			t.Errorf("вывод:%s", out.String())
		}
	})

	t.Run("отмена", func(t *testing.T) {
		t.Parallel()
		var out bytes.Buffer
		a := newAutoRun(&out, t.Logf, req, func() { t.Error("окно показано после отмены") })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		a.run(ctx, &fakePage{state: withCheckbox}, nil)
		if out.Len() != 0 {
			t.Errorf("вывод:%s", out.String())
		}
	})
}
