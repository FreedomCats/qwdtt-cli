package cli

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	// getConsoleProcessList в golang.org/x/sys/windows нет — берётся из kernel32.
	getConsoleProcessList = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleProcessList")

	// Оконной станции процесса в golang.org/x/sys/windows тоже нет — из user32.
	user32                    = windows.NewLazySystemDLL("user32.dll")
	getProcessWindowStation   = user32.NewProc("GetProcessWindowStation")
	getUserObjectInformationW = user32.NewProc("GetUserObjectInformationW")
)

const (
	uoiFlags   = 1 // UOI_FLAGS
	wsfVisible = 1 // WSF_VISIBLE
)

// ownsConsole — tt единственный процесс своей консоли, и её окно может
// увидеть человек: окно открыто ради tt и закроется вместе с ним. См. pause.go.
func ownsConsole() bool {
	// Двух мест хватает: важно только, один процесс или больше. Ноль —
	// консоли нет вовсе или вызов не удался; тогда и ждать некого.
	var pids [2]uint32
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	return n == 1 && visibleStation()
}

// visibleStation — оконная станция процесса интерактивная (WSF_VISIBLE): на
// ней есть рабочий стол, который видит человек. Службы и задания планировщика
// «для всех пользователей» живут в неинтерактивной станции — там окна консоли
// никто не увидит. Видимость самого окна консоли не годится: в Windows Terminal
// оно формально скрыто и при обычном двойном щелчке. Вызов не удался — считаем
// станцию интерактивной: так было до этой проверки.
func visibleStation() bool {
	ws, _, _ := getProcessWindowStation.Call()
	if ws == 0 {
		return true
	}
	var flags struct {
		inherit  int32
		reserved int32
		flags    uint32
	}
	var needed uint32
	ok, _, _ := getUserObjectInformationW.Call(ws, uoiFlags,
		uintptr(unsafe.Pointer(&flags)), unsafe.Sizeof(flags), uintptr(unsafe.Pointer(&needed)))
	if ok == 0 {
		return true
	}
	return flags.flags&wsfVisible != 0
}
