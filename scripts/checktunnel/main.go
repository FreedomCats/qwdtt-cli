//go:build windows

// Команда checktunnel печатает снимок состояния системы для живой проверки
// qwdtt-cli (plan-e2.md, R2): что qwdtt-cli ставит в систему и что должно уйти после него.
//
//	go run ./scripts/checktunnel [-label "до запуска"] [-name qwdtt-cli] [-timeout 10s] [-offline]
//
// Вывод до запуска qwdtt-cli, во время и после — это и есть отчёт сценария.
// Разделы:
//
//   - процессы qwdtt-cli.exe;
//   - адаптеры: включённые с IP (кроме loopback и Teredo) и любые с именем
//     туннеля или постоянным GUID адаптера wintun, с адресами, MTU, метрикой
//     интерфейса (из winipcfg, а не из Get-NetIPInterface — там она бывает
//     пустой) и DNS;
//   - маршруты /0 и /1 обоих семейств — маршрут по умолчанию и половины
//     полного туннеля;
//   - внешний адрес IPv4 и IPv6 — HTTPS без прокси: в консоли сессии стоит
//     HTTPS_PROXY на локальный xray, который выходит на тот же VPS, и адрес
//     через него ничего не говорит о туннеле;
//   - резолвер: TXT o-o.myaddr.l.google.com системным резолвером (DnsQuery),
//     то есть адрес того, кто на самом деле спросил Google;
//   - фильтры WFP провайдера защиты DNS (winsys.DNSGuard);
//   - NRPT и ключи Tcpip адаптеров туннеля в реестре;
//   - для datapath external — служба WireGuardTunnel$<имя> и <имя>.conf.
//
// Команда ничего не меняет в системе. Без прав администратора часть разделов
// (WFP) печатает ошибку вместо данных; остальное работает.
//
// Снимок Linux — main_linux.go, общие разделы — common.go; macOS — в Э4.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unsafe"

	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"

	"github.com/FreedomCats/qwdtt-cli/internal/app"
)

// wintunGUID — постоянный GUID адаптера wintun
// (internal/datapath/tun_windows.go).
var wintunGUID = windows.GUID{
	Data1: 0xe46af63b, Data2: 0x12e0, Data3: 0x4377,
	Data4: [8]byte{0x97, 0xdb, 0x29, 0xf1, 0x24, 0xa7, 0x0f, 0x40},
}

// dnsGuardProvider — провайдер WFP защиты DNS
// (internal/winsys/dnsguard_windows.go).
var dnsGuardProvider = wf.ProviderID{
	Data1: 0x4ad3fe23, Data2: 0x87a7, Data3: 0x420b,
	Data4: [8]byte{0xbb, 0xab, 0xcd, 0x89, 0xcc, 0x92, 0x70, 0x9a},
}

const (
	nrptKey       = `SYSTEM\CurrentControlSet\Services\Dnscache\Parameters\DnsPolicyConfig`
	nrptPolicyKey = `SOFTWARE\Policies\Microsoft\Windows NT\DNSClient\DnsPolicyConfig`
	tcpipKey      = `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters\Interfaces\`
	tcpip6Key     = `SYSTEM\CurrentControlSet\Services\Tcpip6\Parameters\Interfaces\`
)

type options struct {
	label   string
	name    string        // имя туннеля: адаптер external, служба, файл конфига
	confDir string        // каталог <имя>.conf у external
	timeout time.Duration // на каждый сетевой запрос
	offline bool          // без внешнего адреса и резолвера
}

func main() {
	var o options
	flag.StringVar(&o.label, "label", "", "подпись снимка (например, «до запуска»)")
	flag.StringVar(&o.name, "name", app.Name, "имя туннеля: адаптер external, служба WireGuardTunnel$<имя>, <имя>.conf")
	flag.StringVar(&o.confDir, "conf-dir", defaultConfDir(), "каталог <имя>.conf у datapath external")
	flag.DurationVar(&o.timeout, "timeout", 10*time.Second, "таймаут каждого сетевого запроса")
	flag.BoolVar(&o.offline, "offline", false, "не проверять внешний адрес и резолвер")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	run(os.Stdout, o)
}

// defaultConfDir — каталог конфига external по умолчанию, как у
// datapath.External.dir.
func defaultConfDir() string {
	if base, err := os.UserConfigDir(); err == nil {
		return filepath.Join(base, app.Name)
	}
	return os.TempDir()
}

// run печатает снимок. Ошибка одного раздела печатается в нём же и не
// мешает остальным.
func run(w io.Writer, o options) {
	header(w, o.label)

	section(w, "Процессы qwdtt-cli.exe", func(w io.Writer) error { return printProcesses(w) })
	var guids []windows.GUID
	section(w, "Адаптеры", func(w io.Writer) (err error) {
		guids, err = printAdapters(w, o.name)
		return err
	})
	section(w, "Маршруты /0 и /1", func(w io.Writer) error { return printRoutes(w) })
	if !o.offline {
		printNetwork(w, o.timeout)
	}
	section(w, "WFP: защита DNS", func(w io.Writer) error { return printWFP(w) })
	section(w, "Реестр", func(w io.Writer) error {
		printRegistry(w, guids)
		return nil
	})
	section(w, "external", func(w io.Writer) error {
		printExternal(w, o.name, o.confDir)
		return nil
	})
}

func printProcesses(w io.Writer) error {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snap)
	var pids []uint32
	e := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), app.Name+".exe") {
			pids = append(pids, e.ProcessID)
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return err
	}
	if len(pids) == 0 {
		fmt.Fprintln(w, "   нет")
		return nil
	}
	for _, pid := range pids {
		fmt.Fprintf(w, "   PID %d\n", pid)
	}
	return nil
}

// printAdapters печатает включённые адаптеры с IP (кроме loopback и
// Teredo) и адаптеры туннеля в любом состоянии. Возвращает GUID адаптеров туннеля (для раздела реестра);
// постоянный GUID wintun в нём всегда.
func printAdapters(w io.Writer, name string) ([]windows.GUID, error) {
	guids := []windows.GUID{wintunGUID}
	addrs, err := winipcfg.GetAdaptersAddresses(windows.AF_UNSPEC, winipcfg.GAAFlagDefault)
	if err != nil {
		return guids, err
	}
	tunnelSeen := false
	for _, a := range addrs {
		guid, _ := a.LUID.GUID()
		isWintun := guid != nil && *guid == wintunGUID
		isTunnel := isWintun || strings.EqualFold(a.FriendlyName(), name)
		noise := a.IfType == winipcfg.IfTypeSoftwareLoopback || a.TunnelType == winipcfg.TunnelTypeTeredo
		if !isTunnel && (a.OperStatus != winipcfg.IfOperStatusUp || noise) {
			continue
		}
		mark := ""
		switch {
		case isWintun:
			mark = "  [qwdtt-cli wintun]"
		case isTunnel:
			mark = "  [туннель " + name + "]"
		}
		if isTunnel {
			tunnelSeen = true
			if guid != nil && *guid != wintunGUID {
				guids = append(guids, *guid)
			}
		}
		fmt.Fprintf(w, "   %s (индекс %d, %s, MTU %d)%s\n",
			a.FriendlyName(), a.IfIndex, operStatus(a.OperStatus), a.MTU, mark)
		if isTunnel {
			fmt.Fprintf(w, "      %s — %s\n", a.Description(), guidString(guid))
		}
		var ips []string
		for u := a.FirstUnicastAddress; u != nil; u = u.Next {
			ip := sockaddrAddr(u.Address)
			if ip.IsLinkLocalUnicast() && !isTunnel {
				continue
			}
			ips = append(ips, fmt.Sprintf("%s/%d", ip, u.OnLinkPrefixLength))
		}
		fmt.Fprintf(w, "      адреса: %s\n", listOrNone(ips))
		fmt.Fprintf(w, "      метрика: IPv4 %s, IPv6 %s\n",
			interfaceMetric(a.LUID, windows.AF_INET), interfaceMetric(a.LUID, windows.AF_INET6))
		var dns []string
		for d := a.FirstDNSServerAddress; d != nil; d = d.Next {
			dns = append(dns, sockaddrAddr(d.Address).String())
		}
		fmt.Fprintf(w, "      DNS: %s\n", listOrNone(dns))
	}
	if !tunnelSeen {
		fmt.Fprintf(w, "   адаптера туннеля (%q или GUID wintun) нет\n", name)
	}
	return guids, nil
}

// interfaceMetric — метрика интерфейса семейства из таблицы winipcfg.
func interfaceMetric(luid winipcfg.LUID, family winipcfg.AddressFamily) string {
	row, err := luid.IPInterface(family)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return "нет"
	}
	if err != nil {
		return "ошибка: " + err.Error()
	}
	if row.UseAutomaticMetric {
		return fmt.Sprintf("%d (авто)", row.Metric)
	}
	return fmt.Sprint(row.Metric)
}

func printRoutes(w io.Writer) error {
	names := interfaceNames()
	var routes []route
	for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
		rows, err := winipcfg.GetIPForwardTable2(family)
		if err != nil {
			return err
		}
		for _, r := range rows {
			p := r.DestinationPrefix.Prefix()
			if !interestingRoute(p) {
				continue
			}
			routes = append(routes, route{
				prefix:  p,
				nextHop: r.NextHop.Addr(),
				iface:   names[r.InterfaceLUID],
				index:   r.InterfaceIndex,
				metric:  r.Metric,
				ifMet:   interfaceMetric(r.InterfaceLUID, family),
			})
		}
	}
	sortRoutes(routes)
	if len(routes) == 0 {
		fmt.Fprintln(w, "   нет")
	}
	for _, r := range routes {
		fmt.Fprintf(w, "   %-14s %-22s %s (индекс %d), метрика %d + интерфейс %s\n",
			r.prefix, hopString(r.nextHop), r.iface, r.index, r.metric, r.ifMet)
	}
	return nil
}

func interfaceNames() map[winipcfg.LUID]string {
	names := map[winipcfg.LUID]string{}
	addrs, err := winipcfg.GetAdaptersAddresses(windows.AF_UNSPEC, winipcfg.GAAFlagIncludeAllInterfaces)
	if err != nil {
		return names
	}
	for _, a := range addrs {
		names[a.LUID] = a.FriendlyName()
	}
	return names
}

func printWFP(w io.Writer) error {
	s, err := wf.New(&wf.Options{Name: "checktunnel", Dynamic: true})
	if err != nil {
		return fmt.Errorf("сессия WFP (нужны права администратора): %w", err)
	}
	defer s.Close()
	sublayers, err := s.Sublayers(dnsGuardProvider)
	if err != nil {
		return fmt.Errorf("подслои: %w", err)
	}
	rules, err := s.Rules()
	if err != nil {
		return fmt.Errorf("фильтры: %w", err)
	}
	byLayer := map[string]int{}
	n := 0
	for _, r := range rules {
		if r.Provider != dnsGuardProvider {
			continue
		}
		n++
		byLayer[layerName(r.Layer)]++
	}
	fmt.Fprintf(w, "   подслой провайдера: %d, фильтров: %d\n", len(sublayers), n)
	var layers []string
	for l, c := range byLayer {
		layers = append(layers, fmt.Sprintf("%s — %d", l, c))
	}
	sort.Strings(layers)
	for _, l := range layers {
		fmt.Fprintf(w, "      %s\n", l)
	}
	return nil
}

func layerName(l wf.LayerID) string {
	switch l {
	case wf.LayerALEAuthConnectV4:
		return "исходящие IPv4"
	case wf.LayerALEAuthRecvAcceptV4:
		return "входящие IPv4"
	case wf.LayerALEAuthConnectV6:
		return "исходящие IPv6"
	case wf.LayerALEAuthRecvAcceptV6:
		return "входящие IPv6"
	}
	return l.String()
}

func printRegistry(w io.Writer, guids []windows.GUID) {
	for _, k := range []string{nrptKey, nrptPolicyKey} {
		fmt.Fprintf(w, "   NRPT HKLM\\%s: %s\n", k, subkeyCount(k))
	}
	for _, g := range guids {
		for _, base := range []string{tcpipKey, tcpip6Key} {
			fmt.Fprintf(w, "   HKLM\\%s%s: %s\n", base, g.String(), nameServer(base+g.String()))
		}
	}
}

func subkeyCount(path string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.ENUMERATE_SUB_KEYS|registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "ключа нет"
	}
	if err != nil {
		return "ошибка: " + err.Error()
	}
	defer k.Close()
	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return "ошибка: " + err.Error()
	}
	return fmt.Sprintf("подключей %d", len(names))
}

func nameServer(path string) string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return "ключа нет"
	}
	if err != nil {
		return "ошибка: " + err.Error()
	}
	defer k.Close()
	v, _, err := k.GetStringValue("NameServer")
	if errors.Is(err, registry.ErrNotExist) {
		return "NameServer нет"
	}
	if err != nil {
		return "ошибка: " + err.Error()
	}
	return fmt.Sprintf("NameServer=%q", v)
}

func printExternal(w io.Writer, name, confDir string) {
	fmt.Fprintf(w, "   служба WireGuardTunnel$%s: %s\n", name, serviceState("WireGuardTunnel$"+name))
	conf := filepath.Join(confDir, name+".conf")
	if _, err := os.Stat(conf); err == nil {
		fmt.Fprintf(w, "   %s: есть\n", conf)
	} else if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(w, "   %s: нет\n", conf)
	} else {
		fmt.Fprintf(w, "   %s: ошибка: %v\n", conf, err)
	}
}

func serviceState(name string) string {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "ошибка: " + err.Error()
	}
	defer windows.CloseServiceHandle(m)
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "ошибка: " + err.Error()
	}
	s, err := windows.OpenService(m, n, windows.SERVICE_QUERY_STATUS)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return "нет"
	}
	if err != nil {
		return "ошибка: " + err.Error()
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(s, &st); err != nil {
		return "ошибка: " + err.Error()
	}
	switch st.CurrentState {
	case windows.SERVICE_RUNNING:
		return "работает"
	case windows.SERVICE_STOPPED:
		return "остановлена"
	case windows.SERVICE_START_PENDING:
		return "запускается"
	case windows.SERVICE_STOP_PENDING:
		return "останавливается"
	}
	return fmt.Sprintf("состояние %d", st.CurrentState)
}

func operStatus(s winipcfg.IfOperStatus) string {
	switch s {
	case winipcfg.IfOperStatusUp:
		return "Up"
	case winipcfg.IfOperStatusDown:
		return "Down"
	case winipcfg.IfOperStatusNotPresent:
		return "NotPresent"
	case winipcfg.IfOperStatusLowerLayerDown:
		return "LowerLayerDown"
	}
	return fmt.Sprintf("состояние %d", s)
}

func sockaddrAddr(sa windows.SocketAddress) netip.Addr {
	switch p := sa.Sockaddr; {
	case p == nil:
		return netip.Addr{}
	case p.Addr.Family == windows.AF_INET:
		return netip.AddrFrom4((*windows.RawSockaddrInet4)(unsafe.Pointer(p)).Addr)
	case p.Addr.Family == windows.AF_INET6:
		return netip.AddrFrom16((*windows.RawSockaddrInet6)(unsafe.Pointer(p)).Addr)
	}
	return netip.Addr{}
}

func guidString(g *windows.GUID) string {
	if g == nil {
		return "GUID неизвестен"
	}
	return g.String()
}
