// menu_edit.go — пункты меню, которые меняют профиль: ссылки на звонки,
// пароль, адрес сервера (plan-e5.md, I3).
//
// Вопросы — те же, что в мастере (wizard.go): ошибка переспрашивается,
// конец ввода — «ничего не изменилось».
package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/FreedomCats/qwdtt-cli/internal/config"
	"github.com/FreedomCats/qwdtt-cli/internal/core"
	"github.com/FreedomCats/qwdtt-cli/internal/supervisor"
)

// updateHashes — «Обновить ссылки на звонки VK». Сначала текущие ссылки
// проверяются: какой звонок закрыт, человек сам не узнает. Закрытые
// убираются, остальные остаются — в том числе те, что проверить не удалось:
// выбросить годную ссылку из-за сбоя сети хуже, чем оставить мёртвую.
func (m *menu) updateHashes(ctx context.Context, name string) error {
	path, file, err := m.load()
	if err != nil {
		return err
	}
	p, err := file.Profile(name)
	if err != nil {
		return err
	}

	current, err := secretHashes(p, config.NewSecretStoreFor(path))
	if err != nil {
		fmt.Fprintf(m.e.stderr, "Текущие ссылки не прочитать: %v\n", err)
		current = nil
	}

	kept := current
	if len(current) > 0 {
		kept = m.checkHashes(ctx, p, current)
	}

	w := m.e.stdout
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  1. Добавить новые ссылки (закрытые звонки уберутся)")
	fmt.Fprintln(w, "  2. Заменить все ссылки новыми")
	fmt.Fprintln(w, "  0. Назад")
	choice := 0
	for {
		line, err := m.ask("Что сделать? [1]: ")
		if errors.Is(err, errNoInput) {
			return nil
		}
		if err != nil {
			return err
		}
		n, ok := menuNumber(line, 2, 1)
		if ok {
			choice = n
			break
		}
		fmt.Fprintf(m.e.stderr, "Нет такого пункта: %q. Введите 0, 1 или 2.\n", line)
	}
	if choice == 0 {
		fmt.Fprintln(w, "Ссылки не изменены.")
		return nil
	}
	if choice == 2 {
		kept = nil
	}

	// Добавлять можно и ничего: тогда только уберутся закрытые звонки.
	added, err := m.askCallLinks(len(kept) > 0)
	if errors.Is(err, errWizardCancelled) {
		fmt.Fprintln(w, "Ссылки не изменены.")
		return nil
	}
	if err != nil {
		return err
	}

	hashes := core.ParseHashes(strings.Join(kept, " ") + " " + added)
	if sameHashes(hashes, current) {
		fmt.Fprintln(w, "Ссылки не изменились.")
		return nil
	}
	if err := storeProfile(m.e, path, file, p, true, profileSecrets{hashes: strings.Join(hashes, " ")}); err != nil {
		return err
	}
	fmt.Fprintf(w, "Ссылок на звонки теперь: %d\n", len(hashes))
	return nil
}

// checkHashes проверяет хеши и печатает исход каждого словами. Возвращает
// те, что стоит оставить: все, кроме закрытых звонков. Ctrl+C прерывает
// проверку, а не меню; непроверенные остаются.
func (m *menu) checkHashes(ctx context.Context, p *config.Profile, hashes []string) []string {
	w := m.e.stdout
	fmt.Fprintf(w, "Проверяю ссылки на звонки (%d). Прервать — Ctrl+C.\n", len(hashes))

	ctx, done := m.connecting(ctx)
	defer done()

	cfg := p.Core()
	cfg.Hashes = hashes
	cfg.Normalize()

	var mu sync.Mutex
	dead := map[string]bool{}
	sink := core.SinkFunc(func(ev core.Event) {
		hc, ok := ev.(core.EventHashCheck)
		if !ok || hc.Status == core.HashCheckRunning {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if hc.Status == core.HashCheckDead {
			dead[hc.Hash] = true
		}
		fmt.Fprintf(w, "  %d. %-9s  %s\n", hc.Index, supervisor.ShortHash(hc.Hash), hashStatusText(hc.Status))
	})

	check := m.e.hashCheck
	if check == nil {
		check = core.RunHashCheck
	}
	err := check(ctx, cfg, sink)
	switch {
	case err != nil:
		fmt.Fprintf(m.e.stderr, "Проверить ссылки не удалось: %v\n", err)
	case ctx.Err() != nil:
		fmt.Fprintln(m.e.stderr, "Проверка прервана.")
	}

	mu.Lock()
	defer mu.Unlock()
	var kept []string
	for _, h := range hashes {
		if !dead[h] {
			kept = append(kept, h)
		}
	}
	switch n := len(hashes) - len(kept); {
	case n == 0:
		fmt.Fprintln(w, "Закрытых звонков нет.")
	case len(kept) == 0:
		fmt.Fprintln(w, "Все звонки закрыты — нужны новые ссылки.")
	default:
		fmt.Fprintf(w, "Закрытых звонков: %d из %d.\n", n, len(hashes))
	}
	return kept
}

// hashStatusText — исход проверки хеша словами, без терминов ядра.
func hashStatusText(s core.HashCheckStatus) string {
	switch s {
	case core.HashCheckOK:
		return "работает"
	case core.HashCheckCaptcha:
		return "работает; при подключении VK попросит капчу"
	case core.HashCheckDead:
		return "звонок закрыт или не найден — ссылка уберётся"
	case core.HashCheckLimited:
		return "VK ограничил запросы, проверить не вышло"
	case core.HashCheckNetwork:
		return "нет связи с VK, проверить не вышло"
	default:
		return "проверить не вышло"
	}
}

func sameHashes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *menu) replacePasswordOrPeer(ctx context.Context, name string) error {
	fmt.Fprintln(m.e.stdout, "  1. Пароль подключения")
	fmt.Fprintln(m.e.stdout, "  2. Адрес сервера")
	fmt.Fprintln(m.e.stdout, "  0. Назад")

	for {
		line, err := m.ask("Что заменить? [0]: ")
		if errors.Is(err, errNoInput) {
			return nil
		}
		if err != nil {
			return err
		}
		n, ok := menuNumber(line, 2, 0)
		switch {
		case !ok:
			fmt.Fprintf(m.e.stderr, "Нет такого пункта: %q. Введите 0, 1 или 2.\n", line)
			continue
		case n == 1:
			return m.replacePassword(name)
		case n == 2:
			return m.replacePeer(name)
		}
		return nil
	}
}

func (m *menu) replacePassword(name string) error {
	path, file, err := m.load()
	if err != nil {
		return err
	}
	p, err := file.Profile(name)
	if err != nil {
		return err
	}

	password, err := m.askPassword()
	if errors.Is(err, errWizardCancelled) {
		fmt.Fprintln(m.e.stdout, "Пароль не изменён.")
		return nil
	}
	if err != nil {
		return err
	}
	return storeProfile(m.e, path, file, p, true, profileSecrets{password: password})
}

func (m *menu) replacePeer(name string) error {
	path, file, err := m.load()
	if err != nil {
		return err
	}
	p, err := file.Profile(name)
	if err != nil {
		return err
	}

	fmt.Fprintf(m.e.stdout, "Сейчас: %s\n", p.Peer)
	for {
		line, err := m.wizardAsk("Новый адрес сервера [не менять]: ")
		if errors.Is(err, errWizardCancelled) || (err == nil && line == "") {
			fmt.Fprintln(m.e.stdout, "Адрес не изменён.")
			return nil
		}
		if err != nil {
			return err
		}
		peer := config.PeerWithPort(line)
		if err := config.CheckPeer(peer); err != nil {
			fmt.Fprintf(m.e.stderr, "Не похоже на адрес: %v\n", err)
			continue
		}
		p.Peer = peer
		return storeProfile(m.e, path, file, p, true, profileSecrets{})
	}
}
