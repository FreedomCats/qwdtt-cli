// connect.go — команда qwdtt-cli connect.
//
// Флаги и разбор аргументов — задача G1. Здесь (задача G2) всё сделанное
// раньше собирается в одну команду: профиль и флаги → core.Config, секреты
// из хранилища, выбор datapath, решатель капчи, цикл подключения.
//
// Сборка отделена от запуска: planConnect ничего не трогает в сети и возвращает
// готовый план, а run уже поднимает туннель. Так план проверяется тестами без
// живого сервера, а сама команда остаётся коротким перечнем шагов.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/captcha"
	"github.com/FreedomCats/qwdtt-cli/internal/config"
	"github.com/FreedomCats/qwdtt-cli/internal/core"
	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
	"github.com/FreedomCats/qwdtt-cli/internal/netguard"
	"github.com/FreedomCats/qwdtt-cli/internal/supervisor"
)

// dnsProbeTimeout — сколько ждать резолвер при проверке шага «dns». Больше
// нескольких секунд ждать незачем: живой резолвер отвечает за доли секунды,
// а мёртвый лучше объявить мёртвым и пойти на следующую попытку.
const dnsProbeTimeout = 5 * time.Second

func connectCommand() *command {
	return &command{
		name:    "connect",
		args:    "[флаги] [профиль]",
		summary: "поднять туннель и отдавать трафик через SOCKS5 или WireGuard",
		description: `Настройки берутся из профиля в profiles.toml. Имя профиля можно не называть,
если в файле он один или назван в default_profile. Флаг перекрывает то же поле
профиля; всё, что флагом не названо, остаётся как в профиле.

Пароль подключения и хеши звонков VK в командную строку не передаются: они
лежат в хранилище секретов, а профиль держит только ссылки на них. Записывает
их qwdtt-cli profile set.

Ход подключения печатается строками в stdout; вопросы к пользователю — капча,
которую ядро не осилило само, — уходят в stderr. Первый Ctrl+C останавливает
туннель и снимает datapath, второй убивает процесс.`,
		run: runConnect,
	}
}

// connectOptions — значения флагов connect.
//
// Поле named говорит, какие флаги действительно назвали в командной строке:
// без него «-workers 0» не отличить от пропущенного флага, а перекрывать
// профиль нулём, которого не просили, нельзя.
type connectOptions struct {
	common *commonOptions

	peer          string
	workers       int
	obfs          string
	turnTransport string
	dtls          bool
	dns           string
	datapath      string
	socksListen   string
	noKillSwitch  bool
	allowLeak     bool

	named map[string]bool
}

// addConnectFlags объявляет флаги connect. Их набор повторяет поля профиля
// (architecture.md §10): что можно записать в файл, то можно назвать и разово
// в командной строке. Умолчания в подсказках не пишутся — умолчание берётся
// из профиля, а не из флага, и напечатать здесь можно было бы только неправду.
func addConnectFlags(fs *flag.FlagSet) *connectOptions {
	o := &connectOptions{common: addCommonFlags(fs)}

	fs.StringVar(&o.peer, "peer", "", "адрес сервера в виде host:port")
	fs.IntVar(&o.workers, "workers", 0, "число воркеров: кратно девяти, потолок 108")
	fs.StringVar(&o.obfs, "obfs", "", "режим RTP-маскировки: audio или video")
	fs.StringVar(&o.turnTransport, "turn-transport", "", "транспорт до TURN-relay: udp или tcp")
	fs.BoolVar(&o.dtls, "dtls", true, "поднимать DTLS поверх TURN; -dtls=false — прямой RTP-obfs AEAD")
	fs.StringVar(&o.dns, "dns", "", "резолвер для доменов VK: yandex, cloudflare, google, doh-*, custom:IP, doh:URL")
	fs.StringVar(&o.datapath, "datapath", "", "как отдавать трафик: socks, tun или external")
	fs.StringVar(&o.socksListen, "socks-listen", "", "адрес локального SOCKS5 при datapath=socks")
	// Выключить на запуск, а не включить: включён он и так, если профиль
	// не выключил (plan-e3.md, решение 2).
	fs.BoolVar(&o.noKillSwitch, "no-kill-switch", false,
		"выключить kill-switch на этот запуск: "+killSwitchUsage)
	// Флаг без поля в профиле: согласие на утечку даётся на один запуск, а не
	// записывается навсегда (plan-e2.md, W4). Один на защиту DNS и
	// kill-switch: отказывают они по одной причине — nftables или WFP
	// (plan-e3.md, решение 3). -allow-dns-leak — прежнее имя.
	fs.BoolVar(&o.allowLeak, "allow-leak", false,
		"подключаться, даже если защита от утечки DNS и kill-switch (tun, external) не включились")
	fs.BoolVar(&o.allowLeak, "allow-dns-leak", false, "прежнее имя -allow-leak")

	return o
}

// killSwitchUsage — что делает kill-switch, для подсказок флагов connect и
// profile set.
const killSwitchUsage = "в tun и external он блокирует трафик системы, пока туннель не поднят"

func runConnect(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(connectCommand())
	opts := addConnectFlags(fs)
	rest, err := parseWithArgs(e, fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("лишний аргумент %q: профиль называется один", rest[1])
	}
	opts.named = setFlags(fs)

	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}

	plan, err := planConnect(opts, name)
	if err != nil {
		return err
	}
	return exhaustedHint(plan.run(ctx, e, opts.common.verbose), plan.profile.Name)
}

// exhaustedHint дописывает к ошибке «годных хешей не осталось», что делать
// дальше. Звонки закрылись — сеанс так обычно и кончается, и выход один: новые
// ссылки. Меню спрашивает их само (menu.connect), здесь только подсказка.
func exhaustedHint(err error, profile string) error {
	if !errors.Is(err, supervisor.ErrHashesExhausted) {
		return err
	}
	return fmt.Errorf("%w; заведите новые ссылки: qwdtt-cli profile set %s -hashes", err, profile)
}

// ──────────────────────────── сборка ────────────────────────────

// connectPlan — всё, что собрано до входа в цикл подключения. Сети здесь
// ещё нет: план можно собрать и выбросить.
type connectPlan struct {
	// profile — профиль с флагами поверх. Секретов в нём по-прежнему нет.
	profile *config.Profile
	// cfg — настройки ядра, уже с паролем и хешами, приведённые к канону
	// и проверенные.
	cfg core.Config

	// datapath строит реализацию для одной попытки. Вывод ей назначается
	// при запуске: принтера на этапе сборки ещё нет.
	datapath func(logf func(string, ...any)) datapath.Datapath
	// stage — какой шаг закрывает подъём datapath: socks или vpn.
	stage core.Stage
	// describe — datapath одной фразой для заголовка: «SOCKS5 на 127.0.0.1:1080».
	describe string

	// allowLeak — -allow-leak: отказ защиты DNS и kill-switch не мешает
	// подключению.
	allowLeak bool
	// killSwitch — kill-switch включён (профиль, -no-kill-switch). Действует
	// только у datapath с защитой DNS: это та же сессия WFP (таблица nftables).
	killSwitch bool
	// killSwitchOff — чем kill-switch выключен, для строки до подключения;
	// пусто — включён.
	killSwitchOff string
	// dnsGuard — защита DNS для datapath, которые её просят; открывается
	// в run, до первой попытки. nil — нет (socks или -allow-leak).
	dnsGuard datapath.DNSGuard
}

// planConnect собирает план: профиль с флагами поверх, секреты из хранилища,
// настройки ядра и datapath.
func planConnect(opts *connectOptions, name string) (*connectPlan, error) {
	path, p, err := profileWithFlags(opts, name)
	if err != nil {
		return nil, err
	}

	cfg, err := coreConfig(p, config.NewSecretStoreFor(path))
	if err != nil {
		return nil, err
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	plan := &connectPlan{profile: p, cfg: cfg, allowLeak: opts.allowLeak, killSwitch: p.KillSwitchOn()}
	switch {
	case plan.killSwitch:
	case opts.named["no-kill-switch"] && opts.noKillSwitch:
		plan.killSwitchOff = "-no-kill-switch"
	default:
		plan.killSwitchOff = "kill_switch = false в профиле"
	}
	if err := plan.chooseDatapath(); err != nil {
		return nil, err
	}
	return plan, nil
}

// profileWithFlags читает файл настроек, выбирает профиль и кладёт поверх
// названные флаги. Общая часть connect, ping и hashes: у диагностических
// команд флагов меньше, но действуют они так же — один запуск, файл не
// трогается. Возвращает путь к файлу: по нему открывается хранилище секретов.
func profileWithFlags(opts *connectOptions, name string) (string, *config.Profile, error) {
	path, file, err := loadProfiles(opts.common.config)
	if err != nil {
		return "", nil, err
	}
	base, err := file.Profile(name)
	if err != nil {
		return "", nil, err
	}

	p := overrideProfile(base, opts)
	if err := p.Validate(); err != nil {
		return "", nil, err
	}
	return path, p, nil
}

// overrideProfile кладёт названные флаги поверх профиля и возвращает копию:
// профиль из файла остаётся как есть, флаг действует один запуск.
//
// Только названные: у флага есть умолчание, и подставить его вместо поля
// профиля значило бы перекрыть профиль тем, чего никто не просил.
func overrideProfile(base *config.Profile, opts *connectOptions) *config.Profile {
	p := *base

	if opts.named["peer"] {
		p.Peer = opts.peer
	}
	if opts.named["workers"] {
		p.Workers = opts.workers
	}
	if opts.named["obfs"] {
		p.Obfs = opts.obfs
	}
	if opts.named["turn-transport"] {
		p.TurnTransport = opts.turnTransport
	}
	if opts.named["dtls"] {
		dtls := opts.dtls
		p.DTLS = &dtls
	}
	if opts.named["dns"] {
		p.DNS = opts.dns
	}
	if opts.named["datapath"] {
		p.Datapath = opts.datapath
	}
	if opts.named["socks-listen"] {
		p.Socks.Listen = opts.socksListen
	}
	if opts.named["no-kill-switch"] && opts.noKillSwitch {
		off := false
		p.KillSwitch = &off
	}
	return &p
}

// coreConfig переводит профиль в настройки ядра и разворачивает ссылки на
// секреты. Profile.Core оставляет пароль и хеши пустыми намеренно (C1):
// хранилище открывает тот, кто знает, где лежит файл настроек.
//
// Секреты разворачиваются по отдельности: `qwdtt-cli hashes` (G3) обходится одними
// хешами, и отсутствие пароля ему не помеха.
func coreConfig(p *config.Profile, store config.SecretStore) (core.Config, error) {
	cfg := p.Core()

	password, err := secretPassword(p, store)
	if err != nil {
		return cfg, err
	}
	hashes, err := secretHashes(p, store)
	if err != nil {
		return cfg, err
	}

	cfg.Password = password
	cfg.Hashes = hashes
	return cfg, nil
}

// secretPassword разворачивает ссылку на пароль подключения.
func secretPassword(p *config.Profile, store config.SecretStore) (string, error) {
	password, err := store.Get(p.PasswordRef)
	if err != nil {
		return "", secretError("пароль подключения", p.Name, "-password", err)
	}
	return password, nil
}

// secretHashes разворачивает ссылку на хеши звонков VK и режет их списком.
//
// Хеши лежат одной строкой, как их ввёл пользователь; режет её ядро — тем же
// разбором, что применяется к флагу апстрима.
func secretHashes(p *config.Profile, store config.SecretStore) ([]string, error) {
	raw, err := store.Get(p.VkHashesRef)
	if err != nil {
		return nil, secretError("хеши звонков VK", p.Name, "-hashes", err)
	}

	hashes := core.ParseHashes(raw)
	if len(hashes) == 0 {
		return nil, fmt.Errorf("в хранилище нет ни одного хеша звонка для профиля %q; "+
			"запишите их заново: qwdtt-cli profile set %s -hashes", p.Name, p.Name)
	}
	return hashes, nil
}

// secretError объясняет, что секрет не развернулся и как это починить. Само
// значение здесь не появляется ни при каком исходе.
func secretError(what, profile, flag string, err error) error {
	if errors.Is(err, config.ErrKeyringLocked) {
		// Секрет цел: человек закрыл окно разблокировки, а не потерял пароль.
		return fmt.Errorf("%s профиля %q не прочитан: %w; разблокируйте связку ключей и повторите",
			what, profile, err)
	}
	return fmt.Errorf("%s профиля %q не прочитан: %w; запишите его заново: qwdtt-cli profile set %s %s",
		what, profile, err, profile, flag)
}

// chooseDatapath выбирает реализацию по профилю. Ядру о выборе знать незачем
// (D2): настройки datapath идут его конструктору, а не в core.Config.
func (plan *connectPlan) chooseDatapath() error {
	switch config.EffectiveDatapath(plan.profile.Datapath) {
	case config.DatapathSocks:
		listen := strings.TrimSpace(plan.profile.Socks.Listen)
		if listen == "" {
			listen = datapath.DefaultSocksListen
		}
		plan.datapath = func(logf func(string, ...any)) datapath.Datapath {
			return datapath.NewSocks(datapath.SocksOptions{Listen: listen, Logf: logf})
		}
		plan.stage = core.StageSocks
		plan.describe = "SOCKS5 на " + listen
		return nil

	case config.DatapathTun:
		plan.datapath = func(logf func(string, ...any)) datapath.Datapath {
			return datapath.NewTun(datapath.TunOptions{Logf: logf, DNSGuard: plan.dnsGuard})
		}
		plan.stage = core.StageVPN
		plan.describe = "адаптер " + datapath.DefaultTunnelName + ", весь трафик системы"
		return nil

	case config.DatapathExternal:
		plan.datapath = func(logf func(string, ...any)) datapath.Datapath {
			return datapath.NewExternal(datapath.ExternalOptions{Logf: logf, DNSGuard: plan.dnsGuard})
		}
		plan.stage = core.StageVPN
		plan.describe = "штатный WireGuard, туннель " + datapath.DefaultTunnelName
		return nil

	default:
		// Validate уже отверг всё остальное с объяснением.
		return fmt.Errorf("datapath %q не поддерживается", plan.profile.Datapath)
	}
}

// requirementsError объясняет одной ошибкой, почему datapath не запустится,
// или возвращает nil. Неподдерживаемая система проверяется первой: права
// и файлы тут не помогут, и советовать их — значит отправить человека
// искать wintun.dll на Linux.
func requirementsError(describe string, req datapath.Requirements) error {
	if req.Unsupported != nil {
		return fmt.Errorf("%w; на этой системе работает -datapath socks", req.Unsupported)
	}
	if problems := requirementProblems(req); len(problems) > 0 {
		// socks не требует ничего, так что проблемы бывают только у остальных,
		// и у них всегда есть выход попроще (architecture.md §09).
		return errors.New("datapath " + describe + ": " + strings.Join(problems, "; ") +
			"; без прав и установленных файлов работает -datapath socks")
	}
	return nil
}

// requirementProblems проверяет то, что datapath просит для запуска: права
// администратора (root), внешние программы и файлы рядом с бинарём.
func requirementProblems(req datapath.Requirements) []string {
	var problems []string

	if req.Admin && !isElevated() {
		problems = append(problems, elevationHint)
	}

	for _, program := range req.Programs {
		if _, err := exec.LookPath(program); err != nil {
			problems = append(problems, fmt.Sprintf("не найдена программа %s", program))
		}
	}

	if len(req.Files) > 0 {
		dir := "."
		if exe, err := os.Executable(); err == nil {
			dir = filepath.Dir(exe)
		}
		for _, file := range req.Files {
			if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
				problems = append(problems, fmt.Sprintf("рядом с qwdtt-cli нет файла %s", file))
			}
		}
	}
	return problems
}

// captchaSolver выбирает решатель капчи: окно WebView2, где оно возможно
// (E3), иначе консольный (E2). Консольный — запасное дно: работает всегда.
//
// Сообщения обоих уходят в stderr через out.aside, чтобы статусная строка
// не встала посреди приглашения; консольный читает ответ со stdin команды.
//
// При kill-switch (ks не nil) консольный снимает его на время капчи: страницу
// открывает браузер пользователя, а не qwdtt-cli (plan-e3.md, K4). Окно WebView2
// ходит через прокси qwdtt-cli, соединяясь dial (сокетами qwdtt-cli мимо туннеля, W2), и
// kill-switch не трогает; не поднялся прокси — окно без него, тоже со снятием.
func captchaSolver(e *env, out *printer, ks *killSwitchGuard, dial captcha.DialFunc) captcha.Solver {
	wrap := func(s captcha.Solver) captcha.Solver {
		if ks == nil {
			return s
		}
		return killSwitchCaptcha{Solver: s, guard: ks}
	}
	console := wrap(captcha.NewManual(captcha.ManualOptions{In: e.stdin, Out: out.aside(e.stderr), Open: e.openURL}))

	if e.captchaWindow == nil {
		return console
	}
	// По SSH окно откроется не там, где сидит пользователь, а то и вовсе
	// нигде: сеанс службы рабочего стола не имеет. Консоль же у него под рукой.
	if os.Getenv("SSH_CONNECTION") != "" {
		return console
	}

	opts := captcha.WebView2Options{Out: out.aside(e.stderr)}
	if out.verbose {
		opts.Logf = out.logf
	}
	window, err := e.captchaWindow(opts)
	if err != nil {
		// Вне Windows окна и не ждут — предупреждать не о чем.
		if !errors.Is(err, errors.ErrUnsupported) {
			fmt.Fprintf(e.stderr, "Предупреждение: %v — капчу, если VK её попросит, придётся решать через консоль\n", err)
		}
		return console
	}
	if ks == nil || dial == nil {
		return wrap(window)
	}
	opts.Dial = dial
	proxied, err := e.captchaWindow(opts)
	if err != nil {
		return wrap(window)
	}
	return proxiedCaptcha{Solver: proxied, direct: wrap(window), logf: out.logf}
}

// newCaptchaWindow — настоящий решатель с окном для env.captchaWindow.
// Отдельной функцией, чтобы не вернуть типизированный nil в интерфейсе.
func newCaptchaWindow(opts captcha.WebView2Options) (captcha.Solver, error) {
	window, err := captcha.NewWebView2(opts)
	if err != nil {
		return nil, err
	}
	return window, nil
}

// ──────────────────────────── запуск ────────────────────────────

// run поднимает туннель по плану и держит его до Ctrl+C или до тех пор, пока
// годных хешей не останется.
func (plan *connectPlan) run(ctx context.Context, e *env, verbose bool) error {
	out := newPrinter(e.stdout, verbose)
	// Статусная строка — только на терминале. В перенаправленном выводе
	// остаются строки: `qwdtt-cli connect > log.txt` даёт файл без перерисовки.
	if width, ok := e.stdoutTerminal(); ok {
		out.enableStatus(width)
	}
	defer out.finish()

	// Требования спрашиваются у экземпляра: они свойство реализации, а
	// экземпляр без Start сети не трогает.
	probe := plan.datapath(nil)
	req := probe.Requires()
	if err := requirementsError(plan.describe, req); err != nil {
		return err
	}
	if req.Warning != "" {
		fmt.Fprintf(e.stderr, "Предупреждение: %s\n", req.Warning)
	}
	// Общее на машину (адаптер tun) занимается до первого похода в сеть
	// и держится весь сеанс: занятый — это ошибка сейчас, а не цикл
	// переподключений с запросами кредов VK.
	if r, ok := probe.(datapath.Reserver); ok {
		release, err := r.Reserve()
		if err != nil {
			return err
		}
		defer release()
	}
	// DNS туннеля в системе (Linux): способ — до сети, остатки упавшего
	// запуска — после резерва адаптера.
	if req.DNSGuard {
		if err := prepareSystemDNS(out); err != nil {
			return err
		}
	}
	// Защита DNS и kill-switch (сессия WFP, на Linux — таблица nftables) —
	// тоже до сети и на весь сеанс: отказ в Start супервизор принял бы за
	// сбой попытки и ушёл бы в переподключения. Kill-switch закрывает
	// систему уже здесь, до первой попытки (plan-e3.md, решение 5).
	var killSwitch *killSwitchGuard
	if req.DNSGuard {
		setup, err := setupDNSGuard(out, e.stderr, plan.allowLeak, plan.killSwitchOff)
		if err != nil {
			return err
		}
		defer setup.close()
		plan.dnsGuard, killSwitch = setup.guard, setup.killSwitch
	}

	cfg := plan.cfg
	var guard *netguard.Guard
	if req.Netguard || netguardForced() {
		// Метка сокетов ядра — только при включённой защите DNS: по ней
		// nftables пропускает qwdtt-cli к DNS VK, а при kill-switch — и вообще
		// (он живёт в той же таблице, без защиты DNS его не бывает).
		var mark uint32
		if plan.dnsGuard != nil {
			mark = dnsGuardMark
		}
		var err error
		if guard, err = setupNetguard(out, verbose, req.Netguard, mark); err != nil {
			return err
		}
		cfg.DialControl = guard.Control
	}

	// Ручка ядра создаётся до запуска и раздаётся обоим, кому она нужна:
	// циклу подключения и диспетчеру капчи, который отвечает через неё.
	ctrl := core.NewControl()

	// Окно капчи при kill-switch ходит сокетами qwdtt-cli: тем же хуком netguard,
	// что и ядро, и его же резолвером (net.DefaultResolver ядро подменяет
	// при запуске, а Dialer без Resolver берёт его в момент соединения).
	var captchaDial captcha.DialFunc
	if killSwitch != nil && guard != nil {
		captchaDial = (&net.Dialer{Control: guard.Control}).DialContext
	}
	dispatcher := captcha.New(captcha.Options{Solver: captchaSolver(e, out, killSwitch, captchaDial), Control: ctrl, Logf: out.logf})
	defer dispatcher.Close()
	var own ownAdapter
	tun, err := supervisor.NewTunnel(supervisor.Options{
		Config: cfg,
		Hashes: cfg.Hashes,
		NewDatapath: func() (datapath.Datapath, error) {
			return own.track(plan.datapath(out.logf)), nil
		},
		DatapathStage: plan.stage,
		Control:       ctrl,
		ProbeDNS: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, dnsProbeTimeout)
			defer cancel()
			return core.ProbeDNS(ctx, cfg)
		},
		// Диспетчер капчи подключается в тот же поток событий, что и вывод:
		// его Observe возвращается сразу, решение уходит в свою горутину.
		OnEvent: func(ev core.Event) {
			out.event(ev)
			dispatcher.Observe(ev)
		},
		OnTransition: out.transition,
		Logf:         out.logf,
		KillSwitch:   killSwitch.lockedFunc(),
	})
	if err != nil {
		return err
	}
	// Смена сети: netguard переставляет индексы, попытка перезапускается.
	// Сокеты, привязанные к ушедшему интерфейсу, иначе молчали бы до
	// watchdog'а.
	if guard != nil {
		defer watchNetwork(out, guard, tun.Restart, own.id, req.Netguard)()
	}

	out.header(cfg.Workers, len(cfg.Hashes))
	out.line("профиль %s: сервер %s, %s, воркеров %d, хешей %d, DNS %s",
		plan.profile.Name, cfg.Peer, plan.describe, cfg.Workers, len(cfg.Hashes), cfg.DNS)

	// Аптайм в статусной строке идёт и без событий: раз в секунду она
	// перерисовывается сама. Тикер снимается до того, как печатается итог.
	stopRefresh := out.refreshEvery(time.Second)
	err = tun.Run(ctx)
	stopRefresh()
	out.finish()
	if err != nil {
		return err
	}
	out.line("остановлено")
	return nil
}
