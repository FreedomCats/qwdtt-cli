package linuxsys

import (
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
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
