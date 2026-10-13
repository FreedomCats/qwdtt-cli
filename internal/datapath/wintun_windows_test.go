package datapath

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// lockHelperEnv включает режим помощника: тестовый бинарь, запущенный самим
// тестом, пробует занять мьютекс как «второй tt» и сообщает итог кодом выхода.
const lockHelperEnv = "TT_WINTUN_LOCK_HELPER"

func init() {
	testHelpers = append(testHelpers, func() (int, bool) {
		if os.Getenv(lockHelperEnv) != "1" {
			return 0, false
		}
		release, err := acquireWintunLock()
		if err != nil {
			os.Stderr.WriteString(err.Error())
			return 3, true
		}
		release()
		return 0, true
	})
}

// tryLockFromOtherProcess — занять мьютекс из отдельного процесса: nil, если
// удалось, иначе текст отказа.
func tryLockFromOtherProcess(t *testing.T) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), lockHelperEnv+"=1")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 3 {
		return errors.New(string(out))
	}
	if err != nil {
		t.Fatalf("помощник: %v\n%s", err, out)
	}
	return nil
}

// Второй tt с datapath wintun получает отказ, пока жив первый, и проходит,
// когда первый отпустил мьютекс. Внутри одного процесса захваты считаются:
// резерв CLI и Start попытки друг другу не мешают.
func TestWintunLockExcludesOtherProcess(t *testing.T) {
	session, err := acquireWintunLock()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Skip("мьютекс в пространстве Global без прав не создать:", err)
	}
	if err != nil {
		t.Fatalf("захват на сеанс: %v", err)
	}
	attempt, err := acquireWintunLock()
	if err != nil {
		session()
		t.Fatalf("повторный захват в том же процессе: %v", err)
	}

	if err := tryLockFromOtherProcess(t); err == nil {
		t.Error("другой процесс занял мьютекс при живом первом")
	} else if !strings.Contains(err.Error(), "-datapath socks") {
		t.Errorf("отказ без совета про socks: %v", err)
	}

	// Попытка кончилась, сеанс держит: другому процессу по-прежнему нельзя.
	attempt()
	attempt() // повторный release — не второй декремент
	if err := tryLockFromOtherProcess(t); err == nil {
		t.Error("мьютекс отпущен вместе с попыткой, хотя сеанс его держит")
	}

	session()
	if err := tryLockFromOtherProcess(t); err != nil {
		t.Errorf("после освобождения другой процесс не смог занять мьютекс: %v", err)
	}
}
