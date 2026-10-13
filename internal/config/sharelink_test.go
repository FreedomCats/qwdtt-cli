package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Ссылка в том виде, в каком её собирает экспорт Android-клиента:
// URLEncoder.encode, пробел — «+», порт в peer закодирован.
const exportedLink = "qwdtt://config?name=%D0%94%D0%BE%D0%BC+2&peer=203.0.113.10%3A56000" +
	"&hashes=aaaaaaaaaaaaaaaa%2Cbbbbbbbbbbbbbbbb&workers=18&port=9000&pass=p%40ss+word"

func TestParseShareLinkExport(t *testing.T) {
	link, err := ParseShareLink(exportedLink)
	if err != nil {
		t.Fatal(err)
	}
	want := ShareLink{
		Name:     "Дом 2",
		Peer:     "203.0.113.10:56000",
		Password: "p@ss word",
		Hashes:   "aaaaaaaaaaaaaaaa,bbbbbbbbbbbbbbbb",
		Workers:  18,
	}
	if *link != want {
		t.Errorf("разобрано %+v, ожидалось %+v", *link, want)
	}
	if n := link.HashCount(); n != 2 {
		t.Errorf("хешей %d, ожидалось 2", n)
	}
}

func TestParseShareLinkForms(t *testing.T) {
	for _, text := range []string{
		"qwdtt:config?peer=example.org&pass=x",
		"  qwdtt://config/?peer=example.org&password=x\n",
		"QWDTT://CONFIG?peer=example.org&pass=x",
		base64.StdEncoding.EncodeToString([]byte("qwdtt://config?peer=example.org&pass=x")),
		base64.RawURLEncoding.EncodeToString([]byte("qwdtt://config?peer=example.org&pass=x")),
	} {
		link, err := ParseShareLink(text)
		if err != nil {
			t.Errorf("%q: %v", text, err)
			continue
		}
		// Без порта — порт qWDTT по умолчанию, как у Android.
		if link.Peer != "example.org:56000" || link.Password != "x" {
			t.Errorf("%q: разобрано %+v", text, *link)
		}
		if link.Workers != 0 || link.Hashes != "" {
			t.Errorf("%q: лишнее в разобранном: %+v", text, *link)
		}
	}
}

// Пробел закодирован то «+», то «%20»; хеши бывают ссылками на звонки и
// через перевод строки.
func TestParseShareLinkEncodings(t *testing.T) {
	link, err := ParseShareLink("qwdtt://config?peer=example.org:443&name=a%20b+c" +
		"&hashes=https%3A%2F%2Fvk.com%2Fcall%2Fjoin%2Faaaaaaaaaaaaaaaa%0Abbbbbbbbbbbbbbbb&workers=9")
	if err != nil {
		t.Fatal(err)
	}
	if link.Name != "a b c" {
		t.Errorf("имя %q", link.Name)
	}
	if n := link.HashCount(); n != 2 {
		t.Errorf("хешей %d, ожидалось 2", n)
	}
	// Умолчание ядра в профиль не переносится.
	if link.Workers != 0 {
		t.Errorf("workers = %d, ожидался 0", link.Workers)
	}
}

func TestParseShareLinkErrors(t *testing.T) {
	for text, want := range map[string]string{
		"":                                   "пуста",
		"https://example.org/sub":            "подписки",
		"просто текст":                       "не ссылка",
		"qwdtt://subscribe?url=x":            "не про профиль",
		"qwdtt://config?name=x&pass=secret1": "нет адреса",
		"qwdtt://config?peer=example.org:0":  "неверный адрес",
		"qwdtt://config?peer=%zz&pass=x":     "повреждена",
	} {
		_, err := ParseShareLink(text)
		if err == nil {
			t.Errorf("%q принята", text)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q: ошибка %q, ожидалось %q", text, err, want)
		}
		if strings.Contains(err.Error(), "secret1") {
			t.Errorf("%q: в ошибке пароль: %v", text, err)
		}
	}
}

func TestProfileName(t *testing.T) {
	for raw, want := range map[string]string{
		"QR Профиль":     "qr-profil",
		"Дом 2":          "dom-2",
		"Щука, ёж & чай": "shchuka-ezh-chay",
		"  test_vpn  ":   "test_vpn",
		"--x--":          "x",
		"🙂":              "",
		"":               "",
	} {
		if got := ProfileName(raw); got != want {
			t.Errorf("ProfileName(%q) = %q, ожидалось %q", raw, got, want)
		}
		if got := ProfileName(raw); got != "" {
			if err := CheckProfileName(got); err != nil {
				t.Errorf("ProfileName(%q) = %q не проходит проверку: %v", raw, got, err)
			}
		}
	}
}
