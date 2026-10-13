package winsys

import (
	"net/netip"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// Подписка видит изменение маршрута и называет его интерфейс. Маршрут —
// на TEST-NET-2 (198.51.100.0/24, RFC 5737), туда никто не ходит, так что
// сеть машины тест не задевает. Ставить маршруты можно только с правами
// администратора, без них тест пропускается.
func TestWatchNetworkSeesRouteChange(t *testing.T) {
	iface, ok, err := DefaultInterface(false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skip("нет маршрута по умолчанию IPv4")
	}

	seen := make(chan uint64, 64)
	stop, err := WatchNetwork(func(luid uint64) {
		select {
		case seen <- luid:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	luid := winipcfg.LUID(iface.LUID)
	prefix := netip.MustParsePrefix("198.51.100.0/24")
	if err := luid.AddRoute(prefix, netip.IPv4Unspecified(), 9999); err != nil {
		if err == windows.ERROR_ACCESS_DENIED {
			t.Skipf("маршрут не ставится (нужны права администратора): %v", err)
		}
		t.Fatalf("AddRoute: %v", err)
	}
	removed := false
	defer func() {
		if !removed {
			_ = luid.DeleteRoute(prefix, netip.IPv4Unspecified())
		}
	}()

	expect := func(what string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case got := <-seen:
				if got == iface.LUID {
					return
				}
			case <-deadline:
				t.Fatalf("%s: события с LUID %s не дождались", what, iface.Alias)
			}
		}
	}
	expect("маршрут добавлен")

	if err := luid.DeleteRoute(prefix, netip.IPv4Unspecified()); err != nil {
		t.Fatalf("DeleteRoute: %v", err)
	}
	removed = true
	expect("маршрут снят")

	// После stop событий нет.
	stop()
	for len(seen) > 0 {
		<-seen
	}
	if err := luid.AddRoute(prefix, netip.IPv4Unspecified(), 9999); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	removed = false
	select {
	case got := <-seen:
		t.Fatalf("событие после stop: LUID %d", got)
	case <-time.After(300 * time.Millisecond):
	}
}
