package config

import (
	"strings"
	"testing"
)

// На каждое сообщение об ошибке — свой случай. Проверяется не текст целиком,
// а то, что в нём есть: что именно неверно и каким значение должно быть.
func TestValidateProfileProblems(t *testing.T) {
	tests := []struct {
		name  string
		patch func(*Profile)
		want  []string
	}{
		{
			name:  "нет адреса сервера",
			patch: func(p *Profile) { p.Peer = "" },
			want:  []string{"peer", "host:port"},
		},
		{
			name:  "адрес без порта",
			patch: func(p *Profile) { p.Peer = "203.0.113.10" },
			want:  []string{"peer", "host:port"},
		},
		{
			name:  "порт не число",
			patch: func(p *Profile) { p.Peer = "203.0.113.10:вертикаль" },
			want:  []string{"порт", "65535"},
		},
		{
			name:  "порт за границей диапазона",
			patch: func(p *Profile) { p.Peer = "203.0.113.10:70000" },
			want:  []string{"порт", "65535"},
		},
		{
			name:  "нет ссылки на пароль",
			patch: func(p *Profile) { p.PasswordRef = "" },
			want:  []string{"password_ref", "tt profile set"},
		},
		{
			name:  "нет ссылки на хеши",
			patch: func(p *Profile) { p.VkHashesRef = "" },
			want:  []string{"vk_hashes_ref", "tt profile set"},
		},
		{
			// Пароль, написанный прямо в поле ссылки, на ссылку не похож —
			// и это единственное, чем его отличишь, не заглядывая в хранилище.
			name:  "пароль вместо ссылки",
			patch: func(p *Profile) { p.PasswordRef = "хорошийпароль" },
			want:  []string{"password_ref", "dpapi:"},
		},
		{
			name:  "ссылка без идентификатора",
			patch: func(p *Profile) { p.VkHashesRef = "dpapi:" },
			want:  []string{"vk_hashes_ref", "dpapi:"},
		},
		{
			name:  "отрицательное число воркеров",
			patch: func(p *Profile) { p.Workers = -9 },
			want:  []string{"workers", "отрицательным"},
		},
		{
			name:  "воркеров больше потолка",
			patch: func(p *Profile) { p.Workers = 200 },
			want:  []string{"workers", "108"},
		},
		{
			name:  "неизвестная маскировка",
			patch: func(p *Profile) { p.Obfs = "voice" },
			want:  []string{"obfs", `"audio"`, `"video"`},
		},
		{
			name:  "неизвестный транспорт",
			patch: func(p *Profile) { p.TurnTransport = "quic" },
			want:  []string{"turn_transport", `"udp"`, `"tcp"`},
		},
		{
			name:  "неизвестный резолвер",
			patch: func(p *Profile) { p.DNS = "opendns" },
			want:  []string{"dns", "custom:", "doh:"},
		},
		{
			name:  "custom без адресов",
			patch: func(p *Profile) { p.DNS = "custom:" },
			want:  []string{"custom:", "IP"},
		},
		{
			name:  "custom с именем вместо адреса",
			patch: func(p *Profile) { p.DNS = "custom:dns.example" },
			want:  []string{"custom:", "IP"},
		},
		{
			name:  "doh не по https",
			patch: func(p *Profile) { p.DNS = "doh:http://dns.example/dns-query" },
			want:  []string{"doh", "https"},
		},
		{
			// Режим есть в архитектуре, но не в этом этапе: сказать про Э2
			// полезнее, чем назвать значение неизвестным.
			name:  "wintun ещё не готов",
			patch: func(p *Profile) { p.Datapath = DatapathWintun },
			want:  []string{"wintun", "Э2", `"socks"`, `"external"`},
		},
		{
			name:  "неизвестный datapath",
			patch: func(p *Profile) { p.Datapath = "tun" },
			want:  []string{"datapath", `"socks"`, `"external"`},
		},
		{
			name:  "адрес SOCKS5 без порта",
			patch: func(p *Profile) { p.Socks.Listen = "127.0.0.1" },
			want:  []string{"socks.listen", "host:port"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := validFile()
			tt.patch(file.Profiles["home"])

			err := file.Validate()
			if err == nil {
				t.Fatal("проверка прошла, хотя настройки неверны")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("в сообщении нет %q: %v", want, err)
				}
			}
			if !strings.Contains(err.Error(), "home") {
				t.Errorf("в сообщении нет имени профиля: %v", err)
			}
		})
	}
}

func TestValidateAcceptsValues(t *testing.T) {
	tests := []struct {
		name  string
		patch func(*Profile)
	}{
		{"custom с одним адресом", func(p *Profile) { p.DNS = "custom:192.168.1.1" }},
		{"custom со списком и портом", func(p *Profile) { p.DNS = "custom:1.1.1.1, 8.8.8.8:5353" }},
		{"doh по https", func(p *Profile) { p.DNS = "doh:https://dns.example/dns-query" }},
		{"пресет резолвера", func(p *Profile) { p.DNS = "doh-cloudflare" }},
		{"адрес с именем узла", func(p *Profile) { p.Peer = "vpn.example:56000" }},
		{"потолок воркеров", func(p *Profile) { p.Workers = 108 }},
		{"внешний datapath", func(p *Profile) { p.Datapath = DatapathExternal }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := validFile()
			tt.patch(file.Profiles["home"])

			if err := file.Validate(); err != nil {
				t.Errorf("корректное значение отклонено: %v", err)
			}
		})
	}
}

// Ошибки собираются все разом: править файл по одной, перезапуская программу
// после каждой правки, — худший способ провести вечер.
func TestValidateReportsEveryProblem(t *testing.T) {
	file := validFile()
	home := file.Profiles["home"]
	home.Peer = ""
	home.Obfs = "voice"
	home.Datapath = "tun"

	err := file.Validate()
	if err == nil {
		t.Fatal("проверка прошла")
	}
	for _, want := range []string{"peer", "obfs", "datapath"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в сообщении нет %q: %v", want, err)
		}
	}
}

func TestValidateDefaultProfileMissing(t *testing.T) {
	file := validFile()
	file.DefaultProfile = "hom"

	err := file.Validate()
	if err == nil {
		t.Fatal("default_profile указывает на несуществующий профиль, а проверка прошла")
	}
	for _, want := range []string{"default_profile", "hom", "home"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в сообщении нет %q: %v", want, err)
		}
	}
}

// Имя профиля — аргумент командной строки, а не описание: пробелы и кириллица
// в нём означают, что набрать его будет тяжело.
func TestValidateProfileName(t *testing.T) {
	file := validFile()
	file.Profiles["домашний профиль"] = file.Profiles["home"]

	err := file.Validate()
	if err == nil {
		t.Fatal("имя с пробелом принято")
	}
	if !strings.Contains(err.Error(), "домашний профиль") {
		t.Errorf("в сообщении нет самого имени: %v", err)
	}
}

// validFile — файл, который проходит проверку. Каждый тест портит в нём
// ровно одно место, поэтому видно, что сработала именно эта проверка.
func validFile() *File {
	return &File{
		DefaultProfile: "home",
		Profiles: map[string]*Profile{
			"home": {
				Name:        "home",
				Peer:        "203.0.113.10:56000",
				PasswordRef: "dpapi:8f2a1c",
				VkHashesRef: "dpapi:1c9bd4",
				Datapath:    DatapathSocks,
			},
		},
	}
}
