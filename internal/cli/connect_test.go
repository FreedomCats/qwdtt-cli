package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/captcha"
	"github.com/FreedomCats/qwdtt-cli/internal/config"
	"github.com/FreedomCats/qwdtt-cli/internal/core"
	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

// Живого сервера в тестах нет, поэтому проверяется сборка плана: профиль,
// флаги поверх него, секреты из хранилища, выбор datapath. Сквозной запуск —
// критерий готовности G2 и задача H2.

// connectEnv — каталог настроек с одним профилем и записанными секретами.
type connectEnv struct {
	t    *testing.T
	dir  string
	file *config.File
}

func newConnectEnv(t *testing.T) *connectEnv {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, config.FileName)

	store := config.NewSecretStoreFor(path)
	passwordRef, err := store.Put("home.password", testPassword)
	if err != nil {
		t.Fatalf("Put(password): %v", err)
	}
	hashesRef, err := store.Put("home.vk_hashes", testHashes)
	if err != nil {
		t.Fatalf("Put(hashes): %v", err)
	}

	file := &config.File{Profiles: map[string]*config.Profile{
		"home": {
			Name:        "home",
			Peer:        "203.0.113.10:56000",
			PasswordRef: passwordRef,
			VkHashesRef: hashesRef,
			Workers:     18,
			DNS:         "google",
			// socks явно: тесты гоняют подключение без прав и без адаптера.
			// Умолчание (tun) — TestPlanConnectDefaultDatapath.
			Datapath: config.DatapathSocks,
			Socks:    config.SocksSection{Listen: "127.0.0.1:1085"},
		},
	}}
	if err := config.Save(path, file); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return &connectEnv{t: t, dir: dir, file: file}
}

func (c *connectEnv) configPath() string {
	return filepath.Join(c.dir, config.FileName)
}

// save перезаписывает файл настроек после правки c.file.
func (c *connectEnv) save() {
	c.t.Helper()
	if err := config.Save(c.configPath(), c.file); err != nil {
		c.t.Fatalf("Save: %v", err)
	}
}

// plan разбирает флаги так же, как runConnect, и собирает план.
func (c *connectEnv) plan(args ...string) (*connectPlan, error) {
	c.t.Helper()

	fs := newFlagSet(connectCommand())
	opts := addConnectFlags(fs)
	args = append(args, "-config", c.configPath())
	rest, err := parseWithArgs(&env{stdout: &strings.Builder{}, stderr: &strings.Builder{}}, fs, args)
	if err != nil {
		c.t.Fatalf("разбор флагов: %v", err)
	}
	opts.named = setFlags(fs)

	name := ""
	if len(rest) > 0 {
		name = rest[0]
	}
	return planConnect(opts, name)
}

// Без флагов план — это профиль с развёрнутыми секретами.
func TestPlanConnectTakesProfileAsIs(t *testing.T) {
	c := newConnectEnv(t)

	plan, err := c.plan("home")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}

	cfg := plan.cfg
	if cfg.Peer != "203.0.113.10:56000" {
		t.Errorf("Peer = %q", cfg.Peer)
	}
	if cfg.Password != testPassword {
		t.Errorf("пароль не развернулся из хранилища")
	}
	if len(cfg.Hashes) != 3 {
		t.Errorf("хешей %d, ожидалось 3: %v", len(cfg.Hashes), cfg.Hashes)
	}
	if cfg.Workers != 18 || cfg.DNS != "google" || cfg.NoDTLS || cfg.TurnTCP {
		t.Errorf("настройки ядра разошлись с профилем: %+v", cfg)
	}
	if plan.stage != core.StageSocks {
		t.Errorf("шаг datapath = %q, ожидался socks", plan.stage)
	}
	if !strings.Contains(plan.describe, "127.0.0.1:1085") {
		t.Errorf("в описании datapath нет адреса из профиля: %q", plan.describe)
	}
	if plan.datapath == nil || plan.datapath(nil) == nil {
		t.Errorf("конструктор datapath не собран")
	}
}

// Имя профиля можно не называть, когда он в файле один.
func TestPlanConnectPicksSingleProfile(t *testing.T) {
	c := newConnectEnv(t)

	plan, err := c.plan()
	if err != nil {
		t.Fatalf("planConnect без имени: %v", err)
	}
	if plan.profile.Name != "home" {
		t.Errorf("профиль = %q, ожидался home", plan.profile.Name)
	}
}

// Профиль без поля datapath подключается режимом по умолчанию: на Windows и
// Linux — весь трафик (tun), на macOS — socks (config.DefaultDatapath).
func TestPlanConnectDefaultDatapath(t *testing.T) {
	c := newConnectEnv(t)
	c.file.Profiles["home"].Datapath = ""
	c.save()

	plan, err := c.plan("home")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}
	want := core.StageVPN
	if config.DefaultDatapath == config.DatapathSocks {
		want = core.StageSocks
	}
	if plan.stage != want {
		t.Errorf("шаг datapath = %q, ожидался %q (умолчание %s)", plan.stage, want, config.DefaultDatapath)
	}
}

// Названный флаг перекрывает поле профиля; неназванный оставляет профиль как есть.
func TestPlanConnectFlagsOverrideProfile(t *testing.T) {
	c := newConnectEnv(t)

	plan, err := c.plan("home",
		"-workers", "27", "-dtls=false", "-dns", "cloudflare",
		"-turn-transport", "tcp", "-socks-listen", "127.0.0.1:1090")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}

	cfg := plan.cfg
	if cfg.Workers != 27 {
		t.Errorf("Workers = %d, ожидалось 27 из флага", cfg.Workers)
	}
	if !cfg.NoDTLS {
		t.Errorf("-dtls=false не дошёл до ядра")
	}
	if cfg.DNS != "cloudflare" {
		t.Errorf("DNS = %q, ожидался cloudflare из флага", cfg.DNS)
	}
	if !cfg.TurnTCP {
		t.Errorf("-turn-transport tcp не дошёл до ядра")
	}
	if !strings.Contains(plan.describe, "127.0.0.1:1090") {
		t.Errorf("адрес SOCKS5 из флага не попал в описание: %q", plan.describe)
	}

	// Не названное — из профиля.
	if cfg.Peer != "203.0.113.10:56000" {
		t.Errorf("Peer перекрыт, хотя флаг не назывался: %q", cfg.Peer)
	}
	// Профиль в файле не тронут: флаг действует один запуск.
	if c.file.Profiles["home"].Workers != 18 {
		t.Errorf("флаг изменил профиль в памяти")
	}
}

// «-workers 0» — это названный флаг, и он должен дойти до ядра (там его
// поднимут до одной группы), а не потеряться как «не задан».
func TestPlanConnectZeroWorkersIsNamed(t *testing.T) {
	c := newConnectEnv(t)

	plan, err := c.plan("home", "-workers", "0")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}
	if plan.cfg.Workers != 9 {
		t.Errorf("Workers = %d, ожидалось 9: ноль из флага нормализуется в одну группу", plan.cfg.Workers)
	}
}

// Значение флага проверяется теми же правилами, что и поле профиля.
func TestPlanConnectRejectsBadFlagValue(t *testing.T) {
	c := newConnectEnv(t)

	_, err := c.plan("home", "-obfs", "loud")
	if err == nil {
		t.Fatal("план собрался с obfs = loud")
	}
	if !strings.Contains(err.Error(), "obfs") {
		t.Errorf("ошибка не называет поле: %v", err)
	}
}

// Datapath external: шаг vpn, права администратора и netguard в требованиях,
// предупреждения нет. Сам wireguard.exe здесь не ищется по-настоящему — план
// только строит конструктор.
func TestPlanConnectExternal(t *testing.T) {
	c := newConnectEnv(t)

	plan, err := c.plan("home", "-datapath", "external")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}
	if plan.stage != core.StageVPN {
		t.Errorf("шаг datapath = %q, ожидался vpn", plan.stage)
	}
	if !strings.Contains(plan.describe, "WireGuard") {
		t.Errorf("описание datapath не про WireGuard: %q", plan.describe)
	}
	req := plan.datapath(nil).Requires()
	if !req.Admin || req.Warning != "" || len(req.Programs) != 1 || !req.Netguard {
		t.Errorf("требования external не те: %+v", req)
	}
}

func TestPlanConnectTun(t *testing.T) {
	c := newConnectEnv(t)

	plan, err := c.plan("home", "-datapath", "tun")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}
	if plan.stage != core.StageVPN {
		t.Errorf("шаг datapath = %q, ожидался vpn", plan.stage)
	}
	if !strings.Contains(plan.describe, "адаптер qwdtt-cli") || !strings.Contains(plan.describe, "весь трафик") {
		t.Errorf("описание datapath не про свой адаптер: %q", plan.describe)
	}
	if _, ok := plan.datapath(nil).(*datapath.Tun); !ok {
		t.Errorf("datapath не tun: %T", plan.datapath(nil))
	}
	req := plan.datapath(nil).Requires()
	if !req.Admin || !req.Netguard || !req.DNSGuard {
		t.Errorf("требования tun не те: %+v", req)
	}
	if runtime.GOOS == "windows" && (len(req.Files) != 1 || req.Files[0] != "wintun.dll") {
		t.Errorf("на Windows tun нужна wintun.dll: %+v", req)
	}
}

// Прежнее имя wintun — синоним tun: старые профили и команды не ломаются.
func TestPlanConnectWintunAlias(t *testing.T) {
	c := newConnectEnv(t)

	plan, err := c.plan("home", "-datapath", "wintun")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}
	if _, ok := plan.datapath(nil).(*datapath.Tun); !ok {
		t.Errorf("wintun не стал tun: %T", plan.datapath(nil))
	}
}

// Права администратора проверяются до подключения: без них external
// объясняет, что делать, одной строкой.
func TestRequirementProblemsAdmin(t *testing.T) {
	req := datapath.Requirements{Admin: true}
	problems := requirementProblems(req)
	if isElevated() {
		if len(problems) != 0 {
			t.Errorf("процесс повышен, а проблемы есть: %v", problems)
		}
		return
	}
	if len(problems) != 1 || problems[0] != elevationHint {
		t.Errorf("без прав ожидалась одна проблема про права: %v", problems)
	}
}

// Неподдерживаемая система перекрывает остальные требования: на Linux
// советовать права администратора и wintun.dll бессмысленно (найдено первым
// прогоном на Linux).
func TestRequirementsErrorUnsupported(t *testing.T) {
	why := errors.New("datapath tun поддерживается только на Windows")
	req := datapath.Requirements{Unsupported: why, Admin: true, Files: []string{"wintun.dll"}}

	err := requirementsError("адаптер qwdtt-cli", req)
	if !errors.Is(err, why) {
		t.Fatalf("ошибка %v, ждали обёрнутую причину", err)
	}
	if msg := err.Error(); strings.Contains(msg, "администратора") || strings.Contains(msg, "wintun.dll") ||
		!strings.Contains(msg, "-datapath socks") {
		t.Errorf("сообщение не то: %q", msg)
	}
}

func TestRequirementsErrorNone(t *testing.T) {
	if err := requirementsError("SOCKS5", datapath.Requirements{}); err != nil {
		t.Errorf("socks ничего не требует, а ошибка есть: %v", err)
	}
}

// tun работает на Windows и Linux, external — только на Windows; на
// остальных системах план строится, но запуск отказывает их собственной
// причиной.
func TestPlanConnectUnsupportedElsewhere(t *testing.T) {
	for _, tc := range []struct {
		datapath string
		systems  []string
	}{
		{"tun", []string{"windows", "linux"}},
		{"external", []string{"windows"}},
	} {
		dp := tc.datapath
		c := newConnectEnv(t)
		plan, err := c.plan("home", "-datapath", dp)
		if err != nil {
			t.Fatalf("%s: planConnect: %v", dp, err)
		}
		err = requirementsError(plan.describe, plan.datapath(nil).Requires())
		if slices.Contains(tc.systems, runtime.GOOS) {
			if err != nil && strings.Contains(err.Error(), "поддерживается только") {
				t.Errorf("%s: на %s отказ «не поддерживается»: %v", dp, runtime.GOOS, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), dp+" ") || !strings.Contains(err.Error(), "поддерживается только") {
			t.Errorf("%s: ждали отказ «поддерживается только», получили %v", dp, err)
		}
	}
}

// Ссылка, которой в хранилище нет, — внятная ошибка с подсказкой, и без
// самого секрета в тексте.
func TestPlanConnectMissingSecret(t *testing.T) {
	c := newConnectEnv(t)
	c.file.Profiles["home"].PasswordRef = "dpapi:0000000000000000"
	c.save()

	_, err := c.plan("home")
	if err == nil {
		t.Fatal("план собрался без пароля")
	}
	msg := err.Error()
	for _, want := range []string{"пароль", "qwdtt-cli profile set home -password"} {
		if !strings.Contains(msg, want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}
	if strings.Contains(msg, testPassword) || strings.Contains(msg, "aaaaaaaa") {
		t.Errorf("в ошибке виден секрет: %v", err)
	}
}

// Окно разблокировки связки закрыли — секрет цел: подсказка про связку, а не
// про перезапись пароля.
func TestSecretErrorLockedKeyring(t *testing.T) {
	locked := fmt.Errorf("секрет %q: %w", "secretservice:0000", config.ErrKeyringLocked)
	msg := secretError("пароль подключения", "home", "-password", locked).Error()
	if !strings.Contains(msg, "разблокируйте связку ключей") {
		t.Errorf("нет подсказки про связку: %s", msg)
	}
	if strings.Contains(msg, "profile set") {
		t.Errorf("подсказка перезаписать цельный секрет: %s", msg)
	}
}

// Без файла настроек connect не притворяется, что подключается: ошибка
// с подсказкой, куда идти, и код возврата 1, а не 2 — командная строка верна.
func TestConnectWithoutProfilesFile(t *testing.T) {
	code, stdout, stderr := runCLI("connect", "-config", filepath.Join(t.TempDir(), "нет.toml"))

	if code != exitError {
		t.Errorf("код возврата = %d, ожидался %d", code, exitError)
	}
	if stdout != "" {
		t.Errorf("ошибка ушла в stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "qwdtt-cli profile set") {
		t.Errorf("в ошибке нет подсказки завести профиль:\n%s", stderr)
	}
}

// Отменённый заранее контекст: цикл подключения возвращается, не начав
// попытки, datapath не поднимается, а команда заканчивается успехом.
func TestConnectStopsOnCancelledContext(t *testing.T) {
	c := newConnectEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out, errOut strings.Builder
	e := &env{stdout: &out, stderr: &errOut, stdin: strings.NewReader("")}
	code := run(ctx, e, []string{"connect", "home", "-config", c.configPath()})

	if code != exitOK {
		t.Errorf("код возврата = %d, ожидался %d; stderr:\n%s", code, exitOK, errOut.String())
	}
	if !strings.Contains(out.String(), "остановлено") {
		t.Errorf("нет строки об остановке:\n%s", out.String())
	}
	if strings.Contains(out.String(), testPassword) {
		t.Errorf("в выводе виден пароль")
	}
}

// windowSolver — подставной решатель с окном: captchaSolver должен отдать
// именно его, а не консольный. proxied — собран с Dial (окно через прокси).
type windowSolver struct{ proxied bool }

func (windowSolver) Solve(context.Context, captcha.Request) (string, error) { return "", nil }
func (windowSolver) NeedsHuman() bool                                       { return true }

func TestCaptchaSolverChoice(t *testing.T) {
	window := func(opts captcha.WebView2Options) (captcha.Solver, error) {
		return windowSolver{proxied: opts.Dial != nil}, nil
	}
	missing := func(captcha.WebView2Options) (captcha.Solver, error) {
		return nil, errors.New("WebView2 Runtime не установлен")
	}
	unsupported := func(captcha.WebView2Options) (captcha.Solver, error) {
		return nil, fmt.Errorf("окно капчи есть только на Windows: %w", errors.ErrUnsupported)
	}
	dial := (&net.Dialer{}).DialContext

	cases := []struct {
		name       string
		window     func(captcha.WebView2Options) (captcha.Solver, error)
		ssh        bool
		killSwitch bool
		dial       captcha.DialFunc
		wantWindow bool
		wantProxy  bool
		wantWarn   bool
	}{
		{name: "окно есть", window: window, wantWindow: true},
		// Без kill-switch прокси не нужен, даже если есть чем соединяться.
		{name: "окно есть, без kill-switch, диалер", window: window, dial: dial, wantWindow: true},
		{name: "окно есть, kill-switch", window: window, killSwitch: true, dial: dial, wantWindow: true, wantProxy: true},
		// Нечем соединяться — окно само, со снятием kill-switch.
		{name: "окно есть, kill-switch, без диалера", window: window, killSwitch: true, wantWindow: true},
		{name: "окна не заводили", window: nil},
		{name: "окна не заводили, kill-switch", window: nil, killSwitch: true, dial: dial},
		{name: "нет Runtime", window: missing, wantWarn: true},
		{name: "не Windows", window: unsupported},
		{name: "по SSH", window: window, ssh: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ssh := ""
			if tc.ssh {
				ssh = "192.0.2.1 50000 192.0.2.2 22"
			}
			t.Setenv("SSH_CONNECTION", ssh)

			var out, errOut strings.Builder
			e := &env{stdout: &out, stderr: &errOut, stdin: strings.NewReader(""), captchaWindow: tc.window}
			var ks *killSwitchGuard
			if tc.killSwitch {
				ks = &killSwitchGuard{session: &fakeLockdownSession{}, logf: func(string, ...any) {}}
			}
			solver := captchaSolver(e, newPrinter(&out, false), ks, tc.dial)

			// Окно через прокси kill-switch не снимает; снимает его запасное
			// окно без прокси.
			proxied, isProxied := solver.(proxiedCaptcha)
			if isProxied != tc.wantProxy {
				t.Fatalf("решатель %T, окно через прокси ожидалось: %v", solver, tc.wantProxy)
			}
			if isProxied {
				if w, ok := proxied.Solver.(windowSolver); !ok || !w.proxied {
					t.Errorf("окно через прокси: %#v", proxied.Solver)
				}
				solver = proxied.direct
			}

			if wrapped, ok := solver.(killSwitchCaptcha); ok {
				if !tc.killSwitch || wrapped.guard != ks {
					t.Errorf("обёртка kill-switch: %+v, kill-switch: %v", wrapped, tc.killSwitch)
				}
				solver = wrapped.Solver
			} else if tc.killSwitch {
				t.Errorf("решатель %T не снимает kill-switch на время капчи", solver)
			}

			w, isWindow := solver.(windowSolver)
			if isWindow != tc.wantWindow {
				t.Errorf("решатель %T, окно ожидалось: %v", solver, tc.wantWindow)
			}
			if isWindow && w.proxied {
				t.Error("окно без обёртки прокси собрано с Dial")
			}
			if _, isConsole := solver.(*captcha.Manual); !tc.wantWindow && !isConsole {
				t.Errorf("решатель %T, ожидался консольный", solver)
			}
			warned := strings.Contains(errOut.String(), "Предупреждение")
			if warned != tc.wantWarn {
				t.Errorf("предупреждение: %v, ожидалось: %v; stderr: %q", warned, tc.wantWarn, errOut.String())
			}
		})
	}
}
