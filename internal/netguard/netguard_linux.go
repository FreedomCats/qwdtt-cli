package netguard

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"

	"github.com/FreedomCats/qwdtt-cli/internal/linuxsys"
)

const supported = true

// bindPerFamily — на Linux привязка к интерфейсу одна на сокет (см. apply).
const bindPerFamily = false

// Опции привязки — для текста ошибок.
const (
	option4 = "SO_BINDTOIFINDEX"
	option6 = "SO_BINDTOIFINDEX"
)

// setter привязывает сокет к интерфейсу и, если задан Mark, метит его.
//
// Привязка побеждает маршруты /1 в туннель и прав не требует (plan-e4.md,
// врезка N1, пп. 1–2). Без маршрута для семейства на этом интерфейсе
// сокет получает ENETUNREACH, а не уходит в туннель (п. 5).
func (g *Guard) setter() setter {
	mark := g.Mark
	return func(fd uintptr, _ bool, index uint32) error {
		if err := bindToIndex(int(fd), index); err != nil {
			return err
		}
		if mark == 0 {
			return nil
		}
		if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_MARK, int(mark)); err != nil {
			return fmt.Errorf("SO_MARK=%#x: %w", mark, err)
		}
		return nil
	}
}

// bindToIndex — SO_BINDTOIFINDEX (ядро 5.0 и новее), а на старом ядре —
// SO_BINDTODEVICE по имени интерфейса.
func bindToIndex(fd int, index uint32) error {
	err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_BINDTOIFINDEX, int(index))
	if !errors.Is(err, unix.ENOPROTOOPT) {
		return err
	}
	iface, ierr := net.InterfaceByIndex(int(index))
	if ierr != nil {
		return fmt.Errorf("SO_BINDTOIFINDEX нет в ядре, а имя интерфейса не узнать: %w", ierr)
	}
	return unix.BindToDevice(fd, iface.Name)
}

// choose выбирает интерфейс (linuxsys.DefaultInterface) без установки: Watch
// сперва сравнивает его с прежним. exclude — индекс своего адаптера.
func choose(exclude uint64) (Choice, error) {
	v4, ok4, err := linuxsys.DefaultInterface(false, exclude)
	if err != nil {
		return Choice{}, fmt.Errorf("netguard: выбор интерфейса IPv4: %w", err)
	}
	v6, ok6, err := linuxsys.DefaultInterface(true, exclude)
	if err != nil {
		return Choice{}, fmt.Errorf("netguard: выбор интерфейса IPv6: %w", err)
	}
	var c Choice
	if ok4 {
		c.Index4, c.Alias4 = v4.Index, v4.Name
	}
	if ok6 {
		c.Index6, c.Alias6 = v6.Index, v6.Name
	}
	return c, nil
}

// subscribe — события netlink о маршрутах, интерфейсах и адресах
// (linuxsys.WatchNetwork).
func subscribe(notify func(id uint64)) (func(), error) {
	return linuxsys.WatchNetwork(notify)
}
