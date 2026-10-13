package datapath

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
)

// fakeDNSGuard — защита DNS без WFP: помнит, включена ли она и с какими
// резолверами.
type fakeDNSGuard struct {
	mu        sync.Mutex
	enabled   bool
	dns       []netip.Addr
	enables   int
	disables  int
	enableErr error
	// onEnable, если задан, зовётся в Enable — чтобы проверить порядок.
	onEnable func()
}

func (g *fakeDNSGuard) Enable(dns []netip.Addr) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.enables++
	if g.onEnable != nil {
		g.onEnable()
	}
	if g.enableErr != nil {
		return g.enableErr
	}
	g.enabled, g.dns = true, dns
	return nil
}

func (g *fakeDNSGuard) Disable() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.disables++
	g.enabled = false
	return nil
}

func (g *fakeDNSGuard) state() (enabled bool, dns []netip.Addr, enables, disables int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.enabled, g.dns, g.enables, g.disables
}

func TestRequiresDNSGuard(t *testing.T) {
	if !NewTun(TunOptions{}).Requires().DNSGuard {
		t.Error("tun: DNSGuard = false, а он забирает весь трафик")
	}
	x := newExternalEnv(t)
	if !x.datapath(ExternalOptions{}).Requires().DNSGuard {
		t.Error("external: DNSGuard = false, а он забирает весь трафик")
	}
	if NewSocks(SocksOptions{}).Requires().DNSGuard {
		t.Error("socks: DNSGuard = true, а трафик системы он не трогает")
	}
}

// Защита включается после маршрутов — с резолверами из конфига — и
// снимается в Close.
func TestTunDNSGuard(t *testing.T) {
	guard := &fakeDNSGuard{}
	w, f, _ := newTestTun(t)
	w.opts.DNSGuard = guard
	guard.onEnable = func() {
		if !f.routed.Load() {
			t.Error("защита DNS включена до маршрутов")
		}
	}
	conf := testWGConfig(t, "127.0.0.1:9")
	conf.DNS = []netip.Addr{netip.MustParseAddr("10.8.0.1")}

	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if on, dns, _, _ := guard.state(); !on || len(dns) != 1 || dns[0] != conf.DNS[0] {
		t.Errorf("после Start: включена=%v, резолверы %v; ждали [10.8.0.1]", on, dns)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if on, _, _, disables := guard.state(); on || disables != 1 {
		t.Errorf("после Close: включена=%v, Disable %d раз", on, disables)
	}
}

// Отказ защиты — отказ Start с уборкой адаптера; следующий Start возможен.
func TestTunDNSGuardFailure(t *testing.T) {
	guard := &fakeDNSGuard{enableErr: errors.New("защита DNS: BFE не отвечает")}
	w, f, _ := newTestTun(t)
	w.opts.DNSGuard = guard
	conf := testWGConfig(t, "127.0.0.1:9")

	err := w.Start(context.Background(), conf)
	if err == nil || !strings.Contains(err.Error(), "tun: защита DNS: BFE не отвечает") {
		t.Fatalf("Start = %v, ждали отказ защиты DNS", err)
	}
	if f.tun.closed.Load() == 0 || f.released.Load() != 1 {
		t.Errorf("после отказа: TUN закрыт %d раз, release %d раз; ждали оба",
			f.tun.closed.Load(), f.released.Load())
	}

	guard.mu.Lock()
	guard.enableErr = nil
	guard.mu.Unlock()
	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("повторный Start: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// blockingDNSGuard — защита, которая умеет закрывать IPv6 (IPv6Blocker).
type blockingDNSGuard struct {
	fakeDNSGuard
	blocks []bool
}

func (g *blockingDNSGuard) BlockIPv6(block bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.blocks = append(g.blocks, block)
}

// Туннель не везёт IPv6 — защита узнаёт об этом до Enable и закрывает его;
// везёт снова — запрет снимается. Без IPv6Blocker — строка «Внимание».
func TestTunBlocksIPv6(t *testing.T) {
	guard := &blockingDNSGuard{}
	w, f, rec := newTestTun(t)
	w.opts.DNSGuard = guard
	f.noIPv6.Store(true)
	guard.onEnable = func() {
		if len(guard.blocks) == 0 {
			t.Error("Enable до BlockIPv6")
		}
	}
	conf := testWGConfig(t, "127.0.0.1:9")

	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !strings.Contains(rec.dump(), "закрыт защитой DNS") {
		t.Errorf("нет строки о закрытом IPv6:\n%s", rec.dump())
	}
	f.noIPv6.Store(false)
	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("повторный Start: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	guard.mu.Lock()
	blocks := guard.blocks
	guard.mu.Unlock()
	if len(blocks) != 2 || !blocks[0] || blocks[1] {
		t.Errorf("BlockIPv6: %v; ждали [true false]", blocks)
	}

	plain := &fakeDNSGuard{}
	w, f, rec = newTestTun(t)
	w.opts.DNSGuard = plain
	f.noIPv6.Store(true)
	if err := w.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start без IPv6Blocker: %v", err)
	}
	defer w.Close()
	if !strings.Contains(rec.dump(), "IPv6 системы идёт мимо туннеля") {
		t.Errorf("нет предупреждения об IPv6 мимо туннеля:\n%s", rec.dump())
	}
}

func TestExternalDNSGuard(t *testing.T) {
	x := newExternalEnv(t)
	guard := &fakeDNSGuard{}
	dp := x.datapath(ExternalOptions{DNSGuard: guard})
	conf := parsedServerConfig(t)

	if err := dp.Start(context.Background(), conf); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if on, dns, _, _ := guard.state(); !on || len(dns) == 0 || dns[0] != conf.DNS[0] {
		t.Errorf("после Start: включена=%v, резолверы %v; ждали %v", on, dns, conf.DNS)
	}
	if err := dp.Close(); err != nil {
		t.Fatal(err)
	}
	if on, _, _, disables := guard.state(); on || disables != 1 {
		t.Errorf("после Close: включена=%v, Disable %d раз", on, disables)
	}
}

// Без строки DNS в [Interface] штатный WireGuard не дал бы адаптеру
// резолверов, а резолвер провайдера запрещает защита DNS: строка
// дописывается теми резолверами, что разрешены. Своя строка не трогается.
func TestAddDefaultDNS(t *testing.T) {
	dns := []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("2606:4700:4700::1111")}
	for _, tc := range []struct {
		name, raw, want string
		changed         bool
	}{
		{
			name:    "нет DNS",
			raw:     "[Interface]\nPrivateKey = k\n\n[Peer]\nPublicKey = p\n",
			want:    "[Interface]\nDNS = 1.1.1.1, 2606:4700:4700::1111\nPrivateKey = k\n\n[Peer]\nPublicKey = p\n",
			changed: true,
		},
		{
			name:    "CRLF",
			raw:     "[Interface]\r\nPrivateKey = k\r\n",
			want:    "[Interface]\r\nDNS = 1.1.1.1, 2606:4700:4700::1111\r\nPrivateKey = k\r\n",
			changed: true,
		},
		{
			name: "свой DNS",
			raw:  "[Interface]\nPrivateKey = k\ndns=10.8.0.1\n[Peer]\n",
			want: "[Interface]\nPrivateKey = k\ndns=10.8.0.1\n[Peer]\n",
		},
		{
			name:    "DNS только в комментарии и в [Peer]",
			raw:     "[Interface]\n# DNS = 9.9.9.9\nPrivateKey = k\n[Peer]\nDNS = 9.9.9.9\n",
			want:    "[Interface]\nDNS = 1.1.1.1, 2606:4700:4700::1111\n# DNS = 9.9.9.9\nPrivateKey = k\n[Peer]\nDNS = 9.9.9.9\n",
			changed: true,
		},
	} {
		got, changed := addDefaultDNS(tc.raw, dns)
		if got != tc.want || changed != tc.changed {
			t.Errorf("%s: изменено=%v\n%q\nждали изменено=%v\n%q", tc.name, changed, got, tc.changed, tc.want)
		}
	}
}

// Отказ защиты — служба снимается, файл с ключом удаляется.
func TestExternalDNSGuardFailure(t *testing.T) {
	x := newExternalEnv(t)
	guard := &fakeDNSGuard{enableErr: errors.New("защита DNS: BFE не отвечает")}
	dp := x.datapath(ExternalOptions{DNSGuard: guard})

	err := dp.Start(context.Background(), parsedServerConfig(t))
	if err == nil || !strings.Contains(err.Error(), "external: защита DNS: BFE не отвечает") {
		t.Fatalf("Start = %v, ждали отказ защиты DNS", err)
	}
	calls := x.calls()
	if len(calls) != 2 || !strings.HasPrefix(calls[1], "/uninstalltunnelservice") {
		t.Errorf("служба не снята после отказа:\n%s", strings.Join(calls, "\n"))
	}
	if _, err := os.Stat(x.confPath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("файл конфига остался после отказа: %v", err)
	}
}
