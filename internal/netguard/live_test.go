//go:build windows || linux

package netguard

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Общее для живых проверок привязки на Windows и Linux. Без прав и без
// туннеля: привязка к loopback-интерфейсу должна не пустить сокет наружу,
// к физическому — пустить, а без хука сокет идёт как обычно. Нет сети —
// t.Skip, а не падение. Платформенные части — physicalIndex
// и requireUnreachable.

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

// Двухстековый сокет слушающего вида (ListenPacket "udp" ":0") шлёт на IPv4
// через выбранный интерфейс; привязка к loopback его не выпускает.
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
