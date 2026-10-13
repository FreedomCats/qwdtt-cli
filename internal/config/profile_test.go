package config

import (
	"strings"
	"testing"

	"qwdtt-cli/internal/core"
)

// Умолчания приходят из ядра — кроме datapath, у которого своё: socks.
func TestCoreDefaults(t *testing.T) {
	profile := parse(t, minimalFile).Profiles["home"]
	cfg := profile.Core()
	defaults := core.DefaultConfig()

	if cfg.Peer != "203.0.113.10:56000" {
		t.Errorf("Peer = %q", cfg.Peer)
	}
	if cfg.Workers != defaults.Workers || cfg.ObfsMode != defaults.ObfsMode || cfg.DNS != defaults.DNS {
		t.Errorf("умолчания разъехались с ядром: %d, %q, %q", cfg.Workers, cfg.ObfsMode, cfg.DNS)
	}
	if cfg.Socks.Listen != defaults.Socks.Listen {
		t.Errorf("socks.listen = %q, ожидалось %q", cfg.Socks.Listen, defaults.Socks.Listen)
	}
	if cfg.TurnTCP {
		t.Error("TurnTCP включён без turn_transport = \"tcp\"")
	}
	if cfg.NoDTLS {
		t.Error("DTLS выключен, хотя в профиле про него не сказано")
	}
	if cfg.Mode != core.ModeSocks {
		t.Errorf("Mode = %q, ожидался %q: режим без прав администратора", cfg.Mode, core.ModeSocks)
	}
}

// Секреты Core не подставляет: ссылки разворачивает вызывающий (задача C2).
func TestCoreLeavesSecretsEmpty(t *testing.T) {
	cfg := parse(t, fullFile).Profiles["home"].Core()

	if cfg.Password != "" || len(cfg.Hashes) != 0 {
		t.Errorf("Core подставил секреты: пароль %q, хеши %v", cfg.Password, cfg.Hashes)
	}
	// Ровно поэтому настройки после Core ещё не проходят проверку ядра.
	if err := cfg.Validate(); err == nil {
		t.Error("настройки без секретов прошли core.Config.Validate")
	}
}

func TestCoreMapping(t *testing.T) {
	cfg := parse(t, fullFile).Profiles["home"].Core()

	if cfg.Workers != 18 || cfg.ObfsMode != "video" || cfg.DNS != "doh-yandex" {
		t.Errorf("workers/obfs/dns = %d, %q, %q", cfg.Workers, cfg.ObfsMode, cfg.DNS)
	}
	if !cfg.TurnTCP {
		t.Error("turn_transport = \"tcp\" не включил TurnTCP")
	}
	if !cfg.NoDTLS {
		t.Error("dtls = false не выключил DTLS")
	}
	if cfg.Socks.Listen != "127.0.0.1:1081" {
		t.Errorf("socks.listen = %q", cfg.Socks.Listen)
	}
}

// datapath = external означает режим ядра vpn: конфиг уходит наружу,
// а туннель поднимает штатный WireGuard.
func TestCoreExternalDatapath(t *testing.T) {
	cfg := parse(t, fullFile).Profiles["work"].Core()

	if cfg.Mode != core.ModeVPN {
		t.Errorf("Mode = %q, ожидался %q", cfg.Mode, core.ModeVPN)
	}
}

func TestProfileByName(t *testing.T) {
	file := parse(t, fullFile)

	profile, err := file.Profile("work")
	if err != nil {
		t.Fatalf("Profile(work): %v", err)
	}
	if profile.Name != "work" {
		t.Errorf("выбран профиль %q", profile.Name)
	}
}

func TestProfileDefault(t *testing.T) {
	file := parse(t, fullFile)

	profile, err := file.Profile("")
	if err != nil {
		t.Fatalf("Profile(\"\"): %v", err)
	}
	if profile.Name != "home" {
		t.Errorf("по умолчанию выбран %q, ожидался home", profile.Name)
	}
}

// Единственный профиль и есть профиль по умолчанию: заставлять писать
// default_profile, когда выбирать не из чего, незачем.
func TestProfileSingleIsDefault(t *testing.T) {
	profile, err := parse(t, minimalFile).Profile("")
	if err != nil {
		t.Fatalf("Profile(\"\"): %v", err)
	}
	if profile.Name != "home" {
		t.Errorf("выбран %q", profile.Name)
	}
}

func TestProfileAmbiguousDefault(t *testing.T) {
	file := parse(t, strings.Replace(fullFile, `default_profile = "home"`, "", 1))

	_, err := file.Profile("")
	if err == nil {
		t.Fatal("профиль выбран, хотя их два и default_profile не задан")
	}
	for _, want := range []string{"default_profile", "home", "work"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в сообщении нет %q: %v", want, err)
		}
	}
}

// Промах с именем — почти всегда опечатка, поэтому в сообщении список имён:
// иначе за ним придётся идти в файл.
func TestProfileUnknownName(t *testing.T) {
	_, err := parse(t, fullFile).Profile("hom")
	if err == nil {
		t.Fatal("несуществующий профиль найден")
	}
	for _, want := range []string{"hom", "home", "work"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("в сообщении нет %q: %v", want, err)
		}
	}
}

func TestProfileEmptyFile(t *testing.T) {
	file := parse(t, "\n")

	if _, err := file.Profile(""); err == nil {
		t.Error("пустой файл отдал профиль по умолчанию")
	}
	if _, err := file.Profile("home"); err == nil {
		t.Error("пустой файл отдал профиль по имени")
	}
}
