package datapath

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// Второй tt с datapath wintun получает отказ, пока жив первый, и проходит,
// когда первый отпустил мьютекс.
func TestWintunLockExcludesSecondRun(t *testing.T) {
	first, err := acquireWintunLock()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Skip("мьютекс в пространстве Global\\ без прав не создать:", err)
	}
	if err != nil {
		t.Fatalf("первый захват: %v", err)
	}

	if h, err := acquireWintunLock(); err == nil {
		windows.CloseHandle(h)
		windows.CloseHandle(first)
		t.Fatal("второй захват прошёл при живом первом")
	} else if !strings.Contains(err.Error(), "-datapath socks") {
		t.Errorf("отказ без совета про socks: %v", err)
	}

	windows.CloseHandle(first)
	again, err := acquireWintunLock()
	if err != nil {
		t.Fatalf("захват после освобождения: %v", err)
	}
	windows.CloseHandle(again)
}
