//go:build !unix

package captcha

import "os/exec"

// prepareCommand — на Windows sudo нет, браузер и так запускается от имени
// того, кто запустил tt, а открывает его rundll32 вне консоли tt, так что
// Ctrl+C его не задевает. См. open_unix.go.
func prepareCommand(*exec.Cmd) error { return nil }
