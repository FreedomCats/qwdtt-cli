package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// header — шапка записанного файла. Предупреждение о потере комментариев стоит
// в самом файле не из вежливости: узнать об этом иначе можно было бы только
// потеряв их.
const header = `# Файл настроек tt. Записан командой ` + "`tt profile set`" + `.
#
# Править руками можно, но комментарии и порядок ключей при следующей записи
# не сохранятся: файл переписывается целиком.
`

// Save записывает файл настроек целиком.
//
// Комментарии и порядок ключей исходного файла не сохраняются: разбор отдаёт
// структуру, а не дерево с комментариями, и класть их обратно неоткуда.
// Переписать файл целиком — честнее, чем править его построчно регулярками
// и однажды испортить чужую разметку.
//
// Перед записью настройки проверяются: писать файл, который сами же откажемся
// читать, нельзя. Запись идёт во временный файл с последующим Rename, чтобы
// обрыв не оставил профили наполовину записанными.
func Save(path string, f *File) error {
	if err := f.Validate(); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("каталог настроек %s: %w", dir, err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(render(f)), 0o600); err != nil {
		return fmt.Errorf("файл настроек %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("файл настроек %s: %w", path, err)
	}
	return nil
}

// render собирает текст файла.
//
// Пустое поле не записывается вовсе: пропуск означает «как по умолчанию»,
// и записать вместо него нынешнее умолчание значило бы заморозить его в файле
// пользователя. Порядок ключей — тот же, что в tt.example.toml.
func render(f *File) string {
	var b strings.Builder

	b.WriteString(header)

	if def := strings.TrimSpace(f.DefaultProfile); def != "" {
		b.WriteString("\ndefault_profile = " + quote(def) + "\n")
	}

	for _, name := range f.Names() {
		p := f.Profiles[name]

		fmt.Fprintf(&b, "\n[profile.%s]\n", name)
		writeString(&b, "peer", p.Peer)
		writeString(&b, "password_ref", p.PasswordRef)
		writeString(&b, "vk_hashes_ref", p.VkHashesRef)
		writeString(&b, "datapath", p.Datapath)
		if p.Workers != 0 {
			fmt.Fprintf(&b, "workers = %d\n", p.Workers)
		}
		writeString(&b, "obfs", p.Obfs)
		writeString(&b, "turn_transport", p.TurnTransport)
		if p.DTLS != nil {
			// Пропущенное dtls и dtls = false — разные вещи: второе требует
			// сервера с -listen-direct. Поэтому пишется только заданное.
			fmt.Fprintf(&b, "dtls = %t\n", *p.DTLS)
		}
		writeString(&b, "dns", p.DNS)

		if listen := strings.TrimSpace(p.Socks.Listen); listen != "" {
			fmt.Fprintf(&b, "\n[profile.%s.socks]\n", name)
			writeString(&b, "listen", listen)
		}
	}

	return b.String()
}

func writeString(b *strings.Builder, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.WriteString(key + " = " + quote(value) + "\n")
}

// quote заключает значение в кавычки по правилам TOML.
//
// strconv.Quote годится потому, что писать сюда попадают только проверенные
// значения: адреса host:port, ссылки вида <хранилище>:<идентификатор> и слова
// из закрытых списков. Экранирование Go и TOML на таком наборе совпадает.
func quote(value string) string {
	return strconv.Quote(value)
}
