package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/FreedomCats/qwdtt-cli/internal/sudo"
)

// Path возвращает путь к profiles.toml по правилам платформы:
// %APPDATA%\qwdtt-cli\profiles.toml на Windows и
// $XDG_CONFIG_HOME/qwdtt-cli/profiles.toml (по умолчанию ~/.config) на прочих.
// Под sudo — ~/.config/qwdtt-cli того, кто вызвал sudo, а не root.
//
// os.UserConfigDir здесь не годится: на macOS он даёт ~/Library/Application
// Support, а клиент консольный, и его настройки логичнее держать там же, где
// у остальных консольных программ.
func Path() (string, error) {
	dir, err := appDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// appDir — каталог настроек: каталог платформы плюс AppDir. В нём лежат
// profiles.toml и хранилище секретов (secret.go), поэтому путь считается
// в одном месте.
func appDir() (string, error) {
	invoker, err := sudo.Invoker()
	if err != nil {
		return "", err
	}
	dir, err := configDir(runtime.GOOS, os.Getenv, invoker)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, AppDir), nil
}

// configDir вынесен отдельно от Path и берёт платформу с окружением
// аргументами: иначе ветку другой системы нечем было бы проверить.
//
// invoker — кто вызвал sudo (nil — не через sudo). Окружение под sudo — уже
// root'а: HOME=/root, а XDG_CONFIG_HOME, если и дожил, может быть чьим угодно.
// Поэтому берётся домашний каталог вызвавшего из базы пользователей, а его
// собственный XDG_CONFIG_HOME sudo не передаёт, и узнать его неоткуда.
func configDir(goos string, env func(string) string, invoker *sudo.User) (string, error) {
	if goos == "windows" {
		if appData := strings.TrimSpace(env("APPDATA")); appData != "" {
			return appData, nil
		}
		return "", errors.New("не задана переменная окружения APPDATA: непонятно, где искать настройки")
	}

	if invoker != nil {
		return filepath.Join(invoker.HomeDir, ".config"), nil
	}

	if xdg := strings.TrimSpace(env("XDG_CONFIG_HOME")); xdg != "" {
		return xdg, nil
	}
	if home := strings.TrimSpace(env("HOME")); home != "" {
		return filepath.Join(home, ".config"), nil
	}
	return "", errors.New("не заданы переменные окружения XDG_CONFIG_HOME и HOME: непонятно, где искать настройки")
}

// Load читает и разбирает файл настроек.
//
// Отсутствие файла — ошибка, обёрнутая вокруг os.ErrNotExist: вызывающий
// отличает «настроек ещё нет» от «настройки испорчены» через errors.Is,
// а не разбором текста.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("файл настроек %s: %w", path, err)
	}
	return Parse(data, path)
}

// Parse разбирает содержимое файла настроек и проверяет его.
//
// source попадает в сообщения об ошибках — это путь к файлу или любое другое
// название источника, по которому пользователь поймёт, что чинить.
func Parse(data []byte, source string) (*File, error) {
	var file File

	md, err := toml.Decode(string(data), &file)
	if err != nil {
		var perr toml.ParseError
		if errors.As(err, &perr) {
			// ErrorWithPosition показывает строку с указателем на место —
			// это гораздо понятнее, чем «ошибка на строке 12».
			return nil, fmt.Errorf("%s: %s", source, strings.TrimRight(perr.ErrorWithPosition(), "\n"))
		}
		return nil, fmt.Errorf("%s: %w", source, err)
	}

	for name, profile := range file.Profiles {
		profile.Name = name
	}

	problems := validateFile(&file)
	problems = append(problems, unknownKeyProblems(md)...)
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s:\n  %s", source, strings.Join(problems, "\n  "))
	}

	return &file, nil
}

// unknownKeyProblems сообщает о ключах, которых в формате нет.
//
// Молча пропускать их нельзя: опечатка в имени ключа означает, что настройка
// не применилась, а узнать об этом можно было бы только по странному поведению
// программы. Отдельно ловятся попытки положить секрет прямо в файл — ключ
// назван почти правильно, и без подсказки причина отказа выглядит придиркой.
func unknownKeyProblems(md toml.MetaData) []string {
	var problems []string
	for _, key := range md.Undecoded() {
		full := key.String()
		switch last(key) {
		case "password", "vk_hashes", "hashes":
			problems = append(problems, fmt.Sprintf(
				"неизвестный ключ %s: секреты в файле не хранятся — запишите значение через `qwdtt-cli profile set`, "+
					"а в профиле оставьте ссылку password_ref или vk_hashes_ref", full))
		default:
			problems = append(problems, fmt.Sprintf("неизвестный ключ %s", full))
		}
	}
	return problems
}

func last(key toml.Key) string {
	if len(key) == 0 {
		return ""
	}
	return key[len(key)-1]
}
