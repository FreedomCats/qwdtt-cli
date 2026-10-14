package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// Живого VK в тестах нет, поэтому проверяется сборка планов ping и hashes:
// профиль, флаги поверх, какие секреты разворачиваются. Прогон против
// настоящего сервера — задача H2. Каталог настроек — свой у каждого теста
// (newConnectEnv): к профилям пользователя тесты не прикасаются.

// diag разбирает флаги так же, как runPing/runHashes, и собирает план
// названной команды.
func (c *connectEnv) diag(cmd string, args ...string) (*diagPlan, error) {
	c.t.Helper()

	args = append(args, "-config", c.configPath())
	e := &env{stdout: &strings.Builder{}, stderr: &strings.Builder{}}

	switch cmd {
	case "ping":
		fs := newFlagSet(pingCommand())
		opts := addPingFlags(fs)
		rest, err := parseWithArgs(e, fs, args)
		if err != nil {
			c.t.Fatalf("разбор флагов: %v", err)
		}
		return planPing(opts.connectOptions(setFlags(fs)), first(rest))
	case "hashes":
		fs := newFlagSet(hashesCommand())
		opts := addHashesFlags(fs)
		rest, err := parseWithArgs(e, fs, args)
		if err != nil {
			c.t.Fatalf("разбор флагов: %v", err)
		}
		return planHashes(opts.connectOptions(setFlags(fs)), first(rest))
	default:
		c.t.Fatalf("нет такой команды: %s", cmd)
		return nil, nil
	}
}

func first(rest []string) string {
	if len(rest) > 0 {
		return rest[0]
	}
	return ""
}

// ping собирает полные настройки: сервер, пароль, хеши — замер идёт тем же
// путём, что и трафик.
func TestPlanPingTakesProfileWithSecrets(t *testing.T) {
	c := newConnectEnv(t)

	plan, err := c.diag("ping", "home")
	if err != nil {
		t.Fatalf("planPing: %v", err)
	}
	cfg := plan.cfg
	if cfg.Peer != "203.0.113.10:56000" || cfg.Password != testPassword || len(cfg.Hashes) != 3 {
		t.Errorf("настройки разошлись с профилем: peer=%q, пароль развёрнут=%v, хешей %d",
			cfg.Peer, cfg.Password == testPassword, len(cfg.Hashes))
	}
	if cfg.DNS != "google" {
		t.Errorf("DNS = %q, ожидался google из профиля", cfg.DNS)
	}
}

// Флаги ping перекрывают профиль на один запуск, как у connect.
func TestPlanPingFlagsOverrideProfile(t *testing.T) {
	c := newConnectEnv(t)

	plan, err := c.diag("ping", "home", "-peer", "198.51.100.7:443", "-dns", "cloudflare")
	if err != nil {
		t.Fatalf("planPing: %v", err)
	}
	if plan.cfg.Peer != "198.51.100.7:443" {
		t.Errorf("Peer = %q, флаг не перекрыл профиль", plan.cfg.Peer)
	}
	if plan.cfg.DNS != "cloudflare" {
		t.Errorf("DNS = %q, флаг не перекрыл профиль", plan.cfg.DNS)
	}
	if plan.profile.Workers != 18 {
		t.Errorf("Workers = %d: неназванное поле профиля изменилось", plan.profile.Workers)
	}
}

// Без пароля замер невозможен: ошибка с подсказкой, как у connect.
func TestPlanPingNeedsPassword(t *testing.T) {
	c := newConnectEnv(t)
	c.file.Profiles["home"].PasswordRef = "dpapi:0000000000000000"
	c.save()

	_, err := c.diag("ping", "home")
	if err == nil {
		t.Fatal("план ping собрался без пароля")
	}
	if !strings.Contains(err.Error(), "qwdtt-cli profile set home -password") {
		t.Errorf("в ошибке нет подсказки: %v", err)
	}
}

// hashes обходится одними хешами: пароль не разворачивается, и его
// отсутствие не мешает. Сервер в настройки попадает как есть — RunHashCheck
// его не проверяет.
func TestPlanHashesNeedsOnlyHashes(t *testing.T) {
	c := newConnectEnv(t)
	c.file.Profiles["home"].PasswordRef = "dpapi:0000000000000000"
	c.save()

	plan, err := c.diag("hashes", "home", "-dns", "yandex")
	if err != nil {
		t.Fatalf("planHashes: %v", err)
	}
	if plan.cfg.Password != "" {
		t.Errorf("hashes развернул пароль, а он ему не нужен")
	}
	if len(plan.cfg.Hashes) != 3 {
		t.Errorf("хешей %d, ожидалось 3", len(plan.cfg.Hashes))
	}
	if plan.cfg.DNS != "yandex" {
		t.Errorf("DNS = %q, флаг не перекрыл профиль", plan.cfg.DNS)
	}
}

// Без хешей проверять нечего — ошибка называет, как их записать.
func TestPlanHashesMissingHashes(t *testing.T) {
	c := newConnectEnv(t)
	c.file.Profiles["home"].VkHashesRef = "dpapi:0000000000000000"
	c.save()

	_, err := c.diag("hashes", "home")
	if err == nil {
		t.Fatal("план hashes собрался без хешей")
	}
	if !strings.Contains(err.Error(), "qwdtt-cli profile set home -hashes") {
		t.Errorf("в ошибке нет подсказки: %v", err)
	}
}

// Без файла настроек обе команды заканчиваются ошибкой с подсказкой и кодом 1,
// а не 2: командная строка верна. Сеть при этом не трогается.
func TestDiagWithoutProfilesFile(t *testing.T) {
	for _, cmd := range []string{"ping", "hashes"} {
		t.Run(cmd, func(t *testing.T) {
			code, stdout, stderr := runCLI(cmd, "-config", filepath.Join(t.TempDir(), "нет.toml"))

			if code != exitError {
				t.Errorf("код возврата = %d, ожидался %d", code, exitError)
			}
			if stdout != "" {
				t.Errorf("ошибка ушла в stdout: %q", stdout)
			}
			if !strings.Contains(stderr, "qwdtt-cli profile set") {
				t.Errorf("в ошибке нет подсказки завести профиль:\n%s", stderr)
			}
		})
	}
}

// Таблица hashes: строка на хеш со статусом и пояснением, хеш обрезан,
// начало проверки без -v не печатается.
func TestPrinterHashCheckTable(t *testing.T) {
	p, buf, _ := testPrinter(false)
	const hash = "abcdefgh12345678secret"

	p.event(core.EventHashCheck{Index: 1, Total: 2, Hash: hash, Status: core.HashCheckRunning})
	p.event(core.EventHashCheck{Index: 1, Total: 2, Hash: hash, Status: core.HashCheckOK, Msg: "TURN urls=2"})
	p.event(core.EventHashCheck{Index: 2, Total: 2, Hash: "zzzzzzzz99999999", Status: core.HashCheckDead, Msg: "Звонок не найден или закрыт"})

	got := buf.String()
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) != 2 {
		t.Fatalf("строк %d, ожидалось 2:\n%s", len(lines), got)
	}
	if !strings.Contains(lines[0], "хеш 1/2 abcdefgh…") || !strings.Contains(lines[0], " ok ") ||
		!strings.Contains(lines[0], "TURN urls=2") {
		t.Errorf("первая строка таблицы не та: %q", lines[0])
	}
	if !strings.Contains(lines[1], "хеш 2/2 zzzzzzzz…") || !strings.Contains(lines[1], " dead ") {
		t.Errorf("вторая строка таблицы не та: %q", lines[1])
	}
	if strings.Contains(got, hash) || strings.Contains(got, "secret") {
		t.Errorf("хеш напечатан целиком:\n%s", got)
	}
	// Столбец статуса начинается на одной позиции в обеих строках.
	if strings.Index(lines[0], " ok ") != strings.Index(lines[1], " dead ") {
		t.Errorf("столбцы статуса не выровнены:\n%s", got)
	}
}

// С -v начало проверки печатается, и хеш в нём тоже обрезан.
func TestPrinterHashCheckVerboseShowsRunning(t *testing.T) {
	p, buf, _ := testPrinter(true)
	p.event(core.EventHashCheck{Index: 1, Total: 1, Hash: "abcdefgh12345678", Status: core.HashCheckRunning})

	got := buf.String()
	if !strings.Contains(got, "хеш 1/1 abcdefgh…: проверяется") {
		t.Errorf("нет строки о начале проверки:\n%s", got)
	}
	if strings.Contains(got, "12345678") {
		t.Errorf("хеш напечатан целиком:\n%s", got)
	}
}

// Итог ping — задержка в миллисекундах; неудачу печатает не принтер,
// а общий обработчик по ошибке RunPing.
func TestPrinterPing(t *testing.T) {
	p, buf, _ := testPrinter(false)
	p.event(core.EventPing{RTT: 42 * 1e6})
	p.event(core.EventPing{Err: errFake})

	got := strings.TrimSpace(buf.String())
	if !strings.HasSuffix(got, "задержка до сервера через TURN: 42 мс") {
		t.Errorf("итог замера не тот: %q", got)
	}
	if strings.Count(got, "\n") != 0 {
		t.Errorf("неудача замера напечатана принтером:\n%s", got)
	}
}

var errFake = errors.New("искусственная ошибка")
