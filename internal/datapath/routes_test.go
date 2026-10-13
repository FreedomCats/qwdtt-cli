package datapath

import (
	"net/netip"
	"strings"
	"testing"
)

func TestPrefixHalves(t *testing.T) {
	for _, tc := range []struct{ p, lo, hi string }{
		{"0.0.0.0/0", "0.0.0.0/1", "128.0.0.0/1"},
		{"10.0.0.0/8", "10.0.0.0/9", "10.128.0.0/9"},
		{"192.168.1.0/31", "192.168.1.0/32", "192.168.1.1/32"},
		{"::/0", "::/1", "8000::/1"},
		{"2001:db8::/32", "2001:db8::/33", "2001:db8:8000::/33"},
	} {
		lo, hi := prefixHalves(netip.MustParsePrefix(tc.p))
		if lo.String() != tc.lo || hi.String() != tc.hi {
			t.Errorf("prefixHalves(%s) = %s, %s; ждали %s, %s", tc.p, lo, hi, tc.lo, tc.hi)
		}
	}
}

func TestAllowedIPsWarnings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed []string
		addrs   []string
		want    []string // по подстроке на каждое предупреждение; nil — молчать
	}{
		{name: "полный IPv4", allowed: []string{"0.0.0.0/0"}, addrs: []string{"10.7.0.4"}},
		{name: "половины /1", allowed: []string{"0.0.0.0/1", "128.0.0.0/1"}, addrs: []string{"10.7.0.4"}},
		{name: "мелкие куски вплотную",
			allowed: []string{"0.0.0.0/2", "64.0.0.0/2", "128.0.0.0/3", "160.0.0.0/3", "192.0.0.0/2"},
			addrs:   []string{"10.7.0.4"}},
		{name: "перекрытия и пробелы", allowed: []string{" 0.0.0.0/1", "10.0.0.0/8", "128.0.0.0/1 "},
			addrs: []string{"10.7.0.4"}},
		{name: "сплит IPv4", allowed: []string{"10.0.0.0/8", "192.168.0.0/16"}, addrs: []string{"10.7.0.4"},
			want: []string{"не весь IPv4 (AllowedIPs: 10.0.0.0/8, 192.168.0.0/16)"}},
		{name: "не хватает одной /32", allowed: []string{"0.0.0.0/1", "128.0.0.0/2", "192.0.0.0/3",
			"224.0.0.0/4", "240.0.0.0/5", "248.0.0.0/6", "252.0.0.0/7", "254.0.0.0/8", "255.0.0.0/9",
			"255.128.0.0/10", "255.192.0.0/11", "255.224.0.0/12", "255.240.0.0/13", "255.248.0.0/14",
			"255.252.0.0/15", "255.254.0.0/16", "255.255.0.0/17", "255.255.128.0/18", "255.255.192.0/19",
			"255.255.224.0/20", "255.255.240.0/21", "255.255.248.0/22", "255.255.252.0/23",
			"255.255.254.0/24", "255.255.255.0/25", "255.255.255.128/26", "255.255.255.192/27",
			"255.255.255.224/28", "255.255.255.240/29", "255.255.255.248/30", "255.255.255.252/31",
			"255.255.255.254/32"},
			addrs: []string{"10.7.0.4"},
			want:  []string{"не весь IPv4"}},
		{name: "только IPv6", allowed: []string{"::/0"}, addrs: []string{"10.7.0.4"},
			want: []string{"не весь IPv4 (AllowedIPs: ничего)"}},
		{name: "IPv6 без адреса не проверяется", allowed: []string{"0.0.0.0/0", "2001:db8::/32"},
			addrs: []string{"10.7.0.4"}},
		{name: "IPv6 с адресом и полным ::/0", allowed: []string{"0.0.0.0/0", "::/0"},
			addrs: []string{"10.7.0.4", "fd00::4"}},
		{name: "IPv6 с адресом без маршрута", allowed: []string{"0.0.0.0/0"},
			addrs: []string{"10.7.0.4", "fd00::4"},
			want:  []string{"не весь IPv6 (AllowedIPs: ничего)"}},
		{name: "битый префикс не считается", allowed: []string{"0.0.0.0/0", "мусор"}, addrs: []string{"10.7.0.4"}},
		{name: "неканоничный префикс", allowed: []string{"1.2.3.4/0"}, addrs: []string{"10.7.0.4"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conf := &WGConfig{AllowedIPs: tc.allowed}
			for _, a := range tc.addrs {
				conf.Addresses = append(conf.Addresses, netip.MustParseAddr(a))
			}
			got := allowedIPsWarnings(conf)
			if len(got) != len(tc.want) {
				t.Fatalf("предупреждения %q, ждали %d", got, len(tc.want))
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("предупреждение %q, ждали подстроку %q", got[i], want)
				}
			}
		})
	}
}
