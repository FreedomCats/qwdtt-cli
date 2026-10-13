// netguard.go — включение netguard для команд, которые запускают ядро
// (задача N3 Э2).
//
// Когда datapath забирает весь трафик системы (Requirements.Netguard), сокеты
// ядра уводятся мимо туннеля: core.Config.DialControl = netguard.Guard.Control
// (architecture.md §05). Для socks это не нужно, и Э1 там не меняется.
package cli

import (
	"errors"
	"os"
	"sync"

	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
	"github.com/FreedomCats/qwdtt-cli/internal/netguard"
)

// netguardEnv — переменная окружения: TT_NETGUARD=1 включает netguard при
// любом datapath и в tt ping / tt hashes. Нужна, чтобы проверить хук без прав
// администратора: socks трафик системы не забирает, и без неё строк
// [NETGUARD] не увидеть.
const netguardEnv = "TT_NETGUARD"

func netguardForced() bool { return os.Getenv(netguardEnv) == "1" }

// setupNetguard выбирает физический интерфейс и возвращает netguard, чей
// Control — хук для ядра. Выбор печатается строкой; при -v каждый
// привязанный сокет — строкой [NETGUARD].
//
// Интерфейс выбирается здесь один раз; tt connect дальше следит за сменой
// сети (watchNetwork), tt ping и tt hashes — нет: они короткие.
func setupNetguard(out *printer, verbose bool) (*netguard.Guard, error) {
	guard := &netguard.Guard{}
	if verbose {
		guard.Logf = out.logf
	}
	choice, err := guard.SelectInterfaces(0)
	if err != nil {
		return nil, err
	}
	out.line("netguard: сокеты ядра идут мимо туннеля, %s", choice)
	return guard, nil
}

// errNetworkChanged — причина перезапуска при смене сети; её печатает
// строка перехода «переподключение: сеть сменилась».
var errNetworkChanged = errors.New("сеть сменилась")

// watchNetwork следит за сменой физического интерфейса (задача S2 Э2):
// новые индексы netguard ставит сам, выбор печатается строкой, попытка
// перезапускается (networkChanged). exclude — LUID своего адаптера, его
// события не в счёт.
//
// Отказ подписки подключиться не мешает: туннель работает, но после смены
// сети восстановится только по watchdog, примерно через минуту, — об этом
// строка.
func watchNetwork(out *printer, guard *netguard.Guard, restart func(error), exclude func() uint64) (stop func()) {
	stop, err := guard.Watch(netguard.WatchOptions{
		Exclude: exclude,
		Changed: networkChanged(out, restart),
	})
	if err != nil {
		out.line("Внимание: за сменой сети не слежу (%v) — после неё туннель восстановится только по watchdog", err)
		return func() {}
	}
	return stop
}

// networkChanged — что делать со свёрнутой сменой сети.
//
// Сеть пропала — попытка не перезапускается: новой не на чем подключиться,
// а когда сеть вернётся, это будет ещё одна смена, и перезапуск — один на
// выдернутый и вставленный кабель. Если сеть пропала надолго, попытку раньше
// оборвёт watchdog, а возврат сети прервёт задержку.
func networkChanged(out *printer, restart func(error)) func(now, before netguard.Choice) {
	return func(now, before netguard.Choice) {
		switch {
		case now.Empty():
			out.line("сеть пропала — туннель переподключится, когда она вернётся")
		case now.Same(before):
			out.line("сеть переподключилась: %s", now)
			restart(errNetworkChanged)
		default:
			out.line("сеть сменилась: %s", now)
			restart(errNetworkChanged)
		}
	}
}

// ownAdapter помнит последний построенный datapath, чтобы исключать его
// адаптер (tun) из слежения за сетью. У datapath без своего адаптера
// LUID 0 — исключать нечего.
type ownAdapter struct {
	mu sync.Mutex
	dp datapath.Datapath
}

func (o *ownAdapter) track(dp datapath.Datapath) datapath.Datapath {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dp = dp
	return dp
}

func (o *ownAdapter) luid() uint64 {
	o.mu.Lock()
	dp := o.dp
	o.mu.Unlock()
	if a, ok := dp.(interface{ LUID() uint64 }); ok {
		return a.LUID()
	}
	return 0
}
