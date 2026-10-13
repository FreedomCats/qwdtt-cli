package linuxsys

import (
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Interface — выбранный физический интерфейс.
type Interface struct {
	Index uint32 // индекс для netguard (SO_BINDTOIFINDEX)
	Name  string // имя, как в ip link: «enp7s0», «wlan0»
}

// DefaultInterface выбирает интерфейс маршрута по умолчанию (0.0.0.0/0 или
// ::/0) таблицы main для семейства: с наименьшей метрикой маршрута, только
// интерфейсы в состоянии UP, без интерфейса exclude (индекс своего адаптера;
// 0 — исключать нечего). Маршруты туннеля — две половины /1, в выбор они не
// попадают, а свой адаптер исключается ещё и явно, поэтому выбор работает и
// при поднятом туннеле. Аналог winsys.DefaultInterface.
//
// ok = false — маршрута по умолчанию для семейства нет (например, сеть без
// IPv6). Это не ошибка.
func DefaultInterface(ipv6 bool, exclude uint64) (iface Interface, ok bool, err error) {
	family := netlink.FAMILY_V4
	if ipv6 {
		family = netlink.FAMILY_V6
	}
	routes, err := netlink.RouteListFiltered(family,
		&netlink.Route{Table: unix.RT_TABLE_MAIN}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return Interface{}, false, err
	}
	iface, ok = pickDefault(routes, exclude, linkState)
	return iface, ok, nil
}

// linkState — имя интерфейса и годится ли он для выбора: поднят
// администратором и не потерял связь. Интерфейса нет (исчез между списком
// маршрутов и этим вызовом) — не годится.
func linkState(index int) (name string, up bool) {
	link, err := netlink.LinkByIndex(index)
	if err != nil {
		return "", false
	}
	attrs := link.Attrs()
	// OperUnknown — у интерфейсов без понятия несущей (tun, часть VPN):
	// для них в счёт только флаг UP.
	oper := attrs.OperState == netlink.OperUp || attrs.OperState == netlink.OperUnknown
	return attrs.Name, attrs.Flags&net.FlagUp != 0 && oper
}

// pickDefault — выбор по готовой таблице: отдельно от netlink, чтобы
// проверять его на поддельной таблице. Ничья по метрике — первый в таблице.
func pickDefault(routes []netlink.Route, exclude uint64, link func(index int) (name string, up bool)) (Interface, bool) {
	var (
		best   Interface
		ok     bool
		lowest int
	)
	for _, r := range routes {
		if !isDefault(r) || r.Type != unix.RTN_UNICAST {
			// unreachable и blackhole по умолчанию — не путь наружу.
			continue
		}
		for _, index := range nexthops(r) {
			if index == 0 || uint64(index) == exclude {
				continue
			}
			if ok && r.Priority >= lowest {
				continue
			}
			name, up := link(index)
			if !up {
				continue
			}
			best, ok, lowest = Interface{Index: uint32(index), Name: name}, true, r.Priority
		}
	}
	return best, ok
}

// isDefault — маршрут на всё пространство семейства. netlink отдаёт его
// с Dst = nil или с нулевой длиной префикса, смотря по версии и семейству.
func isDefault(r netlink.Route) bool {
	if r.Dst == nil {
		return true
	}
	ones, _ := r.Dst.Mask.Size()
	return ones == 0
}

// nexthops — интерфейсы маршрута: один у обычного, несколько у маршрута
// с несколькими шлюзами (ip route add default nexthop … nexthop …).
func nexthops(r netlink.Route) []int {
	if len(r.MultiPath) == 0 {
		return []int{r.LinkIndex}
	}
	indexes := make([]int, 0, len(r.MultiPath))
	for _, nh := range r.MultiPath {
		indexes = append(indexes, nh.LinkIndex)
	}
	return indexes
}
