package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/config"
	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

const (
	deadHash = "bbbbbbbbbbbbbbbb"
	newHash  = "dddddddddddddddd"
)

// fakeHashCheck — проверка хешей без сети: названные хеши — закрытые звонки,
// остальные работают.
func fakeHashCheck(dead ...string) func(context.Context, core.Config, core.Sink) error {
	return func(_ context.Context, cfg core.Config, sink core.Sink) error {
		for i, h := range cfg.Hashes {
			status := core.HashCheckOK
			for _, d := range dead {
				if h == d {
					status = core.HashCheckDead
				}
			}
			sink.Emit(core.EventHashCheck{Index: i + 1, Total: len(cfg.Hashes), Hash: h, Status: status})
		}
		return nil
	}
}

func storedHashes(t *testing.T, p *profileEnv) []string {
	t.Helper()
	h := loadTestFile(t, p).Profiles["home"]
	raw, err := config.NewSecretStoreFor(p.configPath()).Get(h.VkHashesRef)
	if err != nil {
		t.Fatal(err)
	}
	return core.ParseHashes(raw)
}

// Закрытый звонок убирается, новые ссылки добавляются к остальным.
func TestMenuUpdateHashesDropsDead(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.hashCheck = fakeHashCheck(deadHash)
	p.answer("https://vk.com/call/join/" + newHash)
	p.input("4\n\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	for _, want := range []string{"звонок закрыт", "Закрытых звонков: 1 из 3", "теперь: 3"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("в выводе нет %q:\n%s", want, stdout)
		}
	}
	assertNoSecrets(t, "stdout", stdout)

	got := strings.Join(storedHashes(t, p), " ")
	if got != "aaaaaaaaaaaaaaaa cccccccccccccccc "+newHash {
		t.Errorf("хеши после обновления: %s", got)
	}
}

// Без новых ссылок — только убираются закрытые.
func TestMenuUpdateHashesOnlyRemoves(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.hashCheck = fakeHashCheck(deadHash)
	p.answer("")
	p.input("4\n1\n0\n")

	if code, _, stderr := p.run("menu"); code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if got := strings.Join(storedHashes(t, p), " "); got != "aaaaaaaaaaaaaaaa cccccccccccccccc" {
		t.Errorf("хеши после обновления: %s", got)
	}
}

func TestMenuUpdateHashesReplaceAll(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.hashCheck = fakeHashCheck()
	// Заменить все: пустой ответ не годится, нужна хоть одна ссылка.
	p.answer("", newHash)
	p.input("4\n2\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "Закрытых звонков нет") || !strings.Contains(stderr, "Не нашлось ни одной ссылки") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if got := strings.Join(storedHashes(t, p), " "); got != newHash {
		t.Errorf("хеши после замены: %s", got)
	}
}

// Назад — ничего не меняется; проверка, которая не удалась, ничего не
// выбрасывает.
func TestMenuUpdateHashesBackAfterFailedCheck(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.hashCheck = func(context.Context, core.Config, core.Sink) error { return errors.New("нет сети") }
	p.input("4\n0\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "Проверить ссылки не удалось: нет сети") || !strings.Contains(stdout, "Ссылки не изменены") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if got := len(storedHashes(t, p)); got != 3 {
		t.Errorf("хешей %d, ожидалось 3", got)
	}
}

// Пароль — дважды, несовпадение переспрашивается.
func TestMenuReplacesPassword(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.answer("новый", "другой", "новый", "новый")
	p.input("5\n1\n0\n")

	code, _, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "Пароли не совпали") {
		t.Errorf("нет переспроса:\n%s", stderr)
	}
	h := loadTestFile(t, p).Profiles["home"]
	if got, _ := config.NewSecretStoreFor(p.configPath()).Get(h.PasswordRef); got != "новый" {
		t.Errorf("пароль не заменён: %q", got)
	}
}

// Неверный адрес переспрашивается; без порта дописывается порт по умолчанию.
func TestMenuReplacesPeerRepeatsBadAddress(t *testing.T) {
	p := newMenuEnv(t)
	p.setHome()
	p.input("5\n2\nexample.org:порт\n198.51.100.7\n0\n")

	code, _, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "Не похоже на адрес") {
		t.Errorf("нет переспроса:\n%s", stderr)
	}
	if got := loadTestFile(t, p).Profiles["home"].Peer; got != "198.51.100.7:56000" {
		t.Errorf("peer = %q", got)
	}
}
