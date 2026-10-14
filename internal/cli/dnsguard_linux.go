package cli

import (
	"github.com/FreedomCats/qwdtt-cli/internal/linuxsys"
	"github.com/FreedomCats/qwdtt-cli/internal/netguard"
)

// openSystemDNSGuard — таблица nftables (linuxsys.DNSGuard); сокеты qwdtt-cli
// она узнаёт по метке dnsGuardMark.
func openSystemDNSGuard() (dnsGuardSession, error) {
	g, err := linuxsys.OpenDNSGuard(dnsGuardMark)
	if err != nil {
		return nil, err
	}
	return g, nil
}

// dnsGuardMark — метка SO_MARK, которую netguard ставит сокетам ядра, когда
// защита DNS включена: по ней правила nftables пропускают qwdtt-cli к DNS VK.
const dnsGuardMark = netguard.SocketMark
