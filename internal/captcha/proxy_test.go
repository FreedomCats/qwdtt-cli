package captcha

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProxyAllowed(t *testing.T) {
	cases := map[string]bool{
		"id.vk.com":          true,
		"ID.VK.COM.":         true,
		"vk.com":             true,
		"st.vk.ru":           true,
		"sun9-1.userapi.com": true,
		"evilvk.com":         false,
		"vk.com.evil.org":    false,
		"ad.mail.ru":         false,
		"m1.openfpcdn.io":    false,
		"203.0.113.1":        false,
		"2001:db8::1":        false,
		"":                   false,
	}
	for host, want := range cases {
		if got := proxyAllowed(host); got != want {
			t.Errorf("proxyAllowed(%q) = %v, ожидалось %v", host, got, want)
		}
	}
}

// echoServer — сайт за прокси: возвращает всё, что получил.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

// recordingDial соединяется с echo вместо настоящего адреса и запоминает,
// куда просили.
type recordingDial struct {
	echo string
	err  error

	mu    sync.Mutex
	asked []string
}

func (d *recordingDial) dial(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.asked = append(d.asked, network+" "+address)
	d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	var nd net.Dialer
	return nd.DialContext(ctx, "tcp4", d.echo)
}

func (d *recordingDial) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.asked...)
}

func startTestProxy(t *testing.T, d *recordingDial) (*Proxy, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var logged []string
	p, err := StartProxy(d.dial, func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, format)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, &logged
}

// ask шлёт прокси запрос и читает код ответа.
func ask(t *testing.T, p *Proxy, request string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	c, err := net.Dial("tcp4", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, request); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("ответ прокси: %v", err)
	}
	return c, br, resp.StatusCode
}

func TestProxyConnect(t *testing.T) {
	d := &recordingDial{echo: echoServer(t)}
	p, _ := startTestProxy(t, d)

	// Байты сразу за строкой CONNECT (так шлют клиенты, не дожидаясь 200)
	// должны дойти до сайта.
	c, br, code := ask(t, p, "CONNECT id.vk.com:443 HTTP/1.1\r\nHost: id.vk.com:443\r\n\r\nhello ")
	if code != http.StatusOK {
		t.Fatalf("код %d, ожидался 200", code)
	}
	if _, err := io.WriteString(c, "world"); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("hello world"))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello world" {
		t.Errorf("через прокси пришло %q", got)
	}
	if calls := d.calls(); len(calls) != 1 || calls[0] != "tcp id.vk.com:443" {
		t.Errorf("диалер позван так: %q", calls)
	}
}

func TestProxyRefuses(t *testing.T) {
	cases := []struct {
		name    string
		request string
		code    int
	}{
		{"чужой домен", "CONNECT ad.mail.ru:443 HTTP/1.1\r\nHost: ad.mail.ru:443\r\n\r\n", http.StatusForbidden},
		{"порт не 443", "CONNECT id.vk.com:80 HTTP/1.1\r\nHost: id.vk.com:80\r\n\r\n", http.StatusForbidden},
		{"адрес вместо имени", "CONNECT 203.0.113.1:443 HTTP/1.1\r\nHost: 203.0.113.1:443\r\n\r\n", http.StatusForbidden},
		{"без порта", "CONNECT id.vk.com HTTP/1.1\r\nHost: id.vk.com\r\n\r\n", http.StatusForbidden},
		{"Host не тот, что в CONNECT", "CONNECT ad.mail.ru:443 HTTP/1.1\r\nHost: id.vk.com:443\r\n\r\n", http.StatusForbidden},
		{"не CONNECT", "GET http://id.vk.com/ HTTP/1.1\r\nHost: id.vk.com\r\n\r\n", http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &recordingDial{echo: echoServer(t)}
			p, logged := startTestProxy(t, d)
			if _, _, code := ask(t, p, tc.request); code != tc.code {
				t.Errorf("код %d, ожидался %d", code, tc.code)
			}
			if calls := d.calls(); len(calls) != 0 {
				t.Errorf("диалер позван при отказе: %q", calls)
			}
			if len(*logged) == 0 {
				t.Error("отказ не записан в лог")
			}
		})
	}
}

func TestProxyDialFails(t *testing.T) {
	d := &recordingDial{err: errors.New("нет сети")}
	p, logged := startTestProxy(t, d)
	if _, _, code := ask(t, p, "CONNECT id.vk.com:443 HTTP/1.1\r\n\r\n"); code != http.StatusBadGateway {
		t.Errorf("код %d, ожидался 502", code)
	}
	if len(*logged) == 0 || !strings.Contains((*logged)[0], "не соединился") {
		t.Errorf("лог: %q", *logged)
	}
}

// Close обрывает и открытые туннели: окно закрыто — ходить через прокси
// больше некому.
func TestProxyCloseDropsTunnels(t *testing.T) {
	d := &recordingDial{echo: echoServer(t)}
	p, err := StartProxy(d.dial, nil)
	if err != nil {
		t.Fatal(err)
	}
	c, br, code := ask(t, p, "CONNECT st.vk.com:443 HTTP/1.1\r\n\r\n")
	if code != http.StatusOK {
		t.Fatalf("код %d", code)
	}

	closed := make(chan error, 1)
	go func() { closed <- p.Close() }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close не вернулся при открытом туннеле")
	}
	if _, err := br.ReadByte(); err == nil {
		t.Error("туннель жив после Close")
	}
	c.Close()
	if _, err := net.Dial("tcp4", p.Addr()); err == nil {
		t.Error("прокси принимает соединения после Close")
	}
}
