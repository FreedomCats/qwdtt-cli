// run.go — запуск ядра. Файл наш: из апстрима не переносится и при обновлении
// ядра не затирается (см. scripts/vendorcore).
//
// Апстримное ядро было программой: func main() разбирала флаги, ловила сигналы,
// читала команды из stdin и заканчивала работу через os.Exit. Всё это принадлежит
// cmd/qwdtt-cli, а не библиотеке, поэтому func main() при переносе вырезается целиком, и
// то, что от неё осталось по существу, переписано здесь: Run поднимает туннель,
// RunPing и RunHashCheck заменяют ветки -ping-only и -check-hashes.
//
// Три точки входа устроены одинаково: назначить получателя событий, привести
// настройки к канону, проверить их, разложить по глобальному состоянию ядра —
// и только потом работать.

package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
)

// pingStreamID — номер потока, под которым замер задержки ходит за кредами.
// Значение из апстрима: оно же служит ключом кэша кредов и не должно совпадать
// с номерами рабочих потоков.
const pingStreamID = 999

// Run поднимает туннель и возвращается, когда тот закончился.
//
// Остановка — только отменой ctx; отдельного Stop нет намеренно. Остановка по
// запросу ошибкой не считается и даёт nil. Ошибка возвращается, если туннель не
// удалось поднять или он развалился сам — по ней супервизор решает, повторять ли
// попытку (architecture.md §07).
//
// sink получает события из горутин воркеров, поэтому обязан быть безопасным для
// параллельного вызова; см. Sink. Назначается он первым делом: до этого ядро немо.
//
// ctrl — ручка управления запущенным ядром: пауза, ответ на капчу, ответ на
// запрос TURN-кредов (см. Control). Её создаёт вызывающий, потому что Run
// возвращается только вместе с туннелем и отдать её сам не успел бы. nil
// допустим и означает, что управлять ядром не понадобится.
func Run(ctx context.Context, cfg Config, sink Sink, ctrl *Control) error {
	// Готовый канал принадлежит Run целиком: закрыть его нужно и тогда, когда
	// до запуска дело не дошло, — иначе он висит до следующей попытки.
	if cfg.PacketConn != nil {
		defer func() { _ = cfg.PacketConn.Close() }()
	}
	setSink(sink)

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := applyGlobals(&cfg); err != nil {
		return err
	}

	peer, err := net.ResolveUDPAddr("udp", cfg.Peer)
	if err != nil {
		return fmt.Errorf("адрес сервера %q: %w", cfg.Peer, withoutSystemServer(err))
	}
	tp, err := turnParams(&cfg)
	if err != nil {
		return err
	}

	// Свой ctx поверх переданного: по нему снимаются воркеры, статистика и
	// datapath, когда Run возвращается сам. Родительский ctx при этом остаётся
	// нетронутым — по нему ниже видно, остановку запросил вызвавший или нет.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	localConn, err := openLocal(&cfg)
	if err != nil {
		return err
	}
	// Закрытий два, и оба нужны. AfterFunc обрывает чтение, которое иначе
	// висело бы на пустом сокете до конца света; defer гарантирует, что к
	// возврату из Run порт свободен, а не освободится когда-нибудь потом.
	// Повторное закрытие безвредно.
	context.AfterFunc(runCtx, func() { _ = localConn.Close() })
	defer func() { _ = localConn.Close() }()

	_, localPort, _ := net.SplitHostPort(cfg.Listen)
	if localPort == "" {
		localPort = "9000"
	}

	logStartup(&cfg)

	stats := NewStats()
	shutdownCh := make(chan struct{})
	go func() {
		<-runCtx.Done()
		close(shutdownCh)
	}()
	go stats.RunLoop(shutdownCh)

	disp := NewDispatcher(runCtx, localConn, stats)
	defer disp.Shutdown()

	configCh := make(chan string, 1)
	configDone := make(chan struct{})
	go func() {
		defer close(configDone)
		serveConfig(runCtx, configCh)
	}()

	// Флаг паузы читают все воркеры разом, и раздаётся он один раз, при старте
	// групп. Поэтому он живёт в Control, а не здесь: указатель, который получат
	// воркеры, и тот, которым управляет Pause, обязаны быть одним и тем же.
	// Без Control флаг всё равно нужен — воркеры читают его безусловно.
	if ctrl == nil {
		ctrl = NewControl()
	}

	var wg sync.WaitGroup
	startWorkers(runCtx, &wg, &cfg, tp, peer, disp, stats, localPort, configCh, ctrl.pauseFlag())

	wg.Wait()
	close(configCh)

	// Воркеров больше нет — держать datapath не на чем. Отмена нужна и тогда,
	// когда родительский ctx жив: иначе горутина статистики продолжала бы
	// работать, а ожидание конфига — висеть.
	cancel()
	<-configDone
	logln("[КЛИЕНТ] Все воркеры завершены")

	if ctx.Err() != nil {
		return nil
	}
	return errors.New("все воркеры завершились сами: туннель не держится")
}

// RunPing замеряет задержку до TURN-relay и возвращается. Заменяет ветку
// -ping-only, которая заканчивалась os.Exit(0) или os.Exit(1).
//
// Хеши перебираются по очереди: годится первый, по которому замер удался.
// Результат уходит событием EventPing, и та же ошибка возвращается наружу —
// чтобы CLI мог выставить код возврата, не разбирая события.
func RunPing(ctx context.Context, cfg Config, sink Sink) error {
	setSink(sink)

	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := applyGlobals(&cfg); err != nil {
		return err
	}

	peer, err := net.ResolveUDPAddr("udp", cfg.Peer)
	if err != nil {
		return fmt.Errorf("адрес сервера %q: %w", cfg.Peer, withoutSystemServer(err))
	}
	tp, err := turnParams(&cfg)
	if err != nil {
		return err
	}

	var lastErr error
	for i, hash := range cfg.Hashes {
		user, pass, turnURLs, err := GetCreds(ctx, hash, pingStreamID)
		if err != nil {
			lastErr = fmt.Errorf("хеш %d: получение TURN-кредов: %w", i+1, err)
			continue
		}
		creds := &Credentials{User: user, Pass: pass, TurnURLs: turnURLs, CacheStreamID: pingStreamID}

		rtt, err := runPing(ctx, tp, peer, creds)
		if err != nil {
			lastErr = fmt.Errorf("хеш %d: замер: %w", i+1, err)
			continue
		}
		emitPing(rtt)
		return nil
	}
	return emitPingError(lastErr)
}

// RunHashCheck проверяет хеши звонков VK по одному и возвращается. Заменяет
// ветку -check-hashes.
//
// Настройки проверяются не целиком: туннель здесь не поднимается, поэтому ни
// адрес сервера, ни пароль не нужны — нужен только непустой список хешей.
// Исход каждой проверки уходит событием EventHashCheck; ошибку возвращает
// только сам разбор настроек.
func RunHashCheck(ctx context.Context, cfg Config, sink Sink) error {
	setSink(sink)

	cfg.Normalize()
	if len(cfg.Hashes) == 0 {
		return errors.New("настройки проверки: не задано ни одного хеша звонка VK")
	}
	if err := applyGlobals(&cfg); err != nil {
		return err
	}

	logf("[КЛИЕНТ] VK auth mode: %s (hash-check)", cfg.VkAuthMode)
	if cfg.VkAuthMode == VkAuthAnonymous {
		logf("[КЛИЕНТ] VK anon path: %s", cfg.VkAnonPath)
	}
	logf("[КЛИЕНТ] Captcha mode: %s", cfg.CaptchaMode)

	runHashChecks(ctx, cfg.Hashes)
	return nil
}

// applyGlobals раскладывает настройки по глобальному состоянию ядра.
//
// Режим капчи, режим авторизации VK и резолвер живут в апстриме пакетными
// переменными, и добраться до них можно только так. Отсюда следует ограничение,
// которое стоит держать в голове: в одном процессе может работать только один
// Run одновременно.
func applyGlobals(cfg *Config) error {
	// Хук ставится первым: резолвер ниже и всё, что пойдёт после, создают
	// сокеты уже с ним.
	setDialControl(cfg.DialControl)
	setCaptchaMode(cfg.CaptchaMode)
	setVkAuthMode(cfg.VkAuthMode)
	setVkAnonPath(cfg.VkAnonPath)
	setupGlobalResolver(cfg.DNS)

	// Файл с TURN-кредами от аккаунта VK читает ядро: его формат — часть
	// протокола с VK, а не настроек клиента. Пустой путь означает «нет файла».
	if err := loadVkCredsFile(cfg.VkCredsFile); err != nil {
		return fmt.Errorf("файл с TURN-кредами VK %q: %w", cfg.VkCredsFile, err)
	}
	return nil
}

// turnParams собирает параметры транспорта, общие для всех воркеров.
func turnParams(cfg *Config) (*TurnParams, error) {
	wrapKey, err := deriveWrapKey(cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("ключ маскировки трафика: %w", err)
	}
	return &TurnParams{
		Host:         cfg.TurnHost,
		Port:         cfg.TurnPort,
		Hashes:       cfg.Hashes,
		WrapKey:      wrapKey,
		ObfsMode:     cfg.ObfsMode,
		NoDTLS:       cfg.NoDTLS,
		TCPTransport: cfg.TurnTCP,
	}, nil
}

// startWorkers разводит воркеров по группам и запускает их каскадом.
//
// Группа — workersPerGroup воркеров на одних кредах; хеш звонка у группы свой,
// по её номеру. Каскад нужен, чтобы сотня воркеров не ломилась к VK разом:
// каждая группа ждёт сигнала от предыдущей. Конфиг WireGuard приходит только от
// первой группы — остальным канал не передаётся вовсе.
func startWorkers(
	ctx context.Context,
	wg *sync.WaitGroup,
	cfg *Config,
	tp *TurnParams,
	peer *net.UDPAddr,
	disp *Dispatcher,
	stats *Stats,
	localPort string,
	configCh chan<- string,
	pauseFlag *int32,
) {
	numGroups := (cfg.Workers + workersPerGroup - 1) / workersPerGroup
	workerID := 1

	var prevReady <-chan struct{}

	for g := 0; g < numGroups; g++ {
		startIdx := g * workersPerGroup
		endIdx := min(startIdx+workersPerGroup, cfg.Workers)
		if endIdx <= startIdx {
			continue
		}

		ids := make([]int, 0, endIdx-startIdx)
		for range endIdx - startIdx {
			ids = append(ids, workerID)
			workerID++
		}

		isFirst := g == 0

		waitReady := prevReady
		var signalReady chan<- struct{}
		if g < numGroups-1 {
			ready := make(chan struct{})
			signalReady = ready
			prevReady = ready
		}

		var groupConfigCh chan<- string
		if isFirst {
			groupConfigCh = configCh
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			WorkerGroup(ctx, g+1, g, tp, peer, disp, localPort,
				isFirst, groupConfigCh, ids, pauseFlag, cfg.DeviceID, cfg.Password, stats,
				waitReady, signalReady)
		}()
	}
}

// serveConfig ждёт конфиг WireGuard от первой группы воркеров и отдаёт его наружу.
//
// В апстриме та же горутина писала конфиг в файл wg-turn.conf в текущем каталоге —
// вместе с приватным ключом — и сама же поднимала userspace WireGuard с SOCKS5.
// Ядро ничего этого не делает: конфиг уходит событием EventConfig, а что с ним
// делать дальше, решает internal/datapath (architecture.md §04).
func serveConfig(ctx context.Context, configCh <-chan string) {
	var fromServer string
	select {
	case conf, ok := <-configCh:
		if !ok || conf == "" {
			return
		}
		fromServer = conf
	case <-ctx.Done():
		return
	}

	emit(EventConfig{WireGuard: withMTU(fromServer)})
}

// openLocal — канал к datapath: готовый PacketConn или UDP на Listen
// (SO_REUSEADDR — быстрый перезапуск без «address already in use»).
func openLocal(cfg *Config) (net.PacketConn, error) {
	if cfg.PacketConn != nil {
		return cfg.PacketConn, nil
	}
	conn, err := listenUDP(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("локальный слушатель %s: %w", cfg.Listen, err)
	}
	if uc, ok := conn.(*net.UDPConn); ok {
		_ = uc.SetReadBuffer(socketBufSize)
		_ = uc.SetWriteBuffer(socketBufSize)
	}
	return conn, nil
}

// withMTU дописывает MTU в секцию [Interface], если сервер его не прислал.
// 1280 — минимум, гарантированный IPv6, и запас под все заголовки туннеля.
func withMTU(conf string) string {
	if strings.Contains(conf, "MTU =") {
		return conf
	}

	lines := strings.Split(conf, "\n")
	out := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		out = append(out, line)
		if strings.TrimSpace(line) == "[Interface]" {
			out = append(out, "MTU = 1280")
		}
	}
	return strings.Join(out, "\n")
}

// logStartup пишет в лог то, с чем ядро вышло на линию.
//
// Рамок из символов псевдографики, которыми апстрим обрамлял этот блок, здесь
// нет: как выглядит вывод, решает получатель EventLog, а не библиотека.
func logStartup(cfg *Config) {
	numGroups := (cfg.Workers + workersPerGroup - 1) / workersPerGroup

	logf("[КЛИЕНТ] VK auth mode: %s", cfg.VkAuthMode)
	if cfg.VkAuthMode == VkAuthAnonymous {
		logf("[КЛИЕНТ] VK anon path: %s", cfg.VkAnonPath)
	}
	logf("[КЛИЕНТ] Воркеров: %d (групп: %d, по %d)", cfg.Workers, numGroups, workersPerGroup)
	logf("[КЛИЕНТ] Хешей: %d", len(cfg.Hashes))
	if cfg.PacketConn != nil {
		logf("[КЛИЕНТ] Datapath: в памяти, без сокета | Пир: %s", cfg.Peer)
	} else {
		logf("[КЛИЕНТ] Слушаю: %s | Пир: %s", cfg.Listen, cfg.Peer)
	}

	if cfg.TurnTCP {
		logln("[КЛИЕНТ] TURN-транспорт: TCP")
	} else {
		logln("[КЛИЕНТ] TURN-транспорт: UDP")
	}

	// Строки «Режим» и «SOCKS5» из апстрима сюда не переехали: чем занят
	// datapath, ядро больше не знает — оно только отдаёт конфиг событием.

	// В апстриме здесь была развилка «WRAP: ON / OFF»: ключ приходил флагом и
	// мог не прийти вовсе. Теперь он выводится из пароля, а без пароля Validate
	// не пропускает настройки — выключенным WRAP быть уже не может.
	logln("[WRAP] Ключ выведен из пароля, режим RTP AEAD активен")
	emitStage(StageWrap, StageOK, 0, "")

	logf("[КЛИЕНТ] Device ID: %s", cfg.DeviceID)
	logf("[КЛИЕНТ] Captcha: %s", captchaPlan(cfg.CaptchaMode))
}

// captchaPlan описывает словами, каким путём ядро пойдёт решать капчу.
// Шаги через WebView появятся в Э2; до тех пор их место занимает ручной режим.
func captchaPlan(mode string) string {
	switch mode {
	case "wv":
		return "WBV: решает внешний WebView"
	case "rjs":
		return "RJS: Go v2, запасной путь — WBV Auto"
	default:
		return "AUTO: Go v2 ×2 → WBV Auto ×2 → Go v2 ×1 → ручной режим"
	}
}

// ──────────────────────────── проверка DNS ────────────────────────────

// dnsProbeHost — хост VK, по которому проверяется резолвер до запуска ядра.
// Тот же, с которого начинается путь за кредами (creds.go): если он не
// резолвится, дальше идти незачем. Android по той же ошибке — «lookup
// login.vk.ru» — показывает совет сменить DNS (TunnelManager.kt, err_vk_dns).
const dnsProbeHost = "login.vk.ru"

// ProbeDNS настраивает резолвер по cfg.DNS и проверяет, что он отвечает:
// резолвит хост VK. Шаг «dns» на схеме подключения закрывает по ней
// супервизор (architecture.md §06). Из остальных настроек нужен только
// cfg.DialControl: запрос к резолверу — такой же сокет ядра, как прочие.
//
// Резолвер настраивается здесь, а не в Run, чтобы проверка была честной:
// она идёт через тот же net.DefaultResolver, которым потом пользуется ядро,
// а не через системный. Run настраивает его ещё раз — это безвредно.
//
// Срок ожидания задаёт ctx: сама функция ничего не ограничивает.
func ProbeDNS(ctx context.Context, cfg Config) error {
	dns := strings.TrimSpace(cfg.DNS)
	if dns == "" {
		dns = DefaultConfig().DNS
	}
	setDialControl(cfg.DialControl)
	setupGlobalResolver(dns)

	addrs, err := net.DefaultResolver.LookupHost(ctx, dnsProbeHost)
	if err != nil {
		return fmt.Errorf("резолвер %s не отвечает: %w", goDNSLabel(dns), withoutSystemServer(err))
	}
	if len(addrs) == 0 {
		return fmt.Errorf("резолвер %s не нашёл адресов у %s", goDNSLabel(dns), dnsProbeHost)
	}
	return nil
}

// withoutSystemServer убирает из ошибки DNS адрес системного резолвера —
// у проверки DNS и у разрешения адреса сервера.
// net.Resolver подписывает ошибку сервером из /etc/resolv.conf («lookup … on
// 192.168.1.1:53»), хотя собственный Dial ядра отправил запрос на резолвер
// из настроек — его адрес и так виден дальше в тексте ошибки. Человек,
// проверяющий свой DNS, видел чужой адрес (найдено первым прогоном на Linux).
func withoutSystemServer(err error) error {
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) || dnsErr.Server == "" {
		return err
	}
	clean := *dnsErr
	clean.Server = ""
	return &clean
}
