//go:build !windows

package cli

// ownsConsole — окно терминала открыто ради одного tt. Вне Windows так не
// бывает: терминал запускает оболочку, а tt — уже из неё. См. pause.go.
func ownsConsole() bool { return false }
