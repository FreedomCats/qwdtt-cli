//go:build !windows

package cli

// ownsConsole — окно терминала открыто ради одного qwdtt-cli. Вне Windows так не
// бывает: терминал запускает оболочку, а qwdtt-cli — уже из неё. См. pause.go.
func ownsConsole() bool { return false }
