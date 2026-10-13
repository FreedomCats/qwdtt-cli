//go:build !windows

package datapath

import "errors"

// locateWireGuard на других платформах не ищет ничего: штатный клиент там
// устроен иначе (wg-quick, а не служба), и в Э1 datapath external — только
// для Windows (architecture.md §11). Явно заданный ExternalOptions.Program
// этим не ограничен — так проверяются тесты.
func locateWireGuard() (string, error) {
	return "", errors.New("datapath external в этом выпуске поддерживается только на Windows")
}
