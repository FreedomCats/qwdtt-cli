package core

import (
	"context"
	"errors"
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
	err = ProbeDNS(ctx, "custom:"+silent.LocalAddr().String())
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
