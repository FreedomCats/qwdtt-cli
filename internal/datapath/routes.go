package datapath

import (
	"fmt"
	"net/netip"
	"strings"
)

// fullTunnelRoutes — маршруты, которые tun ставит через адаптер: всё
// адресное пространство обоих семейств половинами /1. Маршрут по умолчанию
// не трогается (architecture.md §09), половины выигрывают у него длиной
// префикса.
//
// Набор постоянный и от AllowedIPs сервера не зависит (решение W3): что
// сервер не разрешил, уходит в туннель и отбрасывается wireguard-go, а не
// идёт тихо мимо туннеля. Так же блокируется IPv6, если сервер не выдал
// адреса IPv6 (plan-e2.md, решение 2). Неполные AllowedIPs видны по
// предупреждению allowedIPsWarnings.
var fullTunnelRoutes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/1"),
	netip.MustParsePrefix("128.0.0.0/1"),
	netip.MustParsePrefix("::/1"),
	netip.MustParsePrefix("8000::/1"),
}

// allowedIPsWarnings объясняет, что из трафика туннель отбросит, раз маршруты
// забирают всё, а сервер разрешил не всё. Пустой результат — сказать нечего.
//
// IPv4 проверяется всегда. IPv6 — только если сервер выдал адрес IPv6: без
// адреса IPv6 блокируется намеренно (решение 2), и предупреждать не о чем.
// Префиксы, которые не разбираются, в покрытии не участвуют: такой конфиг
// не примет и сам wireguard-go.
func allowedIPsWarnings(conf *WGConfig) []string {
	var v4, v6 []netip.Prefix
	for _, s := range conf.AllowedIPs {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		p = p.Masked()
		if p.Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	has6 := false
	for _, a := range conf.Addresses {
		if a.Is6() && !a.Is4In6() {
			has6 = true
		}
	}

	var warnings []string
	if !prefixCovered(netip.MustParsePrefix("0.0.0.0/0"), v4) {
		warnings = append(warnings, allowedIPsWarning("IPv4", v4))
	}
	if has6 && !prefixCovered(netip.MustParsePrefix("::/0"), v6) {
		warnings = append(warnings, allowedIPsWarning("IPv6", v6))
	}
	return warnings
}

func allowedIPsWarning(family string, allowed []netip.Prefix) string {
	list := "ничего"
	if len(allowed) > 0 {
		parts := make([]string, len(allowed))
		for i, p := range allowed {
			parts[i] = p.String()
		}
		list = strings.Join(parts, ", ")
	}
	return fmt.Sprintf("сервер разрешил в туннеле не весь %s (AllowedIPs: %s): "+
		"трафик к остальным адресам %s уходит в туннель и отбрасывается", family, list, family)
}

// prefixCovered — покрыт ли p объединением list. Если p не лежит целиком
// в одном из префиксов, он делится пополам, и покрыты должны быть обе
// половины; в половины, которых list не касается, спуск не идёт, так что
// глубина и число шагов ограничены длиной list и разрядностью адреса.
func prefixCovered(p netip.Prefix, list []netip.Prefix) bool {
	overlaps := false
	for _, q := range list {
		if q.Bits() <= p.Bits() && q.Contains(p.Addr()) {
			return true
		}
		if q.Overlaps(p) {
			overlaps = true
		}
	}
	if !overlaps || p.Bits() == p.Addr().BitLen() {
		return false
	}
	lo, hi := prefixHalves(p)
	return prefixCovered(lo, list) && prefixCovered(hi, list)
}

// prefixHalves делит префикс на две половины на один бит длиннее.
func prefixHalves(p netip.Prefix) (lo, hi netip.Prefix) {
	bits := p.Bits()
	a := p.Masked().Addr()
	lo = netip.PrefixFrom(a, bits+1)

	b := a.As16()
	offset := 0
	if a.Is4() {
		offset = 12 // в As16 адрес IPv4 лежит в последних четырёх байтах
	}
	b[offset+bits/8] |= 0x80 >> (bits % 8)
	h := netip.AddrFrom16(b)
	if a.Is4() {
		h = h.Unmap()
	}
	hi = netip.PrefixFrom(h, bits+1)
	return lo, hi
}
