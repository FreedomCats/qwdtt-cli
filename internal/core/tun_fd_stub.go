//go:build !linux

package core

import (
	"fmt"
	"os"
)

// recvTunFD существует только ради компиляции: режим rawtun работает через
// передачу дескриптора по SCM_RIGHTS и доступен лишь на Linux и Android.
func recvTunFD(sockPath string) (*os.File, error) {
	return nil, fmt.Errorf("режим rawtun (передача TUN fd) доступен только на Linux/Android")
}
