package cli

import (
	"os"
	"testing"
)

// Тесты не должны дотянуться до настоящей связки ключей того, кто их
// запускает: профили тестов записали бы туда свои секреты. Без шины сеанса
// связки нет, и секреты идут в файл, как на машине без графики.
func TestMain(m *testing.M) {
	os.Unsetenv("DBUS_SESSION_BUS_ADDRESS")
	os.Unsetenv("XDG_RUNTIME_DIR")
	os.Exit(m.Run())
}
