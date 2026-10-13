// proxy.go — локальный прокси для окна капчи при kill-switch (plan-e3.md, W2).
//
// Сеть окна WebView2 делает msedgewebview2.exe, а не tt, и при kill-switch
// («закрыто») она закрыта. Разрешать его по процессу нельзя: тем же бинарём
// пользуются Teams, Outlook, виджеты, и они пошли бы мимо туннеля. Поэтому
// окно ходит через этот прокси, а он — сокетами tt, которые kill-switch
// пропускает: диалер с хуком netguard и резолвер ядра.
//
// Прокси слушает loopback, и до него может достучаться любой процесс машины.
// Чтобы он не стал дырой в kill-switch, пускает он только CONNECT на порт 443
// доменов VK (proxyDomains) и живёт, пока открыто окно.
package captcha

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// DialFunc — чем прокси соединяется с сайтом: DialContext у net.Dialer.
type DialFunc = func(ctx context.Context, network, address string) (net.Conn, error)

// ErrProxy — прокси для окна не поднялся, окно не открывалось. CLI по нему
// открывает окно без прокси, сняв kill-switch на время капчи.
var ErrProxy = errors.New("прокси для окна капчи не поднялся")

// proxyDomains — куда окну капчи можно ходить: сам домен и его поддомены.
// Страница — id.vk.com (id.vk.ru), статика — st.vk.com (st.vk.ru), её API —
// api.vk.com, login.vk.com; картинки и звук капчи — CDN VK. Реклама и
// сборщики отпечатков, которые страница тоже зовёт (ad.mail.ru,
// openfpcdn.io), капче не нужны — им 403.
var proxyDomains = []string{
	"vk.com",
	"vk.ru",
	"vk.me",
	"userapi.com",
	"vkuser.net",
	"vkuseraudio.net",
	"mycdn.me",
	"vk-cdn.net",
}

// proxyPort — единственный порт, куда пускает прокси: страница капчи вся
// на HTTPS.
const proxyPort = "443"

const (
	// proxyHeaderTimeout — сколько ждать строки CONNECT от клиента.
	proxyHeaderTimeout = 10 * time.Second
	// proxyDialTimeout — сколько ждать соединения с сайтом.
	proxyDialTimeout = 15 * time.Second
)

// proxyAllowed — можно ли окну капчи к host. Адреса вместо имён не
// пускаются: страница VK ходит по именам, а адрес проверить не по чему.
func proxyAllowed(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	for _, domain := range proxyDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// Proxy — локальный HTTP-прокси (только CONNECT) для окна капчи.
type Proxy struct {
	ln   net.Listener
	dial DialFunc
	logf func(string, ...any)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// StartProxy поднимает прокси на 127.0.0.1 со случайным портом. logf
// получает строку на каждый отказ — по ним видно, чего странице не хватило;
// nil — молчать.
func StartProxy(dial DialFunc, logf func(string, ...any)) (*Proxy, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProxy, err)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Proxy{ln: ln, dial: dial, logf: logf, ctx: ctx, cancel: cancel, conns: map[net.Conn]struct{}{}}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

// Addr — адрес прокси, «127.0.0.1:порт».
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// Close закрывает прокси и все соединения через него и ждёт их горутин.
func (p *Proxy) Close() error {
	p.cancel()
	err := p.ln.Close()
	p.mu.Lock()
	for c := range p.conns {
		c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
	return err
}

// track запоминает соединение, чтобы Close его оборвал. false — прокси уже
// закрыт, соединение закрыто здесь же.
func (p *Proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ctx.Err() != nil {
		c.Close()
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.conns, c)
	c.Close()
}

func (p *Proxy) serve() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		if !p.track(c) {
			continue
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer p.untrack(c)
			p.handle(c)
		}()
	}
}

// handle обслуживает одно соединение клиента: строка CONNECT, проверка,
// соединение с сайтом, дальше — байты в обе стороны.
func (p *Proxy) handle(client net.Conn) {
	_ = client.SetReadDeadline(time.Now().Add(proxyHeaderTimeout))
	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	_ = client.SetReadDeadline(time.Time{})

	if req.Method != http.MethodConnect {
		p.logf("[CAPTCHA] прокси: отказ %s %s — только CONNECT", req.Method, req.Host)
		reply(client, http.StatusMethodNotAllowed)
		return
	}
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil || port != proxyPort || !proxyAllowed(host) {
		p.logf("[CAPTCHA] прокси: отказ %s — не домен VK на порту 443", req.Host)
		reply(client, http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(p.ctx, proxyDialTimeout)
	upstream, err := p.dial(ctx, "tcp", req.Host)
	cancel()
	if err != nil {
		p.logf("[CAPTCHA] прокси: %s не соединился: %v", req.Host, err)
		reply(client, http.StatusBadGateway)
		return
	}
	if !p.track(upstream) {
		return
	}
	defer p.untrack(upstream)

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	// Что клиент успел прислать за строкой CONNECT, лежит в br, поэтому
	// к сайту копируется из него, а не из client. Кончилась одна сторона —
	// закрываются обе: TLS поверх полузакрытого соединения не живёт.
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstream); done <- struct{}{} }()
	<-done
	client.Close()
	upstream.Close()
	<-done
}

// reply отвечает клиенту кодом без тела и закрывает разговор.
func reply(c net.Conn, code int) {
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code, http.StatusText(code))
}
