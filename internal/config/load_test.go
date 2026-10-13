package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FreedomCats/qwdtt-cli/internal/sudo"
)

// Полный файл: все поля заданы, оба профиля разбираются, значения доходят
// до полей структуры без потерь.
const fullFile = `
default_profile = "home"

[profile.home]
peer           = "203.0.113.10:56000"
password_ref   = "dpapi:8f2a1c"
vk_hashes_ref  = "dpapi:1c9bd4"
workers        = 18
obfs           = "video"
turn_transport = "tcp"
dtls           = false
dns            = "doh-yandex"
datapath       = "socks"

[profile.home.socks]
listen = "127.0.0.1:1081"

[profile.work]
peer          = "198.51.100.7:56000"
password_ref  = "dpapi:5d0e77"
vk_hashes_ref = "dpapi:aa31b9"
datapath      = "external"
`

// Минимальный файл: только обязательное. Всё остальное — умолчания.
const minimalFile = `
[profile.home]
peer          = "203.0.113.10:56000"
password_ref  = "dpapi:8f2a1c"
vk_hashes_ref = "dpapi:1c9bd4"
`

func parse(t *testing.T, text string) *File {
	t.Helper()
	file, err := Parse([]byte(text), "тест.toml")
	if err != nil {
		t.Fatalf("разбор корректного файла не удался: %v", err)
	}
	return file
}

func TestParseFullFile(t *testing.T) {
	file := parse(t, fullFile)

	if file.DefaultProfile != "home" {
		t.Errorf("default_profile = %q, ожидалось %q", file.DefaultProfile, "home")
	}
	if got := file.Names(); len(got) != 2 || got[0] != "home" || got[1] != "work" {
		t.Fatalf("профили = %v, ожидались [home work]", got)
	}

	home := file.Profiles["home"]
	if home.Name != "home" {
		t.Errorf("Name = %q: имя секции не подставлено", home.Name)
	}
	if home.Peer != "203.0.113.10:56000" {
		t.Errorf("peer = %q", home.Peer)
	}
	if home.PasswordRef != "dpapi:8f2a1c" || home.VkHashesRef != "dpapi:1c9bd4" {
		t.Errorf("ссылки на секреты = %q, %q", home.PasswordRef, home.VkHashesRef)
	}
	if home.Workers != 18 {
		t.Errorf("workers = %d, ожидалось 18", home.Workers)
	}
	if home.Obfs != "video" || home.TurnTransport != "tcp" || home.DNS != "doh-yandex" {
		t.Errorf("obfs/turn_transport/dns = %q, %q, %q", home.Obfs, home.TurnTransport, home.DNS)
	}
	if home.DTLS == nil || *home.DTLS {
		t.Errorf("dtls = %v, ожидалось false", home.DTLS)
	}
	if home.Socks.Listen != "127.0.0.1:1081" {
		t.Errorf("socks.listen = %q", home.Socks.Listen)
	}

	if file.Profiles["work"].Datapath != DatapathExternal {
		t.Errorf("datapath второго профиля = %q", file.Profiles["work"].Datapath)
	}
}

// Пропущенные поля остаются пустыми: умолчания подставляет Core, чтобы они
// не разъезжались с ядром (см. TestCoreDefaults).
func TestParseMinimalFile(t *testing.T) {
	home := parse(t, minimalFile).Profiles["home"]

	if home.Workers != 0 || home.Obfs != "" || home.DNS != "" || home.Datapath != "" {
		t.Errorf("пропущенные поля заполнены при разборе: %+v", home)
	}
	if home.DTLS != nil {
		t.Errorf("dtls = %v, ожидалось nil: «не задано» и «false» — разные вещи", *home.DTLS)
	}
}

// Пример из репозитория попадает в релизный архив, и он же — первое, что
// увидит пользователь. Он обязан разбираться и проходить проверку.
func TestExampleFileParses(t *testing.T) {
	path := filepath.Join("..", "..", "tt.example.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("не прочитан %s: %v", path, err)
	}

	file, err := Parse(data, path)
	if err != nil {
		t.Fatalf("пример не проходит разбор: %v", err)
	}
	profile, err := file.Profile("")
	if err != nil {
		t.Fatalf("в примере не выбирается профиль по умолчанию: %v", err)
	}
	if profile.Name != "home" {
		t.Errorf("профиль по умолчанию = %q, ожидался home", profile.Name)
	}
}

func TestLoadReadsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte(minimalFile), 0o600); err != nil {
		t.Fatal(err)
	}

	file, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := file.Profile("home"); err != nil {
		t.Errorf("профиль home не найден: %v", err)
	}
}

// «Настроек ещё нет» отличается от «настройки испорчены» через errors.Is,
// а не разбором текста ошибки: первое — обычное состояние до `tt profile set`.
func TestLoadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)

	_, err := Load(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ошибка = %v, ожидалась обёртка вокруг os.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("в сообщении нет пути к файлу: %v", err)
	}
}

// Ошибка синтаксиса показывает место: у TOML есть готовое сообщение
// с номером строки и указателем, и оно куда полезнее нашего пересказа.
func TestParseSyntaxError(t *testing.T) {
	_, err := Parse([]byte("[profile.home\npeer = \"x:1\"\n"), "тест.toml")
	if err == nil {
		t.Fatal("испорченный TOML разобран без ошибки")
	}
	if !strings.Contains(err.Error(), "тест.toml") {
		t.Errorf("в сообщении нет источника: %v", err)
	}
	if !strings.Contains(err.Error(), "1") {
		t.Errorf("в сообщении нет номера строки: %v", err)
	}
}

// Опечатка в имени ключа означает, что настройка не применилась. Молчать
// об этом нельзя: узнать было бы можно только по странному поведению.
func TestParseUnknownKey(t *testing.T) {
	_, err := Parse([]byte(minimalFile+"\nworkerz = 18\n"), "тест.toml")
	if err == nil {
		t.Fatal("неизвестный ключ пропущен")
	}
	if !strings.Contains(err.Error(), "workerz") {
		t.Errorf("в сообщении нет самого ключа: %v", err)
	}
}

// Секрет, положенный прямо в файл, — не просто неизвестный ключ: человек
// хотел задать пароль, и ему нужно сказать, куда тот кладётся на самом деле.
func TestParseSecretInFile(t *testing.T) {
	text := "[profile.home]\npeer = \"203.0.113.10:56000\"\npassword = \"хорошийпароль\"\n" +
		"password_ref = \"dpapi:8f2a1c\"\nvk_hashes_ref = \"dpapi:1c9bd4\"\n"

	_, err := Parse([]byte(text), "тест.toml")
	if err == nil {
		t.Fatal("ключ password пропущен")
	}
	if !strings.Contains(err.Error(), "tt profile set") {
		t.Errorf("в сообщении нет подсказки про хранилище: %v", err)
	}
	if strings.Contains(err.Error(), "хорошийпароль") {
		t.Errorf("сообщение печатает сам секрет: %v", err)
	}
}

// Значение не того типа ловит библиотека; наше дело — назвать источник.
func TestParseWrongType(t *testing.T) {
	_, err := Parse([]byte("[profile.home]\nworkers = \"восемнадцать\"\n"), "тест.toml")
	if err == nil {
		t.Fatal("строка в числовом поле принята")
	}
	if !strings.Contains(err.Error(), "тест.toml") {
		t.Errorf("в сообщении нет источника: %v", err)
	}
}

func TestConfigDir(t *testing.T) {
	underSudo := &sudo.User{Name: "user", Uid: 4242, Gid: 4242, HomeDir: "/home/user"}

	tests := []struct {
		name    string
		goos    string
		env     map[string]string
		invoker *sudo.User
		want    string
	}{
		{
			name: "windows берёт APPDATA",
			goos: "windows",
			env:  map[string]string{"APPDATA": `C:\Users\user\AppData\Roaming`},
			want: `C:\Users\user\AppData\Roaming`,
		},
		{
			name: "unix берёт XDG_CONFIG_HOME",
			goos: "linux",
			env:  map[string]string{"XDG_CONFIG_HOME": "/home/user/.config-2", "HOME": "/home/user"},
			want: "/home/user/.config-2",
		},
		{
			name: "unix без XDG откатывается на ~/.config",
			goos: "linux",
			env:  map[string]string{"HOME": "/home/user"},
			want: filepath.Join("/home/user", ".config"),
		},
		{
			// macOS консольный, и настройки лежат там же, где у остальных
			// консольных программ, а не в Library/Application Support.
			name: "macOS считается unix",
			goos: "darwin",
			env:  map[string]string{"HOME": "/Users/user"},
			want: filepath.Join("/Users/user", ".config"),
		},
		{
			// Под sudo окружение — root'а: настройки ищутся у вызвавшего.
			name:    "sudo берёт каталог вызвавшего",
			goos:    "linux",
			env:     map[string]string{"HOME": "/root"},
			invoker: underSudo,
			want:    filepath.Join("/home/user", ".config"),
		},
		{
			// XDG_CONFIG_HOME, доживший до sudo (sudo -E или настройки root),
			// вызвавшему не принадлежит — не учитывается.
			name:    "sudo не смотрит на XDG_CONFIG_HOME",
			goos:    "linux",
			env:     map[string]string{"XDG_CONFIG_HOME": "/root/.config", "HOME": "/root"},
			invoker: underSudo,
			want:    filepath.Join("/home/user", ".config"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := configDir(tt.goos, func(key string) string { return tt.env[key] }, tt.invoker)
			if err != nil {
				t.Fatalf("configDir: %v", err)
			}
			if got != tt.want {
				t.Errorf("configDir = %q, ожидалось %q", got, tt.want)
			}
		})
	}
}

func TestConfigDirWithoutEnv(t *testing.T) {
	empty := func(string) string { return "" }

	for _, goos := range []string{"windows", "linux"} {
		if _, err := configDir(goos, empty, nil); err == nil {
			t.Errorf("%s: пустое окружение принято за корректное", goos)
		}
	}
}

// Path складывает каталог платформы с именами каталога и файла.
func TestPath(t *testing.T) {
	path, err := Path()
	if err != nil {
		t.Skipf("окружение без каталога настроек: %v", err)
	}
	if filepath.Base(path) != FileName || filepath.Base(filepath.Dir(path)) != AppDir {
		t.Errorf("Path = %q, ожидался хвост %s", path, filepath.Join(AppDir, FileName))
	}
}
