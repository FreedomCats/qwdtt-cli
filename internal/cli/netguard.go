// netguard.go — включение netguard для команд, которые запускают ядро
// (задача N3 Э2).
//
// Когда datapath забирает весь трафик системы (Requirements.Netguard), сокеты
// ядра уводятся мимо туннеля: core.Config.DialControl = netguard.Guard.Control
// (architecture.md §05). Для socks это не нужно, и Э1 там не меняется.
package cli

import (
	"os"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
	"github.com/FreedomCats/qwdtt-cli/internal/netguard"
)

// netguardEnv — переменная окружения: TT_NETGUARD=1 включает netguard при
// любом datapath и в tt ping / tt hashes. Нужна, чтобы проверить хук без прав
// администратора: socks трафик системы не забирает, и без неё строк
// [NETGUARD] не увидеть.
const netguardEnv = "TT_NETGUARD"

func netguardForced() bool { return os.Getenv(netguardEnv) == "1" }

// setupNetguard выбирает физический интерфейс и возвращает хук для ядра.
// Выбор печатается строкой; при -v каждый привязанный сокет — строкой
// [NETGUARD].
//
// Интерфейс выбирается один раз на запуск: следить за сменой сети будет S2.
func setupNetguard(out *printer, verbose bool) (core.DialControlFunc, error) {
	guard := &netguard.Guard{}
	if verbose {
		guard.Logf = out.logf
	}
	choice, err := guard.SelectInterfaces(0)
	if err != nil {
		return nil, err
	}
	out.line("netguard: сокеты ядра идут мимо туннеля, %s", choice)
	return guard.Control, nil
}
