// watch.go — слежение за сменой сети (задача S2 Э2, architecture.md §07).
//
// Физический интерфейс выбирается при запуске (SelectInterfaces), но сеть
// меняется на ходу: Wi-Fi → Ethernet, отключение кабеля, сон и пробуждение.
// Watch ловит изменения маршрутов и интерфейсов, сворачивает их серию в одно
// решение, ставит новые индексы и говорит вызывающему, что сеть сменилась, —
// перезапуск туннеля его дело.
package netguard

import (
	"sync"
	"time"
)

// DefaultSettle — сколько тишины после последнего события ждать, прежде чем
// решать. Переключение Wi-Fi → Ethernet даёт серию событий маршрутов
// и интерфейсов за секунду-другую; решение по первому из них дало бы
// несколько перезапусков подряд (§07).
const DefaultSettle = 2 * time.Second

// DefaultMaxWait — потолок ожидания тишины: сеть, которая шумит без конца,
// всё равно получает решение.
const DefaultMaxWait = 10 * time.Second

// WatchOptions — настройки Watch.
type WatchOptions struct {
	// Exclude — свой адаптер на момент события (AdapterID datapath: на
	// Windows — LUID, на Linux — индекс интерфейса); 0 или nil —
	// исключать нечего. События этого интерфейса пропускаются, и в выбор он
	// не попадает: иначе туннель, ставя свои маршруты, перезапускал бы
	// себя сам.
	Exclude func() uint64

	// Changed получает новый и прежний выбор — один раз на свёрнутую серию,
	// когда новые индексы уже стоят. Выборы могут совпасть: интерфейс
	// пропадал и вернулся (сон, переподключение Wi-Fi), и открытые через
	// него сокеты уже мертвы. Вызовы не перекрываются. Обязателен.
	Changed func(now, before Choice)

	// Settle и MaxWait — окно сворачивания; 0 — DefaultSettle
	// и DefaultMaxWait.
	Settle, MaxWait time.Duration
}

// Watch следит за сменой сети, пока не вызвана stop. Начинает с выбора,
// который поставил SelectInterfaces. stop ждёт конца уже идущего Changed.
func (g *Guard) Watch(opts WatchOptions) (stop func(), err error) {
	return g.watch(opts, subscribe, choose, time.Now)
}

// watch — Watch с подменяемыми подпиской, выбором и часами (тесты).
func (g *Guard) watch(opts WatchOptions, sub func(func(uint64)) (func(), error),
	pick func(uint64) (Choice, error), now func() time.Time) (func(), error) {
	if opts.Settle <= 0 {
		opts.Settle = DefaultSettle
	}
	if opts.MaxWait <= 0 {
		opts.MaxWait = DefaultMaxWait
	}
	w := &watcher{
		guard:   g,
		opts:    opts,
		choose:  pick,
		now:     now,
		settler: settler{settle: opts.Settle, maxWait: opts.MaxWait},
	}
	unsubscribe, err := sub(w.event)
	if err != nil {
		return nil, err
	}
	return func() {
		w.mu.Lock()
		w.stopped = true
		if w.timer != nil {
			w.timer.Stop()
		}
		w.mu.Unlock()
		unsubscribe()
		// Дождаться Changed, который уже идёт.
		w.fireMu.Lock()
		w.fireMu.Unlock() //nolint:staticcheck // пустая критическая секция — ожидание
	}, nil
}

type watcher struct {
	guard  *Guard
	opts   WatchOptions
	choose func(uint64) (Choice, error)
	now    func() time.Time

	mu      sync.Mutex
	settler settler
	timer   *time.Timer // заведён, пока серия не решена
	stopped bool

	// fireMu — решения не перекрываются: следующий таймер может сработать,
	// пока предыдущий Changed ещё идёт.
	fireMu sync.Mutex
}

func (w *watcher) exclude() uint64 {
	if w.opts.Exclude == nil {
		return 0
	}
	return w.opts.Exclude()
}

// event — изменение маршрутов или интерфейса id (0 — интерфейс неизвестен).
//
// Выбор считается и сравнивается под w.mu, как и в fire: иначе событие,
// посчитавшее выбор до того, как fire поставил новый, сравнило бы старое
// с новым и открыло бы лишнюю серию — второй перезапуск на одну смену сети.
func (w *watcher) event(id uint64) {
	exclude := w.exclude()
	if exclude != 0 && id == exclude {
		return
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return
	}
	c, err := w.choose(exclude)
	// Отказ системы отдать таблицу — тоже повод перепроверить позже.
	differs := err != nil || !c.Same(w.guard.Choice())
	w.settler.event(w.now(), differs)
	w.arm()
}

// arm заводит таймер на конец окна, если серия идёт, а таймера нет.
// Зовётся под w.mu.
func (w *watcher) arm() {
	if !w.settler.pending || w.timer != nil {
		return
	}
	w.timer = time.AfterFunc(w.settler.deadline().Sub(w.now()), w.fire)
}

// fire — окно кончилось (или ещё нет, если события продолжали идти).
func (w *watcher) fire() {
	w.fireMu.Lock()
	defer w.fireMu.Unlock()

	w.mu.Lock()
	w.timer = nil
	if w.stopped {
		w.mu.Unlock()
		return
	}
	now := w.now()
	if !w.settler.due(now) {
		w.arm()
		w.mu.Unlock()
		return
	}
	c, err := w.choose(w.exclude())
	if err != nil {
		// Решить не на чем — ещё одно окно.
		w.settler.reset()
		w.settler.event(now, true)
		w.arm()
		w.mu.Unlock()
		return
	}
	w.settler.reset()
	before := w.guard.commit(c)
	w.mu.Unlock()

	w.opts.Changed(c, before)
}

// settler — сворачивание серии событий, без часов: время приходит
// аргументом.
//
// Серию открывает событие, после которого выбор интерфейса отличается от
// поставленного; события, которые выбор не меняют (свои маршруты туннеля,
// чужие маршруты не по умолчанию), серию не открывают. Открытую серию
// продлевает любое событие, решение — после settle тишины, но не позже
// maxWait от начала. Решается серия всегда, даже если к концу выбор
// вернулся к прежнему: интерфейс пропадал, и сокеты через него мертвы.
type settler struct {
	settle, maxWait time.Duration

	pending     bool
	first, last time.Time
}

func (s *settler) event(now time.Time, differs bool) {
	if !s.pending {
		if !differs {
			return
		}
		s.pending, s.first = true, now
	}
	s.last = now
}

// deadline — когда решать открытую серию.
func (s *settler) deadline() time.Time {
	quiet := s.last.Add(s.settle)
	if limit := s.first.Add(s.maxWait); limit.Before(quiet) {
		return limit
	}
	return quiet
}

// due — серия открыта, и пора решать.
func (s *settler) due(now time.Time) bool {
	return s.pending && !now.Before(s.deadline())
}

func (s *settler) reset() {
	s.pending = false
	s.first, s.last = time.Time{}, time.Time{}
}
