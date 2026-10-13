package datapath

import (
	"strings"
	"sync/atomic"
	"testing"

	"golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// closeCountingTUN считает Close: startDevice отвечает за TUN с момента вызова,
// и на отказе адаптер Wintun остался бы висеть до конца процесса.
type closeCountingTUN struct {
	tun.Device
	closed atomic.Int32
}

func (c *closeCountingTUN) Close() error {
	c.closed.Add(1)
	return c.Device.Close()
}

func newTestTUN(t *testing.T, conf *WGConfig) *closeCountingTUN {
	t.Helper()
	tunDev, _, err := netstack.CreateNetTUN(conf.Addresses, conf.DNS, conf.MTU)
	if err != nil {
		t.Fatalf("CreateNetTUN: %v", err)
	}
	return &closeCountingTUN{Device: tunDev}
}

func TestStartDevice(t *testing.T) {
	conf := testWGConfig(t, "127.0.0.1:9")
	tunDev := newTestTUN(t, conf)
	rec := &recorder{}

	dev, err := startDevice(tunDev, conf, "[WG-TEST]", rec.logf)
	if err != nil {
		t.Fatalf("startDevice: %v", err)
	}
	if n := tunDev.closed.Load(); n != 0 {
		t.Fatalf("TUN закрыт %d раз до Close устройства", n)
	}
	dev.Close()
	if n := tunDev.closed.Load(); n == 0 {
		t.Fatal("Close устройства не закрыл TUN")
	}
}

func TestStartDeviceFailureClosesTUN(t *testing.T) {
	conf := testWGConfig(t, "127.0.0.1:9")
	conf.PrivateKeyHex = "не-hex"
	tunDev := newTestTUN(t, conf)
	rec := &recorder{}

	dev, err := startDevice(tunDev, conf, "[WG-TEST]", rec.logf)
	if err == nil {
		dev.Close()
		t.Fatal("startDevice принял битый ключ")
	}
	if !strings.HasPrefix(err.Error(), "IpcSet: ") {
		t.Errorf("ошибка = %q, ждали с префиксом шага IpcSet", err)
	}
	if n := tunDev.closed.Load(); n == 0 {
		t.Error("на отказе TUN не закрыт")
	}
	// Ошибку IpcSet wireguard-go пишет и в Errorf — с нашим префиксом.
	if log := rec.dump(); log != "" && !strings.Contains(log, "[WG-TEST] ") {
		t.Errorf("строки лога без префикса:\n%s", log)
	}
}
