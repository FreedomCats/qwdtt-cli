package core

import (
	"slices"
	"strings"
	"testing"
)

func TestNormalizeWorkers(t *testing.T) {
	// Ожидания повторяют расчёт из main() апстрима: в анонимном режиме
	// значение округляется вниз до кратного размеру группы (9), но не ниже
	// одной группы; в режиме аккаунта действует квота TURN в 4 relay.
	tests := []struct {
		in        int
		anonymous int
		account   int
	}{
		{in: 0, anonymous: 9, account: 1},
		{in: 1, anonymous: 9, account: 1},
		{in: 8, anonymous: 9, account: 4},
		{in: 9, anonymous: 9, account: 4},
		{in: 10, anonymous: 9, account: 4},
		{in: 17, anonymous: 9, account: 4},
		{in: 18, anonymous: 18, account: 4},
		{in: 107, anonymous: 99, account: 4},
		{in: 108, anonymous: 108, account: 4},
		{in: 200, anonymous: 108, account: 4},
		{in: -5, anonymous: 9, account: 1},
	}

	for _, tt := range tests {
		if got := normalizeWorkers(tt.in, VkAuthAnonymous); got != tt.anonymous {
			t.Errorf("normalizeWorkers(%d, anonymous) = %d, ожидалось %d", tt.in, got, tt.anonymous)
		}
		if got := normalizeWorkers(tt.in, VkAuthAccount); got != tt.account {
			t.Errorf("normalizeWorkers(%d, account) = %d, ожидалось %d", tt.in, got, tt.account)
		}
	}
}

func TestNormalizeWorkersAlwaysWholeGroups(t *testing.T) {
	for n := -1; n <= maxWorkers+20; n++ {
		got := normalizeWorkers(n, VkAuthAnonymous)
		if got%workersPerGroup != 0 {
			t.Fatalf("normalizeWorkers(%d, anonymous) = %d — не кратно %d", n, got, workersPerGroup)
		}
		if got < workersPerGroup || got > maxWorkers {
			t.Fatalf("normalizeWorkers(%d, anonymous) = %d — вне диапазона [%d, %d]",
				n, got, workersPerGroup, maxWorkers)
		}
	}
}

func TestNormalizeFillsDefaults(t *testing.T) {
	var cfg Config
	cfg.Normalize()

	defaults := DefaultConfig()
	if cfg.DeviceID != defaults.DeviceID {
		t.Errorf("DeviceID = %q, ожидалось %q", cfg.DeviceID, defaults.DeviceID)
	}
	if cfg.Listen != defaults.Listen {
		t.Errorf("Listen = %q, ожидалось %q", cfg.Listen, defaults.Listen)
	}
	if cfg.DNS != defaults.DNS {
		t.Errorf("DNS = %q, ожидалось %q", cfg.DNS, defaults.DNS)
	}
	// Пустой режим авторизации обязан читаться как анонимный: режим аккаунта
	// режет воркеров до четырёх и требует учётных данных.
	if cfg.VkAuthMode != VkAuthAnonymous {
		t.Errorf("VkAuthMode = %q, ожидалось %q", cfg.VkAuthMode, VkAuthAnonymous)
	}
}

func TestNormalizeModes(t *testing.T) {
	tests := []struct {
		field string
		in    string
		want  string
	}{
		{"obfs", "", "audio"},
		{"obfs", "VIDEO", "video"},
		{"obfs", " video ", "video"},
		{"obfs", "чепуха", "audio"},

		{"captcha", "", "auto"},
		{"captcha", "RJS", "rjs"},
		{"captcha", "wv", "wv"},
		{"captcha", "чепуха", "auto"},

		{"vkauth", "", VkAuthAnonymous},
		{"vkauth", "ANONYMOUS", VkAuthAnonymous},
		{"vkauth", "account", VkAuthAccount},
		{"vkauth", "чепуха", VkAuthAccount},

		{"anonpath", "", "vkcalls"},
		{"anonpath", "LEGACY", "legacy"},
		{"anonpath", "чепуха", "vkcalls"},
	}

	for _, tt := range tests {
		var got string
		switch tt.field {
		case "obfs":
			got = normalizeObfsMode(tt.in)
		case "captcha":
			got = normalizeCaptchaMode(tt.in)
		case "vkauth":
			got = normalizeVkAuthMode(tt.in)
		case "anonpath":
			got = normalizeVkAnonPath(tt.in)
		}
		if got != tt.want {
			t.Errorf("%s(%q) = %q, ожидалось %q", tt.field, tt.in, got, tt.want)
		}
	}
}

func TestNormalizeHashes(t *testing.T) {
	cfg := Config{Hashes: []string{" abc ", "abc", "", "def,ghi"}}
	cfg.Normalize()

	want := []string{"abc", "def", "ghi"}
	if !slices.Equal(cfg.Hashes, want) {
		t.Errorf("Hashes = %v, ожидалось %v", cfg.Hashes, want)
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	cfg := Config{
		Peer:     " 203.0.113.10:56000 ",
		Password: "secret",
		Hashes:   []string{"aaa, bbb"},
		Workers:  107,
		ObfsMode: "VIDEO",
	}

	cfg.Normalize()
	first := cfg
	cfg.Normalize()

	if cfg.Workers != first.Workers || cfg.ObfsMode != first.ObfsMode ||
		!slices.Equal(cfg.Hashes, first.Hashes) {
		t.Errorf("повторный Normalize изменил результат: было %+v, стало %+v", first, cfg)
	}
}

func TestValidate(t *testing.T) {
	valid := func() Config {
		cfg := DefaultConfig()
		cfg.Peer = "203.0.113.10:56000"
		cfg.Password = "secret"
		cfg.Hashes = []string{"abc"}
		cfg.Normalize()
		return cfg
	}

	base := valid()
	if err := base.Validate(); err != nil {
		t.Fatalf("корректные настройки отвергнуты: %v", err)
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantSub string
	}{
		{"без peer", func(c *Config) { c.Peer = "" }, "адрес сервера"},
		{"без пароля", func(c *Config) { c.Password = "" }, "пароль подключения"},
		{"без хешей", func(c *Config) { c.Hashes = nil }, "хеша звонка"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := valid()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("ожидалась ошибка, получено nil")
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Errorf("ошибка %q не содержит %q", err.Error(), tt.wantSub)
			}
		})
	}
}

func TestValidateReportsAllProblems(t *testing.T) {
	var cfg Config
	cfg.Normalize()

	err := cfg.Validate()
	if err == nil {
		t.Fatal("ожидалась ошибка для пустых настроек")
	}
	// Пользователю полезнее увидеть все проблемы разом, чем чинить их по одной.
	for _, want := range []string{"адрес сервера", "пароль подключения", "хеша звонка"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("ошибка %q не содержит %q", err.Error(), want)
		}
	}
}
