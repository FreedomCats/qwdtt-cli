package datapath

import (
	"fmt"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// startDevice поднимает wireguard-go поверх готового TUN по конфигу сервера.
//
// Это общая часть socks и wintun: они отличаются только TUN (architecture.md
// §04) — netstack у первого, адаптер Wintun у второго. Bind — стандартный UDP:
// wireguard-go шлёт пакеты на Endpoint из конфига, то есть на loopback ядра
// (решение Р4), и хук netguard ему не нужен — loopback маршрутами туннеля
// не затрагивается.
//
// С момента вызова tunDev принадлежит startDevice: на отказе он закрыт, на
// успехе его закроет Close устройства. tag — префикс строк лога wireguard-go,
// например "[WG-SOCKS]"; logf обязан быть непустым.
//
// Конфиг применяется к свежему устройству, поэтому replace_peers не нужен.
// Перенастройка живого устройства (S1) — отдельная функция: IPCRequest его
// не пишет, и второй IpcSet добавил бы пира к старому, а не заменил.
func startDevice(tunDev tun.Device, conf *WGConfig, tag string, logf func(format string, args ...any)) (*device.Device, error) {
	// Свой Logger вместо device.NewLogger: тот пишет прямо в stdout, а вывод
	// принадлежит CLI. Verbosef — DiscardLogf: это тот же уровень, что был
	// в ядре (LogLevelError), и обе функции обязаны быть непустыми — wireguard-go
	// зовёт их без проверки на nil, в том числе из defer своих горутин.
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), &device.Logger{
		Verbosef: device.DiscardLogf,
		Errorf: func(format string, args ...any) {
			logf(tag+" "+format, args...)
		},
	})
	// Close устройства закрывает и TUN, отдельно его закрывать не нужно.
	if err := dev.IpcSet(conf.IPCRequest()); err != nil {
		dev.Close()
		return nil, fmt.Errorf("IpcSet: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("Up: %w", err)
	}
	return dev, nil
}
