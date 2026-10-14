// Package script — общее для служебных команд в scripts/: корень репозитория,
// вывод с цветом и порядок имён файлов.
//
// Команды переведены с PowerShell и печатают то же, что печатали скрипты:
// Write-Host с цветом — строкой с цветом, если вывод идёт в терминал.
package script

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

// Root — корень модуля qwdtt-cli: каталог go.mod, который видит go из
// текущего каталога. Команды запускаются как `go run ./scripts/<имя>` из
// корня, так что это он и есть.
func Root() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", errors.New("запускать из модуля qwdtt-cli")
	}
	return filepath.Dir(gomod), nil
}

// Цвета Write-Host -ForegroundColor.
const (
	Red    = "31"
	Green  = "32"
	Yellow = "33"
)

// Say печатает строку в stdout, как Write-Host.
func Say(a ...any) { fmt.Fprintln(os.Stdout, a...) }

// Sayf — Say с форматом.
func Sayf(format string, a ...any) { fmt.Fprintf(os.Stdout, format+"\n", a...) }

// Color печатает строку в stdout цветом color — только если это терминал.
func Color(color, line string) { colorLine(os.Stdout, color, line) }

// Fail печатает ошибку красным в stderr и завершает команду с кодом 1 — так
// кончался скрипт на throw.
func Fail(name string, err error) {
	colorLine(os.Stderr, Red, name+": "+err.Error())
	os.Exit(1)
}

func colorLine(w *os.File, color, line string) {
	if term.IsTerminal(int(w.Fd())) && enableColor(w) {
		fmt.Fprintf(w, "\x1b[%sm%s\x1b[0m\n", color, line)
		return
	}
	fmt.Fprintln(w, line)
}
