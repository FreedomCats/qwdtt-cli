package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// runCLI прогоняет команду с буферами вместо потоков процесса.
func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	e := &env{
		stdout: &out,
		stderr: &errOut,
		stdin:  strings.NewReader(""),
		build:  Build{Version: "1.2.3", Commit: "abcdef0"},
	}
	code = run(context.Background(), e, args)
	return code, out.String(), errOut.String()
}

// Запуск без аргументов — вопрос «что ты умеешь», а не ошибка: список команд
// уходит в stdout, код возврата нулевой.
func TestNoArgsListsCommands(t *testing.T) {
	code, stdout, stderr := runCLI()

	if code != exitOK {
		t.Errorf("код возврата = %d, ожидался %d", code, exitOK)
	}
	if stderr != "" {
		t.Errorf("stderr не пуст: %q", stderr)
	}
	for _, cmd := range commands() {
		if !strings.Contains(stdout, cmd.name) {
			t.Errorf("в списке команд нет %q:\n%s", cmd.name, stdout)
		}
		if !strings.Contains(stdout, cmd.summary) {
			t.Errorf("в списке нет описания команды %q:\n%s", cmd.name, stdout)
		}
	}
}

// Каждая команда обязана печатать свои флаги по -h — иначе о них неоткуда
// узнать. Помощь идёт в stdout: её просили.
func TestHelpFlagPrintsFlags(t *testing.T) {
	withFlags := map[string][]string{
		"connect": {"-peer", "-workers", "-datapath", "-config", "-v"},
		"ping":    {"-peer", "-dns", "-config"},
		"hashes":  {"-dns", "-config"},
	}

	for _, cmd := range commands() {
		t.Run(cmd.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(cmd.name, "-h")

			if code != exitOK {
				t.Errorf("код возврата = %d, ожидался %d", code, exitOK)
			}
			if stderr != "" {
				t.Errorf("помощь ушла в stderr: %q", stderr)
			}
			if !strings.Contains(stdout, "Использование: tt "+cmd.name) {
				t.Errorf("нет строки синтаксиса:\n%s", stdout)
			}
			if cmd.name == "profile" && !strings.Contains(stdout, "Подкоманды:") {
				t.Errorf("в помощи profile нет списка подкоманд:\n%s", stdout)
			}
			for _, name := range withFlags[cmd.name] {
				if !strings.Contains(stdout, name) {
					t.Errorf("нет флага %s:\n%s", name, stdout)
				}
			}
		})
	}
}

// tt help <команда> печатает то же, что и tt <команда> -h.
func TestHelpCommandMatchesHelpFlag(t *testing.T) {
	for _, cmd := range commands() {
		if cmd.name == "help" {
			continue
		}
		codeA, viaHelp, _ := runCLI("help", cmd.name)
		codeB, viaFlag, _ := runCLI(cmd.name, "-h")

		if codeA != exitOK || codeB != exitOK {
			t.Errorf("%s: коды возврата %d и %d, ожидался %d", cmd.name, codeA, codeB, exitOK)
		}
		if viaHelp != viaFlag {
			t.Errorf("%s: tt help и -h печатают разное:\n--- help ---\n%s\n--- -h ---\n%s",
				cmd.name, viaHelp, viaFlag)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	code, stdout, stderr := runCLI("конект")

	if code != exitUsage {
		t.Errorf("код возврата = %d, ожидался %d", code, exitUsage)
	}
	if stdout != "" {
		t.Errorf("ошибка ушла в stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "конект") {
		t.Errorf("в сообщении нет набранного имени: %q", stderr)
	}
}

// Неизвестный флаг разбирает пакет flag: он же печатает причину и помощь.
// Наше дело — не напечатать то же самое второй раз и вернуть код 2.
func TestUnknownFlag(t *testing.T) {
	code, stdout, stderr := runCLI("connect", "-нетутакого")

	if code != exitUsage {
		t.Errorf("код возврата = %d, ожидался %d", code, exitUsage)
	}
	if stdout != "" {
		t.Errorf("ошибка ушла в stdout: %q", stdout)
	}
	if n := strings.Count(stderr, "Использование: tt connect"); n != 1 {
		t.Errorf("помощь напечатана %d раз(а), ожидался один:\n%s", n, stderr)
	}
}

func TestUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"два профиля", []string{"connect", "home", "work"}, "лишний аргумент"},
		{"version с аргументом", []string{"version", "лишнее"}, "лишний аргумент"},
		{"profile без подкоманды", []string{"profile"}, "не названа подкоманда"},
		{"profile с чужой подкомандой", []string{"profile", "delete"}, "неизвестная подкоманда"},
		{"profile show без имени", []string{"profile", "show"}, "не названо имя профиля"},
		{"profile list с именем", []string{"profile", "list", "home"}, "лишний аргумент"},
		{"help с двумя командами", []string{"help", "connect", "ping"}, "лишний аргумент"},
		{"help чужой команды", []string{"help", "конект"}, "неизвестная команда"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runCLI(tc.args...)

			if code != exitUsage {
				t.Errorf("код возврата = %d, ожидался %d", code, exitUsage)
			}
			if stdout != "" {
				t.Errorf("ошибка ушла в stdout: %q", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("в сообщении нет %q:\n%s", tc.want, stderr)
			}
			if !strings.Contains(stderr, "Помощь: tt ") {
				t.Errorf("нет подсказки, где смотреть помощь:\n%s", stderr)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	code, stdout, stderr := runCLI("version")

	if code != exitOK {
		t.Errorf("код возврата = %d, ожидался %d", code, exitOK)
	}
	if stderr != "" {
		t.Errorf("stderr не пуст: %q", stderr)
	}
	for _, want := range []string{"1.2.3", "abcdef0"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в выводе нет %q:\n%s", want, stdout)
		}
	}
}

// Флаги-синонимы помощи на месте команды показывают общий список, а не ищутся
// среди команд.
func TestTopLevelHelpFlags(t *testing.T) {
	for _, arg := range []string{"-h", "-help", "--help"} {
		code, stdout, stderr := runCLI(arg)

		if code != exitOK {
			t.Errorf("%s: код возврата = %d, ожидался %d", arg, code, exitOK)
		}
		if stderr != "" {
			t.Errorf("%s: stderr не пуст: %q", arg, stderr)
		}
		if !strings.Contains(stdout, "Команды:") {
			t.Errorf("%s: нет списка команд:\n%s", arg, stdout)
		}
	}
}

// setFlags различает названный флаг и флаг, оставшийся при своём умолчании.
// На этом стоит правило «флаг перекрывает профиль» (G2).
func TestSetFlagsNamesOnlyGivenFlags(t *testing.T) {
	fs := newFlagSet(connectCommand())
	addConnectFlags(fs)
	if err := fs.Parse([]string{"-workers", "0", "-dtls=false", "home"}); err != nil {
		t.Fatalf("разбор: %v", err)
	}

	named := setFlags(fs)
	for _, name := range []string{"workers", "dtls"} {
		if !named[name] {
			t.Errorf("флаг -%s назван, а в списке его нет", name)
		}
	}
	for _, name := range []string{"peer", "obfs", "datapath", "v"} {
		if named[name] {
			t.Errorf("флаг -%s не назывался, а в списке он есть", name)
		}
	}
	if fs.NArg() != 1 || fs.Arg(0) != "home" {
		t.Errorf("имя профиля потерялось: %v", fs.Args())
	}
}
