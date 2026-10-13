//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

func TestInterestingRoute(t *testing.T) {
	cases := map[string]bool{
		"0.0.0.0/0":      true,
		"0.0.0.0/1":      true,
		"128.0.0.0/1":    true,
		"::/0":           true,
		"::/1":           true,
		"8000::/1":       true,
		"10.0.0.0/8":     false,
		"192.168.1.0/24": false,
		"fe80::/64":      false,
	}
	for s, want := range cases {
		if got := interestingRoute(netip.MustParsePrefix(s)); got != want {
			t.Errorf("interestingRoute(%s) = %v, want %v", s, got, want)
		}
	}
	if interestingRoute(netip.Prefix{}) {
		t.Error("пустой префикс — не маршрут")
	}
}

func TestSortRoutes(t *testing.T) {
	rs := []route{
		{prefix: netip.MustParsePrefix("8000::/1"), index: 40},
		{prefix: netip.MustParsePrefix("128.0.0.0/1"), index: 40},
		{prefix: netip.MustParsePrefix("0.0.0.0/0"), index: 3},
		{prefix: netip.MustParsePrefix("::/1"), index: 40},
		{prefix: netip.MustParsePrefix("0.0.0.0/1"), index: 40},
		{prefix: netip.MustParsePrefix("0.0.0.0/0"), index: 2},
	}
	sortRoutes(rs)
	var got []string
	for _, r := range rs {
		got = append(got, fmt.Sprintf("%s@%d", r.prefix, r.index))
	}
	want := "0.0.0.0/0@2 0.0.0.0/0@3 0.0.0.0/1@40 128.0.0.0/1@40 ::/1@40 8000::/1@40"
	if strings.Join(got, " ") != want {
		t.Errorf("порядок:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
}

func TestShortErr(t *testing.T) {
	if got := shortErr(fmt.Errorf("Get: %w", context.DeadlineExceeded)); got != "таймаут" {
		t.Errorf("таймаут: %q", got)
	}
	if got := shortErr(errors.New("dial tcp6 [2600::1]:443: connect: A socket operation was attempted to an unreachable network.")); got != "A socket operation was attempted to an unreachable network." {
		t.Errorf("суть ошибки: %q", got)
	}
	if got := shortErr(errors.New("plain")); got != "plain" {
		t.Errorf("без двоеточия: %q", got)
	}
}

// TestRunOffline прогоняет снимок по настоящей системе без сети: все
// разделы на месте, сетевых нет. Без прав администратора раздел WFP —
// с ошибкой, это нормально.
func TestRunOffline(t *testing.T) {
	var b bytes.Buffer
	run(&b, options{label: "тест", name: "tt", confDir: t.TempDir(), offline: true})
	out := b.String()
	for _, s := range []string{"— тест ===", "-- Процессы tt.exe", "-- Адаптеры", "-- Маршруты /0 и /1",
		"-- WFP: защита DNS", "-- Реестр", "NRPT", "{E46AF63B-12E0-4377-97DB-29F124A70F40}", "-- external", "WireGuardTunnel$tt"} {
		if !strings.Contains(out, s) {
			t.Errorf("в выводе нет %q:\n%s", s, out)
		}
	}
	for _, s := range []string{"Внешний адрес", "Резолвер"} {
		if strings.Contains(out, s) {
			t.Errorf("с offline в выводе есть %q", s)
		}
	}
	t.Log("\n" + out)
}
