package cli

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// getConsoleProcessList в golang.org/x/sys/windows нет — берётся из kernel32.
var getConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

// ownsConsole — tt единственный процесс своей консоли: окно открыто ради него
// и закроется вместе с ним. См. pause.go.
func ownsConsole() bool {
	// Двух мест хватает: важно только, один процесс или больше. Ноль —
	// консоли нет вовсе или вызов не удался; тогда и ждать некого.
	var pids [2]uint32
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1
}
