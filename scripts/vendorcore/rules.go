package main

// =============================== правки =========================================
// Всё, что меняется в апстримном коде, описано здесь и только здесь.

// excludedFile — файл апстрима, который в десктопной сборке не нужен вовсе
// (задача B6). Reason попадает в сообщение проверки.
//
// Наличие файла в апстриме проверяется: если его переименуют, команда упадёт,
// а не выбросит молча не то. Исключённый файл, оставшийся в internal/core,
// удаляется при переносе и считается расхождением при -check.
type excludedFile struct {
	Name, Reason string
	accounting   accounting
}

var excludedFiles = []excludedFile{
	{"tun_fd.go", "приём TUN-дескриптора от Android по SCM_RIGHTS: режим rawtun на десктопе не поддерживается",
		localChange("excludedFiles/tun_fd.go", "Omit Android descriptor passing because the desktop library has no rawtun mode.")},
	{"socks_wg.go", "userspace WireGuard через netstack: переехал в internal/datapath (задача D2)",
		localChange("excludedFiles/socks_wg.go", "Use the owned internal/datapath WireGuard integration instead of upstream netstack.")},
	{"socks5.go", "SOCKS5-сервер поверх netstack: переехал в internal/datapath (задача D2)",
		localChange("excludedFiles/socks5.go", "Use the owned internal/datapath SOCKS server instead of upstream netstack.")},
}

// listen.go задаёт SO_REUSEADDR через syscall.SetsockoptInt, а его сигнатура
// на Windows принимает syscall.Handle вместо int. Разносим по платформам.
var fileOverrides = map[string]accountedFile{
	"listen.go": {accounting: localChange("fileOverrides/listen.go", "Separate UDP reuse-address control from the platform-specific syscall signature."), text: `package core

import (
	"context"
	"net"
)

// listenUDP binds a UDP socket with SO_REUSEADDR so a quick restart can reclaim
// 127.0.0.1:port after the previous client process exits.
func listenUDP(addr string) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: setReuseAddr}
	return lc.ListenPacket(context.Background(), "udp", addr)
}`},
}

// Файлы, которых в апстриме нет вовсе — их создаёт эта команда.
var generatedFiles = map[string]accountedFile{
	"listen_unix.go": {accounting: localChange("generatedFiles/listen_unix.go", "Implement reuse-address socket control with the Unix integer descriptor signature."), text: `//go:build !windows

package core

import "syscall"

func setReuseAddr(network, address string, c syscall.RawConn) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return setErr
}`},

	"listen_windows.go": {accounting: localChange("generatedFiles/listen_windows.go", "Implement reuse-address socket control with the Windows Handle signature."), text: `//go:build windows

package core

import "syscall"

// На Windows syscall.SetsockoptInt принимает syscall.Handle, а не int.
func setReuseAddr(network, address string, c syscall.RawConn) error {
	var setErr error
	if err := c.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return setErr
}`},
}

// rule — замена в апстримном коде. Маркеры stdout становятся событиями из
// events.go (задача B2), а func main() уезжает в наш run.go (задача B4).
//
// Правила ниже — единственное место, где эти правки живут. Прямая правка
// internal/core/*.go будет затёрта при следующем переносе ядра.
//
// Count проверяется: если апстрим перепишет строку и шаблон перестанет совпадать,
// команда упадёт. Молча потерять правку хуже, чем упасть.
type rule struct {
	// File — имя файла в go_client.
	File string
	// Count — сколько раз шаблон обязан совпасть.
	Count int
	// Pattern — regexp по тексту с LF; группа 1 обязана захватывать отступ.
	Pattern string
	// Ровно одно из трёх. Replace — чем заменить совпадение целиком (nil —
	// не задано, "" — вырезать); InsertAfter — строка, дописываемая после
	// совпадения с тем же отступом; InsertBefore — то же, но перед совпадением.
	Replace      *string
	InsertAfter  string
	InsertBefore string
	accounting   accounting
}

// to — Replace правила: строка, в том числе пустая.
func to(s string) *string { return &s }

var replacements = []rule{
	// --- func main() уезжает в cmd/qwdtt-cli ---
	// Разбор флагов, сигналы, чтение stdin и os.Exit принадлежат программе,
	// а не библиотеке; то, что осталось от main() по существу, переписано
	// руками в internal/core/run.go (задача B4). Механической правкой такое
	// не выразить, поэтому функция вырезается целиком.
	//
	// Count здесь работает так же, как везде: если апстрим переименует main
	// или сменит её сигнатуру, команда упадёт и правку придётся пересмотреть —
	// вместе с run.go, который повторяет её содержимое.
	//
	// Шаблон опирается на gofmt: тело функции отбито табами, поэтому первая
	// строка, начинающаяся со скобки в нулевой колонке, и есть её конец.
	// Пустая строка перед объявлением съедается вместе с ним, иначе файл
	// заканчивался бы ею и gofmt считал бы его неотформатированным.
	{
		accounting: localChange("replacements/remove-main-entrypoint", "Move flags, signals, stdin and process exit to the owned CLI wrapper; this is not the upstream per-worker config fix."),
		File:       "main.go",
		Count:      1,
		Pattern:    `(?ms)^\nfunc main\(\) \{\n.*?^\}\n`,
		Replace:    to(""),
	},

	// RunPing из session.go — не точка входа, а один замер поверх готовых кредов.
	// Экспортированной она была ради main(); публичный замер теперь называется
	// так же и живёт в run.go, поэтому апстримную функцию делаем внутренней.
	{
		accounting: localChange("replacements/rename-run-ping", "Keep the upstream measurement helper internal while the owned wrapper provides the public RunPing."),
		File:       "session.go",
		Count:      1,
		Pattern:    `(?m)^func RunPing\($`,
		Replace:    to("func runPing("),
	},

	// --- четыре маркера stdout ---
	{
		accounting: localChange("replacements/hash-check-start-event", "Deliver hash-check progress through the local event sink instead of stdout markers."),
		File:       "main.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)fmt\.Printf\("HASH_CHECK_START\|%d\|%s\\n", i\+1, hash\)$`,
		Replace:    to(`${1}emitHashCheck(i+1, len(hashes), hash, HashCheckRunning, "")`),
	},
	{
		accounting: localChange("replacements/hash-check-result-event", "Deliver hash-check results through the local event sink instead of stdout markers."),
		File:       "main.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)fmt\.Printf\("HASH_CHECK\|%d\|%s\|%s\|%s\\n", i\+1, hash, status, sanitizeHashCheckMessage\(message\)\)$`,
		Replace:    to(`${1}emitHashCheck(i+1, len(hashes), hash, HashCheckStatus(status), sanitizeHashCheckMessage(message))`),
	},
	{
		accounting: localChange("replacements/captcha-request-event", "Deliver captcha requests to the local solver through typed events instead of stdout markers."),
		File:       "creds.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)fmt\.Printf\("CAPTCHA_SOLVE\|%s\|%s\|%s\\n", mode, captchaErr\.RedirectURI, captchaErr\.SessionToken\)$`,
		Replace:    to(`${1}emit(EventCaptchaRequired{Session: streamID, Mode: mode, RedirectURI: captchaErr.RedirectURI, SessionToken: captchaErr.SessionToken})`),
	},
	{
		accounting: localChange("replacements/vk-auth-event", "Deliver VK authentication requests through the local event sink instead of stdout markers."),
		File:       "vk_account.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)fmt\.Printf\("VK_AUTH_REQUIRED\|%s\\n", link\)$`,
		Replace:    to(`${1}emit(EventVkAuthRequired{Link: link})`),
	},

	// --- шаги подключения ---
	// Ядро отправляет только середину схемы: DNS проверяется до его запуска,
	// а VPN/SOCKS/DONE закрывает супервизор по EventConfig (см. events.go).
	{
		accounting:  localChange("replacements/vk-stage-ready", "Report successful credentials as the local VK connection stage."),
		File:        "group.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[ГРУППА #%d\] Креды OK, TURN: %v, %d воркеров", groupID, creds\.TurnURLs, len\(workerIDs\)\)$`,
		InsertAfter: `emitStage(StageVK, StageOK, 0, "")`,
	},
	{
		accounting:   localChange("replacements/captcha-stage-running", "Report the start of captcha solving to the local stage observer."),
		File:         "creds.go",
		Count:        1,
		Pattern:      `(?m)^(\t+)successToken, solveErr := solveCaptchaBySelectedMode\(ctx, streamID, attempt\+1, captchaErr, client, profile, savedProfile\)$`,
		InsertBefore: `emitStage(StageCaptcha, StageRunning, streamID, "")`,
	},
	{
		accounting:  localChange("replacements/captcha-stage-failed", "Report captcha failure and its reason to the local stage observer."),
		File:        "creds.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[STREAM %d\] \[Captcha\] Solve failed: %v", streamID, solveErr\)$`,
		InsertAfter: `emitStage(StageCaptcha, StageFailed, streamID, solveErr.Error())`,
	},
	// Единственное место, куда попадают только после успешного решения капчи:
	// дальше ядро складывает запрос с уже полученным success_token.
	{
		accounting:   localChange("replacements/captcha-stage-ready", "Report successful captcha completion before constructing the credential request."),
		File:         "creds.go",
		Count:        1,
		Pattern:      `(?m)^(\t+)captchaAttempt := captchaErr\.CaptchaAttempt$`,
		InsertBefore: `emitStage(StageCaptcha, StageOK, streamID, "")`,
	},
	{
		accounting:  localChange("replacements/turn-stage-ready", "Report the allocated relay address to the local TURN stage observer."),
		File:        "session.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[СЕССИЯ #%d\] Relay: %s", sessionID, relay\.LocalAddr\(\)\)$`,
		InsertAfter: `emitStage(StageTURN, StageOK, sessionID, relay.LocalAddr().String())`,
	},
	{
		accounting:  localChange("replacements/direct-stage-ready", "Report the direct RTP path as a completed local transport stage."),
		File:        "session.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[ПРЯМОЙ\] Без DTLS, только RTP-obfs AEAD ✓", sessionID\)$`,
		InsertAfter: `emitStage(StageDTLS, StageOK, sessionID, "без DTLS, только RTP-obfs AEAD")`,
	},
	{
		accounting:  localChange("replacements/dtls-stage-running", "Report handshake start to the local DTLS stage observer."),
		File:        "session.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[DTLS\] Рукопожатие \(Handshake\)\.\.\.", sessionID\)$`,
		InsertAfter: `emitStage(StageDTLS, StageRunning, sessionID, "")`,
	},
	{
		accounting:   localChange("replacements/dtls-stage-failed", "Report handshake errors to the local DTLS stage observer."),
		File:         "session.go",
		Count:        1,
		Pattern:      `(?m)^(\t+)return false, fmt\.Errorf\("DTLS хендшейк: %w", err\)$`,
		InsertBefore: `emitStage(StageDTLS, StageFailed, sessionID, err.Error())`,
	},
	{
		accounting:  localChange("replacements/dtls-stage-ready", "Report successful handshakes to the local DTLS stage observer."),
		File:        "session.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[DTLS\] Соединение установлено ✓", sessionID\)$`,
		InsertAfter: `emitStage(StageDTLS, StageOK, sessionID, "")`,
	},
	{
		accounting:  localChange("replacements/worker-stage-ready", "Report tunnel readiness to the local worker stage observer."),
		File:        "session.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[READY\] Туннель готов к работе ✓", sessionID\)$`,
		InsertAfter: `emitStage(StageWorkers, StageOK, sessionID, "")`,
	},

	// --- окно ручного режима капчи (задача E2) ---
	// 60 секунд достались от Android, где окно капчи всплывало само и мгновенно
	// (ManlCaptchaWebViewManager.kt:37). У нас в это окно укладывается ещё и то,
	// как пользователь доберётся до страницы: консольному решателю минуты
	// не хватает заведомо, окну WebView2 и браузеру под управлением — впритык
	// (doc/e2-captcha.md, §6).
	//
	// Цена ошибки — задержка одного воркера: остальные в это время работают,
	// а globalCaptchaLockout отсчитывается уже после отказа и от величины
	// не зависит. Окно auto не трогаем: оно машинное, и человека на него не
	// зовут (internal/captcha/solver.go, NeedsHuman).
	{
		accounting: upstreamChange("replacements/captcha-manual-timeout", "Allow 180 seconds for the human manual window while leaving auto unchanged.", "proposal-6-captcha-timeouts"),
		File:       "creds.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)captchaManualWebViewTimeout   = 60 \* time\.Second$`,
		Replace:    to(`${1}captchaManualWebViewTimeout   = 180 * time.Second`),
	},
	// Окно режима wv (задача «Автоклик капчи», doc/autoclick-captcha.md).
	// Оболочка запускает ядро в wv: нативный решатель Go v2 перестал разбирать
	// страницу капчи, и цепочка auto лишь тянула время до ручного окна. В wv
	// ядро на капчу сразу просит окно selected — то же окно, что manual, и с
	// тем же человеком за ним, поэтому и срок ему тот же. Две минуты апстрима
	// впритык: автоклик в этом окне съедает часть срока, прежде чем окно
	// выйдет человеку.
	{
		accounting: upstreamChange("replacements/captcha-selected-timeout", "Allow 180 seconds for the human selected window, including the direct wv path.", "proposal-6-captcha-timeouts"),
		File:       "creds.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)captchaSelectedWebViewTimeout = 120 \* time\.Second$`,
		Replace:    to(`${1}captchaSelectedWebViewTimeout = 180 * time.Second`),
	},
	// Строка режима wv. Ядро печатает её на каждую капчу, и с -v «режим из
	// настроек Android» сбивал пользователя с толку: Android здесь ни при чём,
	// режим задаёт оболочка (config.Profile.Core).
	{
		accounting: upstreamChange("replacements/captcha-wv-diagnostic", "Describe the wv captcha request without attributing mode selection to Android settings; local wording differs from the Android-compatible upstream patch.", "proposal-7-captcha-wv-log"),
		File:       "creds.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)log\.Printf\("\[STREAM %d\] \[КАПЧА\] WBV: режим из настроек Android \(attempt %d\)", streamID, attempt\)$`,
		Replace:    to(`${1}log.Printf("[STREAM %d] [КАПЧА] режим wv: сразу окно капчи (attempt %d)", streamID, attempt)`),
	},

	// --- режим rawtun (задача B6) ---
	// Режим принимал TUN-дескриптор от Android и на десктопе не поддерживается:
	// Config его не выражает с B1, ветки в Run не появилось с B4. Здесь уходит
	// то, что осталось от него внутри апстримного кода. Оставлять было нечем
	// оправдать: код недостижим, а читающего он сбивает с толку — и первым
	// делом заставляет искать, где же включается raw-режим.
	//
	// Диспетчер трогать не нужно: NewDispatcherPendingTUN и AttachTUN оставлены
	// намеренно, raw-путь может пригодиться Э4 на Linux (см. doc/local/plan-e1.md — локальный журнал, вне git).
	{
		accounting: localChange("replacements/remove-raw-mode-field", "Remove the unsupported desktop rawtun transport option."),
		File:       "group.go",
		Count:      1,
		Pattern:    `(?m)^\t// RawMode: raw-IP без WireGuard \(см\. server/main\.go -listen-raw, handleConnRaw\)\.\n\t// Подразумевает NoDTLS — сервер на -listen-raw DTLS не понимает\.\n\tRawMode bool\n`,
		Replace:    to(""),
	},
	{
		accounting: localChange("replacements/remove-raw-wrap-condition", "Keep only the supported NoDTLS condition when selecting wrapped transport."),
		File:       "session.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)if useWrap && \(tp\.NoDTLS \|\| tp\.RawMode\) \{$`,
		Replace:    to(`${1}if useWrap && tp.NoDTLS {`),
	},
	// Ветка raw-конфига уходит целиком; её else — обычный GETCONF — становится
	// единственным условием.
	{
		accounting: localChange("replacements/remove-raw-config-branch", "Retain ordinary GETCONF without the unsupported rawtun config branch."),
		File:       "session.go",
		Count:      1,
		Pattern:    `(?ms)^(\t+)if getConfig && configCh != nil && tp\.RawMode \{\n.*?^\t\} else if getConfig && configCh != nil \{$`,
		Replace:    to(`${1}if getConfig && configCh != nil {`),
	},
	// Уведомление сервера о намеренном разрыве raw-сессии (DISCONNECT_RAW).
	{
		accounting: localChange("replacements/remove-raw-disconnect", "Remove disconnect notification for the unsupported rawtun session."),
		File:       "session.go",
		Count:      1,
		Pattern:    `(?ms)^\n\tif tp\.RawMode \{\n.*?^\t\}\n`,
		Replace:    to(""),
	},
	// Запрос raw-конфига у сервера (GETCONF_RAW / RAWCONF). Единственный
	// вызывающий — ветка выше.
	{
		accounting: localChange("replacements/remove-raw-config-request", "Remove the raw config helper after its unsupported desktop caller is removed."),
		File:       "protocol.go",
		Count:      1,
		Pattern:    `(?ms)^\n// RequestRawConfig запрашивает у сервера.*?^\}\n`,
		Replace:    to(""),
	},

	// --- отказ хеша звонка ---
	// Группа, не получившая креды, в апстриме умирала молча: строка в лог и
	// return. Туннель от этого не падает — остальные группы работают, — но
	// супервизору нужно знать, какой хеш исключить из следующей попытки и когда
	// годных не осталось вовсе (задача F2). Разбирать лог, как это делал
	// Android, мы не хотим: ради этого и заводились события.
	//
	// Вместе с отказом группа передаёт эстафету следующей. В апстриме
	// signalReady закрывался только после удачных кредов, и при мёртвом хеше
	// первой группы следующие ждали её сигнала вечно: туннель не поднимался
	// и не падал, хотя второй хеш был годен.
	{
		accounting: accounting{
			id: "replacements/credentials-failure-handoff", class: "mixed",
			rationale: "Emit local emitHashFailed so the supervisor can retire a failed hash, and close(signalReady) to fix the upstream baton stall after failed credentials.",
			proposals: []string{"proposal-2-baton"},
		},
		File:    "group.go",
		Count:   1,
		Pattern: `(?m)^(\t+)log\.Printf\("\[ГРУППА #%d\] Ошибка кредов: %v", groupID, err\)$`,
		InsertAfter: "emitHashFailed(hash, groupID, err)\n" +
			"${1}if signalReady != nil {\n" +
			"${1}\tclose(signalReady)\n" +
			"${1}}",
	},
	// Закрытый звонок VK отвечает «Invalid join link (error_code=954)»; ядро
	// верно делает из этого CallUnavailableError (fatalCallError), но
	// классификатор смотрит только в текст и ищет «call not found», «9000»,
	// «callunavailable» — ни одной из этих строк там нет. Статус выходил
	// error вместо dead, HashPool хеш не исключал, и клиент с одним закрытым
	// звонком переподключался бесконечно. Тип ошибки надёжнее текста:
	// проверяем его первым, текстовые проверки остаются для прочих путей.
	{
		accounting: upstreamChange("replacements/classify-closed-call", "Recognize typed and wrapped unavailable-call errors before textual fallbacks so a closed call is dead.", "proposal-1-closed-call"),
		File:       "main.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)text := strings\.ToLower\(err\.Error\(\)\)$`,
		Replace: to("${1}if callErr, ok := asCallUnavailableError(err); ok {\n" +
			"${1}\treturn \"dead\", fmt.Sprintf(\"Звонок не найден или закрыт (error_code=%d)\", callErr.Code)\n" +
			"${1}}\n" +
			"${0}"),
	},

	// --- счётчики трафика ---
	{
		accounting:  localChange("replacements/statistics-event", "Forward worker, traffic and drop counters as local EventStats; upstream proposal 4 observes standard logging instead."),
		File:        "stats.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[СТАТИСТИКА\] Активных: %d \| Трафик: %\.2f МБ \| ↓%\.2f МБ / ↑%\.2f МБ", active, totalMB, downMB, upMB\)$`,
		InsertAfter: `emit(EventStats{ActiveWorkers: active, BytesUp: up, BytesDown: down, DroppedUp: s.DroppedUp.Load(), DroppedDown: s.DroppedDown.Load()})`,
	},

	// --- конфиг WireGuard при исчерпанной квоте TURN ---
	// Конфиг просил один воркер группы: флаг configRequestInFlight брался до
	// входа в RunSession. Воркер, упёршийся в квоту VK (486), держал флаг,
	// пока остальные стартовали — и они, получив relay и DTLS, конфиг уже не
	// просили: решение принимается в начале сеанса, а сеанс живёт долго. Сам
	// он ждал 30–60 с между попытками, туннель без конфига не поднимался
	// минутами, хотя рабочие сеансы были.
	//
	// Теперь конфиг просит каждый воркер, входящий в сеанс, пока он не
	// получен. Сервер на повторный GETCONF отвечает тем же конфигом (ключи
	// устройства заводятся один раз, server/connections.go), лишний доставленный
	// конфиг RunSession сбрасывает сам («уже был доставлен другим воркером»).
	{
		accounting: upstreamChange("replacements/remove-config-request-owner", "Remove the single in-flight config owner that can be held by a quota-blocked worker.", "proposal-3-config-quota"),
		File:       "group.go",
		Count:      1,
		Pattern:    `(?m)^\tvar configRequestInFlight int32\n`,
		Replace:    to(""),
	},
	{
		accounting: upstreamChange("replacements/select-config-per-worker", "Let each entering worker request config until another worker has delivered it.", "proposal-3-config-quota"),
		File:       "group.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)getConf := false\n\t+if shouldGetConfig && atomic\.LoadInt32\(&configSent\) == 0 \{\n\t+getConf = atomic\.CompareAndSwapInt32\(&configRequestInFlight, 0, 1\)\n\t+\}$`,
		Replace:    to(`${1}getConf := shouldGetConfig && atomic.LoadInt32(&configSent) == 0`),
	},
	{
		accounting: upstreamChange("replacements/complete-config-per-worker", "Record successful config delivery without reviving the removed single-owner flag.", "proposal-3-config-quota"),
		File:       "group.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)if getConf \{\n\t+if configDelivered \{\n\t+atomic\.StoreInt32\(&configSent, 1\)\n\t+\} else \{\n\t+atomic\.StoreInt32\(&configRequestInFlight, 0\)\n\t+\}\n\t+\}$`,
		Replace:    to("${1}if getConf && configDelivered {\n${1}\tatomic.StoreInt32(&configSent, 1)\n${1}}"),
	},

	// --- остановка во время запроса конфига ---
	// Ответа на GETCONF RequestConfig ждёт до 45 с и на отмену не смотрит, а
	// stopConn, снимающий соединение по отмене сеанса, ставится уже после
	// запроса. Остановка (Ctrl+C, перезапуск супервизором), пришедшая в это
	// время, ждала ответа, которого после отмены не будет: relay снят. Run
	// ждёт все воркеры, так что держалась вся остановка — до 45 с. Стенд
	// квоты (quota_stand_test.go) ловил это в двух прогонах из восьми.
	{
		accounting: upstreamChange("replacements/install-config-cancellation", "Close the active transport on cancellation while the config request is blocked.", "proposal-5-stop"),
		File:       "session.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)// Запрос конфига$`,
		Replace: to("${1}// Отмена сеанса снимает соединение и во время запроса конфига:\n" +
			"${1}// ответа RequestConfig ждёт до 45 с, а stopConn ставится ниже.\n" +
			"${1}stopConfig := context.AfterFunc(sessCtx, func() {\n" +
			"${1}\t_ = activeConn.SetDeadline(time.Now())\n" +
			"${1}\t_ = activeConn.Close()\n" +
			"${1}})\n${0}"),
	},
	{
		accounting:   upstreamChange("replacements/stop-config-cancellation", "Stop the config-phase cancellation callback once normal session cancellation takes over.", "proposal-5-stop"),
		File:         "session.go",
		Count:        1,
		Pattern:      `(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[READY\] Туннель готов к работе ✓", sessionID\)$`,
		InsertBefore: `stopConfig()`,
	},
	// Чтение сеанса перед каждым Read ставит дедлайн +30 мин. Если отмена
	// пришла раньше — stopConn уже выставил дедлайн «сейчас», — он
	// затирался, и Read ждал 30 минут; Run — вместе с ним. Проверка после
	// установки дедлайна гонки не оставляет: stopConn срабатывает после
	// отмены, то есть либо проверка её увидит, либо его дедлайн ляжет позже.
	{
		accounting: upstreamChange("replacements/check-cancel-after-deadline", "Check cancellation after setting the read deadline so it cannot overwrite cancellation with a long wait.", "proposal-5-stop"),
		File:       "session.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)_ = activeConn\.SetReadDeadline\(time\.Now\(\)\.Add\(sessionReadTimeout\)\)$`,
		Replace:    to("${0}\n${1}if sessCtx.Err() != nil {\n${1}\treturn\n${1}}"),
	},

	// --- сброшенные пакеты ---
	// Ядро выбрасывает пакет, когда очередь полна, и в десктопном режиме молча:
	// единственный счётчик, tunDroppedCount, живёт под if d.tunFile != nil —
	// для rawtun Android. При поднятом туннеле терялось 5–20% UDP, и понять,
	// в ядре ли дело, было не по чему. Поведение не меняется — только счёт.
	{
		accounting: upstreamChange("replacements/drop-counter-fields", "Expose atomic directional packet-drop totals for saturated queues without changing delivery.", "proposal-4-dropped-packets"),
		File:       "stats.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)ActiveConnections atomic\.Int32$`,
		Replace: to("${0}\n\n" +
			"${1}// DroppedUp — пакеты от WireGuard, выброшенные потому, что очереди\n" +
			"${1}// всех воркеров полны (Dispatcher.readLoop). DroppedDown — ответы\n" +
			"${1}// сервера, выброшенные из-за полной очереди к WireGuard (RunSession).\n" +
			"${1}DroppedUp   atomic.Uint64\n" +
			"${1}DroppedDown atomic.Uint64"),
	},
	{
		accounting:  upstreamChange("replacements/count-uplink-saturation", "Count an uplink drop only after all eligible worker queues reject the packet.", "proposal-4-dropped-packets"),
		File:        "dispatcher.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)// Все workers перегружены — сдвигаем указатель, пакет дропается$`,
		InsertAfter: `d.stats.DroppedUp.Add(1)`,
	},
	{
		accounting:  upstreamChange("replacements/count-downlink-saturation", "Count downlink packets discarded by the full return queue without changing the nonblocking branch.", "proposal-4-dropped-packets"),
		File:        "session.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)// воркера — select должен быть неблокирующим\.$`,
		InsertAfter: `stats.DroppedDown.Add(1)`,
	},

	// --- netguard: хук в собственных сокетах ядра (задача N3 Э2) ---
	// Шесть точек, где ядро открывает сокеты наружу. В каждую попадает
	// guardedControl — Config.DialControl текущего запуска; диалеры и таблица
	// точек — в internal/core/netguard_hook.go. Новую точку, которую апстрим
	// заведёт после обновления, ловит TestNetguardCoverage: правила здесь
	// и список в тесте меняются вместе.
	//
	// Поле Control вставляется первым и выровнено под самое длинное имя
	// поля (KeepAlive) — так, как его поставил бы gofmt.

	// 1. TURN по UDP. У net.DialUDP нет Control, поэтому вызов меняется.
	{
		accounting: localChange("replacements/guard-turn-udp", "Route TURN UDP socket creation through the local guarded dial helper."),
		File:       "session.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)c, err := net\.DialUDP\("udp", nil, resolved\)$`,
		Replace:    to(`${1}c, err := dialTURNUDP(resolved)`),
	},
	// 2. TURN по TCP.
	{
		accounting: localChange("replacements/guard-turn-tcp", "Apply the local network guard to TURN TCP socket creation."),
		File:       "session.go",
		Count:      1,
		Pattern:    `(?m)^(\t+)d := net\.Dialer\{Timeout: 10 \* time\.Second\}$`,
		Replace:    to(`${1}d := net.Dialer{Timeout: 10 * time.Second, Control: guardedControl}`),
	},
	// 3. Резолвер VK по UDP/TCP :53 (setupGlobalResolver).
	{
		accounting:  localChange("replacements/guard-vk-resolver", "Apply the local network guard to the VK UDP and TCP resolver sockets."),
		File:        "creds.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)dialer := &net\.Dialer\{$`,
		InsertAfter: "\tControl:   guardedControl,",
	},
	// 4. Загрузочные соединения DoH (newDoHBootstrapDialer).
	{
		accounting:  localChange("replacements/guard-doh-bootstrap", "Apply the local network guard to DoH bootstrap connections."),
		File:        "doh.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)return &net\.Dialer\{$`,
		InsertAfter: "\tControl:   guardedControl,",
	},
	// 5. HTTPS к VK с отпечатком Chrome (getTokenChain). Им же пользуется
	//    решатель капчи v2.
	{
		accounting:  localChange("replacements/guard-token-client", "Use the locally guarded HTTPS dialer for token and native captcha requests."),
		File:        "creds.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)tlsclient\.WithCookieJar\(jar\),$`,
		InsertAfter: `tlsclient.WithDialer(guardedDialer()),`,
	},
	// 6. HTTPS к VK по пути vkcalls.
	{
		accounting:  localChange("replacements/guard-vkcalls-client", "Use the locally guarded HTTPS dialer for the vkcalls client."),
		File:        "creds_vkcalls.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)tlsclient\.WithCookieJar\(tlsclient\.NewCookieJar\(\)\),$`,
		InsertAfter: `tlsclient.WithDialer(guardedDialer()),`,
	},
}

// Логи ядра уходят в Sink (задача B3). Замена сплошная — вызовов больше сотни
// и они во всех файлах, поэтому она идёт отдельным проходом, а не правилами
// со счётчиком: ноль совпадений в конкретном файле здесь норма.
//
// Каждая пара — слово с границей слова перед ним (\b) и чем его заменить.
// Тела logf и logln — в internal/core/log.go. Пары для log.Fatal здесь нет
// намеренно: см. проверку в convertFile.
var logReplacements = []logReplacement{
	{"log.Printf(", "logf(", localChange("logReplacements/log.Printf(", "Forward formatted core logs to the local sink.")},
	{"log.Println(", "logln(", localChange("logReplacements/log.Println(", "Forward line-oriented core logs to the local sink.")},
	{"log.Print(", "logln(", localChange("logReplacements/log.Print(", "Forward unformatted core logs to the local sink.")},
}

// Импорты, которые могут осиротеть после наших правок: вместе с func main()
// уходят bufio, flag, net, os, os/signal, sync и syscall, вместе с log.Printf —
// сам log, а вместе с RequestRawConfig — strconv. Go на неиспользованный импорт
// не смотрит сквозь пальцы, поэтому осиротевшие строки снимаются, а не
// оставляются на память.
//
// Список закрытый: снимается только то, что могли осиротить именно наши правки.
// Ложно снять импорт нельзя — проверка идёт по имени пакета, и если оно в файле
// встречается, строка остаётся.
var prunableImports = []prunableImport{
	{"bufio", localChange("prunableImports/bufio", "Remove the buffered-input import when CLI entry-point extraction leaves it unused.")},
	{"flag", localChange("prunableImports/flag", "Remove flag parsing imports when the owned CLI takes over argument handling.")},
	{"log", localChange("prunableImports/log", "Remove standard logging imports after forwarding all logs to the local sink.")},
	{"net", localChange("prunableImports/net", "Remove the net import when entry-point extraction leaves no network calls in that file.")},
	{"os", localChange("prunableImports/os", "Remove process and stdin imports when the owned CLI takes over entry-point duties.")},
	{"os/signal", localChange("prunableImports/os/signal", "Remove signal handling imports after extracting the process entry point.")},
	{"strconv", localChange("prunableImports/strconv", "Remove numeric parsing imports when removal of the raw config helper leaves them unused.")},
	{"sync", localChange("prunableImports/sync", "Remove synchronization imports when entry-point extraction leaves them unused.")},
	{"syscall", localChange("prunableImports/syscall", "Remove signal syscall imports when the owned CLI takes over process handling.")},
}

// ================================================================================
