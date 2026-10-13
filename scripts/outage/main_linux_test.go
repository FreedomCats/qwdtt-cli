package main

import (
	"net/netip"
	"testing"

	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestDropFrom(t *testing.T) {
	cases := []struct {
		addr   string
		family byte
		offset uint32
		size   uint32
	}{
		{"198.51.100.20", unix.NFPROTO_IPV4, 12, 4},
		{"2001:db8::1", unix.NFPROTO_IPV6, 8, 16},
	}
	for _, c := range cases {
		a := netip.MustParseAddr(c.addr)
		e := dropFrom(a)
		if len(e) != 6 {
			t.Fatalf("%s: выражений %d", a, len(e))
		}
		if fam := e[1].(*expr.Cmp).Data; len(fam) != 1 || fam[0] != c.family {
			t.Errorf("%s: семейство %v, ждали %d", a, fam, c.family)
		}
		p := e[2].(*expr.Payload)
		if p.Offset != c.offset || p.Len != c.size {
			t.Errorf("%s: смещение %d длина %d, ждали %d и %d", a, p.Offset, p.Len, c.offset, c.size)
		}
		if got, _ := netip.AddrFromSlice(e[3].(*expr.Cmp).Data); got != a {
			t.Errorf("%s: адрес %s", a, got)
		}
		if _, ok := e[4].(*expr.Counter); !ok {
			t.Errorf("%s: нет счётчика", a)
		}
		if v := e[5].(*expr.Verdict); v.Kind != expr.VerdictDrop {
			t.Errorf("%s: вердикт %v", a, v.Kind)
		}
	}
}

func TestRestorable(t *testing.T) {
	cases := []struct {
		r    netlink.Route
		want bool
	}{
		{netlink.Route{Table: unix.RT_TABLE_MAIN, Protocol: unix.RTPROT_DHCP}, true},
		{netlink.Route{Table: unix.RT_TABLE_MAIN, Protocol: unix.RTPROT_BOOT}, true},
		{netlink.Route{Table: 100, Protocol: unix.RTPROT_STATIC}, true},
		{netlink.Route{Table: unix.RT_TABLE_MAIN, Protocol: unix.RTPROT_KERNEL}, false},
		{netlink.Route{Table: unix.RT_TABLE_MAIN, Protocol: unix.RTPROT_RA}, false},
		{netlink.Route{Table: unix.RT_TABLE_LOCAL, Protocol: unix.RTPROT_BOOT}, false},
	}
	for i, c := range cases {
		if got := restorable(c.r); got != c.want {
			t.Errorf("[%d] таблица %d, proto %d: %v, ждали %v", i, c.r.Table, c.r.Protocol, got, c.want)
		}
	}
}
