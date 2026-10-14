// menu.go — интерактивный режим: tt без аргументов в терминале (plan-e5.md).
//
// Большинству пользователей консольные команды сложны: на Windows tt
// запускают двойным щелчком, и окно консоли — всё, что они видят. Меню ведёт
// такого человека вопросами и само зовёт те же команды, что набираются руками:
// подключение — runConnect, удаление — runProfileRm и так далее. Своей логики
// подключения и записи профилей у меню нет.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/term"

	"github.com/FreedomCats/qwdtt-cli/internal/config"
	"github.com/FreedomCats/qwdtt-cli/internal/datapath"
	"github.com/FreedomCats/qwdtt-cli/internal/supervisor"
)

func menuCommand() *command {
	return &command{
		name:    "menu",
		args:    "[флаги]",
		summary: "меню: подключение и профили вопросами, без флагов",
		description: `То же меню открывается, если запустить tt без аргументов в терминале —
например, двойным щелчком по tt.exe. Команда нужна там, где запуск без
аргументов печатает список команд: ярлык, чужая оболочка.

Ctrl+C во время подключения останавливает туннель и возвращает в меню,
в самом меню — закрывает его.`,
		run: runMenu,
	}
}

// wantsMenu — откроет ли run меню. Main спрашивает заранее: сигналы в меню
// разбираются по-своему (см. menuSignals), и общий обработчик ставить нельзя.
func wantsMenu(e *env, args []string) bool {
	if len(args) == 0 {
		return e.console()
	}
	return args[0] == "menu" && !wantsHelp(args[1:])
}

// console — stdin и stdout оба терминал: человек сидит перед окном. Меню
// в конвейере или из скрипта ждало бы ответов, которых не будет, поэтому
// без терминала tt без аргументов печатает список команд, как раньше.
func (e *env) console() bool {
	if e.tty != nil {
		return e.tty()
	}
	_, in := fileTerminal(e.stdin)
	_, out := fileTerminal(e.stdout)
	return in && out
}

func runMenu(ctx context.Context, e *env, args []string) error {
	cmd := menuCommand()
	fs := newFlagSet(cmd)
	configPath := addConfigFlag(fs)
	rest, err := parseWithArgs(e, fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return usagef("лишний аргумент %q: команда menu аргументов не принимает", rest[0])
	}
	if !e.console() {
		return errors.New("меню работает только в терминале; без него — команды, список: tt help")
	}

	m := &menu{e: e, config: *configPath}
	if e.signals {
		stop := m.catchSignals()
		defer stop()
	}
	return m.loop(ctx)
}

// menu — состояние одного сеанса меню.
type menu struct {
	e *env
	// config — значение -config: передаётся командам, которые зовёт меню.
	config string
	// current — профиль, выбранный пунктом «Выбрать другой профиль». Только
	// на этот сеанс: default_profile не меняется. Пусто — профиль по умолчанию.
	current string

	// signals — куда отдать Ctrl+C; nil — сигналы не перехвачены (тесты).
	signals *menuSignals
}

// errMenuExit — человек ушёл из меню: выбрал выход или закрыл ввод.
var errMenuExit = errors.New("выход из меню")

// menuItem — пункт меню. Номер — место в списке, выход — всегда 0.
type menuItem struct {
	label string
	run   func(ctx context.Context, name string) error
}

func (m *menu) loop(ctx context.Context) error {
	e := m.e
	fmt.Fprintf(e.stdout, "tt %s — клиент qWDTT\n", e.build.Version)

	for {
		_, file, err := m.load()
		if err != nil {
			return err
		}

		name := m.selected(file)
		items := m.items(name)
		m.show(file, name, items)

		choice, err := m.choose(len(items))
		if errors.Is(err, errMenuExit) {
			// Уход из меню — решение человека, а не ошибка: паузы перед
			// закрытием окна не нужно (pause.go).
			e.noPause = true
			fmt.Fprintln(e.stdout, "До свидания.")
			return nil
		}
		if err != nil {
			return err
		}

		fmt.Fprintln(e.stdout)
		err = items[choice-1].run(ctx, name)
		if errors.Is(err, errMenuExit) {
			e.noPause = true
			return nil
		}
		if err != nil {
			fmt.Fprintf(e.stderr, "Не получилось: %v\n", err)
		}
	}
}

// load читает файл настроек. Файла нет — меню начинается с пустого списка:
// первый профиль как раз и заводится из меню.
func (m *menu) load() (string, *config.File, error) {
	return loadProfilesForWrite(m.config)
}

// selected — с каким профилем работают пункты меню: выбранный в этом сеансе,
// иначе профиль по умолчанию, иначе первый по алфавиту. Пусто — профилей нет.
func (m *menu) selected(file *config.File) string {
	if _, ok := file.Profiles[m.current]; ok {
		return m.current
	}
	m.current = ""
	if p, err := file.Profile(""); err == nil {
		return p.Name
	}
	if names := file.Names(); len(names) > 0 {
		return names[0]
	}
	return ""
}

// items — пункты меню в порядке решения 3 plan-e5.md. Номера постоянные,
// чтобы инструкция «выберите 3» оставалась верной при любом числе профилей.
func (m *menu) items(name string) []menuItem {
	if name == "" {
		return []menuItem{{label: "Создать профиль (по ссылке qwdtt:// или вручную)", run: m.create}}
	}
	return []menuItem{
		{label: fmt.Sprintf("Подключиться (%s)", name), run: m.connect},
		{label: "Выбрать другой профиль", run: m.pick},
		{label: "Создать новый профиль (по ссылке qwdtt:// или вручную)", run: m.create},
		{label: "Обновить ссылки на звонки VK", run: m.updateHashes},
		{label: "Заменить пароль или адрес сервера", run: m.replacePasswordOrPeer},
		{label: fmt.Sprintf("Удалить профиль %s", name), run: m.remove},
	}
}

func (m *menu) show(file *config.File, name string, items []menuItem) {
	w := m.e.stdout
	fmt.Fprintln(w)

	names := file.Names()
	if len(names) == 0 {
		fmt.Fprintln(w, "Профилей пока нет. Профиль — это адрес сервера, пароль и ссылки на звонки VK.")
	} else {
		width, peerWidth := 0, 0
		for _, n := range names {
			width = max(width, len(n))
			peerWidth = max(peerWidth, len(file.Profiles[n].Peer))
		}
		fmt.Fprintln(w, "Профили:")
		for _, n := range names {
			p := file.Profiles[n]
			marker := " "
			if n == name {
				marker = ">"
			}
			note := ""
			if isDefaultProfile(file, n) {
				note = "  (по умолчанию)"
			}
			fmt.Fprintf(w, "  %s %-*s  %-*s  %s%s\n", marker, width, n, peerWidth, p.Peer, modeText(p.Datapath), note)
		}
	}

	fmt.Fprintln(w)
	for i, item := range items {
		fmt.Fprintf(w, "  %d. %s\n", i+1, item.label)
	}
	fmt.Fprintln(w, "  0. Выход")
}

// modeText — режим профиля словами, без названий datapath.
func modeText(datapath string) string {
	switch config.EffectiveDatapath(datapath) {
	case config.DatapathTun:
		return "весь трафик"
	case config.DatapathExternal:
		return "через WireGuard"
	default:
		return "только программы с прокси"
	}
}

// choose спрашивает номер пункта, пока не получит годный. Enter — первый
// пункт, 0 и конец ввода — выход.
func (m *menu) choose(count int) (int, error) {
	for {
		line, err := m.ask("Выберите пункт [1]: ")
		if errors.Is(err, errNoInput) {
			return 0, errMenuExit
		}
		if err != nil {
			return 0, err
		}

		n, ok := menuNumber(line, count, 1)
		switch {
		case !ok:
			fmt.Fprintf(m.e.stderr, "Нет такого пункта: %q. Введите номер от 0 до %d.\n", line, count)
		case n == 0:
			return 0, errMenuExit
		default:
			return n, nil
		}
	}
}

// menuNumber разбирает ответ на вопрос с нумерованным списком 0..count.
// Пустой ответ — fallback.
func menuNumber(line string, count, fallback int) (int, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 0 || n > count {
		return 0, false
	}
	return n, true
}

// ask задаёт вопрос и читает строку ответа. Приглашение — в stderr, как у
// остальных вопросов tt (prompt.go).
func (m *menu) ask(prompt string) (string, error) {
	fmt.Fprint(m.e.stderr, prompt)
	line, err := m.e.readLine()
	if errors.Is(err, errNoInput) {
		fmt.Fprintln(m.e.stderr)
	}
	return line, err
}

// args — аргументы для команды, которую зовёт меню: свои плюс -config, если
// его назвали при запуске меню.
func (m *menu) args(args ...string) []string {
	if m.config != "" {
		args = append(args, "-config", m.config)
	}
	return args
}

// ──────────────────────────── пункты ────────────────────────────

func (m *menu) connect(ctx context.Context, name string) error {
	for {
		opts := &connectOptions{common: &commonOptions{config: m.config}, named: map[string]bool{}}
		plan, err := planConnect(opts, name)
		if err != nil {
			return err
		}
		// Прав не хватает — запросить их, а не объяснять, как запустить tt
		// иначе: человек из меню командной строки не знает.
		if plan.datapath(nil).Requires().Admin && !isElevated() {
			return m.connectElevated(name)
		}

		err = m.connectOnce(ctx, plan)
		if !errors.Is(err, supervisor.ErrHashesExhausted) || ctx.Err() != nil {
			return err
		}

		// Звонки закрылись — чаще всего так кончается сеанс, и выход из него
		// один: новые ссылки. Спросить их здесь же, а не отсылать в пункт 4.
		fmt.Fprintln(m.e.stderr, "Ни одна ссылка на звонок VK больше не работает: звонки закрыты.")
		again, err := m.e.confirm("Ввести новые ссылки и подключиться снова?")
		if err != nil || !again {
			return err
		}
		if err := m.storeHashes(name); err != nil {
			if errors.Is(err, errWizardCancelled) {
				fmt.Fprintln(m.e.stdout, "Ссылки не изменены.")
				return nil
			}
			return err
		}
	}
}

// connectOnce — один сеанс подключения. Ctrl+C на это время останавливает
// его, а не меню.
func (m *menu) connectOnce(ctx context.Context, plan *connectPlan) error {
	ctx, done := m.connecting(ctx)
	defer done()

	fmt.Fprintln(m.e.stdout, "Подключение. Остановить — Ctrl+C, после этого снова откроется меню.")
	if m.e.connectRun != nil {
		return m.e.connectRun(ctx, plan)
	}
	return plan.run(ctx, m.e, false)
}

// storeHashes спрашивает новые ссылки на звонки и записывает их вместо прежних.
func (m *menu) storeHashes(name string) error {
	path, file, err := m.load()
	if err != nil {
		return err
	}
	p, err := file.Profile(name)
	if err != nil {
		return err
	}
	hashes, err := m.askCallLinks(false)
	if err != nil {
		return err
	}
	return storeProfile(m.e, path, file, p, true, profileSecrets{hashes: hashes})
}

// connectElevated перезапускает подключение с правами администратора.
func (m *menu) connectElevated(name string) error {
	done := m.runningChild()
	detached, err := m.e.relaunch(relaunchArgs(name, m.config))
	done()

	switch {
	case errors.Is(err, errNoAdminAccount):
		return m.offerSocks(name)
	case err != nil:
		return err
	case detached:
		// Подключение ушло в своё окно с правами; это окно больше не нужно.
		return errMenuExit
	}
	return nil
}

// offerSocks — прав администратора не получить: предложить режим, которому
// они не нужны.
func (m *menu) offerSocks(name string) error {
	w := m.e.stdout
	fmt.Fprintln(w, "Режиму «весь трафик» нужны права администратора, а у этой учётной записи их нет.")
	fmt.Fprintln(w, "С паролем другой учётной записи tt запустился бы от её имени и не нашёл бы этот профиль.")
	fmt.Fprintf(w, "Без прав работает режим «только программы с прокси» (SOCKS5 %s).\n", datapath.DefaultSocksListen)

	agreed, err := m.e.confirm(fmt.Sprintf("Переключить профиль %s на этот режим?", name))
	if err != nil || !agreed {
		return err
	}
	if err := runProfileSet(context.Background(), m.e, m.args(name, "-datapath", config.DatapathSocks)); err != nil {
		return err
	}
	fmt.Fprintln(w, "Готово. Подключиться — пункт 1 (Enter).")
	return nil
}

func (m *menu) pick(context.Context, string) error {
	_, file, err := m.load()
	if err != nil {
		return err
	}
	names := file.Names()
	if len(names) < 2 {
		fmt.Fprintln(m.e.stdout, "Другого профиля нет. Завести его — пункт «Создать новый профиль».")
		return nil
	}

	for i, n := range names {
		fmt.Fprintf(m.e.stdout, "  %d. %s  %s\n", i+1, n, file.Profiles[n].Peer)
	}
	fmt.Fprintln(m.e.stdout, "  0. Назад")

	for {
		line, err := m.ask("Какой профиль? [0]: ")
		if errors.Is(err, errNoInput) {
			return nil
		}
		if err != nil {
			return err
		}
		n, ok := menuNumber(line, len(names), 0)
		if !ok {
			fmt.Fprintf(m.e.stderr, "Нет такого профиля: %q. Введите номер от 0 до %d.\n", line, len(names))
			continue
		}
		if n > 0 {
			m.current = names[n-1]
		}
		return nil
	}
}

func (m *menu) remove(ctx context.Context, name string) error {
	return runProfileRm(ctx, m.e, m.args(name))
}

// ──────────────────────────── сигналы ────────────────────────────

// menuSignals разбирает Ctrl+C (и SIGTERM — так Windows сообщает о закрытии
// окна) всё время, пока открыто меню.
//
// Во время подключения сигнал отменяет только его: туннель снимается, и
// человек возвращается в меню. Вне подключения сигнал закрывает меню. Второй
// сигнал во время остановки — выход сразу, как у tt connect: ждать зависшую
// уборку человек не обязан.
type menuSignals struct {
	mu sync.Mutex
	// cancel — отменить идущее подключение; nil — подключения нет.
	cancel context.CancelFunc
	// child — идёт подключение, перезапущенное с правами (sudo): сигнал
	// терминал отдал и ему, и разбирается с ним он. Меню ждёт его конца.
	child bool
	// exit — выйти, не дожидаясь меню.
	exit func()
}

// catchSignals ставит обработчик на всё время меню и возвращает функцию,
// которая его снимает.
func (m *menu) catchSignals() func() {
	// Сигнал может прийти посреди скрытого ввода, когда эхо в терминале
	// выключено. Выход без восстановления оставил бы терминал немым.
	var restore func()
	if fd, ok := m.e.terminalFD(); ok {
		if state, err := term.GetState(fd); err == nil {
			restore = func() { _ = term.Restore(fd, state) }
		}
	}

	s := &menuSignals{exit: func() {
		if restore != nil {
			restore()
		}
		fmt.Fprintln(m.e.stderr)
		os.Exit(exitInterrupted)
	}}
	m.signals = s

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				s.handle()
			case <-done:
				return
			}
		}
	}()

	return func() {
		signal.Stop(ch)
		close(done)
		m.signals = nil
	}
}

func (s *menuSignals) handle() {
	s.mu.Lock()
	if s.child {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
		return
	}
	s.exit()
}

// connecting заводит контекст подключения, который отменит Ctrl+C. Вторая
// функция — подключение кончилось, Ctrl+C снова закрывает меню.
func (m *menu) connecting(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	s := m.signals
	if s == nil {
		return ctx, cancel
	}

	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
	return ctx, func() {
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
		cancel()
	}
}

// runningChild — запущен tt с правами: пока он идёт, сигналы меню не
// трогают. Возвращает функцию, которая снимает отметку.
func (m *menu) runningChild() func() {
	s := m.signals
	if s == nil {
		return func() {}
	}
	s.mu.Lock()
	s.child = true
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		s.child = false
		s.mu.Unlock()
	}
}

// exitInterrupted — код выхода по Ctrl+C: 128 + SIGINT, как принято в оболочках.
const exitInterrupted = 130
