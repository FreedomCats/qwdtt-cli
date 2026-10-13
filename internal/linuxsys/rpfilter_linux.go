package linuxsys

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Обратный фильтр (reverse path filter) отбрасывает пакет, если ответ на
// него ушёл бы не через тот интерфейс, на который он пришёл. Под туннелем
// маршруты /1 ведут в tt, а сокеты ядра привязаны к физическому интерфейсу:
// ответы VK и TURN приходят на физический, обратный маршрут к источнику —
// через tt, и строгий фильтр их отбрасывает (plan-e4.md, N1, п. 3–4; T3).
// Нестрогий проверяет только, что маршрут к источнику есть, — он проходит.

// RPFilter* — значения rp_filter IPv4.
const (
	RPFilterOff    = 0
	RPFilterStrict = 1
	RPFilterLoose  = 2
)

// procRoot, etcRoot — корни /proc и /etc; подменяются в тестах.
var (
	procRoot = "/proc"
	etcRoot  = "/etc"
)

// ReversePathFilter — действующий rp_filter IPv4 интерфейса name: ядро
// берёт большее из net.ipv4.conf.all и значения самого интерфейса.
func ReversePathFilter(name string) (int, error) {
	all, err := readSysctlInt("all")
	if err != nil {
		return 0, err
	}
	own, err := readSysctlInt(name)
	if err != nil {
		return 0, err
	}
	return max(all, own), nil
}

func readSysctlInt(iface string) (int, error) {
	path := filepath.Join(procRoot, "sys/net/ipv4/conf", iface, "rp_filter")
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("rp_filter %s: %w", iface, err)
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("rp_filter %s: %q — не число", iface, b)
	}
	return v, nil
}

// FirewalldStrictIPv6 — работает ли firewalld со строгим обратным фильтром
// IPv6 (IPv6_rpfilter в firewalld.conf: yes — умолчание, или strict). Его
// правило (fib saddr . mark . iif oif missing drop) отбрасывает ответы IPv6
// сокетам ядра так же, как строгий rp_filter — IPv4 (проверено опытом в T3).
// Режимы loose и *-forward входящие ответы не трогают.
//
// firewalld узнаётся по процессу (comm «firewalld»): правила у него в
// nftables, но служба может быть установлена и не запущена.
func FirewalldStrictIPv6() (bool, error) {
	running, err := processRunning("firewalld")
	if err != nil || !running {
		return false, err
	}
	conf, err := os.ReadFile(filepath.Join(etcRoot, "firewalld/firewalld.conf"))
	if os.IsNotExist(err) {
		return true, nil // без файла — умолчание yes
	}
	if err != nil {
		return false, fmt.Errorf("firewalld.conf: %w", err)
	}
	switch strings.ToLower(confValue(conf, "IPv6_rpfilter", "yes")) {
	case "yes", "true", "strict":
		return true, nil
	default:
		return false, nil
	}
}

// confValue — значение ключа key в файле вида КЛЮЧ=значение с комментариями
// «#»; нет ключа — def.
func confValue(conf []byte, key, def string) string {
	value := def
	sc := bufio.NewScanner(bytes.NewReader(conf))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			value = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return value
}

// processRunning — есть ли процесс с именем comm.
func processRunning(comm string) (bool, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "comm"))
		if err == nil && strings.TrimSpace(string(b)) == comm {
			return true, nil
		}
	}
	return false, nil
}
