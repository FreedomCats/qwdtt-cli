//go:build windows

package main

import (
	"testing"

	"github.com/tailscale/wf"
)

func TestLayersFor(t *testing.T) {
	v4, _ := parseAddrs("1.2.3.4")
	v6, _ := parseAddrs("2001:db8::1")
	if l := layersFor(v4[0]); l[0] != wf.LayerInboundIPPacketV4 || l[1] != wf.LayerOutboundIPPacketV4 {
		t.Errorf("IPv4: %v", l)
	}
	if l := layersFor(v6[0]); l[0] != wf.LayerInboundIPPacketV6 || l[1] != wf.LayerOutboundIPPacketV6 {
		t.Errorf("IPv6: %v", l)
	}
}
