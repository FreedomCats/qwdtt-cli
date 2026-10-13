package winsys

import (
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// SetAdapterDNS задаёт адаптеру туннеля резолверы: к ним DNS-клиент Windows
// идёт первым, пока у адаптера наименьшая метрика (SetTunnelRoutes).
// Список прежних резолверов заменяется, а не дополняется, так что повторный
// вызов безвреден.
//
// Настройка живёт в реестре под GUID интерфейса и переживает адаптер, но без
// интерфейса ни на что не влияет; GUID у адаптера постоянный, и следующий
// запуск её перезапишет (plan-e2.md, врезка N1).
//
// Резолверы IPv6 без IPv6 на адаптере не ставятся: семейство пропускается.
func SetAdapterDNS(luid uint64, servers []netip.Addr) error {
	l := winipcfg.LUID(luid)
	for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		if family == windows.AF_INET6 {
			if _, err := l.IPInterface(family); errors.Is(err, windows.ERROR_NOT_FOUND) {
				continue
			}
		}
		// SetDNS сам отбирает адреса своего семейства.
		if err := l.SetDNS(family, servers, nil); err != nil {
			return fmt.Errorf("DNS %s на адаптер: %w", familyName(family), err)
		}
	}
	return nil
}
