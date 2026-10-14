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
// PID — Windows-PID из tasklist, а не $! из Git Bash: там msys-PID.
//
// Звать из другого вызова оболочки, чем тот, что запустил qwdtt-cli. Если qwdtt-cli и
// команда запущены одной командной строкой, они в одной группе процессов
// консоли, и Ctrl+Break достаётся и оболочке: qwdtt-cli остановится чисто, но
// оболочка умрёт вместе с ним.
//
// -pid — Windows-PID процесса, лидера группы; без флага команда его спросит.
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

	"golang.org/x/sys/windows"

	"github.com/FreedomCats/qwdtt-cli/scripts/internal/script"
)

const self = "ctrlbreak"

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procFreeConsole       = kernel32.NewProc("FreeConsole")
	procAttachConsole     = kernel32.NewProc("AttachConsole")
	procSetConsoleHandler = kernel32.NewProc("SetConsoleCtrlHandler")
)

func main() {
	pid := flag.Uint("pid", 0, "Windows-PID процесса, лидера группы")
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
	// гасит обработчик рантайма Go: с Ignore он сообщает, что сигнал обработан.
	procSetConsoleHandler.Call(0, 1)
	signal.Ignore(os.Interrupt)
	if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, pid); err != nil {
		return fmt.Errorf("GenerateConsoleCtrlEvent: ошибка %d", errno(err))
	}
	time.Sleep(500 * time.Millisecond)
	procFreeConsole.Call()
	return nil
}

// errno — номер ошибки Windows, как Marshal.GetLastWin32Error.
func errno(err error) uint32 {
	if e, ok := err.(windows.Errno); ok {
		return uint32(e)
	}
	return 0
}
