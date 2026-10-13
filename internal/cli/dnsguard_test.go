package cli

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
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

// Без -allow-leak отказ защиты — ошибка, и она называет флаг.
func TestSetupDNSGuardRefusesByDefault(t *testing.T) {
	stubOpenDNSGuard(t, nil, errors.New("сессия WFP: служба BFE остановлена"))
	var stdout, stderr bytes.Buffer

	setup, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, false, "")
	if err == nil {
		t.Fatal("подключение без защиты DNS разрешено без -allow-leak")
	}
	for _, want := range []string{"защита от утечки DNS и kill-switch не включились", "служба BFE остановлена",
		"без них", "-allow-leak"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}
	if setup != nil {
		t.Error("при отказе вернулась защита")
	}
}

// С -allow-leak отказ — предупреждение, и datapath идёт без защиты.
func TestSetupDNSGuardAllowLeak(t *testing.T) {
	stubOpenDNSGuard(t, nil, errors.New("сессия WFP: служба BFE остановлена"))
	var stdout, stderr bytes.Buffer

	setup, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, true, "-no-kill-switch")
	if err != nil {
		t.Fatalf("с -allow-leak отказ: %v", err)
	}
	if setup.guard != nil || setup.locked != nil {
		t.Errorf("защита %T, а сессии нет", setup.guard)
	}
	setup.close()
	if !strings.Contains(stderr.String(), "Предупреждение: защита от утечки DNS не включилась: сессия WFP") ||
		!strings.Contains(stderr.String(), "подключаюсь без неё") {
		t.Errorf("нет предупреждения в stderr: %q", stderr.String())
	}
}

// Сессия открылась: datapath получает её, закрытие закрывает сессию.
func TestSetupDNSGuardOpens(t *testing.T) {
	session := &fakeGuardSession{}
	stubOpenDNSGuard(t, session, nil)
	var stdout, stderr bytes.Buffer

	setup, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, false, "-no-kill-switch")
	if err != nil {
		t.Fatal(err)
	}
	guard := setup.guard
	if err := guard.Enable(nil); err != nil || !session.enabled {
		t.Errorf("Enable: err=%v, включена=%v", err, session.enabled)
	}
	setup.close()
	if !session.closed {
		t.Error("сессия не закрыта")
	}
	if !strings.Contains(stdout.String(), "защита DNS") {
		t.Errorf("нет строки о защите DNS: %q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "kill-switch выключен (-no-kill-switch)") || setup.locked != nil {
		t.Errorf("нет строки о выключенном kill-switch: %q", stdout.String())
	}

	// Без флага отказ правил доходит до datapath — тот отказывает в Start.
	session.enableErr = errors.New("правило не встало")
	if err := guard.Enable(nil); err == nil {
		t.Error("отказ Enable проглочен без -allow-leak")
	}
}

// С -allow-leak отказ правил посреди сеанса — строка, а не сбой попытки.
func TestSetupDNSGuardAllowLeakTolerantEnable(t *testing.T) {
	session := &fakeGuardSession{enableErr: errors.New("правило не встало")}
	stubOpenDNSGuard(t, session, nil)
	var stdout, stderr bytes.Buffer

	setup, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, true, "-no-kill-switch")
	if err != nil {
		t.Fatal(err)
	}
	defer setup.close()
	guard := setup.guard
	if err := guard.Enable(nil); err != nil {
		t.Errorf("с -allow-leak Enable = %v, ждали nil", err)
	}
	if !strings.Contains(stdout.String(), "[DNS] Внимание: правило не встало") {
		t.Errorf("нет предупреждения об отказе правил: %q", stdout.String())
	}
}

// blockingGuardSession — сессия, которая умеет закрывать IPv6.
type blockingGuardSession struct {
	fakeGuardSession
	blockIPv6 bool
}

func (s *blockingGuardSession) BlockIPv6(block bool) { s.blockIPv6 = block }

// С -allow-leak обёртка не прячет от datapath умение закрыть IPv6.
func TestSetupDNSGuardAllowLeakBlocksIPv6(t *testing.T) {
	session := &blockingGuardSession{}
	prev := openDNSGuard
	openDNSGuard = func() (dnsGuardSession, error) { return session, nil }
	t.Cleanup(func() { openDNSGuard = prev })
	var stdout, stderr bytes.Buffer

	setup, err := setupDNSGuard(newPrinter(&stdout, false), &stderr, true, "-no-kill-switch")
	if err != nil {
		t.Fatal(err)
	}
	defer setup.close()
	guard := setup.guard
	blocker, ok := guard.(datapath.IPv6Blocker)
	if !ok {
		t.Fatalf("%T не IPv6Blocker", guard)
	}
	blocker.BlockIPv6(true)
	if !session.blockIPv6 {
		t.Error("BlockIPv6 не дошёл до сессии")
	}
}

// -allow-leak и его прежнее имя -allow-dns-leak — один флаг.
func TestPlanConnectAllowLeak(t *testing.T) {
	c := newConnectEnv(t)
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"home", "-datapath", "tun"}, false},
		{[]string{"home", "-datapath", "tun", "-allow-leak"}, true},
		{[]string{"home", "-datapath", "tun", "-allow-dns-leak"}, true},
	} {
		plan, err := c.plan(tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if plan.allowLeak != tc.want {
			t.Errorf("%v: allowLeak = %v", tc.args, plan.allowLeak)
		}
		if !plan.datapath(nil).Requires().DNSGuard {
			t.Errorf("%v: tun не просит защиту DNS", tc.args)
		}
	}
}

// kill-switch включён, если профиль не выключил; -no-kill-switch выключает
// на запуск, профиль при этом не меняется.
func TestPlanConnectKillSwitch(t *testing.T) {
	c := newConnectEnv(t)
	for _, tc := range []struct {
		profile *bool
		args    []string
		want    bool
		off     string
	}{
		{nil, []string{"home", "-datapath", "tun"}, true, ""},
		{nil, []string{"home", "-datapath", "tun", "-no-kill-switch"}, false, "-no-kill-switch"},
		{nil, []string{"home", "-datapath", "tun", "-no-kill-switch=false"}, true, ""},
		{boolPtr(false), []string{"home", "-datapath", "tun"}, false, "kill_switch = false в профиле"},
		{boolPtr(false), []string{"home", "-datapath", "tun", "-no-kill-switch=false"}, false,
			"kill_switch = false в профиле"},
		{boolPtr(true), []string{"home", "-datapath", "tun", "-no-kill-switch"}, false, "-no-kill-switch"},
	} {
		c.file.Profiles["home"].KillSwitch = tc.profile
		c.save()
		plan, err := c.plan(tc.args...)
		if err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		if plan.killSwitch != tc.want || plan.killSwitchOff != tc.off {
			t.Errorf("профиль %v, %v: killSwitch = %v, killSwitchOff = %q", tc.profile, tc.args,
				plan.killSwitch, plan.killSwitchOff)
		}
		if got := c.file.Profiles["home"].KillSwitch; got != tc.profile {
			t.Errorf("%v: план изменил профиль", tc.args)
		}
	}
}

func boolPtr(v bool) *bool { return &v }
