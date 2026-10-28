package captcha

import (
	"fmt"
	"os"
	"testing"
)

// TestMain даёт прогону свой временный каталог (перенесено из csqtt-cli):
// профили окон WebView2 — в нём, и проверка уборки (profilesNow) не видит
// чужих профилей в общем %TEMP% — другого прогона или настоящего окна
// qwdtt-cli, открытого в то же время.
//
// os.TempDir на Windows берёт каталог у GetTempPath2/GetTempPath, а те —
// из переменной TMP, затем TEMP; процессы msedgewebview2.exe наследуют обе.
// Задаются обе: у какой первенство, не важно.
//
// Удаляется каталог через removeProfile: msedgewebview2.exe держит файлы
// ещё какое-то время после закрытия окна. Не удалился — остаётся в %TEMP%,
// секретов в нём нет.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "captcha-tests-")
	for _, name := range []string{"TMP", "TEMP"} {
		if err == nil {
			err = os.Setenv(name, dir)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "временный каталог тестов: %v\n", err)
		os.Exit(1)
	}
	code := m.Run()
	removeProfile(dir)
	os.Exit(code)
}
