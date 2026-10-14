package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tokenElevationTypeLimited — TOKEN_ELEVATION_TYPE: урезанный токен
// администратора при включённом UAC. Только у такого есть полный двойник,
// и окно UAC поднимет того же пользователя.
const tokenElevationTypeLimited = 3

// relaunchElevated запускает qwdtt-cli с правами администратора в новом окне: окно
// UAC, затем отдельная консоль — повышенный процесс к чужой консоли не
// присоединится. Меню в старом окне после этого закрывается (detached).
func relaunchElevated(e *env, args []string) (detached bool, err error) {
	// Обычная учётная запись: UAC спросил бы пароль администратора, и qwdtt-cli
	// работал бы от его имени — другой %APPDATA%, а секреты DPAPI другой
	// пользователь не расшифрует.
	if elevationType() != tokenElevationTypeLimited {
		return false, errNoAdminAccount
	}

	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("не найти исполняемый файл qwdtt-cli: %w", err)
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = windows.EscapeArg(arg)
	}
	cwd, _ := os.Getwd()

	fmt.Fprintln(e.stdout, "Нужны права администратора: Windows спросит разрешение.")
	err = windows.ShellExecute(0, windows.StringToUTF16Ptr("runas"), windows.StringToUTF16Ptr(exe),
		windows.StringToUTF16Ptr(strings.Join(quoted, " ")), windows.StringToUTF16Ptr(cwd), windows.SW_SHOWNORMAL)
	if errors.Is(err, windows.ERROR_CANCELLED) {
		return false, errors.New("права администратора не выданы — подключение отменено")
	}
	if err != nil {
		return false, fmt.Errorf("запуск с правами администратора: %w", err)
	}

	fmt.Fprintln(e.stdout, "Подключение идёт в новом окне. Остановить — Ctrl+C или закрыть то окно.")
	return true, nil
}

// elevationType — TOKEN_ELEVATION_TYPE процесса; 0 — узнать не удалось.
func elevationType() uint32 {
	var t, n uint32
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenElevationType,
		(*byte)(unsafe.Pointer(&t)), uint32(unsafe.Sizeof(t)), &n)
	if err != nil {
		return 0
	}
	return t
}
