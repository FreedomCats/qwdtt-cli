package winsys

import (
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// Interface — выбранный физический интерфейс.
type Interface struct {
	Index uint32 // индекс для netguard (IP_UNICAST_IF)
	LUID  uint64
	Alias string // имя, как в Get-NetAdapter: «Ethernet 3», «Wi-Fi»
}

// DefaultInterface выбирает интерфейс маршрута по умолчанию (0.0.0.0/0 или
// ::/0) для семейства: с наименьшей суммой метрик маршрута и интерфейса,
// только интерфейсы в состоянии Up, без интерфейса exclude (LUID своего
// адаптера; 0 — исключать нечего). Маршруты туннеля — две половины /1
// (решения 2 и 5 plan-e2.md), в выбор они не попадают, а свой адаптер
// исключается ещё и явно, поэтому выбор работает и при поднятом туннеле.
//
// ok = false — маршрута по умолчанию для семейства нет (например, сеть без
// IPv6). Это не ошибка.
//
// Перенос findDefaultLUID из golang.zx2c4.com/wireguard/windows
// tunnel/mtumonitor.go (MIT, v1.1.1).
func DefaultInterface(ipv6 bool, exclude uint64) (iface Interface, ok bool, err error) {
	family := winipcfg.AddressFamily(windows.AF_INET)
	if ipv6 {
		family = windows.AF_INET6
	}
	rows, err := winipcfg.GetIPForwardTable2(family)
	if err != nil {
		return Interface{}, false, err
	}
	lowestMetric := ^uint64(0)
	for i := range rows {
		if rows[i].DestinationPrefix.PrefixLength != 0 || uint64(rows[i].InterfaceLUID) == exclude {
			continue
		}
		ifrow, err := rows[i].InterfaceLUID.Interface()
		if err != nil || ifrow.OperStatus != winipcfg.IfOperStatusUp {
			continue
		}
		ipif, err := rows[i].InterfaceLUID.IPInterface(family)
		if err != nil {
			continue
		}
		combinedMetric := uint64(rows[i].Metric) + uint64(ipif.Metric)
		if combinedMetric < lowestMetric {
			lowestMetric = combinedMetric
			iface = Interface{
				Index: rows[i].InterfaceIndex,
				LUID:  uint64(rows[i].InterfaceLUID),
				Alias: ifrow.Alias(),
			}
			ok = true
		}
	}
	return iface, ok, nil
}
