package cli

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// fakeGuardSession — сессия защиты DNS без WFP.
type fakeGuardSession struct {
	enableErr error
	enabled   bool
	closed    bool
}

func (s *fakeGuardSession) Enable([]netip.Addr) error {
	if s.enableErr != nil {
		return s.enableErr
	}
	s.enabled = true
	return nil
}
func (s *fakeGuardSession) Disable() error { s.enabled = false; return nil }
func (s *fakeGuardSession) Close() error   { s.closed = true; return nil }

func stubOpenDNSGuard(t *testing.T, session *fakeGuardSession, err error) {
	t.Helper()
	prev := openDNSGuard
	openDNSGuard = func() (dnsGuardSession, error) {
		if err != nil {
			return nil, err
		}
		return session, nil
	}
	t.Cleanup(func() { openDNSGuard = prev })
}

// Без -allow-dns-leak отказ защиты — ошибка, и она называет флаг.
func TestSetupDNSGuardRefusesByDefault(t *testing.T) {
	stubOpenDNSGuard(t, nil, errors.New("сессия WFP: служба BFE остановлена"))
	var stdout, stderr bytes.Buffer

	guard, closeGuard, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, false)
	if err == nil {
		t.Fatal("подключение без защиты DNS разрешено без -allow-dns-leak")
	}
	for _, want := range []string{"служба BFE остановлена", "-allow-dns-leak"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}
	if guard != nil || closeGuard != nil {
		t.Error("при отказе вернулась защита или функция закрытия")
	}
}

// С -allow-dns-leak отказ — предупреждение, и datapath идёт без защиты.
func TestSetupDNSGuardAllowLeak(t *testing.T) {
	stubOpenDNSGuard(t, nil, errors.New("сессия WFP: служба BFE остановлена"))
	var stdout, stderr bytes.Buffer

	guard, closeGuard, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, true)
	if err != nil {
		t.Fatalf("с -allow-dns-leak отказ: %v", err)
	}
	if guard != nil {
		t.Errorf("защита %T, а сессии нет", guard)
	}
	closeGuard()
	if !strings.Contains(stderr.String(), "Предупреждение: защита от утечки DNS не включилась") {
		t.Errorf("нет предупреждения в stderr: %q", stderr.String())
	}
}

// Сессия открылась: datapath получает её, закрытие закрывает сессию.
func TestSetupDNSGuardOpens(t *testing.T) {
	session := &fakeGuardSession{}
	stubOpenDNSGuard(t, session, nil)
	var stdout, stderr bytes.Buffer

	guard, closeGuard, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Enable(nil); err != nil || !session.enabled {
		t.Errorf("Enable: err=%v, включена=%v", err, session.enabled)
	}
	closeGuard()
	if !session.closed {
		t.Error("сессия не закрыта")
	}
	if !strings.Contains(stdout.String(), "защита DNS") {
		t.Errorf("нет строки о защите DNS: %q", stdout.String())
	}

	// Без флага отказ правил доходит до datapath — тот отказывает в Start.
	session.enableErr = errors.New("правило не встало")
	if err := guard.Enable(nil); err == nil {
		t.Error("отказ Enable проглочен без -allow-dns-leak")
	}
}

// С -allow-dns-leak отказ правил посреди сеанса — строка, а не сбой попытки.
func TestSetupDNSGuardAllowLeakTolerantEnable(t *testing.T) {
	session := &fakeGuardSession{enableErr: errors.New("правило не встало")}
	stubOpenDNSGuard(t, session, nil)
	var stdout, stderr bytes.Buffer

	guard, closeGuard, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeGuard()
	if err := guard.Enable(nil); err != nil {
		t.Errorf("с -allow-dns-leak Enable = %v, ждали nil", err)
	}
	if !strings.Contains(stdout.String(), "[DNS] Внимание: правило не встало") {
		t.Errorf("нет предупреждения об отказе правил: %q", stdout.String())
	}
}

func TestPlanConnectAllowDNSLeak(t *testing.T) {
	c := newConnectEnv(t)
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"home", "-datapath", "wintun"}, false},
		{[]string{"home", "-datapath", "wintun", "-allow-dns-leak"}, true},
	} {
		plan, err := c.plan(tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if plan.allowDNSLeak != tc.want {
			t.Errorf("%v: allowDNSLeak = %v", tc.args, plan.allowDNSLeak)
		}
		if !plan.datapath(nil).Requires().DNSGuard {
			t.Errorf("%v: wintun не просит защиту DNS", tc.args)
		}
	}
}
