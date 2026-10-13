// diag.go — команды tt ping и tt hashes: две проверки, которые делаются
// до того, как поднимать туннель.
//
// Флаги и разбор аргументов — задача G1. Вызов core.RunPing и core.RunHashCheck
// и печать их событий — задача G3.
package cli

import (
	"context"
	"flag"
)

// --- ping ---

func pingCommand() *command {
	return &command{
		name:    "ping",
		args:    "[флаги] [профиль]",
		summary: "замерить задержку до сервера через TURN",
		description: `Замер идёт по тому же пути, что и рабочий трафик: креды TURN у VK, relay,
сервер. Хеши перебираются по очереди, годится первый, по которому замер удался.
Туннель при этом не поднимается.

Код возврата 1 означает, что замер не удался ни по одному хешу.`,
		run: runPing,
	}
}

type pingOptions struct {
	common *commonOptions

	peer string
	dns  string
}

func addPingFlags(fs *flag.FlagSet) *pingOptions {
	o := &pingOptions{common: addCommonFlags(fs)}
	fs.StringVar(&o.peer, "peer", "", "адрес сервера в виде host:port")
	fs.StringVar(&o.dns, "dns", "", "резолвер для доменов VK: yandex, cloudflare, google, doh-*, custom:IP, doh:URL")
	return o
}

func runPing(_ context.Context, e *env, args []string) error {
	fs := newFlagSet(pingCommand())
	opts := addPingFlags(fs)
	rest, err := parseWithArgs(e, fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("лишний аргумент %q: профиль называется один", rest[1])
	}
	_ = opts

	return todo("G3", "замер задержки")
}

// --- hashes ---

func hashesCommand() *command {
	return &command{
		name:    "hashes",
		args:    "[флаги] [профиль]",
		summary: "проверить хеши звонков VK по одному",
		description: `Каждый хеш из профиля проверяется отдельно; таблица показывает, что с ним
не так: ok, captcha, dead, limited, network, error. Ни сервер, ни пароль
подключения для этого не нужны — проверка идёт только к VK.`,
		run: runHashes,
	}
}

type hashesOptions struct {
	common *commonOptions

	dns string
}

func addHashesFlags(fs *flag.FlagSet) *hashesOptions {
	o := &hashesOptions{common: addCommonFlags(fs)}
	fs.StringVar(&o.dns, "dns", "", "резолвер для доменов VK: yandex, cloudflare, google, doh-*, custom:IP, doh:URL")
	return o
}

func runHashes(_ context.Context, e *env, args []string) error {
	fs := newFlagSet(hashesCommand())
	opts := addHashesFlags(fs)
	rest, err := parseWithArgs(e, fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("лишний аргумент %q: профиль называется один", rest[1])
	}
	_ = opts

	return todo("G3", "проверка хешей")
}
