//go:build !unix

package captcha

import "os/exec"

// prepareCommand — на Windows sudo нет, браузер и так запускается от имени
// того, кто запустил qwdtt-cli, а открывает его rundll32 вне консоли qwdtt-cli, так что
// Ctrl+C его не задевает. См. open_unix.go.
func prepareCommand(*exec.Cmd) error { return nil }
