package datapath

import (
	"errors"

	"golang.org/x/sys/windows"
)

// lockNeedsPrivilege — мьютекс в пространстве Global без прав не создать.
func lockNeedsPrivilege(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
