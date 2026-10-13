//go:build unix

package captcha

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/FreedomCats/qwdtt-cli/internal/sudo"
)

// prepareCommand настраивает запуск программы, открывающей ссылку.
//
// Своя группа процессов (Setpgid): Ctrl+C в терминале получает вся группа
// переднего плана, и браузер, которого до капчи не было и которого запустил
// сам tt, закрылся бы вместе с tt.
//
// Под sudo — запуск от имени того, кто вызвал sudo: его uid, группы и
// окружение его сеанса. Ошибка — sudo есть, а пользователя найти нельзя: тогда
// браузер не открывается вовсе (капча остаётся ручной, ссылка уже напечатана),
// а не открывается от имени root.
func prepareCommand(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	u, err := sudo.Invoker()
	if err != nil || u == nil {
		return err
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: u.Uid, Gid: u.Gid, Groups: u.Groups}
	cmd.Env = invokerEnv(os.Environ(), u, exists)
	return nil
}

// invokerEnv — окружение сеанса пользователя из окружения sudo.
//
// sudo оставляет DISPLAY и XAUTHORITY, но HOME, USER и LOGNAME — уже root'а, а
// XDG_RUNTIME_DIR и DBUS_SESSION_BUS_ADDRESS не доживают вовсе. Без них
// xdg-open не найдёт ни настроек пользователя, ни его шины D-Bus, через
// которую браузер открывает вкладку в уже запущенном окне. Каталог сеанса —
// /run/user/<uid> (systemd-logind); если его нет, остаётся что было.
// Переменные SUDO_* браузеру ни к чему — убираются.
func invokerEnv(environ []string, u *sudo.User, exists func(string) bool) []string {
	set := map[string]string{
		"HOME":    u.HomeDir,
		"USER":    u.Name,
		"LOGNAME": u.Name,
	}
	runtimeDir := filepath.Join("/run/user", strconv.FormatUint(uint64(u.Uid), 10))
	if exists(runtimeDir) {
		set["XDG_RUNTIME_DIR"] = runtimeDir
		if bus := filepath.Join(runtimeDir, "bus"); exists(bus) {
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

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
