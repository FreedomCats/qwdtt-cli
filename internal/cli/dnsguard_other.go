//go:build !windows && !linux

package cli

import "errors"

// openSystemDNSGuard — защиты DNS здесь нет: datapath, которым она нужна,
// есть только на Windows и Linux.
func openSystemDNSGuard() (dnsGuardSession, error) { return nil, errors.ErrUnsupported }

// dnsGuardMark — см. dnsguard_linux.go.
const dnsGuardMark = 0
