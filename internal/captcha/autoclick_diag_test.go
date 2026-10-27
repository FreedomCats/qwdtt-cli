package captcha

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// logLines — журнал, собранный в строки.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logLines) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// Описание страницы не несёт запросов адресов (данные сессии) и
// управляющих символов со страницы.
func TestDescribeNoSecrets(t *testing.T) {
	t.Parallel()
	dom := &domReport{
		Loc: "https://id.vk.com/not_robot_captcha?session_token=SECRET1&domain=vk.com", Ready: "complete",
		Vis: "visible", VW: 500, VH: 680, Shadows: 1, Total: 2,
		Frames: []domFrame{{Src: "https://example.com/frame#SECRET2", Same: false, X: 10, Y: 20, W: 300, H: 150}},
		Els: []domElement{
			{Why: "checkbox", Where: "shadow", Tag: "input", ID: "c", Type: "checkbox", Text: "Я не робот", X: 40,
				Y: 120, W: 1, H: 1},
			{Why: "text", Where: "document", Tag: "span", Class: "a\x1b[31mb", Text: "робот‮\r\n", X: 60, Y: 120,
				W: 90, H: 20, Shown: true},
		},
	}
	var log logLines
	describePage(context.Background(), &fakePage{dom: dom}, log.logf)
	text := log.text()
	t.Logf("журнал:\n%s", text)
	for _, bad := range []string{"SECRET", "session_token", "domain=", "\x1b", "‮", "\r"} {
		if strings.Contains(text, bad) {
			t.Errorf("в журнале %q", bad)
		}
	}
	for _, want := range []string{
		"https://id.vk.com/not_robot_captcha,", "теневых корней 1", "фрейм чужого происхождения",
		"https://example.com/frame 10,20 300×150", `1. checkbox/shadow: input id="c" type="checkbox" «Я не робот» 40,120 1×1 скрыт`,
		"2. text/document: span", "виден",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("в журнале нет %q", want)
		}
	}
}

// Описание ограничено: не больше 40 кандидатов и ~4 КиБ.
func TestDescribeLimits(t *testing.T) {
	t.Parallel()
	dom := &domReport{Loc: "https://id.vk.com/x", Ready: "complete", Total: 300}
	for i := range describeMaxElements {
		dom.Els = append(dom.Els, domElement{Why: "pointer", Where: "document", Tag: "div",
			Class: strings.Repeat("длинный-класс ", 20), Text: strings.Repeat("т", 200), W: 10, H: 10, X: float64(i)})
	}
	lines := dom.lines()
	size := 0
	for _, l := range lines[:len(lines)-1] {
		size += len(l)
	}
	if size > describeMaxBytes {
		t.Errorf("описание %d байт, предел %d", size, describeMaxBytes)
	}
	if last := lines[len(lines)-1]; !strings.Contains(last, "обрезано") {
		t.Errorf("последняя строка %q — не о том, что описание обрезано", last)
	}
	for _, l := range lines {
		if strings.Contains(l, strings.Repeat("т", 61)) {
			t.Errorf("текст не обрезан: %q", l)
		}
	}
	// Мало кандидатов, но страница сказала, что их больше: сколько не показано.
	small := domReport{Loc: "https://id.vk.com/x", Total: 50, Els: dom.Els[:2]}
	if l := small.lines(); !strings.Contains(l[len(l)-1], "ещё кандидатов: 48") {
		t.Errorf("описание: %q", l)
	}
}

// Неудача автофазы: описание страницы — в журнал до того, как окно выходит
// человеку.
func TestAutoRunDiagnostics(t *testing.T) {
	t.Parallel()
	req := Request{Mode: ModeSelected, Deadline: time.Now().Add(3 * time.Minute)}
	var log logLines
	var out strings.Builder
	revealedAfter := ""
	a := newAutoRun(&out, log.logf, req, func() { revealedAfter = log.text() })
	a.timing = fastTiming
	page := &fakePage{
		state: func(bool) pageProbe { return pageProbe{State: "none", Ready: "complete"} },
		dom: &domReport{Loc: "https://id.vk.com/not_robot_captcha", Ready: "complete", Total: 1,
			Els: []domElement{{Why: "label", Where: "document", Tag: "label", Text: "Я не робот", W: 100, H: 20}}},
	}
	a.run(context.Background(), page, nil)
	if !strings.Contains(revealedAfter, "страница при неудаче автоклика: https://id.vk.com/not_robot_captcha") ||
		!strings.Contains(revealedAfter, "«Я не робот»") {
		t.Errorf("до показа окна в журнале нет описания страницы:\n%s", revealedAfter)
	}
}
