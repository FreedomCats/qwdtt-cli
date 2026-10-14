package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
	"github.com/FreedomCats/qwdtt-cli/internal/supervisor"
)

// fixedClock — часы принтера в тестах: время в строках предсказуемо, аптайм
// двигается вручную.
type fixedClock struct{ t time.Time }

func newClock() *fixedClock {
	return &fixedClock{t: time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)}
}

func (c *fixedClock) now() time.Time { return c.t }

func (c *fixedClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func testPrinter(verbose bool) (*printer, *bytes.Buffer, *fixedClock) {
	var buf bytes.Buffer
	clock := newClock()
	p := newPrinter(&buf, verbose)
	p.now = clock.now
	return p, &buf, clock
}

func move(p *printer, from, to supervisor.State) {
	p.transition(supervisor.Transition{From: from, To: to})
}

// connect проводит принтер по успешной линии до «туннель работает».
func connect(p *printer) {
	move(p, supervisor.StateIdle, supervisor.StateResolving)
	p.event(core.EventStage{Stage: core.StageDNS, Status: core.StageOK})
	move(p, supervisor.StateResolving, supervisor.StateConfiguring)
	p.event(core.EventStage{Stage: core.StageDone, Status: core.StageOK})
	move(p, supervisor.StateConfiguring, supervisor.StateConnected)
}

func TestRedirectedOutputIsPlainLines(t *testing.T) {
	p, buf, clock := testPrinter(false)
	p.header(18, 1)
	connect(p)
	p.event(core.EventStats{ActiveWorkers: 18, BytesUp: 2048, BytesDown: 3 * 1024 * 1024})
	clock.advance(5 * time.Second)
	p.refresh()
	p.finish()
	p.line("остановлено")

	got := buf.String()
	for _, forbidden := range []string{"\r", "\x1b"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("в перенаправленном выводе есть %q:\n%s", forbidden, got)
		}
	}
	want := []string{
		"12:00:00 == проверка DNS\n",
		"12:00:00 шаг DNS: готов\n",
		"12:00:00 == поднимается туннель\n",
		"12:00:00 шаг туннель: готов\n",
		"12:00:00 == туннель работает\n",
		"12:00:05 остановлено\n",
	}
	if got != strings.Join(want, "") {
		t.Fatalf("вывод:\n%s\nожидалось:\n%s", got, strings.Join(want, ""))
	}
}

func TestVerbosePrintsStatsAndRunningStages(t *testing.T) {
	p, buf, _ := testPrinter(true)
	p.event(core.EventStage{Stage: core.StageTURN, Status: core.StageRunning, Session: 3})
	p.event(core.EventLog{Level: core.LevelInfo, Tag: "S3", Msg: "relay получен"})
	p.event(core.EventStats{ActiveWorkers: 9, BytesUp: 10, BytesDown: 20})

	got := buf.String()
	for _, want := range []string{
		"шаг relay TURN (воркер 3): идёт\n",
		"[S3] relay получен\n",
		"воркеров на линии 9, отправлено 10 Б, получено 20 Б\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("нет строки %q в выводе:\n%s", want, got)
		}
	}
}

// Сброшенные ядром пакеты: с -v — в строке счётчиков, а итог за все попытки
// копится через перезапуски: у каждой попытки ядро считает с нуля.
func TestDroppedPackets(t *testing.T) {
	p, buf, _ := testPrinter(true)
	p.event(core.EventStats{ActiveWorkers: 9, BytesUp: 10, BytesDown: 20})
	if strings.Contains(buf.String(), "сброшено") {
		t.Errorf("нулевой сброс напечатан:\n%s", buf.String())
	}

	move(p, supervisor.StateIdle, supervisor.StateResolving)
	p.event(core.EventStats{ActiveWorkers: 9, DroppedUp: 3, DroppedDown: 7})
	if !strings.Contains(buf.String(), "сброшено пакетов туда 3, обратно 7\n") {
		t.Errorf("нет сброшенных в строке счётчиков:\n%s", buf.String())
	}

	// Новая попытка: ядро начинает счёт заново, прежние числа не теряются.
	move(p, supervisor.StateConnected, supervisor.StateResolving)
	p.event(core.EventStats{ActiveWorkers: 9, DroppedUp: 1})
	if up, down := p.dropped(); up != 4 || down != 7 {
		t.Errorf("итог: туда %d, обратно %d; ожидалось 4 и 7", up, down)
	}
}

func TestQuietHidesInfoLogsButKeepsWarnings(t *testing.T) {
	p, buf, _ := testPrinter(false)
	p.event(core.EventLog{Level: core.LevelInfo, Msg: "тихо"})
	p.event(core.EventLog{Level: core.LevelWarn, Tag: "WG", Msg: "громко"})
	p.event(core.EventStats{ActiveWorkers: 9})

	got := buf.String()
	if strings.Contains(got, "тихо") {
		t.Errorf("info-лог напечатан без -v:\n%s", got)
	}
	if !strings.Contains(got, "предупреждение: [WG] громко\n") {
		t.Errorf("предупреждение не напечатано:\n%s", got)
	}
	if strings.Contains(got, "воркеров") {
		t.Errorf("счётчики напечатаны без -v:\n%s", got)
	}
}

func TestReconnectDoesNotRepeatStageFailure(t *testing.T) {
	p, buf, _ := testPrinter(false)
	move(p, supervisor.StateIdle, supervisor.StateResolving)
	p.event(core.EventStage{Stage: core.StageDNS, Status: core.StageFailed, Msg: "lookup login.vk.ru: no such host"})
	p.transition(supervisor.Transition{
		From:  supervisor.StateResolving,
		To:    supervisor.StateReconnecting,
		Cause: errors.New("dns: lookup login.vk.ru: no such host"),
	})

	got := buf.String()
	if !strings.Contains(got, "шаг DNS: отказ: lookup login.vk.ru: no such host\n") {
		t.Errorf("отказ шага не напечатан:\n%s", got)
	}
	if !strings.Contains(got, "== переподключение: отказ шага DNS\n") {
		t.Errorf("переход не сослался на шаг:\n%s", got)
	}
	if strings.Count(got, "no such host") != 1 {
		t.Errorf("причина напечатана дважды:\n%s", got)
	}
}

func TestReconnectPrintsOwnCause(t *testing.T) {
	p, buf, _ := testPrinter(false)
	connect(p)
	p.transition(supervisor.Transition{
		From:  supervisor.StateDegraded,
		To:    supervisor.StateReconnecting,
		Cause: errors.New("нет трафика 50 с"),
	})
	if !strings.Contains(buf.String(), "== переподключение: нет трафика 50 с\n") {
		t.Errorf("причина супервизора не напечатана:\n%s", buf.String())
	}
}

func TestReconnectForgetsFailureOfPreviousAttempt(t *testing.T) {
	p, buf, _ := testPrinter(false)
	move(p, supervisor.StateIdle, supervisor.StateResolving)
	p.event(core.EventStage{Stage: core.StageDNS, Status: core.StageFailed, Msg: "no such host"})
	move(p, supervisor.StateResolving, supervisor.StateReconnecting)
	move(p, supervisor.StateReconnecting, supervisor.StateResolving)
	p.transition(supervisor.Transition{
		From:  supervisor.StateResolving,
		To:    supervisor.StateReconnecting,
		Cause: errors.New("dns: no such host"),
	})
	// Во второй попытке шаг не отказывал — причина печатается целиком.
	if !strings.Contains(buf.String(), "== переподключение: dns: no such host\n") {
		t.Errorf("причина второй попытки не напечатана:\n%s", buf.String())
	}
}

func TestStatusModelRender(t *testing.T) {
	clock := newClock()
	var m statusModel
	if got := m.render(clock.now()); got != "" {
		t.Fatalf("пустая модель дала %q", got)
	}

	m.workersTotal, m.hashesTotal = 18, 3
	m.moved(supervisor.Transition{From: supervisor.StateIdle, To: supervisor.StateResolving}, clock.now())
	clock.advance(3 * time.Second)
	if got, want := m.render(clock.now()), "проверка DNS · 3с"; got != want {
		t.Errorf("первая попытка: %q, ожидалось %q", got, want)
	}

	m.moved(supervisor.Transition{From: supervisor.StateResolving, To: supervisor.StateReconnecting}, clock.now())
	m.moved(supervisor.Transition{From: supervisor.StateReconnecting, To: supervisor.StateResolving}, clock.now())
	m.hashFailed(core.EventHashFailed{Hash: "abc", Status: core.HashCheckDead})
	m.hashFailed(core.EventHashFailed{Hash: "abc", Status: core.HashCheckDead, Group: 2})
	m.hashFailed(core.EventHashFailed{Hash: "def", Status: core.HashCheckCaptcha})
	if got, want := m.render(clock.now()), "проверка DNS · 0с · попытка 2 · хешей 2/3"; got != want {
		t.Errorf("вторая попытка: %q, ожидалось %q", got, want)
	}

	m.moved(supervisor.Transition{From: supervisor.StateResolving, To: supervisor.StateConnected}, clock.now())
	m.workers, m.up, m.down = 18, 2048, 3*1024*1024
	clock.advance(62 * time.Second)
	if got, want := m.render(clock.now()), "туннель работает · 1м 02с · воркеров 18/18 · ↑ 2.0 КиБ ↓ 3.0 МиБ · хешей 2/3"; got != want {
		t.Errorf("подключено: %q, ожидалось %q", got, want)
	}

	// «Трафика нет» и обратно аптайм не сбрасывают.
	m.moved(supervisor.Transition{From: supervisor.StateConnected, To: supervisor.StateDegraded}, clock.now())
	clock.advance(30 * time.Second)
	if got := m.render(clock.now()); !strings.HasPrefix(got, "трафика нет · 1м 32с") {
		t.Errorf("деградация: %q", got)
	}
	m.moved(supervisor.Transition{From: supervisor.StateDegraded, To: supervisor.StateConnected}, clock.now())
	if got := m.render(clock.now()); !strings.HasPrefix(got, "туннель работает · 1м 32с") {
		t.Errorf("восстановление: %q", got)
	}

	m.moved(supervisor.Transition{From: supervisor.StateConnected, To: supervisor.StateReconnecting}, clock.now())
	if got, want := m.render(clock.now()), "переподключение · 0с · попытка 2 · хешей 2/3"; got != want {
		t.Errorf("переподключение: %q, ожидалось %q", got, want)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0с"},
		{-time.Second, "0с"},
		{7 * time.Second, "7с"},
		{65 * time.Second, "1м 05с"},
		{time.Hour + 2*time.Minute + 15*time.Second, "1ч 02м 15с"},
		{49*time.Hour + 30*time.Minute, "2д 01ч"},
	}
	for _, c := range cases {
		if got := formatDuration(c.d); got != c.want {
			t.Errorf("formatDuration(%v) = %q, ожидалось %q", c.d, got, c.want)
		}
	}
}

func TestStatusLineRedrawsUnderLines(t *testing.T) {
	p, buf, clock := testPrinter(false)
	p.enableStatus(func() int { return 80 })
	p.header(18, 1)

	move(p, supervisor.StateIdle, supervisor.StateResolving)
	if got, want := buf.String(), "12:00:00 == проверка DNS\n\rпроверка DNS · 0с"; got != want {
		t.Fatalf("после первой строки:\n%q\nожидалось:\n%q", got, want)
	}
	shown := len([]rune("проверка DNS · 0с"))

	// Следующая строка стирает статус пробелами, печатается и рисует статус
	// заново под собой.
	buf.Reset()
	clock.advance(time.Second)
	p.event(core.EventStage{Stage: core.StageDNS, Status: core.StageOK})
	want := "\r" + strings.Repeat(" ", shown) + "\r" + "12:00:01 шаг DNS: готов\n" + "\rпроверка DNS · 1с"
	if got := buf.String(); got != want {
		t.Fatalf("после второй строки:\n%q\nожидалось:\n%q", got, want)
	}

	// Перерисовка без новой строки: та же длина — только возврат каретки.
	buf.Reset()
	clock.advance(time.Second)
	p.refresh()
	if got, want := buf.String(), "\rпроверка DNS · 2с"; got != want {
		t.Fatalf("перерисовка:\n%q\nожидалось:\n%q", got, want)
	}

	// Строка стала короче — хвост прежней стирается, курсор возвращается.
	buf.Reset()
	p.model.state = supervisor.StateHandshake
	p.refresh()
	text := "рукопожатие с сервером · 2с"
	buf.Reset()
	p.model.state = supervisor.StateVK
	p.refresh()
	shorter := "получены креды VK · 2с"
	diff := len([]rune(text)) - len([]rune(shorter))
	if diff <= 0 {
		t.Fatalf("тест рассчитан на укорочение строки: %q → %q", text, shorter)
	}
	if got, want := buf.String(), "\r"+shorter+strings.Repeat(" ", diff)+"\r"+shorter; got != want {
		t.Fatalf("укорочение:\n%q\nожидалось:\n%q", got, want)
	}

	// finish убирает строку, дальше идут только строки.
	buf.Reset()
	p.finish()
	p.line("остановлено")
	if got, want := buf.String(), "\r"+strings.Repeat(" ", len([]rune(shorter)))+"\r"+"12:00:02 остановлено\n"; got != want {
		t.Fatalf("после finish:\n%q\nожидалось:\n%q", got, want)
	}
	p.finish() // повторный вызов безвреден
}

func TestStatusLineTruncatesToWidth(t *testing.T) {
	p, buf, _ := testPrinter(false)
	p.enableStatus(func() int { return 10 })
	move(p, supervisor.StateIdle, supervisor.StateResolving)
	if got, want := buf.String(), "12:00:00 == проверка DNS\n\rпроверка "; got != want {
		t.Fatalf("обрезка:\n%q\nожидалось:\n%q", got, want)
	}

	buf.Reset()
	p.enableStatus(func() int { return 0 })
	p.refresh()
	if got := buf.String(); len([]rune(got)) > defaultStatusWidth {
		t.Fatalf("без ширины строка длиннее %d: %q", defaultStatusWidth, got)
	}
}

func TestStatusLineStatsRefreshQuietly(t *testing.T) {
	p, buf, _ := testPrinter(false)
	p.enableStatus(func() int { return 80 })
	p.header(18, 1)
	connect(p)

	buf.Reset()
	p.event(core.EventStats{ActiveWorkers: 18, BytesUp: 1024, BytesDown: 2048})
	got := buf.String()
	if strings.Contains(got, "\n") {
		t.Fatalf("счётчики без -v напечатаны строкой:\n%q", got)
	}
	if !strings.Contains(got, "воркеров 18/18 · ↑ 1.0 КиБ ↓ 2.0 КиБ") {
		t.Fatalf("статусная строка без счётчиков:\n%q", got)
	}
}

func TestAsideWriterPausesStatusLine(t *testing.T) {
	p, buf, _ := testPrinter(false)
	p.enableStatus(func() int { return 80 })
	move(p, supervisor.StateIdle, supervisor.StateResolving)
	shown := len([]rune("проверка DNS · 0с"))

	var side bytes.Buffer
	buf.Reset()
	if _, err := p.aside(&side).Write([]byte("Вставьте токен: ")); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "\r"+strings.Repeat(" ", shown)+"\r"; got != want {
		t.Fatalf("перед приглашением статус не стёрт:\n%q\nожидалось:\n%q", got, want)
	}
	if side.String() != "Вставьте токен: " {
		t.Fatalf("приглашение не дошло: %q", side.String())
	}

	// Пока человек отвечает, тикер строку не рисует.
	buf.Reset()
	p.refresh()
	if buf.Len() != 0 {
		t.Fatalf("статус нарисован поверх приглашения: %q", buf.String())
	}

	// Следующая строка принтера возвращает статус.
	buf.Reset()
	p.line("капча: ответ отправлен")
	if got, want := buf.String(), "12:00:00 капча: ответ отправлен\n\rпроверка DNS · 0с"; got != want {
		t.Fatalf("после строки:\n%q\nожидалось:\n%q", got, want)
	}
}

func TestSecondAttemptStartIsNotALine(t *testing.T) {
	p, buf, _ := testPrinter(false)
	move(p, supervisor.StateIdle, supervisor.StateResolving)
	move(p, supervisor.StateResolving, supervisor.StateReconnecting)
	buf.Reset()
	move(p, supervisor.StateReconnecting, supervisor.StateResolving)
	if buf.Len() != 0 {
		t.Fatalf("начало второй попытки напечатано строкой: %q", buf.String())
	}
}
