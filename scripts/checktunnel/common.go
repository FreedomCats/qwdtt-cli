//go:build windows || linux

// Общее для снимков Windows и Linux: разделы, маршруты /0 и /1, внешний
// адрес и резолвер.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"
)

const (
	myaddrName = "o-o.myaddr.l.google.com"
	ipURL      = "https://ifconfig.me/ip"
)

// header печатает заголовок снимка.
func header(w io.Writer, label string) {
	title := "снимок " + time.Now().Format("2006-01-02 15:04:05")
	if label != "" {
		title += " — " + label
	}
	fmt.Fprintf(w, "=== %s ===\n", title)
}

func section(w io.Writer, name string, f func(io.Writer) error) {
	fmt.Fprintf(w, "\n-- %s\n", name)
	if err := f(w); err != nil {
		fmt.Fprintf(w, "   ошибка: %v\n", err)
	}
}

// printNetwork печатает внешний адрес и резолвер — разделы, которым нужна
// сеть.
func printNetwork(w io.Writer, timeout time.Duration) {
	section(w, "Внешний адрес (без прокси)", func(w io.Writer) error {
		printExternalIP(w, timeout)
		return nil
	})
	section(w, "Резолвер ("+myaddrName+")", func(w io.Writer) error {
		return printResolver(w, timeout)
	})
}

type route struct {
	prefix  netip.Prefix
	nextHop netip.Addr
	iface   string
	index   uint32
	metric  uint32 // маршрута
	ifMet   string // интерфейса (Windows)
	table   int    // таблица маршрутизации (Linux)
	proto   string // кто поставил маршрут (Linux)
}

// interestingRoute — маршрут по умолчанию или половина полного туннеля.
func interestingRoute(p netip.Prefix) bool {
	return p.IsValid() && p.Bits() <= 1
}

// sortRoutes — IPv4 перед IPv6, затем по префиксу и индексу интерфейса.
func sortRoutes(rs []route) {
	sort.SliceStable(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.prefix.Addr().Is4() != b.prefix.Addr().Is4() {
			return a.prefix.Addr().Is4()
		}
		if c := a.prefix.Addr().Compare(b.prefix.Addr()); c != 0 {
			return c < 0
		}
		if a.prefix.Bits() != b.prefix.Bits() {
			return a.prefix.Bits() < b.prefix.Bits()
		}
		return a.index < b.index
	})
}

// hopString — шлюз маршрута для вывода.
func hopString(nextHop netip.Addr) string {
	if nextHop.IsValid() && !nextHop.IsUnspecified() {
		return "через " + nextHop.String()
	}
	return "on-link"
}

func printExternalIP(w io.Writer, timeout time.Duration) {
	for _, network := range []string{"tcp4", "tcp6"} {
		ip, err := externalIP(network, timeout)
		label := "IPv4"
		if network == "tcp6" {
			label = "IPv6"
		}
		if err != nil {
			fmt.Fprintf(w, "   %s: нет (%v)\n", label, shortErr(err))
			continue
		}
		fmt.Fprintf(w, "   %s: %s\n", label, ip)
	}
}

// externalIP спрашивает ifconfig.me по заданному семейству и мимо прокси
// из окружения: в консоли сессии стоит HTTPS_PROXY на локальный xray,
// который выходит на тот же VPS, и адрес через него ничего не говорит о
// туннеле.
func externalIP(network string, timeout time.Duration) (string, error) {
	d := &net.Dialer{Timeout: timeout}
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
		},
	}
	req, err := http.NewRequest(http.MethodGet, ipURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "curl/8")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(string(body)))
	if err != nil {
		return "", fmt.Errorf("ответ не адрес: %q", body)
	}
	return ip.String(), nil
}

// printResolver — TXT o-o.myaddr.l.google.com системным резолвером: адрес
// того, кто на самом деле спросил Google.
func printResolver(w io.Writer, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	txt, err := net.DefaultResolver.LookupTXT(ctx, myaddrName)
	if err != nil {
		return errors.New(shortErr(err))
	}
	for _, t := range txt {
		fmt.Fprintf(w, "   %s\n", t)
	}
	return nil
}

func listOrNone(xs []string) string {
	if len(xs) == 0 {
		return "нет"
	}
	return strings.Join(xs, ", ")
}

// shortErr обрезает длинные сетевые ошибки Go до сути: «dial tcp6 …: connect:
// …» превращается в последнюю часть после двоеточия, если она не пустая.
func shortErr(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsNotFound {
			return "имя не найдено"
		}
		if dnsErr.IsTimeout {
			return "таймаут DNS"
		}
		return dnsErr.Err
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return "таймаут"
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && i+2 < len(s) {
		return s[i+2:]
	}
	return s
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
