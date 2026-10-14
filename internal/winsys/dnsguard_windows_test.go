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
// процесс», то есть длина пути к exe. У qwdtt-cli.exe путь постоянный, а у
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

// Набор «закрыто» — без WFP: на каждом слое сверху вниз по весу свой
// процесс и loopback, запрет DNS, служебные протоколы и локальная сеть
// своего семейства, запрет всего.
func TestLockdownRules(t *testing.T) {
	rules, err := lockdownRules("app-id")
	if err != nil {
		t.Fatal(err)
	}
	ids := map[wf.RuleID]bool{}
	byLayer := map[wf.LayerID][]*wf.Rule{}
	for _, r := range rules {
		if r.Sublayer != dnsGuardSublayer || r.Provider != dnsGuardProvider {
			t.Errorf("правило %q не в своём подслое или провайдере", r.Name)
		}
		if ids[r.ID] || r.ID.IsZero() {
			t.Errorf("GUID правила %q пустой или повторён", r.Name)
		}
		ids[r.ID] = true
		byLayer[r.Layer] = append(byLayer[r.Layer], r)
	}

	for _, layer := range dnsGuardLayers {
		got := byLayer[layer.id]
		want := 6
		if layer.ipv6 {
			want = 7 // и ICMPv6 ND
		}
		if len(got) != want {
			t.Fatalf("%s: правил %d, ждали %d", layer.name, len(got), want)
		}
		for i := 1; i < len(got); i++ {
			if got[i].Weight > got[i-1].Weight {
				t.Errorf("%s: %q тяжелее %q, стоящего выше", layer.name, got[i].Name, got[i-1].Name)
			}
		}
		self, loopback, dns, dhcp, local, block := got[0], got[1], got[2], got[3], got[len(got)-2], got[len(got)-1]

		for _, r := range []*wf.Rule{self, loopback, dhcp, local} {
			if r.Action != wf.ActionPermit {
				t.Errorf("%s: %q — %v, ждали разрешение", layer.name, r.Name, r.Action)
			}
		}
		for _, r := range []*wf.Rule{dns, block} {
			if r.Action != wf.ActionBlock {
				t.Errorf("%s: %q — %v, ждали запрет", layer.name, r.Name, r.Action)
			}
		}
		if self.Weight != dnsGuardPermitWeight || loopback.Weight != dnsGuardPermitWeight {
			t.Errorf("%s: свой процесс и loopback с весами %d и %d", layer.name, self.Weight, loopback.Weight)
		}
		if dns.Weight != dnsGuardBlockWeight || dns.Weight <= local.Weight {
			t.Errorf("%s: запрет DNS с весом %d не тяжелее локальной сети (%d)", layer.name, dns.Weight, local.Weight)
		}
		if block.Weight >= local.Weight || block.Weight >= dhcp.Weight || len(block.Conditions) != 0 {
			t.Errorf("%s: запрет всего — вес %d, условий %d", layer.name, block.Weight, len(block.Conditions))
		}

		checkMatches(t, layer.name+": свой процесс", self.Conditions, []wf.Match{
			{Field: wf.FieldALEAppID, Op: wf.MatchTypeEqual, Value: "app-id"},
		})
		checkMatches(t, layer.name+": loopback", loopback.Conditions, []wf.Match{
			{Field: wf.FieldFlags, Op: wf.MatchTypeFlagsAllSet, Value: wf.ConditionFlagIsLoopback},
		})
		checkMatches(t, layer.name+": запрет DNS", dns.Conditions, []wf.Match{
			{Field: wf.FieldIPRemotePort, Op: wf.MatchTypeEqual, Value: uint16(53)},
			{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoUDP},
			{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoTCP},
		})
		client, server := uint16(68), uint16(67)
		if layer.ipv6 {
			client, server = 546, 547
		}
		checkMatches(t, layer.name+": DHCP", dhcp.Conditions, []wf.Match{
			{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoUDP},
			{Field: wf.FieldIPLocalPort, Op: wf.MatchTypeEqual, Value: client},
			{Field: wf.FieldIPRemotePort, Op: wf.MatchTypeEqual, Value: server},
		})
		if layer.ipv6 {
			ndp := got[4]
			want := []wf.Match{{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoICMPV6}}
			for typ := uint16(133); typ <= 137; typ++ {
				want = append(want, wf.Match{Field: wf.FieldIPLocalPort, Op: wf.MatchTypeEqual, Value: typ})
			}
			if ndp.Action != wf.ActionPermit || ndp.Weight != local.Weight {
				t.Errorf("%s: ND — %v с весом %d", layer.name, ndp.Action, ndp.Weight)
			}
			checkMatches(t, layer.name+": ND", ndp.Conditions, want)
		}

		var wantLocal []wf.Match
		for _, p := range localNets {
			if p.Addr().Is6() == layer.ipv6 {
				wantLocal = append(wantLocal, wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: p})
			}
		}
		if len(wantLocal) == 0 {
			t.Fatalf("%s: локальной сети нет", layer.name)
		}
		checkMatches(t, layer.name+": локальная сеть", local.Conditions, wantLocal)
	}
}

// Переходы между наборами на настоящем WFP (нужны права администратора):
// «закрыто» → защита DNS → «закрыто» → без правил. После каждого перехода
// в сессии ровно правила нового набора — старые сняты, новые на месте.
func TestDNSGuardLockdownSession(t *testing.T) {
	g, err := OpenDNSGuard()
	if err != nil {
		t.Skipf("сессия WFP не открылась (нужны права администратора): %v", err)
	}
	defer g.Close()
	lockdown, err := lockdownRules(g.appID)
	if err != nil {
		t.Fatal(err)
	}
	dns := []netip.Addr{netip.MustParseAddr("1.1.1.1")}

	check := func(what string, state guardState, want int) {
		t.Helper()
		if g.state != state || len(g.rules) != want {
			t.Errorf("%s: состояние %d, правил %d; ждали %d и %d", what, g.state, len(g.rules), state, want)
		}
		if inSession := sublayerRules(t, g); inSession != want {
			t.Errorf("%s: в подслое WFP правил %d, ждали %d", what, inSession, want)
		}
	}

	steps := []struct {
		what  string
		do    func() error
		state guardState
		want  int
	}{
		{"Lockdown", g.Lockdown, guardLockdown, len(lockdown)},
		{"повторный Lockdown", g.Lockdown, guardLockdown, len(lockdown)},
		{"Enable", func() error { return g.Enable(dns) }, guardTunnel, 3 * len(dnsGuardLayers)},
		{"Lockdown после Enable", g.Lockdown, guardLockdown, len(lockdown)},
		{"Disable", g.Disable, guardOpen, 0},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.what, err)
		}
		check(s.what, s.state, s.want)
	}
}

// Набор, который не встал целиком, снимается, а прежний остаётся: здесь
// «закрыто» остаётся «закрыто». Отказ — правило с неверным значением
// условия: оно легче прочих и ставится последним, после всех верных.
func TestDNSGuardReplaceRollback(t *testing.T) {
	g, err := OpenDNSGuard()
	if err != nil {
		t.Skipf("сессия WFP не открылась (нужны права администратора): %v", err)
	}
	defer g.Close()
	if err := g.Lockdown(); err != nil {
		t.Fatal(err)
	}
	want := len(g.rules)

	rules, err := dnsGuardRules([]netip.Addr{netip.MustParseAddr("1.1.1.1")}, g.appID)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := newGuardRule(wf.LayerALEAuthConnectV4, "qwdtt-cli: тест отказа", 1, wf.ActionBlock,
		[]*wf.Match{{Field: wf.FieldIPRemotePort, Op: wf.MatchTypeEqual, Value: "не порт"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.replaceLocked(guardTunnel, "тест", append(rules, bad)); err == nil {
		t.Fatal("набор с неверным правилом встал")
	}
	if g.state != guardLockdown || len(g.rules) != want {
		t.Errorf("после отказа: состояние %d, правил %d; ждали «закрыто» и %d", g.state, len(g.rules), want)
	}
	if inSession := sublayerRules(t, g); inSession != want {
		t.Errorf("в подслое WFP правил %d, ждали %d — новые не сняты", inSession, want)
	}
}

// sublayerRules — сколько правил стоит в нашем подслое WFP.
func sublayerRules(t *testing.T, g *DNSGuard) int {
	t.Helper()
	all, err := g.session.Rules()
	if err != nil {
		t.Fatalf("список правил WFP: %v", err)
	}
	n := 0
	for _, r := range all {
		if r.Sublayer == dnsGuardSublayer {
			n++
		}
	}
	return n
}
