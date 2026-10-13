package linuxsys

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// SetTunnelRoutes ставит маршруты prefixes через интерфейс index таблицы
// main: без шлюза, область link, как wg-quick. Повторный вызов безвреден
// (RouteReplace). Маршруты висят на интерфейсе и уходят вместе с ним.
// Аналог winsys.SetTunnelRoutes.
func SetTunnelRoutes(index int, prefixes []netip.Prefix) error {
	for _, p := range prefixes {
		if err := netlink.RouteReplace(tunnelRoute(index, p)); err != nil {
			return fmt.Errorf("маршрут %v: %w", p, err)
		}
	}
	return nil
}

// ClearTunnelRoutes снимает маршруты prefixes с интерфейса index; маршрута
// уже нет — не ошибка. Снимает все, что может, и возвращает первую ошибку.
func ClearTunnelRoutes(index int, prefixes []netip.Prefix) error {
	var first error
	for _, p := range prefixes {
		err := netlink.RouteDel(tunnelRoute(index, p))
		if err != nil && !errors.Is(err, unix.ESRCH) && first == nil {
			first = fmt.Errorf("снятие маршрута %v: %w", p, err)
		}
	}
	return first
}

func tunnelRoute(index int, p netip.Prefix) *netlink.Route {
	p = p.Masked()
	return &netlink.Route{
		LinkIndex: index,
		Dst:       &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())},
		Scope:     netlink.SCOPE_LINK,
		Table:     unix.RT_TABLE_MAIN,
	}
}
