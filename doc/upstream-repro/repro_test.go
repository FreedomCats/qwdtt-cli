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
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
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
