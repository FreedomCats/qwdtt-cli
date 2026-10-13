// connect.go — команда tt connect.
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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"qwdtt-cli/internal/captcha"
	"qwdtt-cli/internal/config"
	"qwdtt-cli/internal/core"
	"qwdtt-cli/internal/datapath"
	"qwdtt-cli/internal/supervisor"
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
их tt profile set.

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
	listen        string

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
	fs.StringVar(&o.datapath, "datapath", "", "как отдавать трафик: socks или external")
	fs.StringVar(&o.socksListen, "socks-listen", "", "адрес локального SOCKS5 при datapath=socks")
	fs.StringVar(&o.listen, "listen", "", "локальный UDP-адрес, куда datapath отдаёт трафик")

	return o
}

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
	return plan.run(ctx, e, opts.common.verbose)
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
}

// planConnect собирает план: профиль с флагами поверх, секреты из хранилища,
// настройки ядра и datapath.
func planConnect(opts *connectOptions, name string) (*connectPlan, error) {
	path, file, err := loadProfiles(opts.common.config)
	if err != nil {
		return nil, err
	}
	base, err := file.Profile(name)
	if err != nil {
		return nil, err
	}

	p := overrideProfile(base, opts)
	if err := p.Validate(); err != nil {
		return nil, err
	}

	cfg, err := coreConfig(p, config.NewSecretStoreFor(path))
	if err != nil {
		return nil, err
	}
	if opts.named["listen"] {
		cfg.Listen = opts.listen
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	plan := &connectPlan{profile: p, cfg: cfg}
	if err := plan.chooseDatapath(); err != nil {
		return nil, err
	}
	return plan, nil
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
	return &p
}

// coreConfig переводит профиль в настройки ядра и разворачивает ссылки на
// секреты. Profile.Core оставляет пароль и хеши пустыми намеренно (C1):
// хранилище открывает тот, кто знает, где лежит файл настроек.
func coreConfig(p *config.Profile, store config.SecretStore) (core.Config, error) {
	cfg := p.Core()

	password, err := store.Get(p.PasswordRef)
	if err != nil {
		return cfg, secretError("пароль подключения", p.Name, "-password", err)
	}
	raw, err := store.Get(p.VkHashesRef)
	if err != nil {
		return cfg, secretError("хеши звонков VK", p.Name, "-hashes", err)
	}

	// Хеши лежат одной строкой, как их ввёл пользователь; режет её ядро —
	// тем же разбором, что применяется к флагу апстрима.
	hashes := core.ParseHashes(raw)
	if len(hashes) == 0 {
		return cfg, fmt.Errorf("в хранилище нет ни одного хеша звонка для профиля %q; "+
			"запишите их заново: tt profile set %s -hashes", p.Name, p.Name)
	}

	cfg.Password = password
	cfg.Hashes = hashes
	return cfg, nil
}

// secretError объясняет, что секрет не развернулся и как это починить. Само
// значение здесь не появляется ни при каком исходе.
func secretError(what, profile, flag string, err error) error {
	return fmt.Errorf("%s профиля %q не прочитан: %w; запишите его заново: tt profile set %s %s",
		what, profile, err, profile, flag)
}

// chooseDatapath выбирает реализацию по профилю. Ядру о выборе знать незачем
// (D2): настройки datapath идут его конструктору, а не в core.Config.
func (plan *connectPlan) chooseDatapath() error {
	switch strings.TrimSpace(plan.profile.Datapath) {
	case "", config.DatapathSocks:
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

	case config.DatapathExternal:
		return todo("D3", "datapath external")

	default:
		// Validate уже отверг всё остальное, включая wintun, с объяснением.
		return fmt.Errorf("datapath %q не поддерживается", plan.profile.Datapath)
	}
}

// requirementProblems проверяет то, что datapath просит для запуска: внешние
// программы и файлы рядом с бинарём. Проверка прав администратора — вместе
// с datapath external (D3): socks их не просит, а угадывать нечего.
func requirementProblems(req datapath.Requirements) []string {
	var problems []string

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
				problems = append(problems, fmt.Sprintf("рядом с tt нет файла %s", file))
			}
		}
	}
	return problems
}

// ──────────────────────────── запуск ────────────────────────────

// run поднимает туннель по плану и держит его до Ctrl+C или до тех пор, пока
// годных хешей не останется.
func (plan *connectPlan) run(ctx context.Context, e *env, verbose bool) error {
	out := newPrinter(e.stdout, verbose)
	// Статусная строка — только на терминале. В перенаправленном выводе
	// остаются строки: `tt connect > log.txt` даёт файл без перерисовки.
	if width, ok := e.stdoutTerminal(); ok {
		out.enableStatus(width)
	}
	defer out.finish()

	// Требования спрашиваются у экземпляра: они свойство реализации, а
	// экземпляр без Start сети не трогает.
	req := plan.datapath(nil).Requires()
	if problems := requirementProblems(req); len(problems) > 0 {
		return errors.New("datapath " + plan.describe + ": " + strings.Join(problems, "; "))
	}
	if req.Warning != "" {
		fmt.Fprintf(e.stderr, "Предупреждение: %s\n", req.Warning)
	}

	// Ручка ядра создаётся до запуска и раздаётся обоим, кому она нужна:
	// циклу подключения и диспетчеру капчи, который отвечает через неё.
	ctrl := core.NewControl()

	// Решатель капчи: консольный (E2). Вопрос и инструкция уходят в stderr,
	// ответ читается со stdin команды. Окно WebView2 — второй приём E2.
	// Через out.aside, чтобы статусная строка не встала посреди приглашения.
	solver := captcha.NewManual(captcha.ManualOptions{In: e.stdin, Out: out.aside(e.stderr), Open: e.openURL})
	dispatcher := captcha.New(captcha.Options{Solver: solver, Control: ctrl, Logf: out.logf})
	defer dispatcher.Close()

	cfg := plan.cfg
	tun, err := supervisor.NewTunnel(supervisor.Options{
		Config: cfg,
		Hashes: cfg.Hashes,
		NewDatapath: func() (datapath.Datapath, error) {
			return plan.datapath(out.logf), nil
		},
		DatapathStage: plan.stage,
		Control:       ctrl,
		ProbeDNS: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, dnsProbeTimeout)
			defer cancel()
			return core.ProbeDNS(ctx, cfg.DNS)
		},
		// Диспетчер капчи подключается в тот же поток событий, что и вывод:
		// его Observe возвращается сразу, решение уходит в свою горутину.
		OnEvent: func(ev core.Event) {
			out.event(ev)
			dispatcher.Observe(ev)
		},
		OnTransition: out.transition,
	})
	if err != nil {
		return err
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
