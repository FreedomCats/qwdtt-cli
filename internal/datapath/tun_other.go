//go:build !windows && !linux

package datapath

import "errors"

// openTunAdapter на прочих системах не создаёт ничего: свой адаптер на
// macOS — этап Э4 (utun), Wintun там нет.
func openTunAdapter(string, *WGConfig, func(string, ...any)) (*tunAdapter, error) {
	return nil, errTunUnsupported
}

// acquireTunLock отказывает сразу: так CLI скажет «не поддерживается» до
// подключения, а не после него.
func acquireTunLock() (func(), error) { return nil, errTunUnsupported }

var errTunUnsupported = errors.New("datapath tun поддерживается только на Windows и Linux")

// tunUnsupported — для Requires: CLI откажет им до проверки прав и файлов.
var tunUnsupported = errTunUnsupported

// tunFiles — файлов нет: Unsupported перекрывает остальные требования.
var tunFiles []string
