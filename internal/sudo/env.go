package sudo

import (
	"os"
	"path"
	"strconv"
	"strings"
)

// SessionEnv — окружение сеанса пользователя u из окружения sudo: для
// программ, которые qwdtt-cli запускает от его имени (браузер для капчи,
// связка ключей).
//
// sudo оставляет DISPLAY и XAUTHORITY, но HOME, USER и LOGNAME — уже root'а, а
// XDG_RUNTIME_DIR и DBUS_SESSION_BUS_ADDRESS не доживают вовсе. Без них
// xdg-open не найдёт ни настроек пользователя, ни его шины D-Bus, через
// которую браузер открывает вкладку в уже запущенном окне, а связки ключей
// без шины нет. Каталог сеанса — /run/user/<uid> (systemd-logind); если его
// нет, остаётся что было. Переменные SUDO_* там ни к чему — убираются.
func SessionEnv(environ []string, u *User) []string {
	return sessionEnv(environ, u, exists)
}

func sessionEnv(environ []string, u *User, exists func(string) bool) []string {
	set := map[string]string{
		"HOME":    u.HomeDir,
		"USER":    u.Name,
		"LOGNAME": u.Name,
	}
	// Пути — Linux-овые и на любой системе: filepath на Windows дал бы
	// \run\user\….
	runtimeDir := path.Join("/run/user", strconv.FormatUint(uint64(u.Uid), 10))
	if exists(runtimeDir) {
		set["XDG_RUNTIME_DIR"] = runtimeDir
		if bus := path.Join(runtimeDir, "bus"); exists(bus) {
			set["DBUS_SESSION_BUS_ADDRESS"] = "unix:path=" + bus
		}
	}

	env := make([]string, 0, len(environ)+len(set))
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if _, replaced := set[key]; replaced || strings.HasPrefix(key, "SUDO_") {
			continue
		}
		env = append(env, kv)
	}
	for _, key := range []string{"HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if value, ok := set[key]; ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func exists(name string) bool {
	_, err := os.Stat(name)
	return err == nil
}
