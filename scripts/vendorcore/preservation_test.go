package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These byte expectations predate the accounting records and deliberately do not
// read their payloads. In particular, overrides return before any conversion.
const wantListen = `package core

import (
	"context"
	"net"
)

// listenUDP binds a UDP socket with SO_REUSEADDR so a quick restart can reclaim
// 127.0.0.1:port after the previous client process exits.
func listenUDP(addr string) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: setReuseAddr}
	return lc.ListenPacket(context.Background(), "udp", addr)
}`

const wantListenUnix = `//go:build !windows

package core

import "syscall"

func setReuseAddr(network, address string, c syscall.RawConn) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return setErr
}
`

const wantListenWindows = `//go:build windows

package core

import "syscall"

// На Windows syscall.SetsockoptInt принимает syscall.Handle, а не int.
func setReuseAddr(network, address string, c syscall.RawConn) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return setErr
}
`

const wantFixtureVersion = "# Версия ядра qWDTT, перенесённого в internal/core.\n" +
	"# Файл создаётся командой go run ./scripts/vendorcore — руками не править.\n\n" +
	"upstream = https://github.com/SpaceNeuroX/proxy-turn-vk-android\n" +
	"commit   = unknown\ndate     = unknown\nfiles    = 5\n"

func writeFixture(t *testing.T, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireFixtureBytes(t *testing.T, name, want string) {
	t.Helper()
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("%s bytes = %q, want %q", filepath.Base(name), got, want)
	}
}

func TestPreserveOverrideEarlyReturn(t *testing.T) {
	got, err := convertFile("LISTEN.GO", "\xef\xbb\xbfpackage main\r\nlog.Fatal(1)\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != wantListen {
		t.Errorf("override bytes = %q, want %q", got, wantListen)
	}
}

func TestPreserveRunBytes(t *testing.T) {
	saved := replacements
	t.Cleanup(func() { replacements = saved })
	replacements = nil
	for _, r := range saved {
		if r.File == "session.go" && r.Pattern == `(?m)^func RunPing\($` {
			replacements = append(replacements, r)
		}
	}
	if len(replacements) != 1 {
		t.Fatal("fixture must exercise the RunPing rename")
	}

	root, up := t.TempDir(), t.TempDir()
	source := filepath.Join(up, "go_client")
	target := filepath.Join(root, "internal", "core")
	for name, text := range map[string]string{
		"tun_fd.go":   "excluded Android input\r\n",
		"socks_wg.go": "excluded WireGuard input\n",
		"socks5.go":   "excluded SOCKS input",
		"LiStEn.Go":   "package main\r\nlog.Fatal(1)",
		"session.go": "\xef\xbb\xbfpackage main\r\n\r\nimport (\r\n\t\"log\"\r\n)\r\n\r\n" +
			"func RunPing(\r\n) {\r\n\tlog.Printf(\"fixture\")\r\n}\r\n",
		"no_final.go": "package main\n\nvar noFinal = true",
		"final.go":    "package main\n\nvar final = true\n",
		"extra.go":    "package main\n\nvar extra = true\n\n\n",
	} {
		writeFixture(t, filepath.Join(source, name), text)
	}
	for _, name := range []string{"tun_fd.go", "socks_wg.go", "socks5.go"} {
		writeFixture(t, filepath.Join(target, name), "stale excluded output\n")
	}
	sentinel := "\xef\xbb\xbfproject-owned sentinel\r\n\r\n"
	writeFixture(t, filepath.Join(target, "owned.go"), sentinel)
	want := map[string]string{
		"LiStEn.Go":         wantListen + "\n",
		"listen_unix.go":    wantListenUnix,
		"listen_windows.go": wantListenWindows,
		"CORE_VERSION":      wantFixtureVersion,
		"session.go": "package core\n\nimport (\n)\n\n" +
			"func runPing(\n) {\n\tlogf(\"fixture\")\n}\n",
		"no_final.go": "package core\n\nvar noFinal = true\n",
		"final.go":    "package core\n\nvar final = true\n",
		"extra.go":    "package core\n\nvar extra = true\n\n\n",
		"owned.go":    sentinel,
	}
	verify := func(t *testing.T) {
		t.Helper()
		for name, text := range want {
			requireFixtureBytes(t, filepath.Join(target, name), text)
		}
		for _, name := range []string{"tun_fd.go", "socks_wg.go", "socks5.go"} {
			if _, err := os.Stat(filepath.Join(target, name)); !os.IsNotExist(err) {
				t.Errorf("excluded %s still exists: %v", name, err)
			}
		}
		entries, err := os.ReadDir(target)
		if err != nil || len(entries) != len(want) {
			t.Fatalf("target entries = %d, want %d: %v", len(entries), len(want), err)
		}
	}
	for _, check := range []bool{false, false, true} {
		code, err := run(root, up, check)
		if err != nil || code != 0 {
			t.Fatalf("run(check=%v) = %d, %v", check, code, err)
		}
		verify(t)
	}

	// The read-side BOM/CRLF equivalence guard must not rewrite the target.
	current := "\xef\xbb\xbf" + strings.ReplaceAll(want["final.go"], "\n", "\r\n")
	writeFixture(t, filepath.Join(target, "final.go"), current)
	if code, err := run(root, up, true); err != nil || code != 0 {
		t.Fatalf("equivalent target -check = %d, %v", code, err)
	}
	want["final.go"] = current
	verify(t)
}
