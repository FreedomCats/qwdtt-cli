package supervisor

import (
	"testing"
	"time"

	"qwdtt-cli/internal/core"
)

// ready — шаг «туннель готов», который супервизор отправляет после подъёма
// datapath. От него watchdog отсчитывает StallNoUplink.
func ready(w *Watchdog, at time.Time) {
	w.Observe(core.EventStage{Stage: core.StageDone, Status: core.StageOK}, at)
}

// Datapath поднят, а в ядро от него не пришло ни байта: так выглядела служба
// штатного WireGuard, упавшая сразу после установки (живая проверка H2).
// Через окно терпения это претензия, а не «простаивающий туннель».
func TestWatchdogNoUplinkAfterReady(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)
	next := pump(w, base, 0, 0)

	ready(w, next())
	var at time.Time
	for range 8 { // 24 секунды — ещё в пределах окна
		at = next()
		if reason := w.Stall(at); reason != StallNone {
			t.Fatalf("претензия %q раньше окна терпения", reason)
		}
	}
	at = next() // 27 секунд
	if reason := w.Stall(at); reason != StallNoUplink {
		t.Errorf("претензия %q, ожидалась %q", reason, StallNoUplink)
	}
}

// Здоровый простой: рукопожатие сразу после подъёма, дальше keepalive.
// Это не молчание datapath.
func TestWatchdogHandshakeIsUplink(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)

	w.Observe(stats(9, 0, 0), base)
	ready(w, base)
	w.Observe(stats(9, 148, 92), base.Add(statsInterval))
	for tick := 2; tick <= 40; tick++ {
		at := base.Add(time.Duration(tick) * statsInterval)
		w.Observe(stats(9, 148, 92), at)
		if reason := w.Stall(at); reason != StallNone {
			t.Fatalf("тик %d: претензия %q к туннелю после рукопожатия", tick, reason)
		}
	}
}

// До подъёма datapath восходящего трафика нет законно: ядро может минутами
// ждать креды или капчу. Отсчёт идёт от готовности, а не от первой статистики.
func TestWatchdogUplinkCountsFromReady(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)

	// Минута статистики с нулями до подъёма datapath.
	var at time.Time
	for tick := range 20 {
		at = base.Add(time.Duration(tick) * statsInterval)
		w.Observe(stats(9, 0, 0), at)
		if reason := w.Stall(at); reason != StallNone {
			t.Fatalf("тик %d: претензия %q до подъёма datapath", tick, reason)
		}
	}

	ready(w, at)
	at = at.Add(statsInterval)
	w.Observe(stats(9, 148, 0), at)
	if reason := w.Stall(at); reason != StallNone {
		t.Errorf("претензия %q сразу после подъёма datapath", reason)
	}
}

// Байты, отправленные до готовности, не засчитываются за трафик datapath:
// важен рост после шага «туннель готов».
func TestWatchdogUplinkBeforeReadyDoesNotCount(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)

	w.Observe(stats(9, 500, 0), base)
	ready(w, base)
	var at time.Time
	for tick := 1; tick <= 10; tick++ {
		at = base.Add(time.Duration(tick) * statsInterval)
		w.Observe(stats(9, 500, 0), at)
	}
	if reason := w.Stall(at); reason != StallNoUplink {
		t.Errorf("претензия %q, ожидалась %q", reason, StallNoUplink)
	}
}

// Reset забывает и готовность: новая попытка ждёт свой шаг «туннель готов».
func TestWatchdogResetForgetsReady(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)
	w.Observe(stats(9, 0, 0), base)
	ready(w, base)
	w.Reset()

	var at time.Time
	for tick := range 20 {
		at = base.Add(time.Duration(tick) * statsInterval)
		w.Observe(stats(9, 0, 0), at)
	}
	if reason := w.Stall(at); reason != StallNone {
		t.Errorf("претензия %q после Reset без нового шага готовности", reason)
	}
}
