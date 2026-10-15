package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeKeyring — связка ключей в памяти.
type fakeKeyring struct {
	values map[string]string
	labels map[string]string
	// unavailable — связки в сеансе нет; err — она есть, но операция не прошла.
	unavailable bool
	err         error
	opened      int
}

func newFakeKeyring() *fakeKeyring {
	return &fakeKeyring{values: map[string]string{}, labels: map[string]string{}}
}

func (f *fakeKeyring) open() (keyring, error) {
	f.opened++
	if f.unavailable {
		return nil, errKeyringUnavailable
	}
	return f, nil
}

func (f *fakeKeyring) Put(id, label, value string) error {
	if f.err != nil {
		return f.err
	}
	f.values[id], f.labels[id] = value, label
	return nil
}

func (f *fakeKeyring) Get(id string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	v, ok := f.values[id]
	if !ok {
		return "", errKeyringNotFound
	}
	return v, nil
}

func (f *fakeKeyring) Delete(id string) error {
	if f.err != nil {
		return f.err
	}
	if _, ok := f.values[id]; !ok {
		return errKeyringNotFound
	}
	delete(f.values, id)
	return nil
}

func (f *fakeKeyring) Close() error { return nil }

// keyringStore — хранилище с подделанной связкой поверх файла path.
func keyringStore(path string, k *fakeKeyring) *store {
	s := newSecretStoreAt(path)
	s.keyring = k.open
	return s
}

// Со связкой значение уходит в неё, а в файле остаются только имя и
// идентификатор.
func TestKeyringPut(t *testing.T) {
	path := filepath.Join(t.TempDir(), SecretsFileName)
	k := newFakeKeyring()
	s := keyringStore(path, k)

	ref, err := s.Put("home.password", "пароль-подключения")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	id, ok := strings.CutPrefix(ref, keyringScheme+":")
	if !ok {
		t.Fatalf("ссылка %q, ожидалась схема %s", ref, keyringScheme)
	}
	if k.values[id] != "пароль-подключения" || k.labels[id] != "qwdtt-cli home.password" {
		t.Errorf("в связке: %q с меткой %q", k.values[id], k.labels[id])
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "пароль") || strings.Contains(string(raw), `"data"`) {
		t.Errorf("значение попало в файл:\n%s", raw)
	}
	if !strings.Contains(string(raw), "home.password") {
		t.Errorf("в файле нет имени записи:\n%s", raw)
	}

	got, err := keyringStore(path, k).Get(ref)
	if err != nil || got != "пароль-подключения" {
		t.Errorf("Get: %q, %v", got, err)
	}
	if SecretRefKind(ref) != "связка ключей (Secret Service)" {
		t.Errorf("SecretRefKind(%q) = %q", ref, SecretRefKind(ref))
	}
}

// Секрет, записанный в файл раньше, читается по-прежнему и переезжает в
// связку при перезаписи — под тем же идентификатором, без копии в файле.
func TestKeyringMovesOnRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), SecretsFileName)
	old, err := newSecretStoreAt(path).Put("home.vk_hashes", "a1, b2")
	if err != nil {
		t.Fatal(err)
	}

	k := newFakeKeyring()
	s := keyringStore(path, k)
	if got, err := s.Get(old); err != nil || got != "a1, b2" {
		t.Fatalf("старая ссылка со связкой: %q, %v", got, err)
	}

	ref, err := s.Put("home.vk_hashes", "c3")
	if err != nil {
		t.Fatal(err)
	}
	_, oldID, _ := strings.Cut(old, ":")
	if ref != keyringScheme+":"+oldID {
		t.Errorf("ссылка после перезаписи %q, ожидалась %s:%s", ref, keyringScheme, oldID)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), `"data"`) {
		t.Errorf("в файле осталось значение:\n%s", raw)
	}

	// Профиль не успели записать с новой ссылкой — старая ведёт в связку.
	if got, err := s.Get(old); err != nil || got != "c3" {
		t.Errorf("старая ссылка после переезда: %q, %v", got, err)
	}
}

// Связки нет — файл, как раньше.
func TestKeyringUnavailableFallsBackToFile(t *testing.T) {
	k := newFakeKeyring()
	k.unavailable = true
	s := keyringStore(filepath.Join(t.TempDir(), SecretsFileName), k)

	ref, err := s.Put("home.password", "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, secretScheme+":") {
		t.Errorf("ссылка %q, ожидалась схема %s", ref, secretScheme)
	}
	if got, err := s.Get(ref); err != nil || got != "x" {
		t.Errorf("Get: %q, %v", got, err)
	}
	if !strings.HasPrefix(SecretRefKind(ref), SecretsFileName) {
		t.Errorf("SecretRefKind(%q) = %q", ref, SecretRefKind(ref))
	}
}

// Связка есть, но запись не удалась (окно разблокировки закрыли) — ошибка,
// а не тихий уход в файл: человек отказал, и молча обойти его нельзя.
func TestKeyringPutErrorIsNotFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), SecretsFileName)
	k := newFakeKeyring()
	k.err = errors.New("окно закрыто без ответа")

	if _, err := keyringStore(path, k).Put("home.password", "x"); err == nil {
		t.Fatal("Put прошёл")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("файл хранилища создан: %v", err)
	}
}

// Окно разблокировки закрыли — следующие секреты того же хранилища связку
// не открывают: второе окно на тот же отказ не появляется. Прочие ошибки
// (записи нет) не запоминаются.
func TestKeyringRefusalSticks(t *testing.T) {
	k := newFakeKeyring()
	s := keyringStore(filepath.Join(t.TempDir(), SecretsFileName), k)
	password, err := s.Put("home.password", "x")
	if err != nil {
		t.Fatal(err)
	}
	hashes, err := s.Put("home.vk_hashes", "y")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(keyringScheme + ":0123456789abcdef"); err == nil {
		t.Fatal("Get несуществующей записи прошёл")
	}

	k.err = fmt.Errorf("%w: окно закрыто без ответа", ErrKeyringLocked)
	opened := k.opened
	if _, err := s.Get(password); !errors.Is(err, ErrKeyringLocked) {
		t.Fatalf("Get: %v", err)
	}
	// Связку тем временем разблокировали — хранилище всё равно её не открывает:
	// отказ действует до конца команды.
	k.err = nil
	if _, err := s.Get(hashes); !errors.Is(err, ErrKeyringLocked) {
		t.Errorf("второй Get: %v", err)
	}
	if err := s.Delete(hashes); !errors.Is(err, ErrKeyringLocked) {
		t.Errorf("Delete: %v", err)
	}
	if k.opened != opened+1 {
		t.Errorf("связку открывали %d раз после отказа, ждали 1", k.opened-opened)
	}
}

// Удаление: из связки и из файла; записи, пропавшей из связки, — не ошибка.
func TestKeyringDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), SecretsFileName)
	k := newFakeKeyring()
	s := keyringStore(path, k)

	ref, err := s.Put("home.password", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(k.values) != 0 {
		t.Errorf("в связке осталось: %v", k.values)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "home.password") {
		t.Errorf("в файле осталась запись:\n%s", raw)
	}
	if err := s.Delete(ref); err != nil {
		t.Errorf("повторный Delete: %v", err)
	}
}

// Запись удалили из связки руками — Get объясняет, а не отдаёт пустоту.
func TestKeyringGetMissing(t *testing.T) {
	k := newFakeKeyring()
	s := keyringStore(filepath.Join(t.TempDir(), SecretsFileName), k)
	if _, err := s.Get(keyringScheme + ":0123456789abcdef"); err == nil || !strings.Contains(err.Error(), "не найден") {
		t.Errorf("Get: %v", err)
	}
}

// Ссылка на связку там, где связки нет (Windows, macOS, тесты), — чужая схема.
func TestKeyringRefWithoutKeyring(t *testing.T) {
	_, err := tempStore(t).Get(keyringScheme + ":0123456789abcdef")
	if err == nil || !strings.Contains(err.Error(), keyringScheme) {
		t.Errorf("Get: %v", err)
	}
}
