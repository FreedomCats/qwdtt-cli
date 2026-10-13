package core

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// recorder — Sink, складывающий события в память. Безопасен для параллельного
// вызова, потому что ядро отправляет события из горутин воркеров.
type recorder struct {
	mu     sync.Mutex
	events []Event
}

func (r *recorder) Emit(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *recorder) all() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Event(nil), r.events...)
}

// install ставит получателя событий на время теста и снимает его после.
// Получатель в ядре один на процесс, поэтому тесты событий не параллелятся.
func install(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	setSink(rec)
	t.Cleanup(func() { setSink(nil) })
	return rec
}

func TestEmitWithoutSink(t *testing.T) {
	setSink(nil)
	// Ядро отправляет события и до того, как CLI назначит получателя,
	// и после остановки. Это не должно быть падением.
	emit(EventVkAuthRequired{Link: "https://vk.com/call/join/abc"})
	emitStage(StageVK, StageOK, 0, "")
}

func TestSinkFunc(t *testing.T) {
	var got Event
	setSink(SinkFunc(func(ev Event) { got = ev }))
	t.Cleanup(func() { setSink(nil) })

	emit(EventVkAuthRequired{Link: "abc"})

	ev, ok := got.(EventVkAuthRequired)
	if !ok {
		t.Fatalf("получено %T, ожидалось EventVkAuthRequired", got)
	}
	if ev.Link != "abc" {
		t.Errorf("Link = %q, ожидалось %q", ev.Link, "abc")
	}
}

// Ради этого фильтр и написан: TURN и DTLS проходит каждый воркер
// самостоятельно, а схему подключения потребитель должен увидеть один раз.
func TestEmitStageDeduplicatesConcurrentWorkers(t *testing.T) {
	rec := install(t)

	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 1; i <= workers; i++ {
		go func() {
			defer wg.Done()
			emitStage(StageDTLS, StageOK, i, "")
		}()
	}
	wg.Wait()

	if got := len(rec.all()); got != 1 {
		t.Fatalf("отправлено событий: %d, ожидалось 1", got)
	}
}

func TestEmitStageKeepsDifferentStatuses(t *testing.T) {
	rec := install(t)

	emitStage(StageDTLS, StageRunning, 1, "")
	emitStage(StageDTLS, StageFailed, 1, "таймаут")
	emitStage(StageDTLS, StageOK, 2, "")
	emitStage(StageDTLS, StageOK, 3, "") // повтор, наружу не уходит

	events := rec.all()
	if len(events) != 3 {
		t.Fatalf("отправлено событий: %d, ожидалось 3", len(events))
	}

	want := []StageStatus{StageRunning, StageFailed, StageOK}
	for i, status := range want {
		ev, ok := events[i].(EventStage)
		if !ok {
			t.Fatalf("событие %d: %T, ожидалось EventStage", i, events[i])
		}
		if ev.Status != status {
			t.Errorf("событие %d: статус %q, ожидался %q", i, ev.Status, status)
		}
	}
}

// Шаги, закрытые ядром до старта, не должны глушить шаги, которые
// закрываются позже: WRAP выводится из пароля и проходит раньше VK.
func TestEmitStageIgnoresOrder(t *testing.T) {
	rec := install(t)

	emitStage(StageWrap, StageOK, 0, "")
	emitStage(StageVK, StageOK, 0, "")

	if got := len(rec.all()); got != 2 {
		t.Fatalf("отправлено событий: %d, ожидалось 2", got)
	}
}

// Переподключение начинает схему заново, иначе второй заход был бы неотличим
// от продолжения первого и не показал бы ни одного шага.
func TestResetStagesAllowsRepeat(t *testing.T) {
	rec := install(t)

	emitStage(StageTURN, StageOK, 1, "")
	emitStage(StageTURN, StageOK, 1, "")
	resetStages()
	emitStage(StageTURN, StageOK, 1, "")

	if got := len(rec.all()); got != 2 {
		t.Fatalf("отправлено событий: %d, ожидалось 2", got)
	}
}

func TestEmitPing(t *testing.T) {
	rec := install(t)

	emitPing(42)

	ev, ok := rec.all()[0].(EventPing)
	if !ok {
		t.Fatalf("получено %T, ожидалось EventPing", rec.all()[0])
	}
	if ev.Err != nil {
		t.Errorf("Err = %v, ожидалось nil", ev.Err)
	}
	if ev.RTT != 42*time.Millisecond {
		t.Errorf("RTT = %v, ожидалось 42ms", ev.RTT)
	}
}

func TestEmitPingError(t *testing.T) {
	rec := install(t)

	cause := errors.New("GetCreds hash 0: call not found")
	emitPingError(cause)
	// Хешей не было вовсе — причины не осталось, но событие всё равно нужно.
	emitPingError(nil)

	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("отправлено событий: %d, ожидалось 2", len(events))
	}

	withCause, ok := events[0].(EventPing)
	if !ok {
		t.Fatalf("событие 0: %T, ожидалось EventPing", events[0])
	}
	if !errors.Is(withCause.Err, cause) {
		t.Errorf("Err = %v, ожидалось обёрнутое %v", withCause.Err, cause)
	}

	empty, ok := events[1].(EventPing)
	if !ok {
		t.Fatalf("событие 1: %T, ожидалось EventPing", events[1])
	}
	if empty.Err == nil {
		t.Error("Err = nil, ожидалась ошибка")
	}
}

func TestEmitHashCheck(t *testing.T) {
	rec := install(t)

	emitHashCheck(1, 3, "abc", HashCheckRunning, "")
	emitHashCheck(1, 3, "abc", HashCheckDead, "Звонок не найден или закрыт")

	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("отправлено событий: %d, ожидалось 2", len(events))
	}

	start := events[0].(EventHashCheck)
	if start.Status != HashCheckRunning || start.Index != 1 || start.Total != 3 || start.Hash != "abc" {
		t.Errorf("начало проверки: %+v", start)
	}

	result := events[1].(EventHashCheck)
	if result.Status != HashCheckDead || result.Msg == "" {
		t.Errorf("итог проверки: %+v", result)
	}
}

// Строки classifyHashCheckError должны совпадать со значениями HashCheckStatus:
// ядро приводит одно к другому прямым преобразованием типа.
func TestHashCheckStatusMatchesClassifier(t *testing.T) {
	cases := map[error]HashCheckStatus{
		nil:                            HashCheckOK,
		errors.New("captcha_required"): HashCheckCaptcha,
		errors.New("call not found"):   HashCheckDead,
		errors.New("flood control"):    HashCheckLimited,
		errors.New("i/o timeout"):      HashCheckNetwork,
		errors.New("сервер ответил ерундой"): HashCheckError,
	}

	for err, want := range cases {
		status, _ := classifyHashCheckError(err)
		if HashCheckStatus(status) != want {
			t.Errorf("classifyHashCheckError(%v) = %q, ожидалось %q", err, status, want)
		}
	}
}

// Отказ хеша уходит наружу классифицированным: супервизор по статусу решает,
// исключать хеш навсегда или пробовать снова (задача F2).
func TestEmitHashFailed(t *testing.T) {
	rec := install(t)

	emitHashFailed("a1b2c3d4e5f6", 2, errors.New("vk: call not found"))
	emitHashFailed("ffffffffffff", 3, errors.New("i/o timeout"))

	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("событий %d, ожидалось 2", len(events))
	}

	dead, ok := events[0].(EventHashFailed)
	if !ok {
		t.Fatalf("получено %T, ожидалось EventHashFailed", events[0])
	}
	if dead.Hash != "a1b2c3d4e5f6" || dead.Group != 2 {
		t.Errorf("хеш %q, группа %d", dead.Hash, dead.Group)
	}
	if dead.Status != HashCheckDead {
		t.Errorf("статус %q, ожидался %q", dead.Status, HashCheckDead)
	}
	if dead.Msg == "" {
		t.Error("пояснение пустое")
	}

	network, _ := events[1].(EventHashFailed)
	if network.Status != HashCheckNetwork {
		t.Errorf("статус %q, ожидался %q", network.Status, HashCheckNetwork)
	}
}
