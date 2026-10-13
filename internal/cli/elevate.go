// elevate.go — перезапуск tt с правами администратора из меню (plan-e5.md, I4).
//
// Режиму «весь трафик» (tun) нужны права администратора, а человек,
// запустивший tt двойным щелчком, их не давал. Меню не отправляет его
// перечитывать инструкцию, а запрашивает права само: на Windows — окном UAC,
// на Linux — через sudo в том же терминале. Перезапускается не меню, а одна
// команда — tt connect <профиль>.
package cli

import (
	"errors"
	"path/filepath"
	"strings"
)

// errNoAdminAccount — права администратора этой учётной записи взять неоткуда:
// повышение шло бы от имени другого пользователя, у которого нет ни профиля,
// ни ключа к его секретам.
var errNoAdminAccount = errors.New("у этой учётной записи нет прав администратора")

// relaunchArgs — аргументы перезапуска: подключение к профилю, с тем же файлом
// настроек. Путь делается абсолютным: у повышенного процесса другой текущий
// каталог (на Windows — System32).
func relaunchArgs(name, configPath string) []string {
	args := []string{"connect"}
	if configPath = strings.TrimSpace(configPath); configPath != "" {
		if abs, err := filepath.Abs(configPath); err == nil {
			configPath = abs
		}
		args = append(args, "-config", configPath)
	}
	return append(args, name)
}

// relaunch — перезапуск с правами: подмена из env или настоящий.
func (e *env) relaunch(args []string) (detached bool, err error) {
	if e.elevate != nil {
		return e.elevate(args)
	}
	return relaunchElevated(e, args)
}
