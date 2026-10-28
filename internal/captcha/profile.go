// profile.go — временные профили браузеров капчи: окна WebView2 и браузера
// под управлением по CDP. Профиль у каждой капчи свой и пустой — это замена
// clearCaptchaCookies() Android: чистая сессия по построению.
package captcha

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/FreedomCats/qwdtt-cli/internal/app"
)

// profilePrefix — начало имени временного каталога профиля во временном
// каталоге системы.
const profilePrefix = app.Name + "-captcha-"

// staleProfileAge — каталог профиля старше этого считается брошенным и
// убирается при следующем запуске. Окно капчи живёт не дольше трёх минут,
// так что час — с большим запасом: чужой живой профиль под руку не попадёт.
const staleProfileAge = time.Hour

// removeProfile удаляет временный профиль.
//
// Сразу может не выйти: на Windows процессы msedgewebview2.exe держат файлы
// каталога ещё какое-то время после закрытия окна. Поэтому несколько попыток,
// а при неудаче каталог остаётся. Это не ошибка — секретов в нём нет, а
// следующий запуск уберёт его по возрасту (sweepStaleProfiles).
func removeProfile(dir string) {
	for attempt := 0; attempt < 40; attempt++ {
		if err := os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// cleanupWait — сколько Close диспетчера ждёт уборки окон капчи, прежде чем
// отпустить выход. На стенде WebView2 после отмены окно закрывается и профиль
// удаляется примерно за три десятых секунды (TestWebView2CloseWaitsCleanup).
// Срок больше browserStop (5 с, Linux) с запасом: зависший браузер убивается
// только по его истечении, и ожидание, кончившееся тогда же, отпустило бы
// выход раньше SIGKILL — браузер в своей группе процессов пережил бы
// qwdtt-cli вместе с профилем. removeProfile пробует до десяти секунд, но так
// долго выход не держим: не убранный профиль — не беда (removeProfile).
const cleanupWait = 7 * time.Second

// cleanups — уборка окон капчи, которая идёт уже после Solve: окно или
// браузер закрываются, прокси останавливается, профиль удаляется. Solve ради
// неё не задерживается — токен ядру нужен сейчас, — а выход из qwdtt-cli её
// дожидается (Dispatcher.Close): иначе процесс кончался раньше, чем
// msedgewebview2.exe отпускал файлы, и профиль оставался во временном
// каталоге. Учёт общий на пакет, а не у решателя: CLI оборачивает решатели
// (kill-switch, прокси, запасной консольный), и диспетчер их не видит.
var cleanups cleanupSet

// cleanupSet — счётчик незаконченных уборок с ожиданием, у которого есть
// срок. sync.WaitGroup не годится: срока у его Wait нет, а Add после Wait,
// брошенного по сроку, ему запрещён.
type cleanupSet struct {
	mu   sync.Mutex
	n    int
	idle chan struct{} // закрывается, когда n доходит до нуля
}

// add отмечает начало уборки и возвращает, чем отметить её конец. Звать —
// до того, как Solve вернётся: иначе Close может её не застать.
func (c *cleanupSet) add() (done func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.n == 0 {
		c.idle = make(chan struct{})
	}
	c.n++
	return sync.OnceFunc(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.n--
		if c.n == 0 {
			close(c.idle)
		}
	})
}

// wait ждёт, пока незаконченных уборок не останется, но не дольше limit.
// false — не дождался.
func (c *cleanupSet) wait(limit time.Duration) bool {
	c.mu.Lock()
	if c.n == 0 {
		c.mu.Unlock()
		return true
	}
	idle := c.idle
	c.mu.Unlock()

	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case <-idle:
		return true
	case <-timer.C:
		return false
	}
}

// sweepStaleProfiles убирает профили, брошенные прошлыми запусками: те, что
// removeProfile не успел удалить до конца процесса.
func sweepStaleProfiles() {
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), profilePrefix+"*"))
	for _, dir := range matches {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() || time.Since(info.ModTime()) < staleProfileAge {
			continue
		}
		_ = os.RemoveAll(dir)
	}
}
