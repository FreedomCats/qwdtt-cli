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

// netguardEnv — переменная окружения: QWDTT_NETGUARD=1 включает netguard при
// любом datapath и в qwdtt-cli ping / qwdtt-cli hashes. Нужна, чтобы проверить хук без прав
// администратора: socks трафик системы не забирает, и без неё строк
// [NETGUARD] не увидеть.
const netguardEnv = "QWDTT_NETGUARD"

func netguardForced() bool { return os.Getenv(netguardEnv) == "1" }

// setupNetguard выбирает физический интерфейс и возвращает netguard, чей
// Control — хук для ядра. Выбор печатается строкой; при -v каждый
// привязанный сокет — строкой [NETGUARD].
//
// fullTunnel — datapath забирает весь трафик системы (Requirements.Netguard):
// тогда проверяется обратный фильтр системы. Строгий rp_filter на выбранном
// интерфейсе — отказ до подключения, строгий фильтр IPv6 — сокеты ядра
// только IPv4 (plan-e4.md, T3).
//
// mark — метка SO_MARK сокетов ядра (Guard.Mark), 0 — без неё: по ней
// защита DNS на Linux пропускает qwdtt-cli к DNS VK. Ставится, только когда защита
// включена: без прав SO_MARK отказал бы (plan-e4.md, врезка N1, п. 1).
//
// Интерфейс выбирается здесь один раз; qwdtt-cli connect дальше следит за сменой
// сети (watchNetwork), qwdtt-cli ping и qwdtt-cli hashes — нет: они короткие.
func setupNetguard(out *printer, verbose, fullTunnel bool, mark uint32) (*netguard.Guard, error) {
	guard := &netguard.Guard{Mark: mark}
	if verbose {
		guard.Logf = out.logf
	}
	if fullTunnel {
		why, err := ipv6ReplyFilter()
		if err != nil {
			out.line("Внимание: фильтр обратного пути IPv6 не проверен: %v", err)
		}
		if why != "" {
			guard.NoIPv6 = true
			out.line("netguard: сокеты ядра — только IPv4: %s", why)
		}
	}
	choice, err := guard.SelectInterfaces(0)
	if err != nil {
		return nil, err
	}
	out.line("netguard: сокеты ядра идут мимо туннеля, %s", choice)
	if fullTunnel {
		problem, err := reversePathCheck(guard.Choice())
		if err != nil {
			out.line("Внимание: rp_filter не проверен: %v", err)
		}
		if problem != "" {
			return nil, errors.New(problem + "; или подключитесь с -datapath socks")
		}
	}
	return guard, nil
}

// reversePathCheck — переменная ради тестов: настоящая читает /proc.
var reversePathCheck = reversePathProblem

// errNetworkChanged — причина перезапуска при смене сети; её печатает
// строка перехода «переподключение: сеть сменилась».
var errNetworkChanged = errors.New("сеть сменилась")

// watchNetwork следит за сменой физического интерфейса (задача S2 Э2):
// новые индексы netguard ставит сам, выбор печатается строкой, попытка
// перезапускается (networkChanged). exclude — AdapterID своего адаптера,
// его события не в счёт.
//
// Отказ подписки подключиться не мешает: туннель работает, но после смены
// сети восстановится только по watchdog, примерно через минуту, — об этом
// строка.
func watchNetwork(out *printer, guard *netguard.Guard, restart func(error), exclude func() uint64, fullTunnel bool) (stop func()) {
	stop, err := guard.Watch(netguard.WatchOptions{
		Exclude: exclude,
		Changed: networkChanged(out, restart, fullTunnel),
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
//
// fullTunnel — обратный фильтр проверяется и у нового интерфейса: строгий
// не останавливает переподключения (сеть может смениться ещё раз), но
// объясняет, почему они не удаются.
func networkChanged(out *printer, restart func(error), fullTunnel bool) func(now, before netguard.Choice) {
	return func(now, before netguard.Choice) {
		switch {
		case now.Empty():
			out.line("сеть пропала — туннель переподключится, когда она вернётся")
			return
		case now.Same(before):
			out.line("сеть переподключилась: %s", now)
		default:
			out.line("сеть сменилась: %s", now)
		}
		if fullTunnel {
			problem, err := reversePathCheck(now)
			switch {
			case err != nil:
				out.line("Внимание: rp_filter не проверен: %v", err)
			case problem != "":
				out.line("Внимание: %s", problem)
			}
		}
		restart(errNetworkChanged)
	}
}

// ownAdapter помнит последний построенный datapath, чтобы исключать его
// адаптер (tun) из слежения за сетью. У datapath без своего адаптера
// AdapterID нет — исключать нечего.
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

func (o *ownAdapter) id() uint64 {
	o.mu.Lock()
	dp := o.dp
	o.mu.Unlock()
	if a, ok := dp.(interface{ AdapterID() uint64 }); ok {
		return a.AdapterID()
	}
	return 0
}
