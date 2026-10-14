//go:build unix

package captcha

import (
	"os/exec"
	"syscall"
	"testing"
)

// Программа, открывающая ссылку, — в своей группе процессов: Ctrl+C в
// терминале qwdtt-cli до неё не доходит.
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
