//go:build !linux

package config

// Связки ключей, которую знает qwdtt-cli, здесь нет: на Windows секреты
// шифрует DPAPI, на macOS — только файл (Keychain — когда будет на чём
// проверить).
var openKeyring func() (keyring, error)
