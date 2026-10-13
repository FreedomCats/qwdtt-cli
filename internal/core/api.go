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
	"strings"
	"sync/atomic"
)

const (
	// maxWorkers — потолок числа воркеров в анонимном режиме.
	maxWorkers = 108
	// accountMaxWorkers — потолок в режиме авторизации по аккаунту VK:
	// на сессию выдаётся квота примерно в 4 TURN-relay.
	accountMaxWorkers = 4
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

	// Listen — локальный UDP-адрес, куда datapath отдаёт трафик.
	//
	// Что именно подаёт в него трафик — userspace WireGuard с SOCKS5, штатный
	// wireguard.exe или свой адаптер — ядра не касается: оно только принимает
	// UDP и отдаёт наружу конфиг событием EventConfig. Выбор и настройки
	// datapath живут в internal/datapath (architecture.md §04).
	Listen string
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
		Listen:      "127.0.0.1:9000",
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

	c.Listen = strings.TrimSpace(c.Listen)
	if c.Listen == "" {
		c.Listen = defaults.Listen
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

	if len(problems) == 0 {
		return nil
	}
	return errors.New("настройки подключения: " + strings.Join(problems, "; "))
}

// ─────────────────────────── управление ───────────────────────────

// Control — управление уже работающим ядром; заменяет команды stdin.
//
// Апстрим читал из stdin пять команд: PAUSE, RESUME, STOP, CAPTCHA_RESULT| и
// TURN_CREDS|. STOP здесь нет намеренно — его роль играет отмена ctx, переданного
// в Run. Остальные четыре стали методами.
//
// Control создаётся до запуска и передаётся в Run четвёртым аргументом. Так, а не
// возвратом из Run: Run возвращается вместе с туннелем, то есть за ручкой
// управления было бы поздно приходить. И не полем Config: Config — настройки,
// его копируют, приводят к канону и в задаче C1 будут читать из профиля, а
// Control — живая ручка с состоянием, общим у нескольких горутин.
//
// Все методы безопасны для параллельного вызова и не блокируются: ни один из них
// не ждёт, пока ядро прочитает переданное. Вызов на nil-ручке — не падение, а
// ничего: Run без Control работает, просто управлять им нечем.
//
// Один Control — один Run. Ограничение идёт не от него, а от ядра: канал капчи и
// креды VK живут пакетными переменными, поэтому и Run в процессе может быть
// только один (см. applyGlobals).
type Control struct {
	// pause читают все воркеры разом через atomic.LoadInt32, поэтому Run
	// раздаёт им указатель на это поле, а не значение.
	pause int32
}

// NewControl возвращает ручку управления для будущего запуска.
//
// Пауза, выставленная до Run, действует с самого старта: группа проверяет флаг
// прежде, чем идти за кредами.
func NewControl() *Control { return &Control{} }

// Pause придерживает группы воркеров, которые ещё не пошли за кредами VK.
//
// Уже поднятый туннель пауза не рвёт и трафик через него не останавливает —
// в апстриме эта команда приходила от Android при уходе в doze-режим, где
// незачем начинать новые сессии, а старым никто не мешает. Обещать большее
// было бы неправдой: флаг проверяется в одном месте, перед запросом кредов
// (group.go).
//
// Вызов повторной паузы ничего не меняет.
func (c *Control) Pause() {
	if c == nil {
		return
	}
	atomic.StoreInt32(&c.pause, 1)
}

// Resume снимает паузу: придержанные группы пойдут за кредами в течение секунды
// (интервал проверки флага). Снятие непоставленной паузы ничего не меняет.
func (c *Control) Resume() {
	if c == nil {
		return
	}
	atomic.StoreInt32(&c.pause, 0)
}

// pauseFlag отдаёт Run указатель, который тот раздаёт воркерам. Указатель, а не
// значение: флаг раздаётся один раз при старте групп и дальше только читается.
func (c *Control) pauseFlag() *int32 { return &c.pause }

// SubmitCaptcha отдаёт ядру токен решённой капчи — ответ на EventCaptchaRequired.
//
// Пустой токен означает «решить не удалось»: ядро засчитает попытку провальной
// и пойдёт дальше своей цепочкой (captchaPlan) или сдастся. Молчание тоже
// работает — ядро ждёт ответа с таймаутом, — но стоит минуты ожидания.
//
// Токен, которого никто не ждёт, остаётся в канале и будет выброшен перед
// следующим запросом капчи. Так же вёл себя и апстрим: перед каждым ожиданием
// он чистит канал от несвежих ответов.
func (c *Control) SubmitCaptcha(token string) {
	if c == nil {
		return
	}
	if strings.TrimSpace(token) == "" {
		token = "error:cancelled"
	}
	drainCaptchaResult()
	select {
	case CaptchaResultChan <- token:
	default:
	}
}

// TurnCreds — учётные данные TURN, добытые живым аккаунтом VK. Нужны только
// в режиме VkAuthAccount, где ядро само их получить не может.
type TurnCreds struct {
	// Link — ссылка на звонок, для которого креды добыты; та же, что пришла
	// в EventVkAuthRequired. Под ней ядро их и запомнит.
	Link string
	// Username и Password — учётные данные TURN-relay.
	Username string
	Password string
	// URLs — адреса relay в виде turn:host:port или host:port.
	URLs []string
}

// SubmitTurnCreds отдаёт ядру TURN-креды — ответ на EventVkAuthRequired.
//
// Креды запоминаются под ссылкой на звонок и живут accountCredsLifetime, поэтому
// отданные заранее пригодятся и той группе, которая спросит их позже.
//
// Неполные креды (нет ссылки, логина, пароля или адресов) читаются как отказ:
// ожидающая группа сразу получит ошибку вместо того, чтобы стоять до таймаута
// в пять минут. Апстрим различал отказ и мусор — на отказ у него была отдельная
// строка stdin, TURN_CREDS|error:, а мусор он молча выбрасывал; здесь такого
// различия нет, потому что отдаёт креды не Android, а наш же CLI.
func (c *Control) SubmitTurnCreds(creds TurnCreds) {
	if c == nil {
		return
	}

	link := strings.TrimSpace(creds.Link)
	urls := turnURLsToAddresses(creds.URLs)

	payload := turnCredsPayload{}
	if link != "" && creds.Username != "" && creds.Password != "" && len(urls) > 0 {
		injectTurnCreds(link, creds.Username, creds.Password, urls)
		payload = turnCredsPayload{User: creds.Username, Pass: creds.Password, URLs: urls}
	}

	drainTurnCredsResult()
	select {
	case TurnCredsResultChan <- payload:
	default:
	}
}
