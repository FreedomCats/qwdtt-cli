// sharelink.go — профиль из ссылки qwdtt://config?… (plan-e5.md, I8).
//
// Такую ссылку (и QR-код с ней) выдают Android-клиент qWDTT («Экспорт
// профиля») и бот сервера. В ней всё, что нужно для подключения:
//
//	qwdtt://config?name=<имя>&peer=<host:port>&hashes=<хеши>&workers=<N>&port=<порт>&pass=<пароль>
//
// Значения закодированы URLEncoder'ом Java: UTF-8, пробел — «+». Разбор
// повторяет parseQwdttUri апстрима: принимается и qwdtt:config?… без «//»,
// ссылка в base64, порт сервера по умолчанию дописывается, неизвестные
// параметры пропускаются.
//
// В ссылке пароль и хеши — она сама секрет. Принимать её аргументом командной
// строки нельзя по той же причине, что и пароль (см. internal/cli/profile.go).
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/FreedomCats/qwdtt-cli/internal/core"
)

// ShareLink — то, что прочитано из ссылки.
type ShareLink struct {
	// Name — имя профиля, как его назвали в приложении: бывают кириллица и
	// пробелы. Имя для нашего файла — ProfileName.
	Name string
	// Peer — адрес сервера, с портом.
	Peer string
	// Password — пароль подключения. Пусто — в ссылке его не было.
	Password string
	// Hashes — хеши звонков VK строкой, как в ссылке: через запятую, перевод
	// строки, бывают ссылки на звонки целиком. Пусто — в ссылке хешей нет
	// (у Android это «общие хеши» приложения, у нас их нет).
	Hashes string
	// Workers — число воркеров, если оно в ссылке есть и отличается от
	// умолчания ядра; иначе 0.
	Workers int
}

// ParseShareLink разбирает ссылку qwdtt://config. Сообщения об ошибках не
// повторяют саму ссылку: в ней пароль.
func ParseShareLink(text string) (*ShareLink, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("ссылка пуста")
	}

	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return nil, errors.New("это адрес в интернете, а не ссылка qwdtt://config; подписки не поддерживаются")
	}
	if !strings.HasPrefix(lower, "qwdtt:") {
		decoded, ok := decodeBase64(text)
		if !ok || !strings.HasPrefix(strings.ToLower(decoded), "qwdtt:") {
			return nil, errors.New("это не ссылка qwdtt://config: она начинается с qwdtt://")
		}
		text = decoded
	}

	// Схему и «//» снимаем руками: url.Parse поняла бы qwdtt:config?… как
	// непрозрачную ссылку, а qwdtt://config — как ссылку с хостом config,
	// и обе формы пришлось бы разбирать по-разному.
	rest := strings.TrimPrefix(text[len("qwdtt:"):], "//")
	kind, query, _ := strings.Cut(rest, "?")
	if !strings.EqualFold(strings.TrimSuffix(kind, "/"), "config") {
		return nil, fmt.Errorf("ссылка qwdtt:%s не про профиль: ожидалась qwdtt://config?…", safeKind(kind))
	}

	values, err := url.ParseQuery(query)
	if err != nil {
		return nil, errors.New("ссылка повреждена: параметры не разбираются; скопируйте её целиком ещё раз")
	}

	link := &ShareLink{
		Name:     strings.TrimSpace(values.Get("name")),
		Password: values.Get("pass"),
		Hashes:   strings.TrimSpace(values.Get("hashes")),
	}
	if link.Password == "" {
		link.Password = values.Get("password")
	}

	peer := strings.TrimSpace(values.Get("peer"))
	if peer == "" {
		return nil, errors.New("в ссылке нет адреса сервера (peer)")
	}
	link.Peer = PeerWithPort(peer)
	if err := CheckPeer(link.Peer); err != nil {
		return nil, fmt.Errorf("в ссылке неверный адрес сервера: %w", err)
	}

	// Неверное число воркеров — не повод отвергать ссылку: Android в таком
	// случае берёт умолчание, и мы тоже.
	if n, err := strconv.Atoi(strings.TrimSpace(values.Get("workers"))); err == nil && n > 0 &&
		n != core.DefaultConfig().Workers {
		link.Workers = min(n, maxWorkers)
	}

	return link, nil
}

// HashCount — сколько хешей разобрано из ссылки.
func (l *ShareLink) HashCount() int {
	return len(core.ParseHashes(l.Hashes))
}

// decodeBase64 пробует base64 во всех ходовых видах: с дополнением и без,
// обычный и для адресов. Переводы строк внутри (так переносят длинные
// строки) не мешают.
func decodeBase64(text string) (string, bool) {
	text = strings.Join(strings.Fields(text), "")
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if raw, err := enc.DecodeString(text); err == nil {
			return strings.TrimSpace(string(raw)), true
		}
	}
	return "", false
}

// safeKind — то, что стоит после qwdtt:, для сообщения: только начало и без
// параметров, чтобы в ошибку не попало ничего секретного.
func safeKind(kind string) string {
	if r := []rune(kind); len(r) > 20 {
		return string(r[:20]) + "…"
	}
	return kind
}

// ProfileName превращает имя из ссылки в имя профиля qwdtt-cli: латиница, цифры,
// дефис и подчёркивание (см. profileNameProblem). Кириллица
// транслитерируется, пробелы и прочие разделители становятся дефисом, всё
// остальное выбрасывается. Пусто — из имени ничего не осталось.
func ProfileName(raw string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
			dash = false
		case translit[r] != "":
			b.WriteString(translit[r])
			dash = false
		case r == '-' || unicode.IsSpace(r) || unicode.IsPunct(r):
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// translit — кириллица в латиницу по-простому, как пишут в адресах и именах
// файлов. Мягкий и твёрдый знаки пропадают.
var translit = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "e", 'ж': "zh",
	'з': "z", 'и': "i", 'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o",
	'п': "p", 'р': "r", 'с': "s", 'т': "t", 'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts",
	'ч': "ch", 'ш': "sh", 'щ': "shch", 'ы': "y", 'э': "e", 'ю': "yu", 'я': "ya",
	'і': "i", 'ї': "yi", 'є': "ye", 'ґ': "g", 'ў': "u",
}
