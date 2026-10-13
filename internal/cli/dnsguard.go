// dnsguard.go — защита от утечки DNS для datapath, которые забирают весь
// трафик системы (задача W4 Э2, Requirements.DNSGuard).
//
// Сессия WFP открывается здесь, до первой попытки, и живёт весь tt connect;
// правила ставит и снимает сам datapath. Отказ — по умолчанию ошибка с
// подсказкой про -allow-dns-leak (решение пользователя: подключение без
// защиты — только явным согласием).
package cli

import (
	"fmt"
	"io"
	"net/netip"

	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
	"github.com/FreedomCats/qwdtt-cli/internal/winsys"
)

// dnsGuardSession — то, что CLI получает от winsys.DNSGuard.
type dnsGuardSession interface {
	datapath.DNSGuard
	Close() error
}

// openDNSGuard — переменная ради тестов: настоящая сессия WFP требует прав
// администратора.
var openDNSGuard = func() (dnsGuardSession, error) {
	g, err := winsys.OpenDNSGuard()
	if err != nil {
		return nil, err
	}
	return g, nil
}

// setupDNSGuard открывает защиту DNS и возвращает её для datapath вместе
// с функцией закрытия. Без -allow-dns-leak отказ — ошибка, с ним —
// предупреждение в warn и работа без защиты (guard == nil).
func setupDNSGuard(out *printer, warn io.Writer, allowLeak bool) (guard datapath.DNSGuard, closeGuard func(), err error) {
	session, err := openDNSGuard()
	if err != nil {
		if !allowLeak {
			return nil, nil, fmt.Errorf("защита от утечки DNS не включилась: %w; без неё DNS-запросы "+
				"системы могут уходить мимо туннеля к провайдеру. Подключиться без защиты: "+
				"добавьте флаг -allow-dns-leak", err)
		}
		fmt.Fprintf(warn, "Предупреждение: защита от утечки DNS не включилась: %v — "+
			"с -allow-dns-leak подключаюсь без неё, DNS-запросы системы могут уходить мимо туннеля\n", err)
		return nil, func() {}, nil
	}
	out.line("защита DNS: пока туннель поднят, DNS системы — только к резолверам туннеля")

	guard = session
	if allowLeak {
		guard = tolerantDNSGuard{guard: session, logf: out.logf}
	}
	return guard, func() { _ = session.Close() }, nil
}

// tolerantDNSGuard — защита DNS под -allow-dns-leak: отказ правил посреди
// сеанса — строка в выводе, а не сбой попытки.
type tolerantDNSGuard struct {
	guard datapath.DNSGuard
	logf  func(string, ...any)
}

func (g tolerantDNSGuard) Enable(dns []netip.Addr) error {
	if err := g.guard.Enable(dns); err != nil {
		g.logf("[DNS] Внимание: %v — с -allow-dns-leak туннель работает без защиты DNS", err)
	}
	return nil
}

func (g tolerantDNSGuard) Disable() error { return g.guard.Disable() }
