package cli

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/captcha"
	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

// fakeLockdownSession — сессия с kill-switch: помнит, какой набор стоит.
type fakeLockdownSession struct {
	fakeGuardSession
	locked      bool
	lockdowns   int
	lockdownErr error
	disableErr  error
}

func (s *fakeLockdownSession) Enable(dns []netip.Addr) error {
	if err := s.fakeGuardSession.Enable(dns); err != nil {
		return err
	}
	s.locked = false
	return nil
}

func (s *fakeLockdownSession) Disable() error {
	if s.disableErr != nil {
		return s.disableErr
	}
	s.locked = false
	return s.fakeGuardSession.Disable()
}

func (s *fakeLockdownSession) Lockdown() error {
	s.lockdowns++
	if s.lockdownErr != nil {
		return s.lockdownErr
	}
	s.locked, s.enabled = true, false
	return nil
}

func setupKillSwitch(t *testing.T, session *fakeLockdownSession, allowLeak bool) (*dnsGuardSetup, *bytes.Buffer, *bytes.Buffer, error) {
	t.Helper()
	prev := openDNSGuard
	openDNSGuard = func() (dnsGuardSession, error) { return session, nil }
	t.Cleanup(func() { openDNSGuard = prev })
	var stdout, stderr bytes.Buffer
	setup, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, allowLeak, "")
	return setup, &stdout, &stderr, err
}

// «Закрыто» ставится сразу, до первой попытки; Enable — туннель поднят,
// Disable datapath'а — снова «закрыто», а не пустая таблица.
func TestKillSwitchLocksBeforeFirstAttempt(t *testing.T) {
	session := &fakeLockdownSession{}
	setup, stdout, _, err := setupKillSwitch(t, session, false)
	if err != nil {
		t.Fatal(err)
	}
	if !session.locked || setup.killSwitch == nil || !setup.killSwitch.Locked() {
		t.Fatalf("после открытия: закрыто=%v, признак=%v", session.locked, setup.killSwitch != nil)
	}
	if !strings.Contains(stdout.String(), "kill-switch: пока туннель не поднят, трафик системы заблокирован") {
		t.Errorf("нет строки о kill-switch: %q", stdout.String())
	}

	if err := setup.guard.Enable(nil); err != nil {
		t.Fatal(err)
	}
	if session.locked || !session.enabled || setup.killSwitch.Locked() {
		t.Errorf("после Enable: закрыто=%v, защита DNS=%v", session.locked, session.enabled)
	}
	if err := setup.guard.Disable(); err != nil {
		t.Fatal(err)
	}
	if !session.locked || session.enabled || !setup.killSwitch.Locked() {
		t.Errorf("после Disable: закрыто=%v, защита DNS=%v", session.locked, session.enabled)
	}
	if _, ok := setup.guard.(datapath.IPv6Blocker); !ok {
		t.Errorf("%T не IPv6Blocker", setup.guard)
	}
	setup.close()
	if !session.closed {
		t.Error("сессия не закрыта")
	}
}

// Не встал до подключения: без -allow-leak — ошибка с подсказками, сессия
// закрыта; с ним — предупреждение и работа без kill-switch.
func TestKillSwitchSetupFailure(t *testing.T) {
	session := &fakeLockdownSession{lockdownErr: errors.New("правило не встало")}
	setup, _, _, err := setupKillSwitch(t, session, false)
	if err == nil || setup != nil {
		t.Fatal("подключение без kill-switch разрешено без -allow-leak")
	}
	for _, want := range []string{"kill-switch не включился: правило не встало", "-no-kill-switch", "-allow-leak"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}
	if !session.closed {
		t.Error("сессия не закрыта при отказе")
	}

	session = &fakeLockdownSession{lockdownErr: errors.New("правило не встало")}
	setup, _, stderr, err := setupKillSwitch(t, session, true)
	if err != nil {
		t.Fatalf("с -allow-leak отказ: %v", err)
	}
	defer setup.close()
	if setup.killSwitch != nil {
		t.Error("признак kill-switch есть, а он не встал")
	}
	if _, ok := setup.guard.(tolerantDNSGuard); !ok {
		t.Errorf("защита %T, ждали терпимую", setup.guard)
	}
	if !strings.Contains(stderr.String(), "Предупреждение: kill-switch не включился") {
		t.Errorf("нет предупреждения: %q", stderr.String())
	}
}

// Отказ защиты DNS при подъёме: без -allow-leak — отказ Start, система
// остаётся закрытой; с ним туннель работает, и система открыта целиком.
func TestKillSwitchEnableFailure(t *testing.T) {
	session := &fakeLockdownSession{}
	setup, _, _, err := setupKillSwitch(t, session, false)
	if err != nil {
		t.Fatal(err)
	}
	session.enableErr = errors.New("правило не встало")
	if err := setup.guard.Enable(nil); err == nil {
		t.Error("отказ Enable проглочен без -allow-leak")
	}
	if !session.locked || !setup.killSwitch.Locked() {
		t.Error("после отказа Enable система не закрыта")
	}

	session = &fakeLockdownSession{}
	setup, stdout, _, err := setupKillSwitch(t, session, true)
	if err != nil {
		t.Fatal(err)
	}
	session.enableErr = errors.New("правило не встало")
	if err := setup.guard.Enable(nil); err != nil {
		t.Errorf("с -allow-leak Enable = %v", err)
	}
	if session.locked || setup.killSwitch.Locked() {
		t.Error("с -allow-leak туннель поднят, а система закрыта")
	}
	if !strings.Contains(stdout.String(), "[DNS] Внимание: правило не встало") {
		t.Errorf("нет строки об отказе: %q", stdout.String())
	}
}

// «Закрыто» не встало посреди сеанса — громкая строка, правила сняты
// целиком, datapath отказа не видит.
func TestKillSwitchDisableFailure(t *testing.T) {
	session := &fakeLockdownSession{}
	setup, stdout, _, err := setupKillSwitch(t, session, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.guard.Enable(nil); err != nil {
		t.Fatal(err)
	}
	session.lockdownErr = errors.New("правило не встало")
	if err := setup.guard.Disable(); err != nil {
		t.Errorf("Disable = %v, ждали nil", err)
	}
	if session.locked || session.enabled || setup.killSwitch.Locked() {
		t.Errorf("после отказа: закрыто=%v, защита DNS=%v; ждали пустую таблицу", session.locked, session.enabled)
	}
	if !strings.Contains(stdout.String(), "Внимание: kill-switch не встал: правило не встало") {
		t.Errorf("нет громкой строки: %q", stdout.String())
	}
}

// Защита без Lockdown (Windows до W1) — предупреждение, работа как без
// kill-switch.
func TestKillSwitchUnsupportedSession(t *testing.T) {
	session := &fakeGuardSession{}
	stubOpenDNSGuard(t, session, nil)
	var stdout, stderr bytes.Buffer
	setup, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, false, "")
	if err != nil {
		t.Fatal(err)
	}
	defer setup.close()
	if setup.killSwitch != nil || setup.guard != session {
		t.Errorf("защита %T, признак %v", setup.guard, setup.killSwitch != nil)
	}
	if !strings.Contains(stderr.String(), "kill-switch на этой системе ещё не сделан") {
		t.Errorf("нет предупреждения: %q", stderr.String())
	}
}

// stepSolver — решатель капчи для тестов окна: во время Solve зовёт during
// (подъём или падение туннеля посреди капчи) и запоминает, стояло ли
// «закрыто», пока человек решал.
type stepSolver struct {
	session      *fakeLockdownSession
	during       func()
	token        string
	err          error
	lockedInside bool
}

func (s *stepSolver) Solve(ctx context.Context, _ captcha.Request) (string, error) {
	s.lockedInside = s.session.locked
	if s.during != nil {
		s.during()
	}
	if s.err != nil {
		return "", s.err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return s.token, nil
}

func (*stepSolver) NeedsHuman() bool { return true }

// Ручная капча при «закрыто»: на время Solve правил нет, после — снова
// «закрыто», на любом исходе.
func TestKillSwitchCaptchaWindow(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name  string
		ctx   context.Context
		token string
		err   error
	}{
		{name: "ответ", ctx: context.Background(), token: "токен"},
		{name: "отказ", ctx: context.Background(), err: captcha.ErrCancelled},
		{name: "срок или Ctrl+C", ctx: cancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := &fakeLockdownSession{}
			setup, stdout, _, err := setupKillSwitch(t, session, false)
			if err != nil {
				t.Fatal(err)
			}
			inner := &stepSolver{session: session, token: tc.token, err: tc.err}
			solver := killSwitchCaptcha{Solver: inner, guard: setup.killSwitch}

			token, err := solver.Solve(tc.ctx, captcha.Request{})
			if token != tc.token || (err == nil) != (tc.token != "") {
				t.Errorf("Solve = %q, %v", token, err)
			}
			if inner.lockedInside {
				t.Error("во время капчи стояло «закрыто»")
			}
			if !session.locked || !setup.killSwitch.Locked() {
				t.Error("после капчи «закрыто» не вернулось")
			}
			for _, want := range []string{"kill-switch снят на время капчи — трафик системы идёт мимо туннеля",
				"kill-switch возвращён"} {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("нет строки %q: %q", want, stdout.String())
				}
			}
		})
	}
}

// Туннель поднят — браузер идёт через него: окно не открывается, строк нет,
// защита DNS остаётся.
func TestKillSwitchCaptchaTunnelUp(t *testing.T) {
	session := &fakeLockdownSession{}
	setup, stdout, _, err := setupKillSwitch(t, session, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := setup.guard.Enable(nil); err != nil {
		t.Fatal(err)
	}
	lockdowns := session.lockdowns
	inner := &stepSolver{session: session, token: "токен"}
	if _, err := (killSwitchCaptcha{Solver: inner, guard: setup.killSwitch}).Solve(context.Background(), captcha.Request{}); err != nil {
		t.Fatal(err)
	}
	if !session.enabled || session.locked || session.lockdowns != lockdowns {
		t.Errorf("защита DNS=%v, закрыто=%v, Lockdown звали %d раз", session.enabled, session.locked,
			session.lockdowns-lockdowns)
	}
	if strings.Contains(stdout.String(), "на время капчи") || strings.Contains(stdout.String(), "возвращён") {
		t.Errorf("строки окна при поднятом туннеле: %q", stdout.String())
	}
}

// Туннель поднялся посреди капчи — после неё защита DNS, не «закрыто»;
// упал посреди капчи — браузер не отрезается, «закрыто» — после неё.
func TestKillSwitchCaptchaTunnelChanges(t *testing.T) {
	session := &fakeLockdownSession{}
	setup, stdout, _, err := setupKillSwitch(t, session, false)
	if err != nil {
		t.Fatal(err)
	}
	up := &stepSolver{session: session, token: "токен", during: func() {
		if err := setup.guard.Enable(nil); err != nil {
			t.Error(err)
		}
	}}
	if _, err := (killSwitchCaptcha{Solver: up, guard: setup.killSwitch}).Solve(context.Background(), captcha.Request{}); err != nil {
		t.Fatal(err)
	}
	if !session.enabled || session.locked || setup.killSwitch.Locked() {
		t.Errorf("туннель поднялся во время капчи: защита DNS=%v, закрыто=%v", session.enabled, session.locked)
	}
	if strings.Contains(stdout.String(), "возвращён") {
		t.Errorf("«закрыто» вернулось при поднятом туннеле: %q", stdout.String())
	}

	var lockedAfterDown bool
	down := &stepSolver{session: session, token: "токен", during: func() {
		if err := setup.guard.Disable(); err != nil {
			t.Error(err)
		}
		lockedAfterDown = session.locked
	}}
	if _, err := (killSwitchCaptcha{Solver: down, guard: setup.killSwitch}).Solve(context.Background(), captcha.Request{}); err != nil {
		t.Fatal(err)
	}
	if lockedAfterDown || setup.killSwitch.Locked() != session.locked {
		t.Error("туннель упал во время капчи — браузер отрезан")
	}
	if !session.locked || session.enabled {
		t.Errorf("после капчи: закрыто=%v, защита DNS=%v", session.locked, session.enabled)
	}

	// Отказ Enable посреди капчи (без -allow-leak) тоже не отрезает браузер.
	session.enableErr = errors.New("правило не встало")
	failed := &stepSolver{session: session, token: "токен", during: func() {
		if err := setup.guard.Enable(nil); err == nil {
			t.Error("отказ Enable проглочен")
		}
		lockedAfterDown = session.locked
	}}
	if _, err := (killSwitchCaptcha{Solver: failed, guard: setup.killSwitch}).Solve(context.Background(), captcha.Request{}); err != nil {
		t.Fatal(err)
	}
	if lockedAfterDown || !session.locked {
		t.Errorf("отказ Enable во время капчи: закрыто внутри=%v, после=%v", lockedAfterDown, session.locked)
	}
}

// Правила не снялись — громкая строка, «закрыто» остаётся; не вернулись —
// громкая строка K3, правила сняты целиком.
func TestKillSwitchCaptchaFailures(t *testing.T) {
	session := &fakeLockdownSession{}
	setup, stdout, _, err := setupKillSwitch(t, session, false)
	if err != nil {
		t.Fatal(err)
	}
	session.disableErr = errors.New("таблица занята")
	inner := &stepSolver{session: session, token: "токен"}
	if _, err := (killSwitchCaptcha{Solver: inner, guard: setup.killSwitch}).Solve(context.Background(), captcha.Request{}); err != nil {
		t.Fatal(err)
	}
	if !inner.lockedInside || !session.locked || !setup.killSwitch.Locked() {
		t.Errorf("отказ снятия: закрыто внутри=%v, после=%v", inner.lockedInside, session.locked)
	}
	if !strings.Contains(stdout.String(), "Внимание: kill-switch не снялся на время капчи: таблица занята") {
		t.Errorf("нет строки об отказе снятия: %q", stdout.String())
	}

	session.disableErr = nil
	inner = &stepSolver{session: session, token: "токен", during: func() {
		session.lockdownErr = errors.New("правило не встало")
	}}
	if _, err := (killSwitchCaptcha{Solver: inner, guard: setup.killSwitch}).Solve(context.Background(), captcha.Request{}); err != nil {
		t.Fatal(err)
	}
	if session.locked || setup.killSwitch.Locked() {
		t.Error("«закрыто» не встало, а признак стоит")
	}
	if !strings.Contains(stdout.String(), "Внимание: kill-switch не встал: правило не встало") ||
		strings.Contains(stdout.String(), "возвращён") {
		t.Errorf("строки после отказа возврата: %q", stdout.String())
	}
}
