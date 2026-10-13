package netguard

import (
	"context"
	"errors"
	"math/bits"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/FreedomCats/qwdtt-cli/internal/winsys"
)

// Живые проверки из разведки N1 (plan-e2.md). Без прав и без туннеля:
// привязка к loopback-интерфейсу должна не пустить сокет наружу, к
// физическому — пустить, а без хука сокет идёт как обычно. Нет сети —
// t.Skip, а не падение.

const (
	tcpTarget = "1.1.1.1:443"
	udpTarget = "1.1.1.1:53"
)

// DNS-запрос A example.com, 29 байт.
var dnsQuery = []byte{
	0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
	7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0,
	0x00, 0x01, 0x00, 0x01,
}

func loopbackIndex(t *testing.T) uint32 {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 {
			return uint32(iface.Index)
		}
	}
	t.Fatal("нет loopback-интерфейса")
	return 0
}

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

func dialTCP(control func(string, string, syscall.RawConn) error) error {
	d := net.Dialer{Timeout: 4 * time.Second, Control: control}
	c, err := d.DialContext(context.Background(), "tcp", tcpTarget)
	if err == nil {
		c.Close()
	}
	return err
}

func dialUDP(control func(string, string, syscall.RawConn) error) error {
	d := net.Dialer{Timeout: 4 * time.Second, Control: control}
	c, err := d.DialContext(context.Background(), "udp", udpTarget)
	if err != nil {
		return err
	}
	defer c.Close()
	return exchangeDNS(c.(net.PacketConn), nil)
}

// exchangeDNS шлёт запрос и ждёт ответ 3 с. to == nil — сокет уже соединён.
func exchangeDNS(c net.PacketConn, to net.Addr) error {
	c.SetDeadline(time.Now().Add(3 * time.Second))
	var err error
	if to == nil {
		_, err = c.(net.Conn).Write(dnsQuery)
	} else {
		_, err = c.WriteTo(dnsQuery, to)
	}
	if err != nil {
		return err
	}
	buf := make([]byte, 512)
	n, _, err := c.ReadFrom(buf)
	if err != nil {
		return err
	}
	if n < 12 || buf[0] != 0x12 || buf[1] != 0x34 {
		return errors.New("не тот ответ DNS")
	}
	return nil
}

// requireNet пропускает тест, если без хука до 1.1.1.1 не достучаться.
func requireNet(t *testing.T) {
	t.Helper()
	if err := dialTCP(nil); err != nil {
		t.Skipf("без хука TCP не доходит, сети нет: %v", err)
	}
	if err := dialUDP(nil); err != nil {
		t.Skipf("без хука UDP не доходит, сети нет: %v", err)
	}
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

func TestLoopbackIndexBlocks(t *testing.T) {
	requireNet(t)
	var g Guard
	g.SetInterfaces(loopbackIndex(t), 0)
	t.Run("TCP", func(t *testing.T) { requireUnreachable(t, dialTCP(g.Control)) })
	t.Run("UDP", func(t *testing.T) { requireUnreachable(t, dialUDP(g.Control)) })
}

func TestPhysicalIndexPasses(t *testing.T) {
	requireNet(t)
	var g Guard
	g.SetInterfaces(physicalIndex(t), 0)
	if err := dialTCP(g.Control); err != nil {
		t.Errorf("TCP: %v", err)
	}
	if err := dialUDP(g.Control); err != nil {
		t.Errorf("UDP: %v", err)
	}
}

func TestUnknownIndexRefuses(t *testing.T) {
	var g Guard
	err := dialTCP(g.Control)
	if err == nil || !strings.Contains(err.Error(), "нет физического интерфейса") {
		t.Fatalf("ошибка %v", err)
	}
}

// Двухстековый сокет слушающего вида (ListenPacket "udp" ":0") получает
// IP_UNICAST_IF и шлёт на IPv4 через выбранный интерфейс.
func TestDualStackListener(t *testing.T) {
	requireNet(t)
	to, err := net.ResolveUDPAddr("udp", udpTarget)
	if err != nil {
		t.Fatal(err)
	}
	listen := func(index4 uint32) error {
		var g Guard
		g.SetInterfaces(index4, 0)
		lc := net.ListenConfig{Control: g.Control}
		c, err := lc.ListenPacket(context.Background(), "udp", ":0")
		if err != nil {
			return err
		}
		defer c.Close()
		return exchangeDNS(c, to)
	}
	t.Run("loopback", func(t *testing.T) { requireUnreachable(t, listen(loopbackIndex(t))) })
	t.Run("физический", func(t *testing.T) {
		if err := listen(physicalIndex(t)); err != nil {
			t.Fatal(err)
		}
	})
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
