// killswitch.go — kill-switch для datapath (plan-e3.md, K3): пока туннель
// не поднят, трафик системы заблокирован.
//
// Отдельного объекта у kill-switch нет: это третий набор правил в той же
// сессии WFP (на Linux — таблице nftables), что и защита DNS. Datapath о нём
// не знает: обёртка killSwitchGuard подменяет Disable защиты на «закрыто»,
// а Enable оставляет как есть — поднятый туннель держит одна защита DNS.
//
// Исключение — ручная капча (K4, решение 1): её страница открывается в
// браузере пользователя, а он мимо туннеля при «закрыто» не пройдёт. На время
// капчи — от ссылки до ответа — правила снимаются (killSwitchCaptcha).
package cli

import (
	"context"
	"net/netip"
	"sync"

	"github.com/FreedomCats/qwdtt-cli/internal/captcha"
	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

// lockdownSession — защита, которая умеет kill-switch (Lockdown):
// linuxsys.DNSGuard и winsys.DNSGuard.
type lockdownSession interface {
	datapath.DNSGuard
	// Lockdown ставит набор «закрыто» вместо прежних правил, одним
	// переходом.
	Lockdown() error
}

// killSwitchGuard — защита DNS для datapath при включённом kill-switch.
//
// Какой набор стоит, следует из двух признаков: туннель поднят — защита DNS;
// не поднят и идёт ручная капча — правил нет; иначе — «закрыто». Капча и
// туннель друг от друга не зависят: туннель может подняться посреди капчи
// (её просит одна группа, а поднимает другая) и упасть посреди неё — браузер
// пользователя при этом отрезан быть не должен.
type killSwitchGuard struct {
	session   lockdownSession
	allowLeak bool
	logf      func(string, ...any)

	mu      sync.Mutex
	locked  bool // стоит «закрыто»
	up      bool // туннель поднят
	captcha bool // идёт ручная капча
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
		g.locked, g.up = false, true
		return nil
	}
	if !g.allowLeak {
		g.downLocked()
		return err
	}
	g.logf("[DNS] Внимание: %v — с -allow-leak туннель работает без защиты DNS", err)
	g.locked, g.up = false, true
	if derr := g.session.Disable(); derr != nil {
		g.logf("[DNS] %v", derr)
	}
	return nil
}

// Disable — туннеля больше нет: «закрыто» вместо защиты DNS (во время
// капчи — без правил). Отказ «закрыто» — не отказ datapath: громкая строка,
// правила снимаются целиком, и трафик системы, как без kill-switch, идёт
// мимо туннеля. Ошибка возвращается, только если не снялись и они.
func (g *killSwitchGuard) Disable() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.downLocked()
}

// downLocked — набор для упавшего туннеля.
func (g *killSwitchGuard) downLocked() error {
	g.up = false
	if g.captcha {
		g.locked = false
		return g.session.Disable()
	}
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

// openForCaptcha — началась ручная капча: при «закрыто» правила снимаются.
// При поднятом туннеле снимать нечего — браузер идёт через туннель.
func (g *killSwitchGuard) openForCaptcha() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.captcha = true
	if !g.locked {
		return
	}
	if err := g.session.Disable(); err != nil {
		// На Linux пакет правил атомарен: «закрыто» осталось как было. На
		// Windows правила снимаются по одному, и часть могла уйти — но
		// WFP отказывает в снятии своих правил разве что при сбое BFE.
		g.logf("Внимание: kill-switch не снялся на время капчи: %v — страница капчи в браузере может не открыться", err)
		return
	}
	g.locked = false
	g.logf("kill-switch снят на время капчи — трафик системы идёт мимо туннеля")
}

// closeAfterCaptcha — капча кончилась (ответ, отказ или срок): если туннель
// так и не поднят, снова «закрыто».
func (g *killSwitchGuard) closeAfterCaptcha() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.captcha = false
	if g.up || g.locked {
		return
	}
	if err := g.lockdownLocked(); err != nil {
		g.logf("[DNS] %v", err)
	}
	if g.locked {
		g.logf("kill-switch возвращён: пока туннель не поднят, трафик системы заблокирован")
	}
}

// Locked — стоит ли сейчас «закрыто»; для строк супервизора.
func (g *killSwitchGuard) Locked() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.locked
}

// lockedFunc — Locked для опций супервизора; nil, если kill-switch нет.
func (g *killSwitchGuard) lockedFunc() func() bool {
	if g == nil {
		return nil
	}
	return g.Locked
}

// BlockIPv6 передаётся защите, если она это умеет (datapath.IPv6Blocker).
func (g *killSwitchGuard) BlockIPv6(block bool) {
	if blocker, ok := g.session.(datapath.IPv6Blocker); ok {
		blocker.BlockIPv6(block)
	}
}

// killSwitchCaptcha — решатель капчи при kill-switch: на время Solve
// правила сняты (plan-e3.md, решение 1). Решатель о сети не знает, поэтому
// обёртка — здесь. Окно WebView2 пока оборачивается так же: его сеть
// делает msedgewebview2.exe, и при «закрыто» страница не загрузится. С W2
// оно пойдёт через прокси tt, и обёртка ему станет не нужна.
type killSwitchCaptcha struct {
	captcha.Solver
	guard *killSwitchGuard
}

func (s killSwitchCaptcha) Solve(ctx context.Context, req captcha.Request) (string, error) {
	s.guard.openForCaptcha()
	defer s.guard.closeAfterCaptcha()
	return s.Solver.Solve(ctx, req)
}
