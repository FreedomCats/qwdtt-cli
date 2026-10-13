package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/config"
)

func TestRelaunchArgs(t *testing.T) {
	if got, want := relaunchArgs("home", ""), []string{"connect", "home"}; !reflect.DeepEqual(got, want) {
		t.Errorf("без -config: %q, ожидалось %q", got, want)
	}

	abs, err := filepath.Abs("profiles.toml")
	if err != nil {
		t.Fatal(err)
	}
	got := relaunchArgs("home", "profiles.toml")
	if want := []string{"connect", "-config", abs, "home"}; !reflect.DeepEqual(got, want) {
		t.Errorf("с -config: %q, ожидалось %q", got, want)
	}
}

// elevatedMenu — меню с профилем tun, которому не хватает прав.
func elevatedMenu(t *testing.T) *profileEnv {
	t.Helper()
	if isElevated() {
		t.Skip("тест запущен с правами администратора: меню подключилось бы по-настоящему")
	}
	p := newMenuEnv(t)
	p.setHome("-datapath", "tun")
	return p
}

// Прав нет — меню перезапускает подключение с ними и после него
// возвращается (Linux: sudo в том же терминале).
func TestMenuConnectRelaunchesElevated(t *testing.T) {
	p := elevatedMenu(t)
	var got []string
	p.elevate = func(args []string) (bool, error) {
		got = args
		return false, nil
	}
	p.input("\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if want := []string{"connect", "-config", p.configPath(), "home"}; !reflect.DeepEqual(got, want) {
		t.Errorf("перезапуск с %q, ожидалось %q", got, want)
	}
	if n := strings.Count(stdout, "1. Подключиться"); n != 2 {
		t.Errorf("меню показано %d раз, ожидалось 2:\n%s", n, stdout)
	}
}

// Подключение ушло в своё окно (Windows) — меню закрывается без паузы.
func TestMenuConnectDetached(t *testing.T) {
	p := elevatedMenu(t)
	p.elevate = func([]string) (bool, error) { return true, nil }
	p.input("\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if n := strings.Count(stdout, "1. Подключиться"); n != 1 {
		t.Errorf("меню показано %d раз, ожидалось 1:\n%s", n, stdout)
	}
}

// У учётной записи нет прав администратора — предложить режим без них.
func TestMenuConnectOffersSocks(t *testing.T) {
	p := elevatedMenu(t)
	p.elevate = func([]string) (bool, error) { return false, errNoAdminAccount }
	p.input("\nда\n0\n")

	code, stdout, stderr := p.run("menu")
	if code != exitOK {
		t.Fatalf("код возврата %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "у этой учётной записи их нет") {
		t.Errorf("нет объяснения:\n%s", stdout)
	}
	if got := loadTestFile(t, p).Profiles["home"].Datapath; got != config.DatapathSocks {
		t.Errorf("datapath = %q, ожидался socks", got)
	}
}
