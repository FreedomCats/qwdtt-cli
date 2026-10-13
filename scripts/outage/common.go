//go:build windows || linux

package main

import (
	"fmt"
	"net/netip"
	"strings"
)

func parseAddrs(s string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("адрес %q: %w", f, err)
		}
		out = append(out, a.Unmap())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("нет адресов")
	}
	return out, nil
}
