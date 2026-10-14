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

// Ссылка wdtt:// в том виде, в каком её выдаёт бот сервера: адрес, порты
// DTLS, WG и локальный, пароль, хеш. Порт сервера — порт DTLS.
func TestParseWDTTLinkBot(t *testing.T) {
	link, err := ParseShareLink("wdtt://203.0.113.10:56000:56001:9000:p@ss:aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	want := ShareLink{Peer: "203.0.113.10:56000", Password: "p@ss", Hashes: "aaaaaaaaaaaaaaaa"}
	if *link != want {
		t.Errorf("разобрано %+v, ожидалось %+v", *link, want)
	}
}

func TestParseWDTTLinkForms(t *testing.T) {
	for text, want := range map[string]ShareLink{
		// Без хешей — их спросят, как у qwdtt://config.
		"wdtt://203.0.113.10:443:56001:9000:secret": {Peer: "203.0.113.10:443", Password: "secret"},
		// Имя после «#» (PWDTT), в том числе закодированное.
		"wdtt://203.0.113.10:443:1:2:secret:aaaaaaaaaaaaaaaa,bbbbbbbbbbbbbbbb#Дом 2": {
			Name: "Дом 2", Peer: "203.0.113.10:443", Password: "secret", Hashes: "aaaaaaaaaaaaaaaa,bbbbbbbbbbbbbbbb"},
		"wdtt://203.0.113.10:443:1:2:secret#%D0%94%D0%BE%D0%BC": {
			Name: "Дом", Peer: "203.0.113.10:443", Password: "secret"},
		// Хеш — ссылка на звонок с двоеточием в «https:», как у Android.
		"wdtt://203.0.113.10:443:1:2:secret:https://vk.com/call/join/aaaaaaaaaaaaaaaa": {
			Peer: "203.0.113.10:443", Password: "secret", Hashes: "https://vk.com/call/join/aaaaaaaaaaaaaaaa"},
		// Схема без «//», регистр, IPv6 в скобках, имя узла.
		"WDTT:example.org:56000:56001:9000:secret":     {Peer: "example.org:56000", Password: "secret"},
		"wdtt://[2001:db8::1]:56000:56001:9000:secret": {Peer: "[2001:db8::1]:56000", Password: "secret"},
		// В base64, как qwdtt://config.
		base64.StdEncoding.EncodeToString([]byte("wdtt://203.0.113.10:443:1:2:secret")): {
			Peer: "203.0.113.10:443", Password: "secret"},
	} {
		link, err := ParseShareLink(text)
		if err != nil {
			t.Errorf("%q: %v", text, err)
			continue
		}
		if *link != want {
			t.Errorf("%q: разобрано %+v, ожидалось %+v", text, *link, want)
		}
	}

	link, err := ParseShareLink("wdtt://203.0.113.10:443:1:2:secret:https://vk.com/call/join/aaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if n := link.HashCount(); n != 1 {
		t.Errorf("хешей %d, ожидался 1", n)
	}
}

func TestParseWDTTLinkErrors(t *testing.T) {
	for text, want := range map[string]string{
		"wdtt://203.0.113.10:443:secret1":      "неполная",
		"wdtt://:443:1:2:secret1":              "нет адреса",
		"wdtt://203.0.113.10:0:1:2:secret1":    "неверный адрес",
		"wdtt://203.0.113.10:порт:1:2:secret1": "неверный адрес",
		"wdtt://[2001:db8::1:443:1:2:secret1":  "закрывающей",
		"wdtt-but-not://203.0.113.10:1:2:3:x":  "не ссылка",
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
