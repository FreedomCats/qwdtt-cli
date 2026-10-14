// wizard.go — мастер профиля: новый профиль вопросами, по одному полю за раз
// (plan-e5.md, I2).
//
// Ошибка в ответе переспрашивается, а не завершает мастер: человек, который
// ошибся в адресе сервера, не обязан заново вводить пароль. Конец ввода на
// любом вопросе — отмена, профиль не записывается.
package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/FreedomCats/qwdtt-cli/internal/config"
	"github.com/FreedomCats/qwdtt-cli/internal/core"
	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
)

// errWizardCancelled — человек закрыл ввод посреди мастера.
var errWizardCancelled = errors.New("мастер профиля отменён")

func (m *menu) create(context.Context, string) error {
	path, file, err := m.load()
	if err != nil {
		return err
	}

	err = m.wizard(path, file)
	if errors.Is(err, errWizardCancelled) {
		fmt.Fprintln(m.e.stdout, "Профиль не записан.")
		return nil
	}
	return err
}

func (m *menu) wizard(path string, file *config.File) error {
	w := m.e.stdout
	fmt.Fprintln(w, "Новый профиль. Где в вопросе есть [значение], Enter выбирает его.")
	fmt.Fprintln(w)

	link, err := m.askShareLink()
	if err != nil {
		return err
	}

	suggested := ""
	if link != nil {
		suggested = config.ProfileName(link.Name)
	}
	if suggested == "" {
		suggested = freeProfileName(file)
	}
	name, existed, err := m.askProfileName(file, suggested)
	if err != nil {
		return err
	}

	p := &config.Profile{Name: name}
	if existed {
		p = file.Profiles[name]
	}
	var secrets profileSecrets
	if link != nil {
		applyShareLink(p, link)
		secrets = profileSecrets{password: link.Password, hashes: link.Hashes}
		if link.HashCount() == 0 {
			secrets.hashes = ""
		}
	} else if p.Peer, err = m.askPeer(); err != nil {
		return err
	}

	// Чего в ссылке не было, спрашивается так же, как при вводе вручную.
	if secrets.password == "" {
		if secrets.password, err = m.askPassword(); err != nil {
			return err
		}
	}
	if secrets.hashes == "" {
		if secrets.hashes, err = m.askCallLinks(false); err != nil {
			return err
		}
	}
	if p.Datapath, err = m.askMode(); err != nil {
		return err
	}

	// Первый профиль — профиль по умолчанию. Если профиль уже был один и
	// default_profile не задан, умолчанием он был по правилу «единственный»;
	// второй профиль это правило отменил бы, поэтому умолчание записывается.
	if file.DefaultProfile == "" && !existed {
		switch names := file.Names(); len(names) {
		case 0:
			file.DefaultProfile = name
		case 1:
			file.DefaultProfile = names[0]
		}
	}

	fmt.Fprintln(w)
	if err := storeProfile(m.e, path, file, p, existed, secrets); err != nil {
		return err
	}

	m.current = name
	fmt.Fprintf(w, "Готово. Подключиться к %s — пункт 1 (Enter).\n", name)
	return nil
}

// askShareLink — первый вопрос мастера: ссылка qwdtt://config или Enter для
// ввода вручную (nil). Скрыто: в ссылке пароль.
func (m *menu) askShareLink() (*config.ShareLink, error) {
	fmt.Fprintln(m.e.stdout, "Если у вас есть ссылка qwdtt://config?… (из приложения qWDTT или от бота сервера),")
	fmt.Fprintln(m.e.stdout, "вставьте её — остальное qwdtt-cli возьмёт из неё. Нет ссылки — нажмите Enter.")
	for {
		text, err := m.wizardSecret("Ссылка (при вводе не отображается) [ввести вручную]: ")
		if err != nil {
			return nil, err
		}
		if text == "" {
			return nil, nil
		}
		link, err := config.ParseShareLink(text)
		if err != nil {
			fmt.Fprintf(m.e.stderr, "Ссылка не подошла: %v. Вставьте её ещё раз или нажмите Enter.\n", err)
			continue
		}

		fmt.Fprintf(m.e.stdout, "Ссылка принята: сервер %s", link.Peer)
		if link.Password == "" {
			fmt.Fprint(m.e.stdout, ", пароля в ней нет")
		}
		if n := link.HashCount(); n > 0 {
			fmt.Fprintf(m.e.stdout, ", ссылок на звонки: %d", n)
		} else {
			fmt.Fprint(m.e.stdout, ", ссылок на звонки в ней нет")
		}
		fmt.Fprintln(m.e.stdout, ".")
		return link, nil
	}
}

// wizardAsk — вопрос мастера обычной строкой. Конец ввода — отмена.
func (m *menu) wizardAsk(prompt string) (string, error) {
	line, err := m.ask(prompt)
	if errors.Is(err, errNoInput) {
		return "", errWizardCancelled
	}
	return line, err
}

// wizardSecret — то же скрытым вводом.
func (m *menu) wizardSecret(prompt string) (string, error) {
	value, err := m.e.askSecret(prompt)
	if errors.Is(err, errNoInput) {
		return "", errWizardCancelled
	}
	return value, err
}

// askProfileName спрашивает имя профиля. Имя занято — предлагает заменить
// тот профиль; existed — согласились заменить.
func (m *menu) askProfileName(file *config.File, suggested string) (name string, existed bool, err error) {
	for {
		name, err := m.wizardAsk(fmt.Sprintf("Имя профиля [%s]: ", suggested))
		if err != nil {
			return "", false, err
		}
		if name == "" {
			name = suggested
		}
		if err := config.CheckProfileName(name); err != nil {
			fmt.Fprintf(m.e.stderr, "Такое имя не подойдёт: латинские буквы, цифры, дефис и подчёркивание, например %s.\n",
				freeProfileName(file))
			continue
		}
		if _, taken := file.Profiles[name]; !taken {
			return name, false, nil
		}

		replace, err := m.e.confirm(fmt.Sprintf("Профиль %s уже есть. Заменить его?", name))
		if err != nil {
			return "", false, err
		}
		if replace {
			return name, true, nil
		}
		fmt.Fprintln(m.e.stderr, "Назовите новый профиль иначе.")
	}
}

// freeProfileName — имя, которое мастер предлагает: home, если свободно, иначе
// первое свободное profile-N.
func freeProfileName(file *config.File) string {
	if _, taken := file.Profiles["home"]; !taken {
		return "home"
	}
	for n := 2; ; n++ {
		name := "profile-" + strconv.Itoa(n)
		if _, taken := file.Profiles[name]; !taken {
			return name
		}
	}
}

func (m *menu) askPeer() (string, error) {
	for {
		line, err := m.wizardAsk("Адрес сервера (например, example.org:56000): ")
		if err != nil {
			return "", err
		}
		if line == "" {
			fmt.Fprintln(m.e.stderr, "Адрес сервера нужен: его даёт тот, кто выдал вам пароль.")
			continue
		}
		// Без порта — порт qWDTT по умолчанию, как у Android-клиента.
		peer := config.PeerWithPort(line)
		if err := config.CheckPeer(peer); err != nil {
			fmt.Fprintf(m.e.stderr, "Не похоже на адрес: %v\n", err)
			continue
		}
		if peer != line {
			fmt.Fprintf(m.e.stdout, "Порт не указан — взят %s: %s\n", config.DefaultPeerPort, peer)
		}
		return peer, nil
	}
}

// askPassword спрашивает пароль дважды: опечатку в невидимом вводе иначе не
// заметить, а обнаружилась бы она отказом сервера при подключении.
func (m *menu) askPassword() (string, error) {
	for {
		value, err := m.wizardSecret("Пароль подключения (при вводе не отображается): ")
		if err != nil {
			return "", err
		}
		if value == "" {
			fmt.Fprintln(m.e.stderr, "Пароль не может быть пустым.")
			continue
		}
		repeat, err := m.wizardSecret("Повторите пароль: ")
		if err != nil {
			return "", err
		}
		if repeat != value {
			fmt.Fprintln(m.e.stderr, "Пароли не совпали. Введите ещё раз.")
			continue
		}
		return value, nil
	}
}

// askCallLinks спрашивает ссылки на звонки VK. Скрыто: по ссылке в звонок
// зайдёт кто угодно, это такой же секрет, как пароль. optional — пустой
// ответ допустим и значит «новых ссылок нет».
func (m *menu) askCallLinks(optional bool) (string, error) {
	prompt := "Ссылки на звонки VK через пробел (при вводе не отображаются): "
	if optional {
		prompt = "Новые ссылки на звонки VK через пробел (не отображаются) [без новых]: "
	}
	for {
		value, err := m.wizardSecret(prompt)
		if err != nil {
			return "", err
		}
		if optional && value == "" {
			return "", nil
		}
		n := len(core.ParseHashes(value))
		if n == 0 {
			fmt.Fprintln(m.e.stderr, "Не нашлось ни одной ссылки. Ссылка на звонок выглядит так: https://vk.com/call/join/…")
			continue
		}
		fmt.Fprintf(m.e.stdout, "Принято ссылок: %d\n", n)
		return value, nil
	}
}

// askMode спрашивает режим. Весь трафик — первым и по Enter (решение 2
// plan-e5.md): человеку без технической подготовки прокси настраивать негде.
func (m *menu) askMode() (string, error) {
	w := m.e.stdout
	fmt.Fprintln(w, "Что пускать через туннель?")
	fmt.Fprintln(w, "  1. Весь трафик компьютера. Нужны права администратора — qwdtt-cli попросит их сам.")
	fmt.Fprintf(w, "  2. Только программы, в которых указан прокси SOCKS5 %s.\n", datapath.DefaultSocksListen)
	for {
		line, err := m.wizardAsk("Выберите [1]: ")
		if err != nil {
			return "", err
		}
		switch n, ok := menuNumber(line, 2, 1); {
		case ok && n == 1:
			return config.DatapathTun, nil
		case ok && n == 2:
			return config.DatapathSocks, nil
		}
		fmt.Fprintf(m.e.stderr, "Нет такого пункта: %q. Введите 1 или 2.\n", line)
	}
}
