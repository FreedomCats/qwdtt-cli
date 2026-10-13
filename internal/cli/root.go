// Package cli — разбор командной строки tt и запуск того, что за ней стоит.
//
// Разбор идёт стандартным flag с подкомандами, без внешних библиотек: команд
// пять, флаги у них простые, а чужая библиотека принесла бы с собой свой
// формат помощи, свои соглашения о кодах возврата и ещё одну зависимость,
// которую пришлось бы обновлять.
//
// Здесь только каркас: список команд, общие флаги, помощь и коды возврата.
// Тела команд лежат рядом — connect.go, diag.go, profile.go.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
)

// Коды возврата. Три, и разделены они намеренно: скрипту важно отличить
// «команда набрана неверно» от «команда отработала и не смогла».
const (
	exitOK    = 0 // всё получилось
	exitError = 1 // команда отработала и вернула ошибку
	exitUsage = 2 // ошибка в командной строке: нет такой команды, флага, аргумента
)

// Build — сведения о сборке. Подставляются в cmd/tt через -ldflags (задача H1).
type Build struct {
	Version string
	Commit  string
}

// env — окружение команды: потоки ввода-вывода и сведения о сборке.
//
// Отдельной структурой ради тестов: они подставляют буферы вместо os.Stdout
// и os.Stderr, а команды никуда, кроме env, не пишут.
type env struct {
	stdout io.Writer
	stderr io.Writer
	stdin  io.Reader
	build  Build
}

// command — одна команда tt.
//
// Своего набора флагов у команды здесь нет: его заводит сама run, потому что
// разбор и значения флагов нужны в одном месте. Помощь по команде печатается
// тем же путём — help зовёт run с аргументом -h.
type command struct {
	// name — как команда называется в командной строке.
	name string
	// args — строка синтаксиса после имени команды, для строки «Использование».
	args string
	// summary — одна строка для списка команд. С маленькой буквы, без точки.
	summary string
	// description — абзац между строкой синтаксиса и списком флагов.
	// Необязателен.
	description string
	// run разбирает свои аргументы и делает работу.
	run func(ctx context.Context, e *env, args []string) error
}

// commands — все команды tt в порядке, в котором их показывает помощь:
// сначала то, ради чего программу запускают, потом обслуживающее.
func commands() []*command {
	return []*command{
		connectCommand(),
		pingCommand(),
		hashesCommand(),
		profileCommand(),
		versionCommand(),
		helpCommand(),
	}
}

func lookup(name string) *command {
	for _, cmd := range commands() {
		if cmd.name == name {
			return cmd
		}
	}
	return nil
}

// Main — точка входа CLI. Возвращает код возврата; os.Exit зовёт cmd/tt.
func Main(args []string, build Build) int {
	e := &env{stdout: os.Stdout, stderr: os.Stderr, stdin: os.Stdin, build: build}

	// Ctrl+C и SIGTERM отменяют контекст команды. Второй Ctrl+C с немедленным
	// выходом и снятие datapath — задача F3; здесь заводится только контекст,
	// без которого командам нечего передать в ядро.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return run(ctx, e, args)
}

// run разделён с Main ради тестов: им нужны свои потоки и свой контекст.
func run(ctx context.Context, e *env, args []string) int {
	if len(args) == 0 {
		// Запуск без аргументов — это вопрос «что ты умеешь», а не ошибка,
		// поэтому список команд уходит в stdout и код возврата нулевой.
		printUsage(e.stdout)
		return exitOK
	}

	name := args[0]
	if isHelpFlag(name) {
		printUsage(e.stdout)
		return exitOK
	}

	cmd := lookup(name)
	if cmd == nil {
		fmt.Fprintf(e.stderr, "tt: неизвестная команда %q\n", name)
		fmt.Fprintf(e.stderr, "Список команд: tt help\n")
		return exitUsage
	}

	return report(e, cmd, cmd.run(ctx, e, args[1:]))
}

// report превращает ошибку команды в код возврата и печатает то, о чём ещё
// не сказано.
func report(e *env, cmd *command, err error) int {
	switch {
	case err == nil:
		return exitOK
	case errors.Is(err, flag.ErrHelp):
		// Помощь просили — значит, её печать и есть успешный исход.
		return exitOK
	case errors.Is(err, errReported):
		// Пакет flag уже напечатал и ошибку, и помощь.
		return exitUsage
	}

	fmt.Fprintf(e.stderr, "tt %s: %v\n", cmd.name, err)

	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintf(e.stderr, "Помощь: tt %s -h\n", cmd.name)
		return exitUsage
	}
	return exitError
}

// usageError — ошибка в командной строке, о которой ещё не сказано: лишний
// аргумент, отсутствующая подкоманда, несовместимые флаги. От обычной ошибки
// отличается кодом возврата и подсказкой, где смотреть помощь.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error {
	return usageError{msg: fmt.Sprintf(format, a...)}
}

// errReported — ошибка, о которой уже сказано пользователю. Такую печатать
// второй раз нельзя: пакет flag сам пишет и причину, и помощь по команде.
var errReported = errors.New("ошибка в аргументах команды")

// newFlagSet заводит набор флагов команды с нашей помощью вместо стандартной.
func newFlagSet(cmd *command) *flag.FlagSet {
	fs := flag.NewFlagSet("tt "+cmd.name, flag.ContinueOnError)
	fs.Usage = func() { writeUsage(cmd, fs) }
	return fs
}

// parse разбирает флаги команды.
//
// Помощь по -h уходит в stdout, сообщение об ошибке — в stderr: помощь просили,
// ошибку — нет. Пакет flag пишет и то и другое в один поток, поэтому поток
// выбирается заранее, по самим аргументам.
func parse(e *env, fs *flag.FlagSet, args []string) error {
	if wantsHelp(args) {
		fs.SetOutput(e.stdout)
	} else {
		fs.SetOutput(e.stderr)
	}

	err := fs.Parse(args)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, flag.ErrHelp):
		return err
	default:
		return fmt.Errorf("%w: %w", errReported, err)
	}
}

// setFlags возвращает имена флагов, которые действительно назвали в командной
// строке. Нужно, чтобы отличить «-workers 0» от «-workers не задан»: профиль
// перекрывается только тем, что названо (задача G2).
func setFlags(fs *flag.FlagSet) map[string]bool {
	named := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { named[f.Name] = true })
	return named
}

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "-help" || arg == "--help"
}

// wantsHelp — просили ли помощь. Аргументы после "--" не считаются: там уже
// не флаги, а значения.
func wantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if isHelpFlag(arg) {
			return true
		}
	}
	return false
}

// printUsage печатает список команд — то, что видно при запуске без аргументов.
func printUsage(w io.Writer) {
	fmt.Fprintln(w, "tt — консольный клиент qWDTT.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Использование: tt <команда> [флаги] [аргументы]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Команды:")
	for _, cmd := range commands() {
		fmt.Fprintf(w, "  %-9s %s\n", cmd.name, cmd.summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Помощь по команде: tt help <команда> или tt <команда> -h")
}

// writeUsage печатает помощь по одной команде: строку синтаксиса, описание
// и флаги. Зовётся пакетом flag через fs.Usage, поэтому пишет туда же, куда
// пишет сам набор флагов.
func writeUsage(cmd *command, fs *flag.FlagSet) {
	w := fs.Output()

	fmt.Fprintf(w, "Использование: tt %s", cmd.name)
	if cmd.args != "" {
		fmt.Fprintf(w, " %s", cmd.args)
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "\n%s\n", capitalize(cmd.summary))
	if cmd.description != "" {
		fmt.Fprintf(w, "\n%s\n", strings.TrimRight(cmd.description, "\n"))
	}

	if hasFlags(fs) {
		fmt.Fprintln(w, "\nФлаги:")
		fs.PrintDefaults()
	}
}

func hasFlags(fs *flag.FlagSet) bool {
	found := false
	fs.VisitAll(func(*flag.Flag) { found = true })
	return found
}

// capitalize поднимает первую букву: summary пишется строчной для ровного
// списка команд, а в помощи по команде это отдельное предложение.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// --- общие флаги ---

// commonOptions — флаги, общие для команд, которые читают профиль.
//
// Имени профиля среди них нет: оно позиционный аргумент (`tt connect home`),
// потому что это главное слово команды, а не её настройка.
type commonOptions struct {
	// config — путь к profiles.toml вместо файла по умолчанию.
	config string
	// verbose — печатать весь лог ядра, а не только значимые события.
	// Разбор вывода — задача G4.
	verbose bool
}

func addCommonFlags(fs *flag.FlagSet) *commonOptions {
	o := &commonOptions{}
	fs.StringVar(&o.config, "config", "", "путь к profiles.toml вместо файла настроек по умолчанию")
	fs.BoolVar(&o.verbose, "v", false, "печатать весь лог ядра, а не только значимые события")
	return o
}

// --- version ---

func versionCommand() *command {
	return &command{
		name:    "version",
		summary: "версия и коммит сборки",
		run:     runVersion,
	}
}

func runVersion(_ context.Context, e *env, args []string) error {
	fs := newFlagSet(versionCommand())
	if err := parse(e, fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return usagef("лишний аргумент %q: команда version аргументов не принимает", fs.Arg(0))
	}

	fmt.Fprintf(e.stdout, "tt %s\n", e.build.Version)
	fmt.Fprintf(e.stdout, "коммит: %s\n", e.build.Commit)
	fmt.Fprintf(e.stdout, "сборка: %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return nil
}

// --- help ---

func helpCommand() *command {
	return &command{
		name:    "help",
		args:    "[команда]",
		summary: "помощь по команде",
		run:     runHelp,
	}
}

func runHelp(ctx context.Context, e *env, args []string) error {
	// Свой набор флагов у help пустой, но заводится он на общих основаниях:
	// иначе `tt help -h` искал бы команду с именем «-h».
	fs := newFlagSet(helpCommand())
	if err := parse(e, fs, args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		printUsage(e.stdout)
		return nil
	}
	if fs.NArg() > 1 {
		return usagef("лишний аргумент %q: help принимает не больше одного имени команды", fs.Arg(1))
	}

	cmd := lookup(fs.Arg(0))
	if cmd == nil {
		return usagef("неизвестная команда %q; список команд: tt help", fs.Arg(0))
	}

	// Помощь печатает сама команда: набор флагов заводится в её run, и другого
	// способа добраться до него нет. Возвращённый flag.ErrHelp наверху считается
	// успехом — см. report.
	return cmd.run(ctx, e, []string{"-h"})
}

// todo — заглушка на время, пока за командой ничего не стоит. Называет задачу,
// в которой она появится, чтобы сообщение не выглядело поломкой.
func todo(task, what string) error {
	return fmt.Errorf("%s появится в задаче %s (см. doc/plan-e1.md)", what, task)
}
