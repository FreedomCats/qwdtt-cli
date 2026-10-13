//go:build !windows

package datapath

import "errors"

// openWintunAdapter вне Windows не создаёт ничего: свой адаптер на Linux
// и macOS — этап Э4 (/dev/net/tun и utun), Wintun там нет.
func openWintunAdapter(string, *WGConfig, func(string, ...any)) (*wintunAdapter, error) {
	return nil, errors.New("datapath wintun поддерживается только на Windows")
}
