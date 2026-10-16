module github.com/FreedomCats/qwdtt-cli

go 1.26.2

// Зависимости ядра qWDTT. Версии взяты из go_client/go.mod апстрима
// (коммит в internal/core/CORE_VERSION) — на них ядро собрано и протестировано.
// Менять их отдельно от обновления ядра не нужно.
//
// golang.org/x/sys снова прямой с задачи C2: его импортирует secret_windows.go
// ради DPAPI (golang.org/x/sys/windows). В B6 он был indirect — тогда его
// единственным потребителем оставался убранный tun_fd.go.
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
	golang.org/x/sys v0.47.0
	golang.zx2c4.com/wireguard v0.0.0-20260522210424-ecfc5a8d5446
)

// Наши зависимости — те, которых у ядра не было.
//
// BurntSushi/toml (MIT) — разбор profiles.toml, задача C1.
// golang.org/x/term (BSD-3) — скрытый ввод пароля и хешей в `qwdtt-cli profile set`,
// задача C3.
// jchv/go-webview2 (MIT) — окно капчи на Windows, задача E3. Без cgo,
// WebView2Loader.dll встроен; тянет за собой jchv/go-winloader (MIT).
// golang.zx2c4.com/wireguard/windows (MIT) — пакет tunnel/winipcfg: маршруты,
// адреса, DNS и выбор интерфейса, задача N2 Э2. Импортируется только из
// *_windows.go, в сборки под другие системы не попадает.
// tailscale/wf (BSD-3) — фильтры WFP защиты от утечки DNS, задача W4 Э2;
// тянет go4.org/netipx (BSD-3). Тоже только из *_windows.go. Прочие его
// косвенные (x/tools, honnef.co/go/tools, x/exp/typeparams) нужны лишь
// его генераторам и в бинарь не попадают: tidy видит их через tooldeps.go.
// vishvananda/netlink (Apache-2.0) — выбор интерфейса и подписка на смену
// сети на Linux, задача N2 Э4; тянет vishvananda/netns (Apache-2.0). Только из
// *_linux.go. Модуль и раньше был в графе (требование gvisor), но в сборку не
// попадал; версия поднята с v1.1.1-0.20211118161826-650dca95af54.
// google/nftables (Apache-2.0) — защита от утечки DNS на Linux, задача D1 Э4;
// тянет mdlayher/netlink и mdlayher/socket (MIT). Взят из ветки main, а не из
// релиза: флаг таблицы owner (таблица уходит вместе с процессом) есть только
// там. Только из *_linux.go. go-cmp (BSD-3) нужен лишь тестам mdlayher/netlink
// и в бинарь не попадает.
// godbus/dbus (BSD-2) — DNS туннеля через systemd-resolved на Linux, задача
// D2 Э4, и секреты в связке ключей (Secret Service). Только из *_linux.go.
// Все лицензии с GPL v3 совместимы.
require (
	github.com/BurntSushi/toml v1.6.0
	github.com/godbus/dbus/v5 v5.1.0
	github.com/google/nftables v0.3.1-0.20260430172505-f9b52ed2ba65
	github.com/jchv/go-webview2 v0.0.0-20260205173254-56598839c808
	github.com/tailscale/wf v0.0.0-20240214030419-6fbb0a674ee6
	github.com/vishvananda/netlink v1.3.1
	golang.org/x/term v0.45.0
	golang.zx2c4.com/wireguard/windows v1.1.1
)

require (
	github.com/andybalholm/brotli v1.2.0 // indirect
	github.com/bdandy/go-errors v1.2.2 // indirect
	github.com/bdandy/go-socks4 v1.2.3 // indirect
	github.com/bogdanfinn/quic-go-utls v1.0.9-utls // indirect
	github.com/bogdanfinn/utls v1.7.7-barnius // indirect
	github.com/bogdanfinn/websocket v1.5.5-barnius // indirect
	github.com/google/btree v1.1.2 // indirect
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/jchv/go-winloader v0.0.0-20250406163304-c1995be93bd1 // indirect
	github.com/klauspost/compress v1.18.2 // indirect
	github.com/mdlayher/netlink v1.8.1-0.20251028132421-dcc6cab9a6eb // indirect
	github.com/mdlayher/socket v0.5.1 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/stun/v3 v3.1.6 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/tam7t/hpkp v0.0.0-20160821193359-2b70b4024ed5 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	go4.org/netipx v0.0.0-20220725152314-7e7bdc8411bf // indirect
	golang.org/x/exp/typeparams v0.0.0-20231108232855-2478ac86f678 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	golang.org/x/tools v0.47.0 // indirect
	golang.org/x/tools/go/expect v0.1.1-deprecated // indirect
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2 // indirect
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c // indirect
	honnef.co/go/tools v0.5.1 // indirect
)

// Пакет tun/netstack существует и внутри модуля golang.zx2c4.com/wireguard,
// и как отдельный модуль. Без exclude команда go mod tidy видит оба и падает
// с ambiguous import. Нам нужен тот, что внутри wireguard.
exclude golang.zx2c4.com/wireguard/tun/netstack v0.0.0-20220703234212-c31a7b1ab478

// tailscale/wf — из своего форка: арена библиотеки отдавала невыровненную
// память, и запись правила WFP во время разметки GC роняла процесс
// («bulkBarrierPreWrite: unaligned arguments», найдено живым прогоном S1 Э2).
// В форке одна правка, malloc.go, с тестом; апстрим с 2024 года не
// развивается. Регрессия у нас — TestDNSGuardUnderGC в internal/winsys.
replace github.com/tailscale/wf => github.com/FreedomCats/tailscale-wf v0.0.0-20261016000000-b03557658b85

// jchv/go-webview2 — из своего форка: библиотека не передаёт опций окружения
// WebView2, а переменную WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS рантайм с её
// встроенным загрузчиком не применяет — окно капчи шло мимо прокси qwdtt-cli
// (найдено приёмкой R2 Э3). В форке одна правка: опция
// AdditionalBrowserArguments через ICoreWebView2EnvironmentOptions, с тестом.
// Регрессия у нас — TestWebView2Proxy в internal/captcha (QWDTT_WEBVIEW2=1).
replace github.com/jchv/go-webview2 => github.com/FreedomCats/go-webview2 v0.0.0-20261014000000-08b745a2490c
