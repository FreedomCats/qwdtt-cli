// fulltunnel_linux.go — проверки Linux для datapath, который забирает весь
// трафик системы (plan-e4.md, T3): обратный фильтр отбросил бы ответы
// сокетам ядра, привязанным к физическому интерфейсу.
package cli

import (
	"fmt"

	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
	"github.com/FreedomCats/qwdtt-cli/internal/linuxsys"
	"github.com/FreedomCats/qwdtt-cli/internal/netguard"
)

// reversePathProblem — почему ответы на выбранный интерфейс IPv4 отбросит
// строгий rp_filter; "" — не отбросит. Менять sysctl сам qwdtt-cli не берётся:
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
	return fmt.Sprintf("на %[1]s строгий обратный фильтр (rp_filter = 1): ответы VK и TURN сокетам qwdtt-cli "+
		"будут отбрасываться, и туннель не поднимется; включите нестрогий режим: "+
		"sudo sysctl -w net.ipv4.conf.%[1]s.rp_filter=2 (насовсем — файлом в /etc/sysctl.d)", c.Alias4), nil
}

// ipv6ReplyFilter — почему сокетам ядра нельзя давать IPv6; "" — можно.
func ipv6ReplyFilter() (string, error) {
	strict, err := linuxsys.FirewalldStrictIPv6()
	if err != nil || !strict {
		return "", err
	}
	return "firewalld строго проверяет обратный путь IPv6 (IPv6_rpfilter) и отбросил бы ответы IPv6 сокетам qwdtt-cli", nil
}

// systemDNSReady узнаёт, как datapath tun задаст системе DNS туннеля
// (plan-e4.md, D2), и печатает способ. Незнакомое устройство resolv.conf —
// отказ сейчас, а не в каждой попытке после похода в VK. Заодно убирается
// то, что оставил упавший прошлый запуск: CLI зовёт это после Reserve,
// когда второго qwdtt-cli с адаптером на машине быть не может.
func systemDNSReady(out *printer) error {
	dns, err := linuxsys.DetectSystemDNS(datapath.DefaultTunnelName)
	if err != nil {
		return fmt.Errorf("DNS туннеля в системе: %w; или подключитесь с -datapath socks", err)
	}
	done, err := dns.Recover()
	if err != nil {
		return fmt.Errorf("DNS системы: %w", err)
	}
	if done != "" {
		out.line("DNS системы: %s", done)
	}
	out.line("DNS системы: %s", dns)
	if w := dns.Warning(); w != "" {
		out.line("Внимание: %s", w)
	}
	return nil
}
