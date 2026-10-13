//go:build unix

package captcha

import (
	"os/exec"
	"slices"
	"syscall"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/sudo"
)

var sudoUser = &sudo.User{Name: "user", Uid: 4242, Gid: 4343, HomeDir: "/home/user"}

// Окружение sudo превращается в окружение сеанса пользователя: его HOME и имя,
// каталог сеанса и шина D-Bus, без SUDO_*; DISPLAY и прочее остаётся.
func TestInvokerEnv(t *testing.T) {
	environ := []string{
		"HOME=/root", "USER=root", "LOGNAME=root",
		"DISPLAY=:0", "XAUTHORITY=/home/user/.Xauthority", "PATH=/usr/bin",
		"SUDO_USER=user", "SUDO_UID=4242", "SUDO_GID=4343", "SUDO_COMMAND=/usr/bin/tt",
	}
	present := map[string]bool{"/run/user/4242": true, "/run/user/4242/bus": true}

	got := invokerEnv(environ, sudoUser, func(path string) bool { return present[path] })
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
func TestInvokerEnvWithoutSession(t *testing.T) {
	environ := []string{"HOME=/root", "XDG_RUNTIME_DIR=/run/user/4242"}

	got := invokerEnv(environ, sudoUser, func(string) bool { return false })
	want := []string{"XDG_RUNTIME_DIR=/run/user/4242", "HOME=/home/user", "USER=user", "LOGNAME=user"}
	if !slices.Equal(got, want) {
		t.Errorf("окружение: %q, ожидалось %q", got, want)
	}
}

// Программа, открывающая ссылку, — в своей группе процессов: Ctrl+C в
// терминале tt до неё не доходит.
func TestPrepareCommandOwnProcessGroup(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := prepareCommand(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("нет программы sleep: %v", err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if pgid != cmd.Process.Pid || pgid == syscall.Getpgrp() {
		t.Errorf("группа процессов %d, ожидалась своя (%d), а не группа теста %d",
			pgid, cmd.Process.Pid, syscall.Getpgrp())
	}
}
