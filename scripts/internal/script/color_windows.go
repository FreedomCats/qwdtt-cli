package script

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableColor включает в консоли Windows разбор ESC-последовательностей.
// Старая консоль без него напечатала бы их буквами — тогда без цвета.
func enableColor(w *os.File) bool {
	h := windows.Handle(w.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
