package datapath

import (
	"fmt"
	"os"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"
)

// wgDebugEnv — переменная окружения: TT_WG_DEBUG=1 пишет в лог подробные
// строки wireguard-go (рукопожатия, keepalive, ключи сессий). Для разбора
// живых прогонов; по умолчанию — только ошибки.
const wgDebugEnv = "TT_WG_DEBUG"

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
// Перенастройка живого устройства — reconfigureDevice.
func startDevice(tunDev tun.Device, conf *WGConfig, tag string, logf func(format string, args ...any)) (*device.Device, error) {
	// Свой Logger вместо device.NewLogger: тот пишет прямо в stdout, а вывод
	// принадлежит CLI. Verbosef — DiscardLogf (без TT_WG_DEBUG): это тот же
	// уровень, что был в ядре (LogLevelError), и обе функции обязаны быть
	// непустыми — wireguard-go
	// зовёт их без проверки на nil, в том числе из defer своих горутин.
	verbosef := device.DiscardLogf
	if os.Getenv(wgDebugEnv) != "" {
		verbosef = func(format string, args ...any) {
			logf(tag+" "+format, args...)
		}
	}
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), &device.Logger{
		Verbosef: verbosef,
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

// reconfigureDevice применяет к живому устройству новый конфиг сервера —
// ключ, пира и endpoint на loopback ядра, которые на каждое подключение
// бывают новыми. replace_peers убирает прежнего пира: без него IpcSet
// добавил бы нового рядом со старым. TUN, а с ним и адаптер, остаётся.
//
// Сокеты устройства перед этим открываются заново (BindUpdate). Прошлая
// попытка могла оставить wireguard-go без приёма: когда ядро закрывает
// свой порт на loopback, а рукопожатие туда ещё летит, Windows отвечает на
// ICMP «порт недоступен» ошибкой WSAECONNRESET на приёме, и приёмная
// горутина wireguard-go выходит насовсем (device/receive.go). Рукопожатия
// после этого уходят, а ответы никто не читает (R2, сценарий 4).
func reconfigureDevice(dev *device.Device, conf *WGConfig) error {
	if err := dev.BindUpdate(); err != nil {
		return fmt.Errorf("BindUpdate: %w", err)
	}
	if err := dev.IpcSet("replace_peers=true\n" + conf.IPCRequest()); err != nil {
		return fmt.Errorf("IpcSet: %w", err)
	}
	return nil
}
