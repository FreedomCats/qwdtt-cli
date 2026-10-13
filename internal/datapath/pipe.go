package datapath

import (
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

// pipeQueue — сколько пакетов ждёт в каждую сторону, пока их не заберут. С
// запасом против буфера сокета, который ядро ставит своему UDP на loopback
// (625 КиБ — около 450 пакетов по 1400 байт): канал в памяти не должен
// сбрасывать раньше, чем сбрасывал сокет.
const pipeQueue = 1024

// Pipe — канал в памяти между wireguard-go и ядром вместо UDP на loopback
// (решение Р4, plan-e3.md, блок B).
//
// Сам Pipe — conn.Bind для wireguard-go; сторону ядра отдаёт CoreConn. Через
// loopback порт ядра был открыт любому процессу машины, а диспетчер ядра
// отвечает последнему отправителю: один посторонний датаграм уводил ответы
// сервера. В памяти постороннему писать некуда.
//
// Время жизни сторон разное. Bind живёт вместе с устройством wireguard-go —
// у tun это весь сеанс, через все попытки; Close и Open он переживает
// (BindUpdate в reconfigureDevice). Сторона ядра — одна попытка: core.Run
// закрывает её на выходе, следующая попытка берёт новую.
//
// Семантика — как у UDP: очередь переполнена или ядра нет — пакет
// сбрасывается, отправитель не ждёт. Сброс от переполнения считается
// (Dropped).
type Pipe struct {
	// toWG — пакеты ядра для wireguard-go. Одна на весь Pipe: сторон ядра
	// много, а читает их один Bind.
	toWG chan []byte
	// core — текущая сторона ядра; nil — ядра нет (до первой попытки).
	core atomic.Pointer[pipeConn]
	// endpoint — последний адрес пира из конфига (ParseEndpoint). Его
	// приёмник отдаёт wireguard-go как адрес отправителя: иначе endpoint
	// пира в IpcGet подменился бы заглушкой.
	endpoint atomic.Pointer[pipeEndpoint]

	mu     sync.Mutex
	closed chan struct{} // закрыт — Bind закрыт; nil — ни разу не открывался

	droppedUp   atomic.Uint64 // wireguard-go → ядро
	droppedDown atomic.Uint64 // ядро → wireguard-go
}

var _ conn.Bind = (*Pipe)(nil)

// NewPipe возвращает Pipe без стороны ядра и с закрытым Bind: открывает его
// wireguard-go (device.Up).
func NewPipe() *Pipe {
	return &Pipe{toWG: make(chan []byte, pipeQueue)}
}

// CoreConn отдаёт новую сторону ядра для core.Config.PacketConn. Прежняя
// закрывается: две попытки разом ядро не держит, а пакет wireguard-go должен
// уйти той, что жива.
func (p *Pipe) CoreConn() net.PacketConn {
	c := &pipeConn{
		pipe:   p,
		in:     make(chan []byte, pipeQueue),
		closed: make(chan struct{}),
	}
	if old := p.core.Swap(c); old != nil {
		_ = old.Close()
	}
	return c
}

// Dropped — сколько пакетов сброшено из-за полной очереди: up — от
// wireguard-go к ядру, down — обратно.
func (p *Pipe) Dropped() (up, down uint64) {
	return p.droppedUp.Load(), p.droppedDown.Load()
}

// Open — conn.Bind. Порт ни к чему не привязывается и возвращается как есть.
func (p *Pipe) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed != nil {
		select {
		case <-p.closed:
		default:
			return nil, 0, conn.ErrBindAlreadyOpen
		}
	}
	closed := make(chan struct{})
	p.closed = closed
	return []conn.ReceiveFunc{p.receiveFunc(closed)}, port, nil
}

// Close — conn.Bind: функции приёма, выданные Open, возвращают
// net.ErrClosed. Повторный вызов безвреден.
func (p *Pipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed != nil {
		select {
		case <-p.closed:
		default:
			close(p.closed)
		}
	}
	return nil
}

// receiveFunc ждёт первый пакет, затем без ожидания добирает, сколько есть,
// до размера пачки.
func (p *Pipe) receiveFunc(closed <-chan struct{}) conn.ReceiveFunc {
	return func(bufs [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		// Закрытие — первым: после Close приём обязан кончаться
		// net.ErrClosed, даже если в очереди остались пакеты.
		select {
		case <-closed:
			return 0, net.ErrClosed
		default:
		}
		var pkt []byte
		select {
		case <-closed:
			return 0, net.ErrClosed
		case pkt = <-p.toWG:
		}
		ep := p.currentEndpoint()
		n := 0
		for {
			sizes[n] = copy(bufs[n], pkt)
			eps[n] = ep
			n++
			if n == len(bufs) {
				return n, nil
			}
			select {
			case pkt = <-p.toWG:
			default:
				return n, nil
			}
		}
	}
}

// Send — conn.Bind: пакеты уходят текущей стороне ядра, адрес ep не нужен —
// пир один. Ядра нет — пакеты молча пропадают, как UDP в закрытый порт.
func (p *Pipe) Send(bufs [][]byte, _ conn.Endpoint) error {
	if p.isClosed() {
		return net.ErrClosed
	}
	c := p.core.Load()
	if c == nil {
		return nil
	}
	for _, b := range bufs {
		if !c.deliver(b) {
			return nil
		}
	}
	return nil
}

func (p *Pipe) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed == nil {
		return true
	}
	select {
	case <-p.closed:
		return true
	default:
		return false
	}
}

// ParseEndpoint — conn.Bind. Endpoint конфига — номинальный loopback ядра
// (сервер возвращает порт, который ядро прислало в GETCONF); он нужен только
// для IpcGet и строк лога.
func (p *Pipe) ParseEndpoint(s string) (conn.Endpoint, error) {
	addr, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	ep := &pipeEndpoint{addr: addr}
	p.endpoint.Store(ep)
	return ep, nil
}

func (p *Pipe) currentEndpoint() conn.Endpoint {
	if ep := p.endpoint.Load(); ep != nil {
		return ep
	}
	return &pipeEndpoint{}
}

// SetMark — conn.Bind. Сокета нет, метить нечего.
func (p *Pipe) SetMark(uint32) error { return nil }

// BatchSize — conn.Bind.
func (p *Pipe) BatchSize() int { return conn.IdealBatchSize }

// pipeEndpoint — адрес пира для wireguard-go. Источника у канала нет.
type pipeEndpoint struct{ addr netip.AddrPort }

func (e *pipeEndpoint) ClearSrc()           {}
func (e *pipeEndpoint) SrcToString() string { return "" }
func (e *pipeEndpoint) DstToString() string { return e.addr.String() }
func (e *pipeEndpoint) DstToBytes() []byte {
	b, _ := e.addr.MarshalBinary()
	return b
}
func (e *pipeEndpoint) DstIP() netip.Addr { return e.addr.Addr() }
func (e *pipeEndpoint) SrcIP() netip.Addr { return netip.Addr{} }

// errPipeDeadline — сроки чтения и записи стороне ядра не нужны: диспетчер
// ядра их не ставит, а останавливается закрытием.
var errPipeDeadline = errors.New("канал в памяти: сроки не поддерживаются")

// pipeConn — сторона ядра: net.PacketConn для диспетчера ядра.
type pipeConn struct {
	pipe      *Pipe
	in        chan []byte // пакеты wireguard-go для ядра
	closed    chan struct{}
	closeOnce sync.Once
}

// deliver кладёт копию пакета wireguard-go в очередь ядра: wireguard-go
// переиспользует буфер, как только Send вернулся. false — сторона закрыта.
func (c *pipeConn) deliver(b []byte) bool {
	select {
	case <-c.closed:
		return false
	default:
	}
	select {
	case c.in <- append([]byte(nil), b...):
	default:
		c.pipe.droppedUp.Add(1)
	}
	return true
}

// ReadFrom отдаёт пакет wireguard-go. Длиннее буфера — обрезается, как
// датаграм UDP.
func (c *pipeConn) ReadFrom(b []byte) (int, net.Addr, error) {
	// Закрытие проверяется первым: пакеты, оставшиеся в очереди закрытой
	// стороны, ядру уже не нужны.
	select {
	case <-c.closed:
		return 0, nil, net.ErrClosed
	default:
	}
	select {
	case <-c.closed:
		return 0, nil, net.ErrClosed
	case pkt := <-c.in:
		return copy(b, pkt), pipeAddr{}, nil
	}
}

// WriteTo отдаёт пакет ядра wireguard-go; адрес не нужен — получатель один.
// Копия — потому что диспетчер возвращает буфер в пул сразу после записи.
func (c *pipeConn) WriteTo(b []byte, _ net.Addr) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	select {
	case c.pipe.toWG <- append([]byte(nil), b...):
	default:
		c.pipe.droppedDown.Add(1)
	}
	return len(b), nil
}

// Close закрывает сторону ядра; Bind и следующая сторона живут.
func (c *pipeConn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		c.pipe.core.CompareAndSwap(c, nil)
	})
	return nil
}

func (c *pipeConn) LocalAddr() net.Addr              { return pipeAddr{} }
func (c *pipeConn) SetDeadline(time.Time) error      { return errPipeDeadline }
func (c *pipeConn) SetReadDeadline(time.Time) error  { return errPipeDeadline }
func (c *pipeConn) SetWriteDeadline(time.Time) error { return errPipeDeadline }

// pipeAddr — адрес обеих сторон канала. Ядро пишет его в лог («первый пакет
// от локального WireGuard с адреса …») и запоминает как адрес клиента.
type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "wireguard-go (в памяти)" }
