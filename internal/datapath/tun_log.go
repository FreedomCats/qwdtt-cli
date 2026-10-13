package datapath

import (
	"log"
	"strings"
)

// captureStdLog направляет стандартный log в logf и возвращает функцию,
// которая вернёт прежний вывод.
//
// На Windows нужно ради wintun.dll: golang.zx2c4.com/wintun передаёт
// сообщения драйвера в log.Default() (wintun.go, logMessage), а тот по
// умолчанию пишет прямо в stderr — поверх статусной строки CLI. Перехват
// глобальный и живёт, пока жив адаптер. Ядро в log не пишет (у него свой
// logf), но изредка пишут библиотеки под ним — fhttp и x/net/http2 об ошибках
// протокола; их строки на это время тоже идут в logf, поэтому тег у всех
// общий — [LOG], а строки драйвера узнаются по тексту. На Linux драйвера нет,
// а библиотеки те же — перехват такой же.
func captureStdLog(logf func(string, ...any)) (restore func()) {
	out, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	log.SetOutput(stdLogWriter{logf: logf})
	log.SetFlags(0)
	log.SetPrefix("")
	return func() {
		log.SetOutput(out)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	}
}

type stdLogWriter struct {
	logf func(string, ...any)
}

func (w stdLogWriter) Write(p []byte) (int, error) {
	w.logf("[LOG] %s", strings.TrimRight(string(p), "\r\n"))
	return len(p), nil
}
