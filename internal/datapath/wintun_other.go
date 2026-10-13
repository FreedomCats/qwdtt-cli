//go:build !windows

package datapath

import "errors"

// openWintunAdapter вне Windows не создаёт ничего: свой адаптер на Linux
// и macOS — этап Э4 (/dev/net/tun и utun), Wintun там нет.
func openWintunAdapter(string, *WGConfig, func(string, ...any)) (*wintunAdapter, error) {
	return nil, errWintunWindowsOnly
}

// acquireWintunLock отказывает сразу: так CLI скажет «только на Windows» до
// подключения, а не после него.
func acquireWintunLock() (func(), error) { return nil, errWintunWindowsOnly }

var errWintunWindowsOnly = errors.New("datapath wintun поддерживается только на Windows")

// wintunUnsupported — для Requires: CLI откажет им до проверки прав и файлов.
var wintunUnsupported = errWintunWindowsOnly
