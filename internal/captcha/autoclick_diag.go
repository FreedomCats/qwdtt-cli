// autoclick_diag.go — что было на странице, когда автоклик не прошёл.
//
// Живая страница VK может разойтись со стендом: галочка с другими классами,
// в теневом корне, во фрейме. Чтобы понять это по журналу, при неудаче
// автофазы в журнал (с -v) уходит короткое описание страницы: адрес без
// запроса, готовность, фреймы и кандидаты в галочку — флажки, label,
// кнопки, текст про робота, небольшие элементы с курсором-рукой. Объём
// ограничен, значений полей, ссылок и запросов в описании нет: в адресе
// страницы капчи — данные сессии.
package captcha

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Срок описания страницы: окно человеку выходит после него, а страница,
// которая перестала отвечать, не должна его держать.
const describeTimeout = 2 * time.Second

// Пределы описания в журнале.
const (
	describeMaxElements = 40
	describeMaxBytes    = 4 << 10
)

// describeJS — описание страницы для журнала (domReport).
const describeJS = `(function () {` + domHelpersJS + `
	function cut(s, n) { s = flat(s); return s.length > n ? s.slice(0, n) + '…' : s; }
	function src(el) {
		if (el.getAttribute('src')) { return url(el.src); }
		return el.hasAttribute('srcdoc') ? 'srcdoc' : '(без src)';
	}
	var rs = roots();
	var out = {loc: url(location.href), ready: document.readyState, vis: document.visibilityState,
		vw: innerWidth, vh: innerHeight, shadows: 0, frames: [], els: [], total: 0};
	rs.list.forEach(function (c) {
		if (/shadow$/.test(c.where)) { out.shadows++; }
		if (/frame$/.test(c.where)) {
			var r = c.host.getBoundingClientRect();
			out.frames.push({src: src(c.host), same: true, x: c.dx, y: c.dy, w: r.width, h: r.height});
		}
	});
	rs.foreign.forEach(function (f) {
		out.frames.push({src: src(f.el), same: false, x: f.x, y: f.y, w: f.w, h: f.h});
	});
	var seen = new Set(), picks = [];
	function add(el, c, why) {
		if (!seen.has(el)) { seen.add(el); picks.push({el: el, c: c, why: why}); }
	}
	var groups = [
		['checkbox', 'input[type="checkbox"],[role="checkbox"],[class*="heckbox"]'],
		['label', 'label'],
		['button', 'button,[role="button"],input[type="button"],input[type="submit"]']
	];
	groups.forEach(function (g) {
		rs.list.forEach(function (c) {
			var all = c.root.querySelectorAll(g[1]);
			for (var j = 0; j < all.length && j < 500; j++) { add(all[j], c, g[0]); }
		});
	});
	// Текст про робота — у элементов, где он в своих текстовых узлах.
	var about = /робот|robot/i, skip = /^(SCRIPT|STYLE|TITLE|NOSCRIPT|TEMPLATE)$/;
	rs.list.forEach(function (c) {
		var all = c.root.querySelectorAll('*');
		for (var j = 0; j < all.length && j < 5000; j++) {
			var el = all[j], own = '';
			if (skip.test(el.tagName)) { continue; }
			for (var n = el.firstChild; n; n = n.nextSibling) {
				if (n.nodeType === 3) { own += n.data; }
			}
			if (about.test(own)) { add(el, c, 'text'); }
		}
	});
	// Небольшие кликабельные: верхний элемент с курсором-рукой.
	rs.list.forEach(function (c) {
		var all = c.root.querySelectorAll('*');
		for (var j = 0; j < all.length && j < 3000; j++) {
			var el = all[j], win = el.ownerDocument.defaultView;
			if (seen.has(el) || !win) { continue; }
			var r = el.getBoundingClientRect();
			if (r.width < 5 || r.height < 5 || r.width > 420 || r.height > 160) { continue; }
			var p = el.parentElement;
			if (win.getComputedStyle(el).cursor === 'pointer' && !(p && win.getComputedStyle(p).cursor === 'pointer')) {
				add(el, c, 'pointer');
			}
		}
	});
	out.total = picks.length;
	out.els = picks.slice(0, 40).map(function (p) {
		var el = p.el, r = el.getBoundingClientRect(), aria = [];
		for (var i = 0; i < el.attributes.length && aria.length < 4; i++) {
			var a = el.attributes[i];
			if (a.name.indexOf('aria-') === 0) { aria.push(a.name.slice(5) + '=' + cut(a.value, 30)); }
		}
		return {why: p.why, where: p.c.where, tag: el.tagName.toLowerCase(), id: cut(el.id, 40),
			cls: cut(el.getAttribute('class'), 80), role: cut(el.getAttribute('role'), 20), aria: aria.join(' '),
			for: cut(el.getAttribute('for'), 40), type: cut(el.getAttribute('type'), 20),
			text: cut(el.textContent, 60), x: r.left + p.c.dx, y: r.top + p.c.dy, w: r.width, h: r.height,
			shown: !!shown(el, p.c)};
	});
	return out;
})()`

// domReport — ответ describeJS.
type domReport struct {
	Loc     string       `json:"loc"`
	Ready   string       `json:"ready"`
	Vis     string       `json:"vis"`
	VW      float64      `json:"vw"`
	VH      float64      `json:"vh"`
	Shadows int          `json:"shadows"`
	Frames  []domFrame   `json:"frames"`
	Total   int          `json:"total"`
	Els     []domElement `json:"els"`
}

type domFrame struct {
	Src  string  `json:"src"`
	Same bool    `json:"same"` // того же происхождения: внутрь заглянули
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
}

type domElement struct {
	Why   string  `json:"why"`   // почему кандидат: checkbox, label, button, text, pointer
	Where string  `json:"where"` // document, shadow, frame, …
	Tag   string  `json:"tag"`
	ID    string  `json:"id"`
	Class string  `json:"cls"`
	Role  string  `json:"role"`
	Aria  string  `json:"aria"`
	For   string  `json:"for"`
	Type  string  `json:"type"`
	Text  string  `json:"text"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	W     float64 `json:"w"`
	H     float64 `json:"h"`
	Shown bool    `json:"shown"`
}

// describePage пишет в журнал описание страницы d.
func describePage(ctx context.Context, d autoDriver, logf func(string, ...any)) {
	dctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()
	var r domReport
	if err := evaluate(dctx, d, describeJS, &r); err != nil {
		if ctx.Err() == nil {
			logf("[CAPTCHA] автоклик: страницу не описать: %v", err)
		}
		return
	}
	for _, line := range r.lines() {
		logf("[CAPTCHA] %s", line)
	}
}

// lines — описание строками журнала, не больше describeMaxBytes вместе.
func (r domReport) lines() []string {
	shown := min(len(r.Els), describeMaxElements)
	head := fmt.Sprintf("страница при неудаче автоклика: %s, readyState %s, видимость %s, окно %.0f×%.0f; "+
		"теневых корней %d, фреймов %d; кандидатов в галочку %d",
		safeURL(r.Loc), clean(r.Ready, 20), clean(r.Vis, 20), r.VW, r.VH, r.Shadows, len(r.Frames), r.Total)
	body := make([]string, 0, len(r.Frames)+shown)
	for _, f := range r.Frames {
		kind := "своего происхождения"
		if !f.Same {
			kind = "чужого происхождения — внутрь не заглянуть"
		}
		body = append(body, fmt.Sprintf("  фрейм %s: %s %s", kind, safeURL(f.Src), rectText(f.X, f.Y, f.W, f.H)))
	}
	for i, e := range r.Els[:shown] {
		body = append(body, fmt.Sprintf("  %d. %s", i+1, e.line()))
	}
	out := []string{head}
	size := len(head)
	for i, s := range body {
		if size+len(s) > describeMaxBytes {
			return append(out, fmt.Sprintf("  … описание обрезано, не показано строк: %d", len(body)-i))
		}
		out = append(out, s)
		size += len(s)
	}
	if r.Total > shown {
		out = append(out, fmt.Sprintf("  … ещё кандидатов: %d", r.Total-shown))
	}
	return out
}

// line — кандидат одной строкой: почему и где, тег с атрибутами, текст,
// прямоугольник, виден ли.
func (e domElement) line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s: %s", clean(e.Why, 10), clean(e.Where, 40), clean(e.Tag, 20))
	attr := func(name, v string, n int) {
		if v = clean(v, n); v != "" {
			fmt.Fprintf(&b, " %s=%q", name, v)
		}
	}
	attr("id", e.ID, 40)
	attr("class", e.Class, 80)
	attr("role", e.Role, 20)
	attr("type", e.Type, 20)
	attr("for", e.For, 40)
	attr("aria", e.Aria, 130)
	if t := clean(e.Text, 60); t != "" {
		fmt.Fprintf(&b, " «%s»", t)
	}
	vis := "скрыт"
	if e.Shown {
		vis = "виден"
	}
	fmt.Fprintf(&b, " %s %s", rectText(e.X, e.Y, e.W, e.H), vis)
	return b.String()
}

func rectText(x, y, w, h float64) string { return fmt.Sprintf("%.0f,%.0f %.0f×%.0f", x, y, w, h) }

// safeURL — адрес без запроса и якоря (страница их уже отрезает; здесь —
// на всякий случай).
func safeURL(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	return clean(s, 200)
}

// clean — строка со страницы для журнала: без управляющих символов (в
// консоль не должно попасть ничего, что ею управляет) и не длиннее n рун.
func clean(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n]) + "…"
	}
	return s
}
