// pause.go — пауза перед закрытием окна консоли.
//
// Окно, которое открыл Проводник (двойной щелчок по tt.exe) или перезапуск
// с правами администратора, закрывается вместе с процессом: последнюю строку —
// чаще всего ошибку — не успеть прочитать. Поэтому, если процесс в своей
// консоли один, перед выходом он ждёт Enter. В консоли, открытой человеком,
// процессов минимум два (оболочка и tt), и паузы нет.
//
// Своя консоль бывает и без человека: планировщик заданий, служба, обвязка
// с перенаправленным выводом. Там Enter не нажмёт никто, и пауза повесила бы
// процесс навсегда — перезапуск после сбоя не сработал бы. Поэтому паузы нет
// и тогда, когда stdin или stderr — не консоль (строку не увидеть или на неё не
// ответить), и когда окно не может увидеть человек (ownsConsole на Windows).
package cli

import "fmt"

func pauseBeforeExit(e *env) {
	if !shouldPause(e) {
		return
	}
	fmt.Fprint(e.stderr, "\nНажмите Enter, чтобы закрыть окно.")
	_, _ = e.readLine()
}

// shouldPause — ждать ли Enter перед выходом (см. начало файла).
func shouldPause(e *env) bool {
	if e.noPause {
		return false
	}
	owns := ownsConsole
	if e.ownsConsole != nil {
		owns = e.ownsConsole
	}
	if !owns() {
		return false
	}
	_, in := fileTerminal(e.stdin)
	_, errOut := fileTerminal(e.stderr)
	return in && errOut
}
