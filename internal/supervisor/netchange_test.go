package supervisor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

var errNetChanged = errors.New("сеть сменилась")

// Смена сети посреди работающей попытки: попытка обрывается с причиной
// «сеть сменилась», маршруты снимаются, следующая попытка идёт без задержки
// на том же долгоживущем datapath, и маршруты возвращаются.
func TestRestartEndsAttemptWithoutDelay(t *testing.T) {
	dp := fakePersistent{&fakeFullTunnel{fakeDatapath: newFakeDatapath()}}
	lines := &lineRecorder{}
	rec := &recorder{}
	var causesMu sync.Mutex
	var causes []error

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempts atomic.Int32
	tun, delays := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return dp, nil },
		OnTransition: func(tr Transition) {
			rec.onTransition(tr)
			if tr.To == StateReconnecting {
				causesMu.Lock()
				causes = append(causes, tr.Cause)
				causesMu.Unlock()
			}
		},
		Logf: lines.logf,
	}, func(actx context.Context, cfg core.Config, sink core.Sink) error {
		connectTo(actx, sink)
		if attempts.Add(1) == 2 {
			cancel()
		}
		<-actx.Done()
		return nil
	})

	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()

	waitFor(t, func() bool { return tun.State() == StateConnected }, rec.dump)
	tun.Restart(errNetChanged)

	if err := waitErr(t, done); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if n := attempts.Load(); n != 2 {
		t.Fatalf("попыток %d, ждали 2", n)
	}
	if len(*delays) != 1 || (*delays)[0] != 0 {
		t.Errorf("задержки %v, ждали одну нулевую", *delays)
	}
	if got := dp.suspendCount(); got != 1 {
		t.Errorf("Suspend %d раз, ждали 1 — маршруты снимаются и при смене сети", got)
	}
	if starts, closes := dp.counts(); starts != 2 || closes != 1 {
		t.Errorf("datapath: %d Start, %d Close; ждали 2 и 1", starts, closes)
	}
	if lines.count(lineBypassed) != 1 || lines.count(lineReturned) != 1 {
		t.Errorf("строки маршрутов:\n%s", lines.dump())
	}
	causesMu.Lock()
	defer causesMu.Unlock()
	if len(causes) != 1 || !errors.Is(causes[0], errNetChanged) {
		t.Errorf("причины переподключения %v, ждали одну «сеть сменилась»", causes)
	}
}

// Смена сети, пока цикл ждёт задержку после череды неудач: ожидание
// прерывается, следующая попытка идёт сразу, а счётчик задержек обнулён —
// неудачи старой сети новой не в счёт.
func TestRestartWakesPauseAndResetsBackoff(t *testing.T) {
	tun, _ := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
	}, func(context.Context, core.Config, core.Sink) error {
		return errors.New("нет сети")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var delays []time.Duration
	asleep := make(chan struct{})
	tun.sleep = func(sctx context.Context, d time.Duration) bool {
		mu.Lock()
		delays = append(delays, d)
		n := len(delays)
		mu.Unlock()
		switch n {
		case 4:
			// Задержка 8 с: ждём по-настоящему, пока не разбудят.
			close(asleep)
			<-sctx.Done()
			return false
		case 5:
			cancel()
			return false
		}
		return true
	}

	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()

	select {
	case <-asleep:
	case <-time.After(5 * time.Second):
		t.Fatal("не дошли до задержки")
	}
	tun.Restart(errNetChanged)

	if err := waitErr(t, done); err != nil {
		t.Fatalf("Run = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, time.Second}
	if len(delays) != len(want) {
		t.Fatalf("задержки %v, ждали %v", delays, want)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("задержки %v, ждали %v", delays, want)
		}
	}
}

// Попытка кончилась своим отказом, но смену сети уже попросили: решение —
// перезапуск без задержки, а не задержка по отказу.
func TestRestartWinsOverAttemptError(t *testing.T) {
	var tun *Tunnel
	var attempts atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tun, delays := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
	}, func(context.Context, core.Config, core.Sink) error {
		if attempts.Add(1) == 1 {
			tun.Restart(errNetChanged)
			return errors.New("TURN-relay закрыл соединение")
		}
		cancel()
		return errors.New("остановка")
	})

	if err := tun.Run(ctx); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if len(*delays) != 1 || (*delays)[0] != 0 {
		t.Errorf("задержки %v, ждали одну нулевую", *delays)
	}
	if n := tun.Attempts(); n != 0 {
		t.Errorf("счётчик неудач %d после смены сети, ждали 0", n)
	}
}

// Просьба, пришедшая после конца попытки, но до задержки: задержки нет
// вовсе, счётчик обнулён.
func TestRestartBeforePauseSkipsIt(t *testing.T) {
	tun, delays := newTestTunnel(t, Options{
		NewDatapath: func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
	}, nil)
	tun.restarter.Failed(errors.New("нет сети"))
	tun.restarter.Failed(errors.New("нет сети"))

	tun.Restart(errNetChanged)
	if !tun.pause(context.Background(), 4*time.Second) {
		t.Fatal("pause = false без остановки")
	}
	if len(*delays) != 0 {
		t.Errorf("ждали задержку %v, хотя перезапуск уже попросили", *delays)
	}
	if n := tun.Attempts(); n != 0 {
		t.Errorf("счётчик неудач %d, ждали 0", n)
	}
}

// Просьба до Run исполняется первой попыткой и следующую не торопит; после
// Run Restart ничего не ломает.
func TestRestartOutsideRun(t *testing.T) {
	tun, _ := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
	}, func(context.Context, core.Config, core.Sink) error {
		return errors.New("нет сети")
	})
	tun.Restart(errNetChanged)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var delays []time.Duration
	tun.sleep = func(_ context.Context, d time.Duration) bool {
		delays = append(delays, d)
		cancel()
		return false
	}
	if err := tun.Run(ctx); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if len(delays) != 1 || delays[0] != time.Second {
		t.Errorf("задержки %v, ждали обычную секунду после отказа", delays)
	}
	tun.Restart(errNetChanged)
}
