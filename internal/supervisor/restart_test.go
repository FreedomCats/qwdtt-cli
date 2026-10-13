package supervisor

import (
	"errors"
	"sync"
	"testing"
	"time"

	"qwdtt-cli/internal/core"
)

// base — начало отсчёта в тестах. Watchdog бесчасовой, время ему передают,
// поэтому ни один тест здесь не спит.
var base = time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)

// hashes — три хеша звонка, как в профиле с тремя ссылками.
func hashes() []string {
	return []string{"aaaaaaaaaaaa1", "bbbbbbbbbbbb2", "cccccccccccc3"}
}

func TestBackoffSequence(t *testing.T) {
	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, 64 * time.Second,
		64 * time.Second, 64 * time.Second, 64 * time.Second,
	}
	for attempt, delay := range want {
		if got := Backoff(attempt); got != delay {
			t.Errorf("Backoff(%d) = %v, ожидалось %v", attempt, got, delay)
		}
	}
	// Счётчик попыток ниже нуля не бывает, но задержка обязана остаться
	// осмысленной: ноль означал бы перезапуск без паузы.
	if got := Backoff(-1); got != time.Second {
		t.Errorf("Backoff(-1) = %v, ожидалось %v", got, time.Second)
	}
}

// Последовательность задержек, которую выдаёт сам Restarter, плюс сброс
// счётчика поднявшимся туннелем.
func TestRestarterDelaySequence(t *testing.T) {
	m := New()
	r := NewRestarter(m, NewHashPool(hashes()))

	want := []time.Duration{
		1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, 64 * time.Second, 64 * time.Second,
	}
	for i, delay := range want {
		m.BeginAttempt()
		d := r.Failed(errors.New("туннель не держится"))
		if d.Stop {
			t.Fatalf("попытка %d: Stop при трёх годных хешах", i+1)
		}
		if d.Delay != delay {
			t.Errorf("попытка %d: задержка %v, ожидалась %v", i+1, d.Delay, delay)
		}
		if d.Attempt != i+1 {
			t.Errorf("номер попытки %d, ожидался %d", d.Attempt, i+1)
		}
		if m.State() != StateReconnecting {
			t.Fatalf("состояние %q, ожидалось %q", m.State(), StateReconnecting)
		}
	}

	// Туннель, проработавший час, должен переподключаться через секунду.
	m.BeginAttempt()
	observe(m, successEvents()...)
	r.Connected()
	if r.Attempts() != 0 {
		t.Errorf("после Connected попыток %d, ожидалось 0", r.Attempts())
	}
	if d := r.Failed(errors.New("сеть отвалилась")); d.Delay != time.Second {
		t.Errorf("задержка после сброса %v, ожидалась %v", d.Delay, time.Second)
	}
}

// Отказ одного хеша из трёх туннель не останавливает.
func TestDeadHashDoesNotStopTunnel(t *testing.T) {
	pool := NewHashPool(hashes())
	m := New()
	r := NewRestarter(m, pool)

	m.BeginAttempt()
	observe(m, successEvents()...)

	dead := core.EventHashFailed{
		Hash:   hashes()[1],
		Group:  2,
		Status: core.HashCheckDead,
		Msg:    "Звонок не найден или закрыт",
	}

	// Автомат о хешах не знает: туннель держат остальные группы.
	if tr, ok := m.Observe(dead); ok {
		t.Fatalf("отказ хеша сдвинул состояние: %+v", tr)
	}
	if m.State() != StateConnected {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateConnected)
	}

	if !pool.Observe(dead) {
		t.Fatal("пул не исключил мёртвый хеш")
	}
	if got := len(pool.Alive()); got != 2 {
		t.Errorf("годных хешей %d, ожидалось 2", got)
	}
	if pool.Exhausted() {
		t.Error("пул объявлен исчерпанным при двух годных хешах")
	}
	if got := pool.DeadReason(hashes()[1]); got != dead.Msg {
		t.Errorf("причина %q, ожидалась %q", got, dead.Msg)
	}
	// Исключённый хеш в следующую попытку не попадает, остальные — в прежнем порядке.
	want := []string{hashes()[0], hashes()[2]}
	if alive := pool.Alive(); len(alive) != 2 || alive[0] != want[0] || alive[1] != want[1] {
		t.Errorf("Alive() = %v, ожидалось %v", alive, want)
	}

	// Когда туннель всё же упадёт, перезапуск обычный — не остановка.
	if d := r.Failed(errors.New("все воркеры завершились сами")); d.Stop {
		t.Error("Stop при двух годных хешах")
	}
	if m.State() != StateReconnecting {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateReconnecting)
	}

	// Повторный отказ того же хеша ничего не меняет.
	if pool.Observe(dead) {
		t.Error("повторное исключение того же хеша засчитано")
	}
}

// Остановка — только когда годных хешей не осталось.
func TestAllHashesDeadStops(t *testing.T) {
	pool := NewHashPool(hashes())
	m := New()
	r := NewRestarter(m, pool)

	m.BeginAttempt()
	for i, hash := range hashes() {
		pool.Observe(core.EventHashFailed{
			Hash: hash, Group: i + 1, Status: core.HashCheckDead, Msg: "закрыт",
		})
	}
	if !pool.Exhausted() {
		t.Fatal("пул не исчерпан после трёх мёртвых хешей")
	}

	d := r.Failed(errors.New("все воркеры завершились сами"))
	if !d.Stop {
		t.Error("Stop не назначен при нуле годных хешей")
	}
	if d.Delay != 0 {
		t.Errorf("задержка %v, ожидался ноль: ждать нечего", d.Delay)
	}
	if m.State() != StateStopping {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateStopping)
	}
}

// Временные отказы хеш не исключают: иначе одна неурядица со связью срезала бы
// все хеши разом и остановила клиент.
func TestTemporaryFailuresKeepHash(t *testing.T) {
	for _, status := range []core.HashCheckStatus{
		core.HashCheckCaptcha, core.HashCheckLimited,
		core.HashCheckNetwork, core.HashCheckError,
	} {
		t.Run(string(status), func(t *testing.T) {
			pool := NewHashPool(hashes())
			if pool.Observe(core.EventHashFailed{Hash: hashes()[0], Status: status}) {
				t.Error("хеш исключён по временному отказу")
			}
			if got := len(pool.Alive()); got != 3 {
				t.Errorf("годных хешей %d, ожидалось 3", got)
			}
		})
	}
}

func TestHashPoolIgnoresUnknownAndOtherEvents(t *testing.T) {
	pool := NewHashPool(hashes())

	if pool.Observe(core.EventHashFailed{Hash: "чужой", Status: core.HashCheckDead}) {
		t.Error("исключён хеш, которого в пуле нет")
	}
	if pool.MarkDead("чужой", "закрыт") {
		t.Error("MarkDead согласился на чужой хеш")
	}
	for _, ev := range []core.Event{
		core.EventStats{ActiveWorkers: 9},
		core.EventStage{Stage: core.StageVK, Status: core.StageOK},
		core.EventHashCheck{Hash: hashes()[0], Status: core.HashCheckDead},
	} {
		if pool.Observe(ev) {
			t.Errorf("%T исключило хеш", ev)
		}
	}
	if got := len(pool.Alive()); got != 3 {
		t.Errorf("годных хешей %d, ожидалось 3", got)
	}
}

// Подключаться без хешей не к чему — пустой пул исчерпан сразу.
func TestEmptyHashPoolExhausted(t *testing.T) {
	if !NewHashPool(nil).Exhausted() {
		t.Error("пустой пул не считается исчерпанным")
	}
}

func TestShortHash(t *testing.T) {
	if got := ShortHash("0123456789abcdef"); got != "01234567…" {
		t.Errorf("ShortHash = %q, ожидалось %q", got, "01234567…")
	}
	if got := ShortHash(" abc "); got != "abc" {
		t.Errorf("ShortHash = %q, ожидалось %q", got, "abc")
	}
}

// stats — статистика от ядра: воркеры живы, счётчики нарастающие.
func stats(workers int32, up, down int64) core.EventStats {
	return core.EventStats{ActiveWorkers: workers, BytesUp: up, BytesDown: down}
}

// statsInterval — с какой частотой ядро присылает счётчики (stats.go, RunLoop).
// Тесты подают статистику так же: иначе претензия «ядро не присылает
// статистику» срабатывала бы раньше всех остальных.
const statsInterval = 3 * time.Second

// pump возвращает функцию, подающую очередной тик статистики и отдающую его
// время. Счётчики нарастают на заданный шаг; первый тик идёт с нулями — это
// база отсчёта, с которой сравнивается рост.
func pump(w *Watchdog, from time.Time, upStep, downStep int64) func() time.Time {
	var up, down int64
	tick := 0
	return func() time.Time {
		at := from.Add(time.Duration(tick) * statsInterval)
		w.Observe(stats(9, up, down), at)
		tick++
		up += upStep
		down += downStep
		return at
	}
}

// Простаивающий туннель здоров: не отправляли — нечего и ждать.
func TestWatchdogIdleTunnelIsHealthy(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)
	next := pump(w, base, 0, 0)

	for tick := range 40 {
		at := next()
		if reason := w.Stall(at); reason != StallNone {
			t.Fatalf("тик %d: претензия %q к простаивающему туннелю", tick, reason)
		}
	}
}

// Отправляем, ответа нет: через окно терпения это деградация.
func TestWatchdogNoDownlink(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)
	next := pump(w, base, 1500, 0)

	// Первый тик — база отсчёта: счётчики нарастающие, и свои начальные
	// значения за отправку считать нельзя. Долг открывает второй тик.
	last := next()
	debtAt := base.Add(statsInterval)

	for range 12 {
		at := next()
		last = at

		// Непрерывная отправка отсчёт не сбрасывает: ждём ответа с того
		// момента, как задолжали, а не с последнего отправленного байта.
		want := StallNone
		if at.Sub(debtAt) > DefaultStallWindow {
			want = StallNoDownlink
		}
		if reason := w.Stall(at); reason != want {
			t.Errorf("на %v претензия %q, ожидалась %q", at.Sub(base), reason, want)
		}
	}

	// Ответ закрывает долг.
	answered := last.Add(statsInterval)
	w.Observe(stats(9, 1<<20, 64), answered)
	if reason := w.Stall(answered); reason != StallNone {
		t.Errorf("после ответа претензия %q, ожидалось пусто", reason)
	}
}

// Обмен идёт в обе стороны — претензий нет.
func TestWatchdogHealthyExchange(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)
	next := pump(w, base, 2048, 16384)

	for tick := range 20 {
		at := next()
		if reason := w.Stall(at); reason != StallNone {
			t.Fatalf("тик %d: претензия %q к работающему туннелю", tick, reason)
		}
	}
}

// Ядро молчит дольше окна: статистика идёт раз в три секунды, и её пропажа
// означает, что ядро застряло.
func TestWatchdogNoStats(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)

	// До первой статистики судить не о чем: подключение ещё идёт.
	if reason := w.Stall(base.Add(time.Hour)); reason != StallNone {
		t.Errorf("до первой статистики претензия %q", reason)
	}

	w.Observe(stats(9, 1024, 4096), base)
	if reason := w.Stall(base.Add(DefaultStallWindow)); reason != StallNone {
		t.Errorf("в пределах окна претензия %q", reason)
	}
	if reason := w.Stall(base.Add(DefaultStallWindow + time.Second)); reason != StallNoStats {
		t.Errorf("претензия %q, ожидалась %q", reason, StallNoStats)
	}
}

// Воркеров не осталось — это отказ, а не деградация: ядро вернётся из Run само.
func TestWatchdogWithoutWorkers(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)

	at := base
	w.Observe(stats(9, 0, 0), at)
	for tick := 1; tick <= 12; tick++ {
		at = base.Add(time.Duration(tick) * 3 * time.Second)
		w.Observe(stats(0, int64(tick)*1500, 0), at)
	}
	if reason := w.Stall(at); reason != StallNone {
		t.Errorf("претензия %q, ожидалось пусто: воркеров нет", reason)
	}
}

// Счётчики прошлой попытки не должны выглядеть молчанием этой.
func TestWatchdogReset(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)
	next := pump(w, base, 1500, 0)

	var stalled time.Time
	for range 12 {
		stalled = next()
	}
	if reason := w.Stall(stalled); reason != StallNoDownlink {
		t.Fatalf("претензия %q, ожидалась %q", reason, StallNoDownlink)
	}

	w.Reset()
	if reason := w.Stall(stalled); reason != StallNone {
		t.Errorf("после Reset претензия %q, ожидалось пусто", reason)
	}
}

func TestWatchdogIgnoresOtherEvents(t *testing.T) {
	w := NewWatchdog(DefaultStallWindow)
	w.Observe(core.EventStage{Stage: core.StageDone, Status: core.StageOK}, base)
	w.Observe(core.EventLog{Level: core.LevelInfo, Msg: "поехали"}, base)

	if reason := w.Stall(base.Add(time.Hour)); reason != StallNone {
		t.Errorf("претензия %q, ожидалось пусто: статистики не было", reason)
	}
}

// Нулевое окно означает значение по умолчанию: watchdog с нулевым терпением
// объявлял бы сломанным любой туннель.
func TestWatchdogDefaultWindow(t *testing.T) {
	w := NewWatchdog(0)
	next := pump(w, base, 1500, 0)
	debtAt := base.Add(statsInterval)

	var at time.Time
	for at.Sub(debtAt) <= DefaultStallWindow {
		at = next()
		want := StallNone
		if at.Sub(debtAt) > DefaultStallWindow {
			want = StallNoDownlink
		}
		if reason := w.Stall(at); reason != want {
			t.Fatalf("на %v претензия %q, ожидалась %q", at.Sub(base), reason, want)
		}
	}
}

// Деградировавший туннель доходит до перезапуска: watchdog → Degrade →
// Failed → задержка → новая попытка.
func TestDegradedTunnelRestarts(t *testing.T) {
	m := New()
	pool := NewHashPool(hashes())
	r := NewRestarter(m, pool)
	w := NewWatchdog(DefaultStallWindow)

	m.BeginAttempt()
	observe(m, successEvents()...)
	r.Connected()

	next := pump(w, base, 9000, 0)
	var stalledAt time.Time
	for range 12 {
		stalledAt = next()
	}
	reason := w.Stall(stalledAt)
	if reason != StallNoDownlink {
		t.Fatalf("претензия %q, ожидалась %q", reason, StallNoDownlink)
	}

	tr, ok := m.Degrade()
	if !ok || tr.To != StateDegraded {
		t.Fatalf("Degrade дал %+v, ok = %v", tr, ok)
	}

	d := r.Failed(errors.New(string(reason)))
	if d.Stop || d.Delay != time.Second || d.Attempt != 1 {
		t.Errorf("решение %+v, ожидались пауза в секунду и первая попытка", d)
	}
	if m.State() != StateReconnecting {
		t.Fatalf("состояние %q, ожидалось %q", m.State(), StateReconnecting)
	}

	w.Reset()
	if next, ok := m.BeginAttempt(); !ok || next.To != StateResolving {
		t.Fatalf("BeginAttempt дал %+v, ok = %v", next, ok)
	}
}

// Пул и watchdog получают события из горутин воркеров, решения приходят из
// горутины супервизора. Проверяется под -race.
func TestRestartPolicyIsConcurrencySafe(t *testing.T) {
	pool := NewHashPool(hashes())
	w := NewWatchdog(DefaultStallWindow)
	r := NewRestarter(New(), pool)

	var wg sync.WaitGroup
	for group, hash := range hashes() {
		wg.Add(1)
		go func(group int, hash string) {
			defer wg.Done()
			for tick := range 20 {
				at := base.Add(time.Duration(tick) * 3 * time.Second)
				w.Observe(stats(9, int64(tick)*1024, int64(tick)*2048), at)
				pool.Observe(core.EventHashFailed{
					Hash: hash, Group: group + 1, Status: core.HashCheckDead, Msg: "закрыт",
				})
			}
		}(group, hash)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			w.Stall(base.Add(time.Minute))
			pool.Alive()
			r.Attempts()
		}
	}()
	wg.Wait()

	if !pool.Exhausted() {
		t.Error("пул не исчерпан, хотя все три хеша объявлены мёртвыми")
	}
}
