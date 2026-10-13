package winsys

import (
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// WatchNetwork подписывается на смену маршрутов (NotifyRouteChange2) и
// IP-интерфейсов (NotifyIpInterfaceChange) обоих семейств и зовёт notify
// с LUID интерфейса, которого коснулось изменение (задача S2 plan-e2.md).
//
// Сами события ничего не решают: переключение Wi-Fi → Ethernet даёт их
// десятки, а изменения своего адаптера (маршруты туннеля) выбора
// физического интерфейса не меняют. Сворачивает серию и решает, сменилась
// ли сеть, вызывающий (netguard.Guard.Watch).
//
// notify зовётся из горутин winipcfg, возможно одновременно. stop снимает
// подписку и ждёт уже начатых вызовов notify.
func WatchNetwork(notify func(luid uint64)) (stop func(), err error) {
	routes, err := winipcfg.RegisterRouteChangeCallback(func(_ winipcfg.MibNotificationType, route *winipcfg.MibIPforwardRow2) {
		notify(uint64(route.InterfaceLUID))
	})
	if err != nil {
		return nil, err
	}
	ifaces, err := winipcfg.RegisterInterfaceChangeCallback(func(_ winipcfg.MibNotificationType, iface *winipcfg.MibIPInterfaceRow) {
		notify(uint64(iface.InterfaceLUID))
	})
	if err != nil {
		_ = routes.Unregister()
		return nil, err
	}
	return func() {
		_ = ifaces.Unregister()
		_ = routes.Unregister()
	}, nil
}
