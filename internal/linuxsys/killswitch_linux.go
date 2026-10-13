package linuxsys

import (
	"net/netip"

	"golang.org/x/sys/unix"
)

// Lockdown ставит набор «закрыто» — kill-switch (plan-e3.md, K2), пока
// туннель не поднят: системе разрешены только сокеты tt, loopback,
// служебные протоколы (DHCP, ICMPv6 ND и RA) и локальная сеть, а DNS —
// никуда, кроме сокетов tt и loopback, даже к локальной сети: резолвер
// роутера отдал бы запросы провайдеру. Прочее отбрасывается; отправка
// UDP тогда кончается EPERM, TCP — таймаутом.
//
// Правила заменяются одним пакетом, как в Enable: перехода через пустую
// таблицу нет. На отказе ядро не применяет ничего, и остаётся прежний
// набор.
func (g *DNSGuard) Lockdown() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.applyLocked(guardLockdown, lockdownRules(false), lockdownRules(true))
}

// localNets — локальная сеть, к которой kill-switch пропускает (plan-e3.md,
// решение 4): частные и link-local диапазоны, мультикаст, широковещание.
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

// lockdownRules — правила цепочки «закрыто» по порядку. Запрет порта 53
// идёт раньше разрешений: иначе DNS ушёл бы к резолверу в локальной сети.
// forward — транзитный трафик контейнеров и ВМ: метки, loopback и
// служебных протоколов хоста там нет.
func lockdownRules(forward bool) []guardRule {
	var rules []guardRule
	if !forward {
		rules = append(rules,
			guardRule{mark: true, accept: true},
			guardRule{loopback: true, accept: true})
	}
	rules = append(rules,
		guardRule{proto: unix.IPPROTO_UDP, dport: 53},
		guardRule{proto: unix.IPPROTO_TCP, dport: 53})
	if !forward {
		// DHCP продлевает аренду и одноадресно — сервер бывает и не в
		// локальной сети; ND и RA ходят и на глобальные адреса соседей.
		rules = append(rules,
			guardRule{family: unix.NFPROTO_IPV4, proto: unix.IPPROTO_UDP, sport: 68, dport: 67, accept: true},
			guardRule{family: unix.NFPROTO_IPV6, proto: unix.IPPROTO_UDP, sport: 546, dport: 547, accept: true},
			guardRule{family: unix.NFPROTO_IPV6, ndp: true, accept: true})
	}
	for _, p := range localNets {
		family := byte(unix.NFPROTO_IPV4)
		if p.Addr().Is6() {
			family = unix.NFPROTO_IPV6
		}
		rules = append(rules, guardRule{family: family, daddr: p, accept: true})
	}
	return append(rules, guardRule{})
}
