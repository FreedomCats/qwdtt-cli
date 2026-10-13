//go:build windows

package core

import "syscall"

// На Windows syscall.SetsockoptInt принимает syscall.Handle, а не int.
func setReuseAddr(network, address string, c syscall.RawConn) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return setErr
}