package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

// fakeFullTunnel — datapath, который забирает весь трафик системы
// (Requires().Netguard), как tun и external. С persistent он ещё и
// datapath.Persistent, как tun.
type fakeFullTunnel struct {
	*fakeDatapath
	suspendErr error

	mu       sync.Mutex
	suspends int
}

func (d *fakeFullTunnel) Requires() datapath.Requirements {
	return datapath.Requirements{Netguard: true}
}

func (d *fakeFullTunnel) suspendCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.suspends
}

// fakePersistent — fakeFullTunnel с Suspend.
type fakePersistent struct{ *fakeFullTunnel }

func (d fakePersistent) Suspend() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.suspends++
	return d.suspendErr
}

// lineRecorder собирает строки Logf.
type lineRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *lineRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

// count — сколько строк содержат sub.
func (r *lineRecorder) count(sub string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

func (r *lineRecorder) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

const (
	lineBypassed = "маршруты сняты на время переподключения"
	lineReturned = "маршруты возвращены"
	lineBlocked  = "трафик системы заблокирован (kill-switch)"
	lineUpAgain  = "туннель снова поднят"
)

// failAfterConnect — ядро, которое доходит до конфига, а потом рвётся:
// попытка поднимает datapath и кончается неудачей.
func failAfterConnect(ctx context.Context, cfg core.Config, sink core.Sink) error {
	connectTo(ctx, sink)
	return errors.New("TURN-relay закрыл соединение")
}

// Долгоживущий datapath построен один раз на весь Run, Start — на каждую
// попытку, Suspend — после каждой неудачной и до задержки, Close — один раз,
// при выходе. Окно без туннеля видно строками (решение 3).
func TestPersistentDatapathLivesWholeRun(t *testing.T) {
	dp := fakePersistent{&fakeFullTunnel{fakeDatapath: newFakeDatapath()}}
	var built int
	lines := &lineRecorder{}

	tun, _ := newTestTunnel(t, Options{
		Hashes: []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) {
			built++
			return dp, nil
		},
		Logf: lines.logf,
	}, failAfterConnect)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Три попытки; на каждом ожидании проверяем, что маршруты уже сняты
	// и datapath не закрыт.
	var waits int
	tun.sleep = func(context.Context, time.Duration) bool {
		waits++
		if got := dp.suspendCount(); got != waits {
			t.Errorf("ожидание %d: Suspend %d раз — маршруты должны сниматься до задержки", waits, got)
		}
		if _, closes := dp.counts(); closes != 0 {
			t.Errorf("ожидание %d: datapath закрыт %d раз между попытками", waits, closes)
		}
		if waits >= 3 {
			cancel()
			return false
		}
		return true
	}

	if err := tun.Run(ctx); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if built != 1 {
		t.Errorf("datapath построен %d раз, ждали один на весь Run", built)
	}
	if starts, closes := dp.counts(); starts != 3 || closes != 1 {
		t.Errorf("datapath: %d Start, %d Close; ждали 3 и 1", starts, closes)
	}
	if got := lines.count(lineBypassed); got != 3 {
		t.Errorf("строк «сняты» %d, ждали 3:\n%s", got, lines.dump())
	}
	if got := lines.count(lineReturned); got != 2 {
		t.Errorf("строк «возвращены» %d, ждали 2 (первый подъём — не возврат):\n%s", got, lines.dump())
	}
}

// Ctrl+C — не переподключение: Suspend не зовётся, строки «сняты» нет,
// datapath закрывается один раз.
func TestPersistentDatapathClosedOnStop(t *testing.T) {
	dp := fakePersistent{&fakeFullTunnel{fakeDatapath: newFakeDatapath()}}
	lines := &lineRecorder{}

	tun, _ := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return dp, nil },
		Logf:        lines.logf,
	}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
		connectTo(ctx, sink)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tun.Run(ctx) }()
	<-dp.started
	waitFor(t, func() bool { return tun.State() == StateConnected },
		func() string { return "состояние " + string(tun.State()) })
	cancel()
	if err := waitErr(t, done); err != nil {
		t.Fatalf("Run = %v", err)
	}

	if starts, closes := dp.counts(); starts != 1 || closes != 1 {
		t.Errorf("datapath: %d Start, %d Close; ждали по одному", starts, closes)
	}
	if dp.suspendCount() != 0 || lines.count(lineBypassed) != 0 {
		t.Errorf("при остановке: Suspend %d раз, строки:\n%s", dp.suspendCount(), lines.dump())
	}
}

// Suspend не удался — маршруты, может быть, стоят на мёртвом туннеле, и
// капча не загрузится. Datapath снимается целиком, следующая попытка
// строит новый.
func TestPersistentSuspendFailureRebuilds(t *testing.T) {
	var built []fakePersistent
	lines := &lineRecorder{}

	tun, _ := newTestTunnel(t, Options{
		Hashes: []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) {
			dp := fakePersistent{&fakeFullTunnel{
				fakeDatapath: newFakeDatapath(),
				suspendErr:   errors.New("доступ запрещён"),
			}}
			built = append(built, dp)
			return dp, nil
		},
		Logf: lines.logf,
	}, failAfterConnect)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits int
	tun.sleep = func(context.Context, time.Duration) bool {
		waits++
		if _, closes := built[len(built)-1].counts(); closes != 1 {
			t.Errorf("ожидание %d: datapath с неснятыми маршрутами не закрыт", waits)
		}
		if waits >= 2 {
			cancel()
			return false
		}
		return true
	}

	if err := tun.Run(ctx); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if len(built) != 2 {
		t.Fatalf("datapath построен %d раз, ждали 2", len(built))
	}
	for i, dp := range built {
		if _, closes := dp.counts(); closes != 1 {
			t.Errorf("datapath %d закрыт %d раз, ждали один", i+1, closes)
		}
	}
	if lines.count("маршруты не снялись: доступ запрещён") != 2 {
		t.Errorf("нет строки об отказе Suspend:\n%s", lines.dump())
	}
}

// Одноразовый datapath, который забирает весь трафик (external), закрывается
// в конце каждой попытки — и окно без туннеля у него то же, и строки те же.
func TestFullTunnelDatapathReportsBypass(t *testing.T) {
	var built []*fakeFullTunnel
	lines := &lineRecorder{}

	tun, _ := newTestTunnel(t, Options{
		Hashes: []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) {
			dp := &fakeFullTunnel{fakeDatapath: newFakeDatapath()}
			built = append(built, dp)
			return dp, nil
		},
		Logf: lines.logf,
	}, failAfterConnect)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits int
	tun.sleep = func(context.Context, time.Duration) bool {
		waits++
		if waits >= 2 {
			cancel()
			return false
		}
		return true
	}

	if err := tun.Run(ctx); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if len(built) != 2 {
		t.Fatalf("datapath построен %d раз, ждали по одному на попытку", len(built))
	}
	for i, dp := range built {
		if starts, closes := dp.counts(); starts != 1 || closes != 1 {
			t.Errorf("datapath %d: %d Start, %d Close; ждали по одному", i+1, starts, closes)
		}
	}
	if lines.count(lineBypassed) != 2 || lines.count(lineReturned) != 1 {
		t.Errorf("строки о маршрутах:\n%s", lines.dump())
	}
}

// socks трафик системы не забирает: строк о маршрутах нет.
func TestSocksDatapathNoBypassLines(t *testing.T) {
	lines := &lineRecorder{}
	tun, _ := newTestTunnel(t, Options{
		Hashes:      []string{"hash-one"},
		NewDatapath: func() (datapath.Datapath, error) { return newFakeDatapath(), nil },
		Logf:        lines.logf,
	}, failAfterConnect)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tun.sleep = func(context.Context, time.Duration) bool { cancel(); return false }

	if err := tun.Run(ctx); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if dump := lines.dump(); dump != "" {
		t.Errorf("строки у socks:\n%s", dump)
	}
}

// С kill-switch окно закрыто: вместо «маршруты сняты…» — «заблокирован», а
// вместо «маршруты возвращены» — «туннель снова поднят». Не встал
// kill-switch (KillSwitch() == false) — прежние строки: они правдивы.
func TestKillSwitchLines(t *testing.T) {
	for _, locked := range []bool{true, false} {
		dp := fakePersistent{&fakeFullTunnel{fakeDatapath: newFakeDatapath()}}
		lines := &lineRecorder{}
		var asked int
		tun, _ := newTestTunnel(t, Options{
			Hashes:      []string{"hash-one"},
			NewDatapath: func() (datapath.Datapath, error) { return dp, nil },
			Logf:        lines.logf,
			KillSwitch: func() bool {
				// Спрашивать — после Suspend: он и ставит «закрыто».
				if dp.suspendCount() == asked {
					t.Error("KillSwitch спрошен до Suspend")
				}
				asked++
				return locked
			},
		}, failAfterConnect)

		ctx, cancel := context.WithCancel(context.Background())
		var waits int
		tun.sleep = func(context.Context, time.Duration) bool {
			waits++
			if waits >= 2 {
				cancel()
				return false
			}
			return true
		}
		if err := tun.Run(ctx); err != nil {
			t.Fatalf("Run = %v", err)
		}
		cancel()

		blocked, upAgain := lines.count(lineBlocked), lines.count(lineUpAgain)
		bypassed, returned := lines.count(lineBypassed), lines.count(lineReturned)
		if locked && (blocked != 2 || upAgain != 1 || bypassed != 0 || returned != 0) ||
			!locked && (blocked != 0 || upAgain != 0 || bypassed != 2 || returned != 1) {
			t.Errorf("kill-switch стоит=%v, строки:\n%s", locked, lines.dump())
		}
	}
}

// fakePiped — fakePersistent с каналом в памяти, как tun: каждая сторона
// ядра, которую он отдал, запоминается.
type fakePiped struct {
	fakePersistent
	pipe  *datapath.Pipe
	conns *[]net.PacketConn
}

func (d fakePiped) CoreConn() net.PacketConn {
	c := d.pipe.CoreConn()
	*d.conns = append(*d.conns, c)
	return c
}

// Datapath с каналом в памяти даёт ядру новую сторону на каждую попытку;
// без канала ядро слушает loopback, как раньше.
func TestPipedDatapathGivesCoreConnEachAttempt(t *testing.T) {
	for _, piped := range []bool{true, false} {
		t.Run(fmt.Sprintf("piped=%v", piped), func(t *testing.T) {
			persistent := fakePersistent{&fakeFullTunnel{fakeDatapath: newFakeDatapath()}}
			var conns []net.PacketConn
			var dp datapath.Datapath = persistent
			if piped {
				dp = fakePiped{fakePersistent: persistent, pipe: datapath.NewPipe(), conns: &conns}
			}

			var seen []net.PacketConn
			tun, _ := newTestTunnel(t, Options{
				Hashes:      []string{"hash-one"},
				NewDatapath: func() (datapath.Datapath, error) { return dp, nil },
			}, func(ctx context.Context, cfg core.Config, sink core.Sink) error {
				seen = append(seen, cfg.PacketConn)
				return failAfterConnect(ctx, cfg, sink)
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var waits int
			tun.sleep = func(context.Context, time.Duration) bool {
				waits++
				if waits >= 2 {
					cancel()
					return false
				}
				return true
			}
			if err := tun.Run(ctx); err != nil {
				t.Fatalf("Run = %v", err)
			}

			if len(seen) != 2 {
				t.Fatalf("ядро запускалось %d раз, ждали 2", len(seen))
			}
			if !piped {
				if seen[0] != nil || seen[1] != nil {
					t.Errorf("без канала ядро получило PacketConn: %v", seen)
				}
				return
			}
			if len(conns) != 2 || seen[0] != conns[0] || seen[1] != conns[1] || seen[0] == seen[1] {
				t.Errorf("ядро получило %v, datapath отдал %v; ждали новую сторону на попытку", seen, conns)
			}
		})
	}
}
