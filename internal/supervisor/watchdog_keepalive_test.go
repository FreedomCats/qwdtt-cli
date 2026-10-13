package supervisor

import (
	"testing"
	"time"
)

// feed подаёт статистику с постоянными счётчиками каждые statsInterval,
// начиная со следующего тика после from, и проверяет претензию на каждом:
// до истечения окна с момента debtAt её быть не должно, после — ожидается want.
// Нулевое debtAt означает «долга нет, претензий не ждём вовсе».
func feed(t *testing.T, w *Watchdog, from, debtAt time.Time, ticks int, up, down int64, want StallReason) time.Time {
	t.Helper()
	at := from
	for range ticks {
		at = at.Add(statsInterval)
		w.Observe(stats(9, up, down), at)

		expect := StallNone
		if !debtAt.IsZero() && at.Sub(debtAt) > DefaultStallWindow {
			expect = want
		}
		if reason := w.Stall(at); reason != expect {
			t.Fatalf("на %v претензия %q, ожидалась %q", at.Sub(base), reason, expect)
		}
	}
	return at
}

// Keepalive WireGuard — 32 байта раз в 25 секунд без ответа — не отправка,
// а шум протокола: простаивающий туннель с ним здоров. На живой проверке G2
// без этого правила туннель перезапускался каждые 50 секунд.
func TestWatchdogKeepaliveIsNotSilence(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)

	// Пять минут простоя: keepalive каждые 25 с, ответов нет вовсе — даже
	// рукопожатие раз в две минуты здесь не считаем, чтобы правило держалось
	// само по себе.
	var up int64 = 180
	w.Observe(stats(9, up, 92), base)
	at := base
	for tick := 1; tick <= 100; tick++ {
		at = base.Add(time.Duration(tick) * statsInterval)
		if tick%8 == 0 {
			up += 32
		}
		w.Observe(stats(9, up, 92), at)
		if reason := w.Stall(at); reason != StallNone {
			t.Fatalf("на %v претензия %q к простаивающему туннелю", at.Sub(base), reason)
		}
	}

	// Настоящая отправка — килобайт без ответа — открывает долг с этого тика,
	// и через окно терпения это деградация.
	debtAt := at.Add(statsInterval)
	up += DefaultStallBytes
	w.Observe(stats(9, up, 92), debtAt)
	feed(t, w, debtAt, debtAt, 12, up, 92, StallNoDownlink)
}

// Мелкая отправка копится: три тика по 400 байт без ответа — уже запрос,
// а не keepalive, и отсчёт идёт с тика, на котором порог перейдён.
func TestWatchdogSmallSendsAddUp(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)
	next := pump(w, base, 400, 0)

	next()            // база
	next()            // 400
	next()            // 800
	crossed := next() // 1200 — порог перейдён здесь
	last := feed(t, w, crossed, crossed, 12, 1200, 0, StallNoDownlink)

	// Ответ обнуляет и долг, и накопленные байты: следующие 400 — снова шум.
	answered := last.Add(statsInterval)
	w.Observe(stats(9, 1200, 64), answered)
	if reason := w.Stall(answered); reason != StallNone {
		t.Fatalf("после ответа претензия %q, ожидалось пусто", reason)
	}
	feed(t, w, answered, time.Time{}, 20, 1600, 64, StallNone)
}
