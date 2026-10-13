package winsys

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"sync"

	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"
)

// Постоянные GUID провайдера и подслоя WFP: по ним наши фильтры узнаются
// в `netsh wfp show filters`. Правила получают новые GUID на каждый Enable.
var (
	dnsGuardProvider = wf.ProviderID{
		Data1: 0x4ad3fe23, Data2: 0x87a7, Data3: 0x420b,
		Data4: [8]byte{0xbb, 0xab, 0xcd, 0x89, 0xcc, 0x92, 0x70, 0x9a},
	}
	dnsGuardSublayer = wf.SublayerID{
		Data1: 0xf26eba4e, Data2: 0xb57d, Data3: 0x4e55,
		Data4: [8]byte{0xaf, 0xbc, 0xae, 0x12, 0x7a, 0xd5, 0x7f, 0xc4},
	}
)

// Веса правил внутри подслоя: разрешение тяжелее запрета, как в blockDNS
// у wireguard-windows (tunnel/firewall/rules.go, MIT, v1.1.1).
const (
	dnsGuardPermitWeight = 15
	dnsGuardBlockWeight  = 14
)

// DNSGuard — защита от утечки DNS (plan-e2.md, решение 4): фильтры WFP
// запрещают обращения на порт 53, UDP и TCP, ко всем адресам, кроме
// резолверов туннеля, loopback и запросов самого tt.
//
// Сессия WFP динамическая: всё, что в ней создано, система снимает при её
// закрытии или со смертью процесса, поэтому упавший tt фильтров не оставляет
// и уборка при запуске не нужна.
//
// Своему процессу разрешено всё: ядро ходит к DNS VK (резолвер из профиля)
// сокетами мимо туннеля (netguard), и без этого не подключилось бы.
// Системные запросы идут от службы DNS-клиента, а не от программ, — их
// пропускает правило по адресу резолвера туннеля.
//
// Сессия открывается один раз на tt connect (OpenDNSGuard), а правила
// ставятся и снимаются по ходу (Enable, Disable): отказ WFP так узнаётся до
// подключения, а не посреди него.
type DNSGuard struct {
	mu      sync.Mutex
	session *wf.Session
	appID   string
	rules   []wf.RuleID
}

// OpenDNSGuard открывает сессию WFP, заводит в ней провайдера и подслой;
// правил ещё нет. Нужны права администратора и работающая служба BFE
// (Base Filtering Engine).
func OpenDNSGuard() (*DNSGuard, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("путь к tt.exe: %w", err)
	}
	appID, err := wf.AppID(exe)
	if err != nil {
		return nil, fmt.Errorf("идентификатор приложения WFP для %s: %w", exe, err)
	}

	session, err := wf.New(&wf.Options{
		Name:        "tt",
		Description: "tt: защита от утечки DNS",
		Dynamic:     true,
	})
	if err != nil {
		return nil, fmt.Errorf("сессия WFP: %w", err)
	}
	if err := session.AddProvider(&wf.Provider{
		ID:          dnsGuardProvider,
		Name:        "tt",
		Description: "tt: защита от утечки DNS",
	}); err != nil {
		session.Close()
		return nil, fmt.Errorf("провайдер WFP: %w", err)
	}
	if err := session.AddSublayer(&wf.Sublayer{
		ID:          dnsGuardSublayer,
		Name:        "tt DNS",
		Description: "tt: DNS только через туннель",
		Provider:    dnsGuardProvider,
		Weight:      ^uint16(0),
	}); err != nil {
		session.Close()
		return nil, fmt.Errorf("подслой WFP: %w", err)
	}
	return &DNSGuard{session: session, appID: appID}, nil
}

// Enable ставит правила: DNS — только к резолверам dns. Прежние правила
// заменяются, так что повторный вызов безвреден. На отказе не остаётся ни
// старых правил, ни части новых.
func (g *DNSGuard) Enable(dns []netip.Addr) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.session == nil {
		return errors.New("защита DNS: сессия WFP закрыта")
	}
	if err := g.disableLocked(); err != nil {
		return err
	}
	rules, err := dnsGuardRules(dns, g.appID)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if err := g.session.AddRule(r); err != nil {
			err = fmt.Errorf("защита DNS: правило %q: %w", r.Name, err)
			return errors.Join(err, g.disableLocked())
		}
		g.rules = append(g.rules, r.ID)
	}
	return nil
}

// Disable снимает правила; сессия остаётся для следующего Enable. Без
// правил — ничего не делает.
func (g *DNSGuard) Disable() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.disableLocked()
}

func (g *DNSGuard) disableLocked() error {
	var errs []error
	for _, id := range g.rules {
		if err := g.session.DeleteRule(id); err != nil && !errors.Is(err, windows.Errno(fwpEFilterNotFound)) {
			errs = append(errs, err)
		}
	}
	g.rules = nil
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("защита DNS: снятие правил: %w", err)
	}
	return nil
}

// Close закрывает сессию: система снимает всё, что в ней создано.
// Повторный вызов безвреден.
func (g *DNSGuard) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.session == nil {
		return nil
	}
	err := g.session.Close()
	g.session, g.rules = nil, nil
	return err
}

// fwpEFilterNotFound — FWP_E_FILTER_NOT_FOUND: правила уже нет.
const fwpEFilterNotFound = 0x80320003

// dnsGuardLayers — слои, где действуют правила: исходящие соединения и
// первый входящий пакет несвязанного сокета, IPv4 и IPv6, как у blockDNS
// в wireguard-windows.
var dnsGuardLayers = []struct {
	id   wf.LayerID
	ipv6 bool
	name string
}{
	{wf.LayerALEAuthConnectV4, false, "исходящие IPv4"},
	{wf.LayerALEAuthRecvAcceptV4, false, "входящие IPv4"},
	{wf.LayerALEAuthConnectV6, true, "исходящие IPv6"},
	{wf.LayerALEAuthRecvAcceptV6, true, "входящие IPv6"},
}

// dnsGuardRules строит правила для Enable; отдельно — ради теста без прав.
// На каждом слое: запрет порта 53 по UDP и TCP, разрешение своему
// процессу, разрешение резолверам туннеля своего семейства и loopback.
// Повтор условия с тем же полем WFP читает как «или».
func dnsGuardRules(dns []netip.Addr, appID string) ([]*wf.Rule, error) {
	var rules []*wf.Rule
	add := func(layer wf.LayerID, name string, weight uint64, action wf.Action, conds []*wf.Match) error {
		guid, err := windows.GenerateGUID()
		if err != nil {
			return fmt.Errorf("защита DNS: GUID правила: %w", err)
		}
		rules = append(rules, &wf.Rule{
			ID:         wf.RuleID(guid),
			Name:       name,
			Layer:      layer,
			Sublayer:   dnsGuardSublayer,
			Provider:   dnsGuardProvider,
			Weight:     weight,
			Conditions: conds,
			Action:     action,
		})
		return nil
	}

	for _, layer := range dnsGuardLayers {
		block := []*wf.Match{
			{Field: wf.FieldIPRemotePort, Op: wf.MatchTypeEqual, Value: uint16(53)},
			{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoUDP},
			{Field: wf.FieldIPProtocol, Op: wf.MatchTypeEqual, Value: wf.IPProtoTCP},
		}
		if err := add(layer.id, "tt: запрет DNS мимо туннеля, "+layer.name,
			dnsGuardBlockWeight, wf.ActionBlock, block); err != nil {
			return nil, err
		}

		self := []*wf.Match{{Field: wf.FieldALEAppID, Op: wf.MatchTypeEqual, Value: appID}}
		if err := add(layer.id, "tt: DNS самого tt, "+layer.name,
			dnsGuardPermitWeight, wf.ActionPermit, self); err != nil {
			return nil, err
		}

		var allowed []*wf.Match
		if layer.ipv6 {
			allowed = append(allowed, &wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: netip.IPv6Loopback()})
		} else {
			allowed = append(allowed, &wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: netip.MustParsePrefix("127.0.0.0/8")})
		}
		for _, a := range dns {
			a = a.Unmap()
			if a.Is6() == layer.ipv6 {
				allowed = append(allowed, &wf.Match{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: a})
			}
		}
		if err := add(layer.id, "tt: DNS туннеля и loopback, "+layer.name,
			dnsGuardPermitWeight, wf.ActionPermit, allowed); err != nil {
			return nil, err
		}
	}
	return rules, nil
}
