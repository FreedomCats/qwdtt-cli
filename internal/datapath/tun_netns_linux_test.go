package datapath

import (
	"context"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// netnsEnv включает проверки, которые создают настоящий TUN. Только в
// отдельном сетевом пространстве (контейнер, plan-e4.md, «Где гонять живые
// прогоны»), не на машине разработчика:
//
//	CGO_ENABLED=0 go test -c -o build/out/datapath.test ./internal/datapath
//	docker run --rm --cap-add NET_ADMIN --device /dev/net/tun -e QWDTT_TEST_NETNS=1 \
//	  -v "$PWD/build/out":/qwdtt-cli:ro ubuntu:latest /qwdtt-cli/datapath.test -test.v -test.run Netns
const netnsEnv = "QWDTT_TEST_NETNS"

func skipOutsideNetns(t *testing.T) {
	t.Helper()
	if os.Getenv(netnsEnv) != "1" || os.Geteuid() != 0 {
		t.Skipf("создаёт TUN: только в контейнере, %s=1 и root", netnsEnv)
	}
}

// linkAddrs — адреса глобальной области интерфейса name и его MTU.
func linkAddrs(t *testing.T, name string) (addrs []netip.Addr, mtu int, up bool) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("интерфейс %s: %v", name, err)
	}
	list, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Scope == unix.RT_SCOPE_UNIVERSE {
			ip, _ := netip.AddrFromSlice(a.IP)
			addrs = append(addrs, ip.Unmap())
		}
	}
	slices.SortFunc(addrs, netip.Addr.Compare)
	return addrs, link.Attrs().MTU, link.Attrs().Flags&unix.IFF_UP != 0
}

// linkRoutes — маршруты через интерфейс name, кроме тех, что ядро ставит
// само под адреса (fe80::/64, /128 адреса IPv6).
func linkRoutes(t *testing.T, name string) []netip.Prefix {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("интерфейс %s: %v", name, err)
	}
	list, err := netlink.RouteList(link, netlink.FAMILY_ALL)
	if err != nil {
		t.Fatal(err)
	}
	var out []netip.Prefix
	for _, r := range list {
		if r.Dst == nil || r.Protocol == unix.RTPROT_KERNEL {
			continue
		}
		ones, _ := r.Dst.Mask.Size()
		ip, _ := netip.AddrFromSlice(r.Dst.IP)
		p := netip.PrefixFrom(ip.Unmap(), ones)
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int { return a.Addr().Compare(b.Addr()) })
	return out
}

func sortedRoutes(routes []netip.Prefix) []netip.Prefix {
	out := slices.Clone(routes)
	slices.SortFunc(out, func(a, b netip.Prefix) int { return a.Addr().Compare(b.Addr()) })
	return out
}

func linkExists(name string) bool {
	_, err := netlink.LinkByName(name)
	return err == nil
}

// Настоящий адаптер: Start создаёт интерфейс с адресами, MTU и маршрутами /1,
// Suspend снимает маршруты, повторный Start с другими адресами и MTU
// перенастраивает тот же интерфейс и возвращает маршруты, Close его убирает.
//
// DNS в контейнере — перезапись /etc/resolv.conf (docker монтирует его
// отдельным файлом, так что запись идёт на месте): на время туннеля — DNS
// конфига, после Close — прежний файл.
func TestTunAdapterNetns(t *testing.T) {
	skipOutsideNetns(t)
	const name = "tttest0"
	origResolv, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		t.Fatal(err)
	}
	rec := &recorder{}
	w := NewTun(TunOptions{Name: name, Logf: rec.logf})
	defer w.Close()

	conf := testWGConfig(t, "127.0.0.1:9")
	conf.Addresses = []netip.Addr{netip.MustParseAddr("10.7.0.2"), netip.MustParseAddr("fd00::2")}
	conf.MTU = 1380
	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start: %v\n%s", err, rec.dump())
	}
	addrs, mtu, up := linkAddrs(t, name)
	if !slices.Equal(addrs, conf.Addresses) || mtu != 1380 || !up {
		t.Errorf("после Start: адреса %v, MTU %d, up %v; ждали %v, 1380, up", addrs, mtu, up, conf.Addresses)
	}
	if got, want := linkRoutes(t, name), sortedRoutes(fullTunnelRoutes); !slices.Equal(got, want) {
		t.Errorf("маршруты после Start: %v, ждали %v", got, want)
	}
	if resolv, _ := os.ReadFile("/etc/resolv.conf"); !strings.Contains(string(resolv), "\nnameserver 8.8.8.8\n") {
		t.Errorf("resolv.conf после Start:\n%s", resolv)
	}
	if err := w.Suspend(); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if got := linkRoutes(t, name); len(got) != 0 {
		t.Errorf("маршруты после Suspend: %v", got)
	}
	if err := w.Suspend(); err != nil {
		t.Errorf("повторный Suspend: %v", err)
	}
	link, _ := netlink.LinkByName(name)
	if w.AdapterID() != uint64(link.Attrs().Index) {
		t.Errorf("AdapterID = %d, индекс интерфейса %d", w.AdapterID(), link.Attrs().Index)
	}

	next := testWGConfig(t, "127.0.0.1:10")
	next.Addresses = []netip.Addr{netip.MustParseAddr("10.7.0.3")}
	next.MTU = 1420
	if err := w.Start(context.Background(), next); err != nil {
		t.Fatalf("повторный Start: %v\n%s", err, rec.dump())
	}
	addrs, mtu, _ = linkAddrs(t, name)
	if !slices.Equal(addrs, next.Addresses) || mtu != 1420 {
		t.Errorf("после перенастройки: адреса %v, MTU %d; ждали %v, 1420", addrs, mtu, next.Addresses)
	}
	if link2, _ := netlink.LinkByName(name); link2 == nil || link2.Attrs().Index != link.Attrs().Index {
		t.Error("перенастройка создала новый интерфейс")
	}
	if got, want := linkRoutes(t, name), sortedRoutes(fullTunnelRoutes); !slices.Equal(got, want) {
		t.Errorf("маршруты после повторного Start: %v, ждали %v", got, want)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if linkExists(name) {
		t.Error("интерфейс остался после Close")
	}
	if resolv, _ := os.ReadFile("/etc/resolv.conf"); string(resolv) != string(origResolv) {
		t.Errorf("resolv.conf после Close:\n%s\nждали прежний:\n%s", resolv, origResolv)
	}
	if _, err := os.Stat("/etc/resolv.conf.qwdtt-cli-backup"); err == nil {
		t.Error("копия resolv.conf осталась после Close")
	}
	t.Logf("лог:\n%s", rec.dump())
}

// MTU меньше 1280 ядро не совмещает с IPv6: адрес и маршруты IPv6 не
// ставятся, о чём есть строки в логе, а подъём не падает.
func TestTunAdapterSmallMTUNetns(t *testing.T) {
	skipOutsideNetns(t)
	const name = "tttest1"
	rec := &recorder{}
	w := NewTun(TunOptions{Name: name, Logf: rec.logf})
	defer w.Close()

	conf := testWGConfig(t, "127.0.0.1:9")
	conf.Addresses = []netip.Addr{netip.MustParseAddr("10.7.0.2"), netip.MustParseAddr("fd00::2")}
	conf.MTU = 1200
	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start: %v\n%s", err, rec.dump())
	}
	addrs, mtu, _ := linkAddrs(t, name)
	if !slices.Equal(addrs, conf.Addresses[:1]) || mtu != 1200 {
		t.Errorf("адреса %v, MTU %d; ждали только IPv4 и 1200", addrs, mtu)
	}
	if !strings.Contains(rec.dump(), "fd00::2 не поставлен") || !strings.Contains(rec.dump(), "маршруты IPv6 не ставятся") {
		t.Errorf("в логе нет строк про IPv6:\n%s", rec.dump())
	}
	if got, want := linkRoutes(t, name), sortedRoutes(ipv4Only(fullTunnelRoutes)); !slices.Equal(got, want) {
		t.Errorf("маршруты при MTU 1200: %v, ждали %v", got, want)
	}
}

// Чужой интерфейс с именем адаптера не удаляется: Start отказывает и
// называет его.
func TestTunAdapterForeignLinkNetns(t *testing.T) {
	skipOutsideNetns(t)
	const name = "tttest2"
	dummy := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}
	if err := netlink.LinkAdd(dummy); err != nil {
		t.Fatal(err)
	}
	defer netlink.LinkDel(dummy)

	w := NewTun(TunOptions{Name: name})
	defer w.Close()
	err := w.Start(context.Background(), testWGConfig(t, "127.0.0.1:9"))
	if err == nil || !strings.Contains(err.Error(), "тип dummy") || !strings.Contains(err.Error(), "ip link del "+name) {
		t.Errorf("Start при чужом интерфейсе: %v", err)
	}
	if !linkExists(name) {
		t.Error("чужой интерфейс удалён")
	}
	// Отказ отпустил блокировку: следующая попытка её получит.
	release, err := acquireTunLock()
	if err != nil {
		t.Fatalf("блокировка после отказа: %v", err)
	}
	release()
}
