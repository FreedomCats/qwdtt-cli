package netguard

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	ethernet = Choice{Index4: 3, Alias4: "Ethernet 3"}
	wifi     = Choice{Index4: 7, Alias4: "Wi-Fi", Index6: 7, Alias6: "Wi-Fi"}
	offline  = Choice{}
)

func TestSettlerCollapsesSeries(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := settler{settle: 2 * time.Second, maxWait: 10 * time.Second}

	// Переключение Wi-Fi → Ethernet: первое событие меняет выбор, остальные
	// уже нет, но серию продлевают.
	s.event(t0, true)
	for _, d := range []time.Duration{300, 800, 1500} {
		s.event(t0.Add(d*time.Millisecond), false)
	}
	if s.due(t0.Add(3400 * time.Millisecond)) {
		t.Fatal("решение раньше 2 с тишины после последнего события")
	}
	if !s.due(t0.Add(3500 * time.Millisecond)) {
		t.Fatal("нет решения после 2 с тишины")
	}
	s.reset()
	if s.due(t0.Add(time.Hour)) {
		t.Fatal("после решения серия всё ещё открыта")
	}
}

func TestSettlerMaxWait(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := settler{settle: 2 * time.Second, maxWait: 10 * time.Second}
	s.event(t0, true)
	// Сеть шумит каждую секунду без конца.
	for i := 1; i <= 15; i++ {
		s.event(t0.Add(time.Duration(i)*time.Second), false)
		if got, want := s.due(t0.Add(time.Duration(i)*time.Second)), i >= 10; got != want {
			t.Fatalf("на %d с due = %v, ждали %v", i, got, want)
		}
	}
}

func TestSettlerIgnoresUnchanged(t *testing.T) {
	t0 := time.Unix(1000, 0)
	s := settler{settle: 2 * time.Second, maxWait: 10 * time.Second}
	for i := 0; i < 5; i++ {
		s.event(t0.Add(time.Duration(i)*time.Second), false)
	}
	if s.pending || s.due(t0.Add(time.Hour)) {
		t.Fatal("события, не менявшие выбор, открыли серию")
	}
}

// fakeNet — подмена подписки и выбора для watch.
type fakeNet struct {
	mu     sync.Mutex
	choice Choice
	err    error
	notify func(uint64)
	picks  atomic.Int32
	// excluded — что выбор получил последним в exclude.
	excluded atomic.Uint64
}

func (f *fakeNet) set(c Choice) {
	f.mu.Lock()
	f.choice = c
	f.mu.Unlock()
}

func (f *fakeNet) sub(notify func(uint64)) (func(), error) {
	f.notify = notify
	return func() {}, nil
}

func (f *fakeNet) pick(exclude uint64) (Choice, error) {
	f.picks.Add(1)
	f.excluded.Store(exclude)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.choice, f.err
}

type change struct{ now, before Choice }

// startWatch — Guard с выбором start и наблюдатель с коротким окном.
func startWatch(t *testing.T, start Choice, exclude uint64) (*Guard, *fakeNet, <-chan change) {
	t.Helper()
	g := &Guard{}
	g.commit(start)
	f := &fakeNet{choice: start}
	changes := make(chan change, 16)
	stop, err := g.watch(WatchOptions{
		Exclude: func() uint64 { return exclude },
		Changed: func(now, before Choice) { changes <- change{now, before} },
		Settle:  40 * time.Millisecond,
		MaxWait: time.Second,
	}, f.sub, f.pick, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return g, f, changes
}

func expectChange(t *testing.T, changes <-chan change) change {
	t.Helper()
	select {
	case c := <-changes:
		return c
	case <-time.After(2 * time.Second):
		t.Fatal("смены сети не дождались")
		return change{}
	}
}

func expectNoChange(t *testing.T, changes <-chan change) {
	t.Helper()
	select {
	case c := <-changes:
		t.Fatalf("лишняя смена сети: %v (было %v)", c.now, c.before)
	case <-time.After(200 * time.Millisecond):
	}
}

// Серия событий Wi-Fi → Ethernet, с промежуточным «сети нет», даёт ровно
// одну смену сети, и индексы к ней уже стоят.
func TestWatchOneChangePerSeries(t *testing.T) {
	g, f, changes := startWatch(t, wifi, 0)

	f.set(offline)
	for i := 0; i < 5; i++ {
		f.notify(7)
	}
	f.set(ethernet)
	for i := 0; i < 5; i++ {
		f.notify(3)
	}

	c := expectChange(t, changes)
	if !c.now.Same(ethernet) || !c.before.Same(wifi) {
		t.Fatalf("смена %v (было %v), ждали Ethernet после Wi-Fi", c.now, c.before)
	}
	if i4, i6 := g.Interfaces(); i4 != 3 || i6 != 0 {
		t.Fatalf("индексы %d/%d, ждали 3/0", i4, i6)
	}
	expectNoChange(t, changes)

	// Следующая смена — снова одна.
	f.set(wifi)
	f.notify(7)
	f.notify(7)
	if c := expectChange(t, changes); !c.now.Same(wifi) || !c.before.Same(ethernet) {
		t.Fatalf("вторая смена %v (было %v)", c.now, c.before)
	}
	expectNoChange(t, changes)
}

// Свой адаптер: его события пропускаются без выбора, а исключение уходит
// в выбор. События чужих интерфейсов, не менявшие выбор, тоже ничего не дают.
func TestWatchIgnoresOwnAdapter(t *testing.T) {
	_, f, changes := startWatch(t, ethernet, 42)

	for i := 0; i < 10; i++ {
		f.notify(42)
	}
	if n := f.picks.Load(); n != 0 {
		t.Fatalf("события своего адаптера дошли до выбора %d раз", n)
	}
	for i := 0; i < 10; i++ {
		f.notify(9) // чужой маршрут не по умолчанию
	}
	expectNoChange(t, changes)
	if f.picks.Load() == 0 || f.excluded.Load() != 42 {
		t.Fatalf("выбор звался %d раз с exclude %d, ждали с 42", f.picks.Load(), f.excluded.Load())
	}
}

// Интерфейс пропал и вернулся внутри окна (сон, переподключение Wi-Fi): выбор
// тот же, но сокеты через него мертвы — смена всё равно одна.
func TestWatchBlipStillChanges(t *testing.T) {
	_, f, changes := startWatch(t, wifi, 0)

	f.set(offline)
	f.notify(7)
	f.set(wifi)
	f.notify(7)

	c := expectChange(t, changes)
	if !c.now.Same(wifi) || !c.before.Same(wifi) {
		t.Fatalf("смена %v (было %v), ждали Wi-Fi после Wi-Fi", c.now, c.before)
	}
	expectNoChange(t, changes)
}

// Отказ системы отдать таблицу на решении — решение откладывается, а не
// ставит пустой выбор.
func TestWatchChooseErrorRetries(t *testing.T) {
	g, f, changes := startWatch(t, wifi, 0)

	f.mu.Lock()
	f.choice, f.err = ethernet, errors.New("таблица маршрутов недоступна")
	f.mu.Unlock()
	f.notify(3)
	expectNoChange(t, changes)
	if i4, _ := g.Interfaces(); i4 != 7 {
		t.Fatalf("индекс IPv4 %d после отказа выбора, ждали прежний 7", i4)
	}

	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()
	if c := expectChange(t, changes); !c.now.Same(ethernet) {
		t.Fatalf("смена %v, ждали Ethernet", c.now)
	}
}

func TestWatchStop(t *testing.T) {
	g := &Guard{}
	g.commit(wifi)
	f := &fakeNet{choice: wifi}
	var calls atomic.Int32
	stop, err := g.watch(WatchOptions{
		Changed: func(Choice, Choice) { calls.Add(1) },
		Settle:  20 * time.Millisecond,
	}, f.sub, f.pick, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	f.set(ethernet)
	f.notify(3) // серия открыта, таймер заведён
	stop()
	f.notify(3)
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 0 {
		t.Fatalf("Changed после stop: %d раз", n)
	}
}

// NoIPv6: выбор IPv6 отбрасывается и при первом выборе, и при смене сети,
// а смена одного интерфейса IPv6 сменой сети не считается.
func TestNoIPv6DropsIPv6Choice(t *testing.T) {
	g := &Guard{NoIPv6: true}
	g.commit(wifi)
	if i4, i6 := g.Interfaces(); i4 != 7 || i6 != 0 {
		t.Fatalf("индексы %d, %d; ждали 7 и 0", i4, i6)
	}
	if c := g.Choice(); c.Index6 != 0 || c.Alias6 != "" {
		t.Fatalf("в выборе остался IPv6: %+v", c)
	}

	f := &fakeNet{choice: wifi}
	changes := make(chan change, 16)
	stop, err := g.watch(WatchOptions{
		Changed: func(now, before Choice) { changes <- change{now, before} },
		Settle:  40 * time.Millisecond,
		MaxWait: time.Second,
	}, f.sub, f.pick, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	other6 := wifi
	other6.Index6 = 9
	f.set(other6)
	f.notify(9)
	expectNoChange(t, changes)

	f.set(ethernet)
	f.notify(3)
	if c := expectChange(t, changes); c.now.Index4 != 3 || c.now.Index6 != 0 {
		t.Errorf("смена сети: %+v", c.now)
	}
}
