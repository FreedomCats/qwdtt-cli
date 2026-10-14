// events.go — события ядра. Файл наш: из апстрима не переносится и при
// обновлении ядра не затирается (см. scripts/vendorcore).
//
// Апстримное ядро — отдельный процесс, и единственным его каналом связи с
// приложением был stdout: шесть маркеров вида "CAPTCHA_SOLVE|…", рамки с
// конфигом WireGuard и обычные логи. Android разбирал маркеры регулярками, а
// состояние подключения восстанавливал, вылавливая подстроки из логов
// (TunnelManager.kt, ~940–1030). Здесь всё это заменяется типизированными
// событиями — решение из architecture.md §06.
//
// Потребитель (CLI, супервизор, решатель капчи) реализует Sink и разбирает
// событие через type switch.

package core

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Event — событие ядра. Набор реализаций закрыт: метод-маркер неэкспортируемый,
// чтобы снаружи нельзя было добавить тип, о котором не знает ни один type switch.
type Event interface{ isCoreEvent() }

// Sink принимает события ядра.
//
// Emit вызывается из горутин воркеров, поэтому обязан быть безопасным для
// параллельного вызова. Блокироваться надолго ему нельзя: пока Emit не вернулся,
// воркер стоит.
type Sink interface{ Emit(Event) }

// SinkFunc превращает функцию в Sink.
type SinkFunc func(Event)

// Emit реализует Sink.
func (f SinkFunc) Emit(ev Event) { f(ev) }

// sinkHolder существует только затем, чтобы в atomic.Value можно было положить
// интерфейс: она требует одинакового конкретного типа при каждой записи, а
// разные Sink — это разные типы.
type sinkHolder struct{ sink Sink }

var currentSink atomic.Value // sinkHolder

// setSink назначает получателя событий. До первого вызова события отбрасываются.
// Вызывается из Run (задача B4); отдельного публичного метода нет намеренно —
// приёмник задаётся вместе с запуском ядра.
func setSink(s Sink) {
	currentSink.Store(sinkHolder{sink: s})
	resetStages()
}

// emit отправляет событие текущему получателю. Без получателя — тихо ничего.
func emit(ev Event) {
	holder, _ := currentSink.Load().(sinkHolder)
	if holder.sink == nil {
		return
	}
	holder.sink.Emit(ev)
}

// ───────────────────────────── логи ─────────────────────────────

// LogLevel — важность записи. Ядро различает только три уровня: подробностей
// достаточно в тексте сообщения, а отладочный уровень в апстриме и так
// перемешан с информационным.
type LogLevel string

const (
	LevelInfo  LogLevel = "info"
	LevelWarn  LogLevel = "warn"
	LevelError LogLevel = "error"
)

// EventLog — то, что в апстриме уходило в log.Printf.
//
// Tag — префикс сообщения, который ядро и так всегда писало: "КЛИЕНТ",
// "СЕССИЯ #3", "КАПЧА". Он вынесен в отдельное поле, чтобы потребитель мог
// фильтровать по подсистеме, не разбирая текст. Подстановку тега делает
// задача B3.
type EventLog struct {
	Level LogLevel
	Tag   string
	Msg   string
}

func (EventLog) isCoreEvent() {}

// ──────────────────────── шаги подключения ────────────────────────

// Stage — шаг подключения. Список повторяет ConnectionStep из Android
// (app/.../ConnectionPipeline.kt) один в один, чтобы обе оболочки показывали
// пользователю одну и ту же схему.
type Stage string

const (
	StageDNS     Stage = "dns"     // резолвер до VK живой
	StageVK      Stage = "vk"      // получены TURN-креды
	StageCaptcha Stage = "captcha" // решается Smart Captcha
	StageWrap    Stage = "wrap"    // выведен ключ RTP-обфускации
	StageTURN    Stage = "turn"    // получен relay на TURN-сервере VK
	StageDTLS    Stage = "dtls"    // рукопожатие с сервером
	StageWorkers Stage = "workers" // воркеры вышли на линию
	StageVPN     Stage = "vpn"     // поднят WireGuard
	StageSocks   Stage = "socks"   // поднят локальный SOCKS5
	StageDone    Stage = "done"    // туннель работает
)

// Кто какие шаги отправляет.
//
// Ядро отвечает за середину: VK, CAPTCHA, WRAP, TURN, DTLS, WORKERS.
// Края принадлежат супервизору, потому что ядро о них не знает:
//
//   - DNS — предварительная проверка резолвера идёт до запуска ядра
//     (на Android этим тоже занимается оболочка, TunnelManager.kt:522);
//   - VPN, SOCKS, DONE — datapath живёт вне ядра и поднимается по EventConfig.

// StageStatus — что случилось с шагом.
type StageStatus string

const (
	StageRunning StageStatus = "running"
	StageOK      StageStatus = "ok"
	StageFailed  StageStatus = "failed"
)

// EventStage — переход шага подключения.
//
// Session — номер воркера или сессии, породившей событие; 0 означает событие
// клиента целиком. Поле нужно для лога: воркеров бывает до 108, и знать, чей
// именно DTLS не сложился, полезно.
type EventStage struct {
	Stage   Stage
	Status  StageStatus
	Session int
	Msg     string
}

func (EventStage) isCoreEvent() {}

// stageKey — пара, которую ядро отправляет не больше одного раза за подключение.
type stageKey struct {
	stage  Stage
	status StageStatus
}

var (
	stagesMu   sync.Mutex
	stagesSeen = map[stageKey]bool{}
)

// resetStages открывает новую последовательность шагов. Нужен при переподключении:
// иначе фильтр повторов посчитает второй заход продолжением первого и съест его.
func resetStages() {
	stagesMu.Lock()
	clear(stagesSeen)
	stagesMu.Unlock()
}

// emitStage отправляет переход шага, отсеивая повторы.
//
// Фильтр обязателен: воркеров девять и больше, каждый самостоятельно проходит
// TURN и DTLS, и без фильтра потребитель получал бы сотню одинаковых событий
// за секунду. Проходит первый воркер, добравшийся до пары (шаг, статус),
// остальные молчат.
//
// Порядок событий между собой ядро не выравнивает: WRAP закрывается на старте,
// раньше VK, потому что ключ выводится из пароля, а не из ответа VK. Складывать
// из событий схему подключения — дело потребителя; Android так же держал
// множество пройденных шагов отдельно от указателя на текущий.
func emitStage(stage Stage, status StageStatus, session int, msg string) {
	key := stageKey{stage: stage, status: status}

	stagesMu.Lock()
	seen := stagesSeen[key]
	stagesSeen[key] = true
	stagesMu.Unlock()

	if seen {
		return
	}
	emit(EventStage{Stage: stage, Status: status, Session: session, Msg: msg})
}

// ───────────────────────────── капча ─────────────────────────────

// EventCaptchaRequired — ядро само капчу не осилило и ждёт ответа снаружи.
// Заменяет маркер CAPTCHA_SOLVE.
//
// Ответ возвращается через Control.SubmitCaptcha (задача B5). Ядро ждёт его
// с таймаутом, так что молчание потребителя не вешает подключение — но и
// подключение тогда не состоится.
type EventCaptchaRequired struct {
	// Session — номер потока, запросившего капчу.
	Session int
	// Mode — auto, manual или selected: чем занималось ядро до того, как сдалось.
	Mode string
	// RedirectURI — страница капчи VK, которую нужно открыть.
	RedirectURI string
	// SessionToken — токен сессии капчи; без него ответ не примут.
	SessionToken string
}

func (EventCaptchaRequired) isCoreEvent() {}

// EventVkAuthRequired — режиму vk-auth=account нужны TURN-креды от живого
// аккаунта VK. Заменяет маркер VK_AUTH_REQUIRED.
//
// Ответ возвращается через Control.SubmitTurnCreds (задача B5).
type EventVkAuthRequired struct {
	// Link — ссылка на звонок, для которого нужны креды.
	Link string
}

func (EventVkAuthRequired) isCoreEvent() {}

// ──────────────────────── проверка хешей ────────────────────────

// HashCheckStatus — исход проверки одного хеша звонка.
// Значения повторяют строки из classifyHashCheckError.
type HashCheckStatus string

const (
	HashCheckRunning HashCheckStatus = "running" // проверка началась
	HashCheckOK      HashCheckStatus = "ok"
	HashCheckCaptcha HashCheckStatus = "captcha" // VK просит капчу
	HashCheckDead    HashCheckStatus = "dead"    // звонок не найден или закрыт
	HashCheckLimited HashCheckStatus = "limited" // VK ограничил запросы
	HashCheckNetwork HashCheckStatus = "network"
	HashCheckError   HashCheckStatus = "error"
)

// EventHashCheck — шаг команды `qwdtt-cli hashes`. Заменяет пару маркеров
// HASH_CHECK_START и HASH_CHECK: началу проверки соответствует HashCheckRunning.
type EventHashCheck struct {
	// Index — номер хеша, начиная с единицы; Total — сколько их всего.
	Index int
	Total int
	Hash  string

	Status HashCheckStatus
	Msg    string
}

func (EventHashCheck) isCoreEvent() {}

// emitHashCheck — обёртка ради коротких вызовов в runHashChecks.
func emitHashCheck(index, total int, hash string, status HashCheckStatus, msg string) {
	emit(EventHashCheck{Index: index, Total: total, Hash: hash, Status: status, Msg: msg})
}

// EventHashFailed — группа воркеров не получила креды VK по своему хешу звонка
// и не стартовала.
//
// Ядро переживает это само: группа возвращается, остальные продолжают работать,
// и туннель держится на них. Наружу событие уходит затем, что решение «этот хеш
// больше не предлагать, а когда годных не останется — остановиться» принимает
// супервизор (architecture.md §07, задача F2). В апстриме сигнала не было вовсе:
// Android вылавливал отказ из строки лога.
//
// Событие приходит по одному на группу, фильтра повторов у него нет: у каждой
// группы свой хеш, и терять вторую её не за что.
type EventHashFailed struct {
	// Hash — хеш звонка, по которому не вышло. Целиком: супервизор сопоставляет
	// его со своим списком. Печатать его нельзя — хеши лежат в хранилище
	// секретов (задача C2).
	Hash string
	// Group — номер группы воркеров, начиная с единицы.
	Group int
	// Status — чем кончилась попытка. HashCheckDead означает, что звонок закрыт
	// и с тем же хешем не поможет ни повтор, ни перезапуск; остальные статусы
	// временные.
	Status HashCheckStatus
	// Msg — пояснение для пользователя.
	Msg string
}

func (EventHashFailed) isCoreEvent() {}

// emitHashFailed классифицирует ошибку и отправляет событие.
//
// Классификация — та же classifyHashCheckError, которой пользуется `qwdtt-cli hashes`:
// отличать «звонок закрыт» от «сеть отвалилась» нужно одинаково в обоих местах,
// иначе одна и та же ошибка значила бы в команде одно, а в туннеле другое.
func emitHashFailed(hash string, group int, err error) {
	status, msg := classifyHashCheckError(err)
	emit(EventHashFailed{
		Hash:   hash,
		Group:  group,
		Status: HashCheckStatus(status),
		Msg:    sanitizeHashCheckMessage(msg),
	})
}

// ───────────────────────────── ping ─────────────────────────────

// EventPing — итог команды `qwdtt-cli ping`. Заменяет маркеры PING_RESULT и PING_ERROR.
// При Err != nil значение RTT не определено.
type EventPing struct {
	RTT time.Duration
	Err error
}

func (EventPing) isCoreEvent() {}

// emitPing отправляет удачный замер. Значение приходит в миллисекундах — так
// его считает runPing.
func emitPing(rttMillis int64) {
	emit(EventPing{RTT: time.Duration(rttMillis) * time.Millisecond})
}

// emitPingError отправляет неудачу и возвращает ту же ошибку: RunPing отдаёт
// её наружу, чтобы CLI мог выставить код возврата, не разбирая события.
//
// lastErr бывает нулевым: если хешей не было вовсе, ни одной попытки не
// делалось и ошибке взяться неоткуда.
func emitPingError(lastErr error) error {
	var err error
	if lastErr == nil {
		err = errors.New("не проверено ни одного хеша звонка")
	} else {
		err = fmt.Errorf("все хеши провалились, последняя ошибка: %w", lastErr)
	}
	emit(EventPing{Err: err})
	return err
}

// ──────────────────────────── конфиг ────────────────────────────

// EventConfig — конфиг WireGuard, выданный сервером. Заменяет рамку, которую
// ядро печатало в stdout.
//
// Текст содержит приватный ключ, поэтому в лог он не идёт: получатель —
// datapath, который поднимает по нему туннель (architecture.md §04).
type EventConfig struct {
	WireGuard string
}

func (EventConfig) isCoreEvent() {}

// ─────────────────────────── статистика ───────────────────────────

// EventStats — счётчики трафика, снятые раз в несколько секунд.
// В апстриме они существовали только строкой в логе.
type EventStats struct {
	// ActiveWorkers — сколько воркеров сейчас держат соединение.
	ActiveWorkers int32
	// BytesUp и BytesDown — нарастающий итог с момента запуска.
	BytesUp   int64
	BytesDown int64
	// DroppedUp и DroppedDown — пакеты, выброшенные ядром из-за полных
	// очередей, нарастающим итогом с момента запуска: туда — к воркерам,
	// обратно — к WireGuard. В апстриме их не считали вовсе.
	DroppedUp   uint64
	DroppedDown uint64
}

func (EventStats) isCoreEvent() {}
