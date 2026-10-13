//go:build !windows

package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// relaunchElevated запускает tt через sudo в том же терминале и ждёт, пока
// он закончит: после остановки подключения человек возвращается в меню.
// Сигналы терминал отдаёт обоим процессам, и разбирает их запущенный tt
// (menuSignals.child).
func relaunchElevated(e *env, args []string) (detached bool, err error) {
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return false, errors.New("нужны права root, а sudo не найден: запустите tt от root")
	}
	exe, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("не найти исполняемый файл tt: %w", err)
	}

	fmt.Fprintln(e.stdout, "Нужны права root: sudo спросит пароль вашей учётной записи.")
	cmd := exec.Command(sudo, append([]string{"--", exe}, args...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = e.stdin, e.stdout, e.stderr

	var exitErr *exec.ExitError
	switch err := cmd.Run(); {
	case errors.As(err, &exitErr):
		// Что не так, запущенный tt (или sudo) уже напечатал сам.
		return false, fmt.Errorf("подключение с правами root завершилось с кодом %d", exitErr.ExitCode())
	case err != nil:
		return false, fmt.Errorf("sudo не запустился: %w", err)
	}
	return false, nil
}
