package datapath

import (
	"context"
	"net/netip"
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
	// routed — стоят ли маршруты; routesErr — отказ их постановки.
	routed    atomic.Bool
	routesErr error
	// configured — сколько раз адаптер перенастраивался (без создания).
	configured atomic.Int32
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
	return &wintunAdapter{
		tun:  f.tun,
		luid: 42,
		configure: func(*WGConfig) error {
			f.configured.Add(1)
			return nil
		},
		addRoutes: func() error {
			if f.routesErr != nil {
				return f.routesErr
			}
			f.routed.Store(true)
			return nil
		},
		removeRoutes: func() error {
			f.routed.Store(false)
			return nil
		},
		release: func() {
			time.Sleep(f.releaseDelay)
			f.releasedAfterTUN.Store(f.tun.closed.Load() > 0)
			f.released.Add(1)
		},
	}, nil
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

// Persistent: Suspend снимает маршруты и защиту DNS, адаптер остаётся;
// следующий Start — перенастройка того же адаптера, а не новый.
func TestWintunSuspendAndRestart(t *testing.T) {
	w, f, rec := newTestWintun(t)
	guard := &fakeDNSGuard{}
	w.opts.DNSGuard = guard
	if err := w.Suspend(); err != nil {
		t.Errorf("Suspend до Start: %v", err)
	}

	if err := w.Start(context.Background(), testWGConfig(t, "127.0.0.1:9")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !f.routed.Load() {
		t.Fatal("Start не поставил маршруты")
	}

	for range 2 { // повторный Suspend безвреден
		if err := w.Suspend(); err != nil {
			t.Fatalf("Suspend: %v", err)
		}
	}
	if on, _, _, _ := guard.state(); f.routed.Load() || on {
		t.Errorf("после Suspend: маршруты стоят=%v, защита DNS=%v", f.routed.Load(), on)
	}
	if f.tun.closed.Load() != 0 || w.LUID() != 42 {
		t.Fatal("Suspend снял адаптер")
	}

	next := testWGConfig(t, "127.0.0.1:10")
	if err := w.Start(context.Background(), next); err != nil {
		t.Fatalf("Start после Suspend: %v", err)
	}
	if !f.routed.Load() {
		t.Error("перенастройка не вернула маршруты")
	}
	if on, _, _, _ := guard.state(); !on {
		t.Error("перенастройка не вернула защиту DNS")
	}
	if f.opened.Load() != 1 || f.tun.closed.Load() != 0 {
		t.Errorf("адаптер создавался %d раз, TUN закрыт %d раз; ждали один и ни разу",
			f.opened.Load(), f.tun.closed.Load())
	}
	if f.configured.Load() != 0 {
		t.Error("адаптер перенастроен, хотя адрес, MTU и DNS те же")
	}
	if got := w.Endpoint().String(); got != "127.0.0.1:10" {
		t.Errorf("Endpoint после перенастройки = %s", got)
	}
	if log := rec.dump(); !strings.Contains(log, `[WINTUN] адаптер "tt" перенастроен (endpoint=127.0.0.1:10)`) {
		t.Errorf("в логе нет строки о перенастройке:\n%s", log)
	}

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if f.released.Load() != 1 {
		t.Errorf("release %d раз, ждали один", f.released.Load())
	}
	if err := w.Suspend(); err != nil {
		t.Errorf("Suspend после Close: %v", err)
	}
}

// Сервер выдал на новой попытке другой адрес — адаптер перенастраивается.
func TestWintunRestartReconfiguresAdapter(t *testing.T) {
	w, f, _ := newTestWintun(t)
	if err := w.Start(context.Background(), testWGConfig(t, "127.0.0.1:9")); err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_ = w.Suspend()

	next := testWGConfig(t, "127.0.0.1:9")
	next.Addresses = []netip.Addr{netip.MustParseAddr("10.7.0.3")}
	if err := w.Start(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if f.configured.Load() != 1 {
		t.Errorf("адаптер перенастроен %d раз, ждали один", f.configured.Load())
	}
}

// Отказ перенастройки — отказ попытки, но адаптер остаётся, маршрутов нет,
// и следующий Start работает.
func TestWintunRestartFailureKeepsAdapter(t *testing.T) {
	w, f, _ := newTestWintun(t)
	conf := testWGConfig(t, "127.0.0.1:9")
	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_ = w.Suspend()

	f.routesErr = errorString("доступ запрещён")
	if err := w.Start(context.Background(), conf); err == nil {
		t.Fatal("перенастройка с отказом маршрутов принята")
	}
	if f.tun.closed.Load() != 0 || f.released.Load() != 0 {
		t.Error("отказ перенастройки снял адаптер")
	}

	f.routesErr = nil
	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start после отказа: %v", err)
	}
	if !f.routed.Load() || f.opened.Load() != 1 {
		t.Errorf("маршруты стоят=%v, адаптер создавался %d раз", f.routed.Load(), f.opened.Load())
	}
}

func TestWintunRouteFailureReleases(t *testing.T) {
	w, f, _ := newTestWintun(t)
	conf := testWGConfig(t, "127.0.0.1:9")

	f.routesErr = errorString("доступ запрещён")
	err := w.Start(context.Background(), conf)
	if err == nil || !strings.Contains(err.Error(), "wintun: маршруты: доступ запрещён") {
		t.Fatalf("Start = %v, ждали отказ маршрутов", err)
	}
	if f.tun.closed.Load() == 0 || f.released.Load() != 1 {
		t.Errorf("после отказа: TUN закрыт %d раз, release %d раз; ждали оба",
			f.tun.closed.Load(), f.released.Load())
	}

	f.routesErr = nil
	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("повторный Start после отказа: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWintunWarnsPartialAllowedIPs(t *testing.T) {
	w, _, rec := newTestWintun(t)
	conf := testWGConfig(t, "127.0.0.1:9")
	conf.AllowedIPs = []string{"10.0.0.0/8"}

	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer w.Close()
	want := "[WINTUN] Внимание: сервер разрешил в туннеле не весь IPv4 (AllowedIPs: 10.0.0.0/8)"
	if log := rec.dump(); !strings.Contains(log, want) {
		t.Errorf("в логе нет %q:\n%s", want, log)
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

// Адаптер переживает попытку: отмена ctx, с которым звали Start, его не
// снимает (Persistent) — снимает только Close.
func TestWintunOutlivesStartContext(t *testing.T) {
	w, f, _ := newTestWintun(t)
	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx, testWGConfig(t, "127.0.0.1:9")); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(50 * time.Millisecond)
	if f.released.Load() != 0 || f.tun.closed.Load() != 0 {
		t.Fatal("отмена ctx попытки сняла адаптер")
	}
	if err := w.Close(); err != nil || f.released.Load() != 1 {
		t.Errorf("Close: err=%v, release %d раз", err, f.released.Load())
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
