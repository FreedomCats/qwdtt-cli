package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/config"
)

// menuEnv — profileEnv, который притворяется терминалом.
func newMenuEnv(t *testing.T) *profileEnv {
	t.Helper()
	p := newProfileEnv(t)
	p.tty = true
	return p
}

// В терминале tt без аргументов открывает меню, а не список команд.
func TestNoArgsOpensMenuInTerminal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)

	var out, errOut bytes.Buffer
	e := &env{
		stdout: &out,
		stderr: &errOut,
		stdin:  strings.NewReader(""),
		tty:    func() bool { return true },
	}
	code := run(context.Background(), e, nil)

	if code != exitOK {
		t.Errorf("код возврата = %d, ожидался %d; stderr:\n%s", code, exitOK, errOut.String())
	}
	if !strings.Contains(out.String(), "Профилей пока нет") {
		t.Errorf("меню не открылось:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Использование") {
		t.Errorf("вместо меню — список команд:\n%s", out.String())
	}
	if !e.noPause {
		t.Error("уход из меню концом ввода не отменил паузу перед закрытием окна")
	}
}

// Без терминала команда menu отказывает: отвечать на её вопросы некому.
func TestMenuNeedsTerminal(t *testing.T) {
	p := newProfileEnv(t)

	code, _, stderr := p.run("menu")
	if code != exitError {
		t.Errorf("код возврата = %d, ожидался %d", code, exitError)
	}
	if !strings.Contains(stderr, "только в терминале") {
		t.Errorf("непонятное сообщение:\n%s", stderr)
	}
}

func TestMenuListsProfiles(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome("-datapath", "tun")
	p.input("0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"> home", "203.0.113.10:56000", "весь трафик", "(по умолчанию)",
		"1. Подключиться (home)", "6. Удалить профиль home", "0. Выход", "До свидания"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в меню нет %q:\n%s", want, stdout)
		}
	}
}

// Неверный ответ переспрашивается, а не закрывает меню.
func TestMenuRepeatsQuestionOnBadChoice(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.input("9\nабв\n0\n")

	code, _, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if got := strings.Count(stderr, "Нет такого пункта"); got != 2 {
		t.Errorf("переспросили %d раз, ожидалось 2:\n%s", got, stderr)
	}
	if got := strings.Count(stderr, "Выберите пункт"); got != 3 {
		t.Errorf("вопрос задан %d раз, ожидалось 3:\n%s", got, stderr)
	}
}

// Подключение, которое не удалось, возвращает в меню: человек видит ошибку
// и может исправить профиль, не перезапуская tt.
func TestMenuConnectErrorReturnsToMenu(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	// Без хранилища секретов план подключения не соберётся — до сети дело
	// не дойдёт.
	if err := os.Remove(p.secretsPath()); err != nil {
		t.Fatal(err)
	}
	p.input("\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "Не получилось") || !strings.Contains(stderr, "пароль") {
		t.Errorf("ошибка подключения не показана:\n%s", stderr)
	}
	if got := strings.Count(stdout, "1. Подключиться"); got != 2 {
		t.Errorf("меню показано %d раз, ожидалось 2 — до и после подключения:\n%s", got, stdout)
	}
}

// Выбор другого профиля меняет, к чему подключается первый пункт, но не
// трогает default_profile.
func TestMenuPicksAnotherProfile(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome("-default")
	p.answer(testPassword, testPassword, testHashes)
	if code, _, stderr := p.run("profile", "set", "second", "-peer", "198.51.100.7:56000"); code != exitOK {
		t.Fatalf("profile set: код %d, stderr:\n%s", code, stderr)
	}
	p.input("2\n2\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "1. Подключиться (second)") {
		t.Errorf("выбор профиля не подействовал:\n%s", stdout)
	}

	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if file.DefaultProfile != "home" {
		t.Errorf("default_profile = %q, ожидался home", file.DefaultProfile)
	}
}

func TestMenuRemovesProfile(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.input("6\nда\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "удалён") || !strings.Contains(stdout, "Профилей пока нет") {
		t.Errorf("профиль не удалён:\n%s", stdout)
	}
}

func TestMenuReplacesPeer(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.input("5\n2\n198.51.100.7:56000\n0\n")

	code, _, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Profiles["home"].Peer; got != "198.51.100.7:56000" {
		t.Errorf("peer = %q, ожидался новый адрес", got)
	}
}

func TestMenuNumber(t *testing.T) {
	for _, tc := range []struct {
		line string
		want int
		ok   bool
	}{
		{"", 1, true},
		{" 3 ", 3, true},
		{"0", 0, true},
		{"7", 0, false},
		{"-1", 0, false},
		{"один", 0, false},
	} {
		got, ok := menuNumber(tc.line, 6, 1)
		if got != tc.want || ok != tc.ok {
			t.Errorf("menuNumber(%q) = %d, %v; ожидалось %d, %v", tc.line, got, ok, tc.want, tc.ok)
		}
	}
}
