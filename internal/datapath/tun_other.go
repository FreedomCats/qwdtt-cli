//go:build !windows

package datapath

import "errors"

// openTunAdapter вне Windows не создаёт ничего: свой адаптер на Linux
// и macOS — этап Э4 (/dev/net/tun и utun), Wintun там нет.
func openTunAdapter(string, *WGConfig, func(string, ...any)) (*tunAdapter, error) {
	return nil, errTunWindowsOnly
}

// acquireTunLock отказывает сразу: так CLI скажет «только на Windows» до
// подключения, а не после него.
func acquireTunLock() (func(), error) { return nil, errTunWindowsOnly }

var errTunWindowsOnly = errors.New("datapath tun поддерживается только на Windows")

// tunUnsupported — для Requires: CLI откажет им до проверки прав и файлов.
var tunUnsupported = errTunWindowsOnly
