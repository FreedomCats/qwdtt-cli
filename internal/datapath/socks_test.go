package datapath

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// Туннель в этих тестах никуда не ведёт: peer на loopback-порту, которого нет,
// рукопожатие WireGuard не проходит. Это и не нужно — проверяется всё, что
// datapath делает до первого байта трафика: слушатель, правила, авторизация
// и остановка. Настоящая проверка сквозного трафика — задача H2.
func testWGConfig(t *testing.T, endpoint string) *WGConfig {
	t.Helper()
	conf := "[Interface]\n" +
		"PrivateKey = " + testPrivB64 + "\n" +
		"Address = 10.7.0.2/32\n" +
		"DNS = 8.8.8.8\n" +
		"MTU = 1280\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + testPubB64 + "\n" +
		"AllowedIPs = 0.0.0.0/0\n" +
		"Endpoint = " + endpoint + "\n" +
		"PersistentKeepalive = 25"
	cfg, err := ParseWgQuick(conf)
	if err != nil {
		t.Fatalf("ParseWgQuick: %v", err)
	}
	return cfg
}

// recorder собирает вывод datapath: писать в stdout он не должен, а в тесте
// пригодится, если что-то пойдёт не так.
type recorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *recorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (r *recorder) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

// startSocks поднимает datapath на свободном порту и снимает его по окончании теста.
func startSocks(t *testing.T, opts SocksOptions) (*Socks, *recorder) {
	t.Helper()
	rec := &recorder{}
	if opts.Listen == "" {
		opts.Listen = "127.0.0.1:0"
	}
	opts.Logf = rec.logf

	s := NewSocks(opts)
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Start(context.Background(), testWGConfig(t, "127.0.0.1:51820")); err != nil {
		t.Fatalf("Start: %v\nвывод:\n%s", err, rec.dump())
	}
	return s, rec
}

func TestSocksRequiresNothing(t *testing.T) {
	if got := NewSocks(SocksOptions{}).Requires(); got.Admin || len(got.Files) != 0 || len(got.Programs) != 0 || got.Warning != "" || got.Netguard || got.Unsupported != nil {
		t.Errorf("Requires = %+v, а режим на то и socks, что ничего не требует", got)
	}
}

// Фильтр вывода go-socks5: штатные закрытия клиентом и IPv6 глушатся,
// настоящие отказы печатаются. Первая строка — дословно из живого прогона.
func TestSocksLogNoise(t *testing.T) {
	cases := []struct {
		msg   string
		noise bool
	}{
		{"[ERR] socks: Failed to handle request: writeto tcp 127.0.0.1:1080->127.0.0.1:47238: read tcp 127.0.0.1:1080->127.0.0.1:47238: read: connection reset by peer", true},
		{"[ERR] socks: Failed to handle request: readfrom tcp 127.0.0.1:1080->127.0.0.1:50000: write: broken pipe", true},
		{"[ERR] socks: Failed to get version byte: EOF", true},
		{"[ERR] socks: Failed to handle request: Connect to [2001:db8::1]:443 failed: no route to host", true},
		{"[ERR] socks: Failed to handle request: Connect to 203.0.113.5:443 failed: connection reset by peer", false},
		{"[ERR] socks: Failed to handle request: Connect to 203.0.113.5:443 failed: connect: connection refused", false},
		{"[ERR] socks: Unsupported SOCKS version: [4]", false},
		{"[ERR] socks: Failed to authenticate: no supported authentication mechanism", false},
	}
	for _, c := range cases {
		if got := isBenignSocksNoise(c.msg); got != c.noise {
			t.Errorf("isBenignSocksNoise(%q) = %v, ждали %v", c.msg, got, c.noise)
		}
	}
}

// Close зовётся из defer супервизора, в том числе когда Start не случился.
func TestSocksCloseWithoutStart(t *testing.T) {
	s := NewSocks(SocksOptions{})
	if err := s.Close(); err != nil {
		t.Errorf("Close до Start: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("повторный Close: %v", err)
	}
	if err := s.Start(context.Background(), testWGConfig(t, "127.0.0.1:51820")); err == nil {
		t.Error("Start после Close прошёл")
	}
}

func TestSocksStartListens(t *testing.T) {
	s, rec := startSocks(t, SocksOptions{})

	addr := s.ListenAddr()
	if addr == nil {
		t.Fatal("ListenAddr пуст после Start")
	}
	if _, err := net.DialTimeout("tcp", addr.String(), 2*time.Second); err != nil {
		t.Fatalf("соединение с SOCKS5 %s: %v\nвывод:\n%s", addr, err, rec.dump())
	}
	if want := netip.MustParseAddrPort("127.0.0.1:51820"); s.Endpoint() != want {
		t.Errorf("Endpoint = %v, ожидался %v", s.Endpoint(), want)
	}
	if err := s.Start(context.Background(), testWGConfig(t, "127.0.0.1:51820")); err == nil {
		t.Error("второй Start прошёл")
	}
}

// Занятый порт обязан выясниться в Start, а не в фоне после того, как
// CLI отчитался о готовности.
func TestSocksListenBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	defer busy.Close()

	s := NewSocks(SocksOptions{Listen: busy.Addr().String()})
	defer s.Close()

	err = s.Start(context.Background(), testWGConfig(t, "127.0.0.1:51820"))
	if err == nil {
		t.Fatal("Start на занятом порту прошёл")
	}
	if !strings.Contains(err.Error(), "listen") {
		t.Errorf("ошибка %q не называет причину", err)
	}
}

// IPv6 отклоняется намеренно: маршрутов на v6 в туннеле нет, и клиент,
// пробующий AAAA первым, должен получить отказ сразу.
func TestSocksRejectsIPv6(t *testing.T) {
	s, rec := startSocks(t, SocksOptions{})

	conn := dialSocks(t, s)
	defer conn.Close()

	greet(t, conn, 0x00)
	if rep := connectIPv6(t, conn); rep == 0x00 {
		t.Errorf("CONNECT на IPv6 разрешён\nвывод:\n%s", rec.dump())
	}
}

func TestSocksAuth(t *testing.T) {
	s, _ := startSocks(t, SocksOptions{Auth: true, Username: "u", Password: "p"})

	t.Run("без авторизации не пускают", func(t *testing.T) {
		conn := dialSocks(t, s)
		defer conn.Close()

		if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
			t.Fatalf("приветствие: %v", err)
		}
		reply := make([]byte, 2)
		if _, err := io.ReadFull(conn, reply); err != nil {
			t.Fatalf("ответ на приветствие: %v", err)
		}
		if reply[1] != 0xFF {
			t.Errorf("метод %#x, ожидался отказ 0xFF", reply[1])
		}
	})

	t.Run("чужой пароль не подходит", func(t *testing.T) {
		conn := dialSocks(t, s)
		defer conn.Close()

		greet(t, conn, 0x02)
		if status := authenticate(t, conn, "u", "не тот"); status == 0x00 {
			t.Error("чужой пароль принят")
		}
	})

	t.Run("свой пароль подходит", func(t *testing.T) {
		conn := dialSocks(t, s)
		defer conn.Close()

		greet(t, conn, 0x02)
		if status := authenticate(t, conn, "u", "p"); status != 0x00 {
			t.Fatalf("свой пароль отвергнут: статус %#x", status)
		}
		// Дальше сервер ведёт себя как обычно: IPv6 всё так же отклоняется.
		if rep := connectIPv6(t, conn); rep == 0x00 {
			t.Error("CONNECT на IPv6 разрешён после авторизации")
		}
	})
}

// Включённая авторизация с пустыми учётными данными означала бы «пускаем
// любого»: библиотека приняла бы пустую пару как валидную.
func TestSocksAuthNeedsCredentials(t *testing.T) {
	s := NewSocks(SocksOptions{Listen: "127.0.0.1:0", Auth: true, Username: "u"})
	defer s.Close()

	err := s.Start(context.Background(), testWGConfig(t, "127.0.0.1:51820"))
	if err == nil {
		t.Fatal("Start с пустым паролем прошёл")
	}
	if !strings.Contains(err.Error(), "логин и пароль") {
		t.Errorf("ошибка %q не объясняет причину", err)
	}
}

// Отмена ctx равносильна Close: так супервизор снимает datapath (задача F3).
func TestSocksStopsOnContext(t *testing.T) {
	rec := &recorder{}
	s := NewSocks(SocksOptions{Listen: "127.0.0.1:0", Logf: rec.logf})
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	if err := s.Start(ctx, testWGConfig(t, "127.0.0.1:51820")); err != nil {
		t.Fatalf("Start: %v", err)
	}
	addr := s.ListenAddr().String()
	cancel()

	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return
		}
		conn.Close()
		if time.Now().After(deadline) {
			t.Fatalf("SOCKS5 всё ещё слушает %s после отмены ctx\nвывод:\n%s", addr, rec.dump())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// ─────────────────────────── разговор по SOCKS5 ───────────────────────────

func dialSocks(t *testing.T, s *Socks) net.Conn {
	t.Helper()
	addr := s.ListenAddr()
	if addr == nil {
		t.Fatal("datapath не слушает")
	}
	conn, err := net.DialTimeout("tcp", addr.String(), 2*time.Second)
	if err != nil {
		t.Fatalf("соединение с SOCKS5: %v", err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	return conn
}

// greet шлёт приветствие с единственным предложенным методом и убеждается,
// что сервер выбрал именно его.
func greet(t *testing.T, conn net.Conn, method byte) {
	t.Helper()
	if _, err := conn.Write([]byte{0x05, 0x01, method}); err != nil {
		t.Fatalf("приветствие: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("ответ на приветствие: %v", err)
	}
	if reply[0] != 0x05 || reply[1] != method {
		t.Fatalf("ответ на приветствие %v, ожидался метод %#x", reply, method)
	}
}

// authenticate проходит подпереговоры RFC 1929 и возвращает статус.
func authenticate(t *testing.T, conn net.Conn, user, pass string) byte {
	t.Helper()
	msg := []byte{0x01, byte(len(user))}
	msg = append(msg, user...)
	msg = append(msg, byte(len(pass)))
	msg = append(msg, pass...)
	if _, err := conn.Write(msg); err != nil {
		t.Fatalf("учётные данные: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("ответ на учётные данные: %v", err)
	}
	return reply[1]
}

// connectIPv6 просит CONNECT на адрес IPv6 и возвращает поле REP ответа.
func connectIPv6(t *testing.T, conn net.Conn) byte {
	t.Helper()
	ip := net.ParseIP("2001:db8::1").To16()
	req := []byte{0x05, 0x01, 0x00, 0x04}
	req = append(req, ip...)
	req = binary.BigEndian.AppendUint16(req, 443)
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("запрос CONNECT: %v", err)
	}
	head := make([]byte, 4)
	if _, err := io.ReadFull(conn, head); err != nil {
		t.Fatalf("ответ на CONNECT: %v", err)
	}
	if head[0] != 0x05 {
		t.Fatalf("ответ на CONNECT начинается с %#x", head[0])
	}
	return head[1]
}
