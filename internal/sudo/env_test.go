package sudo

import (
	"slices"
	"testing"
)

var sudoUser = &User{Name: "user", Uid: 4242, Gid: 4343, HomeDir: "/home/user"}

// Окружение sudo превращается в окружение сеанса пользователя: его HOME и имя,
// каталог сеанса и шина D-Bus, без SUDO_*; DISPLAY и прочее остаётся.
func TestSessionEnv(t *testing.T) {
	environ := []string{
		"HOME=/root", "USER=root", "LOGNAME=root",
		"DISPLAY=:0", "XAUTHORITY=/home/user/.Xauthority", "PATH=/usr/bin",
		"SUDO_USER=user", "SUDO_UID=4242", "SUDO_GID=4343", "SUDO_COMMAND=/usr/bin/qwdtt-cli",
	}
	present := map[string]bool{"/run/user/4242": true, "/run/user/4242/bus": true}

	got := sessionEnv(environ, sudoUser, func(path string) bool { return present[path] })
	want := []string{
		"DISPLAY=:0", "XAUTHORITY=/home/user/.Xauthority", "PATH=/usr/bin",
		"HOME=/home/user", "USER=user", "LOGNAME=user",
		"XDG_RUNTIME_DIR=/run/user/4242", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/4242/bus",
	}
	if !slices.Equal(got, want) {
		t.Errorf("окружение:\n  %q\nожидалось:\n  %q", got, want)
	}
}

// Каталога сеанса нет (пользователь не вошёл в систему) — переменные сеанса
// не выдумываются, а пришедшие остаются как есть.
func TestSessionEnvWithoutSession(t *testing.T) {
	environ := []string{"HOME=/root", "XDG_RUNTIME_DIR=/run/user/4242"}

	got := sessionEnv(environ, sudoUser, func(string) bool { return false })
	want := []string{"XDG_RUNTIME_DIR=/run/user/4242", "HOME=/home/user", "USER=user", "LOGNAME=user"}
	if !slices.Equal(got, want) {
		t.Errorf("окружение: %q, ожидалось %q", got, want)
	}
}
