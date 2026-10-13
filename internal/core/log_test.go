package core

import "testing"

func TestSplitLogTag(t *testing.T) {
	cases := []struct {
		name    string
		msg     string
		wantTag string
		wantMsg string
	}{
		{
			name:    "обычный префикс",
			msg:     "[КЛИЕНТ] Воркеров: 9 (групп: 1, по 9)",
			wantTag: "КЛИЕНТ",
			wantMsg: "Воркеров: 9 (групп: 1, по 9)",
		},
		{
			name:    "префикс с номером",
			msg:     "[СЕССИЯ #3] Relay: 1.2.3.4:50000",
			wantTag: "СЕССИЯ #3",
			wantMsg: "Relay: 1.2.3.4:50000",
		},
		{
			// Тегом становится тот, кто пишет; подсистема остаётся в тексте,
			// поэтому строка собирается обратно один в один.
			name:    "двойной префикс",
			msg:     "[ВОРКЕР #3] [DTLS] Рукопожатие (Handshake)...",
			wantTag: "ВОРКЕР #3",
			wantMsg: "[DTLS] Рукопожатие (Handshake)...",
		},
		{
			name:    "без префикса",
			msg:     "Туннель работает",
			wantTag: "",
			wantMsg: "Туннель работает",
		},
		{
			name:    "скобка не закрыта",
			msg:     "[оборванный текст",
			wantTag: "",
			wantMsg: "[оборванный текст",
		},
		{
			name:    "пустое сообщение",
			msg:     "",
			wantTag: "",
			wantMsg: "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tag, msg := splitLogTag(c.msg)
			if tag != c.wantTag {
				t.Errorf("тег = %q, ожидался %q", tag, c.wantTag)
			}
			if msg != c.wantMsg {
				t.Errorf("текст = %q, ожидался %q", msg, c.wantMsg)
			}
		})
	}
}

func TestGuessLogLevel(t *testing.T) {
	cases := map[string]LogLevel{
		"Туннель готов к работе":                     LevelInfo,
		"Relay: 1.2.3.4:50000":                       LevelInfo,
		"Ошибка слушателя 127.0.0.1:9000":            LevelError,
		"ошибки авторизации нет":                     LevelError,
		"Не удалось обновить креды после retry":      LevelError,
		"v2 endSession failed: closed":               LevelError,
		"FATAL_AUTH: пароль не подтверждён":          LevelError,
		"Warning: getCallPreview failed: timeout":    LevelWarn,
		"ВНИМАНИЕ: в апстриме незакоммиченные файлы": LevelWarn,
	}

	for text, want := range cases {
		if got := guessLogLevel(text); got != want {
			t.Errorf("guessLogLevel(%q) = %q, ожидалось %q", text, got, want)
		}
	}
}

func TestLogfEmitsEventLog(t *testing.T) {
	rec := install(t)

	logf("[ГРУППА #%d] Креды OK, TURN: %d", 2, 3)
	logln("[КЛИЕНТ] Все воркеры завершены")

	events := rec.all()
	if len(events) != 2 {
		t.Fatalf("отправлено событий: %d, ожидалось 2", len(events))
	}

	first, ok := events[0].(EventLog)
	if !ok {
		t.Fatalf("событие 0: %T, ожидалось EventLog", events[0])
	}
	if first.Tag != "ГРУППА #2" {
		t.Errorf("тег = %q, ожидался %q", first.Tag, "ГРУППА #2")
	}
	if first.Msg != "Креды OK, TURN: 3" {
		t.Errorf("текст = %q", first.Msg)
	}
	if first.Level != LevelInfo {
		t.Errorf("уровень = %q, ожидался %q", first.Level, LevelInfo)
	}

	// logln не должен оставлять перевод строки: получатель сам решает,
	// как разделять записи.
	second := events[1].(EventLog)
	if second.Msg != "Все воркеры завершены" {
		t.Errorf("текст = %q", second.Msg)
	}
}
