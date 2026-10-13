// netguard_hook.go — хук netguard в собственных сокетах ядра (задача N3 Э2).
// Файл наш: из апстрима не переносится и при обновлении ядра не затирается
// (см. scripts/vendor-core.ps1).
//
// Когда datapath забирает весь трафик системы, сокеты ядра нужно увести мимо
// туннеля, иначе туннель зависит от самого себя (architecture.md §05). Сам
// хук — Config.DialControl, его даёт CLI (internal/netguard). Здесь — диалеры,
// которые его зовут. В апстримные файлы они попадают правилами переноса,
// по одному на точку:
//
//	1  session.go       dialTURNConn, UDP     net.DialUDP → dialTURNUDP
//	2  session.go       dialTURNConn, TCP     Control в net.Dialer
//	3  creds.go         setupGlobalResolver   Control в net.Dialer резолвера
//	4  doh.go           newDoHBootstrapDialer Control в net.Dialer DoH
//	5  creds.go         getTokenChain         tlsclient.WithDialer(guardedDialer())
//	6  creds_vkcalls.go getVKCredsViaVKCalls  то же
//
// Резолв имён в точках 1, 5, 6 и у pion/turn идёт через net.DefaultResolver,
// а его ядро подменяет в точках 3 и 4 — значит, и он под хуком. Решатель
// капчи v2 получает клиента из точки 5. HTTP/3 (QUIC по UDP мимо диалера)
// tls-client включает только опцией WithProtocolRacing, ядро её не ставит.
// listen.go — loopback для datapath external (socks и tun — канал в памяти,
// Config.PacketConn), хук ему не нужен. Всё это сверяет
// TestNetguardCoverage: новая точка после обновления ядра роняет тест, а не
// туннель.

package core

import (
	"net"
	"sync/atomic"
	"syscall"
)

// DialControlFunc — сигнатура поля Control у net.Dialer.
type DialControlFunc = func(network, address string, c syscall.RawConn) error

// dialControl — хук текущего запуска. Как и прочие настройки, живёт пакетной
// переменной (см. applyGlobals): точки внедрения сидят глубоко в апстримном
// коде, и протащить до них Config нечем. Читается при каждом сокете, поэтому
// диалеры, собранные до запуска (резолвер), подхватывают хук без пересборки.
var dialControl atomic.Pointer[DialControlFunc]

// setDialControl ставит хук; nil снимает его — поведение Э1.
func setDialControl(f DialControlFunc) {
	if f == nil {
		dialControl.Store(nil)
		return
	}
	dialControl.Store(&f)
}

// guardedControl — значение поля Control у всех диалеров ядра.
func guardedControl(network, address string, c syscall.RawConn) error {
	if f := dialControl.Load(); f != nil {
		return (*f)(network, address, c)
	}
	return nil
}

// guardedDialer — диалер для tls-client (точки 5 и 6). WithDialer принимает
// net.Dialer значением; Timeout tls-client ставит сам, из WithTimeoutSeconds.
func guardedDialer() net.Dialer {
	return net.Dialer{Control: guardedControl}
}

// dialTURNUDP заменяет net.DialUDP в dialTURNConn (точка 1): у DialUDP нет
// поля Control. Адрес уже разрешён, так что Dial по строке DNS не трогает.
func dialTURNUDP(raddr *net.UDPAddr) (*net.UDPConn, error) {
	d := net.Dialer{Control: guardedControl}
	c, err := d.Dial("udp", raddr.String())
	if err != nil {
		return nil, err
	}
	return c.(*net.UDPConn), nil
}
