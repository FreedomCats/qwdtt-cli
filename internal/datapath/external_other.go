//go:build !windows

package datapath

import (
	"context"
	"errors"
)

// locateWireGuard на других платформах не ищет ничего: штатный клиент там
// устроен иначе (wg-quick, а не служба), и в Э1 datapath external — только
// для Windows (architecture.md §11). Явно заданный ExternalOptions.Program
// этим не ограничен — так проверяются тесты.
func locateWireGuard() (string, error) {
	return "", externalUnsupported
}

// externalUnsupported — для Requires: CLI откажет им до проверки прав и
// программы.
var externalUnsupported = errors.New("datapath external в этом выпуске поддерживается только на Windows")

// waitTunnelService на других платформах проверять нечего: служб WireGuard
// для Windows там нет, а явно заданный Program — это тесты.
func waitTunnelService(context.Context, string) error { return nil }
