// fulltunnel_linux.go — проверки Linux для datapath, который забирает весь
// трафик системы (plan-e4.md, T3): обратный фильтр отбросил бы ответы
// сокетам ядра, привязанным к физическому интерфейсу.
package cli

import (
	"fmt"

	"github.com/FreedomCats/qwdtt-cli/internal/linuxsys"
	"github.com/FreedomCats/qwdtt-cli/internal/netguard"
)

// reversePathProblem — почему ответы на выбранный интерфейс IPv4 отбросит
// строгий rp_filter; "" — не отбросит. Менять sysctl сам tt не берётся:
// изменение пережило бы его падение (решение T3).
func reversePathProblem(c netguard.Choice) (string, error) {
	if c.Index4 == 0 {
		return "", nil
	}
	mode, err := linuxsys.ReversePathFilter(c.Alias4)
	if err != nil {
		return "", err
	}
	if mode != linuxsys.RPFilterStrict {
		return "", nil
	}
	// Ядро берёт большее из all и значения интерфейса, поэтому нестрогий
	// режим на самом интерфейсе перекрывает строгий all.
	return fmt.Sprintf("на %[1]s строгий обратный фильтр (rp_filter = 1): ответы VK и TURN сокетам tt "+
		"будут отбрасываться, и туннель не поднимется; включите нестрогий режим: "+
		"sudo sysctl -w net.ipv4.conf.%[1]s.rp_filter=2 (насовсем — файлом в /etc/sysctl.d)", c.Alias4), nil
}

// ipv6ReplyFilter — почему сокетам ядра нельзя давать IPv6; "" — можно.
func ipv6ReplyFilter() (string, error) {
	strict, err := linuxsys.FirewalldStrictIPv6()
	if err != nil || !strict {
		return "", err
	}
	return "firewalld строго проверяет обратный путь IPv6 (IPv6_rpfilter) и отбросил бы ответы IPv6 сокетам tt", nil
}
