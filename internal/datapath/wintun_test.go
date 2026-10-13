package datapath

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWintun подменяет создание адаптера: вместо драйвера — netstack-TUN,
// который считает Close, и счётчик release. Логика Start и Close от
// настоящего адаптера не зависит, а он требует прав администратора.
type fakeWintun struct {
	t        *testing.T
	tun      *closeCountingTUN
	opened   atomic.Int32
	released atomic.Int32
	// releasedAfterTUN — release пришёл, когда TUN уже был закрыт: адаптер
	// снят до того, как отпущена блокировка второго запуска.
	releasedAfterTUN atomic.Bool
	openErr          error
	// gate, если задан, держит openAdapter до закрытия канала.
	gate chan struct{}
	// releaseDelay — медленная уборка, чтобы поймать неподождавший Close.
	releaseDelay time.Duration
}

func (f *fakeWintun) open(name string, conf *WGConfig, logf func(string, ...any)) (*wintunAdapter, error) {
	f.opened.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.tun = newTestTUN(f.t, conf)
	return &wintunAdapter{tun: f.tun, luid: 42, release: func() {
		time.Sleep(f.releaseDelay)
		f.releasedAfterTUN.Store(f.tun.closed.Load() > 0)
		f.released.Add(1)
	}}, nil
}

func newTestWintun(t *testing.T) (*Wintun, *fakeWintun, *recorder) {
	t.Helper()
	rec := &recorder{}
	w := NewWintun(WintunOptions{Logf: rec.logf})
	f := &fakeWintun{t: t}
	w.openAdapter = f.open
	return w, f, rec
}

func TestWintunRequires(t *testing.T) {
	req := NewWintun(WintunOptions{}).Requires()
	if !req.Admin || !req.Netguard || len(req.Programs) != 0 || req.Warning != "" {
		t.Errorf("Requires = %+v, ждали Admin и Netguard без программ и предупреждений", req)
	}
	if len(req.Files) != 1 || req.Files[0] != "wintun.dll" {
		t.Errorf("Files = %v, ждали [wintun.dll]", req.Files)
	}
}

func TestWintunCloseWithoutStart(t *testing.T) {
	w, f, _ := newTestWintun(t)
	if err := w.Close(); err != nil {
		t.Fatalf("Close до Start: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("повторный Close: %v", err)
	}
	if err := w.Start(context.Background(), testWGConfig(t, "127.0.0.1:9")); err == nil {
		t.Fatal("Start после Close принят")
	}
	if f.opened.Load() != 0 {
		t.Error("адаптер создавался после Close")
	}
}

func TestWintunStartClose(t *testing.T) {
	w, f, rec := newTestWintun(t)
	conf := testWGConfig(t, "127.0.0.1:9")

	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := w.Endpoint().String(); got != "127.0.0.1:9" {
		t.Errorf("Endpoint = %s", got)
	}
	if w.LUID() != 42 {
		t.Errorf("LUID = %d, ждали 42", w.LUID())
	}
	if err := w.Start(context.Background(), conf); err == nil {
		t.Error("второй Start принят")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if f.tun.closed.Load() == 0 {
		t.Error("TUN не закрыт")
	}
	if f.released.Load() != 1 || !f.releasedAfterTUN.Load() {
		t.Errorf("release: вызовов %d, после TUN %v; ждали один, после закрытия TUN",
			f.released.Load(), f.releasedAfterTUN.Load())
	}
	if w.LUID() != 0 {
		t.Error("LUID после Close не нулевой")
	}
	if err := w.Close(); err != nil || f.released.Load() != 1 {
		t.Errorf("повторный Close: err=%v, release %d раз", err, f.released.Load())
	}
	log := rec.dump()
	for _, want := range []string{`[WINTUN] адаптер "tt" поднят`, `[WINTUN] адаптер "tt" снят`} {
		if !strings.Contains(log, want) {
			t.Errorf("в логе нет %q:\n%s", want, log)
		}
	}
}

func TestWintunOpenFailureAllowsRetry(t *testing.T) {
	w, f, _ := newTestWintun(t)
	conf := testWGConfig(t, "127.0.0.1:9")

	f.openErr = errorString("драйвер не отвечает")
	err := w.Start(context.Background(), conf)
	if err == nil || !strings.Contains(err.Error(), "wintun: драйвер не отвечает") {
		t.Fatalf("Start = %v, ждали ошибку создания адаптера", err)
	}

	f.openErr = nil
	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("повторный Start после отказа: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWintunDeviceFailureReleases(t *testing.T) {
	w, f, _ := newTestWintun(t)
	conf := testWGConfig(t, "127.0.0.1:9")
	conf.PrivateKeyHex = "не-hex"

	err := w.Start(context.Background(), conf)
	if err == nil || !strings.Contains(err.Error(), "wintun: IpcSet") {
		t.Fatalf("Start = %v, ждали отказ IpcSet", err)
	}
	if f.tun.closed.Load() == 0 || f.released.Load() != 1 {
		t.Errorf("после отказа: TUN закрыт %d раз, release %d раз; ждали оба",
			f.tun.closed.Load(), f.released.Load())
	}
	if err := w.Close(); err != nil || f.released.Load() != 1 {
		t.Errorf("Close после отказа: err=%v, release %d раз", err, f.released.Load())
	}
}

func TestWintunStopsOnContext(t *testing.T) {
	w, f, _ := newTestWintun(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx, testWGConfig(t, "127.0.0.1:9")); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for f.released.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("отмена ctx не сняла адаптер")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Контракт Datapath.Close: одновременный вызов возвращается только после
// конца уборки (гонка, найденная в N4 у external).
func TestWintunConcurrentCloseWaits(t *testing.T) {
	w, f, _ := newTestWintun(t)
	f.releaseDelay = 300 * time.Millisecond
	if err := w.Start(context.Background(), testWGConfig(t, "127.0.0.1:9")); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.Close()
			if f.released.Load() != 1 {
				t.Error("Close вернулся до конца уборки")
			}
		}()
	}
	wg.Wait()
}

// Close, пришедший посреди Start, убирать ещё нечего; адаптер, созданный
// после него, Start обязан снять сам.
func TestWintunCloseDuringStart(t *testing.T) {
	w, f, _ := newTestWintun(t)
	f.gate = make(chan struct{})

	errc := make(chan error, 1)
	go func() { errc <- w.Start(context.Background(), testWGConfig(t, "127.0.0.1:9")) }()
	for f.opened.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	close(f.gate)

	if err := <-errc; err == nil {
		t.Fatal("Start после Close вернул успех")
	}
	if f.tun.closed.Load() == 0 || f.released.Load() != 1 {
		t.Errorf("адаптер пережил datapath: TUN закрыт %d раз, release %d раз",
			f.tun.closed.Load(), f.released.Load())
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }
