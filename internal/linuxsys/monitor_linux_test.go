package linuxsys

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeSub — подписка, которой управляет тест: каждая попытка отдаёт новый
// канал, обрыв — закрытие канала, как делает netlink при ENOBUFS.
type fakeSub struct {
	mu       sync.Mutex
	attempts int
	fail     int // столько следующих попыток откажут
	ch       chan int
	ended    int // сколько попыток закрыто через done
}

func (f *fakeSub) subscribe(done <-chan struct{}) (chan int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.fail > 0 {
		f.fail--
		return nil, errors.New("подписка отказала")
	}
	ch := make(chan int, 8)
	f.ch = ch
	go func() {
		<-done
		f.mu.Lock()
		f.ended++
		// Сокет закрыт — netlink закрывает канал. После обрыва он уже закрыт.
		if f.ch == ch {
			close(ch)
			f.ch = nil
		}
		f.mu.Unlock()
	}()
	return ch, nil
}

// send шлёт событие в текущую подписку.
func (f *fakeSub) send(t *testing.T, index int) {
	t.Helper()
	f.mu.Lock()
	ch := f.ch
	f.mu.Unlock()
	if ch == nil {
		t.Fatal("подписки нет")
	}
	ch <- index
}

// breakOff обрывает текущую подписку, как netlink при ошибке приёма.
func (f *fakeSub) breakOff() {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.ch)
	f.ch = nil
}

func (f *fakeSub) state() (attempts, ended int, live bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts, f.ended, f.ch != nil
}

type notes struct {
	mu  sync.Mutex
	got []uint64
}

func (n *notes) add(index uint64) {
	n.mu.Lock()
	n.got = append(n.got, index)
	n.mu.Unlock()
}

func (n *notes) wait(t *testing.T, want ...uint64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		n.mu.Lock()
		got := append([]uint64(nil), n.got...)
		n.mu.Unlock()
		if len(got) >= len(want) {
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("события %v, ждали %v", got, want)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("события %v, ждали %v", got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newWatch(n *notes) *watch {
	w := &watch{stop: make(chan struct{})}
	w.notify = func(index uint64) {
		if !w.stopping.Load() {
			n.add(index)
		}
	}
	return w
}

// Обрыв подписки — новая подписка и notify(0): события за обрыв потеряны,
// выбор надо перепроверить.
func TestFollowResubscribes(t *testing.T) {
	var n notes
	w := newWatch(&n)
	f := &fakeSub{}
	if err := follow(w, f.subscribe, func(i int) int { return i }); err != nil {
		t.Fatal(err)
	}
	f.send(t, 3)
	n.wait(t, 3)

	f.mu.Lock()
	f.fail = 1 // первая переподписка откажет, вторая выйдет
	f.mu.Unlock()
	f.breakOff()
	n.wait(t, 3, 0)
	attempts, ended, live := f.state()
	if attempts != 3 || !live {
		t.Fatalf("попыток %d, подписка жива: %v; ждали 3 и да", attempts, live)
	}
	if ended != 1 {
		t.Errorf("закрыто попыток %d, ждали 1 — оборванную", ended)
	}

	f.send(t, 5)
	n.wait(t, 3, 0, 5)

	w.close()
	if _, ended, live := f.state(); live || ended != 2 {
		t.Errorf("после stop: закрыто %d, жива %v", ended, live)
	}
}

// Первая подписка отказала — ошибка вызывающему, читателя нет.
func TestFollowInitialError(t *testing.T) {
	var n notes
	w := newWatch(&n)
	f := &fakeSub{fail: 1}
	if err := follow(w, f.subscribe, func(i int) int { return i }); err == nil {
		t.Fatal("ошибки нет")
	}
	w.close() // не виснет
}

// stop во время паузы перед переподпиской не ждёт её конца.
func TestFollowStopDuringRetry(t *testing.T) {
	var n notes
	w := newWatch(&n)
	f := &fakeSub{}
	if err := follow(w, f.subscribe, func(i int) int { return i }); err != nil {
		t.Fatal(err)
	}
	f.breakOff()
	start := time.Now()
	w.close()
	if d := time.Since(start); d > resubscribeDelay/2 {
		t.Errorf("stop ждал %v", d)
	}
	if attempts, _, _ := f.state(); attempts != 1 {
		t.Errorf("после stop переподписка всё же была: попыток %d", attempts)
	}
}

// Живая подписка netlink: заводится без прав и снимается без зависания.
func TestWatchNetworkLive(t *testing.T) {
	stop, err := WatchNetwork(func(uint64) {})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stop завис")
	}
	stop() // повторный — безвреден
}
