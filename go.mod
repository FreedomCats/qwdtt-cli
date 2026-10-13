module qwdtt-cli

go 1.26.2

// Зависимости ядра qWDTT. Версии взяты из go_client/go.mod апстрима
// (коммит в internal/core/CORE_VERSION) — на них ядро собрано и протестировано.
// Менять их отдельно от обновления ядра не нужно.
//
// golang.org/x/sys помечен indirect с задачи B6: напрямую его импортировал
// tun_fd.go, который перестал переноситься вместе с режимом rawtun. Прямым он
// снова станет в C2 — DPAPI живёт в golang.org/x/sys/windows.
require (
	github.com/armon/go-socks5 v0.0.0-20160902184237-e75332964ef5
	github.com/bogdanfinn/fhttp v0.6.8
	github.com/bogdanfinn/tls-client v1.15.1
	github.com/cbeuw/connutil v1.0.1
	github.com/google/uuid v1.6.0
	github.com/pion/dtls/v3 v3.1.5
	github.com/pion/logging v0.2.4
	github.com/pion/transport/v4 v4.0.2
	github.com/pion/turn/v5 v5.0.12
	golang.org/x/crypto v0.54.0
	golang.org/x/net v0.57.0
	golang.org/x/sys v0.47.0 // indirect
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
)

// Наши зависимости — те, которых у ядра не было.
require github.com/BurntSushi/toml v1.6.0

require (
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/bdandy/go-errors v1.2.2 // indirect
	github.com/bdandy/go-socks4 v1.2.3 // indirect
	github.com/bogdanfinn/quic-go-utls v1.0.9-utls // indirect
	github.com/bogdanfinn/utls v1.7.7-barnius // indirect
	github.com/bogdanfinn/websocket v1.5.5-barnius // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/klauspost/compress v1.18.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/stun/v3 v3.1.6 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/tam7t/hpkp v0.0.0-20160821193359-2b70b4024ed5 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c // indirect
)

// Пакет tun/netstack существует и внутри модуля golang.zx2c4.com/wireguard,
// и как отдельный модуль. Без exclude команда go mod tidy видит оба и падает
// с ambiguous import. Нам нужен тот, что внутри wireguard.
exclude golang.zx2c4.com/wireguard/tun/netstack v0.0.0-20220703234212-c31a7b1ab478
