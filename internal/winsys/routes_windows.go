package winsys

import (
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// SetTunnelRoutes ставит маршруты через адаптер туннеля и делает его
// интерфейсы предпочтительными: метрика 0 вместо автоматической, как
// у wireguard-windows при полном туннеле (tunnel/addressconfig.go,
// configureInterface, MIT, v1.1.1). От метрики интерфейса зависит, к чьим
// резолверам DNS-клиент Windows идёт первым.
//
// Маршруты — без шлюза (on-link): адаптер точка-точка. Уже стоящий маршрут
// не ошибка, так что повторный вызов безвреден. Маршрут по умолчанию не
// трогается (architecture.md §09): половины /1 выигрывают у него длиной
// префикса, а снять их можно, не трогая чужого.
//
// Если на адаптере нет IPv6 (протокол отвязан от него или выключен в системе),
// маршруты IPv6 пропускаются, и logf получает строку об этом: блокировать
// IPv6 через туннель тогда нечем.
func SetTunnelRoutes(luid uint64, routes []netip.Prefix, logf func(string, ...any)) error {
	l := winipcfg.LUID(luid)
	for _, family := range routeFamilies(routes) {
		ipif, err := l.IPInterface(family)
		if family == windows.AF_INET6 && errors.Is(err, windows.ERROR_NOT_FOUND) {
			logf("[WINTUN] IPv6 на адаптере выключен: маршруты IPv6 не поставлены, IPv6 машины идёт мимо туннеля")
			continue
		}
		if err != nil {
			return fmt.Errorf("параметры интерфейса %s: %w", familyName(family), err)
		}
		for _, r := range routes {
			if r.Addr().Is4() != (family == windows.AF_INET) {
				continue
			}
			err := l.AddRoute(r.Masked(), unspecified(family), 0)
			if err != nil && !errors.Is(err, windows.ERROR_OBJECT_ALREADY_EXISTS) {
				return fmt.Errorf("маршрут %s: %w", r, err)
			}
		}
		ipif.UseAutomaticMetric = false
		ipif.Metric = 0
		if err := ipif.Set(); err != nil {
			return fmt.Errorf("метрика интерфейса %s: %w", familyName(family), err)
		}
	}
	return nil
}

// ClearTunnelRoutes снимает то, что поставил SetTunnelRoutes, и возвращает
// интерфейсам автоматическую метрику. Адаптер при этом остаётся. Маршрута,
// которого уже нет, не ошибка, так что повторный вызов безвреден.
//
// При закрытии адаптера звать не нужно: маршруты висят на его LUID и уходят
// вместе с ним.
func ClearTunnelRoutes(luid uint64, routes []netip.Prefix) error {
	l := winipcfg.LUID(luid)
	var errs []error
	for _, family := range routeFamilies(routes) {
		ipif, err := l.IPInterface(family)
		if family == windows.AF_INET6 && errors.Is(err, windows.ERROR_NOT_FOUND) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("параметры интерфейса %s: %w", familyName(family), err))
			continue
		}
		for _, r := range routes {
			if r.Addr().Is4() != (family == windows.AF_INET) {
				continue
			}
			err := l.DeleteRoute(r.Masked(), unspecified(family))
			if err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) {
				errs = append(errs, fmt.Errorf("маршрут %s: %w", r, err))
			}
		}
		ipif.UseAutomaticMetric = true
		if err := ipif.Set(); err != nil {
			errs = append(errs, fmt.Errorf("метрика интерфейса %s: %w", familyName(family), err))
		}
	}
	return errors.Join(errs...)
}

// routeFamilies — семейства, для которых в списке есть маршруты, IPv4 первым.
func routeFamilies(routes []netip.Prefix) []winipcfg.AddressFamily {
	var has4, has6 bool
	for _, r := range routes {
		if r.Addr().Is4() {
			has4 = true
		} else {
			has6 = true
		}
	}
	var families []winipcfg.AddressFamily
	if has4 {
		families = append(families, windows.AF_INET)
	}
	if has6 {
		families = append(families, windows.AF_INET6)
	}
	return families
}

func unspecified(family winipcfg.AddressFamily) netip.Addr {
	if family == windows.AF_INET6 {
		return netip.IPv6Unspecified()
	}
	return netip.IPv4Unspecified()
}
