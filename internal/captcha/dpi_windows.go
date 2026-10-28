//go:build windows

// dpi_windows.go — окна WebView2 в родном DPI монитора.
//
// qwdtt-cli — консольная программа без манифеста, и процесс не осведомлён о
// DPI: на мониторе с масштабом больше 100 % Windows рисует его окна в 96 DPI
// и растягивает картинкой — окно капчи выходит смазанным. Так было с первого
// окна капчи (E3); найдено в csqtt-cli, когда там делали автоклик.
//
// Окно капчи — единственное окно qwdtt-cli: меню и вывод живут в консоли, а
// её рисует conhost или терминал, своим процессом, и осведомлённость нашего
// процесса их не касается. Но и её менять незачем: окна живут каждое на своей
// прибитой нити, и нить можно сделать осведомлённой о DPI каждого монитора
// (Per-Monitor V2) до создания окна — окна этой нити Windows больше не
// растягивает, WebView2 рисует страницу в масштабе монитора и сам следит за
// его сменой. Размеры окна тогда — в физических пикселях: go-webview2 их не
// пересчитывает, поэтому размер задаётся уже умноженным на масштаб основного
// монитора (окно встаёт посередине него). Скрытому окну автоклика место за
// краем экрана считается после этого и вне перехватчика WH_CBT
// (hiddenwindow_windows.go), а при показе его размер пересчитывается, если
// DPI основного монитора другой.
package captcha

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procSetThreadDpiAwarenessContext = user32.NewProc("SetThreadDpiAwarenessContext")
	procMonitorFromPoint             = user32.NewProc("MonitorFromPoint")
	procGetDpiForWindow              = user32.NewProc("GetDpiForWindow")
	procGetDpiForMonitor             = windows.NewLazySystemDLL("shcore.dll").NewProc("GetDpiForMonitor")
)

const (
	// dpiAwarenessPerMonitorV2 — DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4).
	dpiAwarenessPerMonitorV2 = ^uintptr(3)
	monitorDefaultToPrimary  = 1
	monitorDefaultToNearest  = 2
	mdtEffectiveDPI          = 0
)

// primaryDPI — DPI основного монитора (для нити, осведомлённой о DPI); 0 —
// не узнать.
func primaryDPI() uint32 {
	if procGetDpiForMonitor.Find() != nil {
		return 0
	}
	mon, _, _ := procMonitorFromPoint.Call(0, monitorDefaultToPrimary)
	var dx, dy uint32
	if hr, _, _ := procGetDpiForMonitor.Call(mon, mdtEffectiveDPI, uintptr(unsafe.Pointer(&dx)),
		uintptr(unsafe.Pointer(&dy))); hr != 0 {
		return 0
	}
	return dx
}

// monitorDPIAt — DPI монитора, ближайшего к точке (x, y) в физических
// пикселях; 0 — не узнать. Переменной: тесты подменяют мониторы.
var monitorDPIAt = func(x, y int32) uint32 {
	if procGetDpiForMonitor.Find() != nil {
		return 0
	}
	// POINT передаётся по значению: x — младшие 32 бита, y — старшие.
	pt := uintptr(uint32(x)) | uintptr(uint32(y))<<32
	mon, _, _ := procMonitorFromPoint.Call(pt, monitorDefaultToNearest)
	var dx, dy uint32
	if hr, _, _ := procGetDpiForMonitor.Call(mon, mdtEffectiveDPI, uintptr(unsafe.Pointer(&dx)),
		uintptr(unsafe.Pointer(&dy))); hr != 0 {
		return 0
	}
	return dx
}

// scaleOf — масштаб по DPI (1 — 100 %); DPI не узнать — fallback.
func scaleOf(dpi uint32, fallback float64) float64 {
	if dpi == 0 {
		return fallback
	}
	return float64(dpi) / 96
}

// rescale — размер окна при переходе из DPI from в DPI to (показ скрытого
// окна на основном мониторе); 0 — не узнать, размер прежний.
func rescale(w, h int32, from, to uint32) (int32, int32) {
	if from == 0 || to == 0 || from == to {
		return w, h
	}
	return int32((int64(w)*int64(to) + int64(from)/2) / int64(from)),
		int32((int64(h)*int64(to) + int64(from)/2) / int64(from))
}

// windowDPI — DPI, в котором сейчас окно; 0 — не узнать.
func windowDPI(hwnd uintptr) uint32 {
	if procGetDpiForWindow.Find() != nil {
		return 0
	}
	d, _, _ := procGetDpiForWindow.Call(hwnd)
	return uint32(d)
}

// dpiAwareThread делает текущую нить осведомлённой о DPI каждого монитора и
// возвращает масштаб основного монитора (1 — 100 %). Звать на нити окна, до
// его создания; нить окна не отпускается, поэтому и вернуть прежнее незачем.
// Нет нужных функций (Windows старше 10 1703) — нить как была, масштаб 1:
// окно просто останется таким, как раньше.
func dpiAwareThread() float64 {
	if procSetThreadDpiAwarenessContext.Find() != nil || procGetDpiForMonitor.Find() != nil {
		return 1
	}
	if old, _, _ := procSetThreadDpiAwarenessContext.Call(dpiAwarenessPerMonitorV2); old == 0 {
		return 1
	}
	return scaleOf(primaryDPI(), 1)
}

// scaledSize — размер окна в физических пикселях: px в пикселях 100 %.
func scaledSize(px uint, scale float64) uint { return uint(float64(px)*scale + 0.5) }
