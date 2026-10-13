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

// Мастер с нуля: Enter на имени, адрес без порта, режим по умолчанию — весь
// трафик. Первый профиль становится профилем по умолчанию.
func TestWizardCreatesFirstProfile(t *testing.T) {
	p := newMenuEnv(t)
	p.answer("", testPassword, testPassword, testHashes)
	p.input("\n\nexample.org\n\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"example.org:56000", "Принято ссылок: 3", "создан", "1. Подключиться (home)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в выводе нет %q:\n%s", want, stdout)
		}
	}
	assertNoSecrets(t, "stdout", stdout)
	assertNoSecrets(t, "stderr", stderr)

	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	h := file.Profiles["home"]
	if h == nil || h.Peer != "example.org:56000" || h.Datapath != config.DatapathTun {
		t.Fatalf("профиль записан неверно: %+v", h)
	}
	if file.DefaultProfile != "home" {
		t.Errorf("default_profile = %q, ожидался home", file.DefaultProfile)
	}
	store := config.NewSecretStoreFor(p.configPath())
	if got, _ := store.Get(h.PasswordRef); got != testPassword {
		t.Errorf("пароль записан неверно: %q", got)
	}
	if got, _ := store.Get(h.VkHashesRef); got != testHashes {
		t.Errorf("хеши записаны неверно: %q", got)
	}
}

// Второй профиль: имя занято — предлагается свободное, режим SOCKS5, прежний
// единственный профиль остаётся профилем по умолчанию, а меню переходит
// к новому.
func TestWizardCreatesSecondProfile(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.answer("", testPassword, testPassword, testHashes)
	// home занят: на «заменить?» Enter — нет, и мастер спрашивает имя снова.
	p.input("3\nhome\n\n\n198.51.100.7:56000\n2\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "Профиль home уже есть") {
		t.Errorf("занятое имя не отвергнуто:\n%s", stderr)
	}
	if !strings.Contains(stdout, "1. Подключиться (profile-2)") {
		t.Errorf("меню не перешло к новому профилю:\n%s", stdout)
	}

	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Profiles["profile-2"]; got == nil || got.Datapath != config.DatapathSocks {
		t.Errorf("второй профиль записан неверно: %+v", got)
	}
	if file.DefaultProfile != "home" {
		t.Errorf("default_profile = %q, ожидался home", file.DefaultProfile)
	}
}

// Ошибки в ответах переспрашиваются: неверное имя и адрес, несовпавший
// пароль, ввод без единой ссылки, неверный режим.
func TestWizardRepeatsBadAnswers(t *testing.T) {
	p := newMenuEnv(t)
	p.answer("qwdtt://config?name=x", "", testPassword, "другой", "", testPassword, testPassword, "https://example.org/", testHashes)
	p.input("\nмой дом\nhome\n\nexample.org:порт\n203.0.113.5:443\n5\n1\n0\n")

	code, _, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"Ссылка не подошла", "Такое имя не подойдёт", "Адрес сервера нужен", "Не похоже на адрес",
		"Пароли не совпали", "Пароль не может быть пустым", "Не нашлось ни одной ссылки", "Введите 1 или 2"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("нет переспроса %q:\n%s", want, stderr)
		}
	}
	if len(p.answers) != 0 {
		t.Errorf("не все ответы на скрытый ввод спрошены: осталось %d", len(p.answers))
	}

	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := file.Profiles["home"]; got == nil || got.Peer != "203.0.113.5:443" {
		t.Errorf("профиль записан неверно: %+v", got)
	}
}

// Конец ввода посреди мастера — отмена: файл настроек не появляется.
func TestWizardCancelled(t *testing.T) {
	p := newMenuEnv(t)
	// Ссылки нет, и ввод кончается на вопросе об имени.
	p.answer("")
	p.input("\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "Профиль не записан") {
		t.Errorf("не сказано, что профиль не записан:\n%s", stdout)
	}
	if _, err := os.Stat(p.configPath()); !os.IsNotExist(err) {
		t.Errorf("файл настроек создан при отмене")
	}
}
