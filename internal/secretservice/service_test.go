//go:build linux

package secretservice

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/app"
	"github.com/FreedomCats/qwdtt-cli/internal/sudo"
)

// Круг: записали, прочитали, перезаписали, удалили.
func TestRoundTrip(t *testing.T) {
	f, c := newFakeService(t)

	if err := c.Put("0123abcd", "qwdtt-cli home.password", "пароль"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := c.Get("0123abcd"); err != nil || got != "пароль" {
		t.Fatalf("Get: %q, %v", got, err)
	}
	if err := c.Put("0123abcd", "qwdtt-cli home.password", "новый"); err != nil {
		t.Fatalf("Put поверх: %v", err)
	}
	if got, _ := c.Get("0123abcd"); got != "новый" {
		t.Errorf("после перезаписи: %q", got)
	}
	f.with(func() {
		if len(f.items) != 1 {
			t.Errorf("записей %d, ожидалась одна: перезапись не должна плодить их", len(f.items))
		}
		for _, item := range f.items {
			if item.attrs[attrApp] != app.Name || item.label != "qwdtt-cli home.password" {
				t.Errorf("запись: метка %q, атрибуты %v", item.label, item.attrs)
			}
		}
		if f.algorithm != "plain" {
			t.Errorf("сеанс открыт с алгоритмом %q", f.algorithm)
		}
	})

	if err := c.Delete("0123abcd"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := c.Get("0123abcd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get после удаления: %v, ожидалось ErrNotFound", err)
	}
	if err := c.Delete("0123abcd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("повторный Delete: %v, ожидалось ErrNotFound", err)
	}
}

// Заблокированная связка разблокируется через своё окно, и человеку сказано,
// чего от него ждут.
func TestUnlockPrompt(t *testing.T) {
	f, c := newFakeService(t)
	if err := c.Put("id1", "label", "значение"); err != nil {
		t.Fatal(err)
	}
	f.with(func() { f.locked = true })
	var notice strings.Builder
	c.notify = &notice

	got, err := c.Get("id1")
	if err != nil || got != "значение" {
		t.Fatalf("Get из заблокированной связки: %q, %v", got, err)
	}
	f.with(func() {
		if f.prompts != 1 {
			t.Errorf("окон разблокировки %d, ожидалось одно", f.prompts)
		}
	})
	if !strings.Contains(notice.String(), "заблокирована") {
		t.Errorf("человеку не сказано про окно: %q", notice.String())
	}
}

// Окно закрыли без ответа — ошибка, а не пустое значение.
func TestUnlockDismissed(t *testing.T) {
	f, c := newFakeService(t)
	if err := c.Put("id1", "label", "значение"); err != nil {
		t.Fatal(err)
	}
	f.with(func() { f.locked, f.dismiss = true, true })

	if _, err := c.Get("id1"); !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "закрыто") {
		t.Errorf("Get: %v, ожидалась ErrLocked «окно закрыто»", err)
	}
	if err := c.Put("id2", "label", "x"); err == nil {
		t.Error("Put в заблокированную связку прошёл")
	}
}

// Окно не отвечает (его не видно) — ошибка по таймауту, окно убирается.
func TestUnlockTimeout(t *testing.T) {
	f, c := newFakeService(t)
	if err := c.Put("id1", "label", "значение"); err != nil {
		t.Fatal(err)
	}
	f.with(func() { f.locked, f.silent = true, true })
	c.timeout = 200 * time.Millisecond

	if _, err := c.Get("id1"); !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "не ответило") {
		t.Errorf("Get: %v, ожидалась ErrLocked по таймауту", err)
	}
	f.with(func() {
		if f.dismissed != 1 {
			t.Errorf("окно не убрано: Dismiss %d раз", f.dismissed)
		}
	})
}

// Нет коллекции по умолчанию — связки как бы нет: секреты пойдут в файл.
func TestNoDefaultCollection(t *testing.T) {
	f, c := newFakeService(t)
	f.with(func() { f.noDefault = true })
	if err := c.Put("id1", "label", "x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Put: %v, ожидалось ErrUnavailable", err)
	}
}

// Шина есть, а сервиса на ней нет — ErrUnavailable. Запускать его через
// D-Bus activation нельзя: без графики он спросил бы пароль в пустоту.
func TestServiceNotRunning(t *testing.T) {
	addr := startBus(t)
	_, err := dial(func(key string) string {
		if key == "DBUS_SESSION_BUS_ADDRESS" {
			return addr
		}
		return ""
	}, io.Discard)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("dial: %v, ожидалось ErrUnavailable", err)
	}
}

// Без шины сеанса (SSH, сервер) — ErrUnavailable, а не своя шина.
func TestNoSessionBus(t *testing.T) {
	_, err := dial(func(key string) string {
		if key == "XDG_RUNTIME_DIR" {
			return t.TempDir() // каталог есть, сокета bus в нём нет
		}
		return ""
	}, io.Discard)
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("dial: %v, ожидалось ErrUnavailable", err)
	}
}

func TestBusAddress(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"XDG_RUNTIME_DIR": dir}
	getenv := func(key string) string { return env[key] }

	if got := busAddress(getenv); got != "" {
		t.Errorf("без сокета: %q", got)
	}
	bus := filepath.Join(dir, "bus")
	if err := writeEmpty(bus); err != nil {
		t.Fatal(err)
	}
	if got, want := busAddress(getenv), "unix:path="+bus; got != want {
		t.Errorf("сокет в каталоге сеанса: %q, ожидалось %q", got, want)
	}
	env["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=/elsewhere"
	if got := busAddress(getenv); got != "unix:path=/elsewhere" {
		t.Errorf("с DBUS_SESSION_BUS_ADDRESS: %q", got)
	}
}

// helper под sudo: запрос — в stdin процесса от имени вызвавшего, значение
// секрета не попадает в его аргументы; ошибки связки доезжают теми же.
func TestHelper(t *testing.T) {
	mem := &memKeyring{values: map[string]string{}}
	u := &sudo.User{Name: "user", Uid: 4242, Gid: 4343, Groups: []uint32{4343}, HomeDir: "/home/user"}
	var cmds []*exec.Cmd
	h := &helper{user: u, exe: "/usr/bin/qwdtt-cli", stderr: io.Discard, run: func(cmd *exec.Cmd) error {
		cmds = append(cmds, cmd)
		var req request
		raw, err := io.ReadAll(cmd.Stdin)
		if err != nil {
			return err
		}
		if err := jsonUnmarshal(raw, &req); err != nil {
			return err
		}
		answer(req, cmd.Stdout, cmd.Stderr, func() (Keyring, error) { return mem, nil })
		return nil
	}}

	if err := h.Put("id1", "label", "секрет"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := h.Get("id1"); err != nil || got != "секрет" {
		t.Fatalf("Get: %q, %v", got, err)
	}
	if err := h.Delete("id1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := h.Get("id1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get после удаления: %v, ожидалось ErrNotFound", err)
	}

	cmd := cmds[0]
	if strings.Join(cmd.Args, " ") != "/usr/bin/qwdtt-cli "+HelperCommand {
		t.Errorf("аргументы: %q", cmd.Args)
	}
	cred := cmd.SysProcAttr.Credential
	if cred == nil || cred.Uid != 4242 || cred.Gid != 4343 {
		t.Errorf("запуск не от имени вызвавшего: %+v", cred)
	}
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "SUDO_") {
			t.Errorf("в окружении осталось %s", kv)
		}
	}

	mem.err = ErrUnavailable
	if err := h.Put("id2", "label", "x"); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Put без связки: %v, ожидалось ErrUnavailable", err)
	}
	mem.err = fmt.Errorf("%w: окно закрыто без ответа", ErrLocked)
	if _, err := h.Get("id1"); !errors.Is(err, ErrLocked) || err.Error() != mem.err.Error() {
		t.Errorf("Get при заблокированной связке: %v, ожидалось ErrLocked с тем же текстом", err)
	}
}

// Процесс помощника не запустился — ошибка, а не «записи нет».
func TestHelperRunFails(t *testing.T) {
	h := &helper{user: &sudo.User{Name: "user"}, exe: "qwdtt-cli", stderr: io.Discard,
		run: func(*exec.Cmd) error { return errors.New("exec: отказано") }}
	_, err := h.Get("id1")
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnavailable) {
		t.Errorf("Get: %v", err)
	}
}

// RunHelper на кривом запросе не молчит.
func TestRunHelperBadRequest(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := RunHelper(strings.NewReader("не json"), &out, &errOut); code == 0 {
		t.Error("код 0 на кривом запросе")
	}
	if errOut.Len() == 0 {
		t.Error("ошибка не напечатана")
	}
}
