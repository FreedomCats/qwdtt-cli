// dnsguard.go — защита от утечки DNS для datapath, которые забирают весь
// трафик системы (задача W4 Э2, Requirements.DNSGuard).
//
// Сессия WFP (на Linux — таблица nftables, plan-e4.md, D1) открывается здесь,
// до первой попытки, и живёт весь tt connect; правила ставит и снимает сам
// datapath. Отказ — по умолчанию ошибка с
// подсказкой про -allow-leak (решение пользователя: подключение без
// защиты — только явным согласием).
package cli

import (
	"fmt"
	"io"
	"net/netip"

	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

// dnsGuardSession — то, что CLI получает от winsys.DNSGuard и
// linuxsys.DNSGuard.
type dnsGuardSession interface {
	datapath.DNSGuard
	Close() error
}

// openDNSGuard — переменная ради тестов: настоящая защита требует прав
// администратора (root на Linux). Платформенная — openSystemDNSGuard.
var openDNSGuard = openSystemDNSGuard

// prepareSystemDNS — тоже ради тестов: настоящая читает /etc и убирает
// остатки прошлого запуска (systemDNSReady, plan-e4.md, D2).
var prepareSystemDNS = systemDNSReady

// setupDNSGuard открывает защиту DNS и возвращает её для datapath вместе
// с функцией закрытия. Без -allow-leak отказ — ошибка, с ним —
// предупреждение в warn и работа без защиты (guard == nil).
func setupDNSGuard(out *printer, warn io.Writer, allowLeak bool) (guard datapath.DNSGuard, closeGuard func(), err error) {
	session, err := openDNSGuard()
	if err != nil {
		if !allowLeak {
			return nil, nil, fmt.Errorf("защита от утечки DNS не включилась: %w; без неё DNS-запросы "+
				"системы могут уходить мимо туннеля к провайдеру. Подключиться без защиты: "+
				"добавьте флаг -allow-leak", err)
		}
		fmt.Fprintf(warn, "Предупреждение: защита от утечки DNS не включилась: %v — "+
			"с -allow-leak подключаюсь без неё, DNS-запросы системы могут уходить мимо туннеля\n", err)
		return nil, func() {}, nil
	}
	out.line("защита DNS: пока туннель поднят, DNS системы — только к резолверам туннеля")

	guard = session
	if allowLeak {
		guard = tolerantDNSGuard{guard: session, logf: out.logf}
	}
	return guard, func() { _ = session.Close() }, nil
}

// tolerantDNSGuard — защита DNS под -allow-leak: отказ правил посреди
// сеанса — строка в выводе, а не сбой попытки.
type tolerantDNSGuard struct {
	guard datapath.DNSGuard
	logf  func(string, ...any)
}

func (g tolerantDNSGuard) Enable(dns []netip.Addr) error {
	if err := g.guard.Enable(dns); err != nil {
		g.logf("[DNS] Внимание: %v — с -allow-leak туннель работает без защиты DNS", err)
	}
	return nil
}

func (g tolerantDNSGuard) Disable() error { return g.guard.Disable() }

// BlockIPv6 передаётся защите, если она это умеет (datapath.IPv6Blocker).
func (g tolerantDNSGuard) BlockIPv6(block bool) {
	if blocker, ok := g.guard.(datapath.IPv6Blocker); ok {
		blocker.BlockIPv6(block)
	}
}
