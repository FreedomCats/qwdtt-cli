package netguard

import (
	"errors"
	"math/bits"
	"net"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"github.com/FreedomCats/qwdtt-cli/internal/winsys"
)

// Живые проверки из разведки N1 (plan-e2.md); общие с Linux — в
// live_test.go, здесь — платформенные части и порядок байт.

func physicalIndex(t *testing.T) uint32 {
	t.Helper()
	iface, ok, err := winsys.DefaultInterface(false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skip("нет маршрута по умолчанию IPv4")
	}
	t.Logf("физический интерфейс: %d %q", iface.Index, iface.Alias)
	return iface.Index
}

// Отказ должен прийти на connect/send, а не на setsockopt: ошибка setsockopt
// значит, что порядок байт перепутан и индекс превратился в несуществующий.
// Если же сокет дошёл — порядок перепутан на сборке, которая это молча
// игнорирует. Обе ошибки порядка байт этот тест ловит.
func requireUnreachable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("привязка к loopback пропустила сокет наружу")
	}
	if strings.Contains(err.Error(), "netguard:") {
		t.Fatalf("отказ на setsockopt, а не на маршруте: %v", err)
	}
	if !errors.Is(err, windows.WSAENETUNREACH) {
		t.Logf("отказ не WSAENETUNREACH (тоже годится): %v", err)
	}
}

// Ловушка порядка байт напрямую: для IPv4 машинный порядок отвергается,
// для IPv6 — сетевой. На этой машине нет IPv6-маршрута наружу, поэтому
// для IPv6 проверяется только порядок.
func TestByteOrder(t *testing.T) {
	lo := loopbackIndex(t)
	swapped := bits.ReverseBytes32(lo)

	withSocket := func(t *testing.T, network, address string, f func(windows.Handle) error) error {
		c, err := net.ListenPacket(network, address)
		if err != nil {
			t.Skipf("%s %s: %v", network, address, err)
		}
		defer c.Close()
		rc, err := c.(*net.UDPConn).SyscallConn()
		if err != nil {
			t.Fatal(err)
		}
		var setErr error
		if err := rc.Control(func(fd uintptr) { setErr = f(windows.Handle(fd)) }); err != nil {
			t.Fatal(err)
		}
		return setErr
	}

	t.Run("IPv4", func(t *testing.T) {
		if err := withSocket(t, "udp4", "0.0.0.0:0", func(h windows.Handle) error { return bindSocketToInterface4(h, lo) }); err != nil {
			t.Fatalf("сетевой порядок (наш): %v", err)
		}
		// Сырой setsockopt без перестановки байт — то, что было бы при ошибке.
		err := withSocket(t, "udp4", "0.0.0.0:0", func(h windows.Handle) error {
			return windows.SetsockoptInt(h, windows.IPPROTO_IP, 31, int(lo))
		})
		t.Logf("IPv4, машинный порядок: %v", err)
	})
	t.Run("IPv6", func(t *testing.T) {
		if err := withSocket(t, "udp6", "[::]:0", func(h windows.Handle) error { return bindSocketToInterface6(h, lo) }); err != nil {
			t.Fatalf("машинный порядок (наш): %v", err)
		}
		err := withSocket(t, "udp6", "[::]:0", func(h windows.Handle) error { return bindSocketToInterface6(h, swapped) })
		if err == nil {
			t.Fatalf("IPV6_UNICAST_IF=%#x (сетевой порядок) принят", swapped)
		}
		t.Logf("IPv6, сетевой порядок: %v", err)
	})
}
