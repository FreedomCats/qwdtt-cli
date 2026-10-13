package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// Настройки проверяются раньше, чем ядро трогает глобальное состояние и сеть,
// поэтому эти тесты ничего никуда не подключают.

func TestRunRejectsInvalidConfig(t *testing.T) {
	rec := install(t)

	err := Run(context.Background(), Config{}, rec, nil)
	if err == nil {
		t.Fatal("Run с пустыми настройками вернул nil")
	}
	for _, want := range []string{"peer", "пароль", "хеша"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в ошибке нет %q: %v", want, err)
		}
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("до проверки настроек ушло %d событий: %v", len(got), got)
	}
}

// closeRecorder — готовый канал datapath, который помнит, закрыли ли его.
type closeRecorder struct {
	net.PacketConn
	closed bool
}

func (c *closeRecorder) Close() error {
	c.closed = true
	return nil
}

// Готовый канал принадлежит Run: закрывается, даже если до запуска не дошло.
func TestRunClosesPacketConnOnInvalidConfig(t *testing.T) {
	rec := install(t)
	pc := &closeRecorder{}

	if err := Run(context.Background(), Config{PacketConn: pc}, rec, nil); err == nil {
		t.Fatal("Run с пустыми настройками вернул nil")
	}
	if !pc.closed {
		t.Error("Run вернулся, не закрыв PacketConn")
	}
}

func TestOpenLocal(t *testing.T) {
	pc := &closeRecorder{}
	got, err := openLocal(&Config{Listen: "127.0.0.1:1", PacketConn: pc})
	if err != nil || got != pc {
		t.Fatalf("openLocal с PacketConn = %v, %v; ожидался тот же канал", got, err)
	}

	got, err = openLocal(&Config{Listen: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("openLocal без PacketConn: %v", err)
	}
	defer got.Close()
	if _, ok := got.(*net.UDPConn); !ok {
		t.Fatalf("openLocal без PacketConn = %T, ожидался UDP", got)
	}
}

func TestRunPingRejectsInvalidConfig(t *testing.T) {
	rec := install(t)

	if err := RunPing(context.Background(), Config{}, rec); err == nil {
		t.Fatal("RunPing с пустыми настройками вернул nil")
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("до проверки настроек ушло %d событий: %v", len(got), got)
	}
}

// RunHashCheck туннель не поднимает, поэтому ни адрес сервера, ни пароль ему
// не нужны — только список хешей.
func TestRunHashCheckNeedsOnlyHashes(t *testing.T) {
	rec := install(t)

	err := RunHashCheck(context.Background(), Config{}, rec)
	if err == nil {
		t.Fatal("RunHashCheck без хешей вернул nil")
	}
	if strings.Contains(err.Error(), "peer") || strings.Contains(err.Error(), "пароль") {
		t.Errorf("RunHashCheck требует лишнего: %v", err)
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("до проверки настроек ушло %d событий: %v", len(got), got)
	}
}

// Получателя событий Run назначает первым делом: иначе отладка идёт вслепую —
// ядро молчит обо всём, что случилось до назначения.
func TestRunSetsSinkBeforeAnythingElse(t *testing.T) {
	setSink(nil)
	t.Cleanup(func() { setSink(nil) })

	rec := &recorder{}
	if err := Run(context.Background(), Config{}, rec, nil); err == nil {
		t.Fatal("Run с пустыми настройками вернул nil")
	}

	emit(EventVkAuthRequired{Link: "после Run"})
	if got := rec.all(); len(got) != 1 {
		t.Fatalf("получено %d событий, ожидалось 1: Run не назначил получателя", len(got))
	}
}

func TestWithMTU(t *testing.T) {
	tests := []struct {
		name string
		conf string
		want string
	}{
		{
			name: "дописывает после [Interface]",
			conf: "[Interface]\nAddress = 10.0.0.2/32\n\n[Peer]\nEndpoint = 127.0.0.1:9000",
			want: "[Interface]\nMTU = 1280\nAddress = 10.0.0.2/32\n\n[Peer]\nEndpoint = 127.0.0.1:9000",
		},
		{
			name: "свой MTU не трогает",
			conf: "[Interface]\nMTU = 1420\nAddress = 10.0.0.2/32",
			want: "[Interface]\nMTU = 1420\nAddress = 10.0.0.2/32",
		},
		{
			name: "без секции [Interface] не выдумывает её",
			conf: "[Peer]\nEndpoint = 127.0.0.1:9000",
			want: "[Peer]\nEndpoint = 127.0.0.1:9000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withMTU(tt.conf); got != tt.want {
				t.Errorf("withMTU:\nполучено:\n%s\nожидалось:\n%s", got, tt.want)
			}
		})
	}
}

// Ошибку замера RunPing и отправляет событием, и возвращает: по событию её
// показывают, по возвращённой — выставляют код возврата.
func TestEmitPingErrorReturnsWhatItEmitted(t *testing.T) {
	rec := install(t)

	cause := errors.New("relay не ответил")
	err := emitPingError(cause)

	if !errors.Is(err, cause) {
		t.Errorf("возвращённая ошибка потеряла причину: %v", err)
	}

	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("получено %d событий, ожидалось 1", len(events))
	}
	ping, ok := events[0].(EventPing)
	if !ok {
		t.Fatalf("получено %T, ожидалось EventPing", events[0])
	}
	if ping.Err != err {
		t.Errorf("в событии ошибка %v, возвращена %v — они должны быть одной", ping.Err, err)
	}
}

// Ни одного хеша — ни одной попытки, и причине взяться неоткуда.
func TestEmitPingErrorWithoutCause(t *testing.T) {
	install(t)

	if err := emitPingError(nil); err == nil {
		t.Fatal("emitPingError(nil) вернул nil")
	}
}

// ProbeDNS ходит через тот резолвер, который настроил, и уважает срок из ctx:
// сервер, который не отвечает, — это ошибка за отведённое время, а не вечное
// ожидание.
func TestProbeDNSHonoursContext(t *testing.T) {
	install(t)

	saved := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = saved })

	// Сокет, который слушает и молчит: запросы к нему уходят, ответов нет.
	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer silent.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = ProbeDNS(ctx, Config{DNS: "custom:" + silent.LocalAddr().String()})
	if err == nil {
		t.Fatal("ProbeDNS через молчащий сервер вернул nil")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("ProbeDNS ждал %s, а срок был 300 мс", elapsed)
	}
	if !strings.Contains(err.Error(), "резолвер") {
		t.Errorf("ошибка не объясняет, что не так с резолвером: %v", err)
	}
}

// В ошибке ProbeDNS нет адреса системного резолвера: запрос ушёл на тот, что
// в настройках, а «on <системный>» от net.Resolver сбивал с толку.
func TestProbeDNSErrorNamesNoSystemServer(t *testing.T) {
	install(t)

	saved := net.DefaultResolver
	t.Cleanup(func() { net.DefaultResolver = saved })

	silent, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer silent.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err = ProbeDNS(ctx, Config{DNS: "custom:" + silent.LocalAddr().String()})
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) {
		t.Fatalf("ждали net.DNSError внутри, получили %v", err)
	}
	if dnsErr.Server != "" {
		t.Errorf("в ошибке системный резолвер %q: %v", dnsErr.Server, err)
	}
}

func TestWithoutSystemServer(t *testing.T) {
	orig := &net.DNSError{Err: "timeout", Name: "login.vk.ru", Server: "192.168.1.1:53", IsTimeout: true}
	got := withoutSystemServer(fmt.Errorf("обёртка: %w", orig))
	if strings.Contains(got.Error(), "192.168.1.1") || !strings.Contains(got.Error(), "login.vk.ru") {
		t.Errorf("withoutSystemServer = %q", got)
	}
	if orig.Server == "" {
		t.Error("исходная ошибка испорчена: Server стёрт у оригинала")
	}
	var dnsErr *net.DNSError
	if !errors.As(got, &dnsErr) || !dnsErr.IsTimeout {
		t.Errorf("признаки ошибки потеряны: %#v", got)
	}

	plain := errors.New("не DNS")
	if withoutSystemServer(plain) != plain {
		t.Error("ошибка не про DNS должна вернуться как есть")
	}
}
