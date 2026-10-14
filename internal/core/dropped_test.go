package core

import (
	"context"
	"net"
	"testing"
	"time"
)

// Очереди всех воркеров полны — диспетчер выбрасывает пакет от WireGuard и
// считает это в DroppedUp. В апстриме сброс в десктопном режиме был молчаливым.
func TestDispatcherCountsDroppedUp(t *testing.T) {
	local, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	stats := NewStats()
	d := NewDispatcher(context.Background(), local, stats)
	// readLoop стоит в ReadFrom, отмена контекста его не будит: сначала
	// закрыть сокет, потом ждать горутины.
	defer d.Shutdown()
	defer local.Close()

	// Воркер с забитыми очередями: ни SendCh, ни PrioCh пакет не примут.
	slot := &WorkerSlot{ID: 1, SendCh: make(chan []byte, 1), PrioCh: make(chan []byte, 1)}
	slot.SendCh <- []byte{0}
	slot.PrioCh <- []byte{0}
	d.Register(slot)

	wg, err := net.Dial("udp", local.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer wg.Close()

	const sent = 5
	big := make([]byte, prioThreshold+100)
	for range sent {
		if _, err := wg.Write(big); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for stats.DroppedUp.Load() < sent && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := stats.DroppedUp.Load(); got != sent {
		t.Errorf("DroppedUp = %d, ожидалось %d", got, sent)
	}
	if got := stats.DroppedDown.Load(); got != 0 {
		t.Errorf("DroppedDown = %d, ожидался 0", got)
	}
}
