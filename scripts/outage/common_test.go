//go:build windows || linux

package main

import "testing"

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
