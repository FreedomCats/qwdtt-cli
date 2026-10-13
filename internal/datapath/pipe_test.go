package datapath

import (
	"errors"
	"net"
	"testing"
	"time"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
)

// openPipe открывает Bind и возвращает его функцию приёма.
func openPipe(t *testing.T, p *Pipe) conn.ReceiveFunc {
	t.Helper()
	fns, port, err := p.Open(51820)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if len(fns) != 1 || port != 51820 {
		t.Fatalf("Open: %d функций, порт %d", len(fns), port)
	}
	return fns[0]
}

// receive зовёт функцию приёма с пачкой из batch буферов и ждёт её не
// дольше секунды.
func receive(t *testing.T, fn conn.ReceiveFunc, batch int) ([][]byte, []conn.Endpoint, error) {
	t.Helper()
	bufs := make([][]byte, batch)
	for i := range bufs {
		bufs[i] = make([]byte, 2000)
	}
	sizes := make([]int, batch)
	eps := make([]conn.Endpoint, batch)
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := fn(bufs, sizes, eps)
		done <- result{n, err}
	}()
	select {
	case r := <-done:
		out := make([][]byte, r.n)
		for i := range out {
			out[i] = bufs[i][:sizes[i]]
		}
		return out, eps[:r.n], r.err
	case <-time.After(time.Second):
		t.Fatal("функция приёма не вернулась за секунду")
		return nil, nil, nil
	}
}

func readFrom(t *testing.T, c net.PacketConn) (string, net.Addr, error) {
	t.Helper()
	type result struct {
		s    string
		addr net.Addr
		err  error
	}
	done := make(chan result, 1)
	go func() {
		b := make([]byte, 2000)
		n, addr, err := c.ReadFrom(b)
		done <- result{string(b[:n]), addr, err}
	}()
	select {
	case r := <-done:
		return r.s, r.addr, r.err
	case <-time.After(time.Second):
		t.Fatal("ReadFrom не вернулся за секунду")
		return "", nil, nil
	}
}

func TestPipeBothWays(t *testing.T) {
	p := NewPipe()
	recv := openPipe(t, p)
	defer p.Close()
	ep, err := p.ParseEndpoint("127.0.0.1:9000")
	if err != nil {
		t.Fatal(err)
	}
	core := p.CoreConn()
	defer core.Close()

	// wireguard-go → ядро; буфер после Send переиспользуется.
	buf := []byte("handshake")
	if err := p.Send([][]byte{buf, []byte("data")}, ep); err != nil {
		t.Fatalf("Send: %v", err)
	}
	copy(buf, "XXXXXXXXX")
	for _, want := range []string{"handshake", "data"} {
		got, addr, err := readFrom(t, core)
		if err != nil || got != want {
			t.Fatalf("ReadFrom = %q, %v; ожидался %q", got, err, want)
		}
		if addr == nil || addr.String() == "" {
			t.Errorf("ReadFrom: пустой адрес")
		}
	}

	// Ядро → wireguard-go, пачкой; адрес пира — из конфига.
	for _, s := range []string{"one", "two", "three"} {
		b := []byte(s)
		if _, err := core.WriteTo(b, nil); err != nil {
			t.Fatalf("WriteTo: %v", err)
		}
		copy(b, "ZZZ")
	}
	pkts, eps, err := receive(t, recv, 2)
	if err != nil || len(pkts) != 2 || string(pkts[0]) != "one" || string(pkts[1]) != "two" {
		t.Fatalf("приём = %q, %v; ожидались one, two", pkts, err)
	}
	if got := eps[0].DstToString(); got != "127.0.0.1:9000" {
		t.Errorf("адрес отправителя = %s, ожидался endpoint конфига", got)
	}
	pkts, _, err = receive(t, recv, 2)
	if err != nil || len(pkts) != 1 || string(pkts[0]) != "three" {
		t.Fatalf("приём = %q, %v; ожидался three", pkts, err)
	}
}

// BindUpdate — Close и Open: приём прежнего Open кончается net.ErrClosed
// (иначе приёмная горутина wireguard-go выйдет не сразу и с ошибкой),
// следующий Open работает.
func TestPipeCloseOpen(t *testing.T) {
	p := NewPipe()
	if err := p.Send([][]byte{[]byte("x")}, nil); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Send до Open = %v, ожидался net.ErrClosed", err)
	}
	recv := openPipe(t, p)
	if _, _, err := p.Open(0); !errors.Is(err, conn.ErrBindAlreadyOpen) {
		t.Errorf("второй Open = %v, ожидался ErrBindAlreadyOpen", err)
	}

	done := make(chan error, 1)
	go func() {
		bufs, sizes, eps := [][]byte{make([]byte, 100)}, make([]int, 1), make([]conn.Endpoint, 1)
		_, err := recv(bufs, sizes, eps)
		done <- err
	}()
	time.Sleep(20 * time.Millisecond) // приём ждёт пакета
	_ = p.Close()
	_ = p.Close()
	select {
	case err := <-done:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("приём после Close = %v, ожидался net.ErrClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("приём не прервался Close")
	}
	if err := p.Send([][]byte{[]byte("x")}, nil); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Send после Close = %v, ожидался net.ErrClosed", err)
	}

	// Пакет в очереди закрытый Bind не отдаёт — он дождётся следующего Open.
	core := p.CoreConn()
	if _, err := core.WriteTo([]byte("after"), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := receive(t, recv, 1); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("приём закрытого Bind с пакетом в очереди = %v, ожидался net.ErrClosed", err)
	}
	recv = openPipe(t, p)
	defer p.Close()
	pkts, _, err := receive(t, recv, 1)
	if err != nil || len(pkts) != 1 || string(pkts[0]) != "after" {
		t.Fatalf("приём после Open = %q, %v", pkts, err)
	}
}

// Каждая попытка берёт новую сторону ядра: прежняя закрывается, пакеты
// wireguard-go идут новой. Закрытие стороны ядра Bind не трогает.
func TestPipeCoreSwap(t *testing.T) {
	p := NewPipe()
	recv := openPipe(t, p)
	defer p.Close()

	// Ядра ещё нет — пакеты пропадают молча.
	if err := p.Send([][]byte{[]byte("lost")}, nil); err != nil {
		t.Fatalf("Send без ядра: %v", err)
	}

	first := p.CoreConn()
	second := p.CoreConn()
	if _, _, err := readFrom(t, first); !errors.Is(err, net.ErrClosed) {
		t.Errorf("ReadFrom прежней стороны = %v, ожидался net.ErrClosed", err)
	}
	if _, err := first.WriteTo([]byte("x"), nil); !errors.Is(err, net.ErrClosed) {
		t.Errorf("WriteTo прежней стороны = %v, ожидался net.ErrClosed", err)
	}
	if err := p.Send([][]byte{[]byte("fresh")}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _, err := readFrom(t, second); err != nil || got != "fresh" {
		t.Fatalf("ReadFrom новой стороны = %q, %v", got, err)
	}

	// Ядро закрыло свою сторону на выходе из Run — Bind живёт, следующая
	// сторона работает.
	_ = second.Close()
	_ = first.Close() // повторное закрытие уже закрытой не отцепляет новую
	if err := p.Send([][]byte{[]byte("lost")}, nil); err != nil {
		t.Fatalf("Send после закрытия стороны ядра: %v", err)
	}
	third := p.CoreConn()
	defer third.Close()
	if err := p.Send([][]byte{[]byte("third")}, nil); err != nil {
		t.Fatal(err)
	}
	if got, _, err := readFrom(t, third); err != nil || got != "third" {
		t.Fatalf("ReadFrom третьей стороны = %q, %v", got, err)
	}
	if _, err := third.WriteTo([]byte("back"), nil); err != nil {
		t.Fatal(err)
	}
	if pkts, _, err := receive(t, recv, 1); err != nil || len(pkts) != 1 || string(pkts[0]) != "back" {
		t.Fatalf("приём = %q, %v", pkts, err)
	}
}

// Полная очередь — пакет сбрасывается и считается; отправитель не ждёт.
func TestPipeOverflow(t *testing.T) {
	p := NewPipe()
	_ = openPipe(t, p)
	defer p.Close()
	core := p.CoreConn()
	defer core.Close()

	for i := 0; i < pipeQueue+3; i++ {
		if err := p.Send([][]byte{[]byte("up")}, nil); err != nil {
			t.Fatal(err)
		}
		if n, err := core.WriteTo([]byte("down"), nil); err != nil || n != 4 {
			t.Fatalf("WriteTo = %d, %v", n, err)
		}
	}
	if up, down := p.Dropped(); up != 3 || down != 3 {
		t.Errorf("Dropped = %d, %d; ожидалось 3, 3", up, down)
	}
}

// Устройство wireguard-go на Pipe договаривается с настоящим сервером, если
// сторона ядра пересылает пакеты в UDP, — и снова, после смены стороны ядра
// и перенастройки, как у tun между попытками.
func TestPipeWireGuard(t *testing.T) {
	clientPriv, clientPub := testKeyPair(t)
	serverPriv, serverPub := testKeyPair(t)
	serverPort := startTestServer(t, serverPriv, clientPub)

	pipe := NewPipe()
	// Сторона ядра — до устройства, как в супервизоре: core.Run стартует
	// раньше datapath.
	stopRelay := relayToUDP(t, pipe.CoreConn(), serverPort)

	conf := testWGConfig(t, "127.0.0.1:9000")
	conf.PrivateKeyHex, conf.PeerPublicHex = clientPriv, serverPub
	conf.Keepalive = 1
	dev := device.NewDevice(newTestTUN(t, conf), pipe, &device.Logger{
		Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf,
	})
	defer dev.Close()
	if err := dev.IpcSet(conf.IPCRequest()); err != nil {
		t.Fatal(err)
	}
	if err := dev.Up(); err != nil {
		t.Fatal(err)
	}
	if !waitHandshake(dev, 10*time.Second) {
		state, _ := dev.IpcGet()
		t.Fatalf("рукопожатие через Pipe не прошло:\n%s", state)
	}

	// Попытка кончилась: ядро закрыло свою сторону, Suspend снял пира.
	stopRelay()
	dev.RemoveAllPeers()
	stopRelay = relayToUDP(t, pipe.CoreConn(), serverPort)
	defer stopRelay()
	if err := reconfigureDevice(dev, conf); err != nil {
		t.Fatalf("reconfigureDevice: %v", err)
	}
	if !waitHandshake(dev, 10*time.Second) {
		state, _ := dev.IpcGet()
		t.Fatalf("рукопожатие после смены стороны ядра не прошло:\n%s", state)
	}
}

// relayToUDP изображает ядро: пересылает пакеты стороны ядра серверу на
// loopback и ответы обратно. Возвращает остановку, закрывающую сторону ядра.
func relayToUDP(t *testing.T, core net.PacketConn, serverPort int) func() {
	t.Helper()
	udp, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	server := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: serverPort}
	go func() {
		b := make([]byte, 2000)
		for {
			n, _, err := core.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = udp.WriteTo(b[:n], server)
		}
	}()
	go func() {
		b := make([]byte, 2000)
		for {
			n, _, err := udp.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = core.WriteTo(b[:n], nil)
		}
	}()
	var stopped bool
	return func() {
		if stopped {
			return
		}
		stopped = true
		_ = core.Close()
		_ = udp.Close()
	}
}
