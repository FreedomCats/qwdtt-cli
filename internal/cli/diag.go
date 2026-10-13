// diag.go — команды tt ping и tt hashes: две проверки, которые делаются
// до того, как поднимать туннель.
//
// Флаги и разбор аргументов — задача G1. Здесь (задача G3) команды собраны
// целиком: профиль и флаги → core.Config, секреты из хранилища, вызов
// core.RunPing и core.RunHashCheck, печать их событий тем же принтером,
// что у connect.
//
// Как и у connect, сборка отделена от запуска: planPing и planHashes сети
// не трогают и проверяются тестами, а run* только зовёт ядро.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"sync/atomic"

	"qwdtt-cli/internal/config"
	"qwdtt-cli/internal/core"
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

Флаг перекрывает то же поле профиля на один запуск. Пароль подключения и хеши
берутся из хранилища секретов, как у connect.

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

// connectOptions переводит флаги ping в общий вид, который понимает
// overrideProfile: у ping подмножество флагов connect с теми же именами.
func (o *pingOptions) connectOptions(named map[string]bool) *connectOptions {
	return &connectOptions{common: o.common, peer: o.peer, dns: o.dns, named: named}
}

func runPing(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(pingCommand())
	opts := addPingFlags(fs)
	rest, err := parseWithArgs(e, fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("лишний аргумент %q: профиль называется один", rest[1])
	}

	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}

	plan, err := planPing(opts.connectOptions(setFlags(fs)), name)
	if err != nil {
		return err
	}
	return plan.ping(ctx, e, opts.common.verbose)
}

// diagPlan — всё, что собрано до выхода в сеть у ping и hashes: профиль
// с флагами поверх и настройки ядра. Datapath и решателя капчи здесь нет —
// туннель не поднимается.
type diagPlan struct {
	profile *config.Profile
	cfg     core.Config
}

// planPing собирает план замера. Настройки нужны те же, что для туннеля:
// замер идёт через TURN-relay до сервера, а ключ маскировки выводится
// из пароля, — поэтому проверка полная, как у connect.
func planPing(opts *connectOptions, name string) (*diagPlan, error) {
	path, p, err := profileWithFlags(opts, name)
	if err != nil {
		return nil, err
	}

	cfg, err := coreConfig(p, config.NewSecretStoreFor(path))
	if err != nil {
		return nil, err
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &diagPlan{profile: p, cfg: cfg}, nil
}

// ping делает замер и печатает итог. Неудача возвращается ошибкой: ядро
// отдаёт ту же ошибку, что кладёт в EventPing, и код возврата 1 выставляет
// общий обработчик — печатать её из события второй раз незачем.
func (plan *diagPlan) ping(ctx context.Context, e *env, verbose bool) error {
	out := newPrinter(e.stdout, verbose)
	cfg := plan.cfg

	out.line("профиль %s: сервер %s, хешей %d, DNS %s",
		plan.profile.Name, cfg.Peer, len(cfg.Hashes), cfg.DNS)
	return core.RunPing(ctx, cfg, core.SinkFunc(out.event))
}

// --- hashes ---

func hashesCommand() *command {
	return &command{
		name:    "hashes",
		args:    "[флаги] [профиль]",
		summary: "проверить хеши звонков VK по одному",
		description: `Каждый хеш из профиля проверяется отдельно; таблица показывает, что с ним
не так: ok, captcha, dead, limited, network, error. Ни сервер, ни пароль
подключения для этого не нужны — проверка идёт только к VK.

Хеши в выводе обрезаны до первых знаков: целиком они секрет.

Код возврата 1 означает, что годных хешей не нашлось ни одного.`,
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

// connectOptions — как у pingOptions: тот же перевод для overrideProfile.
func (o *hashesOptions) connectOptions(named map[string]bool) *connectOptions {
	return &connectOptions{common: o.common, dns: o.dns, named: named}
}

func runHashes(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(hashesCommand())
	opts := addHashesFlags(fs)
	rest, err := parseWithArgs(e, fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return usagef("лишний аргумент %q: профиль называется один", rest[1])
	}

	name := ""
	if len(rest) == 1 {
		name = rest[0]
	}

	plan, err := planHashes(opts.connectOptions(setFlags(fs)), name)
	if err != nil {
		return err
	}
	return plan.hashes(ctx, e, opts.common.verbose)
}

// planHashes собирает план проверки. Из секретов разворачиваются только
// хеши: пароль здесь не нужен, и профиль без него проверяется так же.
// Проверки core.Config.Validate тоже нет — она требует сервер и пароль,
// а RunHashCheck проверяет ровно то, что ему нужно.
func planHashes(opts *connectOptions, name string) (*diagPlan, error) {
	path, p, err := profileWithFlags(opts, name)
	if err != nil {
		return nil, err
	}

	cfg := p.Core()
	cfg.Hashes, err = secretHashes(p, config.NewSecretStoreFor(path))
	if err != nil {
		return nil, err
	}
	cfg.Normalize()
	return &diagPlan{profile: p, cfg: cfg}, nil
}

// hashes проверяет хеши по одному и печатает таблицу по мере проверки:
// каждая строка — событие EventHashCheck. Итог — сколько годных; ни одного
// годного — ошибка, чтобы скрипт увидел это по коду возврата.
func (plan *diagPlan) hashes(ctx context.Context, e *env, verbose bool) error {
	out := newPrinter(e.stdout, verbose)
	cfg := plan.cfg

	out.line("профиль %s: хешей %d, DNS %s", plan.profile.Name, len(cfg.Hashes), cfg.DNS)

	// Счётчик годных ведётся здесь, а не в принтере: принтер печатает, а
	// решение о коде возврата — дело команды. Atomic — на всякий случай:
	// Sink по контракту зовут из любых горутин.
	var good atomic.Int32
	sink := core.SinkFunc(func(ev core.Event) {
		out.event(ev)
		if hc, ok := ev.(core.EventHashCheck); ok && hc.Status == core.HashCheckOK {
			good.Add(1)
		}
	})
	if err := core.RunHashCheck(ctx, cfg, sink); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		// Проверку прервали (Ctrl+C): итог по неполному списку ничего
		// не значит, а ошибка контекста скажет человеку, что случилось.
		return fmt.Errorf("проверка прервана: %w", err)
	}

	n := int(good.Load())
	out.line("годных хешей %d из %d", n, len(cfg.Hashes))
	if n == 0 {
		return errors.New("ни один хеш звонка не годен; заведите новые: tt profile set " +
			plan.profile.Name + " -hashes")
	}
	return nil
}
