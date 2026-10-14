package core

import (
	"context"
	"testing"
	"time"
)

// Группа, не получившая креды, передаёт эстафету следующей. В апстриме
// signalReady закрывался только после удачных кредов, и при мёртвом хеше первой
// группы остальные ждали её вечно.
func TestWorkerGroupPassesBatonOnCredsFailure(t *testing.T) {
	rec := install(t)

	// Отменённый контекст: креды не получить, и сеть не нужна.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ready := make(chan struct{})
	var paused int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		WorkerGroup(ctx, 1, 0, &TurnParams{Hashes: []string{"a1b2c3d4e5f6"}}, nil, nil, "9000",
			true, nil, []int{1}, &paused, "device", "password", NewStats(), nil, ready)
	}()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("WorkerGroup не вернулась")
	}
	select {
	case <-ready:
	default:
		t.Fatal("эстафета не передана: следующая группа ждала бы вечно")
	}

	var failed bool
	for _, ev := range rec.all() {
		if _, ok := ev.(EventHashFailed); ok {
			failed = true
		}
	}
	if !failed {
		t.Error("отказ хеша не отправлен")
	}
}
