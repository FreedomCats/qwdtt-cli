// output.go — вывод хода подключения: события ядра и смена состояния строками.
//
// Здесь первый приём (задача G2): каждое значимое событие — одна строка с
// временем, без перерисовки. Уровни вывода, статусная строка со счётчиками
// и аптаймом для терминала — задача G4; она достраивает этот файл, а не
// заменяет его: строки без escape-последовательностей остаются тем, что
// уходит в перенаправленный вывод.
package cli

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

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

// printer печатает события и переходы. Пишет только в свой Writer: в `tt
// connect` это stdout команды, и тесты подставляют туда буфер.
//
// Свой замок нужен, хотя супервизор и сериализует OnEvent с OnTransition:
// через тот же принтер пишут диспетчер капчи (из своей горутины, через logf)
// и datapath (из горутин wireguard-go), а строки не должны перемешиваться.
type printer struct {
	w       io.Writer
	verbose bool
	now     func() time.Time

	mu sync.Mutex
}

func newPrinter(w io.Writer, verbose bool) *printer {
	return &printer{w: w, verbose: verbose, now: time.Now}
}

// line печатает одну строку с временем. Формат без даты: аптайм измеряется
// часами, и дата видна из соседних строк лога, если он перенаправлен.
func (p *printer) line(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.w, "%s %s\n", p.now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

// logf — для тех, кто пишет через func(format, args...): диспетчер капчи и
// datapath. Их строки идут на общих основаниях, без уровня.
func (p *printer) logf(format string, args ...any) {
	p.line(format, args...)
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
		// Хеш целиком печатать нельзя (C2), а различать их нужно: их бывает
		// несколько, и негодным становится один.
		p.line("хеш %s (группа %d): %s — %s", supervisor.ShortHash(ev.Hash), ev.Group,
			hashStatusLabel(ev.Status), ev.Msg)

	case core.EventStats:
		if p.verbose {
			p.line("воркеров на линии %d, отправлено %s, получено %s",
				ev.ActiveWorkers, formatBytes(ev.BytesUp), formatBytes(ev.BytesDown))
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
	}
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
		p.line("шаг %s%s: отказ%s", label, who, detail(ev.Msg))
	default:
		p.line("шаг %s%s: %s%s", label, who, ev.Status, detail(ev.Msg))
	}
}

// transition печатает смену состояния — §07 требует её строкой.
func (p *printer) transition(tr supervisor.Transition) {
	switch tr.To {
	case supervisor.StateReconnecting:
		if tr.Cause != nil {
			p.line("== %s: %v", tr.To.Label(), tr.Cause)
			return
		}
	case supervisor.StateResolving:
		// Начало попытки видно и так — по шагам; строка о состоянии здесь
		// только шумит. Исключение — первая попытка, ей нужно откуда-то начаться.
		if tr.From != supervisor.StateIdle {
			return
		}
	}
	p.line("== %s", tr.To.Label())
}

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
