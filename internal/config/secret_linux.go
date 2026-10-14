package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/FreedomCats/qwdtt-cli/internal/secretservice"
)

// На Linux секреты идут в связку ключей сеанса (Secret Service): там они
// зашифрованы ключом, который открывается входом в систему, как DPAPI на
// Windows. Связки нет — файл с правами 0600 (secret_unix.go). Ссылки обоих
// видов читаются: секреты, записанные в файл раньше, переезжают в связку,
// только когда их перезаписывают.
var openKeyring = func() (keyring, error) {
	k, err := secretservice.Open(os.Stderr)
	if err != nil {
		return nil, keyringError(err)
	}
	return linuxKeyring{k}, nil
}

// linuxKeyring переводит ошибки пакета secretservice в ошибки хранилища.
type linuxKeyring struct{ k secretservice.Keyring }

func (l linuxKeyring) Put(id, label, value string) error {
	return keyringError(l.k.Put(id, label, value))
}
func (l linuxKeyring) Delete(id string) error { return keyringError(l.k.Delete(id)) }
func (l linuxKeyring) Close() error           { return l.k.Close() }

func (l linuxKeyring) Get(id string) (string, error) {
	value, err := l.k.Get(id)
	return value, keyringError(err)
}

func keyringError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, secretservice.ErrUnavailable):
		return fmt.Errorf("%w: %w", errKeyringUnavailable, err)
	case errors.Is(err, secretservice.ErrNotFound):
		return fmt.Errorf("%w: %w", errKeyringNotFound, err)
	}
	return err
}
