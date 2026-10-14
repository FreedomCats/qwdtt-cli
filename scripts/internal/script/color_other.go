//go:build !windows

package script

import "os"

func enableColor(*os.File) bool { return true }
