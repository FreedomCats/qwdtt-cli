//go:build !windows

package winsys

import (
	"errors"
	"net/netip"
)

// DNSGuard вне Windows не существует: datapath, которым он нужен (tun,
// external), есть только там. Тип объявлен, чтобы CLI собирался везде.
type DNSGuard struct{}

// OpenDNSGuard вне Windows всегда отказывает.
func OpenDNSGuard() (*DNSGuard, error) { return nil, errors.ErrUnsupported }

func (*DNSGuard) Enable([]netip.Addr) error { return errors.ErrUnsupported }
func (*DNSGuard) Disable() error            { return nil }
func (*DNSGuard) Close() error              { return nil }
