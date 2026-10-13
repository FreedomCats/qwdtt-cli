package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecretsFileName — имя файла хранилища секретов. Лежит в каталоге настроек,
// рядом с profiles.toml.
const SecretsFileName = "secrets.json"

// SecretStore держит секреты — пароль подключения и хеши звонков VK — вне
// profiles.toml. В профиль попадает только ссылка вида <хранилище>:<идентификатор>,
// которую возвращает Put и разворачивает Get.
//
// Что означает «хранилище», решает платформа. На Windows это DPAPI: значение
// шифруется CryptProtectData и привязано к учётной записи, схема ссылки — dpapi.
// На прочих системах шифрования нет, защита — права 0600 на самом файле
// хранилища, схема — file. Ссылка одной платформы на другой не разворачивается:
// Get отвергает чужую схему, а не отдаёт мусор (см. secret_windows.go,
// secret_unix.go).
type SecretStore interface {
	// Put кладёт значение под именем name и возвращает ссылку на него.
	// Повторный вызов с тем же name перезаписывает прежнее значение и
	// возвращает ту же ссылку — чтобы `tt profile set` не плодил осиротевшие
	// записи при каждом запуске.
	Put(name, value string) (ref string, err error)
	// Get разворачивает ссылку обратно в значение.
	Get(ref string) (value string, err error)
	// Delete убирает значение из хранилища. Ссылка, которой в хранилище нет,
	// ошибкой не считается: `tt profile rm` должен доводить удаление до конца,
	// даже если запись пропала раньше.
	Delete(ref string) error
}

// NewSecretStore открывает хранилище в каталоге настроек платформы — там же,
// где Path ищет profiles.toml.
func NewSecretStore() (SecretStore, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}
	return NewSecretStoreFor(path), nil
}

// NewSecretStoreFor открывает хранилище рядом с указанным файлом настроек.
//
// Нужен команде с флагом -config: секреты обязаны лежать в том же каталоге,
// что и профили, которые на них ссылаются, иначе перенесённый каталог
// настроек окажется наполовину рабочим.
func NewSecretStoreFor(configPath string) SecretStore {
	return newSecretStoreAt(filepath.Join(filepath.Dir(configPath), SecretsFileName))
}

// newSecretStoreAt открывает хранилище по явному пути. Нужен тестам и на случай,
// если появится флаг с каталогом настроек; публичного двойника пока нет —
// добавится, когда понадобится.
func newSecretStoreAt(path string) *store {
	return &store{path: path}
}

type store struct {
	path string
}

// secretsData — формат файла хранилища. Записи адресуются коротким
// идентификатором, попадающим в ссылку; name — человекочитаемая метка для
// `tt profile show` и ключ, по которому Put находит запись для перезаписи.
type secretsData struct {
	Secrets map[string]secretEntry `json:"secrets"`
}

type secretEntry struct {
	Name string `json:"name"`
	// Data — base64 от protect(value). На Windows это DPAPI-шифртекст,
	// на прочих системах — само значение: там защищает файл, а не шифр.
	Data string `json:"data"`
}

func (s *store) Put(name, value string) (string, error) {
	sealed, err := protect([]byte(value))
	if err != nil {
		return "", fmt.Errorf("не удалось защитить секрет %q: %w", name, err)
	}

	data, err := s.read()
	if err != nil {
		return "", err
	}

	id := idForName(data.Secrets, name)
	if id == "" {
		if id, err = newSecretID(); err != nil {
			return "", err
		}
	}
	data.Secrets[id] = secretEntry{
		Name: name,
		Data: base64.StdEncoding.EncodeToString(sealed),
	}

	if err := s.write(data); err != nil {
		return "", err
	}
	return secretScheme + ":" + id, nil
}

func (s *store) Get(ref string) (string, error) {
	_, id, err := splitSecretRef(ref)
	if err != nil {
		return "", err
	}

	data, err := s.read()
	if err != nil {
		return "", err
	}
	entry, ok := data.Secrets[id]
	if !ok {
		return "", fmt.Errorf(
			"секрет %q не найден в хранилище %s — возможно, профиль заводили не через `tt profile set`", ref, s.path)
	}

	sealed, err := base64.StdEncoding.DecodeString(entry.Data)
	if err != nil {
		return "", fmt.Errorf("секрет %q в хранилище %s испорчен: %w", ref, s.path, err)
	}
	plain, err := unprotect(sealed)
	if err != nil {
		return "", fmt.Errorf("не удалось прочитать секрет %q (%s): %w", ref, secretStoreKind, err)
	}
	return string(plain), nil
}

func (s *store) Delete(ref string) error {
	_, id, err := splitSecretRef(ref)
	if err != nil {
		return err
	}

	data, err := s.read()
	if err != nil {
		return err
	}
	if _, ok := data.Secrets[id]; !ok {
		// Удалять нечего — значит, уже удалено. Ошибка здесь остановила бы
		// `tt profile rm` на полпути, оставив профиль в файле.
		return nil
	}
	delete(data.Secrets, id)
	return s.write(data)
}

// splitSecretRef разбирает ссылку и убеждается, что она из здешнего хранилища.
//
// Чужая схема — не мусор, а секрет с другой платформы: DPAPI-шифртекст на unix
// всё равно не прочитать, и честнее сказать об этом, чем молча отдать пустоту.
func splitSecretRef(ref string) (scheme, id string, err error) {
	scheme, id, ok := strings.Cut(strings.TrimSpace(ref), ":")
	if !ok || scheme == "" || id == "" {
		return "", "", fmt.Errorf("ссылка на секрет %q: ожидается вид <хранилище>:<идентификатор>", ref)
	}
	if scheme != secretScheme {
		return "", "", fmt.Errorf(
			"ссылка на секрет %q: хранилище %q на этой системе недоступно — здесь секреты лежат в схеме %q (%s)",
			ref, scheme, secretScheme, secretStoreKind)
	}
	return scheme, id, nil
}

// idForName ищет идентификатор записи с данным именем. Пусто — записи нет,
// Put заведёт новый идентификатор. Дубликаты имён нормальным путём не заводятся
// (их и предотвращает эта проверка); при обходе карты берётся любой из них.
func idForName(secrets map[string]secretEntry, name string) string {
	for id, entry := range secrets {
		if entry.Name == name {
			return id
		}
	}
	return ""
}

func (s *store) read() (*secretsData, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &secretsData{Secrets: map[string]secretEntry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("хранилище секретов %s: %w", s.path, err)
	}
	var data secretsData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("хранилище секретов %s повреждено: %w", s.path, err)
	}
	if data.Secrets == nil {
		data.Secrets = map[string]secretEntry{}
	}
	return &data, nil
}

func (s *store) write(data *secretsData) error {
	if err := makeDir(filepath.Dir(s.path)); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')

	// Пишем во временный файл и переименовываем: обрыв записи не должен
	// оставить хранилище наполовину записанным. Права 0600 — на unix это вся
	// защита секретов; на Windows биты режима роли не играют, значения там уже
	// зашифрованы DPAPI.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("хранилище секретов %s: %w", tmp, err)
	}
	if err := giveToInvoker(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("хранилище секретов %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("хранилище секретов %s: %w", s.path, err)
	}
	return nil
}

func newSecretID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("не удалось создать идентификатор секрета: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}
