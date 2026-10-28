//go:build windows

// hiddenwindow_windows.go — окно капчи за краем экрана: пока идёт автоклик,
// человек его не видит.
//
// go-webview2 создаёт окно сам, посередине экрана, и сразу показывает его с
// фокусом (ShowWindow, SetFocus). Опций положения у него нет, поэтому на
// нити окна, от его создания до закрытия, стоит перехватчик WH_CBT — только
// для этой нити:
//
//   - HCBT_CREATEWND — окно класса go-webview2 создаётся правее виртуального
//     экрана (всех мониторов сразу) и без кнопки на панели задач
//     (WS_EX_TOOLWINDOW);
//   - HCBT_ACTIVATE и HCBT_SETFOCUS — окно не активируется и не берёт фокус,
//     пока скрыто: у человека ничего не мигает и не пропадает фокус.
//
// Показ человеку (reveal) возвращает кнопку на панели задач, ставит окно
// посередине основного монитора, как go-webview2, и выводит на передний план.
//
// Окно, которое Chromium сочтёт перекрытым, становится hidden:
// requestAnimationFrame замирает, таймеры душатся. По опыту (стенд, в том
// числе 10 с за краем) окно за краем экрана перекрытым не считается и страница
// видна и без флагов; аргументы hiddenBrowserArgs — страховка на другие версии
// рантайма и Windows.
package captcha

import (
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// hiddenBrowserArgs — аргументы браузера окна, которое создаётся за краем
// экрана: не считать окно перекрытым и не притормаживать его.
const hiddenBrowserArgs = "--disable-features=CalculateNativeWinOcclusion " +
	"--disable-backgrounding-occluded-windows --disable-renderer-backgrounding"

// webviewClass — класс окна go-webview2 (CreateWithOptions).
const webviewClass = "webview"

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procGetClassNameW       = user32.NewProc("GetClassNameW")
	procGetWindowLongPtrW   = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW   = user32.NewProc("SetWindowLongPtrW")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procGetWindowRect       = user32.NewProc("GetWindowRect")
	procSetWindowPos        = user32.NewProc("SetWindowPos")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

const (
	whCBT          = 5
	hcbtCreateWnd  = 3
	hcbtActivate   = 5
	hcbtSetFocus   = 9
	wsExToolWindow = 0x00000080
	wsExAppWindow  = 0x00040000

	smCXScreen        = 0
	smCYScreen        = 1
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79

	swHide           = 0
	swShow           = 5
	swShowNoActivate = 4

	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020
)

// gwlExStyle — GWL_EXSTYLE. Переменной, а не константой: индекс
// отрицательный, и в uintptr для вызова он переводится только так.
var gwlExStyle = -20

// rect — RECT.
type rect struct{ Left, Top, Right, Bottom int32 }

// createStruct — CREATESTRUCTW.
type createStruct struct {
	CreateParams uintptr
	Instance     uintptr
	Menu         uintptr
	Parent       uintptr
	Cy, Cx       int32
	Y, X         int32
	Style        int32
	Name         *uint16
	Class        *uint16
	ExStyle      uint32
}

// cbtCreateWnd — CBT_CREATEWNDW.
type cbtCreateWnd struct {
	CS          *createStruct
	InsertAfter uintptr
}

// hiddenWindow — окно одной нити, которое создаётся и держится за краем
// экрана. Поля — под hiddenMu: перехватчик зовётся на нити окна, reveal —
// тоже, но состояние читают и тесты.
type hiddenWindow struct {
	hook   uintptr
	hwnd   uintptr
	hidden bool // окно ещё не показано человеку: не активировать, не давать фокус
	quiet  bool // показать без переднего плана и без активации (тесты)
	// where — где окно создано (для тестов: было ли оно на экране).
	where rect
	// x, y — куда окно ставить; считаются заранее, в hideNextWindow: в самом
	// перехватчике GetSystemMetrics отвечает в 96 DPI, даже когда нить окна
	// осведомлена о DPI (dpi_windows.go), а координаты окна — физические.
	x, y int32
	// dpi — DPI монитора, ближайшего к (x, y): за ним окно числится, пока
	// скрыто, под него задаётся размер (scale) и от него пересчитывается при
	// показе; 0 — не узнать.
	dpi uint32
}

// scale — масштаб, под который задавать размер скрытого окна: монитора, за
// которым оно будет числиться, а не основного (основной 100 %, крайний
// справа 200 % — с масштабом основного страница вышла бы вдвое уже, и клик
// мог уйти за окно). Не узнать — fallback.
func (hw *hiddenWindow) scale(fallback float64) float64 { return scaleOf(hw.dpi, fallback) }

var (
	hiddenOnce sync.Once
	hiddenProc uintptr
	hiddenMu   sync.Mutex
	// hiddenByThread — скрытые окна по нитям: перехватчик WH_CBT один на
	// процесс, а окон капчи может быть открыто больше одного.
	hiddenByThread = map[uint32]*hiddenWindow{}
)

// hideNextWindow ставит перехватчик на текущую нить: окно go-webview2,
// созданное на ней дальше, окажется за краем экрана. Звать на нити окна,
// после dpiAwareThread и до webview2.NewWithOptions; снять — unhook, на той
// же нити.
func hideNextWindow(quiet bool) (*hiddenWindow, error) {
	hiddenOnce.Do(func() { hiddenProc = windows.NewCallback(cbtHook) })
	tid := windows.GetCurrentThreadId()
	h, _, err := procSetWindowsHookExW.Call(whCBT, hiddenProc, 0, uintptr(tid))
	if h == 0 {
		return nil, err
	}
	hw := &hiddenWindow{hook: h, hidden: true, quiet: quiet}
	hw.x, hw.y = offscreen()
	hw.dpi = monitorDPIAt(hw.x, hw.y)
	hiddenMu.Lock()
	hiddenByThread[tid] = hw
	hiddenMu.Unlock()
	return hw, nil
}

// unhook снимает перехватчик. На нити окна.
func (hw *hiddenWindow) unhook() {
	hiddenMu.Lock()
	delete(hiddenByThread, windows.GetCurrentThreadId())
	h := hw.hook
	hw.hook = 0
	hiddenMu.Unlock()
	if h != 0 {
		procUnhookWindowsHookEx.Call(h)
	}
}

// offscreen — где поставить окно, чтобы его не было видно ни на одном
// мониторе: правее виртуального экрана.
func offscreen() (x, y int32) {
	vx := metric(smXVirtualScreen)
	vy := metric(smYVirtualScreen)
	vw := metric(smCXVirtualScreen)
	return vx + vw + 200, vy + 100
}

func metric(i int) int32 {
	r, _, _ := procGetSystemMetrics.Call(uintptr(i))
	return int32(r)
}

// cbtHook — перехватчик WH_CBT нити скрытого окна.
func cbtHook(code, wParam, lParam uintptr) uintptr {
	if int32(code) >= 0 {
		hiddenMu.Lock()
		hw := hiddenByThread[windows.GetCurrentThreadId()]
		block := false
		if hw != nil {
			switch int32(code) {
			case hcbtCreateWnd:
				if hw.hwnd == 0 && className(wParam) == webviewClass {
					cw := *(**cbtCreateWnd)(unsafe.Pointer(&lParam))
					hw.hwnd = wParam
					cw.CS.X, cw.CS.Y = hw.x, hw.y
					hw.where = rect{cw.CS.X, cw.CS.Y, cw.CS.X + cw.CS.Cx, cw.CS.Y + cw.CS.Cy}
					ex, _, _ := procGetWindowLongPtrW.Call(wParam, uintptr(gwlExStyle))
					procSetWindowLongPtrW.Call(wParam, uintptr(gwlExStyle), (ex|wsExToolWindow)&^wsExAppWindow)
				}
			case hcbtActivate, hcbtSetFocus:
				block = hw.hidden && wParam != 0 && wParam == hw.hwnd
			}
		}
		hiddenMu.Unlock()
		if block {
			return 1
		}
	}
	r, _, _ := procCallNextHookEx.Call(0, code, wParam, lParam)
	return r
}

func className(hwnd uintptr) string {
	var buf [64]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return windows.UTF16ToString(buf[:n])
}

// windowRect — где окно сейчас.
func windowRect(hwnd uintptr) rect {
	var r rect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r)))
	return r
}

// onScreen — пересекается ли r с виртуальным экраном.
func onScreen(r rect) bool {
	vx, vy := metric(smXVirtualScreen), metric(smYVirtualScreen)
	vr := rect{vx, vy, vx + metric(smCXVirtualScreen), vy + metric(smCYVirtualScreen)}
	return r.Left < vr.Right && r.Right > vr.Left && r.Top < vr.Bottom && r.Bottom > vr.Top
}

// reveal показывает окно человеку: кнопка на панели задач, посередине
// основного монитора, на передний план (quiet — без него). На нити окна.
func (hw *hiddenWindow) reveal() {
	hiddenMu.Lock()
	hwnd, quiet := hw.hwnd, hw.quiet
	if !quiet {
		hw.hidden = false
	}
	hiddenMu.Unlock()
	if hwnd == 0 {
		return
	}
	// Кнопка на панели задач появляется по стилю в момент показа: окно
	// прячется, меняет стиль и показывается снова.
	procShowWindow.Call(hwnd, swHide)
	ex, _, _ := procGetWindowLongPtrW.Call(hwnd, uintptr(gwlExStyle))
	procSetWindowLongPtrW.Call(hwnd, uintptr(gwlExStyle), (ex&^wsExToolWindow)|wsExAppWindow)
	r := windowRect(hwnd)
	w, h := r.Right-r.Left, r.Bottom-r.Top
	// Размер задан под монитор, за которым окно числилось за краем; у
	// основного DPI может быть другим — размер пересчитывается под него
	// (dpi_windows.go).
	from := hw.dpi
	if from == 0 {
		from = windowDPI(hwnd)
	}
	w, h = rescale(w, h, from, primaryDPI())
	x, y := (metric(smCXScreen)-w)/2, (metric(smCYScreen)-h)/2
	flags := uintptr(swpNoZOrder | swpFrameChanged)
	if quiet {
		flags |= swpNoActivate
	}
	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), uintptr(w), uintptr(h), flags)
	if quiet {
		procShowWindow.Call(hwnd, swShowNoActivate)
		return
	}
	procShowWindow.Call(hwnd, swShow)
	procSetForegroundWindow.Call(hwnd)
}
