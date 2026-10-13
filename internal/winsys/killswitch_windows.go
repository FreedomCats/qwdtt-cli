package winsys

import (
	"net/netip"

	"github.com/tailscale/wf"
)

// Веса правил «закрыто» ниже весов защиты DNS: запрет порта 53 (14)
// тяжелее разрешения локальной сети, а свой процесс и loopback (15) —
// тяжелее всех запретов.
const (
	lockdownLocalWeight = 13 // DHCP, ICMPv6 ND и RA, локальная сеть
	lockdownBlockWeight = 12 // всё прочее
)

// Lockdown ставит набор «закрыто» — kill-switch (plan-e3.md, W1), пока
// туннель не поднят: системе разрешены только процесс tt, loopback,
// служебные протоколы (DHCP, ICMPv6 ND и RA) и локальная сеть, а DNS —
// никуда, кроме tt и loopback, даже к локальной сети: резолвер роутера
// отдал бы запросы провайдеру. Прочее запрещено, как в K2 на Linux.
//
// Правила заменяются, как в Enable (replaceLocked): мига без правил на
// переходе нет. На отказе остаётся прежний набор.
func (g *DNSGuard) Lockdown() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	rules, err := lockdownRules(g.appID)
	if err != nil {
		return err
	}
	return g.replaceLocked(guardLockdown, "kill-switch", rules)
}

// localNets — локальная сеть, к которой kill-switch пропускает (plan-e3.md,
// решение 4): частные и link-local диапазоны, мультикаст, широковещание.
// Тот же список, что у linuxsys.
var localNets = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("255.255.255.255/32"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("ff00::/8"),
}

// lockdownRules строит правила для Lockdown; отдельно — ради теста без
// прав. Состав — по образцу tunnel/firewall/rules.go wireguard-windows
// (MIT, v1.1.1: permitLoopback, permitDHCPIPv4/IPv6, permitNdp, blockAll),
// но DHCP — к любому адресу, как в K2: аренда продлевается одноадресно, и
// сервер бывает не в локальной сети. На каждом слое — сверху вниз по весу:
// свой процесс и loopback, запрет порта 53, служебные протоколы и
// локальная сеть, запрет всего.
func lockdownRules(appID string) ([]*wf.Rule, error) {
	var rules []*wf.Rule
	for _, layer := range dnsGuardLayers {
		add := func(name string, weight uint64, action wf.Action, conds ...*wf.Match) error {
			r, err := newGuardRule(layer.id, name+", "+layer.name, weight, action, conds)
			if err != nil {
				return err
			}
			rules = append(rules, r)
			return nil
		}

		if err := add("tt: kill-switch, сам tt", dnsGuardPermitWeight, wf.ActionPermit,
			&wf.Match{Field: wf.FieldALEAppID, Op: wf.MatchTypeEqual, Value: appID}); err != nil {
			return nil, err
		}
		if err := add("tt: kill-switch, loopback", dnsGuardPermitWeight, wf.ActionPermit,
			&wf.Match{Field: wf.FieldFlags, Op: wf.MatchTypeFlagsAllSet, Value: wf.ConditionFlagIsLoopback}); err != nil {
			return nil, err
		}
		if err := add("tt: kill-switch, запрет DNS", dnsGuardBlockWeight, wf.ActionBlock,
			dnsPortMatches()...); err != nil {
			return nil, err
		}

		// DHCP: клиент — порт 68 (546 у DHCPv6), сервер — 67 (547). На
		// входящем слое «локальный» — тоже клиент.
		client, server := uint16(68), uint16(67)
		if layer.ipv6 {
			client, server = 546, 547
		}
		if err := add("tt: kill-switch, DHCP", lockdownLocalWeight, wf.ActionPermit,
			&wf.Match{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoUDP},
			&wf.Match{Field: wf.FieldIPLocalPort, Op: wf.MatchTypeEqual, Value: client},
			&wf.Match{Field: wf.FieldIPRemotePort, Op: wf.MatchTypeEqual, Value: server}); err != nil {
			return nil, err
		}
		if layer.ipv6 {
			// ND и RA ходят и на глобальные адреса соседей. Тип ICMP на
			// слоях ALE лежит в поле локального порта.
			ndp := []*wf.Match{{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoICMPV6}}
			for typ := uint16(133); typ <= 137; typ++ {
				ndp = append(ndp, &wf.Match{Field: wf.FieldIPLocalPort, Op: wf.MatchTypeEqual, Value: typ})
			}
			if err := add("tt: kill-switch, ICMPv6 ND и RA", lockdownLocalWeight, wf.ActionPermit, ndp...); err != nil {
				return nil, err
			}
		}

		var local []*wf.Match
		for _, p := range localNets {
			if p.Addr().Is6() == layer.ipv6 {
				local = append(local, &wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: p})
			}
		}
		if err := add("tt: kill-switch, локальная сеть", lockdownLocalWeight, wf.ActionPermit, local...); err != nil {
			return nil, err
		}
		if err := add("tt: kill-switch, запрет всего", lockdownBlockWeight, wf.ActionBlock); err != nil {
			return nil, err
		}
	}
	return rules, nil
}
