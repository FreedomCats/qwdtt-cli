//go:build linux

// Команда checktunnel печатает снимок состояния системы для живой проверки
// qwdtt-cli (plan-e4.md, R1 и R2): что qwdtt-cli ставит в систему и что должно уйти после
// него.
//
//	go run ./scripts/checktunnel [-label "до запуска"] [-name qwdtt-cli] [-timeout 10s] [-offline]
//
// Вывод до запуска qwdtt-cli, во время и после — это и есть отчёт сценария.
// Разделы на Linux:
//
//   - процессы qwdtt-cli и блокировка адаптера (абстрактный сокет);
//   - интерфейсы: поднятые с адресами (кроме lo) и адаптер туннеля в любом
//     состоянии — тип, MTU, адреса;
//   - маршруты /0 и /1 обоих семейств во всех таблицах, кроме local;
//   - правила маршрутизации (ip rule): qwdtt-cli своих не ставит, нестандартные —
//     чужие;
//   - интерфейс для сокетов ядра — тот, что выбрал бы netguard;
//   - внешний адрес и резолвер — как на Windows (common.go);
//   - nftables: все таблицы, подробно — inet qwdtt-cli (защита DNS) и inet
//     qwdtt-cli-outage (scripts/outage);
//   - DNS системы: resolv.conf, способ, которым qwdtt-cli задал бы DNS туннеля,
//     записи resolvconf, копия перезаписанного файла;
//   - rp_filter и строгий обратный фильтр IPv6 firewalld.
//
// Команда ничего не меняет в системе. Без прав root (не под sudo) раздел
// nftables печатает ошибку вместо данных; остальное работает.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/FreedomCats/qwdtt-cli/internal/app"
	"github.com/FreedomCats/qwdtt-cli/internal/linuxsys"
)

// tunLock — абстрактный сокет блокировки адаптера
// (internal/datapath/tun_linux.go, tunLockName).
const tunLock = "@" + app.Name + "-tun"

// fileMarker — начало первой строки resolv.conf, написанного qwdtt-cli
// (internal/linuxsys/dns_linux.go).
const fileMarker = "# " + app.Name + ": DNS туннеля"

// outageTable — таблица обрыва scripts/outage.
const outageTable = app.Name + "-outage"

// Корни /proc, /etc и /run; подменяются в тестах.
var (
	procRoot = "/proc"
	etcRoot  = "/etc"
	runRoot  = "/run"
)

type options struct {
	label   string
	name    string        // имя адаптера туннеля
	timeout time.Duration // на каждый сетевой запрос
	offline bool          // без внешнего адреса и резолвера
}

func main() {
	var o options
	flag.StringVar(&o.label, "label", "", "подпись снимка (например, «до запуска»)")
	flag.StringVar(&o.name, "name", app.Name, "имя адаптера туннеля")
	flag.DurationVar(&o.timeout, "timeout", 10*time.Second, "таймаут каждого сетевого запроса")
	flag.BoolVar(&o.offline, "offline", false, "не проверять внешний адрес и резолвер")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	run(os.Stdout, o)
}

// run печатает снимок. Ошибка одного раздела печатается в нём же и не
// мешает остальным.
func run(w io.Writer, o options) {
	header(w, o.label)

	section(w, "Процессы qwdtt-cli", func(w io.Writer) error { return printProcesses(w) })
	links, linksErr := netlink.LinkList()
	section(w, "Интерфейсы", func(w io.Writer) error {
		if linksErr != nil {
			return linksErr
		}
		return printInterfaces(w, links, o.name)
	})
	section(w, "Маршруты /0 и /1", func(w io.Writer) error { return printRoutes(w, links) })
	section(w, "Правила маршрутизации", func(w io.Writer) error { return printRules(w) })
	section(w, "Интерфейс для сокетов ядра", func(w io.Writer) error {
		printBypass(w, links, o.name)
		return nil
	})
	if !o.offline {
		printNetwork(w, o.timeout)
	}
	section(w, "nftables", func(w io.Writer) error { return printNftables(w) })
	section(w, "DNS системы", func(w io.Writer) error {
		printDNS(w, o.name)
		return nil
	})
	section(w, "Обратный фильтр", func(w io.Writer) error {
		printReversePath(w, links, o.name)
		return nil
	})
}

// printProcesses — процессы с именем qwdtt-cli (comm) и блокировка адаптера.
func printProcesses(w io.Writer) error {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return err
	}
	n := 0
	for _, e := range entries {
		if _, err := strconv.Atoi(e.Name()); err != nil {
			continue
		}
		dir := filepath.Join(procRoot, e.Name())
		comm, err := os.ReadFile(filepath.Join(dir, "comm"))
		if err != nil || strings.TrimSpace(string(comm)) != app.Name {
			continue
		}
		n++
		status, _ := os.ReadFile(filepath.Join(dir, "status"))
		cmdline, _ := os.ReadFile(filepath.Join(dir, "cmdline"))
		fmt.Fprintf(w, "   PID %s, uid %s: %s\n", e.Name(), statusUID(status), cmdlineString(cmdline))
	}
	if n == 0 {
		fmt.Fprintln(w, "   нет")
	}
	unixSockets, err := os.ReadFile(filepath.Join(procRoot, "net/unix"))
	if err != nil {
		fmt.Fprintf(w, "   блокировка %s: ошибка: %v\n", tunLock, err)
		return nil
	}
	state := "свободна"
	if hasUnixSocket(unixSockets, tunLock) {
		state = "занята"
	}
	fmt.Fprintf(w, "   блокировка %s: %s\n", tunLock, state)
	return nil
}

// statusUID — действующий uid из /proc/<pid>/status (строка «Uid: real
// effective saved fs»).
func statusUID(status []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(status))
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "Uid:"); ok {
			if f := strings.Fields(rest); len(f) >= 2 {
				return f[1]
			}
		}
	}
	return "?"
}

// cmdlineString — командная строка из /proc/<pid>/cmdline: аргументы через
// нулевой байт.
func cmdlineString(b []byte) string {
	args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
	return strings.Join(args, " ")
}

// hasUnixSocket — есть ли в /proc/net/unix сокет с путём path (абстрактные
// печатаются с «@» вместо нулевого байта).
func hasUnixSocket(table []byte, path string) bool {
	sc := bufio.NewScanner(bytes.NewReader(table))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 8 && f[len(f)-1] == path {
			return true
		}
	}
	return false
}

// printInterfaces печатает поднятые интерфейсы с адресами (кроме lo) и
// адаптер туннеля в любом состоянии.
func printInterfaces(w io.Writer, links []netlink.Link, name string) error {
	tunnelSeen := false
	for _, l := range links {
		a := l.Attrs()
		isTunnel := a.Name == name
		if a.Flags&net.FlagLoopback != 0 && !isTunnel {
			continue
		}
		addrs, err := netlink.AddrList(l, netlink.FAMILY_ALL)
		if err != nil {
			return fmt.Errorf("адреса %s: %w", a.Name, err)
		}
		var ips []string
		for _, ad := range addrs {
			p, ok := ipNetPrefix(ad.IPNet)
			if !ok || (p.Addr().IsLinkLocalUnicast() && !isTunnel) {
				continue
			}
			ips = append(ips, p.String())
		}
		if !isTunnel && (a.Flags&net.FlagUp == 0 || len(ips) == 0) {
			continue
		}
		mark := ""
		if isTunnel {
			tunnelSeen = true
			mark = "  [туннель " + name + "]"
		}
		fmt.Fprintf(w, "   %s (индекс %d, %s, связь %s, MTU %d, %s)%s\n",
			a.Name, a.Index, adminState(a.Flags), a.OperState, a.MTU, l.Type(), mark)
		fmt.Fprintf(w, "      адреса: %s\n", listOrNone(ips))
	}
	if !tunnelSeen {
		fmt.Fprintf(w, "   адаптера туннеля %q нет\n", name)
	}
	return nil
}

func adminState(f net.Flags) string {
	if f&net.FlagUp != 0 {
		return "UP"
	}
	return "DOWN"
}

func ipNetPrefix(n *net.IPNet) (netip.Prefix, bool) {
	if n == nil {
		return netip.Prefix{}, false
	}
	addr, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	ones, _ := n.Mask.Size()
	return netip.PrefixFrom(addr.Unmap(), ones), true
}

// printRoutes — маршруты /0 и /1 всех таблиц, кроме local. Маршрут с
// несколькими шлюзами печатается строкой на каждый.
func printRoutes(w io.Writer, links []netlink.Link) error {
	names := map[int]string{}
	for _, l := range links {
		names[l.Attrs().Index] = l.Attrs().Name
	}
	all, err := netlink.RouteListFiltered(netlink.FAMILY_ALL,
		&netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return err
	}
	var routes []route
	for _, r := range all {
		if r.Table == unix.RT_TABLE_LOCAL {
			continue
		}
		p, ok := routePrefix(r)
		if !ok || !interestingRoute(p) {
			continue
		}
		for _, hop := range routeHops(r) {
			routes = append(routes, route{
				prefix:  p,
				nextHop: hop.gw,
				iface:   names[hop.index],
				index:   uint32(hop.index),
				metric:  uint32(r.Priority),
				table:   r.Table,
				proto:   r.Protocol.String(),
			})
		}
	}
	sortRoutes(routes)
	if len(routes) == 0 {
		fmt.Fprintln(w, "   нет")
	}
	for _, r := range routes {
		fmt.Fprintf(w, "   %-14s %-22s %s (индекс %d), метрика %d, %s%s\n",
			r.prefix, hopString(r.nextHop), r.iface, r.index, r.metric, r.proto, tableSuffix(r.table))
	}
	return nil
}

// routePrefix — назначение маршрута. Маршрут по умолчанию netlink отдаёт с
// Dst = nil или с нулевой длиной префикса, смотря по версии и семейству.
func routePrefix(r netlink.Route) (netip.Prefix, bool) {
	if r.Dst != nil {
		return ipNetPrefix(r.Dst)
	}
	switch r.Family {
	case unix.AF_INET:
		return netip.PrefixFrom(netip.IPv4Unspecified(), 0), true
	case unix.AF_INET6:
		return netip.PrefixFrom(netip.IPv6Unspecified(), 0), true
	}
	return netip.Prefix{}, false
}

type hop struct {
	index int
	gw    netip.Addr
}

func routeHops(r netlink.Route) []hop {
	if len(r.MultiPath) == 0 {
		return []hop{{index: r.LinkIndex, gw: ipAddr(r.Gw)}}
	}
	hops := make([]hop, 0, len(r.MultiPath))
	for _, nh := range r.MultiPath {
		hops = append(hops, hop{index: nh.LinkIndex, gw: ipAddr(nh.Gw)})
	}
	return hops
}

func ipAddr(ip net.IP) netip.Addr {
	a, _ := netip.AddrFromSlice(ip)
	return a.Unmap()
}

// tableSuffix — таблица маршрута, если она не main.
func tableSuffix(table int) string {
	if table == unix.RT_TABLE_MAIN {
		return ""
	}
	return fmt.Sprintf(", таблица %d", table)
}

// printRules — правила маршрутизации обоих семейств. qwdtt-cli своих не ставит
// (привязка сокетов, а не fwmark: plan-e4.md, решение 1).
func printRules(w io.Writer) error {
	for _, family := range []int{netlink.FAMILY_V4, netlink.FAMILY_V6} {
		rules, err := netlink.RuleList(family)
		if err != nil {
			return err
		}
		label := "IPv4"
		if family == netlink.FAMILY_V6 {
			label = "IPv6"
		}
		var lines []string
		extra := 0
		for _, r := range rules {
			s := ruleString(r)
			if standardRule(r) {
				s += "  [стандартное]"
			} else {
				extra++
			}
			lines = append(lines, s)
		}
		fmt.Fprintf(w, "   %s: нестандартных %d\n", label, extra)
		for _, l := range lines {
			fmt.Fprintf(w, "      %s\n", l)
		}
	}
	return nil
}

// ruleString — правило в духе ip rule: «32766: from all lookup main».
func ruleString(r netlink.Rule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d:", rulePriority(r))
	if r.Invert {
		b.WriteString(" not")
	}
	b.WriteString(" from ")
	if r.Src != nil {
		b.WriteString(r.Src.String())
	} else {
		b.WriteString("all")
	}
	if r.Dst != nil {
		b.WriteString(" to " + r.Dst.String())
	}
	if r.Mark != 0 || r.Mask != nil {
		fmt.Fprintf(&b, " fwmark %#x", r.Mark)
		if r.Mask != nil {
			fmt.Fprintf(&b, "/%#x", *r.Mask)
		}
	}
	if r.IifName != "" {
		b.WriteString(" iif " + r.IifName)
	}
	if r.OifName != "" {
		b.WriteString(" oif " + r.OifName)
	}
	if r.UIDRange != nil {
		fmt.Fprintf(&b, " uidrange %d-%d", r.UIDRange.Start, r.UIDRange.End)
	}
	if r.SuppressPrefixlen >= 0 {
		fmt.Fprintf(&b, " suppress_prefixlength %d", r.SuppressPrefixlen)
	}
	if r.Goto >= 0 {
		fmt.Fprintf(&b, " goto %d", r.Goto)
	} else {
		b.WriteString(" lookup " + tableName(r.Table))
	}
	return b.String()
}

// rulePriority — приоритет правила: у правила 0 ядро атрибут приоритета
// не присылает, и netlink оставляет -1.
func rulePriority(r netlink.Rule) int {
	return max(r.Priority, 0)
}

func tableName(t int) string {
	switch t {
	case unix.RT_TABLE_LOCAL:
		return "local"
	case unix.RT_TABLE_MAIN:
		return "main"
	case unix.RT_TABLE_DEFAULT:
		return "default"
	}
	return strconv.Itoa(t)
}

// standardRule — правило, которое ядро ставит само: 0 local, 32766 main,
// 32767 default, без условий.
func standardRule(r netlink.Rule) bool {
	plain := r.Src == nil && r.Dst == nil && r.Mark == 0 && r.Mask == nil && r.IifName == "" &&
		r.OifName == "" && r.UIDRange == nil && !r.Invert && r.Goto < 0 && r.SuppressPrefixlen < 0
	switch {
	case !plain:
		return false
	case rulePriority(r) == 0 && r.Table == unix.RT_TABLE_LOCAL,
		r.Priority == 32766 && r.Table == unix.RT_TABLE_MAIN,
		r.Priority == 32767 && r.Table == unix.RT_TABLE_DEFAULT:
		return true
	}
	return false
}

// printBypass — физический интерфейс, к которому netguard привязал бы
// сокеты ядра (без своего адаптера).
func printBypass(w io.Writer, links []netlink.Link, name string) {
	var exclude uint64
	for _, l := range links {
		if l.Attrs().Name == name {
			exclude = uint64(l.Attrs().Index)
		}
	}
	for _, ipv6 := range []bool{false, true} {
		label := "IPv4"
		if ipv6 {
			label = "IPv6"
		}
		iface, ok, err := linuxsys.DefaultInterface(ipv6, exclude)
		switch {
		case err != nil:
			fmt.Fprintf(w, "   %s: ошибка: %v\n", label, err)
		case !ok:
			fmt.Fprintf(w, "   %s: нет (маршрута по умолчанию нет)\n", label)
		default:
			fmt.Fprintf(w, "   %s: %s (индекс %d)\n", label, iface.Name, iface.Index)
		}
	}
}

// printNftables — все таблицы nftables; таблицы qwdtt-cli и обрыва — с цепочками
// и числом правил.
func printNftables(w io.Writer) error {
	conn, err := nftables.New()
	if err != nil {
		return err
	}
	tables, err := conn.ListTables()
	if err != nil {
		if errors.Is(err, unix.EPERM) {
			return fmt.Errorf("нужны права root (sudo): %w", err)
		}
		return err
	}
	if len(tables) == 0 {
		fmt.Fprintln(w, "   таблиц нет")
		return nil
	}
	slices.SortFunc(tables, func(a, b *nftables.Table) int {
		return strings.Compare(tableTitle(a), tableTitle(b))
	})
	var ours []*nftables.Table
	var names []string
	for _, t := range tables {
		names = append(names, tableTitle(t))
		if t.Family == nftables.TableFamilyINet && (t.Name == app.Name || t.Name == outageTable) {
			ours = append(ours, t)
		}
	}
	fmt.Fprintf(w, "   таблицы: %s\n", strings.Join(names, ", "))
	if len(ours) == 0 {
		fmt.Fprintln(w, "   таблиц inet qwdtt-cli и inet "+outageTable+" нет")
		return nil
	}
	chains, err := conn.ListChains()
	if err != nil {
		return fmt.Errorf("цепочки: %w", err)
	}
	for _, t := range ours {
		owner := "без владельца"
		if t.Flags&nftables.TableFlagOwner != 0 {
			owner = fmt.Sprintf("владелец — netlink-порт %d", t.Owner)
		}
		fmt.Fprintf(w, "   %s: %s\n", tableTitle(t), owner)
		for _, c := range chains {
			if c.Table.Name != t.Name || c.Table.Family != t.Family {
				continue
			}
			rules, err := conn.GetRules(t, c)
			count := strconv.Itoa(len(rules))
			if err != nil {
				count = "ошибка: " + err.Error()
			}
			fmt.Fprintf(w, "      %s: %s, правил %s\n", c.Name, chainHook(c), count)
		}
	}
	return nil
}

func tableTitle(t *nftables.Table) string {
	return familyName(t.Family) + " " + t.Name
}

func familyName(f nftables.TableFamily) string {
	switch f {
	case nftables.TableFamilyINet:
		return "inet"
	case nftables.TableFamilyIPv4:
		return "ip"
	case nftables.TableFamilyIPv6:
		return "ip6"
	case nftables.TableFamilyARP:
		return "arp"
	case nftables.TableFamilyNetdev:
		return "netdev"
	case nftables.TableFamilyBridge:
		return "bridge"
	}
	return fmt.Sprintf("семейство %d", f)
}

// chainHook — хук, приоритет и политика базовой цепочки.
func chainHook(c *nftables.Chain) string {
	if c.Hooknum == nil {
		return "обычная"
	}
	hooks := map[nftables.ChainHook]string{
		*nftables.ChainHookPrerouting:  "prerouting",
		*nftables.ChainHookInput:       "input",
		*nftables.ChainHookForward:     "forward",
		*nftables.ChainHookOutput:      "output",
		*nftables.ChainHookPostrouting: "postrouting",
	}
	hook, ok := hooks[*c.Hooknum]
	if !ok {
		hook = fmt.Sprintf("хук %d", *c.Hooknum)
	}
	s := "хук " + hook
	if c.Priority != nil {
		s += fmt.Sprintf(", приоритет %d", *c.Priority)
	}
	if c.Policy != nil {
		policy := "accept"
		if *c.Policy == nftables.ChainPolicyDrop {
			policy = "drop"
		}
		s += ", политика " + policy
	}
	return s
}

// printDNS — resolv.conf, способ qwdtt-cli и следы qwdtt-cli: запись resolvconf и копия
// перезаписанного файла.
func printDNS(w io.Writer, name string) {
	path := filepath.Join(etcRoot, "resolv.conf")
	if fi, err := os.Lstat(path); err != nil {
		fmt.Fprintf(w, "   %s: %v\n", path, err)
	} else if fi.Mode()&fs.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			target = "никуда не ведёт: " + err.Error()
		}
		fmt.Fprintf(w, "   %s → %s\n", path, target)
	} else {
		fmt.Fprintf(w, "   %s — обычный файл\n", path)
	}
	if b, err := os.ReadFile(path); err == nil {
		first, servers, search := resolvConfSummary(b)
		if first != "" {
			fmt.Fprintf(w, "      первая строка: %s\n", first)
		}
		fmt.Fprintf(w, "      nameserver: %s\n", listOrNone(servers))
		fmt.Fprintf(w, "      доменов search: %d\n", search)
		if strings.HasPrefix(first, fileMarker) {
			fmt.Fprintln(w, "      файл написал qwdtt-cli")
		}
	}

	backup := path + "." + app.Name + "-backup"
	if _, err := os.Stat(backup); err == nil {
		fmt.Fprintf(w, "   копия %s: есть\n", backup)
	} else {
		fmt.Fprintf(w, "   копия %s: нет\n", backup)
	}

	if s, err := linuxsys.DetectSystemDNS(name); err != nil {
		fmt.Fprintf(w, "   способ qwdtt-cli: ошибка: %v\n", err)
	} else {
		fmt.Fprintf(w, "   способ qwdtt-cli: %s\n", s)
		if s.Warning() != "" {
			fmt.Fprintf(w, "      внимание: %s\n", s.Warning())
		}
	}

	printResolvconfRecords(w, "tun."+name)
}

// resolvConfSummary — первая строка-комментарий, серверы и число доменов
// search из resolv.conf.
func resolvConfSummary(conf []byte) (first string, servers []string, search int) {
	sc := bufio.NewScanner(bytes.NewReader(conf))
	for i := 0; sc.Scan(); i++ {
		line := strings.TrimSpace(sc.Text())
		if i == 0 && strings.HasPrefix(line, "#") {
			first = line
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "nameserver":
			servers = append(servers, f[1])
		case "search":
			search = len(f) - 1
		}
	}
	return first, servers, search
}

// printResolvconfRecords — записи resolvconf (Debian держит их в
// interface, openresolv — в interfaces); у записи qwdtt-cli — её серверы.
func printResolvconfRecords(w io.Writer, record string) {
	for _, dir := range []string{"interface", "interfaces"} {
		full := filepath.Join(runRoot, "resolvconf", dir)
		entries, err := os.ReadDir(full)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			fmt.Fprintf(w, "   записи %s: ошибка: %v\n", full, err)
			continue
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		fmt.Fprintf(w, "   записи %s: %s\n", full, listOrNone(names))
		if slices.Contains(names, record) {
			b, err := os.ReadFile(filepath.Join(full, record))
			if err != nil {
				fmt.Fprintf(w, "      %s: ошибка: %v\n", record, err)
				continue
			}
			_, servers, _ := resolvConfSummary(b)
			fmt.Fprintf(w, "      %s: nameserver %s\n", record, listOrNone(servers))
		}
	}
}

// printReversePath — rp_filter: all, default, поднятые интерфейсы с
// адресом IPv4 и адаптер туннеля; действует большее из all и значения
// интерфейса. rp_filter — только про IPv4.
func printReversePath(w io.Writer, links []netlink.Link, name string) {
	all, errAll := readRPFilter("all")
	def, errDef := readRPFilter("default")
	fmt.Fprintf(w, "   all %s, default %s\n", rpValue(all, errAll), rpValue(def, errDef))
	for _, l := range links {
		a := l.Attrs()
		if a.Name != name {
			if a.Flags&net.FlagLoopback != 0 || a.Flags&net.FlagUp == 0 {
				continue
			}
			if v4, err := netlink.AddrList(l, netlink.FAMILY_V4); err == nil && len(v4) == 0 {
				continue
			}
		}
		own, err := readRPFilter(a.Name)
		if err != nil {
			fmt.Fprintf(w, "   %s: ошибка: %v\n", a.Name, err)
			continue
		}
		eff := own
		if errAll == nil {
			eff = max(all, own)
		}
		fmt.Fprintf(w, "   %s: %d → действует %s\n", a.Name, own, rpValue(eff, nil))
	}
	strict, err := linuxsys.FirewalldStrictIPv6()
	switch {
	case err != nil:
		fmt.Fprintf(w, "   firewalld: ошибка: %v\n", err)
	case strict:
		fmt.Fprintln(w, "   firewalld: строгий обратный фильтр IPv6 — сокеты ядра qwdtt-cli только IPv4")
	default:
		fmt.Fprintln(w, "   firewalld: строгого обратного фильтра IPv6 нет")
	}
}

func readRPFilter(iface string) (int, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, "sys/net/ipv4/conf", iface, "rp_filter"))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func rpValue(v int, err error) string {
	if err != nil {
		return "ошибка: " + err.Error()
	}
	switch v {
	case linuxsys.RPFilterOff:
		return "0 (выключен)"
	case linuxsys.RPFilterStrict:
		return "1 (строгий)"
	case linuxsys.RPFilterLoose:
		return "2 (нестрогий)"
	}
	return strconv.Itoa(v)
}
