//go:build upstream_repro

// Файл для go_client апстрима, не для этого репозитория: тег сборки прячет его
// от go test ./... и go vet ./... здесь. run.sh передаёт -tags upstream_repro.

package main

// Тесты-воспроизведения для go_client апстрима (SpaceNeuroX/proxy-turn-vk-android).
// Файл кладётся в go_client/ рядом с main.go; сеть и VK не нужны.
//
//	go test -run 'TestRepro' -v .
//
// На коммите fae121e без правок падают все четыре теста; с патчами из этого
// каталога проходят. TestReproStopRightAfterSessionStart — демонстрация гонки:
// он осмыслен только со вставленной задержкой (см. run.sh) и без неё
// пропускается.

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/turn/v5"
)

// ─── 1. Закрытый звонок (error_code=954) ───

// Ошибка «звонок закрыт» любого происхождения классифицируется как dead —
// хеш больше не пробуют. Без правки: статус error.
func TestReproClosedCallIsDead(t *testing.T) {
	closed := &CallUnavailableError{Code: 954, Message: "Invalid join link"}
	cases := map[string]error{
		"как есть":      closed,
		"путь vkcalls":  newVKCallsFailure("step2 calls.getCallPreview", vkCallsFailureCall, closed),
		"обёрнута в %w": fmt.Errorf("креды: %w", closed),
	}
	for name, err := range cases {
		if status, msg := classifyHashCheckError(err); status != "dead" {
			t.Errorf("%s: статус %q (%s), ожидался dead", name, status, msg)
		}
	}
}

// ─── 2. Эстафета групп ───

// Группа, не получившая креды, передаёт эстафету следующей. Без правки
// signalReady не закрывается, и следующая группа ждёт вечно.
func TestReproGroupPassesBatonOnCredsFailure(t *testing.T) {
	// Отменённый контекст: креды не получить, и сеть не нужна.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ready := make(chan struct{})
	var paused int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		WorkerGroup(ctx, 1, 0, &TurnParams{Hashes: []string{"a1b2c3d4e5f6"}}, nil, nil, "9000",
			true, nil, []int{1}, &paused, "device", "password", NewStats(), nil, ready)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("WorkerGroup не вернулась")
	}
	select {
	case <-ready:
	default:
		t.Fatal("эстафета не передана: следующая группа ждала бы вечно")
	}
}

// ─── Стенд: TURN-сервер и заглушка сервера ───
//
// Настоящий TURN-сервер (pion) на 127.0.0.1, за ним — заглушка сервера на
// DTLS, которая отвечает на GETCONF. Креды TURN кладутся прямо в кеш потока
// группы — к VK тест не обращается.

const (
	standUser   = "stand"
	standPass   = "stand-pass"
	standRealm  = "stand"
	standConfig = "[Interface]\nPrivateKey = stand\n"
)

// turnStand — TURN-сервер стенда. С rejectFirst отвечает 486 (Allocation
// Quota Reached) тому клиенту, чей Allocate пришёл первым, и только ему —
// так выглядит исчерпанная квота VK для одного воркера.
type turnStand struct {
	addr        string
	rejectFirst bool
	// quotaDelay — задержка первого ответа 486. Сервер pion обрабатывает
	// запросы по одному, так что задержка держит и остальных — как Allocate
	// к VK, идущий по сети.
	quotaDelay time.Duration

	mu       sync.Mutex
	rejected net.Addr
	quotaHit atomic.Int32
}

func startTURNStand(t *testing.T, rejectFirst bool, quotaDelay time.Duration) *turnStand {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &turnStand{addr: conn.LocalAddr().String(), rejectFirst: rejectFirst, quotaDelay: quotaDelay}
	key := turn.GenerateAuthKey(standUser, standRealm, standPass)
	srv, err := turn.NewServer(turn.ServerConfig{
		Realm: standRealm,
		AuthHandler: func(ra *turn.RequestAttributes) (string, []byte, bool) {
			return ra.Username, key, ra.Username == standUser
		},
		QuotaHandler: s.quota,
		PacketConnConfigs: []turn.PacketConnConfig{{
			PacketConn: conn,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{
				RelayAddress: net.IPv4(127, 0, 0, 1),
				Address:      "127.0.0.1",
			},
		}},
	})
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return s
}

func (s *turnStand) quota(_, _ string, src net.Addr) bool {
	if !s.rejectFirst {
		return true
	}
	s.mu.Lock()
	if s.rejected == nil {
		s.rejected = src
	}
	reject := s.rejected.String() == src.String()
	s.mu.Unlock()
	if !reject {
		return true
	}
	if s.quotaHit.Add(1) == 1 {
		time.Sleep(s.quotaDelay)
	}
	return false
}

// startServerStand поднимает заглушку сервера: DTLS, на GETCONF — standConfig
// (или молчание, если answer == false). Возвращает адрес и счётчик GETCONF.
func startServerStand(t *testing.T, answer bool) (*net.UDPAddr, *atomic.Int32) {
	t.Helper()
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := dtls.Listen("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, &dtls.Config{
		Certificates:          []tls.Certificate{cert},
		ExtendedMasterSecret:  dtls.RequireExtendedMasterSecret,
		CipherSuites:          []dtls.CipherSuiteID{dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		ConnectionIDGenerator: dtls.RandomCIDGenerator(8),
	})
	if err != nil {
		t.Fatal(err)
	}
	var getconf atomic.Int32
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	// Listener pion не закрывает принятые соединения — закрываем сами.
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		for _, c := range conns {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if !strings.HasPrefix(string(buf[:n]), "GETCONF:") {
						continue
					}
					getconf.Add(1)
					if !answer {
						continue
					}
					if _, err := c.Write([]byte(standConfig)); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.UDPAddr), &getconf
}

// standGroup — группа воркеров, запущенная на стенде.
type standGroup struct {
	configCh chan string
	stats    *Stats
	cancel   context.CancelFunc
	done     chan struct{}
}

func runStandGroup(t *testing.T, stand *turnStand, peer *net.UDPAddr, workers int) *standGroup {
	t.Helper()
	const groupID = 7
	const hash = "stand0hash"
	cache := getStreamCache(groupID * 100)
	cache.mutex.Lock()
	cache.creds = TurnCredentials{
		Username:    standUser,
		Password:    standPass,
		ServerAddrs: []string{stand.addr},
		ExpiresAt:   time.Now().Add(time.Hour),
		Link:        hash,
	}
	cache.mutex.Unlock()

	local, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &standGroup{configCh: make(chan string, 1), stats: NewStats(), cancel: cancel, done: make(chan struct{})}
	d := NewDispatcher(ctx, local, g.stats)
	t.Cleanup(func() {
		cancel()
		select {
		case <-g.done:
		case <-time.After(5 * time.Second):
			// Застрявший воркер (5б без правки) ждал бы 30 минут — не ждём,
			// его горутины доживут до конца прогона.
			t.Log("WorkerGroup так и не вернулась")
		}
		local.Close() // readLoop диспетчера стоит в чтении local
		d.Shutdown()
		cache.invalidate(groupID * 100)
	})

	ids := make([]int, workers)
	for i := range ids {
		ids[i] = i + 1
	}
	var paused int32
	go func() {
		defer close(g.done)
		WorkerGroup(ctx, groupID, 0, &TurnParams{Hashes: []string{hash}}, peer, d, "9000",
			true, g.configCh, ids, &paused, "device", "password", g.stats, nil, nil)
	}()
	return g
}

// stop отменяет группу и ждёт её не дольше limit.
func (g *standGroup) stop(t *testing.T, limit time.Duration) {
	t.Helper()
	g.cancel()
	start := time.Now()
	select {
	case <-g.done:
	case <-time.After(limit):
		t.Errorf("WorkerGroup не вернулась за %v после отмены", limit)
		select {
		case <-g.done:
			t.Logf("вернулась через %v", time.Since(start).Truncate(time.Millisecond))
		case <-time.After(60 * time.Second):
			t.Fatalf("и через %v не вернулась — дальше не ждём", time.Since(start).Truncate(time.Second))
		}
	}
}

// ─── 3. Конфиг при исчерпанной квоте TURN ───

// Первый воркер упирается в квоту (486), остальные получают relay и DTLS.
// Конфиг должен прийти через их сеансы. Без правки его просит только
// воркер с флагом configRequestInFlight — тот, что получил 486, — и
// туннель ждёт его повтора, 30–60 с.
func TestReproConfigDespiteQuota(t *testing.T) {
	stand := startTURNStand(t, true, 1500*time.Millisecond)
	peer, getconf := startServerStand(t, true)
	g := runStandGroup(t, stand, peer, 4)

	select {
	case conf := <-g.configCh:
		if conf != standConfig {
			t.Errorf("конфиг %q, ждали %q", conf, standConfig)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("конфиг не пришёл за 15 с: 486 отдано %d раз, GETCONF %d, сеансов %d",
			stand.quotaHit.Load(), getconf.Load(), g.stats.ActiveConnections.Load())
	}
	if stand.quotaHit.Load() == 0 {
		t.Fatal("486 не отдано ни разу — стенд не проверил квоту")
	}
	g.stop(t, 10*time.Second)
}

// ─── 5а. Остановка во время запроса конфига ───

// Сервер не отвечает на GETCONF; остановка приходит, пока воркер ждёт
// ответа. Без правки воркер ждёт до 45 с (дедлайн RequestConfig).
func TestReproStopDuringConfigRequest(t *testing.T) {
	stand := startTURNStand(t, false, 0)
	peer, getconf := startServerStand(t, false)
	g := runStandGroup(t, stand, peer, 1)

	deadline := time.Now().Add(15 * time.Second)
	for getconf.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("GETCONF не дошёл до заглушки за 15 с")
		}
		time.Sleep(10 * time.Millisecond)
	}
	g.stop(t, 5*time.Second)
}

// ─── 5б. Остановка сразу после начала сеанса ───

// Остановка приходит, когда сеанс уже начался, а чтение ещё не поставило
// свой дедлайн. Без правки этот дедлайн (+30 мин) затирает дедлайн
// «сейчас» от stopConn, и Read ждёт 30 минут. Окно гонки — микросекунды;
// run.sh расширяет его задержкой 300 мс перед SetReadDeadline в чтении
// сеанса (REPRO_INJECT_DELAY=1), без неё тест не показателен.
func TestReproStopRightAfterSessionStart(t *testing.T) {
	if os.Getenv("REPRO_INJECT_DELAY") != "1" {
		t.Skip("нужна вставленная задержка: см. run.sh")
	}
	stand := startTURNStand(t, false, 0)
	peer, _ := startServerStand(t, true)
	g := runStandGroup(t, stand, peer, 1)

	select {
	case <-g.configCh:
	case <-time.After(15 * time.Second):
		t.Fatal("конфиг не пришёл за 15 с")
	}
	// Сеанс доходит до [READY] и запускает чтение; оно спит 300 мс перед
	// SetReadDeadline. Остановка — посреди этого сна.
	time.Sleep(100 * time.Millisecond)
	g.stop(t, 5*time.Second)
}

// Optional lookup keeps the entire tagged file buildable on the unpatched pin.
func reproDropCounter(t *testing.T, stats *Stats, name string, required bool) *atomic.Uint64 {
	t.Helper()
	field := reflect.ValueOf(stats).Elem().FieldByName(name)
	if !field.IsValid() {
		if required {
			t.Fatalf("missing drop counter: Stats.%s", name)
		}
		return nil
	}
	if !field.CanAddr() || !field.Addr().CanInterface() {
		t.Fatalf("drop counter is not addressable: Stats.%s", name)
	}
	counter, ok := field.Addr().Interface().(*atomic.Uint64)
	if !ok {
		t.Fatalf("wrong drop counter type: Stats.%s is %s, want atomic.Uint64", name, field.Type())
	}
	return counter
}

func reproWait(t *testing.T, description string, ready func() bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !ready() {
		select {
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", description)
		case <-tick.C:
		}
	}
}

// A new ReadFrom call acknowledges completion of the preceding dispatch, even
// when that packet had no receiver. No socket flood or timing inference is used.
type reproPacketConn struct {
	packets chan []byte
	reads   chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (c *reproPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case c.reads <- struct{}{}:
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
	select {
	case packet := <-c.packets:
		return copy(b, packet), c.LocalAddr(), nil
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *reproPacketConn) WriteTo([]byte, net.Addr) (int, error) {
	return 0, fmt.Errorf("unexpected dispatcher write")
}
func (c *reproPacketConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}
func (c *reproPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}
}
func (c *reproPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *reproPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *reproPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestReproDroppedUp(t *testing.T) {
	stats := NewStats()
	up := reproDropCounter(t, stats, "DroppedUp", false)
	down := reproDropCounter(t, stats, "DroppedDown", false)
	assertUp := func(want uint64) {
		t.Helper()
		if up != nil && up.Load() != want {
			t.Fatalf("DroppedUp = %d at read barrier, want %d", up.Load(), want)
		}
	}
	conn := &reproPacketConn{packets: make(chan []byte), reads: make(chan struct{}), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	d := NewDispatcher(ctx, conn, stats)
	t.Cleanup(func() {
		cancel()
		conn.Close() // Cancellation alone cannot unblock ReadFrom.
		done := make(chan struct{})
		go func() { d.Shutdown(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("dispatcher did not shut down")
		}
	})
	waitRead := func() {
		t.Helper()
		select {
		case <-conn.reads:
		case <-time.After(5 * time.Second):
			t.Fatal("dispatcher did not reach the read barrier")
		}
	}
	waitRead()
	send := func(packet []byte) {
		t.Helper()
		select {
		case conn.packets <- packet:
		case <-time.After(5 * time.Second):
			t.Fatal("dispatcher did not accept the controlled packet")
		}
		waitRead()
	}
	large, priority := bytes.Repeat([]byte{0x42}, prioThreshold+100), []byte("priority")
	send(large) // No workers is not queue saturation.
	assertUp(0)
	slot := func(id int) *WorkerSlot {
		return &WorkerSlot{ID: id, SendCh: make(chan []byte, 1), PrioCh: make(chan []byte, 1)}
	}
	a, b := slot(1), slot(2)
	d.Register(a)
	receive := func(ch chan []byte, want []byte) {
		t.Helper()
		select {
		case got := <-ch:
			if !bytes.Equal(got, want) {
				t.Errorf("delivered packet = %x, want %x", got, want)
			}
			putPktBuf(got)
		default:
			t.Fatal("packet was not delivered before the read barrier")
		}
	}
	send(large)
	receive(a.SendCh, large)
	assertUp(0)
	send(priority)
	receive(a.PrioCh, priority)
	assertUp(0)
	a.PrioCh <- []byte("full")
	send(priority) // Priority queues full, ordinary queue free.
	receive(a.SendCh, priority)
	assertUp(0)
	a.SendCh <- []byte("full")
	d.Register(b)
	// Reset scheduling only while readLoop is at its barrier, so these packets
	// must try the full worker before falling back to the other worker.
	preferA := func() {
		d.mu.Lock()
		d.rrIndex, d.rrCount, d.lastPktTime, d.chunkStartTs = 0, 0, 0, 0
		d.mu.Unlock()
	}
	preferA()
	send(large)
	receive(b.SendCh, large)
	assertUp(0)
	preferA()
	send(priority)
	receive(b.PrioCh, priority)
	assertUp(0)
	b.PrioCh <- []byte("full")
	preferA()
	send(priority) // Both priority queues full; ordinary fallback still works.
	receive(b.SendCh, priority)
	assertUp(0)
	b.SendCh <- []byte("full")
	const largeBatch, priorityBatch = 3, 2
	for i := 0; i < largeBatch; i++ {
		send(large)
		assertUp(uint64(i + 1))
	}
	for i := 0; i < priorityBatch; i++ {
		send(priority)
		assertUp(uint64(largeBatch + i + 1))
	}
	d.Unregister(a)
	d.Unregister(b)
	send(priority)
	if up == nil {
		reproDropCounter(t, stats, "DroppedUp", true)
	} else if got := up.Load(); got != largeBatch+priorityBatch {
		t.Errorf("DroppedUp = %d, want %d saturated packets only", got, largeBatch+priorityBatch)
	}
	if down == nil {
		reproDropCounter(t, stats, "DroppedDown", true)
	} else if got := down.Load(); got != 0 {
		t.Errorf("DroppedDown = %d during uplink, want 0", got)
	}
}

// This separate single-session helper leaves the legacy stands unchanged. It
// completes the handshake/config exchange and acknowledges a real writer packet
// before handing the connection to the test for controlled downlink writes.
type reproDownlinkServer struct {
	peer    *net.UDPAddr
	ln      net.Listener
	ready   chan net.Conn
	done    chan struct{}
	err     chan error
	mu      sync.Mutex
	conn    net.Conn
	stopped bool
	once    sync.Once
}

func (s *reproDownlinkServer) stop() {
	s.once.Do(func() {
		s.mu.Lock()
		s.stopped = true
		conn := s.conn
		s.mu.Unlock()
		_ = s.ln.Close()
		if conn != nil {
			_ = conn.Close()
		}
	})
}

func startReproDownlinkServer(t *testing.T) *reproDownlinkServer {
	t.Helper()
	cert, err := selfsign.GenerateSelfSigned()
	if err != nil {
		t.Fatalf("downlink certificate setup: %v", err)
	}
	ln, err := dtls.Listen("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}, &dtls.Config{
		Certificates:          []tls.Certificate{cert},
		ExtendedMasterSecret:  dtls.RequireExtendedMasterSecret,
		CipherSuites:          []dtls.CipherSuiteID{dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		ConnectionIDGenerator: dtls.RandomCIDGenerator(8),
	})
	if err != nil {
		t.Fatalf("downlink listener setup: %v", err)
	}
	s := &reproDownlinkServer{peer: ln.Addr().(*net.UDPAddr), ln: ln, ready: make(chan net.Conn, 1), done: make(chan struct{}), err: make(chan error, 1)}
	t.Cleanup(func() {
		s.stop()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Error("downlink server did not stop")
		}
	})
	go func() {
		defer close(s.done)
		serve := func() error {
			c, err := ln.Accept()
			if err != nil {
				return err
			}
			s.mu.Lock()
			if s.stopped {
				s.mu.Unlock()
				c.Close()
				return net.ErrClosed
			}
			s.conn = c
			s.mu.Unlock()
			hctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err = c.(*dtls.Conn).HandshakeContext(hctx)
			cancel()
			if err != nil {
				return fmt.Errorf("handshake: %w", err)
			}
			if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				return err
			}
			buf := make([]byte, 4096)
			n, err := c.Read(buf)
			if err != nil {
				return fmt.Errorf("config request: %w", err)
			}
			if string(buf[:n]) != "GETCONF:9000|device|password" {
				return fmt.Errorf("unexpected config request: %q", buf[:n])
			}
			if _, err := c.Write([]byte(standConfig)); err != nil {
				return fmt.Errorf("config response: %w", err)
			}
			n, err = c.Read(buf)
			if err != nil {
				return fmt.Errorf("writer readiness: %w", err)
			}
			if string(buf[:n]) != "writer-ready" {
				return fmt.Errorf("unexpected writer readiness: %q", buf[:n])
			}
			if err := c.SetDeadline(time.Time{}); err != nil {
				return fmt.Errorf("clear server deadline: %w", err)
			}
			s.ready <- c
			return nil
		}
		if err := serve(); err != nil {
			s.err <- err
		}
	}()
	return s
}

func TestReproDroppedDown(t *testing.T) {
	stand := startTURNStand(t, false, 0)
	server := startReproDownlinkServer(t)
	stats := NewStats()
	ctx, cancel := context.WithCancel(context.Background())
	// No dispatcher loops: only this test can free the prefilled return queue.
	d := &Dispatcher{ReturnCh: make(chan []byte, 1), ctx: ctx, cancel: cancel, stats: stats}
	d.ReturnCh <- []byte("full")
	configCh := make(chan string, 1)
	done := make(chan struct{})
	var delivered bool
	var sessionErr error
	go func() {
		defer close(done)
		delivered, sessionErr = RunSession(ctx, &TurnParams{}, server.peer, d, "9000", true, configCh, 1,
			&Credentials{User: standUser, Pass: standPass, TurnURLs: []string{stand.addr}}, "device", "password", stats, nil)
	}()
	t.Cleanup(func() {
		// Close the peer before cancellation: the transport close unblocks Read
		// independently of the baseline's separate read-deadline cancellation bug.
		server.stop()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("downlink session did not stop after peer close")
		}
	})
	select {
	case conf := <-configCh:
		if conf != standConfig {
			t.Fatalf("downlink config = %q, want synthetic config", conf)
		}
	case err := <-server.err:
		t.Fatalf("downlink server setup: %v", err)
	case <-done:
		t.Fatalf("downlink session ended during setup: %v", sessionErr)
	case <-time.After(8 * time.Second):
		t.Fatal("downlink config setup timed out")
	}
	var worker *WorkerSlot
	reproWait(t, "downlink worker registration", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		if len(d.workers) == 1 {
			worker = d.workers[0]
		}
		return worker != nil
	})
	worker.PrioCh <- []byte("writer-ready")
	var conn net.Conn
	select {
	case conn = <-server.ready:
	case err := <-server.err:
		t.Fatalf("downlink server readiness: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("downlink writer readiness timed out")
	}
	write := func(packet []byte) {
		t.Helper()
		if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatalf("downlink write deadline: %v", err)
		}
		if n, err := conn.Write(packet); err != nil || n != len(packet) {
			t.Fatalf("downlink transport write = %d, %v", n, err)
		}
	}
	receive := func(want []byte) {
		t.Helper()
		select {
		case packet := <-d.ReturnCh:
			if !bytes.Equal(packet, want) {
				t.Errorf("downlink delivery = %q, want %q", packet, want)
			}
			putPktBuf(packet)
		case <-done:
			t.Fatalf("downlink session ended before delivery: %v", sessionErr)
		case <-time.After(5 * time.Second):
			t.Fatal("downlink delivery timed out")
		}
	}
	<-d.ReturnCh
	write([]byte("reader-ready"))
	receive([]byte("reader-ready"))
	d.ReturnCh <- []byte("full")
	// A missing field is asserted only after transport/config and both proxy
	// directions have actually worked; setup errors cannot reproduce this bug.
	down := reproDropCounter(t, stats, "DroppedDown", true)
	up := reproDropCounter(t, stats, "DroppedUp", true)
	if down.Load() != 0 || up.Load() != 0 {
		t.Fatal("drop counters changed during successful session setup")
	}
	write([]byte{keepaliveByte}) // A filtered pong is not a rejected payload.
	const batch = 4
	for i := 0; i < batch; i++ {
		write([]byte(fmt.Sprintf("dropped-%d", i)))
		want := uint64(i + 1)
		reproWait(t, "saturated downlink packet processing", func() bool { return down.Load() >= want })
		if got := down.Load(); got != want {
			t.Fatalf("DroppedDown = %d, want exactly %d", got, want)
		}
	}
	<-d.ReturnCh
	write([]byte("delivered-after-full"))
	receive([]byte("delivered-after-full"))
	if got := down.Load(); got != batch {
		t.Errorf("DroppedDown = %d after successful delivery, want %d", got, batch)
	}
	if got := up.Load(); got != 0 {
		t.Errorf("DroppedUp = %d during downlink, want 0", got)
	}
	conn.Close()
	select {
	case <-done:
		if !delivered {
			t.Errorf("downlink session lost config delivery: %v", sessionErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("downlink session did not finish after controlled peer close")
	}
}

type reproStatsCapture struct {
	mu   sync.Mutex
	line chan string
}

func (c *reproStatsCapture) Write(packet []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Contains(string(packet), "[СТАТИСТИКА]") {
		select {
		case c.line <- strings.TrimSpace(string(packet)):
		default:
		}
	}
	return len(packet), nil
}

func TestReproDroppedStatsOutput(t *testing.T) {
	stats := NewStats()
	stats.ActiveConnections.Store(3)
	stats.TotalBytesUp.Store(2 * 1024 * 1024)
	stats.TotalBytesDown.Store(5 * 1024 * 1024)
	for name, value := range map[string]uint64{"DroppedUp": 7, "DroppedDown": 11} {
		if counter := reproDropCounter(t, stats, name, false); counter != nil {
			counter.Store(value)
		}
	}
	capture := &reproStatsCapture{line: make(chan string, 1)}
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(capture)
	log.SetFlags(0)
	log.SetPrefix("")
	shutdown, done := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		close(shutdown)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("statistics loop did not stop")
		}
		log.SetOutput(writer)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	})
	go func() { defer close(done); stats.RunLoop(shutdown) }()
	select {
	case line := <-capture.line:
		t.Logf("statistics output: %s", line)
		if !strings.Contains(line, "[СТАТИСТИКА] Активных: 3 | Трафик: 7.00 МБ | ↓5.00 МБ / ↑2.00 МБ") {
			t.Error("statistics output lost active-worker or traffic data")
		}
		if !strings.Contains(line, " | Dropped: ↑7 / ↓11") {
			t.Error("statistics output omits drop counters: want DroppedUp=7 and DroppedDown=11")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("normal statistics ticker did not produce output")
	}
}
