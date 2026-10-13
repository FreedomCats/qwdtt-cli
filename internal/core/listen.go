package core

import (
	"context"
	"net"
)

// listenUDP binds a UDP socket with SO_REUSEADDR so a quick restart can reclaim
// 127.0.0.1:port after the previous client process exits.
func listenUDP(addr string) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: setReuseAddr}
	return lc.ListenPacket(context.Background(), "udp", addr)
}
