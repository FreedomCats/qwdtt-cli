//go:build !windows

package config

// На не-Windows секреты не шифруются: их защищает файл хранилища с правами 0600.
// DPAPI на этих системах нет, а собственное шифрование с ключом, который негде
// спрятать (он лёг бы рядом, в том же каталоге), защитой не было бы. Отсюда
// схема ссылки — file. На Linux, где в сеансе есть связка ключей, секреты идут
// в неё (secret_linux.go), а файл остаётся для сеансов без неё.
const (
	secretScheme    = "file"
	secretStoreKind = "файл с правами 0600"
)

func protect(plaintext []byte) ([]byte, error)    { return plaintext, nil }
func unprotect(ciphertext []byte) ([]byte, error) { return ciphertext, nil }
