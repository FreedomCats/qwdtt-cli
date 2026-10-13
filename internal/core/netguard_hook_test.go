package core

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	tlsclient "github.com/bogdanfinn/tls-client"
)

// ───────────────────────── покрытие точек ─────────────────────────

// socketCall — то, чем ядро может открыть сокет наружу или собрать клиента,
// который откроет его сам.
var socketCall = regexp.MustCompile(`\bnet\.(Dial\w*|Listen\w*)\(|\bnet\.(Dialer|ListenConfig)\{|` +
	`\btlsclient\.NewHttpClient\(|\bhttp2?\.(Transport|Client)\{|\bhttp\.(Get|Post|Head|PostForm)\(|` +
	`\bhttp\.DefaultClient\b|\btls\.Dial\w*\(|\bWithProtocolRacing\(|\bstdnet\.NewNet\(`)

// guardMarker — признак, что в вызове стоит хук.
var guardMarker = regexp.MustCompile(`\bguarded(Control|Dialer)\b`)

// knownSocket — найденное место и почему оно в порядке. guarded — хук
// обязан стоять в самом вызове (в пределах guardWindow строк); иначе why
// объясняет, почему хук здесь не нужен.
type knownSocket struct {
	file    string
	line    string // подстрока строки с находкой
	guarded bool
	why     string
}

const guardWindow = 6

// knownSockets — все места в internal/core, где появляются сокеты. Меняется
// вместе с правилами netguard в scripts/vendor-core.ps1.
var knownSockets = []knownSocket{
	{"session.go", "d := net.Dialer{Timeout: 10 * time.Second", true, "точка 2, TURN по TCP"},
	{"creds.go", "dialer := &net.Dialer{", true, "точка 3, резолвер VK"},
	{"doh.go", "return &net.Dialer{", true, "точка 4, загрузочный диалер DoH"},
	{"creds.go", "client, err := tlsclient.NewHttpClient(", true, "точка 5, HTTPS к VK"},
	{"creds_vkcalls.go", "client, err := tlsclient.NewHttpClient(", true, "точка 6, HTTPS к VK по vkcalls"},
	{"netguard_hook.go", "return net.Dialer{Control: guardedControl}", true, "guardedDialer"},
	{"netguard_hook.go", "d := net.Dialer{Control: guardedControl}", true, "точка 1, dialTURNUDP"},
	{"doh.go", "transport := &http2.Transport{", false, "DialTLSContext идёт через загрузочный диалер, точка 4"},
	{"doh.go", "return &http.Client{", false, "поверх транспорта выше"},
	{"listen.go", "lc := net.ListenConfig{", false, "loopback для datapath, мимо туннеля и так"},
}

// TestNetguardCoverage читает исходники ядра и проверяет, что каждое место,
// где появляется сокет, известно и закрыто хуком. Новая точка после
// обновления ядра роняет этот тест, а не туннель.
func TestNetguardCoverage(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := make([]int, len(knownSockets))
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		lines := readLines(t, path)
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "//") || !socketCall.MatchString(line) {
				continue
			}
			k := -1
			for j, known := range knownSockets {
				if known.file == path && strings.Contains(line, known.line) {
					k = j
					break
				}
			}
			if k < 0 {
				t.Errorf("%s:%d: новое место с сокетом, хук netguard не проверен: %s\n"+
					"закройте его правилом в scripts/vendor-core.ps1 и внесите в knownSockets",
					path, i+1, strings.TrimSpace(line))
				continue
			}
			seen[k]++
			if !knownSockets[k].guarded {
				continue
			}
			window := strings.Join(lines[i:min(i+guardWindow, len(lines))], "\n")
			if !guardMarker.MatchString(window) {
				t.Errorf("%s:%d: %s — хука нет в вызове: %s", path, i+1, knownSockets[k].why, strings.TrimSpace(line))
			}
		}
	}
	for j, known := range knownSockets {
		if seen[j] != 1 {
			t.Errorf("%s «%s» (%s): найдено %d раз, ждали ровно один", known.file, known.line, known.why, seen[j])
		}
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// ───────────────────────── хук в деле ─────────────────────────

// hookRecorder — считающий хук: запоминает каждый сокет. Сокеты к :443
// он отвергает, чтобы запросы к VK не уходили дальше диала.
type hookRecorder struct {
	mu    sync.Mutex
	calls []string
}

var errHookStop = errors.New("тестовый хук: дальше не пускаем")

func (r *hookRecorder) control(network, address string, _ syscall.RawConn) error {
	r.mu.Lock()
	r.calls = append(r.calls, network+" "+address)
	r.mu.Unlock()
	if strings.HasSuffix(address, ":443") {
		return errHookStop
	}
	return nil
}

func (r *hookRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls
	r.calls = nil
	return calls
}

func (r *hookRecorder) requireCall(t *testing.T, what, want string) {
	t.Helper()
	calls := r.take()
	for _, c := range calls {
		if c == want {
			return
		}
	}
	t.Errorf("%s: хук не вызван для %q; вызовы: %q", what, want, calls)
}

// TestDialControlHook проходит по всем видам соединений ядра без сети:
// резолвер и TURN смотрят на loopback-слушатели, а имена VK поддельный DNS
// разрешает в 127.0.0.1.
func TestDialControlHook(t *testing.T) {
	savedResolver := net.DefaultResolver
	t.Cleanup(func() {
		net.DefaultResolver = savedResolver
		setDialControl(nil)
	})

	var rec hookRecorder
	setDialControl(rec.control)

	dns := startFakeDNS(t)
	dnsAddr := dns.LocalAddr().String()

	t.Run("DNS", func(t *testing.T) {
		setupGlobalResolver("custom:" + dnsAddr)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupHost(ctx, "login.vk.ru")
		if err != nil {
			t.Fatalf("поддельный DNS не ответил: %v", err)
		}
		if len(addrs) == 0 || addrs[0] != "127.0.0.1" {
			t.Fatalf("адреса %v", addrs)
		}
		rec.requireCall(t, "резолвер", "udp4 "+dnsAddr)
	})

	t.Run("DoH", func(t *testing.T) {
		ln := listenTCP(t)
		setupGlobalResolver("doh:https://" + ln.Addr().String() + "/dns-query")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = net.DefaultResolver.LookupHost(ctx, "login.vk.ru")
		rec.requireCall(t, "DoH", "tcp4 "+ln.Addr().String())
	})

	t.Run("TURN UDP", func(t *testing.T) {
		pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		_, closer, err := dialTURNConn(pc.LocalAddr().String(), false)
		if err != nil {
			t.Fatal(err)
		}
		closer.Close()
		rec.requireCall(t, "TURN UDP", "udp4 "+pc.LocalAddr().String())
	})

	t.Run("TURN TCP", func(t *testing.T) {
		ln := listenTCP(t)
		_, closer, err := dialTURNConn(ln.Addr().String(), true)
		if err != nil {
			t.Fatal(err)
		}
		closer.Close()
		rec.requireCall(t, "TURN TCP", "tcp4 "+ln.Addr().String())
	})

	// Дальше имена VK разрешает поддельный DNS в 127.0.0.1, а хук обрывает
	// диал к :443 — до VK запрос не доходит.
	setupGlobalResolver("custom:" + dnsAddr)

	t.Run("VK getTokenChain", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _, _, err := getTokenChain(ctx, "test-link", 1, vkCredentialsList[0], tlsclient.NewCookieJar())
		if err == nil {
			t.Fatal("запрос к VK прошёл, хотя хук его отверг")
		}
		rec.requireCall(t, "getTokenChain", "tcp4 127.0.0.1:443")
	})

	t.Run("VK vkcalls", func(t *testing.T) {
		t.Setenv("VK_SKIP_VKCALLS", "")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _, _, err := getVKCredsViaVKCallsPath(ctx, "test-link", 1)
		if err == nil {
			t.Fatal("запрос к VK прошёл, хотя хук его отверг")
		}
		rec.requireCall(t, "vkcalls", "tcp4 127.0.0.1:443")
	})
}

// Хука нет — сокеты создаются как в Э1.
func TestDialControlNil(t *testing.T) {
	setDialControl(nil)
	if err := guardedControl("tcp4", "1.1.1.1:443", nil); err != nil {
		t.Fatal(err)
	}
	setDialControl(func(string, string, syscall.RawConn) error { return errHookStop })
	setDialControl(nil)
	if err := guardedControl("tcp4", "1.1.1.1:443", nil); err != nil {
		t.Fatalf("снятый хук всё ещё зовётся: %v", err)
	}
}

// Хук из Config доезжает до сокетов через applyGlobals.
func TestApplyGlobalsSetsDialControl(t *testing.T) {
	savedResolver := net.DefaultResolver
	t.Cleanup(func() {
		net.DefaultResolver = savedResolver
		setDialControl(nil)
	})
	var rec hookRecorder
	cfg := DefaultConfig()
	cfg.DialControl = rec.control
	if err := applyGlobals(&cfg); err != nil {
		t.Fatal(err)
	}
	_ = guardedControl("udp4", "198.51.100.1:3478", nil)
	rec.requireCall(t, "applyGlobals", "udp4 198.51.100.1:3478")
}

func listenTCP(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return ln
}

// startFakeDNS — DNS по UDP на loopback: на запрос A отвечает 127.0.0.1,
// на остальные — пустым ответом без ошибки.
func startFakeDNS(t *testing.T) net.PacketConn {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if resp, err := fakeDNSAnswer(buf[:n]); err == nil {
				_, _ = pc.WriteTo(resp, from)
			}
		}
	}()
	return pc
}

func fakeDNSAnswer(q []byte) ([]byte, error) {
	if len(q) < 12 {
		return nil, errors.New("короткий запрос")
	}
	end := 12
	for end < len(q) && q[end] != 0 {
		end += int(q[end]) + 1
	}
	end++ // нулевая метка
	if end+4 > len(q) {
		return nil, fmt.Errorf("битый вопрос")
	}
	question := q[12 : end+4]
	isA := q[end] == 0 && q[end+1] == 1

	resp := []byte{q[0], q[1], 0x81, 0x80, 0, 1, 0, 0, 0, 0, 0, 0}
	if isA {
		resp[7] = 1
	}
	resp = append(resp, question...)
	if isA {
		resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 127, 0, 0, 1)
	}
	return resp, nil
}
