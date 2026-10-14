//go:build !windows

package wintun

import "github.com/FreedomCats/qwdtt-cli/scripts/internal/script"

// verifySignature вне Windows подпись не проверяет: WinVerifyTrust есть
// только там. Хеш архива к этому времени уже сверен.
func verifySignature(string) error {
	script.Say("Подпись не проверялась: WinVerifyTrust есть только на Windows (хеш архива проверен).")
	return nil
}
