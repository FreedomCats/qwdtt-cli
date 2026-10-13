// open.go — открыть ссылку в браузере пользователя.
//
// Живёт здесь, а не в CLI, потому что пользуется этим только капча: ссылку
// на страницу проверки печатает и открывает именно она. Решателю функция
// передаётся полем ManualOptions.Open — запускать посторонние программы должен
// тот, кто знает, хочет ли этого пользователь.
package captcha

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
)

// OpenInBrowser открывает ссылку средствами системы.
//
// Принимаются только http и https. Проверка не формальность: строка приходит
// от VK и уезжает в командную строку чужой программы, а схемы вроде file: или
// javascript: там значат совсем не то, что мы имели в виду.
func OpenInBrowser(rawURL string) error {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("ссылка не разбирается: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("ссылку со схемой %q открывать не будем", parsed.Scheme)
	}

	name, args := openCommand(parsed.String())
	if name == "" {
		return fmt.Errorf("не знаю, чем открыть браузер на %s", runtime.GOOS)
	}
	if err := exec.Command(name, args...).Start(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// openCommand — чем открывают ссылку на этой системе.
//
// На Windows это rundll32, а не `cmd /c start`: у start первый параметр в
// кавычках означает заголовок окна, и ссылка с амперсандом — а у страницы
// капчи их несколько — разбирается им неправильно.
func openCommand(link string) (string, []string) {
	switch runtime.GOOS {
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", link}
	case "darwin":
		return "open", []string{link}
	case "linux", "freebsd", "openbsd", "netbsd":
		return "xdg-open", []string{link}
	default:
		return "", nil
	}
}
