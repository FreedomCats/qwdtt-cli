package linuxsys

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// dnsGuardTable — таблица защиты DNS в nftables (`nft list table inet tt`).
const dnsGuardTable = "tt"

// DNSGuard — защита от утечки DNS на Linux (plan-e4.md, D1), аналог
// winsys.DNSGuard: правила nftables запрещают обращения на порт 53, UDP и
// TCP, ко всем адресам, кроме резолверов туннеля, loopback и сокетов tt.
//
// Таблица inet tt создаётся с флагом owner: ядро держит её, пока открыт
// netlink-сокет, который её создал, и удаляет вместе с ним — в том числе
// когда tt падает. Уборка при запуске поэтому не нужна. Менять таблицу с
// owner может только этот сокет, так что всё идёт через одно долгоживущее
// соединение (nftables.AsLasting).
//
// Сокеты ядра tt опознаются по метке SO_MARK (netguard.SocketMark): фильтр
// по uid пропустил бы под sudo все процессы root. Метка только опознаёт —
// маршрутизации по ней нет.
//
// Цепочка forward — ради контейнеров и ВМ: их запросы к резолверу в
// локальной сети (маршрут конкретнее /1) иначе ушли бы мимо туннеля.
//
// Таблица и цепочки создаются один раз (OpenDNSGuard), а правила ставятся и
// снимаются по ходу (Enable, Disable): отказ так узнаётся до подключения, а
// не посреди него.
type DNSGuard struct {
	mu        sync.Mutex
	conn      *nftables.Conn
	table     *nftables.Table
	output    *nftables.Chain
	forward   *nftables.Chain
	mark      uint32
	blockIPv6 bool
}

// OpenDNSGuard создаёт таблицу inet tt с пустыми цепочками; правил ещё
// нет. mark — метка сокетов tt, им DNS разрешён к любым адресам. Нужны
// CAP_NET_ADMIN и ядро 5.12 или новее (флаг owner).
func OpenDNSGuard(mark uint32) (*DNSGuard, error) {
	conn, err := nftables.New(nftables.AsLasting())
	if err != nil {
		return nil, fmt.Errorf("nftables: %w", err)
	}
	g := &DNSGuard{conn: conn, mark: mark}
	g.table = conn.CreateTable(&nftables.Table{
		Name:   dnsGuardTable,
		Family: nftables.TableFamilyINet,
		Flags:  nftables.TableFlagOwner,
	})
	accept := nftables.ChainPolicyAccept
	g.output = conn.AddChain(&nftables.Chain{
		Name:     "output",
		Table:    g.table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookOutput,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &accept,
	})
	g.forward = conn.AddChain(&nftables.Chain{
		Name:     "forward",
		Table:    g.table,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: nftables.ChainPriorityFilter,
		Policy:   &accept,
	})
	if err := conn.Flush(); err != nil {
		conn.CloseLasting()
		return nil, dnsGuardTableError(err)
	}
	return g, nil
}

// dnsGuardTableError объясняет отказ создать таблицу. Таблица с тем же
// именем уже есть — ядро отвечает EEXIST, а если у неё есть владелец, то
// EPERM, как и при нехватке прав; поэтому при EPERM таблица ищется.
func dnsGuardTableError(err error) error {
	switch {
	case errors.Is(err, unix.EEXIST):
		return fmt.Errorf("nftables: таблица inet %s уже есть, и создал её не tt; "+
			"если она не нужна, удалите её: sudo nft delete table inet %s", dnsGuardTable, dnsGuardTable)
	case errors.Is(err, unix.EPERM):
		if owner, ok := dnsGuardTableOwner(); ok {
			return fmt.Errorf("nftables: таблицу inet %s держит другой процесс (netlink-порт %d, "+
				"обычно это его pid) — похоже, уже работает другой tt", dnsGuardTable, owner)
		}
	}
	return fmt.Errorf("nftables: таблица inet %s: %w", dnsGuardTable, err)
}

// dnsGuardTableOwner — владелец существующей таблицы inet tt, если она есть
// и у неё есть владелец.
func dnsGuardTableOwner() (uint32, bool) {
	conn, err := nftables.New()
	if err != nil {
		return 0, false
	}
	table, err := conn.ListTableOfFamily(dnsGuardTable, nftables.TableFamilyINet)
	if err != nil || table.Flags&nftables.TableFlagOwner == 0 {
		return 0, false
	}
	return table.Owner, true
}

// BlockIPv6 задаёт, закрывать ли при следующем Enable весь IPv6 системы,
// кроме loopback и сокетов tt: туннель его не везёт (MTU < 1280, ядро
// снимает IPv6 с адаптера), и без запрета он шёл бы мимо туннеля.
func (g *DNSGuard) BlockIPv6(block bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.blockIPv6 = block
}

// Enable ставит правила: DNS — только к резолверам dns. Прежние правила
// заменяются одним пакетом nftables, так что повторный вызов безвреден, а
// на отказе ядро не применяет ничего; правила тогда снимаются, чтобы не
// осталось и старых.
func (g *DNSGuard) Enable(dns []netip.Addr) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conn == nil {
		return errors.New("защита DNS: таблица nftables закрыта")
	}
	g.conn.FlushTable(g.table)
	for _, r := range dnsGuardRules(dns, g.blockIPv6, false) {
		g.conn.AddRule(&nftables.Rule{Table: g.table, Chain: g.output, Exprs: r.exprs(g.mark)})
	}
	for _, r := range dnsGuardRules(dns, g.blockIPv6, true) {
		g.conn.AddRule(&nftables.Rule{Table: g.table, Chain: g.forward, Exprs: r.exprs(g.mark)})
	}
	if err := g.conn.Flush(); err != nil {
		err = fmt.Errorf("защита DNS: правила nftables: %w", err)
		return errors.Join(err, g.disableLocked())
	}
	return nil
}

// Disable снимает правила; таблица и цепочки остаются пустыми и ничего не
// запрещают. Повторный вызов безвреден.
func (g *DNSGuard) Disable() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.disableLocked()
}

func (g *DNSGuard) disableLocked() error {
	if g.conn == nil {
		return nil
	}
	g.conn.FlushTable(g.table)
	if err := g.conn.Flush(); err != nil {
		return fmt.Errorf("защита DNS: снятие правил nftables: %w", err)
	}
	return nil
}

// Close закрывает соединение, и ядро удаляет таблицу вместе с правилами.
func (g *DNSGuard) Close() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.conn == nil {
		return nil
	}
	err := g.conn.CloseLasting()
	g.conn = nil
	if err != nil {
		return fmt.Errorf("защита DNS: закрытие nftables: %w", err)
	}
	return nil
}

// guardRule — одно правило защиты в виде, удобном для проверки; exprs
// переводит его в выражения nftables. Условия складываются по «и».
type guardRule struct {
	mark     bool       // сокет tt: meta mark == метка
	loopback bool       // oifname "lo"
	family   byte       // unix.NFPROTO_IPV4 или NFPROTO_IPV6; 0 — любое
	daddr    netip.Addr // адрес назначения; пустой — любой
	proto    byte       // unix.IPPROTO_UDP или IPPROTO_TCP с портом 53; 0 — любой трафик
	accept   bool       // иначе drop
}

// dnsGuardRules — правила цепочки по порядку: сначала разрешения, потом
// запреты. forward — цепочка транзитного трафика: метки и loopback там нет.
//
// При blockIPv6 IPv6 закрыт целиком, раньше разрешений по адресу: резолвер
// туннеля с адресом IPv6 тогда тоже недоступен — маршрута IPv6 в туннель
// нет, и запрос к нему ушёл бы мимо.
func dnsGuardRules(dns []netip.Addr, blockIPv6, forward bool) []guardRule {
	var rules []guardRule
	if !forward {
		rules = append(rules,
			guardRule{mark: true, accept: true},
			guardRule{loopback: true, accept: true})
	}
	if blockIPv6 {
		rules = append(rules, guardRule{family: unix.NFPROTO_IPV6})
	}
	protos := []byte{unix.IPPROTO_UDP, unix.IPPROTO_TCP}
	for _, addr := range dns {
		addr = addr.Unmap()
		if !addr.IsValid() || (blockIPv6 && addr.Is6()) {
			continue
		}
		family := byte(unix.NFPROTO_IPV4)
		if addr.Is6() {
			family = unix.NFPROTO_IPV6
		}
		for _, p := range protos {
			rules = append(rules, guardRule{family: family, daddr: addr, proto: p, accept: true})
		}
	}
	for _, p := range protos {
		rules = append(rules, guardRule{proto: p})
	}
	return rules
}

// exprs — выражения nftables правила r; mark — метка сокетов tt.
func (r guardRule) exprs(mark uint32) []expr.Any {
	var e []expr.Any
	match := func(load expr.Any, data []byte) {
		e = append(e, load, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: data})
	}
	if r.mark {
		match(&expr.Meta{Key: expr.MetaKeyMARK, Register: 1}, binaryutil.NativeEndian.PutUint32(mark))
	}
	if r.loopback {
		match(&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1}, ifname("lo"))
	}
	if r.family != 0 {
		match(&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1}, []byte{r.family})
	}
	if r.daddr.IsValid() {
		// Адрес назначения: смещение 16 в заголовке IPv4, 24 — в IPv6.
		offset, size := uint32(16), uint32(4)
		if r.daddr.Is6() {
			offset, size = 24, 16
		}
		match(&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: size},
			r.daddr.AsSlice())
	}
	if r.proto != 0 {
		match(&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, []byte{r.proto})
		// Порт назначения — смещение 2 и в UDP, и в TCP.
		match(&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2},
			binaryutil.BigEndian.PutUint16(53))
	}
	verdict := expr.VerdictDrop
	if r.accept {
		verdict = expr.VerdictAccept
	}
	return append(e, &expr.Verdict{Kind: verdict})
}

// ifname — имя интерфейса так, как его сравнивает ядро: IFNAMSIZ байт,
// дополненных нулями.
func ifname(name string) []byte {
	b := make([]byte, unix.IFNAMSIZ)
	copy(b, name)
	return b
}
