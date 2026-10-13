//go:build !unix

package captcha

import "os/exec"

// asInvoker — на Windows sudo нет, браузер и так запускается от имени того, кто
// запустил tt. См. open_unix.go.
func asInvoker(*exec.Cmd) error { return nil }
