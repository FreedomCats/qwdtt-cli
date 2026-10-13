//go:build windows

// Команда outage устраивает обрыв связи с заданными адресами на время — для
// живых проверок переподключения tt (plan-e2.md, S1 и R2, сценарий 4).
//
//	go run ./scripts/outage -addr 198.51.100.20,198.51.100.21 [-for 60s]
//
// Адреса — серверы TURN из лога tt (строка «Креды OK, TURN: […]»). Пакеты
// к ним и от них отбрасываются фильтрами WFP на слоях IP-пакетов
// (INBOUND/OUTBOUND_IPPACKET). Способ выяснен в S1: правило брандмауэра на
// tt.exe не годится (слои ALE не трогают уже открытые потоки UDP), маршруты
// /32 режут только исходящие — входящие keepalive сервера держат туннель
// «живым».
//
// Сессия WFP динамическая: фильтры снимает система, когда команда
// завершается — по истечении -for, по Ctrl+C или если процесс убит.
// Обрыв ограничен по времени нарочно: пока туннель мёртв, а маршруты /1 tt
// стоят, весь трафик машины идёт в никуда — и сеть того, кто запустил
// проверку, тоже; снять обрыв руками в этот момент может быть нечем.
//
// Нужны права администратора. Только Windows.
package main

import (
	"flag"
	"fmt"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/tailscale/wf"
	"golang.org/x/sys/windows"
)

// outageSublayer — свой подслой с наибольшим весом: отбрасывание в нём
// перевешивает разрешения в чужих.
var outageSublayer = wf.SublayerID{
	Data1: 0x5c1e0d7a, Data2: 0x3b2f, Data3: 0x4a8e,
	Data4: [8]byte{0x9d, 0x41, 0x7e, 0x0b, 0x6c, 0x23, 0xa5, 0x18},
}

func main() {
	addrs := flag.String("addr", "", "адреса через запятую (IPv4 или IPv6), обязателен")
	dur := flag.Duration("for", 60*time.Second, "сколько держать обрыв")
	flag.Parse()
	if *addrs == "" || flag.NArg() > 0 || *dur <= 0 {
		flag.Usage()
		os.Exit(2)
	}
	list, err := parseAddrs(*addrs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "outage:", err)
		os.Exit(2)
	}
	if err := run(list, *dur); err != nil {
		fmt.Fprintln(os.Stderr, "outage:", err)
		os.Exit(1)
	}
}

func parseAddrs(s string) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("адрес %q: %w", f, err)
		}
		out = append(out, a.Unmap())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("нет адресов")
	}
	return out, nil
}

// layersFor — слои IP-пакетов семейства адреса: входящие и исходящие.
func layersFor(a netip.Addr) []wf.LayerID {
	if a.Is4() {
		return []wf.LayerID{wf.LayerInboundIPPacketV4, wf.LayerOutboundIPPacketV4}
	}
	return []wf.LayerID{wf.LayerInboundIPPacketV6, wf.LayerOutboundIPPacketV6}
}

func run(addrs []netip.Addr, dur time.Duration) error {
	s, err := wf.New(&wf.Options{Name: "tt outage", Description: "tt: обрыв для проверки", Dynamic: true})
	if err != nil {
		return fmt.Errorf("сессия WFP (нужны права администратора): %w", err)
	}
	defer s.Close()
	if err := s.AddSublayer(&wf.Sublayer{ID: outageSublayer, Name: "tt outage", Weight: ^uint16(0)}); err != nil {
		return fmt.Errorf("подслой: %w", err)
	}
	n := 0
	for _, a := range addrs {
		for _, layer := range layersFor(a) {
			guid, err := windows.GenerateGUID()
			if err != nil {
				return err
			}
			if err := s.AddRule(&wf.Rule{
				ID:       wf.RuleID(guid),
				Name:     "tt outage " + a.String(),
				Layer:    layer,
				Sublayer: outageSublayer,
				Weight:   15,
				Conditions: []*wf.Match{
					{Field: wf.FieldIPRemoteAddress, Op: wf.MatchTypeEqual, Value: a},
				},
				Action: wf.ActionBlock,
			}); err != nil {
				return fmt.Errorf("фильтр %s: %w", a, err)
			}
			n++
		}
	}
	start := time.Now()
	fmt.Printf("%s обрыв: %d фильтров, адреса %v, на %s\n", start.Format("15:04:05"), n, addrs, dur)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)
	select {
	case <-time.After(dur):
	case <-stop:
	}
	if err := s.Close(); err != nil {
		return fmt.Errorf("закрытие сессии: %w", err)
	}
	fmt.Printf("%s обрыв снят через %s\n", time.Now().Format("15:04:05"), time.Since(start).Round(time.Second))
	return nil
}
