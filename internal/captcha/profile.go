// profile.go — временные профили браузеров капчи: окна WebView2 и браузера
// под управлением по CDP. Профиль у каждой капчи свой и пустой — это замена
// clearCaptchaCookies() Android: чистая сессия по построению.
package captcha

import (
	"os"
	"path/filepath"
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
