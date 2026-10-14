package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func sampleFile() *File {
	return &File{
		DefaultProfile: "home",
		Profiles: map[string]*Profile{
			"home": {
				Name:          "home",
				Peer:          "203.0.113.10:56000",
				PasswordRef:   "dpapi:8f2a1c",
				VkHashesRef:   "dpapi:1c9bd4",
				Workers:       18,
				Obfs:          "audio",
				TurnTransport: "udp",
				DTLS:          boolPtr(false),
				DNS:           "doh-yandex",
				Datapath:      DatapathSocks,
				KillSwitch:    boolPtr(false),
				Socks:         SocksSection{Listen: "127.0.0.1:1080"},
			},
			"work": {
				Name:        "work",
				Peer:        "198.51.100.7:56000",
				PasswordRef: "dpapi:5d0e77",
				VkHashesRef: "dpapi:aa31b9",
				Datapath:    DatapathExternal,
			},
		},
	}
}

// Записанный файл читается обратно тем же разбором и даёт то же самое.
// Это и есть главное требование к Save: писать можно только то, что мы
// сами согласны прочитать.
func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, sampleFile()); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.DefaultProfile != "home" {
		t.Errorf("default_profile = %q", got.DefaultProfile)
	}
	if len(got.Profiles) != 2 {
		t.Fatalf("профилей %d, ожидалось 2: %v", len(got.Profiles), got.Names())
	}

	home := got.Profiles["home"]
	want := sampleFile().Profiles["home"]
	switch {
	case home.Peer != want.Peer:
		t.Errorf("peer = %q", home.Peer)
	case home.PasswordRef != want.PasswordRef || home.VkHashesRef != want.VkHashesRef:
		t.Errorf("ссылки на секреты потерялись: %+v", home)
	case home.Workers != want.Workers:
		t.Errorf("workers = %d", home.Workers)
	case home.Obfs != want.Obfs || home.TurnTransport != want.TurnTransport || home.DNS != want.DNS:
		t.Errorf("настройки транспорта потерялись: %+v", home)
	case home.Datapath != want.Datapath:
		t.Errorf("datapath = %q", home.Datapath)
	case home.Socks.Listen != want.Socks.Listen:
		t.Errorf("socks.listen = %q", home.Socks.Listen)
	}

	// Пропущенное dtls и dtls = false — разные вещи, и запись обязана
	// их различать.
	if home.DTLS == nil || *home.DTLS {
		t.Errorf("dtls = %v, ожидалось false", home.DTLS)
	}
	if got.Profiles["work"].DTLS != nil {
		t.Errorf("у work появился dtls, которого не задавали: %v", *got.Profiles["work"].DTLS)
	}
	// С kill_switch так же: пропуск — «включён», и вписывать его незачем.
	if home.KillSwitch == nil || *home.KillSwitch {
		t.Errorf("kill_switch = %v, ожидалось false", home.KillSwitch)
	}
	if got.Profiles["work"].KillSwitch != nil {
		t.Errorf("у work появился kill_switch, которого не задавали: %v", *got.Profiles["work"].KillSwitch)
	}
}

// Пустые поля не записываются: пропуск означает «как по умолчанию», и вписать
// вместо него нынешнее умолчание значило бы заморозить его в чужом файле.
func TestSaveOmitsEmptyFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	f := &File{Profiles: map[string]*Profile{
		"bare": {
			Name:        "bare",
			Peer:        "203.0.113.10:56000",
			PasswordRef: "dpapi:8f2a1c",
			VkHashesRef: "dpapi:1c9bd4",
		},
	}}
	if err := Save(path, f); err != nil {
		t.Fatalf("Save: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range []string{"workers", "obfs", "turn_transport", "dtls", "dns", "datapath", "socks"} {
		if strings.Contains(text, key+" =") || strings.Contains(text, "["+key) {
			t.Errorf("в файле есть незаданный ключ %q:\n%s", key, text)
		}
	}
	if strings.Contains(text, "default_profile") {
		t.Errorf("записан пустой default_profile:\n%s", text)
	}
}

// Запись повторяема: тот же файл даёт те же байты. Иначе `qwdtt-cli profile set`
// перетасовывал бы профили при каждом запуске.
func TestSaveIsStable(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.toml")
	second := filepath.Join(dir, "second.toml")

	if err := Save(first, sampleFile()); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(second, loaded); err != nil {
		t.Fatal(err)
	}

	a, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Errorf("повторная запись дала другой файл:\n--- первый ---\n%s\n--- второй ---\n%s", a, b)
	}
}

// Негодные настройки не попадают в файл: писать то, что сами откажемся
// прочитать, нельзя.
func TestSaveRejectsInvalid(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	f := &File{Profiles: map[string]*Profile{
		"home": {Name: "home", Peer: "не адрес", PasswordRef: "dpapi:1", VkHashesRef: "dpapi:2"},
	}}

	if err := Save(path, f); err == nil {
		t.Fatal("негодные настройки записаны")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("файл всё-таки создан: %v", err)
	}
}

// Права 0600: в файле нет секретов, но есть адреса серверов, и лежит он рядом
// с хранилищем секретов. На Windows биты режима роли не играют.
func TestSaveFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("на Windows права файла проверяются не битами режима")
	}

	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, sampleFile()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("права %v, ожидались 0600", mode)
	}
}
