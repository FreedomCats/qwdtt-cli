// Package sudo узнаёт, от чьего имени qwdtt-cli запущен через sudo.
//
// На Linux режиму tun нужны права root, и qwdtt-cli запускают через sudo. Но настройки
// и секреты лежат у того, кто его вызвал, а не у root, и браузер для капчи тоже
// должен открыться у него. Пакет отвечает на один вопрос — кто этот человек —
// и нужен config (каталог настроек, владелец записанных файлов) и captcha
// (браузер от его имени). См. plan-e4.md, P1.
package sudo

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
)

// User — пользователь, вызвавший sudo.
type User struct {
	Name    string
	Uid     uint32
	Gid     uint32
	Groups  []uint32 // дополнительные группы; без них — только Gid
	HomeDir string
}

// Invoker возвращает пользователя, вызвавшего sudo, или nil, если qwdtt-cli запущен
// не через sudo: без прав root, root'ом напрямую или на Windows, где sudo нет и
// Geteuid возвращает -1. Ошибка — sudo есть, но пользователя найти не удалось;
// молча подставлять вместо него root нельзя: настройки окажутся не те, а
// браузер откроется от имени root.
//
// Ответ не меняется за время жизни процесса и считается один раз.
func Invoker() (*User, error) {
	return invokerOnce()
}

var invokerOnce = sync.OnceValues(func() (*User, error) {
	return invoker(os.Geteuid(), os.Getenv, user.Lookup)
})

// invoker вынесен ради тестов: права, окружение и базу пользователей
// подставляет вызывающий.
func invoker(euid int, env func(string) string, lookup func(string) (*user.User, error)) (*User, error) {
	if euid != 0 {
		return nil, nil
	}
	name := strings.TrimSpace(env("SUDO_USER"))
	if name == "" || name == "root" {
		return nil, nil
	}

	u, err := lookup(name)
	if err != nil {
		return nil, fmt.Errorf("qwdtt-cli запущен через sudo пользователем %s, но найти его не удалось: %w; "+
			"%s", name, err, hint)
	}
	if strings.TrimSpace(u.HomeDir) == "" {
		return nil, fmt.Errorf("у пользователя %s, вызвавшего sudo, не задан домашний каталог; %s", name, hint)
	}

	uid, err := parseID(u.Uid)
	if err != nil {
		return nil, fmt.Errorf("пользователь %s: uid: %w", name, err)
	}
	gid, err := parseID(u.Gid)
	if err != nil {
		return nil, fmt.Errorf("пользователь %s: gid: %w", name, err)
	}

	// Дополнительные группы нужны браузеру (видео, звук), но их отсутствие —
	// не повод отказываться: хватит основной.
	groups := []uint32{gid}
	if ids, err := u.GroupIds(); err == nil {
		groups = groups[:0]
		for _, id := range ids {
			if g, err := parseID(id); err == nil {
				groups = append(groups, g)
			}
		}
		if len(groups) == 0 {
			groups = append(groups, gid)
		}
	}

	return &User{Name: name, Uid: uid, Gid: gid, Groups: groups, HomeDir: u.HomeDir}, nil
}

// hint — как обойтись без поиска пользователя: профили читаются по -config
// и под sudo, а записывать их sudo не нужен вовсе.
const hint = "укажите файл настроек флагом -config, а профили меняйте без sudo"

func parseID(s string) (uint32, error) {
	id, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0, errors.New("не число: " + s)
	}
	return uint32(id), nil
}
