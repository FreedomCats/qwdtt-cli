// connect.go — команда tt connect.
//
// Флаги и разбор аргументов — задача G1. Сборка core.Config из профиля
// и флагов, выбор datapath, запуск супервизора и показ хода подключения —
// задача G2.
package cli

import (
	"context"
	"flag"
)

func connectCommand() *command {
	return &command{
		name:    "connect",
		args:    "[флаги] [профиль]",
		summary: "поднять туннель и отдавать трафик через SOCKS5 или WireGuard",
		description: `Настройки берутся из профиля в profiles.toml. Имя профиля можно не называть,
если в файле он один или назван в default_profile. Флаг перекрывает то же поле
профиля; всё, что флагом не названо, остаётся как в профиле.

Пароль подключения и хеши звонков VK в командную строку не передаются: они
лежат в хранилище секретов, а профиль держит только ссылки на них. Записывает
их tt profile set.`,
		run: runConnect,
	}
}

// connectOptions — значения флагов connect.
//
// Поле named говорит, какие флаги действительно назвали в командной строке:
// без него «-workers 0» не отличить от пропущенного флага, а перекрывать
// профиль нулём, которого не просили, нельзя.
type connectOptions struct {
	common *commonOptions

	peer          string
	workers       int
	obfs          string
	turnTransport string
	dtls          bool
	dns           string
	datapath      string
	socksListen   string
	listen        string

	named map[string]bool
}

// addConnectFlags объявляет флаги connect. Их набор повторяет поля профиля
// (architecture.md §10): что можно записать в файл, то можно назвать и разово
// в командной строке. Умолчания в подсказках не пишутся — умолчание берётся
// из профиля, а не из флага, и напечатать здесь можно было бы только неправду.
func addConnectFlags(fs *flag.FlagSet) *connectOptions {
	o := &connectOptions{common: addCommonFlags(fs)}

	fs.StringVar(&o.peer, "peer", "", "адрес сервера в виде host:port")
	fs.IntVar(&o.workers, "workers", 0, "число воркеров: кратно девяти, потолок 108")
	fs.StringVar(&o.obfs, "obfs", "", "режим RTP-маскировки: audio или video")
	fs.StringVar(&o.turnTransport, "turn-transport", "", "транспорт до TURN-relay: udp или tcp")
	fs.BoolVar(&o.dtls, "dtls", true, "поднимать DTLS поверх TURN; -dtls=false — прямой RTP-obfs AEAD")
	fs.StringVar(&o.dns, "dns", "", "резолвер для доменов VK: yandex, cloudflare, google, doh-*, custom:IP, doh:URL")
	fs.StringVar(&o.datapath, "datapath", "", "как отдавать трафик: socks или external")
	fs.StringVar(&o.socksListen, "socks-listen", "", "адрес локального SOCKS5 при datapath=socks")
	fs.StringVar(&o.listen, "listen", "", "локальный UDP-адрес, куда datapath отдаёт трафик")

	return o
}

func runConnect(_ context.Context, e *env, args []string) error {
	fs := newFlagSet(connectCommand())
	opts := addConnectFlags(fs)
	if err := parse(e, fs, args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return usagef("лишний аргумент %q: профиль называется один", fs.Arg(1))
	}
	opts.named = setFlags(fs)

	return todo("G2", "подключение")
}
