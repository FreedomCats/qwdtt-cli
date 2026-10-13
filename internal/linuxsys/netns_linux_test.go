package linuxsys

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// netnsEnv включает проверки, которые меняют сеть: заводят интерфейс
// и маршрут по умолчанию. Только в отдельном сетевом пространстве
// (контейнер с NET_ADMIN, plan-e4.md, «Где гонять живые прогоны»), не на
// машине разработчика:
//
//	CGO_ENABLED=0 go test -c -o build/out/linuxsys.test ./internal/linuxsys
//	docker run --rm --cap-add NET_ADMIN -e TT_TEST_NETNS=1 \
//	  -v "$PWD/build/out":/tt:ro ubuntu:latest /tt/linuxsys.test -test.v
const netnsEnv = "TT_TEST_NETNS"

// Новый интерфейс с маршрутом по умолчанию лучше прежнего: подписка
// приносит события с его индексом, выбор переходит на него, а после
// удаления — обратно.
func TestWatchNetworkEventsNetns(t *testing.T) {
	if os.Getenv(netnsEnv) != "1" || os.Geteuid() != 0 {
		t.Skipf("меняет сеть: только в контейнере, %s=1 и root", netnsEnv)
	}
	before, ok, err := DefaultInterface(false, 0)
	if err != nil || !ok {
		t.Fatalf("исходный выбор: %+v %v %v", before, ok, err)
	}
	demoteDefault(t, before.Index)

	var mu sync.Mutex
	seen := map[uint64]int{}
	stop, err := WatchNetwork(func(index uint64) {
		mu.Lock()
		seen[index]++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "ttwatch0"}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	defer netlink.LinkDel(link)
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	addr, _ := netlink.ParseAddr("192.0.2.2/24")
	if err := netlink.AddrAdd(link, addr); err != nil {
		t.Fatal(err)
	}
	index := link.Attrs().Index
	route := &netlink.Route{LinkIndex: index, Gw: net.ParseIP("192.0.2.1"), Priority: 1}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := seen[uint64(index)]
		mu.Unlock()
		if n >= 3 { // линк, адрес, маршрут — хотя бы по событию
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("событий с индексом %d: %d, все: %v", index, n, seen)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Logf("события: %v", seen)

	now, ok, err := DefaultInterface(false, 0)
	if err != nil || !ok || now.Index != uint32(index) {
		t.Fatalf("выбор после маршрута с метрикой 1: %+v %v %v, ждали %d", now, ok, err, index)
	}
	if other, _, _ := DefaultInterface(false, uint64(index)); other.Index != before.Index {
		t.Errorf("с исключением выбран %+v, ждали прежний %+v", other, before)
	}

	if err := netlink.LinkSetDown(link); err != nil {
		t.Fatal(err)
	}
	if down, _, _ := DefaultInterface(false, 0); down.Index != before.Index {
		t.Errorf("опущенный интерфейс всё ещё выбран: %+v", down)
	}
}

// demoteDefault переносит маршрут по умолчанию через index на метрику 100:
// у docker он с метрикой 0, и новому маршруту было бы его не обойти.
func demoteDefault(t *testing.T, index uint32) {
	t.Helper()
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if !isDefault(r) || r.LinkIndex != int(index) || r.Priority != 0 {
			continue
		}
		demoted := r
		demoted.Priority = 100
		if err := netlink.RouteAdd(&demoted); err != nil {
			t.Fatal(err)
		}
		if err := netlink.RouteDel(&r); err != nil {
			t.Fatal(err)
		}
		return
	}
}

// Защита DNS на настоящих nftables: что пропускает и что отбрасывает
// (отброшенное на выходе — EPERM при отправке), повторное открытие при
// живой таблице, закрытие IPv6 и исчезновение таблицы с соединением.
// Резолвер туннеля — 192.0.2.53, чужой — 198.51.100.1; ответы не нужны.
// TCP проверяется на шлюзе контейнера: он сразу отказывает, а отброшенный
// SYN ядро повторяет, и соединение уходит в таймаут.
//
// Для части про IPv6 контейнеру нужен IPv6:
// --sysctl net.ipv6.conf.all.disable_ipv6=0.
func TestDNSGuardNetns(t *testing.T) {
	if os.Getenv(netnsEnv) != "1" || os.Geteuid() != 0 {
		t.Skipf("меняет сеть: только в контейнере, %s=1 и root", netnsEnv)
	}
	const mark = 0x1074
	tunnelDNS := netip.MustParseAddr("192.0.2.53")

	g, err := OpenDNSGuard(mark)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if !dnsGuardTableExists(t) {
		t.Fatal("после OpenDNSGuard таблицы inet tt нет")
	}
	if _, err := OpenDNSGuard(mark); err == nil || !strings.Contains(err.Error(), "уже работает другой tt") {
		t.Errorf("второе открытие при живой таблице: %v", err)
	}

	// Пустые цепочки ничего не запрещают.
	expectSend(t, "udp", "198.51.100.1:53", 0, nil)

	if err := g.Enable([]netip.Addr{tunnelDNS}); err != nil {
		t.Fatal(err)
	}
	expectSend(t, "udp", "198.51.100.1:53", 0, unix.EPERM)
	expectSend(t, "udp", "192.0.2.53:53", 0, nil)
	expectSend(t, "udp", "127.0.0.1:53", 0, nil)
	expectSend(t, "udp", "198.51.100.1:443", 0, nil)
	expectSend(t, "udp", "198.51.100.1:53", mark, nil)
	expectSend(t, "udp", "198.51.100.1:53", mark+1, unix.EPERM)
	gw := defaultGateway(t)
	expectTCP(t, gw, 0, true)
	expectTCP(t, gw, mark, false)

	// Повторный Enable заменяет правила, а не добавляет.
	if err := g.Enable([]netip.Addr{netip.MustParseAddr("198.51.100.1")}); err != nil {
		t.Fatal(err)
	}
	expectSend(t, "udp", "198.51.100.1:53", 0, nil)
	expectSend(t, "udp", "192.0.2.53:53", 0, unix.EPERM)

	if err := g.Disable(); err != nil {
		t.Fatal(err)
	}
	expectSend(t, "udp", "192.0.2.53:53", 0, nil)
	expectTCP(t, gw, 0, false)
	if err := g.Disable(); err != nil {
		t.Errorf("повторный Disable: %v", err)
	}

	if ipv6Dummy(t) {
		g.BlockIPv6(true)
		if err := g.Enable([]netip.Addr{tunnelDNS}); err != nil {
			t.Fatal(err)
		}
		expectSend(t, "udp", "[2001:db8:1::5]:443", 0, unix.EPERM)
		expectSend(t, "udp", "[2001:db8:1::5]:443", mark, nil)
		expectSend(t, "udp", "[::1]:443", 0, nil)
		expectSend(t, "udp", "192.0.2.53:53", 0, nil)
		g.BlockIPv6(false)
		if err := g.Enable([]netip.Addr{tunnelDNS}); err != nil {
			t.Fatal(err)
		}
		expectSend(t, "udp", "[2001:db8:1::5]:443", 0, nil)
	}

	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if dnsGuardTableExists(t) {
		t.Error("после Close таблица inet tt осталась")
	}
	expectSend(t, "udp", "198.51.100.1:53", 0, nil)
	if err := g.Enable([]netip.Addr{tunnelDNS}); err == nil {
		t.Error("Enable после Close прошёл")
	}
	if err := g.Close(); err != nil {
		t.Errorf("повторный Close: %v", err)
	}
}

// Kill-switch на настоящих nftables: в «закрыто» публичный адрес — EPERM,
// локальная сеть, loopback, сокеты tt и DHCP проходят, DNS не проходит и к
// локальной сети; переходы в защиту DNS и обратно — без пустой таблицы.
// Локальная сеть — 192.168.1.5 (уходит по маршруту по умолчанию) и шлюз
// контейнера (сеть docker — 172.16/12).
func TestKillSwitchNetns(t *testing.T) {
	if os.Getenv(netnsEnv) != "1" || os.Geteuid() != 0 {
		t.Skipf("меняет сеть: только в контейнере, %s=1 и root", netnsEnv)
	}
	const mark = 0x1074
	g, err := OpenDNSGuard(mark)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	if err := g.Lockdown(); err != nil {
		t.Fatal(err)
	}
	expectSend(t, "udp", "198.51.100.1:443", 0, unix.EPERM)
	expectSend(t, "udp", "198.51.100.1:443", mark, nil)
	expectSend(t, "udp", "192.168.1.5:443", 0, nil)
	expectSend(t, "udp", "192.168.1.5:53", 0, unix.EPERM)
	expectSend(t, "udp", "127.0.0.1:53", 0, nil)
	expectSend(t, "udp", "224.0.0.251:5353", 0, nil)
	expectSendFrom(t, 68, "198.51.100.1:67", nil)
	expectSendFrom(t, 69, "198.51.100.1:67", unix.EPERM)
	gw := defaultGateway(t)
	expectTCP(t, gw, 0, true)
	expectTCP(t, gw, mark, false)
	if err := g.Lockdown(); err != nil {
		t.Errorf("повторный Lockdown: %v", err)
	}

	if ipv6Dummy(t) {
		expectSend(t, "udp", "[2001:db8:1::5]:443", 0, unix.EPERM)
		expectSend(t, "udp", "[2001:db8:1::5]:443", mark, nil)
		expectSend(t, "udp", "[::1]:443", 0, nil)
		expectSend(t, "udp", "[ff02::fb%ttdns0]:5353", 0, nil)
		expectICMPv6(t, "2001:db8:1::5", 128, unix.EPERM) // echo request
		expectICMPv6(t, "2001:db8:1::5", 135, nil)        // neighbor solicitation
	}

	// Из «закрыто» в защиту DNS: публичный адрес открыт, DNS — только
	// резолверу туннеля.
	if err := g.Enable([]netip.Addr{netip.MustParseAddr("192.0.2.53")}); err != nil {
		t.Fatal(err)
	}
	expectSend(t, "udp", "198.51.100.1:443", 0, nil)
	expectSend(t, "udp", "198.51.100.1:53", 0, unix.EPERM)
	expectSend(t, "udp", "192.0.2.53:53", 0, nil)

	// И обратно: резолвер туннеля закрыт вместе со всем.
	if err := g.Lockdown(); err != nil {
		t.Fatal(err)
	}
	expectSend(t, "udp", "192.0.2.53:53", 0, unix.EPERM)
	expectSend(t, "udp", "198.51.100.1:443", 0, unix.EPERM)

	if err := g.Disable(); err != nil {
		t.Fatal(err)
	}
	expectSend(t, "udp", "198.51.100.1:443", 0, nil)
	expectSend(t, "udp", "192.168.1.5:53", 0, nil)

	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.Lockdown(); err == nil {
		t.Error("Lockdown после Close прошёл")
	}
}

// expectSendFrom отправляет пакет UDP на address с порта источника port.
func expectSendFrom(t *testing.T, port int, address string, want error) {
	t.Helper()
	d := &net.Dialer{LocalAddr: &net.UDPAddr{Port: port}}
	conn, err := d.Dial("udp4", address)
	if err == nil {
		_, err = conn.Write([]byte("tt"))
		conn.Close()
	}
	switch {
	case want == nil && err != nil:
		t.Errorf("udp :%d → %s: %v, ждали успех", port, address, err)
	case want != nil && !errors.Is(err, want):
		t.Errorf("udp :%d → %s: %v, ждали %v", port, address, err, want)
	}
}

// expectICMPv6 отправляет на address сообщение ICMPv6 типа typ (контрольную
// сумму считает ядро); тело не важно — пакет проверяется на выходе.
func expectICMPv6(t *testing.T, address string, typ byte, want error) {
	t.Helper()
	conn, err := net.ListenPacket("ip6:ipv6-icmp", "::")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg := make([]byte, 24)
	msg[0] = typ
	_, err = conn.WriteTo(msg, &net.IPAddr{IP: net.ParseIP(address)})
	switch {
	case want == nil && err != nil:
		t.Errorf("icmpv6 тип %d → %s: %v, ждали успех", typ, address, err)
	case want != nil && !errors.Is(err, want):
		t.Errorf("icmpv6 тип %d → %s: %v, ждали %v", typ, address, err, want)
	}
}

// expectSend отправляет пакет на address сокетом с меткой mark (0 — без
// неё) и сверяет ошибку отправки с want: отброшенный на выходе пакет UDP —
// EPERM при отправке.
func expectSend(t *testing.T, network, address string, mark int, want error) {
	t.Helper()
	conn, err := markedDialer(mark).Dial(network, address)
	if err == nil {
		_, err = conn.Write([]byte("tt"))
		conn.Close()
	}
	switch {
	case want == nil && err != nil:
		t.Errorf("%s %s, метка %#x: %v, ждали успех", network, address, mark, err)
	case want != nil && !errors.Is(err, want):
		t.Errorf("%s %s, метка %#x: %v, ждали %v", network, address, mark, err, want)
	}
}

// expectTCP соединяется с портом 53 шлюза gw: dropped — SYN отброшен, и
// соединение ждёт до таймаута; иначе шлюз отвечает сразу (успех или отказ).
func expectTCP(t *testing.T, gw netip.Addr, mark int, dropped bool) {
	t.Helper()
	address := netip.AddrPortFrom(gw, 53).String()
	conn, err := markedDialer(mark).Dial("tcp", address)
	if err == nil {
		conn.Close()
	}
	var ne net.Error
	timeout := errors.As(err, &ne) && ne.Timeout()
	if timeout != dropped {
		t.Errorf("tcp %s, метка %#x: %v; ждали, что SYN отброшен: %v", address, mark, err, dropped)
	}
}

func markedDialer(mark int) *net.Dialer {
	d := &net.Dialer{Timeout: 2 * time.Second}
	if mark != 0 {
		d.Control = func(_, _ string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, mark)
			}); err != nil {
				return err
			}
			return serr
		}
	}
	return d
}

// defaultGateway — шлюз маршрута по умолчанию IPv4 (в контейнере — docker0).
func defaultGateway(t *testing.T) netip.Addr {
	t.Helper()
	routes, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range routes {
		if isDefault(r) && r.Gw != nil {
			gw, _ := netip.AddrFromSlice(r.Gw.To4())
			return gw
		}
	}
	t.Fatal("нет маршрута по умолчанию со шлюзом")
	return netip.Addr{}
}

func dnsGuardTableExists(t *testing.T) bool {
	t.Helper()
	conn, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	tables, err := conn.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if table.Name == dnsGuardTable {
			return true
		}
	}
	return false
}

// ipv6Dummy заводит интерфейс с 2001:db8:1::2/64, чтобы пакетам IPv6 было
// куда уйти; false — IPv6 в контейнере выключен, проверка пропускается.
func ipv6Dummy(t *testing.T) bool {
	t.Helper()
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "ttdns0"}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { netlink.LinkDel(link) })
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	addr, _ := netlink.ParseAddr("2001:db8:1::2/64")
	addr.Flags = unix.IFA_F_NODAD
	if err := netlink.AddrAdd(link, addr); err != nil {
		t.Logf("IPv6 не проверен: %v (нужен --sysctl net.ipv6.conf.all.disable_ipv6=0)", err)
		return false
	}
	return true
}
