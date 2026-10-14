package linuxsys

import (
	"net"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func prefix(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// fakeLinks — состояние интерфейсов поддельной таблицы: индекс → имя;
// в down — опущенные.
func fakeLinks(down ...int) func(int) (string, bool) {
	names := map[int]string{1: "lo", 2: "eth0", 3: "wlan0", 4: "tun0", 7: "qwdtt-cli"}
	return func(index int) (string, bool) {
		name, ok := names[index]
		if !ok {
			return "", false
		}
		for _, d := range down {
			if d == index {
				return name, false
			}
		}
		return name, true
	}
}

func TestPickDefault(t *testing.T) {
	route := func(dst *net.IPNet, link, priority int) netlink.Route {
		return netlink.Route{Dst: dst, LinkIndex: link, Priority: priority, Type: unix.RTN_UNICAST}
	}
	all4 := prefix(t, "0.0.0.0/0")
	half1, half2 := prefix(t, "0.0.0.0/1"), prefix(t, "128.0.0.0/1")
	corp := prefix(t, "10.0.0.0/15")

	for _, tc := range []struct {
		name    string
		routes  []netlink.Route
		exclude uint64
		down    []int
		want    string // имя или "" — нет
	}{
		{"один маршрут, Dst nil", []netlink.Route{route(nil, 2, 20100)}, 0, nil, "eth0"},
		{"один маршрут, Dst /0", []netlink.Route{route(all4, 2, 20100)}, 0, nil, "eth0"},
		{"наименьшая метрика", []netlink.Route{route(nil, 2, 20100), route(nil, 3, 600)}, 0, nil, "wlan0"},
		{"ничья — первый", []netlink.Route{route(nil, 3, 100), route(nil, 2, 100)}, 0, nil, "wlan0"},
		{"опущенный пропускается", []netlink.Route{route(nil, 3, 600), route(nil, 2, 20100)}, 0, []int{3}, "eth0"},
		{"исчезнувший интерфейс", []netlink.Route{route(nil, 9, 1), route(nil, 2, 20100)}, 0, nil, "eth0"},
		{"половины туннеля не в счёт", []netlink.Route{route(half1, 7, 0), route(half2, 7, 0), route(nil, 2, 20100)}, 0, nil, "eth0"},
		{"свой адаптер исключён", []netlink.Route{route(nil, 7, 0), route(nil, 2, 20100)}, 7, nil, "eth0"},
		{"корпоративные сети не в счёт", []netlink.Route{route(corp, 4, 50), route(nil, 2, 20100)}, 0, nil, "eth0"},
		{"нет маршрута по умолчанию", []netlink.Route{route(corp, 4, 50)}, 0, nil, ""},
		{"единственный опущен", []netlink.Route{route(nil, 2, 100)}, 0, []int{2}, ""},
		{"unreachable по умолчанию", []netlink.Route{
			{LinkIndex: 1, Priority: 1, Type: unix.RTN_UNREACHABLE}, route(nil, 2, 20100)}, 0, nil, "eth0"},
		{"несколько шлюзов", []netlink.Route{{Priority: 5, Type: unix.RTN_UNICAST,
			MultiPath: []*netlink.NexthopInfo{{LinkIndex: 3}, {LinkIndex: 2}}}}, 0, []int{3}, "eth0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pickDefault(tc.routes, tc.exclude, fakeLinks(tc.down...))
			if tc.want == "" {
				if ok {
					t.Fatalf("выбран %+v, ждали «нет»", got)
				}
				return
			}
			if !ok || got.Name != tc.want {
				t.Fatalf("выбран %+v (ok=%v), ждали %s", got, ok, tc.want)
			}
		})
	}
}

// Живой выбор на этой машине: без прав, ничего не меняет. Нет маршрута
// по умолчанию — тоже исход, а не ошибка.
func TestDefaultInterfaceLive(t *testing.T) {
	for _, ipv6 := range []bool{false, true} {
		iface, ok, err := DefaultInterface(ipv6, 0)
		if err != nil {
			t.Fatalf("ipv6=%v: %v", ipv6, err)
		}
		if !ok {
			t.Logf("ipv6=%v: маршрута по умолчанию нет", ipv6)
			continue
		}
		t.Logf("ipv6=%v: %s (индекс %d)", ipv6, iface.Name, iface.Index)
		if got, err := net.InterfaceByIndex(int(iface.Index)); err != nil || got.Name != iface.Name {
			t.Errorf("ipv6=%v: индекс %d не совпадает с именем %q: %v %v", ipv6, iface.Index, iface.Name, got, err)
		}
		// Исключённый выбранный интерфейс не выбирается снова.
		if again, ok, _ := DefaultInterface(ipv6, uint64(iface.Index)); ok && again.Index == iface.Index {
			t.Errorf("ipv6=%v: исключённый %d выбран снова", ipv6, iface.Index)
		}
	}
}
