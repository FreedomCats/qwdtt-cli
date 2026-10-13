package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tempStore(t *testing.T) *store {
	t.Helper()
	return newSecretStoreAt(filepath.Join(t.TempDir(), SecretsFileName))
}

// Круг: положили — достали. Тест идёт на текущей платформе, поэтому на Windows
// проходит через настоящий DPAPI, на прочих — через файл.
func TestSecretRoundTrip(t *testing.T) {
	s := tempStore(t)

	ref, err := s.Put("home.password", "пароль-подключения")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !strings.HasPrefix(ref, secretScheme+":") {
		t.Errorf("ссылка %q не начинается со схемы %q", ref, secretScheme)
	}

	got, err := s.Get(ref)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "пароль-подключения" {
		t.Errorf("получено %q, ожидалось %q", got, "пароль-подключения")
	}
}

// Ссылка переживает перезапуск: записали одним экземпляром хранилища,
// прочитали другим поверх того же файла.
func TestSecretPersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), SecretsFileName)

	ref, err := newSecretStoreAt(path).Put("home.hashes", "a1, b2, c3")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := newSecretStoreAt(path).Get(ref)
	if err != nil {
		t.Fatalf("Get из нового экземпляра: %v", err)
	}
	if got != "a1, b2, c3" {
		t.Errorf("получено %q", got)
	}
}

// Повторный Put с тем же именем перезаписывает значение и возвращает ту же
// ссылку — иначе `tt profile set` копил бы осиротевшие записи.
func TestSecretPutReusesRefByName(t *testing.T) {
	s := tempStore(t)

	first, err := s.Put("home.password", "старый")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Put("home.password", "новый")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("ссылка сменилась при перезаписи: %q → %q", first, second)
	}

	got, err := s.Get(second)
	if err != nil {
		t.Fatal(err)
	}
	if got != "новый" {
		t.Errorf("получено %q, ожидалось «новый»", got)
	}

	data, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Secrets) != 1 {
		t.Errorf("в хранилище %d записей, ожидалась одна", len(data.Secrets))
	}
}

// Разные имена — разные ссылки, и оба значения достаются.
func TestSecretMultiple(t *testing.T) {
	s := tempStore(t)

	pref, err := s.Put("home.password", "п")
	if err != nil {
		t.Fatal(err)
	}
	href, err := s.Put("home.hashes", "х")
	if err != nil {
		t.Fatal(err)
	}
	if pref == href {
		t.Fatal("разные секреты получили одну ссылку")
	}
	if v, _ := s.Get(pref); v != "п" {
		t.Errorf("пароль = %q", v)
	}
	if v, _ := s.Get(href); v != "х" {
		t.Errorf("хеши = %q", v)
	}
}

func TestSecretGetMalformedRef(t *testing.T) {
	s := tempStore(t)
	for _, ref := range []string{"", "безсхемы", secretScheme + ":", ":идентификатор"} {
		if _, err := s.Get(ref); err == nil {
			t.Errorf("ссылка %q принята, ожидалась ошибка", ref)
		}
	}
}

func TestSecretGetUnknownID(t *testing.T) {
	s := tempStore(t)
	if _, err := s.Get(secretScheme + ":deadbeef"); err == nil {
		t.Error("несуществующий идентификатор принят")
	}
}

// Ссылка с чужой схемой (профиль с Windows открыли на unix или наоборот)
// не разворачивается молча: секрет иначе «пропал» бы без объяснения.
func TestSecretGetForeignScheme(t *testing.T) {
	s := tempStore(t)

	foreign := "file"
	if secretScheme == "file" {
		foreign = "dpapi"
	}

	_, err := s.Get(foreign + ":abc123")
	if err == nil {
		t.Fatal("чужая схема принята")
	}
	if !strings.Contains(err.Error(), foreign) {
		t.Errorf("в сообщении нет чужой схемы: %v", err)
	}
}

// На unix защита секрета — только права файла. Хранилище обязано создаваться
// с 0600. На Windows биты режима ничего не значат — там секрет защищает DPAPI.
func TestSecretFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("на Windows права файла не защищают секрет — это делает DPAPI")
	}
	s := tempStore(t)
	if _, err := s.Put("home.password", "п"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("права файла %o, ожидались 600", perm)
	}
}

// Что именно лежит в файле: на Windows — шифртекст DPAPI (значения внутри не
// видно), на unix — само значение (защита — права файла, а не шифр). Так
// проверяется, что на Windows DPAPI действительно применён.
func TestSecretStorageForm(t *testing.T) {
	s := tempStore(t)
	const secret = "очень-секретное-значение-42"

	if _, err := s.Put("home.password", secret); err != nil {
		t.Fatal(err)
	}
	data, err := s.read()
	if err != nil {
		t.Fatal(err)
	}

	var stored secretEntry
	for _, entry := range data.Secrets {
		stored = entry
	}
	sealed, err := base64.StdEncoding.DecodeString(stored.Data)
	if err != nil {
		t.Fatalf("data не base64: %v", err)
	}

	if runtime.GOOS == "windows" {
		if string(sealed) == secret {
			t.Error("на Windows секрет лежит открытым текстом — DPAPI не применился")
		}
	} else {
		if string(sealed) != secret {
			t.Errorf("на unix ожидалось само значение, лежит %q", string(sealed))
		}
	}
}
