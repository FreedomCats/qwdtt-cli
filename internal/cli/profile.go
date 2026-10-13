// profile.go — команда tt profile: профили подключения и их секреты.
//
// Каркас с подкомандами и разбором аргументов — задача G1. Чтение и запись
// profiles.toml, скрытый ввод пароля и хешей, маскировка секретов в show —
// задача C3.
package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
)

func profileCommand() *command {
	return &command{
		name:    "profile",
		args:    "<подкоманда> [имя]",
		summary: "профили подключения: list, show, set, rm",
		description: `Подкоманды:
  list        перечислить профили из profiles.toml
  show <имя>  показать настройки профиля; секреты — замаскированными
  set <имя>   создать или изменить профиль; пароль и хеши читаются скрытым вводом
  rm <имя>    удалить профиль вместе с его секретами

Пароль подключения и хеши звонков VK не передаются аргументами намеренно:
аргумент попал бы в историю оболочки и в список процессов. Спрашиваются они
скрытым вводом, а в profiles.toml уходит только ссылка на хранилище секретов.`,
		run: runProfile,
	}
}

// profileSubcommands — имена подкоманд в порядке помощи. Список нужен и для
// проверки набранного, и для сообщения об ошибке: оно должно называть то,
// что бывает, а не отсылать к помощи.
var profileSubcommands = []string{"list", "show", "set", "rm"}

type profileOptions struct {
	config string
}

func addProfileFlags(fs *flag.FlagSet) *profileOptions {
	o := &profileOptions{}
	fs.StringVar(&o.config, "config", "", "путь к profiles.toml вместо файла настроек по умолчанию")
	return o
}

func runProfile(_ context.Context, e *env, args []string) error {
	fs := newFlagSet(profileCommand())
	opts := addProfileFlags(fs)
	if err := parse(e, fs, args); err != nil {
		return err
	}
	_ = opts

	if fs.NArg() == 0 {
		return usagef("не названа подкоманда; бывают: %s", strings.Join(profileSubcommands, ", "))
	}

	sub := fs.Arg(0)
	if !knownProfileSubcommand(sub) {
		return usagef("неизвестная подкоманда %q; бывают: %s", sub, strings.Join(profileSubcommands, ", "))
	}

	// Имя профиля обязательно всем подкомандам, кроме list.
	rest := fs.Args()[1:]
	if sub == "list" {
		if len(rest) > 0 {
			return usagef("лишний аргумент %q: list аргументов не принимает", rest[0])
		}
	} else {
		if len(rest) == 0 {
			return usagef("не названо имя профиля: tt profile %s <имя>", sub)
		}
		if len(rest) > 1 {
			return usagef("лишний аргумент %q: профиль называется один", rest[1])
		}
	}

	return todo("C3", fmt.Sprintf("подкоманда profile %s", sub))
}

func knownProfileSubcommand(name string) bool {
	for _, sub := range profileSubcommands {
		if sub == name {
			return true
		}
	}
	return false
}
