// restart.go — политика перезапуска (задача F2): задержки между попытками,
// watchdog по трафику и учёт годных хешей звонка.
//
// Как и автомат в state.go, всё здесь бесчасовое: текущее время приходит
// аргументом, а ждать задержку и опрашивать watchdog — дело цикла подключения
// (lifecycle.go, F3). Иначе тесты спали бы по минуте и зависели от планировщика.
package supervisor

import (
	"strings"
	"sync"
	"time"

	"qwdtt-cli/internal/core"
)

// maxBackoffShift — потолок счётчика попыток. Шестая попытка ждёт 64 секунды,
// все следующие — тоже: дальше растить задержку бессмысленно, пользователь
// с работающей сетью к этому времени уже всё понял, а на сломанной клиент
// должен остаться живым и пробовать.
const maxBackoffShift = 6

// Backoff — задержка перед повтором после неудачной попытки номер attempt
// (нумерация с нуля: первая неудача даёт секунду).
//
// Последовательность перенесена из Android дословно (TunnelManager.kt:1268):
// 1, 2, 4, 8, 16, 32, 64 секунды и дальше по 64.
func Backoff(attempt int) time.Duration {
	switch {
	case attempt < 0:
		attempt = 0
	case attempt > maxBackoffShift:
		attempt = maxBackoffShift
	}
	return time.Duration(1<<attempt) * time.Second
}

// ──────────────────────── хеши звонков ────────────────────────

// HashPool — хеши звонков VK и их годность.
//
// Группы воркеров ядро раздаёт по хешам: группа, не получившая креды, молча
// возвращается, а остальные продолжают работать — туннель от одного негодного
// хеша не падает. Отменять группы пулу поэтому незачем, это ядро уже сделало.
// Его работа — помнить, какие хеши предлагать следующей попытке, и сказать,
// когда предлагать стало нечего (architecture.md §07).
//
// Безопасен для параллельного вызова: Observe зовётся из Sink, то есть из
// горутин воркеров ядра.
type HashPool struct {
	mu   sync.Mutex
	all  []string
	dead map[string]string // хеш → причина исключения
}

// NewHashPool заводит пул из хешей профиля. Порядок сохраняется: ядро раздаёт
// хеши группам по индексу, и менять их местами между попытками незачем.
func NewHashPool(hashes []string) *HashPool {
	return &HashPool{all: append([]string(nil), hashes...), dead: map[string]string{}}
}

// Alive — хеши, которые ещё стоит предлагать ядру, в исходном порядке.
func (p *HashPool) Alive() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	alive := make([]string, 0, len(p.all))
	for _, hash := range p.all {
		if _, bad := p.dead[hash]; !bad {
			alive = append(alive, hash)
		}
	}
	return alive
}

// Exhausted — годных хешей не осталось. Пустой пул исчерпан с самого начала:
// подключаться без хешей не к чему.
func (p *HashPool) Exhausted() bool {
	return len(p.Alive()) == 0
}

// MarkDead исключает хеш. Возвращает false, если хеш уже исключён или пулу
// не принадлежит: пришедший ниоткуда хеш не повод считать, что туннелю хуже.
func (p *HashPool) MarkDead(hash, reason string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, bad := p.dead[hash]; bad {
		return false
	}
	if !p.knows(hash) {
		return false
	}
	p.dead[hash] = reason
	return true
}

// DeadReason — почему хеш исключён. Пустая строка означает, что он годен.
func (p *HashPool) DeadReason(hash string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead[hash]
}

// Observe применяет событие ядра. Исключает хеш только core.HashCheckDead —
// звонок не найден или закрыт, и ни повтор, ни перезапуск его не вернут.
// Капча, лимит запросов и сетевая ошибка временные: по ним хеш остаётся в игре,
// иначе первая же неурядица со связью срезала бы все хеши разом и остановила
// клиент вместо того, чтобы переподключиться.
//
// Возвращает true, если хеш именно этим событием и исключён — вызывающему
// есть о чём напечатать строку.
func (p *HashPool) Observe(ev core.Event) bool {
	failed, ok := ev.(core.EventHashFailed)
	if !ok || failed.Status != core.HashCheckDead {
		return false
	}
	return p.MarkDead(failed.Hash, failed.Msg)
}

// knows — принадлежит ли хеш пулу. Вызывается при захваченном p.mu.
func (p *HashPool) knows(hash string) bool {
	for _, known := range p.all {
		if known == hash {
			return true
		}
	}
	return false
}

// ShortHash — первые восемь символов хеша.
//
// Целиком его печатать нельзя: хеши звонков лежат в хранилище секретов рядом
// с паролем подключения (задача C2). Различать их в выводе при этом нужно,
// и ядро в своих логах обрезает их так же (group.go).
func ShortHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) > 8 {
		return hash[:8] + "…"
	}
	return hash
}

// ──────────────────────────── watchdog ────────────────────────────

// DefaultStallWindow — сколько ждать ответа, прежде чем считать туннель
// неработающим. Та же величина, что у hasRecentTransportActivity на Android.
const DefaultStallWindow = 25 * time.Second

// StallReason — чем именно плох туннель. Пустая строка означает, что он здоров.
type StallReason string

const (
	// StallNone — претензий нет.
	StallNone StallReason = ""
	// StallNoDownlink — мы отправляли, ответа нет.
	StallNoDownlink StallReason = "нет ответа от сервера"
	// StallNoStats — ядро перестало присылать счётчики, хотя работает.
	StallNoStats StallReason = "ядро не присылает статистику"
)

// Watchdog следит, что туннель не только поднят, но и работает.
//
// Судит по core.EventStats — счётчикам, которые ядро присылает раз в три
// секунды. Времени внутри нет: текущее время приходит аргументом, а опрашивать
// Stall своим тикером — дело F3.
//
// Безопасен для параллельного вызова: Observe зовётся из Sink.
type Watchdog struct {
	mu     sync.Mutex
	window time.Duration

	seen    bool  // была ли хоть одна статистика
	workers int32 // из последней статистики
	up      int64
	down    int64

	statsAt time.Time // когда статистика приходила последний раз
	// unansweredAt — когда начался восходящий трафик, на который с тех пор
	// не пришло ни байта в ответ. Нулевое значение означает, что долга нет:
	// либо не отправляли, либо всё отвеченное.
	unansweredAt time.Time
}

// NewWatchdog заводит watchdog с окном терпения. Ноль означает
// DefaultStallWindow.
func NewWatchdog(window time.Duration) *Watchdog {
	if window <= 0 {
		window = DefaultStallWindow
	}
	return &Watchdog{window: window}
}

// Observe применяет событие ядра. События, кроме core.EventStats, он не трогает.
func (w *Watchdog) Observe(ev core.Event, now time.Time) {
	stats, ok := ev.(core.EventStats)
	if !ok {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.seen {
		// Счётчики нарастающие, и первая статистика приходит уже с числами:
		// считать их ростом нельзя, иначе первый же тик выглядел бы как
		// неотвеченная отправка.
		w.seen = true
		w.up, w.down = stats.BytesUp, stats.BytesDown
	}
	// Порядок важен: ответ сначала закрывает прежний долг, и только потом
	// новая отправка открывает новый. Иначе тик, в котором выросли оба
	// счётчика, оставлял бы долг с прошлого раза.
	if stats.BytesDown > w.down {
		w.down, w.unansweredAt = stats.BytesDown, time.Time{}
	}
	if stats.BytesUp > w.up {
		w.up = stats.BytesUp
		if w.unansweredAt.IsZero() {
			w.unansweredAt = now
		}
	}
	w.workers = stats.ActiveWorkers
	w.statsAt = now
}

// Stall — что не так с туннелем прямо сейчас.
//
// Молчание считается не от последнего нисходящего байта, а от начала
// неотвеченной отправки. Оба других способа врут: от последнего нисходящего —
// туннель, через который полчаса никто ничего не посылал, объявлялся бы
// сломанным первым же запросом; от последнего восходящего — пока приложение
// продолжает отправлять, отсчёт сбрасывался бы каждые три секунды и молчание
// сервера не обнаружилось бы никогда. В Android складывали вторым способом,
// прикрывшись отдельной проверкой на живость статистики
// (TunnelManager.kt:1345).
//
// Простаивающий туннель здоров: не отправляли — нечего и ждать.
func (w *Watchdog) Stall(now time.Time) StallReason {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.seen {
		// Первой статистики ещё не было: подключение только идёт.
		return StallNone
	}
	if now.Sub(w.statsAt) > w.window {
		return StallNoStats
	}
	if w.workers <= 0 {
		// Воркеров нет — это не деградация, а отказ: ядро вернётся из Run
		// само, и перезапуск назначит F3, а не watchdog.
		return StallNone
	}
	if !w.unansweredAt.IsZero() && now.Sub(w.unansweredAt) > w.window {
		return StallNoDownlink
	}
	return StallNone
}

// Reset забывает всё: вызывается перед новой попыткой, иначе счётчики прошлой
// выглядели бы как молчание этой.
func (w *Watchdog) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.seen, w.workers, w.up, w.down = false, 0, 0, 0
	w.statsAt, w.unansweredAt = time.Time{}, time.Time{}
}

// ─────────────────────────── перезапуск ───────────────────────────

// Decision — что делать после того, как попытка кончилась.
type Decision struct {
	// Stop — пробовать больше нечем: годных хешей не осталось. Автомат при
	// этом уже переведён в StateStopping.
	Stop bool
	// Delay — сколько ждать до следующей попытки. Ноль при Stop.
	Delay time.Duration
	// Attempt — номер кончившейся попытки, начиная с единицы. Для строки
	// вида «попытка 3, следующая через 4 с».
	Attempt int
}

// Restarter — политика перезапуска: ведёт счётчик неудач и держит автомат
// с пулом хешей в согласии друг с другом.
//
// Порядок вызовов у F3 такой: Failed, когда core.Run вернулся; Connected,
// когда туннель поднялся. Автомат переводит Failed — иначе счётчик попыток
// и состояние расходились бы при любом пропущенном вызове.
type Restarter struct {
	machine *Machine
	hashes  *HashPool

	mu      sync.Mutex
	attempt int
}

// NewRestarter связывает автомат с пулом хешей.
func NewRestarter(machine *Machine, hashes *HashPool) *Restarter {
	return &Restarter{machine: machine, hashes: hashes}
}

// Failed объявляет, что попытка кончилась: уводит автомат с линии и говорит,
// ждать ли следующую и сколько.
//
// cause может быть нулевым — перезапуск бывает намеренным (смена сети в Э2).
// Когда годных хешей не осталось, автомат переводится в StateStopping: ждать
// нечего, подключаться не к чему.
func (r *Restarter) Failed(cause error) Decision {
	r.machine.Reconnect(cause)

	r.mu.Lock()
	attempt := r.attempt
	r.attempt++
	r.mu.Unlock()

	if r.hashes.Exhausted() {
		r.machine.Stop()
		return Decision{Stop: true, Attempt: attempt + 1}
	}
	return Decision{Delay: Backoff(attempt), Attempt: attempt + 1}
}

// Connected сбрасывает счётчик задержек: туннель, проработавший час, должен
// переподключаться через секунду, а не через минуту, заработанную прошлыми
// неудачами.
func (r *Restarter) Connected() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempt = 0
}

// Attempts — сколько попыток подряд кончилось неудачей.
func (r *Restarter) Attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.attempt
}
