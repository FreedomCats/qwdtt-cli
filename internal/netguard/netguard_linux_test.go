package netguard

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/FreedomCats/qwdtt-cli/internal/linuxsys"
)

// Живые проверки Linux (plan-e4.md, врезка N1); общие с Windows — в
// live_test.go. Без прав: привязка их не требует, метка — требует.

func physicalIndex(t *testing.T) uint32 {
	t.Helper()
	iface, ok, err := linuxsys.DefaultInterface(false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Skip("нет маршрута по умолчанию IPv4")
	}
	t.Logf("физический интерфейс: %d %q", iface.Index, iface.Name)
	return iface.Index
}

// Привязанный к loopback сокет наружу не выходит: TCP — таймаут, UDP —
// ответа нет (N1, п. 1). Отказ setsockopt — это ошибка привязки, а не
// маршрута.
func requireUnreachable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("привязка к loopback пропустила сокет наружу")
	}
	if strings.Contains(err.Error(), "netguard:") {
		t.Fatalf("отказ на setsockopt, а не на маршруте: %v", err)
	}
}

// Индекс, который ставит setter, ядро возвращает тем же getsockopt.
func TestSetterBindsToIndex(t *testing.T) {
	index := physicalIndex(t)
	c, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rc, err := c.(*net.UDPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var g Guard
	var setErr, getErr error
	var got int
	err = rc.Control(func(fd uintptr) {
		if setErr = g.setter()(fd, false, index); setErr != nil {
			return
		}
		got, getErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_BINDTOIFINDEX)
	})
	if err != nil || setErr != nil || getErr != nil {
		t.Fatalf("Control: %v, setter: %v, getsockopt: %v", err, setErr, getErr)
	}
	if uint32(got) != index {
		t.Fatalf("SO_BINDTOIFINDEX = %d, ставили %d", got, index)
	}
}

// IPv6 с привязкой к интерфейсу, где нет маршрута IPv6 наружу, отказывает
// сразу, а не уходит в другой интерфейс (N1, п. 5).
func TestFamilyWithoutRouteRefuses(t *testing.T) {
	if _, ok, err := linuxsys.DefaultInterface(true, 0); err != nil || ok {
		t.Skipf("у машины есть маршрут IPv6 по умолчанию (или ошибка %v)", err)
	}
	var g Guard
	g.SetInterfaces(physicalIndex(t), physicalIndex(t))
	d := net.Dialer{Timeout: 4 * time.Second, Control: g.Control}
	start := time.Now()
	c, err := d.DialContext(context.Background(), "tcp6", "[2606:4700:4700::1111]:443")
	if err == nil {
		c.Close()
		t.Fatal("IPv6 без маршрута соединился")
	}
	if !errors.Is(err, unix.ENETUNREACH) {
		t.Errorf("ждали ENETUNREACH: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("отказ через %v, ждали сразу", d)
	}
}

// Метка без CAP_NET_ADMIN — EPERM, и сокет не создаётся: поэтому CLI ставит
// Mark, только когда защита DNS включена (под sudo). Под root — метка
// ставится и читается обратно.
func TestMark(t *testing.T) {
	index := physicalIndex(t)
	g := Guard{Mark: SocketMark}
	g.SetInterfaces(index, 0)
	lc := net.ListenConfig{Control: g.Control}
	c, err := lc.ListenPacket(context.Background(), "udp4", "0.0.0.0:0")
	if os.Geteuid() != 0 {
		if err == nil {
			c.Close()
			t.Fatal("метка без прав поставлена")
		}
		if !errors.Is(err, unix.EPERM) || !strings.Contains(err.Error(), "SO_MARK") {
			t.Fatalf("ждали EPERM на SO_MARK: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	rc, _ := c.(*net.UDPConn).SyscallConn()
	var mark int
	var getErr error
	_ = rc.Control(func(fd uintptr) { mark, getErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK) })
	if getErr != nil || uint32(mark) != SocketMark {
		t.Fatalf("SO_MARK = %#x (%v), ставили %#x", mark, getErr, SocketMark)
	}
}
