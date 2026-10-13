//go:build windows

package main

import (
	"testing"

	"github.com/tailscale/wf"
)

func TestParseAddrs(t *testing.T) {
	got, err := parseAddrs(" 198.51.100.20, ::ffff:198.51.100.21,2001:db8::1,")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"198.51.100.20", "198.51.100.21", "2001:db8::1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	for _, bad := range []string{"", " , ", "198.51.100", "turn.example"} {
		if _, err := parseAddrs(bad); err == nil {
			t.Errorf("parseAddrs(%q) без ошибки", bad)
		}
	}
}

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
