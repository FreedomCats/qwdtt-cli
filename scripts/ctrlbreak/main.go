//go:build windows

// Команда ctrlbreak посылает Ctrl+Break консольному процессу по его PID —
// остановить qwdtt-cli, запущенный в фоне, так же, как его остановил бы Ctrl+C.
//
//	tasklist /FI "IMAGENAME eq qwdtt-cli.exe"
//	go run ./scripts/ctrlbreak -pid 31180
//
// Нужна для живых проверок из Git Bash и других оболочек, где qwdtt-cli запущен
// в фоне. `kill -INT` из Git Bash до Windows-процесса не долетает, а фоновый
// процесс живёт в своей группе, где Ctrl+C отключён. Ctrl+Break в группу
// по PID доходит, и Go переводит его в тот же SIGINT, что и Ctrl+C: qwdtt-cli
// останавливает туннель и убирает за собой.
//
// Команда отключается от своей консоли, подключается к консоли процесса,
// снимает с себя обработку сигнала и генерирует CTRL_BREAK_EVENT.
//
// Процесс, запущенный через Start-Process в PowerShell, сидит в своей
// консоли, но лидером группы не является — Ctrl+Break по PID туда не
// посылается. Тогда команда шлёт Ctrl+C всей его консоли, если в ней нет
// никого, кроме него; иначе — ошибка, и ничего не посылается.
//
// PID — Windows-PID из tasklist, а не $! из Git Bash: там msys-PID.
//
// Звать из другого вызова оболочки, чем тот, что запустил qwdtt-cli. Если qwdtt-cli и
// команда запущены одной командной строкой, они в одной группе процессов
// консоли, и Ctrl+Break достаётся и оболочке: qwdtt-cli остановится чисто, но
// оболочка умрёт вместе с ним.
//
// Проверено так: фон Git Bash (`qwdtt-cli … &`), Start-Process со своим окном
// (в том числе -WindowStyle Hidden) и Start-Process -NoNewWindow. Не годится
// для процесса, запущенного внутри `cmd /c` в общей консоли: он не лидер
// группы, и Ctrl+Break по его PID система рассылает всей консоли — вместе
// с оболочкой.
//
// -pid — Windows-PID процесса; без флага команда его спросит.
//
// Перенесена с PowerShell (scripts/send-ctrl-break.ps1).
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/FreedomCats/qwdtt-cli/scripts/internal/script"
)

const self = "ctrlbreak"

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procFreeConsole       = kernel32.NewProc("FreeConsole")
	procAttachConsole     = kernel32.NewProc("AttachConsole")
	procSetConsoleHandler = kernel32.NewProc("SetConsoleCtrlHandler")
	procGetConsoleProcs   = kernel32.NewProc("GetConsoleProcessList")
)

func main() {
	pid := flag.Uint("pid", 0, "Windows-PID процесса")
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}
	if *pid == 0 {
		// Обязательный параметр без значения PowerShell спрашивал.
		fmt.Fprint(os.Stdout, "ProcessId: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		v, err := strconv.ParseUint(strings.TrimSpace(line), 10, 32)
		if err != nil || v == 0 {
			script.Fail(self, fmt.Errorf("PID: %q", strings.TrimSpace(line)))
		}
		*pid = uint(v)
	}
	if err := run(uint32(*pid)); err != nil {
		script.Fail(self, err)
	}
}

func run(pid uint32) error {
	procFreeConsole.Call()
	if r, _, err := procAttachConsole.Call(uintptr(pid)); r == 0 {
		return fmt.Errorf("AttachConsole(%d): ошибка %d", pid, errno(err))
	}
	// Без этого сигнал, посланный в группу, достался бы и самой команде.
	// SetConsoleCtrlHandler(NULL) снимает с процесса только Ctrl+C, Ctrl+Break
	// гасит обработчик рантайма Go — но лишь когда сигнал кому-то нужен: с
	// signal.Ignore он отвечает «не обработан», и обработчик по умолчанию
	// завершает команду с STATUS_CONTROL_C_EXIT (в Git Bash — код 130).
	procSetConsoleHandler.Call(0, 1)
	signal.Notify(make(chan os.Signal, 1), os.Interrupt)
	err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, pid)
	if err == windows.ERROR_INVALID_PARAMETER {
		// Процесс не лидер группы: так бывает, если его запустили в своей
		// консоли без CREATE_NEW_PROCESS_GROUP (Start-Process в PowerShell).
		// Тогда сигнал — всей консоли (группа 0), но только если в ней нет
		// никого, кроме него и самой команды. И это Ctrl+C, а не Ctrl+Break:
		// его команда пропускает мимо себя на уровне системы, а Ctrl+Break
		// во всю консоль завершал её даже с signal.Notify. Ctrl+C у такого
		// процесса не отключён — группа своя не заводилась.
		if err = consoleHasOnly(pid); err == nil {
			err = windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, 0)
		}
	}
	if err != nil {
		if e, ok := err.(windows.Errno); ok {
			return fmt.Errorf("GenerateConsoleCtrlEvent: ошибка %d", uint32(e))
		}
		return err
	}
	time.Sleep(500 * time.Millisecond)
	procFreeConsole.Call()
	return nil
}

// consoleHasOnly проверяет, что к консоли, к которой подключена команда,
// подключены только процесс pid и она сама.
func consoleHasOnly(pid uint32) error {
	list := make([]uint32, 64)
	r, _, err := procGetConsoleProcs.Call(uintptr(unsafe.Pointer(&list[0])), uintptr(len(list)))
	n := uint32(r)
	if n == 0 {
		return fmt.Errorf("GetConsoleProcessList: ошибка %d", errno(err))
	}
	if int(n) > len(list) {
		return fmt.Errorf("PID %d не лидер группы, а в его консоли %d процессов", pid, n)
	}
	self := windows.GetCurrentProcessId()
	for _, p := range list[:n] {
		if p != pid && p != self {
			return fmt.Errorf("PID %d не лидер группы, а в его консоли есть и PID %d", pid, p)
		}
	}
	return nil
}

// errno — номер ошибки Windows, как Marshal.GetLastWin32Error.
func errno(err error) uint32 {
	if e, ok := err.(windows.Errno); ok {
		return uint32(e)
	}
	return 0
}
