package datapath

import (
	"net/netip"
	"strings"
	"testing"
)

// Ключи собраны из байтов 0…31 и 32…63, поэтому hex читается глазами.
const (
	testPrivB64 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
	testPrivHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	testPubB64  = "ICEiIyQlJicoKSorLC0uLzAxMjM0NTY3ODk6Ozw9Pj8="
	testPubHex  = "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"
)

// serverConfig — ответ сервера дословно по шаблону buildClientConfig
// (server/wireguard.go): сплошной текст без завершающего перевода строки,
// адрес с /32, endpoint на loopback — порт выбрало ядро, сервер вернул его
// обратно.
func serverConfig() string {
	return "[Interface]\n" +
		"PrivateKey = " + testPrivB64 + "\n" +
		"Address = 10.7.0.2/32\n" +
		"DNS = 8.8.8.8\n" +
		"MTU = 1280\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = " + testPubB64 + "\n" +
		"AllowedIPs = 0.0.0.0/0\n" +
		"Endpoint = 127.0.0.1:51820\n" +
		"PersistentKeepalive = 25"
}

func TestParseServerConfig(t *testing.T) {
	cfg, err := ParseWgQuick(serverConfig())
	if err != nil {
		t.Fatalf("ParseWgQuick: %v", err)
	}

	if cfg.PrivateKeyHex != testPrivHex {
		t.Errorf("PrivateKeyHex = %q", cfg.PrivateKeyHex)
	}
	if cfg.PeerPublicHex != testPubHex {
		t.Errorf("PeerPublicHex = %q", cfg.PeerPublicHex)
	}
	if got := cfg.Addresses; len(got) != 1 || got[0] != netip.MustParseAddr("10.7.0.2") {
		t.Errorf("Addresses = %v, длина префикса должна быть отброшена", got)
	}
	if got := cfg.DNS; len(got) != 1 || got[0] != netip.MustParseAddr("8.8.8.8") {
		t.Errorf("DNS = %v", got)
	}
	if cfg.MTU != 1280 {
		t.Errorf("MTU = %d", cfg.MTU)
	}
	if got := cfg.AllowedIPs; len(got) != 1 || got[0] != "0.0.0.0/0" {
		t.Errorf("AllowedIPs = %v", got)
	}
	if cfg.Endpoint != "127.0.0.1:51820" {
		t.Errorf("Endpoint = %q", cfg.Endpoint)
	}
	if cfg.Keepalive != 25 {
		t.Errorf("Keepalive = %d", cfg.Keepalive)
	}
	if cfg.Raw != serverConfig() {
		t.Error("Raw не равен исходному тексту, а external пишет в файл именно его")
	}

	ap, ok := cfg.EndpointAddrPort()
	if !ok || ap != netip.MustParseAddrPort("127.0.0.1:51820") {
		t.Errorf("EndpointAddrPort = %v, %v", ap, ok)
	}

	want := "private_key=" + testPrivHex + "\n" +
		"public_key=" + testPubHex + "\n" +
		"endpoint=127.0.0.1:51820\n" +
		"persistent_keepalive_interval=25\n" +
		"allowed_ip=0.0.0.0/0\n"
	if got := cfg.IPCRequest(); got != want {
		t.Errorf("IPCRequest =\n%q\nwant\n%q", got, want)
	}
}

// Конфиг приезжает из сети, поэтому переводы строк могут оказаться CRLF.
func TestParseCRLF(t *testing.T) {
	cfg, err := ParseWgQuick(strings.ReplaceAll(serverConfig(), "\n", "\r\n"))
	if err != nil {
		t.Fatalf("ParseWgQuick: %v", err)
	}
	if cfg.Endpoint != "127.0.0.1:51820" {
		t.Errorf("Endpoint = %q — \\r не отброшен", cfg.Endpoint)
	}
	if cfg.PrivateKeyHex != testPrivHex {
		t.Errorf("PrivateKeyHex = %q", cfg.PrivateKeyHex)
	}
}

// Всё, что сервер не прислал, имеет умолчание — кроме ключей, адреса и endpoint.
func TestParseDefaults(t *testing.T) {
	conf := "[Interface]\n" +
		"PrivateKey = " + testPrivB64 + "\n" +
		"Address = 10.7.0.2/32\n" +
		"[Peer]\n" +
		"PublicKey = " + testPubB64 + "\n" +
		"Endpoint = 127.0.0.1:51820\n"
	cfg, err := ParseWgQuick(conf)
	if err != nil {
		t.Fatalf("ParseWgQuick: %v", err)
	}
	if cfg.MTU != 1280 {
		t.Errorf("MTU = %d, want 1280", cfg.MTU)
	}
	if cfg.Keepalive != 25 {
		t.Errorf("Keepalive = %d, want 25", cfg.Keepalive)
	}
	if got := cfg.AllowedIPs; len(got) != 1 || got[0] != "0.0.0.0/0" {
		t.Errorf("AllowedIPs = %v, want [0.0.0.0/0]", got)
	}
	if got := cfg.DNS; len(got) != 1 || got[0] != netip.MustParseAddr("1.1.1.1") {
		t.Errorf("DNS = %v, want [1.1.1.1]", got)
	}
}

// Списки через запятую, адрес без префикса, регистр ключей, комментарии,
// незнакомые ключи и секции — всё это wg-quick допускает, и разбор не должен
// на них спотыкаться.
func TestParseTolerance(t *testing.T) {
	conf := "# комментарий\n" +
		"; тоже комментарий\n" +
		"[Interface]\n" +
		"privatekey=" + testPrivB64 + "\n" +
		"ADDRESS = 10.7.0.2, fd00::2/64\n" +
		"DNS = 8.8.8.8 , 1.0.0.1\n" +
		"Table = off\n" +
		"строка без знака равенства\n" +
		"[Unknown]\n" +
		"PrivateKey = мусор\n" +
		"[Peer]\n" +
		"PublicKey = " + testPubB64 + "\n" +
		"AllowedIPs = 10.0.0.0/8, 192.168.0.0/16\n" +
		"Endpoint = 127.0.0.1:51820\n"
	cfg, err := ParseWgQuick(conf)
	if err != nil {
		t.Fatalf("ParseWgQuick: %v", err)
	}
	if got := cfg.Addresses; len(got) != 2 || got[0] != netip.MustParseAddr("10.7.0.2") || got[1] != netip.MustParseAddr("fd00::2") {
		t.Errorf("Addresses = %v", got)
	}
	if got := cfg.DNS; len(got) != 2 || got[1] != netip.MustParseAddr("1.0.0.1") {
		t.Errorf("DNS = %v", got)
	}
	if got := cfg.AllowedIPs; len(got) != 2 || got[1] != "192.168.0.0/16" {
		t.Errorf("AllowedIPs = %v", got)
	}
	if cfg.PrivateKeyHex != testPrivHex {
		t.Error("PrivateKey из чужой секции затёр ключ интерфейса")
	}
}

func TestParseErrors(t *testing.T) {
	full := serverConfig()
	cases := []struct {
		name string
		conf string
		want string
	}{
		{"без PrivateKey", strings.Replace(full, "PrivateKey = "+testPrivB64+"\n", "", 1), "PrivateKey"},
		{"без PublicKey", strings.Replace(full, "PublicKey = "+testPubB64+"\n", "", 1), "PublicKey"},
		{"без Address", strings.Replace(full, "Address = 10.7.0.2/32\n", "", 1), "Address"},
		{"без Endpoint", strings.Replace(full, "Endpoint = 127.0.0.1:51820\n", "", 1), "Endpoint"},
		{"короткий ключ", strings.Replace(full, testPrivB64, "AAEC", 1), "32 bytes"},
		{"не base64", strings.Replace(full, testPrivB64, "не ключ!", 1), "PrivateKey"},
		{"MTU мал", strings.Replace(full, "MTU = 1280", "MTU = 128", 1), "MTU"},
		{"MTU не число", strings.Replace(full, "MTU = 1280", "MTU = много", 1), "MTU"},
		{"keepalive вне диапазона", strings.Replace(full, "PersistentKeepalive = 25", "PersistentKeepalive = 70000", 1), "PersistentKeepalive"},
		{"битый DNS", strings.Replace(full, "DNS = 8.8.8.8", "DNS = 8.8.8.888", 1), "DNS"},
		{"битый Address", strings.Replace(full, "Address = 10.7.0.2/32", "Address = 10.7.0.2/99", 1), "Address"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseWgQuick(c.conf)
			if err == nil {
				t.Fatal("ошибки нет, а должна быть")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("ошибка %q не называет %q", err, c.want)
			}
		})
	}
}

// Формат допускает имя хоста, а Datapath.Endpoint обязан вернуть адрес:
// вместо нулей в таком случае приходит false.
func TestEndpointHostname(t *testing.T) {
	conf := strings.Replace(serverConfig(), "Endpoint = 127.0.0.1:51820", "Endpoint = vpn.example.org:56000", 1)
	cfg, err := ParseWgQuick(conf)
	if err != nil {
		t.Fatalf("ParseWgQuick: %v", err)
	}
	if cfg.Endpoint != "vpn.example.org:56000" {
		t.Errorf("Endpoint = %q — имя хоста должно сохраниться для wireguard-go", cfg.Endpoint)
	}
	if ap, ok := cfg.EndpointAddrPort(); ok {
		t.Errorf("EndpointAddrPort = %v, true — имя хоста адресом не является", ap)
	}
}
