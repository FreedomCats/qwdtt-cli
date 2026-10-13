package datapath

import (
	"context"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// Проверка службы идёт к настоящему диспетчеру служб: на чтение состояния
// прав администратора не нужно. Службы с таким именем нет — ошибка
// называет её, а не притворяется «работает».
func TestWaitTunnelServiceMissing(t *testing.T) {
	err := waitTunnelService(context.Background(), "tt-test-нет-такой")
	if err == nil {
		t.Fatal("несуществующая служба сочтена работающей")
	}
	if !strings.Contains(err.Error(), "WireGuardTunnel$tt-test-нет-такой") {
		t.Errorf("ошибка не называет службу: %v", err)
	}
}

// Код выхода остановившейся службы: системный — с текстом Windows, свой код
// wireguard-windows — номером.
func TestServiceExitError(t *testing.T) {
	notFound := serviceExitError(windows.SERVICE_STATUS{
		CurrentState:  windows.SERVICE_STOPPED,
		Win32ExitCode: uint32(windows.ERROR_FILE_NOT_FOUND),
	})
	if !strings.Contains(notFound.Error(), syscall.Errno(windows.ERROR_FILE_NOT_FOUND).Error()) {
		t.Errorf("системный код без текста Windows: %v", notFound)
	}

	specific := serviceExitError(windows.SERVICE_STATUS{
		CurrentState:            windows.SERVICE_STOPPED,
		Win32ExitCode:           uint32(windows.ERROR_SERVICE_SPECIFIC_ERROR),
		ServiceSpecificExitCode: 7,
	})
	if !strings.Contains(specific.Error(), "кодом WireGuard 7") {
		t.Errorf("свой код wireguard-windows не назван: %v", specific)
	}
}
