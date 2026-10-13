// log.go — логи ядра уходят получателю событий, а не в stderr. Файл наш:
// из апстрима не переносится и при обновлении ядра не затирается.
//
// Апстримное ядро — отдельный процесс, поэтому писало логи прямо в stderr через
// log.Printf, а Android вылавливал из них состояние подключения регулярками.
// Библиотека так не может: куда, в каком виде и с какой подробностью писать,
// решает тот, кто её запустил. Поэтому каждый вызов log.* заменён на
// logf/logln, а те отправляют EventLog.
//
// Сама замена механическая и живёт в scripts/vendor-core.ps1 — иначе она не
// переживёт обновление ядра.

package core

import (
	"fmt"
	"os"
	"strings"
)

// logf — замена log.Printf.
func logf(format string, args ...any) {
	emitLog(fmt.Sprintf(format, args...))
}

// logln — замена log.Println и log.Print. Апстрим зовёт обе только с одним
// аргументом, а на одном аргументе они ведут себя одинаково.
func logln(args ...any) {
	emitLog(strings.TrimRight(fmt.Sprintln(args...), "\n"))
}

// fatalf — замена log.Fatalf.
//
// Библиотеке останавливать процесс нельзя, и os.Exit отсюда уйдёт в задаче B4
// вместе с самими вызовами: все они сидят в main() и проверяют аргументы, а
// это работа CLI. До тех пор поведение сохранено дословно — менять его заодно
// с механической заменой логов было бы способом получить сюрприз.
func fatalf(format string, args ...any) {
	emitFatal(fmt.Sprintf(format, args...))
}

// fatalln — замена log.Fatal и log.Fatalln.
func fatalln(args ...any) {
	emitFatal(strings.TrimRight(fmt.Sprintln(args...), "\n"))
}

func emitLog(msg string) {
	tag, text := splitLogTag(msg)
	emit(EventLog{Level: guessLogLevel(text), Tag: tag, Msg: text})
}

func emitFatal(msg string) {
	tag, text := splitLogTag(msg)
	emit(EventLog{Level: LevelError, Tag: tag, Msg: text})
	os.Exit(1)
}

// splitLogTag отрезает от сообщения ведущий тег в квадратных скобках.
//
// Префикс есть почти у каждой строки ядра: "[КЛИЕНТ]", "[СЕССИЯ #3]", "[КАПЧА]".
// Там, где префикса два — "[ВОРКЕР #3] [DTLS] Рукопожатие…" — тегом становится
// первый: он говорит, кто пишет. Второй остаётся в начале текста, поэтому
// строка собирается обратно ровно такой, какой была в апстриме.
func splitLogTag(msg string) (tag, text string) {
	if !strings.HasPrefix(msg, "[") {
		return "", msg
	}
	end := strings.IndexByte(msg, ']')
	if end < 0 {
		return "", msg
	}
	return msg[1:end], strings.TrimLeft(msg[end+1:], " ")
}

// Слова, по которым уровень определяется из текста. Списки короткие
// намеренно: чем длиннее, тем больше ложных срабатываний.
var (
	// Явная пометка автора сообщения весит больше случайного слова в тексте,
	// поэтому предупреждения проверяются первыми: строка
	// "[VK Auth] Warning: getCallPreview failed" — предупреждение, а не отказ,
	// хотя содержит "failed".
	warnMarkers = []string{"warning", "внимание", "предупрежд"}

	// "ошибк" вместо "ошибка": ловит и "ошибки", и "ошибке".
	errorMarkers = []string{"ошибк", "не удалось", "error", "failed", "fatal"}
)

// guessLogLevel выводит уровень из текста сообщения.
//
// Точнее не получится: замена log.Printf на logf механическая, места вызова
// она не различает, а размечать полторы сотни строк вручную — значит потерять
// разметку при первом же обновлении ядра. Android определял важность строки
// тем же способом, разбирая вывод процесса.
func guessLogLevel(text string) LogLevel {
	lower := strings.ToLower(text)

	for _, marker := range warnMarkers {
		if strings.Contains(lower, marker) {
			return LevelWarn
		}
	}
	for _, marker := range errorMarkers {
		if strings.Contains(lower, marker) {
			return LevelError
		}
	}
	return LevelInfo
}
