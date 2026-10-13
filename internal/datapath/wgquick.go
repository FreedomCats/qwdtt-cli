package datapath

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// WGConfig — разобранный конфиг WireGuard в формате wg-quick, тот самый, что
// сервер возвращает на GETCONF и что ядро отдаёт наружу событием
// core.EventConfig.
//
// Разбор нужен обеим реализациям Э1, поэтому живёт здесь, а не в одной из них:
// socks собирает из полей запрос к wireguard-go (IPCRequest), external пишет
// в файл исходный текст (Raw), но обе обязаны понять, что прислал сервер,
// до того, как поднимать туннель.
type WGConfig struct {
	// Raw — текст конфига, как он пришёл. Нужен external: штатный WireGuard
	// читает файл сам, и пересобирать конфиг из полей значило бы рисковать
	// потерей того, что разбор не понял.
	Raw string

	// PrivateKeyHex и PeerPublicHex — ключи в том виде, в каком их принимает
	// протокол IPC wireguard-go: hex, а не base64 из файла конфига.
	PrivateKeyHex string
	PeerPublicHex string

	// Addresses — адреса интерфейса, уже без длины префикса: netstack.CreateNetTUN
	// принимает именно адреса.
	Addresses []netip.Addr
	// DNS — резолверы туннеля. Если сервер их не прислал, остаётся 1.1.1.1.
	DNS []netip.Addr
	// MTU — по умолчанию 1280: минимум, гарантированный IPv6, и запас под все
	// заголовки туннеля.
	MTU int

	// AllowedIPs — префиксы строками, как в конфиге: дальше они уходят в IPC
	// без разбора. Пустой список читается как 0.0.0.0/0.
	AllowedIPs []string
	// Endpoint — куда слать UDP. Строка, а не netip.AddrPort: wireguard-go
	// принимает и имя хоста и резолвит его сам. Разбор адреса — EndpointAddrPort.
	Endpoint string
	// Keepalive — PersistentKeepalive в секундах, по умолчанию 25.
	Keepalive uint16
}

// ParseWgQuick разбирает конфиг в формате wg-quick.
//
// Разбор намеренно снисходителен ровно там, где снисходителен сам wg-quick:
// незнакомые ключи и секции пропускаются, строка без «=» пропускается,
// комментарии (# и ;) игнорируются. Строгость — только к тому, без чего туннель
// не поднимется: ключи, адрес и endpoint обязаны быть на месте, ключ обязан
// быть 32 байтами base64.
func ParseWgQuick(conf string) (*WGConfig, error) {
	cfg := &WGConfig{
		Raw:       conf,
		MTU:       1280,
		Keepalive: 25,
	}
	section := ""
	for _, raw := range strings.Split(conf, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(strings.ToLower(key))
		val = strings.TrimSpace(val)
		switch section {
		case "interface":
			switch key {
			case "privatekey":
				hexKey, err := b64KeyToHex(val)
				if err != nil {
					return nil, fmt.Errorf("PrivateKey: %w", err)
				}
				cfg.PrivateKeyHex = hexKey
			case "address":
				for _, part := range strings.Split(val, ",") {
					part = strings.TrimSpace(part)
					if part == "" {
						continue
					}
					prefix, err := netip.ParsePrefix(part)
					if err != nil {
						addr, err2 := netip.ParseAddr(part)
						if err2 != nil {
							return nil, fmt.Errorf("Address %q: %w", part, err)
						}
						cfg.Addresses = append(cfg.Addresses, addr)
						continue
					}
					cfg.Addresses = append(cfg.Addresses, prefix.Addr())
				}
			case "dns":
				for _, part := range strings.Split(val, ",") {
					part = strings.TrimSpace(part)
					if part == "" {
						continue
					}
					addr, err := netip.ParseAddr(part)
					if err != nil {
						return nil, fmt.Errorf("DNS %q: %w", part, err)
					}
					cfg.DNS = append(cfg.DNS, addr)
				}
			case "mtu":
				mtu, err := strconv.Atoi(val)
				if err != nil || mtu < 576 {
					return nil, fmt.Errorf("MTU %q: invalid", val)
				}
				cfg.MTU = mtu
			}
		case "peer":
			switch key {
			case "publickey":
				hexKey, err := b64KeyToHex(val)
				if err != nil {
					return nil, fmt.Errorf("PublicKey: %w", err)
				}
				cfg.PeerPublicHex = hexKey
			case "allowedips":
				for _, part := range strings.Split(val, ",") {
					part = strings.TrimSpace(part)
					if part != "" {
						cfg.AllowedIPs = append(cfg.AllowedIPs, part)
					}
				}
			case "endpoint":
				cfg.Endpoint = val
			case "persistentkeepalive":
				n, err := strconv.Atoi(val)
				if err != nil || n < 0 || n > 65535 {
					return nil, fmt.Errorf("PersistentKeepalive %q: invalid", val)
				}
				cfg.Keepalive = uint16(n)
			}
		}
	}
	if cfg.PrivateKeyHex == "" || cfg.PeerPublicHex == "" {
		return nil, fmt.Errorf("config missing PrivateKey or Peer PublicKey")
	}
	if len(cfg.Addresses) == 0 {
		return nil, fmt.Errorf("config missing Address")
	}
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("config missing Endpoint")
	}
	if len(cfg.AllowedIPs) == 0 {
		cfg.AllowedIPs = []string{"0.0.0.0/0"}
	}
	if len(cfg.DNS) == 0 {
		cfg.DNS = []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	}
	return cfg, nil
}

// IPCRequest собирает конфиг в текстовый протокол wireguard-go (device.IpcSet).
func (c *WGConfig) IPCRequest() string {
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", c.PrivateKeyHex)
	fmt.Fprintf(&b, "public_key=%s\n", c.PeerPublicHex)
	fmt.Fprintf(&b, "endpoint=%s\n", c.Endpoint)
	fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", c.Keepalive)
	for _, ip := range c.AllowedIPs {
		fmt.Fprintf(&b, "allowed_ip=%s\n", ip)
	}
	return b.String()
}

// EndpointAddrPort разбирает Endpoint в адрес с портом — то, что реализация
// возвращает из Datapath.Endpoint.
//
// Второе значение — false, если в конфиге не адрес, а имя хоста: сервер
// возвращает 127.0.0.1:<порт>, но формат wg-quick имя допускает, и молча
// подставлять нули вместо адреса нельзя.
func (c *WGConfig) EndpointAddrPort() (netip.AddrPort, bool) {
	ap, err := netip.ParseAddrPort(c.Endpoint)
	if err != nil {
		return netip.AddrPort{}, false
	}
	return ap, true
}

// b64KeyToHex переводит ключ WireGuard из base64 (формат конфига) в hex
// (формат протокола IPC).
func b64KeyToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", err
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("wireguard key must be 32 bytes, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}
