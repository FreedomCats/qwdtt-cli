//go:build linux

// Команда outage устраивает обрыв связи на время — для живых проверок
// переподключения tt (plan-e4.md, R1 и R2; на Windows — plan-e2.md, S1).
//
//	sudo go run ./scripts/outage -addr 198.51.100.20,198.51.100.21 [-for 60s]
//	sudo go run ./scripts/outage -link eth0 [-for 15s]
//
// -addr — обрыв связи с адресами: серверы TURN из лога tt (строка «Креды
// OK, TURN: […]»), как на Windows. Пакеты от них отбрасывает таблица
// nftables inet tt-outage (цепочка input); исходящие уходят, но ответа на
// них нет — как при настоящем обрыве. Исходящие не трогаются нарочно: drop
// в цепочке output ядро возвращает отправителю UDP ошибкой EPERM сразу, и
// tt видел бы ошибку отправки вместо тишины (проверено в R1). Таблица — с
// флагом owner: ядро удаляет её вместе с процессом — по истечении -for, по
// Ctrl+C и если процесс убит, как динамическую сессию WFP. Правила tt её
// не перебивают: accept в одной базовой цепочке не отменяет drop в другой.
//
// -link — пропажа физической сети: интерфейс выключается (ip link set
// down) и по истечении -for включается снова. Маршруты, которые ядро сняло
// вместе с ним, команда возвращает сама (кроме тех, что вернутся и так:
// маршруты подсети и от RA): в контейнере их больше некому вернуть. Если
// команду убить (kill -9), интерфейс останется выключенным — команда для
// возврата печатается при запуске.
//
// Обрыв ограничен по времени нарочно: пока туннель мёртв, а маршруты /1 tt
// стоят, весь трафик машины идёт в никуда — и сеть того, кто запустил
// проверку, тоже; снять обрыв руками в этот момент может быть нечем.
//
// Нужны права root (CAP_NET_ADMIN). На машине с NetworkManager -link
// проверять с осторожностью: он может по-своему отнестись к выключенному
// не им интерфейсу.
package main

import (
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// outageTable — таблица обрыва в nftables (`nft list table inet tt-outage`).
const outageTable = "tt-outage"

func main() {
	addrs := flag.String("addr", "", "адреса через запятую (IPv4 или IPv6): обрыв связи с ними")
	link := flag.String("link", "", "интерфейс: выключить его на время")
	dur := flag.Duration("for", 60*time.Second, "сколько держать обрыв")
	flag.Parse()
	if (*addrs == "") == (*link == "") || flag.NArg() > 0 || *dur <= 0 {
		fmt.Fprintln(os.Stderr, "outage: нужен ровно один из -addr и -link")
		flag.Usage()
		os.Exit(2)
	}
	var err error
	if *link != "" {
		err = runLink(*link, *dur)
	} else {
		var list []netip.Addr
		if list, err = parseAddrs(*addrs); err != nil {
			fmt.Fprintln(os.Stderr, "outage:", err)
			os.Exit(2)
		}
		err = runAddrs(list, *dur)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "outage:", err)
		os.Exit(1)
	}
}

// wait ждёт dur или сигнала остановки; возвращает, сколько прошло.
func wait(start time.Time, dur time.Duration) time.Duration {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(stop)
	select {
	case <-time.After(dur):
	case <-stop:
	}
	return time.Since(start).Round(time.Second)
}

func runAddrs(addrs []netip.Addr, dur time.Duration) error {
	conn, err := nftables.New(nftables.AsLasting())
	if err != nil {
		return fmt.Errorf("nftables: %w", err)
	}
	defer conn.CloseLasting()
	table := conn.CreateTable(&nftables.Table{
		Name:   outageTable,
		Family: nftables.TableFamilyINet,
		Flags:  nftables.TableFlagOwner,
	})
	accept := nftables.ChainPolicyAccept
	input := conn.AddChain(&nftables.Chain{
		Name:     "input",
		Table:    table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookInput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &accept,
	})
	for _, a := range addrs {
		conn.AddRule(&nftables.Rule{Table: table, Chain: input, Exprs: dropFrom(a)})
	}
	if err := conn.Flush(); err != nil {
		if errors.Is(err, unix.EPERM) {
			return fmt.Errorf("таблица inet %s (нужны права root; или её держит другой outage): %w", outageTable, err)
		}
		return fmt.Errorf("таблица inet %s: %w", outageTable, err)
	}
	start := time.Now()
	fmt.Printf("%s обрыв: входящие от %v отбрасываются, на %s\n", start.Format("15:04:05"), addrs, dur)

	took := wait(start, dur)
	dropped, countErr := droppedPackets(conn, table, input)
	if err := conn.CloseLasting(); err != nil {
		return fmt.Errorf("закрытие nftables: %w", err)
	}
	fmt.Printf("%s обрыв снят через %s", time.Now().Format("15:04:05"), took)
	if countErr == nil {
		fmt.Printf(", отброшено пакетов: %d", dropped)
	}
	fmt.Println()
	if countErr != nil {
		return fmt.Errorf("счётчики правил: %w", countErr)
	}
	return nil
}

// dropFrom — «meta nfproto <семейство> ip saddr <адрес> counter drop».
func dropFrom(a netip.Addr) []expr.Any {
	// Адрес источника: смещение 12 в заголовке IPv4, 8 — в IPv6.
	family, offset, size := byte(unix.NFPROTO_IPV4), uint32(12), uint32(4)
	if a.Is6() {
		family, offset, size = unix.NFPROTO_IPV6, 8, 16
	}
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{family}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: size},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: a.AsSlice()},
		&expr.Counter{},
		&expr.Verdict{Kind: expr.VerdictDrop},
	}
}

// droppedPackets — сумма счётчиков правил обрыва.
func droppedPackets(conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain) (uint64, error) {
	rules, err := conn.GetRules(table, chain)
	if err != nil {
		return 0, err
	}
	var n uint64
	for _, r := range rules {
		for _, e := range r.Exprs {
			if c, ok := e.(*expr.Counter); ok {
				n += c.Packets
			}
		}
	}
	return n, nil
}

func runLink(name string, dur time.Duration) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return fmt.Errorf("интерфейс %s: %w", name, err)
	}
	saved, err := netlink.RouteListFiltered(netlink.FAMILY_ALL,
		&netlink.Route{LinkIndex: link.Attrs().Index, Table: unix.RT_TABLE_UNSPEC},
		netlink.RT_FILTER_OIF|netlink.RT_FILTER_TABLE)
	if err != nil {
		return fmt.Errorf("маршруты %s: %w", name, err)
	}
	var restore []netlink.Route
	for _, r := range saved {
		if restorable(r) {
			restore = append(restore, r)
		}
	}
	if err := netlink.LinkSetDown(link); err != nil {
		return fmt.Errorf("выключение %s: %w", name, err)
	}
	start := time.Now()
	fmt.Printf("%s обрыв: %s выключен на %s (маршрутов к возврату: %d)\n",
		start.Format("15:04:05"), name, dur, len(restore))
	fmt.Printf("   если outage убьют, включить руками: sudo ip link set %s up\n", name)

	took := wait(start, dur)
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("включение %s (sudo ip link set %s up): %w", name, name, err)
	}
	added, existed, failed := restoreRoutes(restore)
	fmt.Printf("%s обрыв снят через %s: %s включён, маршрутов возвращено %d, уже были %d\n",
		time.Now().Format("15:04:05"), took, name, added, existed)
	for _, f := range failed {
		fmt.Printf("   не вернулся: %s\n", f)
	}
	return nil
}

// restorable — маршрут, который ядро снимет с выключенным интерфейсом и
// который сам не вернётся: не local, не маршрут подсети адреса (kernel) и
// не от RA.
func restorable(r netlink.Route) bool {
	return r.Table != unix.RT_TABLE_LOCAL && r.Protocol != unix.RTPROT_KERNEL && r.Protocol != unix.RTPROT_RA
}

// restoreRoutes возвращает маршруты. Второй проход — для тех, чей шлюз
// стал доступен только после возврата других.
func restoreRoutes(routes []netlink.Route) (added, existed int, failed []string) {
	pending := routes
	var errs map[int]error
	for pass := 0; pass < 2 && len(pending) > 0; pass++ {
		var next []netlink.Route
		errs = map[int]error{}
		for _, r := range pending {
			r.Flags &^= unix.RTNH_F_DEAD | unix.RTNH_F_LINKDOWN
			err := netlink.RouteAdd(&r)
			switch {
			case err == nil:
				added++
			case errors.Is(err, unix.EEXIST):
				existed++
			default:
				errs[len(next)] = err
				next = append(next, r)
			}
		}
		pending = next
	}
	for i, r := range pending {
		failed = append(failed, fmt.Sprintf("%s: %v", r, errs[i]))
	}
	return added, existed, failed
}
