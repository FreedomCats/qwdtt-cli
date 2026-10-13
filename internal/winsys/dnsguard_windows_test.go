package winsys

import (
	"net/netip"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tailscale/wf"
)

// Правила — без WFP: на каждом из четырёх слоёв запрет порта 53 по UDP
// и TCP легче обоих разрешений, своему процессу и резолверам туннеля
// своего семейства с loopback.
func TestDNSGuardRules(t *testing.T) {
	dns := []netip.Addr{
		netip.MustParseAddr("10.8.0.1"),
		netip.MustParseAddr("::ffff:1.1.1.1"), // IPv4 в IPv6-записи — к IPv4
		netip.MustParseAddr("fd00::1"),
	}
	rules, err := dnsGuardRules(dns, "app-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 3*len(dnsGuardLayers) {
		t.Fatalf("правил %d, ждали %d", len(rules), 3*len(dnsGuardLayers))
	}

	ids := map[wf.RuleID]bool{}
	for i, layer := range dnsGuardLayers {
		block, self, allowed := rules[3*i], rules[3*i+1], rules[3*i+2]
		for _, r := range []*wf.Rule{block, self, allowed} {
			if r.Layer != layer.id || r.Sublayer != dnsGuardSublayer || r.Provider != dnsGuardProvider {
				t.Errorf("%s: правило %q не на своём слое, подслое или провайдере", layer.name, r.Name)
			}
			if ids[r.ID] || r.ID.IsZero() {
				t.Errorf("%s: GUID правила %q пустой или повторён", layer.name, r.Name)
			}
			ids[r.ID] = true
		}

		if block.Action != wf.ActionBlock || block.Weight >= self.Weight || block.Weight >= allowed.Weight {
			t.Errorf("%s: запрет %v с весом %d, разрешения %d и %d — запрет должен быть легче",
				layer.name, block.Action, block.Weight, self.Weight, allowed.Weight)
		}
		wantBlock := []wf.Match{
			{Field: wf.FieldIPRemotePort, Op: wf.MatchTypeEqual, Value: uint16(53)},
			{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoUDP},
			{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoTCP},
		}
		checkMatches(t, layer.name+": запрет", block.Conditions, wantBlock)

		if self.Action != wf.ActionPermit {
			t.Errorf("%s: правило своего процесса — %v", layer.name, self.Action)
		}
		checkMatches(t, layer.name+": свой процесс", self.Conditions, []wf.Match{
			{Field: wf.FieldALEAppID, Op: wf.MatchTypeEqual, Value: "app-id"},
		})

		if allowed.Action != wf.ActionPermit {
			t.Errorf("%s: правило резолверов — %v", layer.name, allowed.Action)
		}
		var want []wf.Match
		if layer.ipv6 {
			want = []wf.Match{
				{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: netip.IPv6Loopback()},
				{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: netip.MustParseAddr("fd00::1")},
			}
		} else {
			want = []wf.Match{
				{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: netip.MustParsePrefix("127.0.0.0/8")},
				{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: netip.MustParseAddr("10.8.0.1")},
				{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: netip.MustParseAddr("1.1.1.1")},
			}
		}
		checkMatches(t, layer.name+": резолверы", allowed.Conditions, want)
	}
}

func checkMatches(t *testing.T, what string, got []*wf.Match, want []wf.Match) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: условий %d, ждали %d: %v", what, len(got), len(want), got)
		return
	}
	for i := range want {
		if *got[i] != want[i] {
			t.Errorf("%s: условие %d = %v, ждали %v", what, i, got[i], want[i])
		}
	}
}

// Без прав администратора сессия не открывается — и это ошибка, а не
// паника. С правами — полный круг на настоящем WFP: Enable, повторный
// Enable, Disable, Close.
func TestDNSGuardSession(t *testing.T) {
	g, err := OpenDNSGuard()
	if err != nil {
		t.Skipf("сессия WFP не открылась (нужны права администратора): %v", err)
	}
	dns := []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	if err := g.Enable(dns); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := g.Enable(dns); err != nil {
		t.Fatalf("повторный Enable: %v", err)
	}
	if n := len(g.rules); n != 3*len(dnsGuardLayers) {
		t.Errorf("после двух Enable правил %d, ждали %d", n, 3*len(dnsGuardLayers))
	}
	if err := g.Disable(); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if err := g.Disable(); err != nil {
		t.Fatalf("повторный Disable: %v", err)
	}
	if err := g.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := g.Enable(dns); err == nil {
		t.Error("Enable после Close принят")
	}
}

// Правила ставятся, пока идёт сборка мусора. У tailscale/wf арена отдавала
// невыровненную память, и запись структуры с указателями во время разметки
// роняла процесс: fatal error «bulkBarrierPreWrite: unaligned arguments»,
// recover её не ловит (найдено живым прогоном S1, на первом же Enable).
//
// Выровнено ли — решает длина идентификатора приложения в правиле «свой
// процесс», то есть длина пути к exe. У tt.exe путь постоянный, а у
// тестового бинаря каталог go-build каждый раз свой, поэтому длины
// перебираются: дополнение в 0–7 байт даёт и невыровненный случай.
// Без исправления в форке тест падает за доли секунды.
func TestDNSGuardUnderGC(t *testing.T) {
	g, err := OpenDNSGuard()
	if err != nil {
		t.Skipf("сессия WFP не открылась (нужны права администратора): %v", err)
	}
	defer g.Close()

	var stop atomic.Bool
	done := make(chan struct{})
	go func() {
		defer close(done)
		for !stop.Load() {
			runtime.GC()
		}
	}()
	defer func() {
		stop.Store(true)
		<-done
	}()

	appID := g.appID
	dns := []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("1.0.0.1")}
	for i := range 50 {
		for pad := range 8 {
			g.appID = appID + strings.Repeat("x", pad)
			if err := g.Enable(dns); err != nil {
				t.Fatalf("Enable %d, дополнение %d: %v", i, pad, err)
			}
		}
	}
	if err := g.Disable(); err != nil {
		t.Fatal(err)
	}
}
