//go:build !windows

package cli

import "os"

// isElevated — root ли процесс. См. elevated_windows.go.
func isElevated() bool {
	return os.Geteuid() == 0
}
