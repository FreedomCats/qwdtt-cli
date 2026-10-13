package netguard

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

const supported = true

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
