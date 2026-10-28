package captcha

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// Канал ответа у ядра один на процесс (core/main.go), поэтому тесты этого
// пакета не параллелятся и убирают за собой сами — как и тесты ядра.
func drain(t *testing.T) {
	t.Helper()
	for {
		select {
		case <-core.CaptchaResultChan:
		default:
			return
		}
	}
}

// answer — что ядро получило бы, ожидая ответа. Пустая строка означает, что
// не получило ничего.
func answer(t *testing.T, wait time.Duration) string {
	t.Helper()
	select {
	case result := <-core.CaptchaResultChan:
		return result
	case <-time.After(wait):
		return ""
	}
}

// fakeSolver — решатель под управлением теста.
type fakeSolver struct {
	human bool
	token string
	err   error

	// block, если не nil, держит Solve, пока его не закроют или пока не
	// отменят ctx: так проверяется и таймаут, и занятость.
	block chan struct{}

	mu    sync.Mutex
	calls []Request
}

func (s *fakeSolver) Solve(ctx context.Context, req Request) (string, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	s.mu.Unlock()

	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return s.token, s.err
}

func (s *fakeSolver) NeedsHuman() bool { return s.human }

func (s *fakeSolver) seen() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.calls...)
}

// newDispatcher собирает диспетчер с коротким окном ядра: настоящие — десять
// секунд и три минуты, и тест на таймаут шёл бы минуты.
func newDispatcher(t *testing.T, opts Options, window time.Duration) *Dispatcher {
	t.Helper()
	drain(t)

	d := New(opts)
	d.timeout = func(string) time.Duration { return window }
	t.Cleanup(func() {
		d.Close()
		drain(t)
	})
	return d
}

func required(mode string) core.EventCaptchaRequired {
	return core.EventCaptchaRequired{
		Session:      7,
		Mode:         mode,
		RedirectURI:  "https://id.vk.com/captcha?sid=12345",
		SessionToken: "сессия-капчи",
	}
}

// ──────────────────── событие доходит до решателя ────────────────────

func TestEventReachesSolver(t *testing.T) {
	solver := &fakeSolver{token: "успешный-токен"}
	ctrl := core.NewControl()
	d := newDispatcher(t, Options{Solver: solver, Control: ctrl}, 5*time.Second)

	d.Observe(required("manual"))

	if got := answer(t, 2*time.Second); got != "успешный-токен" {
		t.Fatalf("ядро получило %q, ожидался токен решателя", got)
	}

	calls := solver.seen()
	if len(calls) != 1 {
		t.Fatalf("решатель вызван %d раз, ожидался один", len(calls))
	}
	req := calls[0]
	switch {
	case req.Session != 7:
		t.Errorf("Session = %d, ожидалось 7", req.Session)
	case req.Mode != ModeManual:
		t.Errorf("Mode = %q, ожидался %q", req.Mode, ModeManual)
	case req.RedirectURI != "https://id.vk.com/captcha?sid=12345":
		t.Errorf("RedirectURI = %q", req.RedirectURI)
	case req.SessionToken != "сессия-капчи":
		t.Errorf("SessionToken = %q", req.SessionToken)
	case req.Deadline.IsZero():
		t.Error("Deadline не проставлен: решателю нечего показать человеку")
	}
}

// Срок считается по режиму и на запас короче, чем ждёт ядро.
func TestDeadlineLeavesMargin(t *testing.T) {
	d := New(Options{Margin: 2 * time.Second})
	d.timeout = func(string) time.Duration { return 10 * time.Second }

	now := time.Now()
	req := d.request(required("auto"), now)

	if got := req.Deadline.Sub(now); got != 8*time.Second {
		t.Errorf("срок = %v, ожидалось 8s: десять секунд ядра минус запас", got)
	}
}

// Режим ядро приводит к одному из трёх, незнакомый читает как auto —
// читаем так же.
func TestUnknownModeReadsAsAuto(t *testing.T) {
	d := New(Options{})
	for _, mode := range []string{"", "wbv", "МАНУАЛ", "MANUAL", "selected"} {
		req := d.request(required(mode), time.Now())
		want := ModeAuto
		switch strings.ToLower(mode) {
		case "manual":
			want = ModeManual
		case "selected":
			want = ModeSelected
		}
		if req.Mode != want {
			t.Errorf("режим %q прочитан как %q, ожидался %q", mode, req.Mode, want)
		}
	}
}

// Срок берётся у ядра, а не выдумывается: у трёх режимов два срока —
// десять секунд у auto и три минуты у manual и selected.
func TestTimeoutFollowsCore(t *testing.T) {
	d := New(Options{})
	for _, mode := range []string{"auto", "manual", "selected", "невнятный"} {
		if got, want := d.timeout(mode), core.CaptchaTimeout(mode); got != want {
			t.Errorf("окно режима %q = %v, у ядра %v", mode, got, want)
		}
	}
	if core.CaptchaTimeout("manual") <= core.CaptchaTimeout("auto") {
		t.Error("окно ручного режима не длиннее автоматического — что-то не так с картой режимов")
	}
	// selected — то же окно человеку, что manual; срок ему поднят до того же
	// правилом в scripts/vendorcore.
	if core.CaptchaTimeout("selected") != core.CaptchaTimeout("manual") {
		t.Errorf("окно selected %v, у manual %v", core.CaptchaTimeout("selected"), core.CaptchaTimeout("manual"))
	}
}

// ──────────────────────── таймаут не вешает ядро ────────────────────────

// Решатель завис — ядро всё равно получает ответ, и раньше, чем перестанет
// слушать.
func TestStuckSolverStillAnswersInTime(t *testing.T) {
	solver := &fakeSolver{token: "поздний-токен", block: make(chan struct{})}
	t.Cleanup(func() { close(solver.block) })

	// Ядро ждёт 300 мс, запас 100 мс: решатель обязан сдаться на двухстах.
	d := newDispatcher(t, Options{
		Solver:  solver,
		Control: core.NewControl(),
		Margin:  100 * time.Millisecond,
	}, 300*time.Millisecond)

	start := time.Now()
	d.Observe(required("manual"))

	got := answer(t, 2*time.Second)
	elapsed := time.Since(start)

	if got != "error:cancelled" {
		t.Fatalf("ядро получило %q, ожидался отказ", got)
	}
	if elapsed >= 300*time.Millisecond {
		t.Errorf("отказ пришёл через %v, а ядро перестало слушать на 300ms", elapsed)
	}
}

// Ошибка решателя — это отказ, и ядро узнаёт о нём сразу, не выстаивая окно.
func TestSolverErrorDeclinesImmediately(t *testing.T) {
	solver := &fakeSolver{err: errors.New("пользователь закрыл окно")}
	d := newDispatcher(t, Options{Solver: solver, Control: core.NewControl()}, 10*time.Second)

	start := time.Now()
	d.Observe(required("manual"))

	if got := answer(t, 2*time.Second); got != "error:cancelled" {
		t.Fatalf("ядро получило %q, ожидался отказ", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("отказ пришёл через %v, ожидался сразу", elapsed)
	}
}

// Пустой токен без ошибки — тоже отказ: обещать успех и не дать токен нельзя.
func TestEmptyTokenIsRefusal(t *testing.T) {
	solver := &fakeSolver{token: "   "}
	d := newDispatcher(t, Options{Solver: solver, Control: core.NewControl()}, 10*time.Second)

	d.Observe(required("manual"))

	if got := answer(t, 2*time.Second); got != "error:cancelled" {
		t.Errorf("ядро получило %q, ожидался отказ", got)
	}
}

// Решателя нет — отказ немедленный и решать никого не просят.
func TestNoSolverDeclinesImmediately(t *testing.T) {
	d := newDispatcher(t, Options{Control: core.NewControl()}, 10*time.Second)

	start := time.Now()
	d.Observe(required("manual"))

	if got := answer(t, 2*time.Second); got != "error:cancelled" {
		t.Fatalf("ядро получило %q, ожидался отказ", got)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("отказ пришёл через %v, ожидался сразу", elapsed)
	}
}

// Десятисекундное окно WBV Auto человеку не предлагают: отказ приходит сразу,
// и ядро идёт дальше, а не стоит десять секунд впустую.
func TestHumanSolverRefusesShortWindow(t *testing.T) {
	solver := &fakeSolver{human: true, token: "не-успеет"}
	d := newDispatcher(t, Options{
		Solver:      solver,
		Control:     core.NewControl(),
		HumanWindow: 30 * time.Second,
	}, 10*time.Second)

	d.Observe(required("auto"))

	if got := answer(t, 2*time.Second); got != "error:cancelled" {
		t.Fatalf("ядро получило %q, ожидался отказ", got)
	}
	if calls := solver.seen(); len(calls) != 0 {
		t.Errorf("человека спросили на окне в 10 с: %d вызовов", len(calls))
	}
}

// А три минуты ручного режима — предлагают.
func TestHumanSolverTakesLongWindow(t *testing.T) {
	solver := &fakeSolver{human: true, token: "успел"}
	d := newDispatcher(t, Options{
		Solver:      solver,
		Control:     core.NewControl(),
		HumanWindow: 30 * time.Second,
	}, 60*time.Second)

	d.Observe(required("manual"))

	if got := answer(t, 2*time.Second); got != "успел" {
		t.Errorf("ядро получило %q, ожидался токен", got)
	}
}

// Режим wv, в котором оболочка запускает ядро: на капчу сразу приходит
// selected. Человек получает окно на полный срок ядра — тот же, что у manual,
// за вычетом запаса, — а не отказ ErrTooShort.
func TestHumanSolverTakesSelectedWindow(t *testing.T) {
	solver := &fakeSolver{human: true, token: "решил-человек"}
	drain(t)
	d := New(Options{Solver: solver, Control: core.NewControl()})
	t.Cleanup(func() {
		d.Close()
		drain(t)
	})

	before := time.Now()
	d.Observe(required("selected"))
	after := time.Now()

	if got := answer(t, 2*time.Second); got != "решил-человек" {
		t.Fatalf("ядро получило %q, ожидался токен", got)
	}
	calls := solver.seen()
	if len(calls) != 1 {
		t.Fatalf("решатель вызван %d раз, ожидался один", len(calls))
	}
	req := calls[0]
	if req.Mode != ModeSelected {
		t.Errorf("Mode = %q, ожидался %q", req.Mode, ModeSelected)
	}
	want := core.CaptchaTimeout("selected") - DefaultMargin
	// Срок отсчитывается в Observe, от момента события.
	if req.Deadline.Before(before.Add(want)) || req.Deadline.After(after.Add(want)) {
		t.Errorf("срок %v, ожидалось %v: полный срок ядра минус запас", req.Deadline.Sub(before), want)
	}
	if want != core.CaptchaTimeout("manual")-DefaultMargin {
		t.Errorf("срок selected %v отличается от manual", want)
	}
}

// ──────────────────────── один решатель на всех ────────────────────────

// Второй воркер, попросивший капчу во время решения, отбрасывается молча:
// отказ вышиб бы из канала токен, добытый для первого.
func TestSecondRequestWhileBusyIsIgnored(t *testing.T) {
	solver := &fakeSolver{token: "токен-первого", block: make(chan struct{})}
	d := newDispatcher(t, Options{Solver: solver, Control: core.NewControl()}, 10*time.Second)

	d.Observe(required("manual"))

	// Ждём, пока решатель возьмётся за первую просьбу.
	deadline := time.Now().Add(2 * time.Second)
	for len(solver.seen()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(solver.seen()) == 0 {
		t.Fatal("решатель так и не взялся за первую капчу")
	}

	second := required("manual")
	second.Session = 42
	d.Observe(second)

	// В канал за это время не должно попасть ничего: первый ещё решает.
	if got := answer(t, 200*time.Millisecond); got != "" {
		t.Fatalf("в канал ушло %q, хотя первый воркер ещё ждёт свой токен", got)
	}

	close(solver.block)
	if got := answer(t, 2*time.Second); got != "токен-первого" {
		t.Errorf("ядро получило %q, ожидался токен первого воркера", got)
	}
	if calls := solver.seen(); len(calls) != 1 {
		t.Errorf("решатель вызван %d раз, ожидался один: второго просить некого", len(calls))
	}
}

// Освободившийся решатель берётся за следующую капчу.
func TestSolverFreeAfterFirstRequest(t *testing.T) {
	solver := &fakeSolver{token: "токен"}
	d := newDispatcher(t, Options{Solver: solver, Control: core.NewControl()}, 10*time.Second)

	for i := range 2 {
		d.Observe(required("manual"))
		if got := answer(t, 2*time.Second); got != "токен" {
			t.Fatalf("капча %d: ядро получило %q", i+1, got)
		}
	}
	if calls := solver.seen(); len(calls) != 2 {
		t.Errorf("решатель вызван %d раз, ожидалось два", len(calls))
	}
}

// ──────────────────────── остановка ────────────────────────

// Close отменяет ctx решателя и дожидается его: клиент не уходит, оставив
// человека перед приглашением.
func TestCloseCancelsAndWaits(t *testing.T) {
	solver := &fakeSolver{token: "не пригодится", block: make(chan struct{})}
	defer close(solver.block)

	drain(t)
	d := New(Options{Solver: solver, Control: core.NewControl()})
	d.timeout = func(string) time.Duration { return time.Hour }

	d.Observe(required("manual"))

	deadline := time.Now().Add(2 * time.Second)
	for len(solver.seen()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(solver.seen()) == 0 {
		t.Fatal("решатель так и не взялся за капчу")
	}

	done := make(chan struct{})
	go func() { d.Close(); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close не вернулся: отмена не дошла до решателя")
	}
	drain(t)
}

// Остановка клиента освобождает ядро сама, без Close: решатель получает
// отмену, ядро — отказ. Иначе Ctrl+C при открытом окне капчи ждал бы срока
// капчи: ядро ждёт ответа без своего ctx, а Close зовётся уже после
// остановки туннеля, которая ждёт ядро.
//
// Режимы — оба, в которых окно ждёт человека: manual цепочки auto и selected
// режима wv.
func TestStopCancelsSolverAndDeclines(t *testing.T) {
	for _, mode := range []string{"manual", "selected"} {
		t.Run(mode, func(t *testing.T) { testStopCancelsSolverAndDeclines(t, mode) })
	}
}

func testStopCancelsSolverAndDeclines(t *testing.T, mode string) {
	solver := &fakeSolver{human: true, token: "не пригодится", block: make(chan struct{})}
	defer close(solver.block)

	stop, cancel := context.WithCancel(context.Background())
	d := newDispatcher(t, Options{Solver: solver, Control: core.NewControl(), Stop: stop}, time.Hour)

	d.Observe(required(mode))
	deadline := time.Now().Add(2 * time.Second)
	for len(solver.seen()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(solver.seen()) == 0 {
		t.Fatal("решатель так и не взялся за капчу")
	}

	cancel()
	if got := answer(t, 2*time.Second); got != "error:cancelled" {
		t.Fatalf("ядро получило %q, ожидался отказ по остановке", got)
	}

	// Ядро после отказа может спросить снова — остановленный клиент
	// отказывает сразу, решатель не трогается.
	d.Observe(required(mode))
	if got := answer(t, 2*time.Second); got != "error:cancelled" {
		t.Errorf("новая просьба после остановки: ядро получило %q, ожидался отказ", got)
	}
	if n := len(solver.seen()); n != 1 {
		t.Errorf("решатель вызван %d раз, ожидался один", n)
	}
}

// После Close новые события не берутся в работу, но ядро всё равно получает
// отказ — иначе воркер выстаивал бы таймаут на уже остановленном клиенте.
func TestObserveAfterCloseDeclines(t *testing.T) {
	solver := &fakeSolver{token: "поздно"}
	drain(t)
	t.Cleanup(func() { drain(t) })

	d := New(Options{Solver: solver, Control: core.NewControl()})
	d.Close()

	d.Observe(required("manual"))

	if got := answer(t, 2*time.Second); got != "error:cancelled" {
		t.Errorf("ядро получило %q, ожидался отказ", got)
	}
	if calls := solver.seen(); len(calls) != 0 {
		t.Errorf("решатель вызван после Close: %d раз", len(calls))
	}
}

func TestCloseTwice(t *testing.T) {
	d := New(Options{})
	d.Close()
	d.Close()
}

// Close дожидается уборки окна, которая идёт уже после Solve, — молча, если
// она короткая. Окно на стенде — TestWebView2CloseWaitsCleanup.
func TestCloseWaitsCleanups(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	d := New(Options{Logf: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, format)
	}})

	cleaned := cleanups.add()
	time.AfterFunc(300*time.Millisecond, cleaned)
	start := time.Now()
	d.Close()
	if took := time.Since(start); took < 250*time.Millisecond {
		t.Errorf("Close вернулся через %s, не дождавшись уборки", took)
	}
	if !cleanups.wait(0) {
		t.Error("после Close уборка не кончилась")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) != 0 {
		t.Errorf("строки при короткой уборке: %q", lines)
	}
}

// Ожидание уборки кончается по сроку, а учёт после этого работает дальше.
func TestCleanupSetWaitLimit(t *testing.T) {
	var c cleanupSet
	if !c.wait(0) {
		t.Fatal("без уборок ожидание не вернулось сразу")
	}

	cleaned := c.add()
	if c.wait(50 * time.Millisecond) {
		t.Fatal("дождался незаконченной уборки")
	}
	cleaned()
	cleaned() // повтор безвреден
	if !c.wait(0) {
		t.Fatal("уборка кончилась, а ожидание её не видит")
	}

	cleaned = c.add()
	time.AfterFunc(20*time.Millisecond, cleaned)
	if !c.wait(time.Second) {
		t.Fatal("вторую уборку не дождался")
	}
}

// ──────────────────────── чужие события ────────────────────────

func TestObserveIgnoresOtherEvents(t *testing.T) {
	solver := &fakeSolver{token: "токен"}
	d := newDispatcher(t, Options{Solver: solver, Control: core.NewControl()}, 10*time.Second)

	for _, ev := range []core.Event{
		core.EventLog{Level: core.LevelInfo, Tag: "КЛИЕНТ", Msg: "поехали"},
		core.EventStage{Stage: core.StageVK, Status: core.StageOK},
		core.EventStats{ActiveWorkers: 9},
		core.EventVkAuthRequired{Link: "https://vk.com/call/join/x"},
		core.EventConfig{WireGuard: "[Interface]"},
	} {
		d.Observe(ev)
	}

	if got := answer(t, 200*time.Millisecond); got != "" {
		t.Errorf("в канал ушло %q на событиях, к капче не относящихся", got)
	}
	if calls := solver.seen(); len(calls) != 0 {
		t.Errorf("решатель вызван %d раз на чужих событиях", len(calls))
	}
}

// ──────────────────────── мелочи ────────────────────────

func TestNewFillsDefaults(t *testing.T) {
	d := New(Options{})
	if d.opts.Margin != DefaultMargin {
		t.Errorf("запас = %v, ожидался %v", d.opts.Margin, DefaultMargin)
	}
	if d.opts.HumanWindow != DefaultHumanWindow {
		t.Errorf("окно человека = %v, ожидалось %v", d.opts.HumanWindow, DefaultHumanWindow)
	}
}

func TestRequestLeft(t *testing.T) {
	now := time.Now()
	if got := (Request{}).Left(now); got != 0 {
		t.Errorf("без срока Left = %v, ожидался ноль", got)
	}
	if got := (Request{Deadline: now.Add(30 * time.Second)}).Left(now); got != 30*time.Second {
		t.Errorf("Left = %v, ожидалось 30s", got)
	}
}

// Вывод у пакета собственного нет: без Logf он молчит и не падает.
func TestNilLogfIsSilent(t *testing.T) {
	solver := &fakeSolver{token: "токен"}
	d := newDispatcher(t, Options{Solver: solver, Control: core.NewControl()}, 10*time.Second)

	d.Observe(required("manual"))
	if got := answer(t, 2*time.Second); got != "токен" {
		t.Errorf("ядро получило %q", got)
	}
}

// Control может быть нулевым: вызовы ядра на nil безопасны.
func TestNilControlDoesNotPanic(t *testing.T) {
	solver := &fakeSolver{token: "токен"}
	d := newDispatcher(t, Options{Solver: solver}, 10*time.Second)

	d.Observe(required("manual"))

	deadline := time.Now().Add(2 * time.Second)
	for len(solver.seen()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if len(solver.seen()) == 0 {
		t.Error("решатель не вызван")
	}
}
