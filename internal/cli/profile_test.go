package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/config"
	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// Значения, которых в выводе быть не должно ни при каких обстоятельствах.
const (
	testPassword = "пароль-который-нельзя-печатать"
	testHashes   = "aaaaaaaaaaaaaaaa, bbbbbbbbbbbbbbbb cccccccccccccccc"
)

// profileEnv — окружение для одного запуска: свой каталог настроек и заранее
// заготовленные ответы на скрытый ввод.
type profileEnv struct {
	t       *testing.T
	dir     string
	answers []string
	asked   []string
	stdin   string
	// tty — притвориться терминалом: без него меню не открывается.
	tty bool
	// pipe — скрытого ввода нет: секреты читаются из stdin, как в конвейере.
	pipe bool
	// hashCheck — подмена проверки хешей (меню).
	hashCheck func(ctx context.Context, cfg core.Config, sink core.Sink) error
	// elevate — подмена перезапуска с правами (меню).
	elevate func(args []string) (bool, error)
}

func newProfileEnv(t *testing.T) *profileEnv {
	t.Helper()
	return &profileEnv{t: t, dir: t.TempDir()}
}

func (p *profileEnv) configPath() string {
	return filepath.Join(p.dir, config.FileName)
}

func (p *profileEnv) secretsPath() string {
	return filepath.Join(p.dir, config.SecretsFileName)
}

// answer заготавливает ответы на скрытый ввод в порядке вопросов.
func (p *profileEnv) answer(values ...string) *profileEnv {
	p.answers = append(p.answers, values...)
	return p
}

// input подставляет обычный stdin — для подтверждения удаления.
func (p *profileEnv) input(s string) *profileEnv {
	p.stdin = s
	return p
}

// run прогоняет команду, подставляя -config со своим каталогом.
func (p *profileEnv) run(args ...string) (code int, stdout, stderr string) {
	p.t.Helper()

	var out, errOut bytes.Buffer
	e := &env{
		stdout: &out,
		stderr: &errOut,
		stdin:  strings.NewReader(p.stdin),
		build:  Build{Version: "1.2.3", Commit: "abcdef0"},
		secrets: func(prompt string) (string, error) {
			p.asked = append(p.asked, prompt)
			if len(p.answers) == 0 {
				p.t.Fatalf("лишний вопрос: %q", prompt)
			}
			value := p.answers[0]
			p.answers = p.answers[1:]
			return value, nil
		},
	}

	if p.tty {
		e.tty = func() bool { return true }
	}
	if p.pipe {
		e.secrets = nil
	}
	e.hashCheck = p.hashCheck
	e.elevate = p.elevate

	args = append(args, "-config", p.configPath())
	code = run(context.Background(), e, args)
	return code, out.String(), errOut.String()
}

// setHome заводит профиль, на котором стоят остальные проверки.
func (p *profileEnv) setHome(extra ...string) {
	p.t.Helper()

	args := append([]string{"profile", "set", "home", "-peer", "203.0.113.10:56000"}, extra...)
	p.answer(testPassword, testPassword, testHashes)
	code, _, stderr := p.run(args...)
	if code != exitOK {
		p.t.Fatalf("profile set: код возврата %d, stderr:\n%s", code, stderr)
	}
}

func TestProfileSetCreatesProfileAndSecrets(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome("-workers", "18", "-dns", "doh-yandex")

	// Файл настроек читается обратно тем же разбором, что и в бою.
	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatalf("файл настроек не читается: %v", err)
	}
	profile, err := file.Profile("home")
	if err != nil {
		t.Fatalf("профиля нет: %v", err)
	}
	if profile.Peer != "203.0.113.10:56000" || profile.Workers != 18 || profile.DNS != "doh-yandex" {
		t.Errorf("настройки записаны неверно: %+v", profile)
	}

	// Секретов в файле настроек быть не должно — только ссылки на них.
	raw, err := os.ReadFile(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, "profiles.toml", string(raw))

	store := config.NewSecretStoreFor(p.configPath())
	password, err := store.Get(profile.PasswordRef)
	if err != nil {
		t.Fatalf("пароль не разворачивается: %v", err)
	}
	if password != testPassword {
		t.Errorf("пароль записан неверно: %q", password)
	}
	hashes, err := store.Get(profile.VkHashesRef)
	if err != nil {
		t.Fatalf("хеши не разворачиваются: %v", err)
	}
	if hashes != testHashes {
		t.Errorf("хеши записаны неверно: %q", hashes)
	}
}

// Пароль спрашивается дважды: опечатку в невидимом вводе иначе не поймать.
func TestProfileSetPasswordMismatch(t *testing.T) {
	p := newProfileEnv(t)
	p.answer(testPassword, "другой пароль")

	code, _, stderr := p.run("profile", "set", "home", "-peer", "203.0.113.10:56000")
	if code != exitError {
		t.Errorf("код возврата = %d, ожидался %d", code, exitError)
	}
	if !strings.Contains(stderr, "не совпали") {
		t.Errorf("непонятное сообщение:\n%s", stderr)
	}
	if _, err := os.Stat(p.configPath()); !os.IsNotExist(err) {
		t.Errorf("файл настроек создан, хотя пароль не подтверждён")
	}
}

// Настройки проверяются до того, как спрашивать секреты: неприятно ввести
// пароль дважды и услышать про опечатку в -obfs.
func TestProfileSetChecksSettingsBeforeAsking(t *testing.T) {
	p := newProfileEnv(t)

	code, _, stderr := p.run("profile", "set", "home", "-peer", "203.0.113.10:56000", "-obfs", "аудио")
	if code != exitError {
		t.Errorf("код возврата = %d, ожидался %d", code, exitError)
	}
	if !strings.Contains(stderr, "obfs") {
		t.Errorf("не сказано, что не так:\n%s", stderr)
	}
	if len(p.asked) != 0 {
		t.Errorf("секреты спрошены зря: %v", p.asked)
	}
}

func TestProfileSetNewProfileNeedsPeer(t *testing.T) {
	p := newProfileEnv(t)

	code, _, stderr := p.run("profile", "set", "home")
	if code != exitUsage {
		t.Errorf("код возврата = %d, ожидался %d", code, exitUsage)
	}
	if !strings.Contains(stderr, "-peer") {
		t.Errorf("не сказано, чего не хватает:\n%s", stderr)
	}
	if len(p.asked) != 0 {
		t.Errorf("секреты спрошены зря: %v", p.asked)
	}
}

// Повторный set меняет одно поле и не спрашивает секреты заново.
func TestProfileSetUpdatesFieldKeepingSecrets(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome()

	file, _ := config.Load(p.configPath())
	wasRef := file.Profiles["home"].PasswordRef

	p.asked = nil
	code, stdout, stderr := p.run("profile", "set", "home", "-datapath", "external")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if len(p.asked) != 0 {
		t.Errorf("секреты спрошены заново: %v", p.asked)
	}
	if !strings.Contains(stdout, "изменён") {
		t.Errorf("не сказано, что профиль изменён:\n%s", stdout)
	}

	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Profiles["home"].Datapath; got != config.DatapathExternal {
		t.Errorf("datapath = %q, ожидался %q", got, config.DatapathExternal)
	}
	if got := file.Profiles["home"].PasswordRef; got != wasRef {
		t.Errorf("ссылка на пароль изменилась: было %q, стало %q", wasRef, got)
	}
	if got := file.Profiles["home"].Peer; got != "203.0.113.10:56000" {
		t.Errorf("peer потерялся: %q", got)
	}
}

// -datapath wintun записывается в профиль уже как tun.
func TestProfileSetNormalizesWintun(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome()

	if code, _, stderr := p.run("profile", "set", "home", "-datapath", "wintun"); code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Profiles["home"].Datapath; got != config.DatapathTun {
		t.Errorf("datapath = %q, ожидался %q", got, config.DatapathTun)
	}
}

// -kill-switch=false записывается в профиль, а show его показывает; без
// флага поле не появляется, и show называет умолчание.
func TestProfileSetKillSwitch(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome()

	file, _ := config.Load(p.configPath())
	if file.Profiles["home"].KillSwitch != nil {
		t.Fatalf("kill_switch записан без флага: %v", *file.Profiles["home"].KillSwitch)
	}
	_, stdout, _ := p.run("profile", "show", "home")
	if !strings.Contains(stdout, "kill_switch") || !strings.Contains(stdout, "true") {
		t.Errorf("show не называет kill_switch по умолчанию:\n%s", stdout)
	}

	if code, _, stderr := p.run("profile", "set", "home", "-kill-switch=false"); code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if file.Profiles["home"].KillSwitchOn() {
		t.Error("-kill-switch=false не выключил kill-switch в профиле")
	}
	_, stdout, _ = p.run("profile", "show", "home")
	if !strings.Contains(stdout, "false") {
		t.Errorf("show не показывает kill_switch = false:\n%s", stdout)
	}
}

// -password перезаписывает секрет по той же ссылке: осиротевших записей
// в хранилище не остаётся.
func TestProfileSetRewritesPasswordInPlace(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome()

	file, _ := config.Load(p.configPath())
	wasRef := file.Profiles["home"].PasswordRef

	p.answer("новый пароль", "новый пароль")
	if code, _, stderr := p.run("profile", "set", "home", "-password"); code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}

	file, _ = config.Load(p.configPath())
	if got := file.Profiles["home"].PasswordRef; got != wasRef {
		t.Errorf("ссылка на пароль изменилась: было %q, стало %q", wasRef, got)
	}

	store := config.NewSecretStoreFor(p.configPath())
	value, err := store.Get(wasRef)
	if err != nil {
		t.Fatal(err)
	}
	if value != "новый пароль" {
		t.Errorf("пароль не перезаписан: %q", value)
	}
	if n := countSecrets(t, p.secretsPath()); n != 2 {
		t.Errorf("записей в хранилище %d, ожидалось 2 (пароль и хеши)", n)
	}
}

func TestProfileShowHidesSecrets(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome("-workers", "18")

	code, stdout, stderr := p.run("profile", "show", "home")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}

	assertNoSecrets(t, "вывод show", stdout+stderr)

	for _, want := range []string{"home", "203.0.113.10:56000", "18", mask, "хешей: 3", "(умолчание)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в выводе нет %q:\n%s", want, stdout)
		}
	}
}

// Секрет с чужой машины не разворачивается — show обязан сказать об этом,
// а не молчать и не падать.
func TestProfileShowReportsUnreadableSecret(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome()

	if err := os.Remove(p.secretsPath()); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := p.run("profile", "show", "home")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "не читается") {
		t.Errorf("не сказано, что секрет не читается:\n%s", stdout)
	}
}

func TestProfileList(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome()
	p.answer("пароль работы", "пароль работы", "dddddddddddddddd")
	if code, _, stderr := p.run("profile", "set", "work",
		"-peer", "198.51.100.7:56000", "-datapath", "external", "-default"); code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}

	code, stdout, stderr := p.run("profile", "list")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"home", "work", "203.0.113.10:56000", "external", "* — профиль по умолчанию"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в списке нет %q:\n%s", want, stdout)
		}
	}
	// Звёздочка стоит у work: его назначили умолчанием.
	for _, line := range strings.Split(stdout, "\n") {
		if strings.Contains(line, "home ") && strings.HasPrefix(line, "*") {
			t.Errorf("умолчанием помечен не тот профиль:\n%s", stdout)
		}
	}
	assertNoSecrets(t, "вывод list", stdout)
}

func TestProfileRmDeletesSecrets(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome()

	file, _ := config.Load(p.configPath())
	refs := []string{file.Profiles["home"].PasswordRef, file.Profiles["home"].VkHashesRef}

	code, stdout, stderr := p.run("profile", "rm", "home", "-y")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "удалён") {
		t.Errorf("не сказано, что профиль удалён:\n%s", stdout)
	}

	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Profiles) != 0 {
		t.Errorf("профиль остался в файле: %v", file.Names())
	}

	store := config.NewSecretStoreFor(p.configPath())
	for _, ref := range refs {
		if _, err := store.Get(ref); err == nil {
			t.Errorf("секрет %s не удалён", ref)
		}
	}
	if n := countSecrets(t, p.secretsPath()); n != 0 {
		t.Errorf("в хранилище осталось записей: %d", n)
	}
}

// Без -y удаление спрашивает подтверждение, и «нет» ничего не меняет.
func TestProfileRmAsksConfirmation(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome()

	p.input("n\n")
	code, stdout, stderr := p.run("profile", "rm", "home")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "Отменено") {
		t.Errorf("не сказано, что отменено:\n%s", stdout)
	}
	if !strings.Contains(stderr, "[y/N]") {
		t.Errorf("подтверждения не спросили:\n%s", stderr)
	}

	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := file.Profiles["home"]; !ok {
		t.Error("профиль удалён, хотя согласия не было")
	}
}

func TestProfileRmClearsDefault(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome("-default")
	p.answer("пароль работы", "пароль работы", "dddddddddddddddd")
	if code, _, stderr := p.run("profile", "set", "work", "-peer", "198.51.100.7:56000"); code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}

	code, stdout, stderr := p.run("profile", "rm", "home", "-y")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "умолчания нет") {
		t.Errorf("не сказано, что умолчание сброшено:\n%s", stdout)
	}

	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatalf("файл после удаления не читается: %v", err)
	}
	if file.DefaultProfile != "" {
		t.Errorf("default_profile остался: %q", file.DefaultProfile)
	}
}

// Команды, которым нужен файл настроек, обязаны сказать, где его искали.
func TestProfileWithoutConfigFile(t *testing.T) {
	for _, args := range [][]string{{"profile", "list"}, {"profile", "show", "home"}, {"profile", "rm", "home"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			p := newProfileEnv(t)
			code, _, stderr := p.run(args...)

			if code != exitError {
				t.Errorf("код возврата = %d, ожидался %d", code, exitError)
			}
			if !strings.Contains(stderr, p.configPath()) {
				t.Errorf("не назван путь, где искали:\n%s", stderr)
			}
		})
	}
}

func TestProfileUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
		cmd  string
	}{
		{"rm без имени", []string{"profile", "rm"}, "не названо имя профиля", "tt profile rm -h"},
		{"show без имени", []string{"profile", "show"}, "не названо имя профиля", "tt profile show -h"},
		{"два имени", []string{"profile", "show", "home", "work"}, "лишний аргумент", "tt profile show -h"},
		{"list с именем", []string{"profile", "list", "home"}, "лишний аргумент", "tt profile list -h"},
		{"флаг вместо подкоманды", []string{"profile", "-config", "x"}, "после подкоманды", "tt profile -h"},
		{"чужая подкоманда", []string{"profile", "delete", "home"}, "неизвестная подкоманда", "tt profile -h"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			e := &env{stdout: &out, stderr: &errOut, stdin: strings.NewReader("")}
			code := run(context.Background(), e, tc.args)

			if code != exitUsage {
				t.Errorf("код возврата = %d, ожидался %d", code, exitUsage)
			}
			if out.Len() != 0 {
				t.Errorf("ошибка ушла в stdout: %q", out.String())
			}
			if !strings.Contains(errOut.String(), tc.want) {
				t.Errorf("в сообщении нет %q:\n%s", tc.want, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.cmd) {
				t.Errorf("подсказка называет не ту команду (ждали %q):\n%s", tc.cmd, errOut.String())
			}
		})
	}
}

// У каждой подкоманды своя помощь со своими флагами.
func TestProfileSubcommandHelp(t *testing.T) {
	want := map[string][]string{
		"list": {"-config"},
		"show": {"-config"},
		"set":  {"-peer", "-password", "-hashes", "-default", "-config"},
		"rm":   {"-y", "-config"},
	}

	for sub, flags := range want {
		t.Run(sub, func(t *testing.T) {
			var out, errOut bytes.Buffer
			e := &env{stdout: &out, stderr: &errOut, stdin: strings.NewReader("")}
			code := run(context.Background(), e, []string{"profile", sub, "-h"})

			if code != exitOK {
				t.Errorf("код возврата = %d, ожидался %d", code, exitOK)
			}
			if errOut.Len() != 0 {
				t.Errorf("помощь ушла в stderr: %q", errOut.String())
			}
			if !strings.Contains(out.String(), "Использование: tt profile "+sub) {
				t.Errorf("нет строки синтаксиса:\n%s", out.String())
			}
			for _, name := range flags {
				if !strings.Contains(out.String(), name) {
					t.Errorf("нет флага %s:\n%s", name, out.String())
				}
			}
		})
	}
}

// assertNoSecrets — главная проверка задачи: ни пароль, ни хеши не должны
// попадать ни в файл настроек, ни в вывод команд.
func assertNoSecrets(t *testing.T, where, text string) {
	t.Helper()

	if strings.Contains(text, testPassword) {
		t.Errorf("%s: пароль в открытом виде:\n%s", where, text)
	}
	for _, hash := range strings.FieldsFunc(testHashes, func(r rune) bool { return r == ',' || r == ' ' }) {
		if strings.Contains(text, hash) {
			t.Errorf("%s: хеш %s в открытом виде:\n%s", where, hash, text)
		}
	}
}

// countSecrets считает записи в хранилище, не заглядывая в их значения.
func countSecrets(t *testing.T, path string) int {
	t.Helper()

	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	// В файле по записи на секрет: {"secrets":{"<id>":{"name":…,"data":…}}}.
	return strings.Count(string(raw), `"name"`)
}
