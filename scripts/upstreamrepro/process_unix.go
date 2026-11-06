//go:build unix

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	// Kill the whole command group before removing inputs: go test has children.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != syscall.ESRCH {
			return err
		}
		return os.ErrProcessDone
	}
}

func finishProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		// A child may still hold a pipe after the command exits or WaitDelay expires.
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
