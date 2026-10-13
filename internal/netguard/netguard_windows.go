package netguard

import (
	"encoding/binary"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/FreedomCats/qwdtt-cli/internal/winsys"
)

const supported = true

// bindPerFamily — у сокета своя привязка для IPv4 и для IPv6.
const bindPerFamily = true

// Опции привязки — для текста ошибок.
const (
	option4 = "IP_UNICAST_IF"
	option6 = "IPV6_UNICAST_IF"
)

// setter на Windows — только привязка: Mark здесь не используется.
func (g *Guard) setter() setter { return setUnicastIf }

// choose выбирает интерфейс (winsys.DefaultInterface) без установки: Watch
// сперва сравнивает его с прежним. exclude — LUID своего адаптера.
func choose(exclude uint64) (Choice, error) {
	v4, ok4, err := winsys.DefaultInterface(false, exclude)
	if err != nil {
		return Choice{}, fmt.Errorf("netguard: выбор интерфейса IPv4: %w", err)
	}
	v6, ok6, err := winsys.DefaultInterface(true, exclude)
	if err != nil {
		return Choice{}, fmt.Errorf("netguard: выбор интерфейса IPv6: %w", err)
	}
	var c Choice
	if ok4 {
		c.Index4, c.Alias4 = v4.Index, v4.Alias
	}
	if ok6 {
		c.Index6, c.Alias6 = v6.Index, v6.Alias
	}
	return c, nil
}

// subscribe — события смены маршрутов и интерфейсов (winsys.WatchNetwork).
func subscribe(notify func(id uint64)) (func(), error) {
	return winsys.WatchNetwork(notify)
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
