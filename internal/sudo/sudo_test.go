package sudo

import (
	"errors"
	"os/user"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func lookupOf(users ...*user.User) func(string) (*user.User, error) {
	return func(name string) (*user.User, error) {
		for _, u := range users {
			if u.Username == name {
				return u, nil
			}
		}
		return nil, user.UnknownUserError(name)
	}
}

var someone = &user.User{Username: "user", Uid: "4242", Gid: "4343", HomeDir: "/home/user"}

func TestInvokerUnderSudo(t *testing.T) {
	got, err := invoker(0, envOf(map[string]string{"SUDO_USER": "user", "HOME": "/root"}), lookupOf(someone))
	if err != nil {
		t.Fatalf("invoker: %v", err)
	}
	if got == nil {
		t.Fatal("под sudo пользователь не найден")
	}
	if got.Name != "user" || got.Uid != 4242 || got.Gid != 4343 || got.HomeDir != "/home/user" {
		t.Errorf("invoker = %+v", got)
	}
	if len(got.Groups) == 0 {
		t.Error("нет ни одной группы: должна остаться хотя бы основная")
	}
}

// Без sudo пакет молчит: ни пользователя, ни ошибки.
func TestInvokerWithoutSudo(t *testing.T) {
	tests := []struct {
		name string
		euid int
		env  map[string]string
	}{
		{"обычный пользователь", 1000, map[string]string{"SUDO_USER": "user"}},
		{"root без sudo", 0, map[string]string{}},
		{"sudo из-под root", 0, map[string]string{"SUDO_USER": "root"}},
		{"windows", -1, map[string]string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(string) (*user.User, error) {
				t.Fatal("без sudo базу пользователей не спрашивают")
				return nil, nil
			}
			got, err := invoker(tt.euid, envOf(tt.env), lookup)
			if got != nil || err != nil {
				t.Errorf("invoker = %+v, %v; ожидалось nil, nil", got, err)
			}
		})
	}
}

// Пользователь под sudo есть, но найти его нельзя — ошибка, а не тихий root.
func TestInvokerUnknownUser(t *testing.T) {
	_, err := invoker(0, envOf(map[string]string{"SUDO_USER": "ghost"}), lookupOf(someone))
	var unknown user.UnknownUserError
	if !errors.As(err, &unknown) {
		t.Errorf("err = %v, ожидалась UnknownUserError", err)
	}
}

func TestInvokerBadRecord(t *testing.T) {
	tests := []struct {
		name string
		u    *user.User
	}{
		{"нет домашнего каталога", &user.User{Username: "user", Uid: "4242", Gid: "4343"}},
		{"uid не число", &user.User{Username: "user", Uid: "S-1-5", Gid: "4343", HomeDir: "/home/user"}},
		{"gid не число", &user.User{Username: "user", Uid: "4242", Gid: "", HomeDir: "/home/user"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := invoker(0, envOf(map[string]string{"SUDO_USER": "user"}), lookupOf(tt.u)); err == nil {
				t.Error("испорченная запись принята")
			}
		})
	}
}
