package cli

import (
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/config"
)

// testLink — ссылка экспорта со всеми полями. Имя кириллицей: оно станет
// именем профиля латиницей.
const testLink = "qwdtt://config?name=%D0%94%D0%BE%D0%BC+2&peer=203.0.113.10%3A56000" +
	"&hashes=aaaaaaaaaaaaaaaa%2C+bbbbbbbbbbbbbbbb+cccccccccccccccc&workers=18&port=9000" +
	"&pass=%D0%BF%D0%B0%D1%80%D0%BE%D0%BB%D1%8C-%D0%BA%D0%BE%D1%82%D0%BE%D1%80%D1%8B%D0%B9-%D0%BD%D0%B5%D0%BB%D1%8C%D0%B7%D1%8F-%D0%BF%D0%B5%D1%87%D0%B0%D1%82%D0%B0%D1%82%D1%8C"

// linkNoHashes — ссылка без хешей (у Android это «общие хеши»).
const linkNoHashes = "qwdtt:config?peer=example.org&pass=x"

func loadTestFile(t *testing.T, p *profileEnv) *config.File {
	t.Helper()
	file, err := config.Load(p.configPath())
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestProfileImport(t *testing.T) {
	p := newProfileEnv(t)
	p.answer(testLink)

	code, stdout, stderr := p.run("profile", "import")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	assertNoSecrets(t, "stdout", stdout)
	assertNoSecrets(t, "stderr", stderr)
	for _, want := range []string{`"dom-2" создан`, "203.0.113.10:56000", "хешей:   3", config.DefaultDatapath} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в сводке нет %q:\n%s", want, stdout)
		}
	}

	h := loadTestFile(t, p).Profiles["dom-2"]
	if h == nil || h.Peer != "203.0.113.10:56000" || h.Workers != 18 || h.Datapath != "" {
		t.Fatalf("профиль записан неверно: %+v", h)
	}
	store := config.NewSecretStoreFor(p.configPath())
	if got, _ := store.Get(h.PasswordRef); got != testPassword {
		t.Errorf("пароль записан неверно: %q", got)
	}
	if got, _ := store.Get(h.VkHashesRef); got != testHashes {
		t.Errorf("хеши записаны неверно: %q", got)
	}
}

// Из конвейера: qwdtt-cli profile import home < файл.
func TestProfileImportFromStdin(t *testing.T) {
	p := newProfileEnv(t)
	p.pipe = true
	p.input(testLink + "\n")

	code, _, stderr := p.run("profile", "import", "home", "-datapath", "tun")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if h := loadTestFile(t, p).Profiles["home"]; h == nil || h.Datapath != config.DatapathTun {
		t.Errorf("профиль записан неверно: %+v", h)
	}
}

// Ссылка wdtt:// от бота сервера: порт сервера — первый из трёх, имя — после «#».
func TestProfileImportWDTT(t *testing.T) {
	p := newProfileEnv(t)
	p.pipe = true
	p.input("wdtt://203.0.113.10:56000:56001:9000:" + testPassword + ":" + testHashes + "#Дом 2\n")

	code, stdout, stderr := p.run("profile", "import")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	assertNoSecrets(t, "stdout", stdout)
	assertNoSecrets(t, "stderr", stderr)

	h := loadTestFile(t, p).Profiles["dom-2"]
	if h == nil || h.Peer != "203.0.113.10:56000" || h.Workers != 0 {
		t.Fatalf("профиль записан неверно: %+v", h)
	}
	store := config.NewSecretStoreFor(p.configPath())
	if got, _ := store.Get(h.PasswordRef); got != testPassword {
		t.Errorf("пароль записан неверно: %q", got)
	}
	if got, _ := store.Get(h.VkHashesRef); got != testHashes {
		t.Errorf("хеши записаны неверно: %q", got)
	}
}

// Занятое имя — отказ; -replace заменяет адрес и секреты и оставляет прочие
// настройки профиля.
func TestProfileImportReplace(t *testing.T) {
	p := newProfileEnv(t)
	p.setHome("-datapath", "tun", "-dns", "doh-yandex")

	p.answer(linkNoHashes)
	code, _, stderr := p.run("profile", "import", "home")
	if code != exitError || !strings.Contains(stderr, "-replace") {
		t.Fatalf("занятое имя: код %d, stderr:\n%s", code, stderr)
	}

	p.answer(testLink)
	if code, _, stderr := p.run("profile", "import", "home", "-replace"); code != exitOK {
		t.Fatalf("-replace: код %d, stderr:\n%s", code, stderr)
	}
	h := loadTestFile(t, p).Profiles["home"]
	if h.Peer != "203.0.113.10:56000" || h.Datapath != config.DatapathTun || h.DNS != "doh-yandex" {
		t.Errorf("профиль после замены: %+v", h)
	}
}

// Хешей в ссылке нет: в терминале спрашиваются, без терминала — отказ.
func TestProfileImportMissingHashes(t *testing.T) {
	p := newProfileEnv(t)
	p.answer(linkNoHashes, testHashes)
	if code, _, stderr := p.run("profile", "import", "home"); code != exitOK {
		t.Fatalf("код %d, stderr:\n%s", code, stderr)
	}
	if len(p.asked) != 2 || !strings.Contains(p.asked[1], "нет хешей") {
		t.Errorf("вопросы: %q", p.asked)
	}

	q := newProfileEnv(t)
	q.pipe = true
	q.input(linkNoHashes + "\n")
	code, _, stderr := q.run("profile", "import", "home")
	if code != exitError || !strings.Contains(stderr, "нет хешей") {
		t.Errorf("без терминала: код %d, stderr:\n%s", code, stderr)
	}
}

func TestProfileImportBadLink(t *testing.T) {
	p := newProfileEnv(t)
	p.answer("qwdtt://config?name=x&pass=" + testPassword)

	code, _, stderr := p.run("profile", "import")
	if code != exitError || !strings.Contains(stderr, "нет адреса") {
		t.Errorf("код %d, stderr:\n%s", code, stderr)
	}
	assertNoSecrets(t, "stderr", stderr)
}

// Мастер со ссылкой спрашивает только имя (Enter — из ссылки) и режим.
func TestWizardShareLink(t *testing.T) {
	p := newMenuEnv(t)
	p.answer(testLink)
	p.input("\n\n\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if len(p.asked) != 1 {
		t.Errorf("лишние вопросы скрытым вводом: %q", p.asked)
	}
	if !strings.Contains(stdout, "Ссылка принята: сервер 203.0.113.10:56000, ссылок на звонки: 3") {
		t.Errorf("не сказано, что взято из ссылки:\n%s", stdout)
	}
	assertNoSecrets(t, "stdout", stdout)
	if h := loadTestFile(t, p).Profiles["dom-2"]; h == nil || h.Datapath != config.DatapathTun || h.Workers != 18 {
		t.Errorf("профиль записан неверно: %+v", h)
	}
}

// Чего нет в ссылке, мастер спрашивает; занятое имя можно заменить.
func TestWizardShareLinkReplacesAndAsks(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.answer(linkNoHashes, testHashes)
	p.input("3\nhome\nда\n2\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "ссылок на звонки в ней нет") || !strings.Contains(stdout, `"home" изменён`) {
		t.Errorf("вывод:\n%s", stdout)
	}
	h := loadTestFile(t, p).Profiles["home"]
	if h.Peer != "example.org:56000" || h.Datapath != config.DatapathSocks {
		t.Errorf("профиль после замены: %+v", h)
	}
	if got, _ := config.NewSecretStoreFor(p.configPath()).Get(h.PasswordRef); got != "x" {
		t.Errorf("пароль не заменён: %q", got)
	}
}
