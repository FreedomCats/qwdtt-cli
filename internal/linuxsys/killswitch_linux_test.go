package linuxsys

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// Порядок правил «закрыто» в output: метка, loopback, запрет порта 53 —
// раньше разрешений, служебные протоколы, локальная сеть, drop всего. В
// forward — без метки, loopback и служебных протоколов.
func TestLockdownRules(t *testing.T) {
	v4, v6 := byte(unix.NFPROTO_IPV4), byte(unix.NFPROTO_IPV6)
	local := []guardRule{
		{family: v4, daddr: netip.MustParsePrefix("10.0.0.0/8"), accept: true},
		{family: v4, daddr: netip.MustParsePrefix("172.16.0.0/12"), accept: true},
		{family: v4, daddr: netip.MustParsePrefix("192.168.0.0/16"), accept: true},
		{family: v4, daddr: netip.MustParsePrefix("169.254.0.0/16"), accept: true},
		{family: v4, daddr: netip.MustParsePrefix("224.0.0.0/4"), accept: true},
		{family: v4, daddr: netip.MustParsePrefix("255.255.255.255/32"), accept: true},
		{family: v6, daddr: netip.MustParsePrefix("fe80::/10"), accept: true},
		{family: v6, daddr: netip.MustParsePrefix("fc00::/7"), accept: true},
		{family: v6, daddr: netip.MustParsePrefix("ff00::/8"), accept: true},
	}
	dns := []guardRule{
		{proto: unix.IPPROTO_UDP, dport: 53},
		{proto: unix.IPPROTO_TCP, dport: 53},
	}

	var out []guardRule
	out = append(out, guardRule{mark: true, accept: true}, guardRule{loopback: true, accept: true})
	out = append(out, dns...)
	out = append(out,
		guardRule{family: v4, proto: unix.IPPROTO_UDP, sport: 68, dport: 67, accept: true},
		guardRule{family: v6, proto: unix.IPPROTO_UDP, sport: 546, dport: 547, accept: true},
		guardRule{family: v6, ndp: true, accept: true})
	out = append(out, local...)
	out = append(out, guardRule{})
	assertRules(t, "output", lockdownRules(false), out)

	var fwd []guardRule
	fwd = append(fwd, dns...)
	fwd = append(fwd, local...)
	fwd = append(fwd, guardRule{})
	assertRules(t, "forward", lockdownRules(true), fwd)
}

// Подсеть сравнивается через маску, адрес — без неё; порты источника и
// назначения — смещения 0 и 2; ND — тип ICMPv6 от 133 до 137; правило без
// условий — один вердикт.
func TestGuardRuleExprsLockdown(t *testing.T) {
	subnet := guardRule{family: unix.NFPROTO_IPV4, daddr: netip.MustParsePrefix("172.16.0.0/12"), accept: true}.exprs(0)
	if len(subnet) != 6 {
		t.Fatalf("подсеть: %d выражений, ждали 6: %#v", len(subnet), subnet)
	}
	if b, ok := subnet[3].(*expr.Bitwise); !ok || b.Len != 4 || !bytes.Equal(b.Mask, []byte{255, 240, 0, 0}) ||
		!bytes.Equal(b.Xor, make([]byte, 4)) {
		t.Errorf("маска: %#v", subnet[3])
	}
	if c := subnet[4].(*expr.Cmp); !bytes.Equal(c.Data, []byte{172, 16, 0, 0}) {
		t.Errorf("подсеть: % x", c.Data)
	}

	v6 := guardRule{family: unix.NFPROTO_IPV6, daddr: netip.MustParsePrefix("fe80::/10")}.exprs(0)
	if b := v6[3].(*expr.Bitwise); b.Len != 16 || !bytes.Equal(b.Mask, append([]byte{255, 192}, make([]byte, 14)...)) {
		t.Errorf("маска IPv6: %#v", b)
	}

	dhcp := guardRule{proto: unix.IPPROTO_UDP, sport: 68, dport: 67, accept: true}.exprs(0)
	if len(dhcp) != 7 {
		t.Fatalf("DHCP: %d выражений, ждали 7: %#v", len(dhcp), dhcp)
	}
	for i, want := range []struct {
		offset uint32
		port   []byte
	}{{0, []byte{0, 68}}, {2, []byte{0, 67}}} {
		p := dhcp[2+2*i].(*expr.Payload)
		c := dhcp[3+2*i].(*expr.Cmp)
		if p.Base != expr.PayloadBaseTransportHeader || p.Offset != want.offset || p.Len != 2 || !bytes.Equal(c.Data, want.port) {
			t.Errorf("порт %d: %#v, % x", i, p, c.Data)
		}
	}

	ndp := guardRule{ndp: true, accept: true}.exprs(0)
	if len(ndp) != 6 {
		t.Fatalf("ND: %d выражений, ждали 6: %#v", len(ndp), ndp)
	}
	if c := ndp[1].(*expr.Cmp); !bytes.Equal(c.Data, []byte{unix.IPPROTO_ICMPV6}) {
		t.Errorf("протокол ND: % x", c.Data)
	}
	if p := ndp[2].(*expr.Payload); p.Offset != 0 || p.Len != 1 {
		t.Errorf("тип ICMPv6: %#v", p)
	}
	lo, hi := ndp[3].(*expr.Cmp), ndp[4].(*expr.Cmp)
	if lo.Op != expr.CmpOpGte || lo.Data[0] != 133 || hi.Op != expr.CmpOpLte || hi.Data[0] != 137 {
		t.Errorf("диапазон типов: %#v %#v", lo, hi)
	}

	if all := (guardRule{}).exprs(0); len(all) != 1 || all[0].(*expr.Verdict).Kind != expr.VerdictDrop {
		t.Errorf("drop всего: %#v", all)
	}
}

func TestPrefixMask(t *testing.T) {
	for _, tt := range []struct {
		prefix string
		want   []byte
	}{
		{"10.0.0.0/8", []byte{255, 0, 0, 0}},
		{"169.254.0.0/16", []byte{255, 255, 0, 0}},
		{"224.0.0.0/4", []byte{240, 0, 0, 0}},
		{"255.255.255.255/32", []byte{255, 255, 255, 255}},
		{"fc00::/7", append([]byte{254}, make([]byte, 15)...)},
	} {
		if got := prefixMask(netip.MustParsePrefix(tt.prefix)); !bytes.Equal(got, tt.want) {
			t.Errorf("%s: % x, ждали % x", tt.prefix, got, tt.want)
		}
	}
}
