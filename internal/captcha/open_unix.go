//go:build unix

package captcha

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/FreedomCats/qwdtt-cli/internal/sudo"
)

// prepareCommand настраивает запуск программы, открывающей ссылку.
//
// Своя группа процессов (Setpgid): Ctrl+C в терминале получает вся группа
// переднего плана, и браузер, которого до капчи не было и которого запустил
// сам qwdtt-cli, закрылся бы вместе с qwdtt-cli.
//
// Под sudo — запуск от имени того, кто вызвал sudo: его uid, группы и
// окружение его сеанса (sudo.SessionEnv). Ошибка — sudo есть, а пользователя найти нельзя: тогда
// браузер не открывается вовсе (капча остаётся ручной, ссылка уже напечатана),
// а не открывается от имени root.
func prepareCommand(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	u, err := sudo.Invoker()
	if err != nil || u == nil {
		return err
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{Uid: u.Uid, Gid: u.Gid, Groups: u.Groups}
	cmd.Env = sudo.SessionEnv(os.Environ(), u)
	return nil
}
