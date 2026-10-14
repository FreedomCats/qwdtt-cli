//go:build !linux

package cli

// runSecretHelper: связка ключей с запуском от имени вызвавшего sudo есть
// только на Linux (secrethelper_linux.go).
func runSecretHelper([]string) (int, bool) { return 0, false }
