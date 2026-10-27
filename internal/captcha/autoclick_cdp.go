// autoclick_cdp.go — автоклик в браузере под управлением по CDP (Linux):
// страница для autoRun поверх разговора с браузером и окно за краем экрана.
//
// Окно капчи браузер открывает в фоне за нижним краем экрана
// (browser_linux.go, cdpCapture.hidden). Спрятать его может не дать
// оконный менеджер — прижать к экрану при показе (windowHidden), а в сеансе
// Wayland места окну не задать вовсе; тогда автоклик идёт в видимом окне, у
// человека на глазах. Не вышло — окно выводится человеку посередине экрана,
// на котором браузер его числит, — Browser.setWindowBounds, и на передний
// план — Page.bringToFront (revealWindow).
//
// Здесь только протокол, без ОС: тесты идут на поддельном браузере где
// угодно.
package captcha

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// cdpDriver — вкладка капчи для автоклика: команды DevTools уходят в
// разговор с браузером (runCapture, cdpCapture.drive), ответы приходят из
// него. done закрыт — разговор кончен, отвечать некому.
type cdpDriver struct {
	calls chan<- cdpCall
	done  <-chan struct{}
}

func (d *cdpDriver) devtools(ctx context.Context, method string, params any) (json.RawMessage, error) {
	call := cdpCall{method: method, params: params, reply: make(chan cdpReply, 1)}
	select {
	case d.calls <- call:
	case <-d.done:
		return nil, ErrWindowClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case r := <-call.reply:
		return r.result, r.err
	case <-d.done:
		return nil, ErrWindowClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Сроки работы с окном: проверка, спряталось ли оно, и показ человеку;
// revealScreenTimeout — часть revealTimeout на скрипт страницы (экран):
// остальное — на перестановку окна, которой рендерер не нужен.
const (
	hiddenCheckTimeout  = 5 * time.Second
	revealTimeout       = 5 * time.Second
	revealScreenTimeout = 2 * time.Second
)

// screenJS — рабочая область экрана, на котором браузер числит окно (для
// окна за краем — ближайшего), в координатах экрана.
const screenJS = `({l: screen.availLeft, t: screen.availTop, w: screen.availWidth, h: screen.availHeight})`

// screenRect — прямоугольник в координатах экрана (DIP).
type screenRect struct {
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// windowGeom — окно и экран, на котором браузер его числит.
type windowGeom struct {
	id     int64
	window screenRect
	screen screenRect
}

// onScreen — окно хоть краем на этом экране.
func (g windowGeom) onScreen() bool {
	w, s := g.window, g.screen
	return w.Left < s.Left+s.Width && w.Left+w.Width > s.Left && w.Top < s.Top+s.Height && w.Top+w.Height > s.Top
}

// centered — место окна посередине экрана, не выше и не левее его рабочей
// области.
func (g windowGeom) centered() (left, top float64) {
	w, s := g.window, g.screen
	return s.Left + max(0, (s.Width-w.Width)/2), s.Top + max(0, (s.Height-w.Height)/2)
}

// geometry — где сейчас окно вкладки и её экран.
func geometry(ctx context.Context, d autoDriver) (windowGeom, error) {
	g, err := windowOf(ctx, d)
	if err != nil {
		return g, err
	}
	g.screen, err = screenOf(ctx, d)
	return g, err
}

// windowOf — окно вкладки: Browser.getWindowForTarget в сессии вкладки берёт
// её окно, положение — то, что браузер знает от оконной системы. Отвечает
// сам браузер, рендерер страницы для этого не нужен.
func windowOf(ctx context.Context, d autoDriver) (windowGeom, error) {
	raw, err := d.devtools(ctx, "Browser.getWindowForTarget", nil)
	if err != nil {
		return windowGeom{}, err
	}
	var w struct {
		WindowID int64      `json:"windowId"`
		Bounds   screenRect `json:"bounds"`
	}
	if err := json.Unmarshal(raw, &w); err != nil || w.WindowID == 0 {
		return windowGeom{}, fmt.Errorf("ответ Browser.getWindowForTarget: %s", raw)
	}
	return windowGeom{id: w.WindowID, window: w.Bounds}, nil
}

// screenOf — экран окна вкладки: скрипт на странице (screenJS).
func screenOf(ctx context.Context, d autoDriver) (screenRect, error) {
	var s struct {
		L float64 `json:"l"`
		T float64 `json:"t"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	if err := evaluate(ctx, d, screenJS, &s); err != nil {
		return screenRect{}, err
	}
	return screenRect{s.L, s.T, s.W, s.H}, nil
}

// windowHidden — окно за краем экрана? Пока страница открывается, скрипт на
// ней может не пройти — спрашивается снова, до hiddenCheckTimeout. Не
// узнать — ошибка: тогда окно считается спрятанным (не выйдет автоклик —
// окно человеку).
func windowHidden(ctx context.Context, d autoDriver, poll time.Duration) (windowGeom, bool, error) {
	cctx, cancel := context.WithTimeout(ctx, hiddenCheckTimeout)
	defer cancel()
	for {
		g, err := geometry(cctx, d)
		if err == nil {
			return g, !g.onScreen(), nil
		}
		if sleep(cctx, poll) != nil {
			if ctx.Err() != nil {
				return g, false, ctx.Err()
			}
			return g, false, err
		}
	}
}

// revealWindow выводит окно человеку: посередине экрана, на котором браузер
// его числит, и — если front — на передний план с фокусом.
//
// Экран знает только страница, а она может не отвечать (зависший рендерер,
// диалог JS — от этого автоклик и мог не пройти). Тогда окно всё равно
// уходит с края — в левый верхний угол экрана: перестановке окна и
// переднему плану рендерер не нужен.
func revealWindow(ctx context.Context, d autoDriver, front bool, logf func(string, ...any)) error {
	g, err := windowOf(ctx, d)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, revealScreenTimeout)
	g.screen, err = screenOf(sctx, d)
	cancel()
	var left, top float64
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case err != nil:
		logf("[CAPTCHA] экран окна капчи не узнать (%v) — окно в левый верхний угол", err)
	default:
		left, top = g.centered()
	}
	// Развёрнутое, свёрнутое или полноэкранное окно Chromium не переставляет
	// (просит сначала вернуть обычный размер) — на передний план оно всё
	// равно выходит.
	bounds := map[string]any{"left": int(left), "top": int(top)}
	_, moveErr := d.devtools(ctx, "Browser.setWindowBounds", map[string]any{"windowId": g.id, "bounds": bounds})
	if moveErr != nil {
		logf("[CAPTCHA] окно капчи не переставить: %v", moveErr)
	}
	if !front {
		return moveErr
	}
	if _, err := d.devtools(ctx, "Page.bringToFront", nil); err != nil && moveErr == nil {
		return err
	}
	return moveErr
}
