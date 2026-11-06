//go:build !unix

package main

import "os/exec"

func configureProcess(cmd *exec.Cmd) {
	// CommandContext kills the command on platforms without Unix process groups.
}

func finishProcess(cmd *exec.Cmd) {}
