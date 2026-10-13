package netguard

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"
)

// fakeConn — syscall.RawConn, у которого Control просто зовёт функцию.
type fakeConn struct{}

func (fakeConn) Control(f func(fd uintptr)) error  { f(42); return nil }
func (fakeConn) Read(func(fd uintptr) bool) error  { return errors.New("не нужен") }
func (fakeConn) Write(func(fd uintptr) bool) error { return errors.New("не нужен") }

var _ syscall.RawConn = fakeConn{}

// recorder запоминает вызовы setter: "4=12", "6=15".
type recorder struct {
	calls []string
	fail  error
}

func (r *recorder) set(fd uintptr, ipv6 bool, index uint32) error {
	family := "4"
	if ipv6 {
		family = "6"
	}
	r.calls = append(r.calls, fmt.Sprintf("%s=%d", family, index))
	return r.fail
}

func TestApply(t *testing.T) {
	for _, tc := range []struct {
		name             string
		index4, index6   uint32
		network, address string
		want             string // вызовы через запятую
		wantErr          string // подстрока ошибки
		// wantSingle — вызовы там, где привязка одна на сокет (Linux,
		// bindPerFamily = false); пусто — как want.
		wantSingle string
	}{
		{"tcp4", 12, 15, "tcp4", "203.0.113.80:443", "4=12", "", ""},
		{"udp4", 12, 15, "udp4", "203.0.113.165:3478", "4=12", "", ""},
		{"tcp6", 12, 15, "tcp6", "[2001:db8:bd::1]:443", "6=15", "", ""},
		{"udp6 с зоной", 12, 15, "udp6", "[fe80::1%3]:53", "6=15", "", ""},
		{"loopback IPv4", 12, 15, "udp4", "127.0.0.1:9000", "", "", ""},
		{"loopback IPv6", 12, 15, "tcp6", "[::1]:9000", "", "", ""},
		{"loopback, отображённый в IPv6", 12, 15, "udp6", "[::ffff:127.0.0.1]:9000", "", "", ""},
		{"нет IPv4", 0, 15, "tcp4", "1.1.1.1:443", "", "нет физического интерфейса для IPv4", ""},
		{"нет IPv6", 12, 0, "tcp6", "[2606:4700::1]:443", "", "нет физического интерфейса для IPv6", ""},
		{"ничего не выбрано", 0, 0, "udp4", "1.1.1.1:53", "", "сокет udp4 1.1.1.1:53 не создаётся", ""},
		{"двухстековый слушатель", 12, 15, "udp6", "[::]:0", "4=12,6=15", "", "4=12"},
		{"двухстековый, пустой хост", 12, 15, "udp6", ":0", "4=12,6=15", "", "4=12"},
		{"двухстековый на 0.0.0.0", 12, 15, "udp6", "0.0.0.0:0", "4=12,6=15", "", "4=12"},
		{"двухстековый без IPv6", 12, 0, "udp6", "[::]:0", "4=12", "", ""},
		{"двухстековый без IPv4", 0, 15, "udp6", "[::]:0", "6=15", "", ""},
		{"двухстековый, ничего не выбрано", 0, 0, "udp6", "[::]:0", "", "для IPv4 и IPv6", ""},
		{"IPv4-слушатель на 0.0.0.0", 12, 15, "udp4", "0.0.0.0:0", "4=12", "", ""},
		{"голая сеть", 12, 15, "tcp", "1.1.1.1:443", "", "не понять семейство", ""},
		{"адрес без порта", 12, 15, "tcp4", "1.1.1.1", "", "не разобрать адрес", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var g Guard
			g.SetInterfaces(tc.index4, tc.index6)
			var r recorder
			err := g.apply(tc.network, tc.address, fakeConn{}, r.set)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ошибка %v, ждали с %q", err, tc.wantErr)
				}
				if len(r.calls) != 0 {
					t.Fatalf("при ошибке setter вызван: %v", r.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := tc.want
			if !bindPerFamily && tc.wantSingle != "" {
				want = tc.wantSingle
			}
			if got := strings.Join(r.calls, ","); got != want {
				t.Fatalf("вызовы %q, ждали %q", got, want)
			}
		})
	}
}

func TestApplySetterError(t *testing.T) {
	var g Guard
	g.SetInterfaces(12, 15)
	r := recorder{fail: errors.New("WSAEINVAL")}
	err := g.apply("udp6", "[::]:0", fakeConn{}, r.set)
	if err == nil || !strings.Contains(err.Error(), option4+"=12") || !strings.Contains(err.Error(), "WSAEINVAL") {
		t.Fatalf("ошибка %v", err)
	}
	// После отказа первой опции вторая не ставится.
	if len(r.calls) != 1 {
		t.Fatalf("вызовы %v", r.calls)
	}
}

func TestSetInterfacesOnTheFly(t *testing.T) {
	var g Guard
	if a, b := g.Interfaces(); a != 0 || b != 0 {
		t.Fatalf("нулевой Guard: %d, %d", a, b)
	}
	g.SetInterfaces(3, 0)
	g.SetInterfaces(7, 9)
	var r recorder
	if err := g.apply("tcp4", "1.1.1.1:443", fakeConn{}, r.set); err != nil {
		t.Fatal(err)
	}
	if r.calls[0] != "4=7" {
		t.Fatalf("вызовы %v", r.calls)
	}
}

func TestLogf(t *testing.T) {
	var lines []string
	g := Guard{Logf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }}
	g.SetInterfaces(3, 0)
	var r recorder
	if err := g.apply("tcp4", "203.0.113.80:443", fakeConn{}, r.set); err != nil {
		t.Fatal(err)
	}
	// Loopback и отказ строк не дают.
	_ = g.apply("udp4", "127.0.0.1:9000", fakeConn{}, r.set)
	_ = g.apply("tcp6", "[2a00::1]:443", fakeConn{}, r.set)
	if len(lines) != 1 || lines[0] != "[NETGUARD] tcp4 203.0.113.80:443 → интерфейс 3" {
		t.Fatalf("строки %q", lines)
	}
}
