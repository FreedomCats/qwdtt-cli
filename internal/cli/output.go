// output.go — вывод хода подключения: события ядра и смена состояния строками,
// статусная строка со счётчиками и аптаймом для терминала.
//
// Два приёма вывода, и они не мешают друг другу. Строки (задача G2) — одна на
// значимое событие, с временем, без перерисовки: это то, что уходит в
// перенаправленный вывод, и в файле после `tt connect > log.txt` нет ничего,
// кроме них. Статусная строка (задача G4) появляется только когда stdout —
// терминал: она живёт под последней строкой и перерисовывается возвратом
// каретки и пробелами. Escape-последовательностей нет и там: `\r` понимает
// любая консоль, включая старый conhost, а стирать строку пробелами ничем не
// хуже, чем командой терминала.
package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"qwdtt-cli/internal/core"
	"qwdtt-cli/internal/supervisor"
)

// stageLabels — как шаг подключения называется в выводе. Ключи повторяют
// core.Stage; неизвестный шаг печатается своим кодом.
var stageLabels = map[core.Stage]string{
	core.StageDNS:     "DNS",
	core.StageVK:      "креды VK",
	core.StageCaptcha: "капча",
	core.StageWrap:    "ключ маскировки",
	core.StageTURN:    "relay TURN",
	core.StageDTLS:    "DTLS",
	core.StageWorkers: "воркеры",
	core.StageVPN:     "WireGuard",
	core.StageSocks:   "SOCKS5",
	core.StageDone:    "туннель",
}

func stageLabel(stage core.Stage) string {
	if label, ok := stageLabels[stage]; ok {
		return label
	}
	return string(stage)
}

// defaultStatusWidth — ширина терминала, если её не удалось узнать.
const defaultStatusWidth = 80

// printer печатает события и переходы. Пишет только в свой Writer: в `tt
// connect` это stdout команды, и тесты подставляют туда буфер.
//
// Свой замок нужен, хотя супервизор и сериализует OnEvent с OnTransition:
// через тот же принтер пишут диспетчер капчи (из своей горутины, через logf),
// datapath (из горутин wireguard-go) и тикер статусной строки, а строки не
// должны перемешиваться.
type printer struct {
	w       io.Writer
	verbose bool
	now     func() time.Time

	mu sync.Mutex

	// model — то, из чего собирается статусная строка. Ведётся всегда,
	// печатается только при status != nil: разделять сбор и показ дешевле,
	// чем спрашивать на каждом событии, есть ли терминал.
	model statusModel
	// status — статусная строка. nil означает, что stdout не терминал и
	// строки нет.
	status *statusLine

	// lastFailed — последний отказавший шаг текущей попытки и его сообщение.
	// Нужны, чтобы строка «переподключение» не повторяла причину, которую
	// только что напечатал сам шаг.
	lastFailed    core.Stage
	lastFailedMsg string
}

func newPrinter(w io.Writer, verbose bool) *printer {
	return &printer{w: w, verbose: verbose, now: time.Now}
}

// statusLine — состояние статусной строки на экране.
type statusLine struct {
	// width отдаёт ширину терминала в знаках. Спрашивается на каждую
	// перерисовку: окно могут растянуть по ходу работы. Ноль и меньше —
	// ширина неизвестна, берётся defaultStatusWidth.
	width func() int
	// shown — сколько знаков сейчас занимает строка на экране. Ноль —
	// строки на экране нет.
	shown int
	// paused — кто-то пишет в терминал мимо принтера и ждёт ответа
	// пользователя (ручной решатель капчи). Перерисовывать строку в это время
	// нельзя: она встала бы посреди приглашения. Снимается следующей
	// строкой принтера.
	paused bool
}

// enableStatus включает статусную строку. Зовётся один раз до запуска,
// когда известно, что stdout — терминал.
func (p *printer) enableStatus(width func() int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status = &statusLine{width: width}
}

// header запоминает, сколько всего воркеров и хешей: статусная строка
// показывает «18/18», а событие ядра приносит только первую цифру.
func (p *printer) header(workers, hashes int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.model.workersTotal = workers
	p.model.hashesTotal = hashes
}

// line печатает одну строку с временем. Формат без даты: аптайм измеряется
// часами, и дата видна из соседних строк лога, если он перенаправлен.
//
// Статусная строка на время печати убирается и рисуется заново под новой
// строкой — так она всегда остаётся последней на экране.
func (p *printer) line(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearStatus()
	if p.status != nil {
		p.status.paused = false
	}
	fmt.Fprintf(p.w, "%s %s\n", p.now().Format("15:04:05"), fmt.Sprintf(format, args...))
	p.drawStatus()
}

// logf — для тех, кто пишет через func(format, args...): диспетчер капчи и
// datapath. Их строки идут на общих основаниях, без уровня.
func (p *printer) logf(format string, args ...any) {
	p.line(format, args...)
}

// refresh перерисовывает статусную строку — для тикера: аптайм идёт, даже
// когда событий нет.
func (p *printer) refresh() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.status == nil || p.status.paused {
		return
	}
	p.drawStatus()
}

// refreshEvery заводит тикер перерисовки и возвращает функцию его снятия.
// Без терминала тикер не нужен, и функция снятия пустая.
func (p *printer) refreshEvery(every time.Duration) func() {
	p.mu.Lock()
	enabled := p.status != nil
	p.mu.Unlock()
	if !enabled {
		return func() {}
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				p.refresh()
			case <-stop:
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// finish убирает статусную строку насовсем: дальше идут только строки.
// Повторный вызов безвреден.
func (p *printer) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.clearStatus()
	p.status = nil
}

// aside возвращает Writer для того, кто пишет в терминал мимо принтера и
// ждёт ответа пользователя, — для ручного решателя капчи, чьё приглашение
// уходит в stderr. Перед записью статусная строка убирается и не рисуется,
// пока принтер не напечатает следующую строку: иначе она встала бы прямо
// в приглашении, где человек вводит токен.
func (p *printer) aside(w io.Writer) io.Writer {
	return asideWriter{p: p, w: w}
}

type asideWriter struct {
	p *printer
	w io.Writer
}

func (a asideWriter) Write(b []byte) (int, error) {
	a.p.mu.Lock()
	defer a.p.mu.Unlock()
	a.p.clearStatus()
	if a.p.status != nil {
		a.p.status.paused = true
	}
	return a.w.Write(b)
}

// clearStatus стирает статусную строку с экрана. Зовётся под замком.
func (p *printer) clearStatus() {
	if p.status == nil || p.status.shown == 0 {
		return
	}
	fmt.Fprintf(p.w, "\r%s\r", strings.Repeat(" ", p.status.shown))
	p.status.shown = 0
}

// drawStatus рисует статусную строку поверх прежней. Зовётся под замком.
//
// Строка обрезается по ширине терминала за вычетом одного знака: строка во
// всю ширину переводит курсор на следующую строку, и `\r` уже не вернёт его
// назад.
func (p *printer) drawStatus() {
	if p.status == nil || p.status.paused {
		return
	}
	text := p.model.render(p.now())
	width := p.status.width()
	if width <= 0 {
		width = defaultStatusWidth
	}
	text = truncateRunes(text, width-1)

	// Новая строка может быть короче прежней: хвост прежней стирается
	// пробелами, курсор возвращается к её началу и остаётся там же, где
	// стоял бы после чистой перерисовки — в конце текста.
	n := utf8.RuneCountInString(text)
	pad := ""
	if p.status.shown > n {
		pad = strings.Repeat(" ", p.status.shown-n) + "\r" + text
	}
	fmt.Fprintf(p.w, "\r%s%s", text, pad)
	p.status.shown = n
}

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// event печатает событие ядра. Что печатать без -v, решено так: всё, что
// меняет картину для человека, — шаги, отказы, предупреждения, негодные хеши;
// без -v молчат информационные логи ядра, счётчики трафика и шаги «идёт».
func (p *printer) event(ev core.Event) {
	switch ev := ev.(type) {
	case core.EventStage:
		p.stage(ev)

	case core.EventLog:
		if ev.Level == core.LevelInfo && !p.verbose {
			return
		}
		p.line("%s%s", logPrefix(ev), ev.Msg)

	case core.EventHashFailed:
		p.mu.Lock()
		p.model.hashFailed(ev)
		p.mu.Unlock()
		// Хеш целиком печатать нельзя (C2), а различать их нужно: их бывает
		// несколько, и негодным становится один.
		p.line("хеш %s (группа %d): %s — %s", supervisor.ShortHash(ev.Hash), ev.Group,
			hashStatusLabel(ev.Status), ev.Msg)

	case core.EventStats:
		p.mu.Lock()
		p.model.workers = int(ev.ActiveWorkers)
		p.model.up = ev.BytesUp
		p.model.down = ev.BytesDown
		p.mu.Unlock()
		if p.verbose {
			p.line("воркеров на линии %d, отправлено %s, получено %s",
				ev.ActiveWorkers, formatBytes(ev.BytesUp), formatBytes(ev.BytesDown))
		} else {
			p.refresh()
		}

	case core.EventConfig:
		// Содержимое — с приватным ключом, его не печатают ни на каком уровне.
		if p.verbose {
			p.line("сервер выдал конфиг WireGuard, поднимается datapath")
		}

	case core.EventVkAuthRequired:
		p.line("ядру нужны TURN-креды от аккаунта VK (%s): режим аккаунта в Э1 не поддерживается", ev.Link)

	case core.EventCaptchaRequired:
		// О капче говорит диспетчер решателя — он знает, возьмётся ли за неё.

	case core.EventHashCheck:
		p.hashCheck(ev)

	case core.EventPing:
		// Неудача не печатается: RunPing возвращает ту же ошибку, и её
		// печатает общий обработчик команды — в stderr, с кодом возврата 1.
		if ev.Err == nil {
			p.line("задержка до сервера через TURN: %d мс", ev.RTT.Milliseconds())
		}
	}
}

// hashCheck печатает строку таблицы `tt hashes`: номер, хеш (обрезанный —
// целиком он секрет), статус словом из ядра и пояснение. Столбцы выровнены,
// чтобы статусы читались сверху вниз; начало проверки — только с -v.
func (p *printer) hashCheck(ev core.EventHashCheck) {
	short := supervisor.ShortHash(ev.Hash)
	if ev.Status == core.HashCheckRunning {
		if p.verbose {
			p.line("хеш %d/%d %s: проверяется", ev.Index, ev.Total, short)
		}
		return
	}
	p.line("хеш %d/%d %-9s %-8s %s", ev.Index, ev.Total, short, ev.Status, strings.TrimSpace(ev.Msg))
}

func (p *printer) stage(ev core.EventStage) {
	label := stageLabel(ev.Stage)
	who := ""
	if ev.Session != 0 {
		who = fmt.Sprintf(" (воркер %d)", ev.Session)
	}

	switch ev.Status {
	case core.StageRunning:
		if p.verbose {
			p.line("шаг %s%s: идёт", label, who)
		}
	case core.StageOK:
		p.line("шаг %s%s: готов", label, who)
	case core.StageFailed:
		p.mu.Lock()
		p.lastFailed, p.lastFailedMsg = ev.Stage, strings.TrimSpace(ev.Msg)
		p.mu.Unlock()
		p.line("шаг %s%s: отказ%s", label, who, detail(ev.Msg))
	default:
		p.line("шаг %s%s: %s%s", label, who, ev.Status, detail(ev.Msg))
	}
}

// transition печатает смену состояния — §07 требует её строкой.
func (p *printer) transition(tr supervisor.Transition) {
	p.mu.Lock()
	p.model.moved(tr, p.now())
	failed, failedMsg := p.lastFailed, p.lastFailedMsg
	if tr.To == supervisor.StateResolving {
		// Новая попытка — новая таблица шагов, прежний отказ к ней не относится.
		p.lastFailed, p.lastFailedMsg = "", ""
	}
	p.mu.Unlock()

	switch tr.To {
	case supervisor.StateReconnecting:
		if tr.Cause != nil {
			// Причину, которую только что напечатал отказавший шаг, второй
			// раз не повторяем — достаточно назвать шаг.
			if failed != "" && failedMsg != "" && strings.Contains(tr.Cause.Error(), failedMsg) {
				p.line("== %s: отказ шага %s", tr.To.Label(), stageLabel(failed))
				return
			}
			p.line("== %s: %v", tr.To.Label(), tr.Cause)
			return
		}
	case supervisor.StateResolving:
		// Начало попытки видно и так — по шагам; строка о состоянии здесь
		// только шумит. Исключение — первая попытка, ей нужно откуда-то начаться.
		if tr.From != supervisor.StateIdle {
			p.refresh()
			return
		}
	}
	p.line("== %s", tr.To.Label())
}

// ──────────────────────── статусная строка ────────────────────────

// statusModel — то, что показывает статусная строка. Собирается из тех же
// событий и переходов, что идут в строки: у принтера они уже есть, и
// спрашивать супервизор отдельно незачем.
type statusModel struct {
	state supervisor.State
	// since — когда вошли в текущее состояние.
	since time.Time
	// connectedAt — когда туннель поднялся; нулевое время — не поднят.
	// Переход в «трафика нет» и обратно аптайм не сбрасывает.
	connectedAt time.Time
	// attempt — номер текущей попытки с начала работы.
	attempt int

	workers, workersTotal int
	up, down              int64

	hashesTotal int
	// dead — исключённые хеши. Множество, а не счётчик: об одном хеше могут
	// сообщить несколько групп.
	dead map[string]bool
}

func (m *statusModel) moved(tr supervisor.Transition, now time.Time) {
	m.state = tr.To
	m.since = now
	switch tr.To {
	case supervisor.StateResolving:
		m.attempt++
		m.connectedAt = time.Time{}
		m.workers = 0
	case supervisor.StateConnected:
		if tr.From != supervisor.StateDegraded {
			m.connectedAt = now
		}
	case supervisor.StateDegraded:
		// Аптайм продолжает идти: туннель поднят, только молчит.
	default:
		m.connectedAt = time.Time{}
	}
}

func (m *statusModel) hashFailed(ev core.EventHashFailed) {
	if ev.Status != core.HashCheckDead {
		return
	}
	if m.dead == nil {
		m.dead = map[string]bool{}
	}
	m.dead[ev.Hash] = true
}

// render собирает строку: состояние, время в нём, и дальше то, что в этом
// состоянии есть смысл показывать. Поля разделяются точкой посередине.
func (m *statusModel) render(now time.Time) string {
	if m.state == "" {
		return ""
	}
	parts := []string{m.state.Label()}

	switch m.state {
	case supervisor.StateConnected, supervisor.StateDegraded:
		if !m.connectedAt.IsZero() {
			parts = append(parts, formatDuration(now.Sub(m.connectedAt)))
		}
		if m.workersTotal > 0 {
			parts = append(parts, fmt.Sprintf("воркеров %d/%d", m.workers, m.workersTotal))
		} else {
			parts = append(parts, fmt.Sprintf("воркеров %d", m.workers))
		}
		parts = append(parts, fmt.Sprintf("↑ %s ↓ %s", formatBytes(m.up), formatBytes(m.down)))
	default:
		if !m.since.IsZero() {
			parts = append(parts, formatDuration(now.Sub(m.since)))
		}
		if m.attempt > 1 {
			parts = append(parts, fmt.Sprintf("попытка %d", m.attempt))
		}
	}

	if len(m.dead) > 0 && m.hashesTotal > 0 {
		parts = append(parts, fmt.Sprintf("хешей %d/%d", m.hashesTotal-len(m.dead), m.hashesTotal))
	}
	return strings.Join(parts, " · ")
}

// formatDuration — длительность в виде «5с», «3м 05с», «1ч 02м 15с», «2д 03ч».
// Точность падает с ростом: за сутки секунды никого не интересуют.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	h := int(d / time.Hour)
	d -= time.Duration(h) * time.Hour
	m := int(d / time.Minute)
	s := int((d - time.Duration(m)*time.Minute) / time.Second)

	switch {
	case days > 0:
		return fmt.Sprintf("%dд %02dч", days, h)
	case h > 0:
		return fmt.Sprintf("%dч %02dм %02dс", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dм %02dс", m, s)
	default:
		return fmt.Sprintf("%dс", s)
	}
}

// ──────────────────────────── строки ────────────────────────────

// logPrefix — тег ядра и уровень: предупреждения и ошибки помечаются словом,
// информационные строки идут как есть.
func logPrefix(ev core.EventLog) string {
	var b strings.Builder
	switch ev.Level {
	case core.LevelWarn:
		b.WriteString("предупреждение: ")
	case core.LevelError:
		b.WriteString("ошибка: ")
	}
	if tag := strings.TrimSpace(ev.Tag); tag != "" {
		b.WriteString("[" + tag + "] ")
	}
	return b.String()
}

func hashStatusLabel(status core.HashCheckStatus) string {
	switch status {
	case core.HashCheckDead:
		return "звонок закрыт, хеш исключён"
	case core.HashCheckCaptcha:
		return "VK просит капчу"
	case core.HashCheckLimited:
		return "VK ограничил запросы"
	case core.HashCheckNetwork:
		return "сетевая ошибка"
	default:
		return string(status)
	}
}

func detail(msg string) string {
	if msg = strings.TrimSpace(msg); msg != "" {
		return ": " + msg
	}
	return ""
}

// formatBytes — объём в удобных единицах. Точности в один знак достаточно:
// счётчик читают, чтобы понять, идёт ли трафик вообще.
func formatBytes(n int64) string {
	const unit = 1024
	switch {
	case n >= unit*unit*unit:
		return fmt.Sprintf("%.1f ГиБ", float64(n)/(unit*unit*unit))
	case n >= unit*unit:
		return fmt.Sprintf("%.1f МиБ", float64(n)/(unit*unit))
	case n >= unit:
		return fmt.Sprintf("%.1f КиБ", float64(n)/unit)
	default:
		return fmt.Sprintf("%d Б", n)
	}
}
