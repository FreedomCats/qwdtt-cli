//go:build windows

package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRunOffline прогоняет снимок по настоящей системе без сети: все
// разделы на месте, сетевых нет. Без прав администратора раздел WFP —
// с ошибкой, это нормально.
func TestRunOffline(t *testing.T) {
	var b bytes.Buffer
	run(&b, options{label: "тест", name: "qwdtt-cli", confDir: t.TempDir(), offline: true})
	out := b.String()
	for _, s := range []string{"— тест ===", "-- Процессы qwdtt-cli.exe", "-- Адаптеры", "-- Маршруты /0 и /1",
		"-- WFP: защита DNS", "-- Реестр", "NRPT", "{E46AF63B-12E0-4377-97DB-29F124A70F40}", "-- external", "WireGuardTunnel$qwdtt-cli"} {
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
