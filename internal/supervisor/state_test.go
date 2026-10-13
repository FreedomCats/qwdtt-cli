package supervisor

import (
	"errors"
	"sync"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// successEvents — события удачного подключения в том порядке, в котором их
// отправляют ядро и супервизор. Порядок взят с живого ядра, а не из схемы:
// wrap закрывается первым, капча приходит раньше «кредов VK», а vpn/socks и
// done отправляет сам супервизор после того, как datapath поднялся.
func successEvents() []core.EventStage {
	return []core.EventStage{
		{Stage: core.StageWrap, Status: core.StageOK},
		{Stage: core.StageDNS, Status: core.StageOK},
		{Stage: core.StageCaptcha, Status: core.StageRunning, Session: 100},
		{Stage: core.StageCaptcha, Status: core.StageOK, Session: 100},
		{Stage: core.StageVK, Status: core.StageOK},
		{Stage: core.StageTURN, Status: core.StageOK, Session: 1},
		{Stage: core.StageDTLS, Status: core.StageRunning, Session: 1},
		{Stage: core.StageDTLS, Status: core.StageOK, Session: 1},
		{Stage: core.StageWorkers, Status: core.StageOK, Session: 1},
		{Stage: core.StageSocks, Status: core.StageOK},
		{Stage: core.StageDone, Status: core.StageOK},
	}
}

// observe прогоняет события и собирает состоявшиеся переходы.
func observe(m *Machine, evs ...core.EventStage) []Transition {
	var got []Transition
	for _, ev := range evs {
		if tr, ok := m.Observe(ev); ok {
			got = append(got, tr)
		}
	}
	return got
}

// states вытаскивает из переходов цепочку состояний: From первого и To каждого.
func states(trs []Transition) []State {
	if len(trs) == 0 {
		return nil
	}
	out := []State{trs[0].From}
	for _, tr := range trs {
		out = append(out, tr.To)
	}
	return out
}

func equalStates(a, b []State) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestFullCycle(t *testing.T) {
	m := New()

	begin, ok := m.BeginAttempt()
	if !ok {
		t.Fatal("BeginAttempt не дал перехода")
	}
	trs := append([]Transition{begin}, observe(m, successEvents()...)...)

	want := []State{
		StateIdle, StateResolving, StateCaptcha, StateVK,
		StateTURN, StateHandshake, StateConfiguring, StateConnected,
	}
	if got := states(trs); !equalStates(got, want) {
		t.Errorf("цепочка состояний %v, ожидалась %v", got, want)
	}
	if m.State() != StateConnected {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateConnected)
	}
	if m.FailedStage() != "" {
		t.Errorf("FailedStage = %q, ожидалось пусто", m.FailedStage())
	}
	for _, ev := range successEvents() {
		if ev.Status != core.StageOK {
			continue
		}
		if got := m.StageStatus(ev.Stage); got != core.StageOK {
			t.Errorf("шаг %q в состоянии %q, ожидалось %q", ev.Stage, got, core.StageOK)
		}
	}

	// Остановка доводит цикл до конца и дальше не пускает.
	stop, ok := m.Stop()
	if !ok || stop.From != StateConnected || stop.To != StateStopping {
		t.Fatalf("Stop дал %+v, ok = %v", stop, ok)
	}
}

// Подключение без капчи: шаг не приходит вовсе, линия идёт Resolving → VK.
func TestCycleWithoutCaptcha(t *testing.T) {
	m := New()
	m.BeginAttempt()

	var evs []core.EventStage
	for _, ev := range successEvents() {
		if ev.Stage == core.StageCaptcha {
			continue
		}
		evs = append(evs, ev)
	}
	trs := observe(m, evs...)

	want := []State{
		StateResolving, StateVK, StateTURN,
		StateHandshake, StateConfiguring, StateConnected,
	}
	if got := states(trs); !equalStates(got, want) {
		t.Errorf("цепочка состояний %v, ожидалась %v", got, want)
	}
	if got := m.StageStatus(core.StageCaptcha); got != "" {
		t.Errorf("StageStatus(captcha) = %q, ожидалось пусто", got)
	}
}

// Отказ на каждом шаге: до возврата из core.Run состояние не меняется, отказ
// виден в таблице шагов, а уводит с линии Reconnect.
func TestFailureAtEveryStage(t *testing.T) {
	for _, stage := range []core.Stage{
		core.StageWrap, core.StageDNS, core.StageCaptcha, core.StageVK,
		core.StageTURN, core.StageDTLS, core.StageWorkers, core.StageSocks,
		core.StageDone,
	} {
		t.Run(string(stage), func(t *testing.T) {
			m := New()
			m.BeginAttempt()

			// Всё, что успело пройти до отказавшего шага.
			var prefix []core.EventStage
			for _, ev := range successEvents() {
				if ev.Stage == stage {
					break
				}
				prefix = append(prefix, ev)
			}
			observe(m, prefix...)

			reached := m.State()
			if tr, ok := m.Observe(core.EventStage{
				Stage: stage, Status: core.StageFailed, Session: 1, Msg: "таймаут",
			}); ok {
				t.Fatalf("отказ шага сдвинул состояние: %+v", tr)
			}
			if m.State() != reached {
				t.Errorf("состояние %q, ожидалось %q", m.State(), reached)
			}
			if got := m.StageStatus(stage); got != core.StageFailed {
				t.Errorf("StageStatus(%q) = %q, ожидалось %q", stage, got, core.StageFailed)
			}
			if m.FailedStage() != stage {
				t.Errorf("FailedStage = %q, ожидалось %q", m.FailedStage(), stage)
			}

			cause := errors.New("ядро остановилось")
			tr, ok := m.Reconnect(cause)
			if !ok {
				t.Fatal("Reconnect не дал перехода")
			}
			if tr.From != reached || tr.To != StateReconnecting {
				t.Errorf("переход %q → %q, ожидался %q → %q", tr.From, tr.To, reached, StateReconnecting)
			}
			if !errors.Is(tr.Cause, cause) || !errors.Is(m.LastError(), cause) {
				t.Errorf("причина %v / %v, ожидалась %v", tr.Cause, m.LastError(), cause)
			}

			// Новая попытка начинает линию заново и забывает прошлые шаги.
			next, ok := m.BeginAttempt()
			if !ok || next.To != StateResolving {
				t.Fatalf("BeginAttempt дал %+v, ok = %v", next, ok)
			}
			if got := m.StageStatus(stage); got != "" {
				t.Errorf("после новой попытки StageStatus(%q) = %q, ожидалось пусто", stage, got)
			}
			if m.FailedStage() != "" || m.LastError() != nil {
				t.Errorf("после новой попытки FailedStage = %q, LastError = %v", m.FailedStage(), m.LastError())
			}
		})
	}
}

// Шаг wrap своего состояния не имеет: ядро закрывает его первым, и линия
// не должна начинаться с середины.
func TestWrapDoesNotMoveState(t *testing.T) {
	m := New()
	if tr, ok := m.Observe(core.EventStage{Stage: core.StageWrap, Status: core.StageOK}); ok {
		t.Fatalf("wrap сдвинул состояние: %+v", tr)
	}
	if m.State() != StateIdle {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateIdle)
	}
	if got := m.StageStatus(core.StageWrap); got != core.StageOK {
		t.Errorf("StageStatus(wrap) = %q, ожидалось %q", got, core.StageOK)
	}
}

// Поднятый туннель не возвращается на ранние шаги: воркеров до 108, и каждый
// проходит TURN и DTLS сам.
func TestLineGoesForwardOnly(t *testing.T) {
	m := New()
	m.BeginAttempt()
	observe(m, successEvents()...)

	late := []core.EventStage{
		{Stage: core.StageDTLS, Status: core.StageRunning, Session: 7},
		{Stage: core.StageDTLS, Status: core.StageFailed, Session: 7, Msg: "таймаут"},
		{Stage: core.StageTURN, Status: core.StageOK, Session: 8},
		{Stage: core.StageCaptcha, Status: core.StageRunning, Session: 900},
	}
	if trs := observe(m, late...); len(trs) != 0 {
		t.Errorf("поздние события дали переходы: %+v", trs)
	}
	if m.State() != StateConnected {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateConnected)
	}
	if got := m.StageStatus(core.StageDTLS); got != core.StageFailed {
		t.Errorf("StageStatus(dtls) = %q, ожидалось %q", got, core.StageFailed)
	}
}

// Отказ шага перекрывается успехом: DTLS у седьмого воркера не сложился,
// у восьмого сложился — шаг пройден.
func TestFailedStageOverriddenByOK(t *testing.T) {
	m := New()
	m.BeginAttempt()
	observe(m,
		core.EventStage{Stage: core.StageDTLS, Status: core.StageRunning, Session: 7},
		core.EventStage{Stage: core.StageDTLS, Status: core.StageFailed, Session: 7},
	)
	if m.FailedStage() != core.StageDTLS {
		t.Fatalf("FailedStage = %q, ожидалось %q", m.FailedStage(), core.StageDTLS)
	}

	observe(m, core.EventStage{Stage: core.StageDTLS, Status: core.StageOK, Session: 8})
	if m.FailedStage() != "" {
		t.Errorf("FailedStage = %q, ожидалось пусто", m.FailedStage())
	}
	if got := m.StageStatus(core.StageDTLS); got != core.StageOK {
		t.Errorf("StageStatus(dtls) = %q, ожидалось %q", got, core.StageOK)
	}
}

func TestDegradeAndRecover(t *testing.T) {
	m := New()
	m.BeginAttempt()

	if tr, ok := m.Degrade(); ok {
		t.Fatalf("Degrade до подключения дал переход: %+v", tr)
	}

	observe(m, successEvents()...)

	tr, ok := m.Degrade()
	if !ok || tr.From != StateConnected || tr.To != StateDegraded {
		t.Fatalf("Degrade дал %+v, ok = %v", tr, ok)
	}
	if _, ok := m.Degrade(); ok {
		t.Error("повторный Degrade дал переход")
	}

	// Пока трафика нет, события шагов состояния не меняют, но в таблицу идут.
	if trs := observe(m, core.EventStage{Stage: core.StageDTLS, Status: core.StageFailed, Session: 9}); len(trs) != 0 {
		t.Errorf("в StateDegraded событие дало переход: %+v", trs)
	}
	if m.State() != StateDegraded {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateDegraded)
	}
	if got := m.StageStatus(core.StageDTLS); got != core.StageFailed {
		t.Errorf("StageStatus(dtls) = %q, ожидалось %q", got, core.StageFailed)
	}

	tr, ok = m.Recover()
	if !ok || tr.From != StateDegraded || tr.To != StateConnected {
		t.Fatalf("Recover дал %+v, ok = %v", tr, ok)
	}
	if _, ok := m.Recover(); ok {
		t.Error("Recover из StateConnected дал переход")
	}
}

// Из StateDegraded перезапуск возможен — это единственный путь, который рисует
// схема §07.
func TestReconnectFromDegraded(t *testing.T) {
	m := New()
	m.BeginAttempt()
	observe(m, successEvents()...)
	m.Degrade()

	tr, ok := m.Reconnect(nil)
	if !ok || tr.From != StateDegraded || tr.To != StateReconnecting {
		t.Fatalf("Reconnect дал %+v, ok = %v", tr, ok)
	}
	// Намеренный перезапуск — без причины: так приходит смена сети.
	if tr.Cause != nil || m.LastError() != nil {
		t.Errorf("причина %v / %v, ожидалось nil", tr.Cause, m.LastError())
	}
	if _, ok := m.Reconnect(errors.New("ещё раз")); ok {
		t.Error("повторный Reconnect дал переход")
	}
}

func TestReconnectFromIdleDoesNothing(t *testing.T) {
	m := New()
	if tr, ok := m.Reconnect(errors.New("нечего переподключать")); ok {
		t.Fatalf("Reconnect из StateIdle дал переход: %+v", tr)
	}
	if m.State() != StateIdle {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateIdle)
	}
	if m.LastError() != nil {
		t.Errorf("LastError = %v, ожидалось nil", m.LastError())
	}
}

// Попытка не начинается посреди уже идущей: иначе таблица шагов очистилась бы
// под поднятым туннелем.
func TestBeginAttemptOnlyFromIdleOrReconnecting(t *testing.T) {
	m := New()
	m.BeginAttempt()
	observe(m, successEvents()...)

	if tr, ok := m.BeginAttempt(); ok {
		t.Fatalf("BeginAttempt из StateConnected дал переход: %+v", tr)
	}
	if m.State() != StateConnected {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateConnected)
	}
}

// StateStopping конечное: после Ctrl+C картину не перерисовывает ничто.
func TestStopIsTerminal(t *testing.T) {
	m := New()
	tr, ok := m.Stop()
	if !ok || tr.From != StateIdle || tr.To != StateStopping {
		t.Fatalf("Stop из StateIdle дал %+v, ok = %v", tr, ok)
	}
	if _, ok := m.Stop(); ok {
		t.Error("повторный Stop дал переход")
	}

	if trs := observe(m, successEvents()...); len(trs) != 0 {
		t.Errorf("после Stop события дали переходы: %+v", trs)
	}
	if got := m.StageStatus(core.StageDNS); got != "" {
		t.Errorf("после Stop StageStatus(dns) = %q, ожидалось пусто", got)
	}
	for name, move := range map[string]func() (Transition, bool){
		"BeginAttempt": m.BeginAttempt,
		"Degrade":      m.Degrade,
		"Recover":      m.Recover,
		"Reconnect":    func() (Transition, bool) { return m.Reconnect(errors.New("поздно")) },
	} {
		if tr, ok := move(); ok {
			t.Errorf("%s после Stop дал переход: %+v", name, tr)
		}
	}
	if m.State() != StateStopping {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateStopping)
	}
}

func TestObserveIgnoresOtherEvents(t *testing.T) {
	m := New()
	m.BeginAttempt()

	for _, ev := range []core.Event{
		core.EventLog{Level: core.LevelInfo, Tag: "КЛИЕНТ", Msg: "поехали"},
		core.EventConfig{WireGuard: "[Interface]"},
		core.EventStats{ActiveWorkers: 9},
		core.EventCaptchaRequired{Session: 1},
		core.EventVkAuthRequired{Link: "https://vk.com/call/join/abc"},
		// Отказ хеша туннель не роняет: его разбирает HashPool (F2).
		core.EventHashFailed{Hash: "abc", Group: 2, Status: core.HashCheckDead},
	} {
		if tr, ok := m.Observe(ev); ok {
			t.Errorf("%T дало переход: %+v", ev, tr)
		}
	}
	if m.State() != StateResolving {
		t.Errorf("состояние %q, ожидалось %q", m.State(), StateResolving)
	}
}

func TestLabels(t *testing.T) {
	for _, s := range []State{
		StateIdle, StateResolving, StateCaptcha, StateVK, StateTURN,
		StateHandshake, StateConfiguring, StateConnected, StateDegraded,
		StateReconnecting, StateStopping,
	} {
		if label := s.Label(); label == "" || label == string(s) {
			t.Errorf("у состояния %q нет названия (получено %q)", s, label)
		}
	}
	if got := State("неизвестное").Label(); got != "неизвестное" {
		t.Errorf("Label неизвестного состояния = %q, ожидалось %q", got, "неизвестное")
	}
}

// Observe зовётся из Sink, то есть из горутин воркеров ядра, а решения
// супервизор принимает из своей. Проверяется под -race.
func TestObserveIsConcurrencySafe(t *testing.T) {
	m := New()
	m.BeginAttempt()

	var wg sync.WaitGroup
	for session := 1; session <= 8; session++ {
		wg.Add(1)
		go func(session int) {
			defer wg.Done()
			for _, ev := range successEvents() {
				ev.Session = session
				m.Observe(ev)
			}
		}(session)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 8 {
			m.State()
			m.StageStatus(core.StageDTLS)
			m.FailedStage()
			m.Degrade()
			m.Recover()
		}
	}()
	wg.Wait()

	if got := m.State(); got != StateConnected && got != StateDegraded {
		t.Errorf("состояние %q, ожидалось %q или %q", got, StateConnected, StateDegraded)
	}
}
