package netguard

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/FreedomCats/qwdtt-cli/internal/winsys"
)

const supported = true

// SelectInterfaces выбирает физический интерфейс маршрута по умолчанию для
// IPv4 и IPv6 (winsys.DefaultInterface) и ставит их индексы. exclude — LUID
// своего адаптера, 0 — исключать нечего. Возвращает выбор словами для вывода.
//
// Сети нет вовсе — не ошибка: индексы обнуляются, и сокеты ядра откажут
// с понятным текстом, а не уйдут в туннель. Ошибка — только отказ системы
// отдать таблицу маршрутов.
func (g *Guard) SelectInterfaces(exclude uint64) (string, error) {
	v4, ok4, err := winsys.DefaultInterface(false, exclude)
	if err != nil {
		return "", fmt.Errorf("netguard: выбор интерфейса IPv4: %w", err)
	}
	v6, ok6, err := winsys.DefaultInterface(true, exclude)
	if err != nil {
		return "", fmt.Errorf("netguard: выбор интерфейса IPv6: %w", err)
	}
	g.SetInterfaces(v4.Index, v6.Index)
	return "IPv4 — " + describe(v4, ok4) + ", IPv6 — " + describe(v6, ok6), nil
}

func describe(iface winsys.Interface, ok bool) string {
	if !ok {
		return "нет маршрута"
	}
	return fmt.Sprintf("%s (индекс %d)", iface.Alias, iface.Index)
}

// setUnicastIf привязывает сокет к интерфейсу. Порядок байт — как в
// wireguard-go, conn/bind_windows.go, bindSocketToInterface4/6: для IPv4
// индекс в сетевом порядке, для IPv6 — в машинном (ловушка из
// architecture.md §05). На Windows 11 перепутанный порядок роняет
// setsockopt (WSAEADDRNOTAVAIL / WSAEINVAL), на старых сборках, по
// документации, может молча игнорироваться — тест ловит оба исхода.
func setUnicastIf(fd uintptr, ipv6 bool, index uint32) error {
	if ipv6 {
		return bindSocketToInterface6(windows.Handle(fd), index)
	}
	return bindSocketToInterface4(windows.Handle(fd), index)
}

// Перенесено из golang.zx2c4.com/wireguard conn/bind_windows.go (MIT).
func bindSocketToInterface4(handle windows.Handle, interfaceIndex uint32) error {
	const IP_UNICAST_IF = 31
	/* MSDN says for IPv4 this needs to be in net byte order, so that it's like an IP address with leading zeros. */
	var bytes [4]byte
	binary.BigEndian.PutUint32(bytes[:], interfaceIndex)
	interfaceIndex = *(*uint32)(unsafe.Pointer(&bytes[0]))
	return windows.SetsockoptInt(handle, windows.IPPROTO_IP, IP_UNICAST_IF, int(interfaceIndex))
}

// Перенесено из golang.zx2c4.com/wireguard conn/bind_windows.go (MIT).
func bindSocketToInterface6(handle windows.Handle, interfaceIndex uint32) error {
	const IPV6_UNICAST_IF = 31
	return windows.SetsockoptInt(handle, windows.IPPROTO_IPV6, IPV6_UNICAST_IF, int(interfaceIndex))
}
