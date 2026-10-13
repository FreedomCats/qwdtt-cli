// api.go — внешний интерфейс ядра. Файл наш, из апстрима не переносится
// и при обновлении ядра не затирается (см. scripts/vendor-core.ps1).
//
// Ядро апстрима — самостоятельная программа: настройки приходят флагами,
// управление приходит строками в stdin, события уходят строками в stdout.
// Здесь всё это заменяется обычным Go API: структурой настроек (B1),
// событиями (B2), функцией Run (B4) и управлением через Control (B5).

package core

import (
	"errors"
	"fmt"
	"strings"
)

const (
	// maxWorkers — потолок числа воркеров в анонимном режиме.
	maxWorkers = 108
	// accountMaxWorkers — потолок в режиме авторизации по аккаунту VK:
	// на сессию выдаётся квота примерно в 4 TURN-relay.
	accountMaxWorkers = 4
)

// Режимы работы клиента.
const (
	ModeVPN   = "vpn"   // слушать UDP, отдавать конфиг WireGuard
	ModeSocks = "socks" // userspace WireGuard и локальный SOCKS5
)

// Режимы получения учётных данных VK.
const (
	VkAuthAnonymous = "anonymous"
	VkAuthAccount   = "account"
)

// Config — настройки запуска ядра; заменяет разбор флагов в main().
//
// Заполняется в internal/cli из профиля и аргументов командной строки.
// Перед использованием обязательно вызвать Normalize, затем Validate.
type Config struct {
	// --- обязательное ---

	// Peer — адрес VPS в виде host:port.
	Peer string
	// Password — пароль подключения. Из него же выводится WRAP-ключ
	// RTP-обфускации (см. deriveWrapKey), поэтому это полноценный секрет.
	Password string
	// Hashes — хеши звонков VK. Нормализуются и дедуплицируются в Normalize.
	Hashes []string

	// --- идентификация ---

	// DeviceID — идентификатор устройства; сервер привязывает к нему пароль.
	DeviceID string

	// --- VK ---

	// VkAuthMode — anonymous или account.
	VkAuthMode string
	// VkAnonPath — vkcalls или legacy; учитывается только при anonymous.
	VkAnonPath string
	// VkCredsFile — файл с готовыми TURN-кредами от аккаунта VK.
	VkCredsFile string
	// CaptchaMode — auto, rjs или wv.
	CaptchaMode string

	// --- транспорт ---

	// TurnHost и TurnPort переопределяют адрес TURN, выданный VK. Пусто = не менять.
	TurnHost string
	TurnPort string
	// TurnTCP — соединяться с TURN-relay по TCP вместо UDP.
	// Помогает в сетях, где душат UDP.
	TurnTCP bool
	// NoDTLS — не поднимать DTLS поверх TURN: RTP-обфускация уже даёт
	// шифрование и аутентификацию каждого пакета. Требует сервера,
	// запущенного с -listen-direct.
	NoDTLS bool
	// ObfsMode — audio или video. Различаются payload type и объёмом набивки.
	ObfsMode string
	// Workers — число воркеров. Normalize приводит его к допустимому значению.
	Workers int
	// DNS — резолвер для доменов VK: yandex, cloudflare, google,
	// doh-yandex, doh-cloudflare, doh-google, custom:IP или doh:URL.
	DNS string

	// --- локальная часть ---

	// Mode — ModeVPN или ModeSocks.
	Mode string
	// Listen — локальный UDP-адрес, куда datapath отдаёт трафик.
	Listen string
	// Socks — настройки локального SOCKS5; учитываются только при ModeSocks.
	Socks SocksConfig
}

// SocksConfig — настройки локального SOCKS5-сервера.
type SocksConfig struct {
	Listen   string
	Auth     bool
	Username string
	Password string
}

// DefaultConfig возвращает настройки со значениями по умолчанию —
// теми же, что были у флагов ядра.
func DefaultConfig() Config {
	return Config{
		DeviceID:    "unknown",
		VkAuthMode:  VkAuthAnonymous,
		VkAnonPath:  "vkcalls",
		CaptchaMode: "auto",
		ObfsMode:    "audio",
		Workers:     9,
		DNS:         "yandex",
		Mode:        ModeVPN,
		Listen:      "127.0.0.1:9000",
		Socks: SocksConfig{
			Listen: "127.0.0.1:1080",
		},
	}
}

// Normalize приводит значения к каноническому виду: подставляет умолчания
// вместо пустых полей и чинит то, что чинится однозначно. Всё, что нельзя
// исправить молча, остаётся на долю Validate.
//
// Метод идемпотентен: повторный вызов ничего не меняет.
func (c *Config) Normalize() {
	defaults := DefaultConfig()

	c.Peer = strings.TrimSpace(c.Peer)
	c.DeviceID = strings.TrimSpace(c.DeviceID)
	if c.DeviceID == "" {
		c.DeviceID = defaults.DeviceID
	}

	// Хеши прогоняем через тот же разбор, что и флаг -vk: он режет по любым
	// разделителям, снимает обрамление и убирает дубликаты.
	c.Hashes = ParseHashes(strings.Join(c.Hashes, ","))

	c.VkAuthMode = normalizeVkAuthMode(c.VkAuthMode)
	c.VkAnonPath = normalizeVkAnonPath(c.VkAnonPath)
	c.CaptchaMode = normalizeCaptchaMode(c.CaptchaMode)
	c.ObfsMode = normalizeObfsMode(c.ObfsMode)

	c.DNS = strings.TrimSpace(c.DNS)
	if c.DNS == "" {
		c.DNS = defaults.DNS
	}

	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if c.Mode == "" {
		c.Mode = defaults.Mode
	}

	c.Listen = strings.TrimSpace(c.Listen)
	if c.Listen == "" {
		c.Listen = defaults.Listen
	}

	c.Socks.Listen = strings.TrimSpace(c.Socks.Listen)
	if c.Socks.Listen == "" {
		c.Socks.Listen = defaults.Socks.Listen
	}

	c.Workers = normalizeWorkers(c.Workers, c.VkAuthMode)
}

// normalizeVkAuthMode повторяет setVkAuthMode: всё, кроме "anonymous",
// считается режимом аккаунта.
//
// Пустое значение — исключение. В ядре оно недостижимо, потому что флаг
// имеет значение по умолчанию, а здесь поле легко забыть заполнить.
// Молча включать режим аккаунта нельзя: он режет число воркеров до четырёх
// и требует учётных данных, поэтому пустое поле читается как "anonymous".
func normalizeVkAuthMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" || mode == VkAuthAnonymous {
		return VkAuthAnonymous
	}
	return VkAuthAccount
}

// normalizeVkAnonPath повторяет setVkAnonPath.
func normalizeVkAnonPath(path string) string {
	if strings.EqualFold(strings.TrimSpace(path), "legacy") {
		return "legacy"
	}
	return "vkcalls"
}

// normalizeWorkers повторяет расчёт из main() апстрима.
//
// В анонимном режиме воркеры работают группами по workersPerGroup, поэтому
// значение округляется вниз до кратного размеру группы, но не ниже одной
// группы. В режиме аккаунта групп нет — есть жёсткая квота TURN.
func normalizeWorkers(n int, vkAuthMode string) int {
	if n > maxWorkers {
		n = maxWorkers
	}

	if vkAuthMode == VkAuthAccount {
		if n > accountMaxWorkers {
			n = accountMaxWorkers
		}
		if n < 1 {
			n = 1
		}
		return n
	}

	if n < workersPerGroup {
		return workersPerGroup
	}
	return (n / workersPerGroup) * workersPerGroup
}

// Validate проверяет то, что нельзя починить молча. Вызывать после Normalize.
//
// Сообщения адресованы пользователю CLI: каждое говорит, что именно не так
// и каким значение должно быть.
func (c *Config) Validate() error {
	var problems []string

	if c.Peer == "" {
		problems = append(problems, "не задан адрес сервера (peer) в виде host:port")
	}
	if c.Password == "" {
		problems = append(problems, "не задан пароль подключения: из него выводится ключ маскировки трафика")
	}
	if len(c.Hashes) == 0 {
		problems = append(problems, "не задано ни одного хеша звонка VK")
	}

	switch c.Mode {
	case ModeVPN, ModeSocks:
	default:
		problems = append(problems, fmt.Sprintf("неизвестный режим %q: допустимы %q и %q", c.Mode, ModeVPN, ModeSocks))
	}

	if c.Mode == ModeSocks && c.Socks.Auth {
		switch {
		case c.Socks.Username == "" || c.Socks.Password == "":
			problems = append(problems, "для авторизации SOCKS5 нужны логин и пароль")
		case len(c.Socks.Username) > 255 || len(c.Socks.Password) > 255:
			problems = append(problems, "логин и пароль SOCKS5 должны быть не длиннее 255 байт")
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return errors.New("настройки подключения: " + strings.Join(problems, "; "))
}
