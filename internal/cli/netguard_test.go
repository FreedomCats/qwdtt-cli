package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/netguard"
)

// Выдернутый и вставленный кабель — один перезапуск: пропажа сети попытку
// не трогает, возврат перезапускает. Смена интерфейса и переподключение
// того же — тоже перезапуск.
func TestNetworkChanged(t *testing.T) {
	ethernet := netguard.Choice{Index4: 3, Alias4: "Ethernet 3"}
	wifi := netguard.Choice{Index4: 7, Alias4: "Wi-Fi"}
	offline := netguard.Choice{}

	var buf bytes.Buffer
	var restarts []error
	changed := networkChanged(newPrinter(&buf, false), func(err error) { restarts = append(restarts, err) }, false)

	changed(offline, ethernet)
	if len(restarts) != 0 {
		t.Fatalf("пропажа сети перезапустила попытку: %v", restarts)
	}
	changed(ethernet, offline)
	changed(wifi, ethernet)
	changed(wifi, wifi)

	if len(restarts) != 3 {
		t.Fatalf("перезапусков %d, ждали 3", len(restarts))
	}
	for _, err := range restarts {
		if !errors.Is(err, errNetworkChanged) {
			t.Errorf("причина %v, ждали «сеть сменилась»", err)
		}
	}
	out := buf.String()
	for _, want := range []string{
		"сеть пропала",
		"сеть сменилась: IPv4 — Ethernet 3 (индекс 3), IPv6 — нет маршрута",
		"сеть сменилась: IPv4 — Wi-Fi (индекс 7)",
		"сеть переподключилась: IPv4 — Wi-Fi (индекс 7)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("в выводе нет %q:\n%s", want, out)
		}
	}
}

// Под полным туннелем у нового интерфейса проверяется обратный фильтр:
// строгий — строка-предупреждение, перезапуск всё равно идёт.
func TestNetworkChangedChecksReversePath(t *testing.T) {
	strict := netguard.Choice{Index4: 5, Alias4: "wlan0"}
	loose := netguard.Choice{Index4: 2, Alias4: "eth0"}
	old := reversePathCheck
	reversePathCheck = func(c netguard.Choice) (string, error) {
		if c.Alias4 == "wlan0" {
			return "на wlan0 строгий обратный фильтр", nil
		}
		return "", nil
	}
	t.Cleanup(func() { reversePathCheck = old })

	var buf bytes.Buffer
	restarts := 0
	changed := networkChanged(newPrinter(&buf, false), func(error) { restarts++ }, true)
	changed(strict, loose)
	changed(loose, strict)
	if restarts != 2 {
		t.Errorf("перезапусков %d, ждали 2", restarts)
	}
	if out := buf.String(); strings.Count(out, "Внимание: на wlan0 строгий") != 1 {
		t.Errorf("предупреждение — ровно одно, для wlan0:\n%s", out)
	}

	buf.Reset()
	changed = networkChanged(newPrinter(&buf, false), func(error) {}, false)
	changed(strict, loose)
	if strings.Contains(buf.String(), "Внимание") {
		t.Errorf("без полного туннеля фильтр не проверяется:\n%s", buf.String())
	}
}
