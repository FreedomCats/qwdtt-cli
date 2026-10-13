package datapath

import (
	"context"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTun подменяет создание адаптера: вместо драйвера — netstack-TUN,
// который считает Close, и счётчик release. Логика Start и Close от
// настоящего адаптера не зависит, а он требует прав администратора.
type fakeTun struct {
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
	// flushed — сброс адаптера; flushedBeforeTUN — пока TUN ещё открыт
	// (после закрытия адаптера сбрасывать уже нечего).
	flushed          atomic.Int32
	flushedBeforeTUN atomic.Bool
}

func (f *fakeTun) open(name string, conf *WGConfig, logf func(string, ...any)) (*tunAdapter, error) {
	f.opened.Add(1)
	if f.gate != nil {
		<-f.gate
	}
	if f.openErr != nil {
		return nil, f.openErr
	}
	f.tun = newTestTUN(f.t, conf)
	return &tunAdapter{
		tun: f.tun,
		id:  42,
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
		flush: func() error {
			f.flushed.Add(1)
			f.flushedBeforeTUN.Store(f.tun.closed.Load() == 0)
			return nil
		},
		release: func() {
			time.Sleep(f.releaseDelay)
			f.releasedAfterTUN.Store(f.tun.closed.Load() > 0)
			f.released.Add(1)
		},
	}, nil
}

func newTestTun(t *testing.T) (*Tun, *fakeTun, *recorder) {
	t.Helper()
	rec := &recorder{}
	w := NewTun(TunOptions{Logf: rec.logf})
	f := &fakeTun{t: t}
	w.openAdapter = f.open
	return w, f, rec
}

func TestTunRequires(t *testing.T) {
	req := NewTun(TunOptions{}).Requires()
	if !req.Admin || !req.Netguard || len(req.Programs) != 0 || req.Warning != "" {
		t.Errorf("Requires = %+v, ждали Admin и Netguard без программ и предупреждений", req)
	}
	// wintun.dll нужна только на Windows: на Linux TUN даёт ядро.
	wantFiles := runtime.GOOS == "windows"
	if wantFiles && (len(req.Files) != 1 || req.Files[0] != "wintun.dll") || !wantFiles && len(req.Files) != 0 {
		t.Errorf("Files = %v на %s", req.Files, runtime.GOOS)
	}
	supported := runtime.GOOS == "windows" || runtime.GOOS == "linux"
	if (req.Unsupported == nil) != supported {
		t.Errorf("Unsupported = %v на %s: tun есть на Windows и Linux", req.Unsupported, runtime.GOOS)
	}
}

func TestTunCloseWithoutStart(t *testing.T) {
	w, f, _ := newTestTun(t)
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

func TestTunStartClose(t *testing.T) {
	w, f, rec := newTestTun(t)
	conf := testWGConfig(t, "127.0.0.1:9")

	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := w.Endpoint().String(); got != "127.0.0.1:9" {
		t.Errorf("Endpoint = %s", got)
	}
	if w.AdapterID() != 42 {
		t.Errorf("AdapterID = %d, ждали 42", w.AdapterID())
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
	if w.AdapterID() != 0 {
		t.Error("AdapterID после Close не нулевой")
	}
	if err := w.Close(); err != nil || f.released.Load() != 1 {
		t.Errorf("повторный Close: err=%v, release %d раз", err, f.released.Load())
	}
	log := rec.dump()
	for _, want := range []string{`[TUN] адаптер "tt" поднят`, `[TUN] адаптер "tt" снят`} {
		if !strings.Contains(log, want) {
			t.Errorf("в логе нет %q:\n%s", want, log)
		}
	}
}

// Persistent: Suspend снимает маршруты и защиту DNS, адаптер остаётся;
// следующий Start — перенастройка того же адаптера, а не новый.
func TestTunSuspendAndRestart(t *testing.T) {
	w, f, rec := newTestTun(t)
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
	if f.tun.closed.Load() != 0 || w.AdapterID() != 42 {
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
	if log := rec.dump(); !strings.Contains(log, `[TUN] адаптер "tt" перенастроен (endpoint=127.0.0.1:10)`) {
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
func TestTunRestartReconfiguresAdapter(t *testing.T) {
	w, f, _ := newTestTun(t)
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
func TestTunRestartFailureKeepsAdapter(t *testing.T) {
	w, f, _ := newTestTun(t)
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

func TestTunRouteFailureReleases(t *testing.T) {
	w, f, _ := newTestTun(t)
	conf := testWGConfig(t, "127.0.0.1:9")

	f.routesErr = errorString("доступ запрещён")
	err := w.Start(context.Background(), conf)
	if err == nil || !strings.Contains(err.Error(), "tun: маршруты: доступ запрещён") {
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

func TestTunWarnsPartialAllowedIPs(t *testing.T) {
	w, _, rec := newTestTun(t)
	conf := testWGConfig(t, "127.0.0.1:9")
	conf.AllowedIPs = []string{"10.0.0.0/8"}

	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer w.Close()
	want := "[TUN] Внимание: сервер разрешил в туннеле не весь IPv4 (AllowedIPs: 10.0.0.0/8)"
	if log := rec.dump(); !strings.Contains(log, want) {
		t.Errorf("в логе нет %q:\n%s", want, log)
	}
}

func TestTunOpenFailureAllowsRetry(t *testing.T) {
	w, f, _ := newTestTun(t)
	conf := testWGConfig(t, "127.0.0.1:9")

	f.openErr = errorString("драйвер не отвечает")
	err := w.Start(context.Background(), conf)
	if err == nil || !strings.Contains(err.Error(), "tun: драйвер не отвечает") {
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

func TestTunDeviceFailureReleases(t *testing.T) {
	w, f, _ := newTestTun(t)
	conf := testWGConfig(t, "127.0.0.1:9")
	conf.PrivateKeyHex = "не-hex"

	err := w.Start(context.Background(), conf)
	if err == nil || !strings.Contains(err.Error(), "tun: IpcSet") {
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
func TestTunOutlivesStartContext(t *testing.T) {
	w, f, _ := newTestTun(t)
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
func TestTunConcurrentCloseWaits(t *testing.T) {
	w, f, _ := newTestTun(t)
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
func TestTunCloseDuringStart(t *testing.T) {
	w, f, _ := newTestTun(t)
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

// Close сбрасывает адаптер (маршруты, адреса, DNS) до того, как закрыть его,
// как wireguard-windows: иначе после остановки в реестре оставался ключ
// интерфейса с DNS туннеля (R2, сценарий 2).
func TestTunCloseFlushesAdapterFirst(t *testing.T) {
	w, f, _ := newTestTun(t)
	if err := w.Start(context.Background(), testWGConfig(t, "127.0.0.1:9")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if f.flushed.Load() != 0 {
		t.Fatal("адаптер сброшен до Close")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if n := f.flushed.Load(); n != 1 {
		t.Fatalf("сбросов %d, ждали 1", n)
	}
	if !f.flushedBeforeTUN.Load() {
		t.Error("сброс после закрытия TUN — адаптера уже нет")
	}
}
