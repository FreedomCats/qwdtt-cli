package cli

import "github.com/FreedomCats/qwdtt-cli/internal/winsys"

// openSystemDNSGuard — сессия WFP (winsys.DNSGuard).
func openSystemDNSGuard() (dnsGuardSession, error) {
	g, err := winsys.OpenDNSGuard()
	if err != nil {
		return nil, err
	}
	return g, nil
}

// dnsGuardMark — метка сокетов qwdtt-cli для защиты DNS: на Windows не нужна,
// WFP пропускает весь процесс qwdtt-cli.
const dnsGuardMark = 0
