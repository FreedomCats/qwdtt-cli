package winsys

import (
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// ConfigureAdapter задаёт адаптеру туннеля адреса и MTU. Адреса ставятся
// с длиной префикса во весь адрес (/32, /128): конфиг сервера приходит без
// маски подсети, а маршруты в туннель ставятся отдельно (SetTunnelRoutes).
//
// Интерфейс настраивается в обоих семействах, даже если адресов IPv6 нет:
// через туннель идут и маршруты IPv6 (ими IPv6 машины блокируется,
// plan-e2.md, решение 2), и MTU у него должен быть тем же. Если IPv6 на
// адаптере выключен, это не ошибка: семейство пропускается.
//
// Заодно на интерфейсе выключаются обнаружение маршрутизаторов и DAD:
// адаптер точка-точка, соседей у него нет, а DAD задерживает готовность
// адреса на секунду. Так же делает wireguard-windows
// (tunnel/addressconfig.go, configureInterface, MIT, v1.1.1).
//
// logf получает строку, если пришлось снимать адрес с чужого отключённого
// интерфейса (см. cleanupAddressesOnDisconnectedInterfaces).
func ConfigureAdapter(luid uint64, addrs []netip.Addr, mtu int, logf func(string, ...any)) error {
	l := winipcfg.LUID(luid)
	for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		var prefixes []netip.Prefix
		for _, a := range addrs {
			if a.Is4() == (family == windows.AF_INET) {
				prefixes = append(prefixes, netip.PrefixFrom(a, a.BitLen()))
			}
		}
		if len(prefixes) > 0 {
			err := l.SetIPAddressesForFamily(family, prefixes)
			if err == windows.ERROR_OBJECT_ALREADY_EXISTS {
				cleanupAddressesOnDisconnectedInterfaces(family, prefixes, logf)
				err = l.SetIPAddressesForFamily(family, prefixes)
			}
			if err != nil {
				return fmt.Errorf("адреса %v на адаптер: %w", prefixes, err)
			}
		}

		ipif, err := l.IPInterface(family)
		if len(prefixes) == 0 && family == windows.AF_INET6 && errors.Is(err, windows.ERROR_NOT_FOUND) {
			continue
		}
		if err != nil {
			return fmt.Errorf("параметры интерфейса %s: %w", familyName(family), err)
		}
		ipif.RouterDiscoveryBehavior = winipcfg.RouterDiscoveryDisabled
		ipif.DadTransmits = 0
		ipif.ManagedAddressConfigurationSupported = false
		ipif.OtherStatefulConfigurationSupported = false
		// IPv6 не принимает MTU меньше 1280 (RFC 8200), а конфиг допускает
		// и меньше. Тогда IPv6 через туннель всё равно не работает, только
		// блокируется, и MTU IPv6 остаётся как есть, а не роняет подъём.
		if mtu > 0 && (family == windows.AF_INET || mtu >= 1280) {
			ipif.NLMTU = uint32(mtu)
		}
		if err := ipif.Set(); err != nil {
			return fmt.Errorf("MTU интерфейса %s: %w", familyName(family), err)
		}
	}
	return nil
}

// cleanupAddressesOnDisconnectedInterfaces снимает наши адреса с интерфейсов,
// которые не в состоянии Up: адрес мог остаться на отключённом адаптере,
// например от штатного WireGuard с тем же конфигом, и тогда Windows не даёт
// поставить его нам (ERROR_OBJECT_ALREADY_EXISTS).
//
// Перенос одноимённой функции из golang.zx2c4.com/wireguard/windows
// tunnel/addressconfig.go (MIT, v1.1.1); log.Printf заменён на logf.
func cleanupAddressesOnDisconnectedInterfaces(family winipcfg.AddressFamily, addresses []netip.Prefix, logf func(string, ...any)) {
	if len(addresses) == 0 {
		return
	}
	addrHash := make(map[netip.Addr]bool, len(addresses))
	for i := range addresses {
		addrHash[addresses[i].Addr()] = true
	}
	interfaces, err := winipcfg.GetAdaptersAddresses(family, winipcfg.GAAFlagDefault)
	if err != nil {
		return
	}
	for _, iface := range interfaces {
		if iface.OperStatus == winipcfg.IfOperStatusUp {
			continue
		}
		for address := iface.FirstUnicastAddress; address != nil; address = address.Next {
			// Unmap — наше дополнение: net.IP бывает и 16-байтовым у IPv4.
			if ip, _ := netip.AddrFromSlice(address.Address.IP()); addrHash[ip.Unmap()] {
				ip = ip.Unmap()
				prefix := netip.PrefixFrom(ip, int(address.OnLinkPrefixLength))
				logf("[WINTUN] снимаю устаревший адрес %s с интерфейса %q", prefix, iface.FriendlyName())
				iface.LUID.DeleteIPAddress(prefix)
			}
		}
	}
}

func familyName(family winipcfg.AddressFamily) string {
	if family == windows.AF_INET6 {
		return "IPv6"
	}
	return "IPv4"
}
