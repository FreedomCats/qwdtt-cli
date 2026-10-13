package linuxsys

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// Порядок правил output: метка, loopback, разрешения резолверам по обоим
// протоколам, потом запреты порта 53. В forward метки и loopback нет.
func TestDNSGuardRules(t *testing.T) {
	v4 := netip.MustParseAddr("192.0.2.53")
	v6 := netip.MustParseAddr("2001:db8::53")
	dns := []netip.Addr{v4, v6}

	out := dnsGuardRules(dns, false, false)
	want := []guardRule{
		{mark: true, accept: true},
		{loopback: true, accept: true},
		{family: unix.NFPROTO_IPV4, daddr: v4, proto: unix.IPPROTO_UDP, accept: true},
		{family: unix.NFPROTO_IPV4, daddr: v4, proto: unix.IPPROTO_TCP, accept: true},
		{family: unix.NFPROTO_IPV6, daddr: v6, proto: unix.IPPROTO_UDP, accept: true},
		{family: unix.NFPROTO_IPV6, daddr: v6, proto: unix.IPPROTO_TCP, accept: true},
		{proto: unix.IPPROTO_UDP},
		{proto: unix.IPPROTO_TCP},
	}
	assertRules(t, "output", out, want)
	assertRules(t, "forward", dnsGuardRules(dns, false, true), want[2:])
}

// При закрытом IPv6 запрет IPv6 идёт раньше разрешений, а резолвер IPv6
// не разрешается: маршрута в туннель для него нет.
func TestDNSGuardRulesBlockIPv6(t *testing.T) {
	v4 := netip.MustParseAddr("192.0.2.53")
	dns := []netip.Addr{v4, netip.MustParseAddr("2001:db8::53")}

	want := []guardRule{
		{mark: true, accept: true},
		{loopback: true, accept: true},
		{family: unix.NFPROTO_IPV6},
		{family: unix.NFPROTO_IPV4, daddr: v4, proto: unix.IPPROTO_UDP, accept: true},
		{family: unix.NFPROTO_IPV4, daddr: v4, proto: unix.IPPROTO_TCP, accept: true},
		{proto: unix.IPPROTO_UDP},
		{proto: unix.IPPROTO_TCP},
	}
	assertRules(t, "output", dnsGuardRules(dns, true, false), want)
	assertRules(t, "forward", dnsGuardRules(dns, true, true), want[2:])
}

// Адрес IPv4 в виде IPv6 (::ffff:a.b.c.d) сравнивается как IPv4, пустой
// пропускается; без резолверов остаются только запреты.
func TestDNSGuardRulesOddAddresses(t *testing.T) {
	mapped := netip.MustParseAddr("::ffff:192.0.2.53")
	rules := dnsGuardRules([]netip.Addr{mapped, {}}, false, true)
	if len(rules) != 4 || rules[0].family != unix.NFPROTO_IPV4 || rules[0].daddr != mapped.Unmap() {
		t.Errorf("правила: %+v", rules)
	}
	if rules := dnsGuardRules(nil, false, true); len(rules) != 2 || rules[0].accept || rules[1].accept {
		t.Errorf("без резолверов: %+v", rules)
	}
}

func assertRules(t *testing.T, chain string, got, want []guardRule) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d правил, ждали %d:\n%+v", chain, len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s, правило %d: %+v, ждали %+v", chain, i, got[i], want[i])
		}
	}
}

// Выражения: что сравнивается и с чем. Адрес — в байтах сети, порт —
// big-endian, имя интерфейса — IFNAMSIZ байт, вердикт — последним.
func TestGuardRuleExprs(t *testing.T) {
	r := guardRule{family: unix.NFPROTO_IPV6, daddr: netip.MustParseAddr("2001:db8::53"),
		proto: unix.IPPROTO_TCP, accept: true}
	e := r.exprs(0x1074)
	if len(e) != 9 {
		t.Fatalf("%d выражений, ждали 9: %#v", len(e), e)
	}
	if p, ok := e[2].(*expr.Payload); !ok || p.Base != expr.PayloadBaseNetworkHeader || p.Offset != 24 || p.Len != 16 {
		t.Errorf("адрес назначения IPv6: %#v", e[2])
	}
	if c := e[3].(*expr.Cmp); !bytes.Equal(c.Data, r.daddr.AsSlice()) {
		t.Errorf("адрес: % x", c.Data)
	}
	if p, ok := e[6].(*expr.Payload); !ok || p.Base != expr.PayloadBaseTransportHeader || p.Offset != 2 || p.Len != 2 {
		t.Errorf("порт назначения: %#v", e[6])
	}
	if c := e[7].(*expr.Cmp); !bytes.Equal(c.Data, []byte{0, 53}) {
		t.Errorf("порт: % x", c.Data)
	}
	if v := e[8].(*expr.Verdict); v.Kind != expr.VerdictAccept {
		t.Errorf("вердикт: %v", v.Kind)
	}

	v4 := guardRule{daddr: netip.MustParseAddr("192.0.2.53")}.exprs(0)
	if p := v4[0].(*expr.Payload); p.Offset != 16 || p.Len != 4 {
		t.Errorf("адрес назначения IPv4: %#v", p)
	}
	if v := v4[2].(*expr.Verdict); v.Kind != expr.VerdictDrop {
		t.Errorf("вердикт без accept: %v", v.Kind)
	}

	lo := guardRule{loopback: true}.exprs(0)
	if c := lo[1].(*expr.Cmp); len(c.Data) != unix.IFNAMSIZ || !bytes.HasPrefix(c.Data, []byte("lo\x00")) {
		t.Errorf("имя интерфейса: % x", c.Data)
	}
	mark := guardRule{mark: true}.exprs(0x1074)
	if m := mark[0].(*expr.Meta); m.Key != expr.MetaKeyMARK {
		t.Errorf("метка: %#v", m)
	}
}
