// Package supervisor сводит ядро, datapath и решатель капчи в один туннель:
// держит состояние подключения, решает, когда перезапускать ядро, и гарантирует,
// что datapath снимется при остановке (architecture.md §07).
//
// В Android этим занимался TunnelManager.kt — 2162 строки с watchdog, разбором
// лога и парой десятков состояний UI. Здесь остаётся конечный автомат (этот
// файл, задача F1), политика перезапуска (restart.go, F2) и завершение
// (lifecycle.go, F3).
//
// state.go — сам автомат. Он ничего не запускает и не ждёт: принимает события
// ядра и решения супервизора, а отдаёт состояние и переходы. Таймеров внутри
// нет намеренно — автомат ведётся событиями, а время живёт в restart.go.
package supervisor

import (
	"sync"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// State — состояние туннеля. Набор повторяет схему из architecture.md §07.
type State string

const (
	// StateIdle — ничего не запущено: до первой попытки подключения.
	StateIdle State = "idle"
	// StateResolving — проверяется резолвер до VK. Шаг наш, не ядра: он идёт
	// до core.Run (см. «Кто какие шаги отправляет» в core/events.go).
	StateResolving State = "resolving"
	// StateCaptcha — VK показал Smart Captcha и она решается.
	StateCaptcha State = "captcha"
	// StateVK — получены TURN-креды.
	StateVK State = "vk"
	// StateTURN — получен relay на TURN-сервере VK.
	StateTURN State = "turn"
	// StateHandshake — рукопожатие DTLS с сервером.
	StateHandshake State = "handshake"
	// StateConfiguring — воркеры на линии, поднимается datapath.
	StateConfiguring State = "configuring"
	// StateConnected — туннель работает.
	StateConnected State = "connected"
	// StateDegraded — воркеры живы, но нисходящего трафика нет дольше 25 с.
	// Выставляется watchdog'ом из restart.go, а не событием ядра.
	StateDegraded State = "degraded"
	// StateReconnecting — попытка закончилась, ядро останавливается, следующая
	// попытка ждёт своей задержки (F2).
	StateReconnecting State = "reconnecting"
	// StateStopping — останавливаемся окончательно: по Ctrl+C или потому, что
	// годных хешей не осталось. Состояние конечное, из него переходов нет.
	StateStopping State = "stopping"
)

// lineRank — место состояния на линии подключения. Линия проходится только
// вперёд: ядро порядок шагов не выравнивает (core/events.go, emitStage), и без
// этого правила поздний отказ DTLS одного воркера из ста вернул бы поднятый
// туннель в состояние «рукопожатие».
//
// Состояний вне линии — Degraded, Reconnecting, Stopping — в карте нет:
// отсутствие ранга и означает «события шагов сюда не применяются».
var lineRank = map[State]int{
	StateIdle:        0,
	StateResolving:   1,
	StateCaptcha:     2,
	StateVK:          3,
	StateTURN:        4,
	StateHandshake:   5,
	StateConfiguring: 6,
	StateConnected:   7,
}

// Капча стоит на линии раньше VK, хотя схема в §07 рисует её после. Так идут
// сами события: ядро закрывает шаг vk, только когда креды получены, а капчу
// просит по ходу запроса (creds.go → group.go). При обратном порядке фильтр
// регрессий съедал бы «креды получены» как шаг назад.

// stateLabels — названия состояний для вывода. Состояние видно только в выводе
// самого qwdtt-cli connect, отдельного канала для опроса извне нет (§07).
var stateLabels = map[State]string{
	StateIdle:         "не подключено",
	StateResolving:    "проверка DNS",
	StateCaptcha:      "решается капча",
	StateVK:           "получены креды VK",
	StateTURN:         "получен relay TURN",
	StateHandshake:    "рукопожатие с сервером",
	StateConfiguring:  "поднимается туннель",
	StateConnected:    "туннель работает",
	StateDegraded:     "трафика нет",
	StateReconnecting: "переподключение",
	StateStopping:     "остановка",
}

// Label — название состояния по-русски. У неизвестного состояния — его же код:
// печатать пустую строку вместо состояния хуже, чем печатать «idle».
func (s State) Label() string {
	if label, ok := stateLabels[s]; ok {
		return label
	}
	return string(s)
}

// stageState — в какое состояние ведёт шаг подключения.
//
// core.StageWrap в карте отсутствует намеренно: своего состояния у него нет.
// Ключ RTP-обфускации выводится из пароля, а не из ответа VK, поэтому ядро
// закрывает этот шаг на старте — раньше всех остальных (architecture.md §06).
// Отдельное состояние означало бы, что подключение началось с конца.
//
// Три шага — workers, vpn и socks — ведут в одно состояние: после выхода
// воркеров на линию остаётся поднять datapath, и для пользователя это одна
// фаза. В Android шаги VPN, RAW и SOCKS ровно так же делили один order.
var stageState = map[core.Stage]State{
	core.StageDNS:     StateResolving,
	core.StageCaptcha: StateCaptcha,
	core.StageVK:      StateVK,
	core.StageTURN:    StateTURN,
	core.StageDTLS:    StateHandshake,
	core.StageWorkers: StateConfiguring,
	core.StageVPN:     StateConfiguring,
	core.StageSocks:   StateConfiguring,
	core.StageDone:    StateConnected,
}

// Transition — состоявшийся переход. Возвращается каждым методом, меняющим
// состояние, чтобы супервизор напечатал строку (§07) и не восстанавливал
// причину перехода из состояния постфактум.
type Transition struct {
	From State
	To   State
	// Stage — шаг, вызвавший переход. Пусто, если переход сделал сам
	// супервизор: начало попытки, watchdog, остановка.
	Stage core.Stage
	// Cause — причина ухода с линии. Заполнена только у перехода
	// в StateReconnecting, и то не всегда: перезапуск бывает намеренным,
	// например при смене сети.
	Cause error
}

// Machine — конечный автомат подключения.
//
// Безопасен для параллельного вызова: Observe зовётся из Sink, то есть из
// горутин воркеров ядра, а остальные методы — из супервизора.
type Machine struct {
	mu      sync.Mutex
	state   State
	stages  map[core.Stage]core.StageStatus
	failed  core.Stage
	lastErr error
}

// New возвращает автомат в состоянии StateIdle.
func New() *Machine {
	return &Machine{state: StateIdle, stages: map[core.Stage]core.StageStatus{}}
}

// State — текущее состояние.
func (m *Machine) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// StageStatus — чем кончился шаг в текущей попытке. Пустая строка означает, что
// о шаге ничего не известно: он не начинался или ядро о нём не сообщает
// (например, core.StageVK о неудаче молчит — молчание и есть неудача).
func (m *Machine) StageStatus(stage core.Stage) core.StageStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stages[stage]
}

// FailedStage — шаг, отказ которого ещё не перекрыт успехом. Пусто, если такого
// нет. Шаг считается перекрытым, когда по нему приходит core.StageOK: TURN и
// DTLS каждый воркер проходит самостоятельно, и отказ одного из ста не означает,
// что шаг не прошёл.
func (m *Machine) FailedStage() core.Stage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failed
}

// LastError — причина последнего ухода с линии, переданная в Reconnect.
// Сбрасывается началом новой попытки.
func (m *Machine) LastError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastErr
}

// BeginAttempt открывает попытку подключения: StateIdle или StateReconnecting
// переходят в StateResolving, таблица шагов очищается.
//
// Из остальных состояний — ничего: попытка уже идёт, а из StateStopping
// переходов нет вовсе.
func (m *Machine) BeginAttempt() (Transition, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != StateIdle && m.state != StateReconnecting {
		return Transition{}, false
	}
	m.stages = map[core.Stage]core.StageStatus{}
	m.failed = ""
	m.lastErr = nil
	return m.set(StateResolving, "", nil)
}

// Observe применяет событие ядра. События, кроме core.EventStage, автомат не
// трогают — их разбирают другие части супервизора.
//
// Шаг двигает состояние только вперёд по линии и только пока автомат на ней:
// в StateDegraded и StateReconnecting событие попадёт в таблицу шагов, но
// состояния не изменит. Событие, пришедшее в StateStopping, отбрасывается
// целиком: попытка кончилась, и умирающие воркеры не должны перерисовывать
// картину.
//
// Отказ шага состояния тоже не меняет. Ядро повторяет шаги само (капчу —
// трижды, TURN и DTLS — каждым воркером), и закончилась попытка или нет,
// знает не шаг, а возврат из core.Run; супервизор скажет об этом через
// Reconnect.
func (m *Machine) Observe(ev core.Event) (Transition, bool) {
	stage, ok := ev.(core.EventStage)
	if !ok {
		return Transition{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == StateStopping {
		return Transition{}, false
	}
	m.record(stage)

	to, known := stageState[stage.Stage]
	if !known || stage.Status == core.StageFailed {
		return Transition{}, false
	}
	rank, onLine := lineRank[m.state]
	if !onLine || lineRank[to] <= rank {
		return Transition{}, false
	}
	return m.set(to, stage.Stage, nil)
}

// Degrade отмечает, что нисходящего трафика нет дольше разрешённого: туннель
// формально поднят, но не работает. Осмысленно только из StateConnected —
// остальные состояния и так не обещают трафика.
func (m *Machine) Degrade() (Transition, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != StateConnected {
		return Transition{}, false
	}
	return m.set(StateDegraded, "", nil)
}

// Recover возвращает туннель из StateDegraded в StateConnected: трафик пошёл
// сам, перезапускать нечего.
//
// Схема в §07 такой стрелки не рисует — там из Degraded только перезапуск или
// остановка. Но watchdog судит по окну в 25 секунд, а молчание такой длины
// бывает и у живого туннеля; считать его отказом значило бы рвать рабочее
// соединение.
func (m *Machine) Recover() (Transition, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state != StateDegraded {
		return Transition{}, false
	}
	return m.set(StateConnected, "", nil)
}

// Reconnect уводит автомат с линии: попытка кончилась, ядро останавливается,
// следующая попытка ждёт задержки. Когда начинать её — решает restart.go (F2),
// автомат остаётся в StateReconnecting до BeginAttempt.
//
// cause может быть нулевым: перезапуск бывает намеренным, например при смене
// сети. Из StateIdle и StateStopping — ничего: в первом нечего переподключать,
// второе конечное.
func (m *Machine) Reconnect(cause error) (Transition, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch m.state {
	case StateIdle, StateReconnecting, StateStopping:
		return Transition{}, false
	}
	m.lastErr = cause
	return m.set(StateReconnecting, "", cause)
}

// Stop переводит автомат в конечное StateStopping: дальше только снятие
// datapath и выход (F3). Работает из любого состояния, включая StateIdle:
// Ctrl+C до первой попытки — тоже остановка.
func (m *Machine) Stop() (Transition, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.set(StateStopping, "", nil)
}

// record запоминает исход шага. Вызывается при захваченном m.mu.
func (m *Machine) record(ev core.EventStage) {
	switch ev.Status {
	case core.StageOK:
		m.stages[ev.Stage] = core.StageOK
		if m.failed == ev.Stage {
			m.failed = ""
		}
	case core.StageFailed:
		m.stages[ev.Stage] = core.StageFailed
		m.failed = ev.Stage
	case core.StageRunning:
		// Поздний «начался» не затирает уже известный исход: у шага, который
		// проходит каждый воркер, порядок событий между воркерами не
		// гарантирован.
		if m.stages[ev.Stage] == "" {
			m.stages[ev.Stage] = core.StageRunning
		}
	}
}

// set меняет состояние и собирает переход. Вызывается при захваченном m.mu.
// Переход в то же состояние переходом не считается: печатать «подключено →
// подключено» незачем.
func (m *Machine) set(to State, stage core.Stage, cause error) (Transition, bool) {
	if m.state == to {
		return Transition{}, false
	}
	tr := Transition{From: m.state, To: to, Stage: stage, Cause: cause}
	m.state = to
	return tr, true
}
