// killswitch.go — kill-switch для datapath (plan-e3.md, K3): пока туннель
// не поднят, трафик системы заблокирован.
//
// Отдельного объекта у kill-switch нет: это третий набор правил в той же
// сессии WFP (на Linux — таблице nftables), что и защита DNS. Datapath о нём
// не знает: обёртка killSwitchGuard подменяет Disable защиты на «закрыто»,
// а Enable оставляет как есть — поднятый туннель держит одна защита DNS.
package cli

import (
	"net/netip"
	"sync"

	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

// lockdownSession — защита, которая умеет kill-switch: linuxsys.DNSGuard
// (Lockdown), на Windows — с W1.
type lockdownSession interface {
	datapath.DNSGuard
	// Lockdown ставит набор «закрыто» вместо прежних правил, одним
	// переходом.
	Lockdown() error
}

// killSwitchGuard — защита DNS для datapath при включённом kill-switch.
type killSwitchGuard struct {
	session   lockdownSession
	allowLeak bool
	logf      func(string, ...any)

	mu     sync.Mutex
	locked bool
}

// newKillSwitchGuard ставит «закрыто» сразу — до первой попытки (решение 5):
// туннеля ещё нет, и трафику системы идти некуда, кроме как мимо него.
func newKillSwitchGuard(session lockdownSession, allowLeak bool, logf func(string, ...any)) (*killSwitchGuard, error) {
	if err := session.Lockdown(); err != nil {
		return nil, err
	}
	return &killSwitchGuard{session: session, allowLeak: allowLeak, logf: logf, locked: true}, nil
}

// Enable — туннель поднят: защита DNS вместо «закрыто». Отказ без
// -allow-leak возвращается — datapath откажет в Start, — а система
// остаётся закрытой. С -allow-leak туннель работает, значит, и система
// открыта: правила снимаются целиком, как без kill-switch.
func (g *killSwitchGuard) Enable(dns []netip.Addr) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	err := g.session.Enable(dns)
	if err == nil {
		g.locked = false
		return nil
	}
	if !g.allowLeak {
		g.lockdownLocked()
		return err
	}
	g.logf("[DNS] Внимание: %v — с -allow-leak туннель работает без защиты DNS", err)
	g.locked = false
	if derr := g.session.Disable(); derr != nil {
		g.logf("[DNS] %v", derr)
	}
	return nil
}

// Disable — туннеля больше нет: «закрыто» вместо защиты DNS. Отказ — не
// отказ datapath: громкая строка, правила снимаются целиком, и трафик
// системы, как без kill-switch, идёт мимо туннеля. Ошибка возвращается,
// только если не снялись и они.
func (g *killSwitchGuard) Disable() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lockdownLocked()
}

func (g *killSwitchGuard) lockdownLocked() error {
	err := g.session.Lockdown()
	if err == nil {
		g.locked = true
		return nil
	}
	g.locked = false
	g.logf("Внимание: kill-switch не встал: %v — пока туннель не поднят, трафик системы идёт мимо него", err)
	return g.session.Disable()
}

// Locked — стоит ли сейчас «закрыто»; для строк супервизора.
func (g *killSwitchGuard) Locked() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.locked
}

// BlockIPv6 передаётся защите, если она это умеет (datapath.IPv6Blocker).
func (g *killSwitchGuard) BlockIPv6(block bool) {
	if blocker, ok := g.session.(datapath.IPv6Blocker); ok {
		blocker.BlockIPv6(block)
	}
}
