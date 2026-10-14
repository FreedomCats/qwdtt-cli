package config

import (
	"errors"
	"fmt"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/secretservice"
)

// Связку не разблокировали — снаружи это ErrKeyringLocked, а текст остаётся
// тем, что сказала связка, без повтора.
func TestKeyringErrorLocked(t *testing.T) {
	src := fmt.Errorf("%w: окно закрыто без ответа", secretservice.ErrLocked)
	err := keyringError(src)
	if !errors.Is(err, ErrKeyringLocked) || !errors.Is(err, secretservice.ErrLocked) {
		t.Errorf("%v: метки потеряны", err)
	}
	if err.Error() != src.Error() {
		t.Errorf("текст %q, ожидался %q", err, src)
	}
	if errors.Is(keyringError(errors.New("другая беда")), ErrKeyringLocked) {
		t.Error("ErrKeyringLocked у посторонней ошибки")
	}
}
