package datapath

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// testKeyPair — ключи WireGuard в hex (формат IPC).
func testKeyPair(t *testing.T) (priv, pub string) {
	t.Helper()
	var k [32]byte
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatal(err)
	}
	k[0] &= 248
	k[31] = (k[31] & 127) | 64
	p, err := curve25519.X25519(k[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(k[:]), hex.EncodeToString(p)
}

// startTestServer поднимает второй wireguard-go — сервер для клиента
// с ключом clientPub — и возвращает его порт на loopback.
func startTestServer(t *testing.T, serverPriv, clientPub string) int {
	t.Helper()
	tunDev, _, err := netstack.CreateNetTUN(nil, nil, 1280)
	if err != nil {
		t.Fatalf("CreateNetTUN: %v", err)
	}
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), &device.Logger{
		Verbosef: device.DiscardLogf, Errorf: device.DiscardLogf,
	})
	t.Cleanup(dev.Close)
	if err := dev.IpcSet(fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=10.7.0.2/32\n",
		serverPriv, clientPub)); err != nil {
		t.Fatalf("сервер IpcSet: %v", err)
	}
	if err := dev.Up(); err != nil {
		t.Fatalf("сервер Up: %v", err)
	}
	state, err := dev.IpcGet()
	if err != nil {
		t.Fatalf("сервер IpcGet: %v", err)
	}
	var port int
	for _, line := range strings.Split(state, "\n") {
		if v, ok := strings.CutPrefix(line, "listen_port="); ok {
			fmt.Sscan(v, &port)
		}
	}
	if port == 0 {
		t.Fatalf("у сервера нет порта:\n%s", state)
	}
	return port
}

// closedUDPPort — порт на loopback, который только что был занят и закрыт:
// пакет туда даёт ICMP «порт недоступен».
func closedUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	port := c.LocalAddr().(*net.UDPAddr).Port
	c.Close()
	return port
}

func waitHandshake(dev *device.Device, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := dev.IpcGet()
		if err == nil && strings.Contains(state, "last_handshake_time_sec=") &&
			!strings.Contains(state, "last_handshake_time_sec=0\n") {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// После обрыва попытки wireguard-go успевает послать рукопожатие в порт
// ядра, которого уже нет. На Windows ответный ICMP «порт недоступен»
// превращается в WSAECONNRESET на приёме, и приёмная горутина wireguard-go
// выходит навсегда (device/receive.go: ошибка не временная). Перенастройка
// одним IpcSet её не возвращает: рукопожатия уходят, ответы никто не читает,
// туннель «работает» без трафика (живой прогон R2, сценарий 4).
// reconfigureDevice обязан открыть сокеты заново.
func TestReconfigureDeviceSurvivesPortUnreachable(t *testing.T) {
	clientPriv, clientPub := testKeyPair(t)
	serverPriv, serverPub := testKeyPair(t)
	serverPort := startTestServer(t, serverPriv, clientPub)

	conf := testWGConfig(t, fmt.Sprintf("127.0.0.1:%d", closedUDPPort(t)))
	conf.PrivateKeyHex, conf.PeerPublicHex = clientPriv, serverPub
	conf.Keepalive = 1
	dev, err := startDevice(newTestTUN(t, conf), conf, "[WG-TEST]", (&recorder{}).logf)
	if err != nil {
		t.Fatalf("startDevice: %v", err)
	}
	defer dev.Close()
	// Рукопожатие и keepalive в закрытый порт, ICMP в ответ.
	time.Sleep(1500 * time.Millisecond)

	// Как Tun.Suspend и следующий Start.
	dev.RemoveAllPeers()
	next := *conf
	next.Endpoint = fmt.Sprintf("127.0.0.1:%d", serverPort)
	if err := reconfigureDevice(dev, &next); err != nil {
		t.Fatalf("reconfigureDevice: %v", err)
	}
	if !waitHandshake(dev, 10*time.Second) {
		state, _ := dev.IpcGet()
		t.Fatalf("рукопожатие с сервером не прошло после перенастройки:\n%s", state)
	}
}
