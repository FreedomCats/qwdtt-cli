package core

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/selfsign"
	"github.com/pion/turn/v5"
)

// Стенд группы воркеров: настоящий TURN-сервер (pion) на loopback, заглушка
// сервера qWDTT за ним и креды в обход VK. Сеть не нужна.

const (
	standUser  = "stand"
	standPass  = "stand-pass"
	standRealm = "stand"
	// standConfig — ответ заглушки на GETCONF.
	standConfig = "[Interface]\nPrivateKey = stand\n"
)

// turnStand — TURN-сервер стенда.
type turnStand struct {
	addr string
	// rejectFirst — отвечать 486 (Allocation Quota Reached) тому клиенту,
	// чей Allocate пришёл первым, и только ему.
	rejectFirst bool
	// quotaDelay — на сколько задержать первый ответ 486. Сервер pion
	// обрабатывает запросы по одному, так что задержка держит и остальных.
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
	// Повторы того же Allocate (клиент pion переспрашивает, пока ждёт)
	// получают отказ сразу.
	if s.quotaHit.Add(1) == 1 {
		time.Sleep(s.quotaDelay)
	}
	return false
}

// startQWDTTStand поднимает заглушку сервера qWDTT: DTLS, на GETCONF —
// standConfig (или молчание, если answer == false), AUTH и keepalive молча
// принимает. Возвращает адрес и счётчик GETCONF.
func startQWDTTStand(t *testing.T, answer bool) (*net.UDPAddr, *atomic.Int32) {
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

// runStandGroup запускает WorkerGroup с workers воркерами против стенда:
// креды лежат в кеше потока группы, VK не спрашивается. Уборка — t.Cleanup.
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
		<-g.done
		// readLoop диспетчера стоит в чтении local: сперва закрыть его.
		local.Close()
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
		<-g.done
		t.Logf("вернулась через %v", time.Since(start).Truncate(time.Millisecond))
	}
}

// Первый воркер группы упирается в квоту TURN (486), остальные получают relay
// и DTLS — конфиг приходит через их сеансы, не дожидаясь, пока квота
// освободится. В апстриме конфиг просил один воркер (флаг
// configRequestInFlight), и если это был воркер с 486, остальные, войдя в
// сеанс, конфиг уже не просили: туннель ждал его повтора, 30–60 с.
func TestWorkerGroupConfigDespiteQuota(t *testing.T) {
	rec := install(t)

	// Ответ 486 придёт позже, чем остальные воркеры войдут в цикл (75 мс
	// между стартами), — как при квоте у VK, где Allocate ходит по сети.
	stand := startTURNStand(t, true, 1500*time.Millisecond)
	peer, getconf := startQWDTTStand(t, true)
	g := runStandGroup(t, stand, peer, 4)

	select {
	case conf := <-g.configCh:
		if conf != standConfig {
			t.Errorf("конфиг %q, ждали %q", conf, standConfig)
		}
	case <-time.After(15 * time.Second):
		dumpLog(t, rec)
		t.Fatalf("конфиг не пришёл за 15 с: 486 отдано %d раз, GETCONF %d, сеансов %d",
			stand.quotaHit.Load(), getconf.Load(), g.stats.ActiveConnections.Load())
	}
	if stand.quotaHit.Load() == 0 {
		dumpLog(t, rec)
		t.Fatal("486 не отдано ни разу — стенд не проверил квоту")
	}
	g.stop(t, 10*time.Second)
}

// Остановка, пришедшая, пока воркер ждёт ответа на GETCONF, не ждёт этого
// ответа: в апстриме RequestConfig держал воркер до 45 с, и Run — вместе
// с ним.
func TestWorkerStopsDuringConfigRequest(t *testing.T) {
	rec := install(t)

	stand := startTURNStand(t, false, 0)
	peer, getconf := startQWDTTStand(t, false)
	g := runStandGroup(t, stand, peer, 1)

	deadline := time.Now().Add(15 * time.Second)
	for getconf.Load() == 0 {
		if time.Now().After(deadline) {
			dumpLog(t, rec)
			t.Fatal("GETCONF не дошёл до заглушки за 15 с")
		}
		time.Sleep(10 * time.Millisecond)
	}
	g.stop(t, 5*time.Second)
}

func dumpLog(t *testing.T, rec *recorder) {
	t.Helper()
	for _, ev := range rec.all() {
		if l, ok := ev.(EventLog); ok {
			t.Log(l.Msg)
		}
	}
}
