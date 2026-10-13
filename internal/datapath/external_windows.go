package datapath

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// externalUnsupported — на Windows external работает (см. external_other.go).
var externalUnsupported error

// locateWireGuard ищет wireguard.exe там, куда его кладёт установщик:
// сначала реестр (HKLM\SOFTWARE\WireGuard, если установщик его оставил),
// затем Program Files, затем PATH. Первый найденный файл побеждает.
func locateWireGuard() (string, error) {
	var candidates []string

	if key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\WireGuard`, registry.QUERY_VALUE); err == nil {
		for _, name := range []string{"", "InstallDir", "Path"} {
			if dir, _, err := key.GetStringValue(name); err == nil && dir != "" {
				candidates = append(candidates, filepath.Join(dir, "wireguard.exe"))
			}
		}
		key.Close()
	}

	for _, env := range []string{"ProgramFiles", "ProgramW6432"} {
		if base := os.Getenv(env); base != "" {
			candidates = append(candidates, filepath.Join(base, "WireGuard", "wireguard.exe"))
		}
	}

	for _, path := range candidates {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path, nil
		}
	}
	if path, err := exec.LookPath("wireguard.exe"); err == nil {
		return path, nil
	}
	return "", errors.New("wireguard.exe не найден: ни в реестре, ни в Program Files\\WireGuard, ни в PATH; " +
		"установите WireGuard с wireguard.com")
}

// serviceStartTimeout — сколько ждать, пока служба туннеля дойдёт до
// «работает». Штатно это одна-три секунды: служба читает конфиг, создаёт
// адаптер и поднимает маршруты.
const serviceStartTimeout = 20 * time.Second

// waitTunnelService ждёт, пока служба WireGuardTunnel$<имя> перейдёт в
// состояние «работает». Остановилась — ошибка с кодом выхода службы.
//
// Нужно потому, что wireguard.exe /installtunnelservice возвращается сразу
// после запуска службы и её судьбы не ждёт: служба, упавшая на чтении
// конфига, выглядела бы успешной установкой (так и было на живой проверке
// H2). Права нужны только на чтение состояния.
func waitTunnelService(ctx context.Context, name string) error {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return fmt.Errorf("диспетчер служб: %w", err)
	}
	defer windows.CloseServiceHandle(scm)

	serviceName, err := windows.UTF16PtrFromString("WireGuardTunnel$" + name)
	if err != nil {
		return err
	}
	svc, err := windows.OpenService(scm, serviceName, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return fmt.Errorf("служба WireGuardTunnel$%s: %w", name, err)
	}
	defer windows.CloseServiceHandle(svc)

	ctx, cancel := context.WithTimeout(ctx, serviceStartTimeout)
	defer cancel()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		var st windows.SERVICE_STATUS
		if err := windows.QueryServiceStatus(svc, &st); err != nil {
			return fmt.Errorf("состояние службы: %w", err)
		}
		switch st.CurrentState {
		case windows.SERVICE_RUNNING:
			return nil
		case windows.SERVICE_STOPPED:
			return serviceExitError(st)
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("служба не дошла до состояния «работает» за %s", serviceStartTimeout)
			}
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// serviceExitError переводит код выхода остановившейся службы в ошибку.
// Обычный код — системная ошибка с текстом от Windows («Не удается найти
// указанный файл»); собственный код wireguard-windows — только номер,
// расшифровка у него в журнале.
func serviceExitError(st windows.SERVICE_STATUS) error {
	if syscall.Errno(st.Win32ExitCode) == windows.ERROR_SERVICE_SPECIFIC_ERROR {
		return fmt.Errorf("служба остановилась с кодом WireGuard %d", st.ServiceSpecificExitCode)
	}
	if st.Win32ExitCode != 0 {
		return fmt.Errorf("служба остановилась: %w", syscall.Errno(st.Win32ExitCode))
	}
	return errors.New("служба остановилась без кода ошибки")
}
