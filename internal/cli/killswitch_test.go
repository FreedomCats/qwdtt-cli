package cli

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

// fakeLockdownSession — сессия с kill-switch: помнит, какой набор стоит.
type fakeLockdownSession struct {
	fakeGuardSession
	locked      bool
	lockdowns   int
	lockdownErr error
}

func (s *fakeLockdownSession) Enable(dns []netip.Addr) error {
	if err := s.fakeGuardSession.Enable(dns); err != nil {
		return err
	}
	s.locked = false
	return nil
}

func (s *fakeLockdownSession) Disable() error {
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
	if !session.locked || setup.locked == nil || !setup.locked() {
		t.Fatalf("после открытия: закрыто=%v, признак=%v", session.locked, setup.locked != nil)
	}
	if !strings.Contains(stdout.String(), "kill-switch: пока туннель не поднят, трафик системы заблокирован") {
		t.Errorf("нет строки о kill-switch: %q", stdout.String())
	}

	if err := setup.guard.Enable(nil); err != nil {
		t.Fatal(err)
	}
	if session.locked || !session.enabled || setup.locked() {
		t.Errorf("после Enable: закрыто=%v, защита DNS=%v", session.locked, session.enabled)
	}
	if err := setup.guard.Disable(); err != nil {
		t.Fatal(err)
	}
	if !session.locked || session.enabled || !setup.locked() {
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
	if setup.locked != nil {
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
	if !session.locked || !setup.locked() {
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
	if session.locked || setup.locked() {
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
	if session.locked || session.enabled || setup.locked() {
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
	if setup.locked != nil || setup.guard != session {
		t.Errorf("защита %T, признак %v", setup.guard, setup.locked != nil)
	}
	if !strings.Contains(stderr.String(), "kill-switch на этой системе ещё не сделан") {
		t.Errorf("нет предупреждения: %q", stderr.String())
	}
}
