package main

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var packageMain = regexp.MustCompile(`(?m)^package[ \t]+main[ \t]*$`)

// convertFile превращает файл апстрима name в файл internal/core.
func convertFile(name, text string) (string, error) {
	if override, ok := lookup(fileOverrides, name); ok {
		return override, nil
	}

	// Шаблоны замен написаны под LF: с CRLF не совпал бы ни один якорь строки.
	text = strings.ReplaceAll(text, "\r\n", "\n")

	// package main -> package core.
	//
	// Пробельные классы здесь строго горизонтальные: \s захватывает и перевод
	// строки, поэтому вариант со \s*$ съедал пустую строку после объявления
	// пакета, и gofmt считал неотформатированным всё ядро разом.
	text = packageMain.ReplaceAllString(text, "package core")

	for _, r := range replacements {
		if !strings.EqualFold(r.File, name) {
			continue
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return "", fmt.Errorf("шаблон для %s: %w", name, err)
		}
		if found := len(re.FindAllStringIndex(text, -1)); found != r.Count {
			return "", fmt.Errorf("замена в %s не сошлась: ожидалось совпадений %d, найдено %d.\n"+
				"Апстрим изменил этот код — поправьте шаблон в scripts/vendorcore/rules.go.\n"+
				"Шаблон: %s", name, r.Count, found, r.Pattern)
		}

		// Группа 1 — отступ совпавшей строки: вставка встаёт на тот же уровень.
		var replacement string
		switch {
		case r.Replace != nil:
			replacement = *r.Replace
		case r.InsertAfter != "":
			replacement = "${0}\n${1}" + r.InsertAfter
		case r.InsertBefore != "":
			replacement = "${1}" + r.InsertBefore + "\n${0}"
		default:
			return "", fmt.Errorf("правило для %s не задаёт ни Replace, ни InsertAfter, ни InsertBefore", name)
		}
		text = re.ReplaceAllString(text, replacement)
	}

	// log.Fatal останавливает процесс, а библиотека этого делать не вправе.
	// В апстриме все восемь вызовов сидели в func main() и проверяли аргументы;
	// эту работу делают теперь Config.Validate и cmd/qwdtt-cli. Если апстрим заведёт
	// новый log.Fatal, разбираться с ним нужно осознанно: молча превратить его
	// в запись в лог нельзя — код после такого вызова писали в расчёте на то,
	// что до него не дойдут.
	if containsWord(text, "log.Fatal") {
		return "", fmt.Errorf("в %s есть log.Fatal, а библиотека не вправе останавливать процесс.\n"+
			"Перенесите проверку в cmd/qwdtt-cli или опишите замену правилом в replacements "+
			"(scripts/vendorcore/rules.go)", name)
	}

	for _, pair := range logReplacements {
		text = replaceWord(text, pair[0], pair[1])
	}

	// Осиротевшие импорты — см. prunableImports. Имя пакета берётся последним
	// сегментом пути: "os/signal" даёт signal. Импорты с псевдонимом и пустые
	// (_ "…") под шаблон не попадают намеренно — в ядре их нет.
	for _, imp := range prunableImports {
		alias := imp[strings.LastIndex(imp, "/")+1:]
		if containsWord(text, alias+".") {
			continue
		}
		line := regexp.MustCompile(`(?m)^\t"` + regexp.QuoteMeta(imp) + `"\n`)
		text = line.ReplaceAllString(text, "")
	}

	return text, nil
}

// lookup — значение по имени файла без учёта регистра: так искали в
// хеш-таблицах PowerShell, и так же сравнивает имена файловая система Windows.
func lookup(m map[string]string, name string) (string, bool) {
	for k, v := range m {
		if strings.EqualFold(k, name) {
			return v, true
		}
	}
	return "", false
}

// isWordRune — буква слова для \b в регулярных выражениях .NET: буквы,
// цифры, соединители и непробельные метки, а также ZWJ и ZWNJ. В RE2 \b
// знает только ASCII, а перед «log.» в ядре бывает и кириллица.
func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Nd, r) ||
		unicode.Is(unicode.Pc, r) || r == '‌' || r == '‍'
}

// atWordStart — перед позицией i строки text граница слова: слева не буква
// слова. Сами слова здесь всегда начинаются с буквы.
func atWordStart(text string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:i])
	return !isWordRune(r)
}

// replaceWord заменяет word на repl там, где перед word граница слова, —
// как [regex]::Replace с шаблоном \bword. Граница смотрит в исходный текст,
// а не в уже заменённый.
func replaceWord(text, word, repl string) string {
	var b strings.Builder
	last := 0
	for i := 0; ; {
		j := strings.Index(text[i:], word)
		if j < 0 {
			break
		}
		i += j
		if !atWordStart(text, i) {
			i++
			continue
		}
		b.WriteString(text[last:i])
		b.WriteString(repl)
		i += len(word)
		last = i
	}
	b.WriteString(text[last:])
	return b.String()
}

// containsWord — есть ли в text слово word (ASCII) с границей слова перед
// ним, без учёта регистра: так проверял оператор -match PowerShell.
func containsWord(text, word string) bool {
	for i := range text {
		if !utf8.RuneStart(text[i]) || !atWordStart(text, i) {
			continue
		}
		if hasPrefixFold(text[i:], word) {
			return true
		}
	}
	return false
}

// hasPrefixFold — начинается ли s с ASCII-строки prefix без учёта регистра.
// Сравнение по unicode.ToLower, как у .NET: знак кельвина равен «k», а
// длинная «ſ» не равна «s».
func hasPrefixFold(s, prefix string) bool {
	for _, p := range prefix {
		if s == "" {
			return false
		}
		r, size := utf8.DecodeRuneInString(s)
		if unicode.ToLower(r) != unicode.ToLower(p) {
			return false
		}
		s = s[size:]
	}
	return true
}
