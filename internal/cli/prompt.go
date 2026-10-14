// prompt.go — ввод, который нельзя брать из аргументов командной строки:
// секреты и подтверждение опасного действия.
package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// mask — чем секрет показывается на экране. Длина постоянная: настоящая
// сказала бы о пароле больше, чем следует.
const mask = "********"

// errNoInput — ввод кончился, не начавшись: команду запустили без терминала
// и без данных на stdin. Отдельная ошибка, чтобы вызывающий мог объяснить,
// чего он ждал.
var errNoInput = errors.New("ввод пуст")

// askSecret спрашивает значение, не показывая его на экране.
//
// Приглашение уходит в stderr: stdout остаётся чистым для того, что у команды
// является результатом. Если stdin не терминал (значение подают из конвейера
// или из теста), эха отключать нечего — читается обычная строка. Это не
// нарушает правила «секрет не приходит аргументом»: в историю оболочки и
// в список процессов конвейер не попадает.
func (e *env) askSecret(prompt string) (string, error) {
	if e.secrets != nil {
		return e.secrets(prompt)
	}

	fmt.Fprint(e.stderr, prompt)

	if fd, ok := e.terminalFD(); ok {
		raw, err := term.ReadPassword(fd)
		fmt.Fprintln(e.stderr)
		if err != nil {
			return "", fmt.Errorf("скрытый ввод: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}

	line, err := e.readLine()
	if err != nil {
		return "", err
	}
	return line, nil
}

// askNewSecret спрашивает новое значение секрета.
//
// Пустое значение не записывается: пустой пароль ядро всё равно не пропустит,
// а в хранилище осталась бы бесполезная запись. Если спрашивают человека,
// значение просят повторить — опечатку в невидимом вводе иначе не заметить,
// а обнаружится она невнятной ошибкой при подключении. Из конвейера значение
// читается один раз: повторять там нечего.
func (e *env) askNewSecret(prompt, again, what string, confirm bool) (string, error) {
	value, err := e.askSecret(prompt)
	if errors.Is(err, errNoInput) {
		return "", fmt.Errorf("%s: ввод пуст", what)
	}
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s: пустое значение не записывается", what)
	}

	if !confirm || !e.interactive() {
		return value, nil
	}

	repeat, err := e.askSecret(again)
	if err != nil && !errors.Is(err, errNoInput) {
		return "", err
	}
	if repeat != value {
		return "", fmt.Errorf("%s: введённые значения не совпали", what)
	}
	return value, nil
}

// interactive — спрашиваем ли человека. Подставленный ввод (тесты) считается
// человеком: повторный вопрос — часть поведения, и проверять его надо.
func (e *env) interactive() bool {
	if e.secrets != nil {
		return true
	}
	_, ok := e.terminalFD()
	return ok
}

// confirm спрашивает согласие на необратимое действие. Умолчание — «нет»:
// пустой ввод и конец ввода означают отказ.
func (e *env) confirm(question string) (bool, error) {
	fmt.Fprintf(e.stderr, "%s [y/N] ", question)

	line, err := e.readLine()
	if errors.Is(err, errNoInput) {
		fmt.Fprintln(e.stderr)
		return false, nil
	}
	if err != nil {
		return false, err
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes", "д", "да":
		return true, nil
	default:
		return false, nil
	}
}

// readLine читает одну строку со stdin.
//
// Буферизованный читатель хранится в env: у команды бывает несколько вопросов
// подряд, а новый bufio.Reader на каждый вопрос уносил бы с собой всё, что
// успел прочитать сверх строки.
func (e *env) readLine() (string, error) {
	if e.lines == nil {
		e.lines = bufio.NewReader(e.stdin)
	}

	line, err := e.lines.ReadString('\n')
	if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
		return "", errNoInput
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("чтение ввода: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// terminalFD отдаёт дескриптор stdin, если это терминал. Только у настоящего
// файла есть дескриптор, а у терминала — режим, в котором можно погасить эхо.
func (e *env) terminalFD() (int, bool) {
	return fileTerminal(e.stdin)
}

// stdoutTerminal говорит, терминал ли stdout, и отдаёт функцию, которая
// возвращает его ширину в знаках (ноль — узнать не удалось). Спрашивать
// ширину каждый раз, а не один, нужно потому, что окно растягивают по ходу
// работы. Для статусной строки `qwdtt-cli connect` (output.go).
func (e *env) stdoutTerminal() (width func() int, ok bool) {
	fd, ok := fileTerminal(e.stdout)
	if !ok {
		return nil, false
	}
	return func() int {
		w, _, err := term.GetSize(fd)
		if err != nil {
			return 0
		}
		return w
	}, true
}

func fileTerminal(stream any) (int, bool) {
	f, ok := stream.(*os.File)
	if !ok {
		return 0, false
	}
	fd := int(f.Fd())
	return fd, term.IsTerminal(fd)
}
