// dnsguard.go — защита от утечки DNS для datapath, которые забирают весь
// трафик системы (задача W4 Э2, Requirements.DNSGuard).
//
// Сессия WFP (на Linux — таблица nftables, plan-e4.md, D1) открывается здесь,
// до первой попытки, и живёт весь tt connect; правила ставит и снимает сам
// datapath. В той же сессии живёт kill-switch (killswitch.go). Отказ — по
// умолчанию ошибка с подсказкой про -allow-leak (решение пользователя:
// подключение без защиты — только явным согласием).
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

// dnsGuardSetup — открытая защита DNS: что отдать datapath и супервизору.
type dnsGuardSetup struct {
	// guard — защита для datapath; nil — без неё (-allow-leak при отказе).
	guard datapath.DNSGuard
	// killSwitch — kill-switch, если он включился: признак «закрыто» для
	// супервизора и окно ручной капчи; nil — его нет.
	killSwitch *killSwitchGuard
	// close закрывает сессию; правила уходят вместе с ней.
	close func()
}

// setupDNSGuard открывает защиту DNS и, если killSwitchOff пуст, ставит
// kill-switch (plan-e3.md, решение 2); killSwitchOff — чем он выключен, для
// строки. Без -allow-leak отказ любой из двух — ошибка, с ним —
// предупреждение в warn и работа без того, что не включилось.
func setupDNSGuard(out *printer, warn io.Writer, allowLeak bool, killSwitchOff string) (*dnsGuardSetup, error) {
	what, them, leak := "защита от утечки DNS не включилась", "неё",
		"DNS-запросы системы могут уходить мимо туннеля к провайдеру"
	if killSwitchOff == "" {
		what, them, leak = "защита от утечки DNS и kill-switch не включились", "них",
			"DNS-запросы и трафик системы могут уходить мимо туннеля"
	}
	session, err := openDNSGuard()
	if err != nil {
		if !allowLeak {
			return nil, fmt.Errorf("%s: %w; без %s %s. Подключиться без защиты: "+
				"добавьте флаг -allow-leak", what, err, them, leak)
		}
		fmt.Fprintf(warn, "Предупреждение: %s: %v — с -allow-leak подключаюсь без %s, %s\n",
			what, err, them, leak)
		return &dnsGuardSetup{close: func() {}}, nil
	}
	setup := &dnsGuardSetup{guard: session, close: func() { _ = session.Close() }}
	out.line("защита DNS: пока туннель поднят, DNS системы — только к резолверам туннеля")

	if allowLeak {
		setup.guard = tolerantDNSGuard{guard: session, logf: out.logf}
	}
	if killSwitchOff != "" {
		out.line("kill-switch выключен (%s): пока туннель не поднят, трафик системы идёт мимо него", killSwitchOff)
		return setup, nil
	}
	lockdown, ok := session.(lockdownSession)
	if !ok {
		// Windows до W1: правил «закрыто» в WFP ещё нет.
		fmt.Fprintln(warn, "Предупреждение: kill-switch на этой системе ещё не сделан — "+
			"пока туннель не поднят, трафик системы идёт мимо него")
		return setup, nil
	}
	ks, err := newKillSwitchGuard(lockdown, allowLeak, out.logf)
	if err != nil {
		if !allowLeak {
			setup.close()
			return nil, fmt.Errorf("kill-switch не включился: %w; без него, пока туннель не поднят, "+
				"трафик системы идёт мимо туннеля. Подключиться без него: -no-kill-switch или -allow-leak", err)
		}
		fmt.Fprintf(warn, "Предупреждение: kill-switch не включился: %v — с -allow-leak подключаюсь без него, "+
			"пока туннель не поднят, трафик системы идёт мимо туннеля\n", err)
		return setup, nil
	}
	out.line("kill-switch: пока туннель не поднят, трафик системы заблокирован")
	setup.guard, setup.killSwitch = ks, ks
	return setup, nil
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
