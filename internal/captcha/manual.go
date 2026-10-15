// manual.go — консольный решатель (задача E2): тот, который работает всегда.
//
// Окна у него нет и браузером он не управляет — только печатает ссылку
// и принимает ответ. Взамен он не требует ни WebView2 Runtime, ни Chromium
// и работает на всех трёх платформах с первого дня. Два удобных решателя,
// которые появятся позже, опираются на него как на запасное дно
// (doc/e2-captcha.md, §7).
//
// Почему у пользователя приходится просить возни с консолью браузера, а не
// просто ссылку, на которую его перенаправило: редиректа с токеном у VK нет
// вовсе, токен приходит в теле ответа captchaNotRobot.check. Подробности —
// в token.go и в том же документе.
package captcha

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

// minTokenLen — короче этого success_token не бывает. Проверка нарочно грубая:
// формат токена VK не описан нигде и может измениться, а вот отличить его
// от «ок», «да» и случайно вставленного слова она успевает.
const minTokenLen = 16

// ErrCancelled — пользователь отказался решать капчу: пустой ответ или конец
// ввода.
var ErrCancelled = errors.New("капча не решена: ответа нет")

// ManualOptions — настройки консольного решателя.
type ManualOptions struct {
	// In — откуда читается ответ. nil — os.Stdin.
	In io.Reader

	// Out — куда уходят приглашение и инструкция. nil — os.Stderr.
	//
	// Именно stderr: stdout у CLI занят тем, что является результатом команды,
	// и `qwdtt-cli connect > log.txt` не должен прятать вопрос от пользователя.
	Out io.Writer

	// Open — открыть ссылку в браузере. nil означает только напечатать её.
	//
	// Полем, а не встроенным вызовом: запускать посторонние программы — решение
	// CLI, а не библиотеки, и тестам это не нужно. Готовая реализация рядом —
	// OpenInBrowser.
	Open func(rawURL string) error

	// Hint — строка, которую стоит показать вместе с инструкцией: например,
	// чем ручной режим можно заменить. Пусто — ничего.
	Hint string
}

// Manual — консольный решатель.
type Manual struct {
	opts  ManualOptions
	lines *bufio.Reader
}

// NewManual собирает решатель. Ввод и вывод не трогаются до Solve.
func NewManual(opts ManualOptions) *Manual {
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stderr
	}
	return &Manual{opts: opts, lines: bufio.NewReader(opts.In)}
}

// NeedsHuman: да, и это главное про него. Диспетчер по этому признаку не станет
// звать его на десятисекундные окна автоцепочки.
func (*Manual) NeedsHuman() bool { return true }

// Solve печатает инструкцию и ждёт ответа.
func (m *Manual) Solve(ctx context.Context, req Request) (string, error) {
	m.prompt(req)

	answer, err := m.readLine(ctx)
	if err != nil {
		return "", err
	}
	return parseAnswer(answer)
}

// prompt печатает всё, что нужно пользователю, одним куском: второй раз
// смотреть в экран ему будет некогда.
func (m *Manual) prompt(req Request) {
	out := m.opts.Out
	left := req.Left(time.Now()).Round(time.Second)

	fmt.Fprintf(out, "\nVK просит решить капчу. Время на это — %s.\n\n", left)
	if m.opts.Hint != "" {
		fmt.Fprintf(out, "%s\n\n", m.opts.Hint)
	}
	fmt.Fprintf(out, "  1. Откройте ссылку:\n     %s\n", req.RedirectURI)

	if m.opts.Open != nil {
		if err := m.opts.Open(req.RedirectURI); err != nil {
			fmt.Fprintf(out, "     (открыть браузер не вышло: %v — откройте вручную)\n", err)
		} else {
			fmt.Fprintln(out, "     (уже открыта в браузере)")
		}
	}

	fmt.Fprintln(out, "  2. Нажмите F12, откройте вкладку Console и вставьте туда эту строку:")
	fmt.Fprintf(out, "\n%s\n\n", consoleSnippet())
	// Chrome и Firefox не дают вставлять код в консоль, пока пользователь не
	// напишет эти два слова: защита от того, что чужой скрипт попросят вставить
	// обманом. Не сказать об этом — значит упереться в неё под таймером.
	fmt.Fprintln(out, "     Если браузер потребует, наберите в консоли allow pasting и нажмите Enter.")
	fmt.Fprintln(out, "  3. Решите капчу. В консоли появится строка вида")
	fmt.Fprintln(out, "     «qwdtt-cli-captcha token <длинная строка>».")
	fmt.Fprint(out, "  4. Вставьте эту длинную строку сюда: ")
}

// readLine читает ответ, не теряя способности вернуться по отмене.
//
// Чтение уходит в горутину потому, что прервать чтение со stdin по ctx
// стандартными средствами нельзя: Read на терминале висит до перевода строки,
// и закрывать os.Stdin, чтобы его расшевелить, — значит ломать stdin всей
// программе. Горутина после отмены остаётся висеть на Read до тех пор, пока
// пользователь не нажмёт Enter или процесс не кончится; буфер канала не даёт
// ей заблокироваться навсегда. Это осознанная цена, и платится она один раз:
// ядро больше капчу не попросит, раз мы уже уходим.
func (m *Manual) readLine(ctx context.Context) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)

	go func() {
		line, err := m.lines.ReadString('\n')
		done <- result{line: line, err: err}
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintln(m.opts.Out)
		return "", ctx.Err()
	case got := <-done:
		line := strings.TrimSpace(got.line)
		if got.err != nil && !errors.Is(got.err, io.EOF) {
			return "", fmt.Errorf("чтение ответа: %w", got.err)
		}
		if line == "" {
			return "", ErrCancelled
		}
		return line, nil
	}
}

// parseAnswer разбирает то, что вставил пользователь.
//
// Принимается не только голый токен: человек под таймером копирует то, что
// попалось под руку, и отвергать целый ответ VK из-за фигурных скобок было бы
// мелочно. Разбор идёт от более определённого к менее.
func parseAnswer(answer string) (string, error) {
	answer = strings.TrimSpace(answer)
	switch {
	case answer == "":
		return "", ErrCancelled

	case strings.HasPrefix(answer, "{"):
		// Скопировали ответ VK целиком — разберём как ответ VK.
		return successTokenFrom([]byte(answer))

	case looksLikeURL(answer):
		// Самая вероятная ошибка, и ровно то заблуждение, с которого начиналась
		// задача: токена в адресе нет ни у одной ссылки VK.
		return "", errors.New("это ссылка, а не токен: токен не появляется в адресной строке, " +
			"его печатает вставленная в консоль строка после слова token")

	case strings.Contains(answer, "success_token"):
		// Скопировали кусок ответа вместе с именем поля.
		if token := afterFieldName(answer); token != "" {
			return token, nil
		}
		return "", errors.New("после success_token не видно значения")

	case strings.ContainsAny(answer, " \t"):
		// Скопировали всю строку из консоли вместе с «qwdtt-cli-captcha token».
		if fields := strings.Fields(answer); len(fields) > 0 {
			if last := fields[len(fields)-1]; len(last) >= minTokenLen {
				return last, nil
			}
		}
		return "", errors.New("в ответе несколько слов и ни одно не похоже на токен")

	case len(answer) < minTokenLen:
		return "", fmt.Errorf("для токена это слишком коротко (%d символов): "+
			"нужна длинная строка после слова token", len(answer))

	default:
		return answer, nil
	}
}

// afterFieldName достаёт значение, стоящее за именем поля, из обрывка вида
// `"success_token":"…"` или `success_token=…`.
func afterFieldName(answer string) string {
	_, tail, ok := strings.Cut(answer, "success_token")
	if !ok {
		return ""
	}
	tail = strings.TrimLeft(tail, `"': =`)
	tail = strings.TrimRight(tail, `"',}] `)
	if len(tail) < minTokenLen {
		return ""
	}
	return tail
}

// looksLikeURL — вставили ссылку, а не токен.
func looksLikeURL(answer string) bool {
	lower := strings.ToLower(answer)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return false
	}
	_, err := url.Parse(answer)
	return err == nil
}
