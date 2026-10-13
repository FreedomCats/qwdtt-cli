package datapath

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

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
