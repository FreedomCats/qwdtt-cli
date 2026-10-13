package linuxsys

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// minMTU6 — наименьший MTU, при котором ядро держит IPv6 на интерфейсе
// (RFC 8200). С меньшим MTU ядро снимает с интерфейса IPv6 целиком.
const minMTU6 = 1280

// LinkKind — тип интерфейса с именем name, как его называет ip -d link
// («tuntap», «wireguard», «dummy»); ok = false — интерфейса нет.
func LinkKind(name string) (kind string, ok bool, err error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		var notFound netlink.LinkNotFoundError
		if errors.As(err, &notFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return link.Type(), true, nil
}

// LinkIndex — индекс интерфейса name.
func LinkIndex(name string) (int, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return 0, err
	}
	return link.Attrs().Index, nil
}

// ConfigureAdapter задаёт своему адаптеру MTU и адреса и поднимает его.
// Аналог winsys.ConfigureAdapter: каждый адрес — префиксом во всю длину
// (/32, /128), маршруты ставит вызывающий. Повторный вызов — перенастройка:
// адреса глобальной области, которых нет в addrs, снимаются (на своём
// адаптере все они наши), автоматический fe80:: не трогается.
//
// MTU меньше 1280 IPv6 на интерфейсе не оставляет: адреса IPv6 тогда не
// ставятся, о чём пишется в logf, — IPv6 через туннель всё равно не
// работает, а подъём из-за него не падает (как на Windows).
func ConfigureAdapter(index int, addrs []netip.Addr, mtu int, logf func(string, ...any)) error {
	link, err := netlink.LinkByIndex(index)
	if err != nil {
		return fmt.Errorf("интерфейс %d: %w", index, err)
	}
	name := link.Attrs().Name
	if mtu > 0 && link.Attrs().MTU != mtu {
		if err := netlink.LinkSetMTU(link, mtu); err != nil {
			return fmt.Errorf("MTU %d на %s: %w", mtu, name, err)
		}
	}

	want := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		a = a.Unmap()
		if a.Is6() && mtu > 0 && mtu < minMTU6 {
			logf("[TUN] адрес %v не поставлен: при MTU %d ядро не держит IPv6 на интерфейсе", a, mtu)
			continue
		}
		want = append(want, a)
	}

	have, err := netlink.AddrList(link, netlink.FAMILY_ALL)
	if err != nil {
		return fmt.Errorf("адреса %s: %w", name, err)
	}
	for _, h := range have {
		if h.Scope != unix.RT_SCOPE_UNIVERSE {
			continue
		}
		a, ok := netip.AddrFromSlice(h.IP)
		if ok && slices.Contains(want, a.Unmap()) {
			continue
		}
		if err := netlink.AddrDel(link, &h); err != nil {
			return fmt.Errorf("снятие адреса %v с %s: %w", h.IPNet, name, err)
		}
	}

	for _, a := range want {
		addr := &netlink.Addr{IPNet: &net.IPNet{
			IP:   a.AsSlice(),
			Mask: net.CIDRMask(a.BitLen(), a.BitLen()),
		}}
		if a.Is6() {
			// Проверка дубликата на точке-точке бессмысленна, а пока она
			// идёт, адрес «предварительный» и сокеты к нему не привязать.
			addr.Flags = unix.IFA_F_NODAD
		}
		if err := netlink.AddrReplace(link, addr); err != nil {
			return fmt.Errorf("адрес %v на %s: %w", a, name, err)
		}
	}

	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("подъём %s: %w", name, err)
	}
	return nil
}
