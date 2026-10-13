package supervisor

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

// Ядро в этих тестах подменено: живого сервера VK и TURN-relay у нас нет, а
// проверять здесь нужно не ядро, а то, что вокруг него, — раздачу событий,
// подъём и снятие datapath, задержки между попытками и остановку. Настоящий
// сквозной запуск — задача H2.
type fakeCore func(ctx context.Context, cfg core.Config, sink core.Sink) error

// fakeDatapath — datapath, который ничего не поднимает, но честно считает
// вызовы. Close у него, как и у настоящего, безопасен до Start и повторно.
type fakeDatapath struct {
	startErr error

	mu      sync.Mutex
	starts  int
	closes  int
	conf    *datapath.WGConfig
	started chan struct{}
}

func newFakeDatapath() *fakeDatapath {
	return &fakeDatapath{started: make(chan struct{})}
}

func (d *fakeDatapath) Start(ctx context.Context, conf *datapath.WGConfig) error {
	d.mu.Lock()
	d.starts++
	first := d.starts == 1
	d.conf = conf
	err := d.startErr
	d.mu.Unlock()

	if first {
		close(d.started)
	}
	return err
}

func (d *fakeDatapath) Endpoint() netip.AddrPort { return netip.AddrPort{} }

func (d *fakeDatapath) Requires() datapath.Requirements { return datapath.Requirements{} }

func (d *fakeDatapath) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closes++
	return nil
}

func (d *fakeDatapath) counts() (starts, closes int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.starts, d.closes
}

// recorder собирает то, что цикл отдаёт наружу: строки о шагах и о смене
// состояния. Сериализацию обеспечивает сам цикл, но свой замок здесь нужен —
// читает их горутина теста.
type recorder struct {
	mu     sync.Mutex
	stages []core.EventStage
	states []State
}

func (r *recorder) onEvent(ev core.Event) {
	stage, ok := ev.(core.EventStage)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stages = append(r.stages, stage)
}

func (r *recorder) onTransition(tr Transition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.states = append(r.states, tr.To)
}

func (r *recorder) seenState(want State) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.states {
		if s == want {
			return true
		}
	}
	return false
}

func (r *recorder) stageStatus(want core.Stage) []core.StageStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []core.StageStatus
	for _, s := range r.stages {
		if s.Stage == want {
			out = append(out, s.Status)
		}
	}
	return out
}

func (r *recorder) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := make([]string, 0, len(r.stages)+len(r.states))
	for _, s := range r.stages {
		lines = append(lines, fmt.Sprintf("шаг %s %s %s", s.Stage, s.Status, s.Msg))
	}
	for _, s := range r.states {
		lines = append(lines, "состояние "+string(s))
	}
	return strings.Join(lines, "\n")
}

// newTestTunnel собирает цикл с подменённым ядром и подменённым ожиданием:
// настоящие задержки в 1, 2, 4 секунды растянули бы тест на минуту.
func newTestTunnel(t *testing.T, opts Options, run fakeCore) (*Tunnel, *[]time.Duration) {
	t.Helper()

	tun, err := NewTunnel(opts)
	if err != nil {
		t.Fatalf("NewTunnel: %v", err)
	}
	tun.runCore = func(ctx context.Context, cfg core.Config, sink core.Sink, _ *core.Control) error {
		return run(ctx, cfg, sink)
	}

	var delays []time.Duration
	tun.sleep = func(ctx context.Context, d time.Duration) bool {
		delays = append(delays, d)
		return ctx.Err() == nil
	}
	return tun, &delays
}

// connectTo проводит подменённое ядро по шагам до выдачи конфига WireGuard
// и дальше ждёт отмены — как настоящее, у которого воркеры на линии.
func connectTo(ctx context.Context, sink core.Sink) {
	for _, stage := range []core.Stage{core.StageVK, core.StageTURN, core.StageDTLS, core.StageWorkers} {
		sink.Emit(core.EventStage{Stage: stage, Status: core.StageOK})
	}
	sink.Emit(core.EventConfig{WireGuard: testWireGuardConf("127.0.0.1:51820")})
}

// testWireGuardConf — конфиг в том виде, в каком его отдаёт сервер. Ключи
// произвольные: рукопожатия в этих тестах всё равно не будет.
func testWireGuardConf(endpoint string) string {
	key := func(seed byte) string {
		raw := make([]byte, 32)
		for i := range raw {
			raw[i] = seed + byte(i)
		}
		return base64.StdEncoding.EncodeToString(raw)
	}
	return "[Interface]\n" +
		"PrivateKey = " + key(1) + "\n" +
		"Address = 10.7.0.2/32\n" +
		"DNS = 8.8.8.8\n" +
		"MTU = 1280\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + key(64) + "\n" +
		"AllowedIPs = 0.0.0.0/0\n" +
		"Endpoint = " + endpoint + "\n" +
		"PersistentKeepalive = 25"
}

// ──────────────────────── полный цикл ────────────────────────

// Успешное подключение: шаги ядра доводят автомат до «поднимается туннель»,
// конфиг поднимает datapath, а шаги socks и done — до «туннель работает».
func TestTunnelConnectsAndStopsCleanly(t *testing.T) {
	dp := newFakeDatapath()
	rec := &recorder{}

	tun, _ := newTestTunnel(t, Options{
		Config:       core.Config{Peer: "1.2.3.4:9000", Password: "пароль"},
		Hashes:       []string{"hash-one"},
		NewDatapath:  func() (datapath.Datapath, error) { return dp, nil },
		OnEvent:      rec.onEvent,
		OnTransition: rec.onTransition,
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		connectTo(ctx, sink)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()

	select {
	case <-dp.started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatalf("datapath так и не поднялся:\n%s", rec.dump())
	}

	waitFor(t, func() bool { return tun.State() == StateConnected },
		func() string { return "состояние " + string(tun.State()) + "\n" + rec.dump() })

	cancel()
	if err := waitErr(t, done); err != nil {
		t.Errorf("Run после отмены = %v, а остановка по просьбе ошибкой не считается", err)
	}

	if got := tun.State(); got != StateStopping {
		t.Errorf("состояние после Run = %q, ожидалось %q", got, StateStopping)
	}
	starts, closes := dp.counts()
	if starts != 1 || closes != 1 {
		t.Errorf("datapath: %d Start, %d Close; ожидалось по одному", starts, closes)
	}
	if got := rec.stageStatus(core.StageDone); len(got) != 1 || got[0] != core.StageOK {
		t.Errorf("шаг done = %v, ожидался один ok:\n%s", got, rec.dump())
	}
	if got := rec.stageStatus(core.StageSocks); len(got) != 2 {
		t.Errorf("шаг socks = %v, ожидались running и ok:\n%s", got, rec.dump())
	}
}

// Конфиг, выданный сервером, доезжает до datapath разобранным.
func TestTunnelPassesServerConfigToDatapath(t *testing.T) {
	dp := newFakeDatapath()

	tun, _ := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return dp, nil },
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		connectTo(ctx, sink)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()

	select {
	case <-dp.started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("datapath так и не поднялся")
	}
	cancel()
	waitErr(t, done)

	dp.mu.Lock()
	conf := dp.conf
	dp.mu.Unlock()

	if conf == nil {
		t.Fatal("datapath получил пустой конфиг")
	}
	if conf.Endpoint != "127.0.0.1:51820" {
		t.Errorf("Endpoint = %q, ожидался 127.0.0.1:51820", conf.Endpoint)
	}
	if len(conf.Addresses) == 0 {
		t.Error("Addresses пуст: конфиг разобран не до конца")
	}
}

// ──────────────────── снятие datapath ────────────────────

// Ядро упало, не дойдя до конфига: поднимать было нечего, но Close всё равно
// зовётся — реализация обязана его пережить (D1), а цикл обязан его сделать.
func TestDatapathClosedWhenCoreFailsEarly(t *testing.T) {
	dp := newFakeDatapath()

	tun, _ := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return dp, nil },
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		return errors.New("адрес сервера не разбирается")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Отменяем на первом же ожидании: одна попытка — всё, что нужно.
	tun.sleep = func(context.Context, time.Duration) bool { cancel(); return false }

	if err := tun.Run(ctx); err != nil {
		t.Errorf("Run = %v, ожидался nil: остановку попросили", err)
	}
	if starts, closes := dp.counts(); starts != 0 || closes != 1 {
		t.Errorf("datapath: %d Start, %d Close; ожидалось 0 и 1", starts, closes)
	}
}

// Паника в ядре не роняет клиент и не оставляет datapath поднятым: она
// становится обычным отказом попытки.
func TestCorePanicClosesDatapathAndRetries(t *testing.T) {
	dp := newFakeDatapath()
	var attempts int

	tun, _ := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return dp, nil },
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		attempts++
		connectTo(ctx, sink)
		panic("индекс за границей среза")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Две попытки: первая паникует, вторая доказывает, что цикл жив.
	var waits int
	tun.sleep = func(context.Context, time.Duration) bool {
		waits++
		if waits >= 2 {
			cancel()
			return false
		}
		return true
	}

	if err := tun.Run(ctx); err != nil {
		t.Errorf("Run = %v, ожидался nil", err)
	}
	if attempts != 2 {
		t.Errorf("попыток = %d, ожидалось 2: паника не должна кончать цикл", attempts)
	}
	if starts, closes := dp.counts(); starts != 2 || closes != 2 {
		t.Errorf("datapath: %d Start, %d Close; ожидалось по два", starts, closes)
	}
}

// Отказ datapath кончает попытку: ядро живо и держит воркеров, а подавать
// в них нечего.
func TestDatapathStartFailureEndsAttempt(t *testing.T) {
	dp := newFakeDatapath()
	dp.startErr = errors.New("порт 1080 занят")
	rec := &recorder{}

	tun, _ := newTestTunnel(t, Options{
		Hashes:       []string{"hash-one"},
		NewDatapath:  func() (datapath.Datapath, error) { return dp, nil },
		OnEvent:      rec.onEvent,
		OnTransition: rec.onTransition,
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		connectTo(ctx, sink)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tun.sleep = func(context.Context, time.Duration) bool { cancel(); return false }

	if err := tun.Run(ctx); err != nil {
		t.Errorf("Run = %v, ожидался nil", err)
	}
	if got := rec.stageStatus(core.StageSocks); len(got) != 2 || got[1] != core.StageFailed {
		t.Errorf("шаг socks = %v, ожидались running и failed:\n%s", got, rec.dump())
	}
	if !rec.seenState(StateReconnecting) {
		t.Errorf("перехода в %q не было:\n%s", StateReconnecting, rec.dump())
	}
	if got := tun.Machine().LastError(); got == nil || !strings.Contains(got.Error(), "порт 1080 занят") {
		t.Errorf("причина отказа = %v, ожидалась про занятый порт", got)
	}
}

// ──────────────────────── перезапуск ────────────────────────

// Задержки между попытками — те самые 1, 2, 4, 8 секунд.
func TestBackoffBetweenAttempts(t *testing.T) {
	tun, delays := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		return errors.New("все воркеры завершились сами")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	real := tun.sleep
	tun.sleep = func(ctx context.Context, d time.Duration) bool {
		if len(*delays) >= 3 {
			cancel()
			return false
		}
		return real(ctx, d)
	}

	if err := tun.Run(ctx); err != nil {
		t.Errorf("Run = %v, ожидался nil", err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(*delays) != len(want) {
		t.Fatalf("задержек = %v, ожидалось %v", *delays, want)
	}
	for i, d := range want {
		if (*delays)[i] != d {
			t.Errorf("задержка %d = %v, ожидалась %v", i+1, (*delays)[i], d)
		}
	}
}

// Удавшееся подключение обнуляет счётчик: туннель, проработавший час, должен
// переподключаться через секунду.
func TestConnectedResetsBackoff(t *testing.T) {
	var attempts int

	tun, delays := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		attempts++
		if attempts == 1 {
			return errors.New("первая попытка не задалась")
		}
		connectTo(ctx, sink)
		return errors.New("туннель развалился после часа работы")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	real := tun.sleep
	tun.sleep = func(ctx context.Context, d time.Duration) bool {
		if len(*delays) >= 2 {
			cancel()
			return false
		}
		return real(ctx, d)
	}

	if err := tun.Run(ctx); err != nil {
		t.Errorf("Run = %v, ожидался nil", err)
	}
	want := []time.Duration{time.Second, time.Second}
	if fmt.Sprint(*delays) != fmt.Sprint(want) {
		t.Errorf("задержки = %v, ожидались %v: после Connected счётчик обнуляется", *delays, want)
	}
}

// ──────────────────────── хеши звонков ────────────────────────

// Один негодный хеш из трёх туннель не останавливает: следующая попытка идёт
// с оставшимися двумя.
func TestDeadHashOfThreeKeepsTunnelGoing(t *testing.T) {
	var offered [][]string

	tun, _ := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one", "hash-two", "hash-three"},
		NewDatapath: func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		offered = append(offered, cfg.Hashes)
		sink.Emit(core.EventHashFailed{
			Hash:   "hash-two",
			Group:  2,
			Status: core.HashCheckDead,
			Msg:    "звонок закрыт",
		})
		return errors.New("группа не стартовала")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var waits int
	tun.sleep = func(context.Context, time.Duration) bool {
		waits++
		if waits >= 2 {
			cancel()
			return false
		}
		return true
	}

	if err := tun.Run(ctx); err != nil {
		t.Errorf("Run = %v, ожидался nil: годные хеши ещё были", err)
	}
	if len(offered) != 2 {
		t.Fatalf("попыток = %d, ожидалось 2", len(offered))
	}
	if len(offered[0]) != 3 {
		t.Errorf("первой попытке предложено %v, ожидались все три", offered[0])
	}
	if fmt.Sprint(offered[1]) != fmt.Sprint([]string{"hash-one", "hash-three"}) {
		t.Errorf("второй попытке предложено %v, ожидались hash-one и hash-three", offered[1])
	}
	if got := tun.Hashes().DeadReason("hash-two"); got != "звонок закрыт" {
		t.Errorf("причина исключения hash-two = %q, ожидалась «звонок закрыт»", got)
	}
}

// Годных хешей не осталось — цикл кончается сам, и это единственный способ.
func TestExhaustedHashesStopTunnel(t *testing.T) {
	tun, _ := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		sink.Emit(core.EventHashFailed{
			Hash:   "hash-one",
			Group:  1,
			Status: core.HashCheckDead,
			Msg:    "звонок закрыт",
		})
		return errors.New("группа не стартовала")
	})

	err := tun.Run(context.Background())
	if !errors.Is(err, ErrHashesExhausted) {
		t.Fatalf("Run = %v, ожидалась ошибка с ErrHashesExhausted", err)
	}
	if !strings.Contains(err.Error(), "группа не стартовала") {
		t.Errorf("ошибка = %v, ожидалась вместе с причиной отказа попытки", err)
	}
	if got := tun.State(); got != StateStopping {
		t.Errorf("состояние = %q, ожидалось %q", got, StateStopping)
	}
}

// ──────────────────────────── watchdog ────────────────────────────

// Туннель поднят, мы отправляем, ответа нет: сначала «трафика нет», потом
// перезапуск.
func TestWatchdogDegradesThenRestarts(t *testing.T) {
	rec := &recorder{}
	var attempts int

	tun, _ := newTestTunnel(t, Options{
		Hashes:            []string{"hash-one"},
		NewDatapath:       func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
		OnEvent:           rec.onEvent,
		OnTransition:      rec.onTransition,
		StallWindow:       40 * time.Millisecond,
		StallPoll:         5 * time.Millisecond,
		StallRestartAfter: 40 * time.Millisecond,
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		attempts++
		connectTo(ctx, sink)

		// Отправляем и отправляем, а в ответ тишина. Счётчики нарастающие,
		// как у настоящего ядра.
		up := int64(1000)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				up += 500
				sink.Emit(core.EventStats{ActiveWorkers: 9, BytesUp: up, BytesDown: 4096})
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tun.sleep = func(context.Context, time.Duration) bool { cancel(); return false }

	if err := tun.Run(ctx); err != nil {
		t.Errorf("Run = %v, ожидался nil", err)
	}
	if attempts != 1 {
		t.Errorf("попыток = %d, ожидалась одна", attempts)
	}
	if !rec.seenState(StateDegraded) {
		t.Errorf("перехода в %q не было:\n%s", StateDegraded, rec.dump())
	}
	if !rec.seenState(StateReconnecting) {
		t.Errorf("перехода в %q не было: watchdog не оборвал попытку:\n%s", StateReconnecting, rec.dump())
	}
	if got := tun.Machine().LastError(); got == nil || !strings.Contains(got.Error(), "молчит") {
		t.Errorf("причина перезапуска = %v, ожидалась про молчание туннеля", got)
	}
}

// Ответ пришёл до истечения терпения — туннель возвращается в рабочее
// состояние, а не рвётся: молчание в двадцать пять секунд бывает и у живого.
func TestWatchdogRecoversWhenTrafficReturns(t *testing.T) {
	rec := &recorder{}

	tun, _ := newTestTunnel(t, Options{
		Hashes:            []string{"hash-one"},
		NewDatapath:       func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
		OnTransition:      rec.onTransition,
		StallWindow:       40 * time.Millisecond,
		StallPoll:         5 * time.Millisecond,
		StallRestartAfter: time.Hour, // рвать соединение в этом тесте нечему
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		connectTo(ctx, sink)

		up, down := int64(1000), int64(4096)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				up += 500
				// Молчим, пока не объявят деградацию, потом отвечаем.
				if rec.seenState(StateDegraded) {
					down += 500
				}
				sink.Emit(core.EventStats{ActiveWorkers: 9, BytesUp: up, BytesDown: down})
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()

	waitFor(t, func() bool { return tun.State() == StateConnected && rec.seenState(StateDegraded) },
		func() string { return "состояние " + string(tun.State()) + "\n" + rec.dump() })

	cancel()
	waitErr(t, done)

	if rec.seenState(StateReconnecting) {
		t.Errorf("туннель перезапустили, хотя трафик вернулся:\n%s", rec.dump())
	}
}

// ──────────────────────── после остановки ────────────────────────

// «Не остаётся ни висящих горутин, ни поднятого SOCKS-порта» — первая половина
// критерия F3.
func TestNoGoroutinesLeftAfterStop(t *testing.T) {
	before := runtime.NumGoroutine()

	for range 3 {
		dp := newFakeDatapath()
		tun, _ := newTestTunnel(t, Options{
			Hashes:      []string{"hash-one"},
			NewDatapath: func() (datapath.Datapath, error) { return dp, nil },
			StallPoll:   5 * time.Millisecond,
		}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
			connectTo(ctx, sink)
			<-ctx.Done()
			return nil
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- tun.Run(ctx) }()

		select {
		case <-dp.started:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("datapath так и не поднялся")
		}
		cancel()
		waitErr(t, done)
	}

	// Горутины теста и рантайма заканчиваются не мгновенно.
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("горутин было %d, стало %d: цикл что-то оставил за собой", before, after)
	}
}

// Вторая половина критерия: настоящий datapath socks, настоящий слушатель —
// после остановки порт свободен.
func TestSocksPortFreedAfterStop(t *testing.T) {
	var socks *datapath.Socks

	tun, _ := newTestTunnel(t, Options{
		Hashes: []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) {
			socks = datapath.NewSocks(datapath.SocksOptions{Listen: "127.0.0.1:0"})
			return socks, nil
		},
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		connectTo(ctx, sink)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()

	waitFor(t, func() bool { return tun.State() == StateConnected },
		func() string { return "состояние " + string(tun.State()) })

	addr := socks.ListenAddr()
	if addr == nil {
		t.Fatal("SOCKS5 не слушает, хотя туннель объявлен рабочим")
	}
	conn, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatalf("порт %s не принимает соединения до остановки: %v", addr, err)
	}
	_ = conn.Close()

	cancel()
	waitErr(t, done)

	// Слушатель закрыт — соединиться больше не с чем.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr.String(), 200*time.Millisecond)
		if err != nil {
			return
		}
		_ = c.Close()
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("порт %s всё ещё принимает соединения после остановки", addr)
}

// ──────────────────────────── Ctrl+C ────────────────────────────

// Отдельного сигнала в тесте не послать — на Windows их не подделать, — но
// проверить можно то, ради чего Interrupt и нужен циклу: контекст живой, пока
// не остановят, и мёртвый после.
func TestInterruptContextStops(t *testing.T) {
	ctx, stop := Interrupt(context.Background())
	if ctx.Err() != nil {
		t.Fatalf("контекст мёртв сразу после Interrupt: %v", ctx.Err())
	}

	stop()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("контекст не отменился после stop()")
	}

	// Повторный вызов безвреден: он идёт из defer.
	stop()
}

// Отмена родителя доходит до контекста Interrupt: цикл останавливают и без
// сигнала — например, когда кончились хеши.
func TestInterruptFollowsParent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, stop := Interrupt(parent)
	defer stop()

	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("отмена родителя не дошла")
	}
}

// ──────────────────────────── настройки ────────────────────────────

func TestNewTunnelRequiresDatapath(t *testing.T) {
	if _, err := NewTunnel(Options{}); err == nil {
		t.Error("NewTunnel без конструктора datapath не пожаловался")
	}
}

func TestNewTunnelFillsDefaults(t *testing.T) {
	tun, err := NewTunnel(Options{
		NewDatapath: func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
	})
	if err != nil {
		t.Fatalf("NewTunnel: %v", err)
	}
	switch {
	case tun.opts.DatapathStage != core.StageSocks:
		t.Errorf("шаг datapath = %q, ожидался %q", tun.opts.DatapathStage, core.StageSocks)
	case tun.opts.StallWindow != DefaultStallWindow:
		t.Errorf("окно watchdog = %v, ожидалось %v", tun.opts.StallWindow, DefaultStallWindow)
	case tun.opts.StallPoll != DefaultStallPoll:
		t.Errorf("опрос watchdog = %v, ожидался %v", tun.opts.StallPoll, DefaultStallPoll)
	case tun.opts.StallRestartAfter != DefaultStallWindow:
		t.Errorf("терпение до перезапуска = %v, ожидалось %v", tun.opts.StallRestartAfter, DefaultStallWindow)
	}
}

// ──────────────────────────── помощники ────────────────────────────

func waitFor(t *testing.T, ok func() bool, why func() string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("не дождались:\n%s", why())
}

func waitErr(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run не вернулся после отмены")
		return nil
	}
}

// ──────────────────────── шаг dns ────────────────────────

// Проверка резолвера идёт до ядра и закрывает шаг dns; её отказ — отказ
// попытки: ядро не запускается, назначается задержка, следующая попытка
// проверяет снова.
func TestDNSProbeFailureEndsAttemptBeforeCore(t *testing.T) {
	dp := newFakeDatapath()
	rec := &recorder{}

	var probes, cores int
	var mu sync.Mutex

	tun, delays := newTestTunnel(t, Options{
		Config:      core.Config{Peer: "1.2.3.4:9000", Password: "пароль"},
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return dp, nil },
		ProbeDNS: func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			probes++
			if probes == 1 {
				return errors.New("резолвер молчит")
			}
			return nil
		},
		OnEvent:      rec.onEvent,
		OnTransition: rec.onTransition,
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		mu.Lock()
		cores++
		mu.Unlock()
		connectTo(ctx, sink)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()

	select {
	case <-dp.started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatalf("datapath так и не поднялся:\n%s", rec.dump())
	}
	cancel()
	if err := waitErr(t, done); err != nil {
		t.Errorf("Run после отмены = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if probes != 2 || cores != 1 {
		t.Errorf("проверок dns %d, запусков ядра %d; ожидалось 2 и 1", probes, cores)
	}
	want := []core.StageStatus{core.StageRunning, core.StageFailed, core.StageRunning, core.StageOK}
	if got := rec.stageStatus(core.StageDNS); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("шаг dns = %v, ожидалось %v:\n%s", got, want, rec.dump())
	}
	if len(*delays) != 1 {
		t.Errorf("задержек %d, ожидалась одна — между отказом dns и второй попыткой", len(*delays))
	}
	if !rec.seenState(StateReconnecting) {
		t.Errorf("отказ dns не увёл автомат в переподключение:\n%s", rec.dump())
	}
}

// Без ProbeDNS шага dns в выводе нет вовсе: печатать «готов» о том, что не
// проверялось, нельзя.
func TestNoDNSStageWithoutProbe(t *testing.T) {
	dp := newFakeDatapath()
	rec := &recorder{}

	tun, _ := newTestTunnel(t, Options{
		Config:      core.Config{Peer: "1.2.3.4:9000", Password: "пароль"},
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return dp, nil },
		OnEvent:     rec.onEvent,
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		connectTo(ctx, sink)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()
	<-dp.started
	cancel()
	_ = waitErr(t, done)

	if got := rec.stageStatus(core.StageDNS); len(got) != 0 {
		t.Errorf("шаг dns отправлен без проверки: %v", got)
	}
}
