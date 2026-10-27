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
}

var excludedFiles = []excludedFile{
	{"tun_fd.go", "приём TUN-дескриптора от Android по SCM_RIGHTS: режим rawtun на десктопе не поддерживается"},
	{"socks_wg.go", "userspace WireGuard через netstack: переехал в internal/datapath (задача D2)"},
	{"socks5.go", "SOCKS5-сервер поверх netstack: переехал в internal/datapath (задача D2)"},
}

// listen.go задаёт SO_REUSEADDR через syscall.SetsockoptInt, а его сигнатура
// на Windows принимает syscall.Handle вместо int. Разносим по платформам.
var fileOverrides = map[string]string{
	"listen.go": `package core

import (
	"context"
	"net"
)

// listenUDP binds a UDP socket with SO_REUSEADDR so a quick restart can reclaim
// 127.0.0.1:port after the previous client process exits.
func listenUDP(addr string) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: setReuseAddr}
	return lc.ListenPacket(context.Background(), "udp", addr)
}`,
}

// Файлы, которых в апстриме нет вовсе — их создаёт эта команда.
var generatedFiles = map[string]string{
	"listen_unix.go": `//go:build !windows

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
}`,

	"listen_windows.go": `//go:build windows

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
}`,
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
		File:    "main.go",
		Count:   1,
		Pattern: `(?ms)^\nfunc main\(\) \{\n.*?^\}\n`,
		Replace: to(""),
	},

	// RunPing из session.go — не точка входа, а один замер поверх готовых кредов.
	// Экспортированной она была ради main(); публичный замер теперь называется
	// так же и живёт в run.go, поэтому апстримную функцию делаем внутренней.
	{
		File:    "session.go",
		Count:   1,
		Pattern: `(?m)^func RunPing\($`,
		Replace: to("func runPing("),
	},

	// --- четыре маркера stdout ---
	{
		File:    "main.go",
		Count:   1,
		Pattern: `(?m)^(\t+)fmt\.Printf\("HASH_CHECK_START\|%d\|%s\\n", i\+1, hash\)$`,
		Replace: to(`${1}emitHashCheck(i+1, len(hashes), hash, HashCheckRunning, "")`),
	},
	{
		File:    "main.go",
		Count:   1,
		Pattern: `(?m)^(\t+)fmt\.Printf\("HASH_CHECK\|%d\|%s\|%s\|%s\\n", i\+1, hash, status, sanitizeHashCheckMessage\(message\)\)$`,
		Replace: to(`${1}emitHashCheck(i+1, len(hashes), hash, HashCheckStatus(status), sanitizeHashCheckMessage(message))`),
	},
	{
		File:    "creds.go",
		Count:   1,
		Pattern: `(?m)^(\t+)fmt\.Printf\("CAPTCHA_SOLVE\|%s\|%s\|%s\\n", mode, captchaErr\.RedirectURI, captchaErr\.SessionToken\)$`,
		Replace: to(`${1}emit(EventCaptchaRequired{Session: streamID, Mode: mode, RedirectURI: captchaErr.RedirectURI, SessionToken: captchaErr.SessionToken})`),
	},
	{
		File:    "vk_account.go",
		Count:   1,
		Pattern: `(?m)^(\t+)fmt\.Printf\("VK_AUTH_REQUIRED\|%s\\n", link\)$`,
		Replace: to(`${1}emit(EventVkAuthRequired{Link: link})`),
	},

	// --- шаги подключения ---
	// Ядро отправляет только середину схемы: DNS проверяется до его запуска,
	// а VPN/SOCKS/DONE закрывает супервизор по EventConfig (см. events.go).
	{
		File:        "group.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[ГРУППА #%d\] Креды OK, TURN: %v, %d воркеров", groupID, creds\.TurnURLs, len\(workerIDs\)\)$`,
		InsertAfter: `emitStage(StageVK, StageOK, 0, "")`,
	},
	{
		File:         "creds.go",
		Count:        1,
		Pattern:      `(?m)^(\t+)successToken, solveErr := solveCaptchaBySelectedMode\(ctx, streamID, attempt\+1, captchaErr, client, profile, savedProfile\)$`,
		InsertBefore: `emitStage(StageCaptcha, StageRunning, streamID, "")`,
	},
	{
		File:        "creds.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[STREAM %d\] \[Captcha\] Solve failed: %v", streamID, solveErr\)$`,
		InsertAfter: `emitStage(StageCaptcha, StageFailed, streamID, solveErr.Error())`,
	},
	// Единственное место, куда попадают только после успешного решения капчи:
	// дальше ядро складывает запрос с уже полученным success_token.
	{
		File:         "creds.go",
		Count:        1,
		Pattern:      `(?m)^(\t+)captchaAttempt := captchaErr\.CaptchaAttempt$`,
		InsertBefore: `emitStage(StageCaptcha, StageOK, streamID, "")`,
	},
	{
		File:        "session.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[СЕССИЯ #%d\] Relay: %s", sessionID, relay\.LocalAddr\(\)\)$`,
		InsertAfter: `emitStage(StageTURN, StageOK, sessionID, relay.LocalAddr().String())`,
	},
	{
		File:        "session.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[ПРЯМОЙ\] Без DTLS, только RTP-obfs AEAD ✓", sessionID\)$`,
		InsertAfter: `emitStage(StageDTLS, StageOK, sessionID, "без DTLS, только RTP-obfs AEAD")`,
	},
	{
		File:        "session.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[DTLS\] Рукопожатие \(Handshake\)\.\.\.", sessionID\)$`,
		InsertAfter: `emitStage(StageDTLS, StageRunning, sessionID, "")`,
	},
	{
		File:         "session.go",
		Count:        1,
		Pattern:      `(?m)^(\t+)return false, fmt\.Errorf\("DTLS хендшейк: %w", err\)$`,
		InsertBefore: `emitStage(StageDTLS, StageFailed, sessionID, err.Error())`,
	},
	{
		File:        "session.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)log\.Printf\("\[ВОРКЕР #%d\] \[DTLS\] Соединение установлено ✓", sessionID\)$`,
		InsertAfter: `emitStage(StageDTLS, StageOK, sessionID, "")`,
	},
	{
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
		File:    "creds.go",
		Count:   1,
		Pattern: `(?m)^(\t+)captchaManualWebViewTimeout   = 60 \* time\.Second$`,
		Replace: to(`${1}captchaManualWebViewTimeout   = 180 * time.Second`),
	},
	// Окно режима wv (задача «Автоклик капчи», doc/autoclick-captcha.md).
	// Оболочка запускает ядро в wv: нативный решатель Go v2 перестал разбирать
	// страницу капчи, и цепочка auto лишь тянула время до ручного окна. В wv
	// ядро на капчу сразу просит окно selected — то же окно, что manual, и с
	// тем же человеком за ним, поэтому и срок ему тот же. Две минуты апстрима
	// впритык: автоклик в этом окне съедает часть срока, прежде чем окно
	// выйдет человеку.
	{
		File:    "creds.go",
		Count:   1,
		Pattern: `(?m)^(\t+)captchaSelectedWebViewTimeout = 120 \* time\.Second$`,
		Replace: to(`${1}captchaSelectedWebViewTimeout = 180 * time.Second`),
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
		File:    "group.go",
		Count:   1,
		Pattern: `(?m)^\t// RawMode: raw-IP без WireGuard \(см\. server/main\.go -listen-raw, handleConnRaw\)\.\n\t// Подразумевает NoDTLS — сервер на -listen-raw DTLS не понимает\.\n\tRawMode bool\n`,
		Replace: to(""),
	},
	{
		File:    "session.go",
		Count:   1,
		Pattern: `(?m)^(\t+)if useWrap && \(tp\.NoDTLS \|\| tp\.RawMode\) \{$`,
		Replace: to(`${1}if useWrap && tp.NoDTLS {`),
	},
	// Ветка raw-конфига уходит целиком; её else — обычный GETCONF — становится
	// единственным условием.
	{
		File:    "session.go",
		Count:   1,
		Pattern: `(?ms)^(\t+)if getConfig && configCh != nil && tp\.RawMode \{\n.*?^\t\} else if getConfig && configCh != nil \{$`,
		Replace: to(`${1}if getConfig && configCh != nil {`),
	},
	// Уведомление сервера о намеренном разрыве raw-сессии (DISCONNECT_RAW).
	{
		File:    "session.go",
		Count:   1,
		Pattern: `(?ms)^\n\tif tp\.RawMode \{\n.*?^\t\}\n`,
		Replace: to(""),
	},
	// Запрос raw-конфига у сервера (GETCONF_RAW / RAWCONF). Единственный
	// вызывающий — ветка выше.
	{
		File:    "protocol.go",
		Count:   1,
		Pattern: `(?ms)^\n// RequestRawConfig запрашивает у сервера.*?^\}\n`,
		Replace: to(""),
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
		File:    "main.go",
		Count:   1,
		Pattern: `(?m)^(\t+)text := strings\.ToLower\(err\.Error\(\)\)$`,
		Replace: to("${1}if callErr, ok := asCallUnavailableError(err); ok {\n" +
			"${1}\treturn \"dead\", fmt.Sprintf(\"Звонок не найден или закрыт (error_code=%d)\", callErr.Code)\n" +
			"${1}}\n" +
			"${0}"),
	},

	// --- счётчики трафика ---
	{
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
		File:    "group.go",
		Count:   1,
		Pattern: `(?m)^\tvar configRequestInFlight int32\n`,
		Replace: to(""),
	},
	{
		File:    "group.go",
		Count:   1,
		Pattern: `(?m)^(\t+)getConf := false\n\t+if shouldGetConfig && atomic\.LoadInt32\(&configSent\) == 0 \{\n\t+getConf = atomic\.CompareAndSwapInt32\(&configRequestInFlight, 0, 1\)\n\t+\}$`,
		Replace: to(`${1}getConf := shouldGetConfig && atomic.LoadInt32(&configSent) == 0`),
	},
	{
		File:    "group.go",
		Count:   1,
		Pattern: `(?m)^(\t+)if getConf \{\n\t+if configDelivered \{\n\t+atomic\.StoreInt32\(&configSent, 1\)\n\t+\} else \{\n\t+atomic\.StoreInt32\(&configRequestInFlight, 0\)\n\t+\}\n\t+\}$`,
		Replace: to("${1}if getConf && configDelivered {\n${1}\tatomic.StoreInt32(&configSent, 1)\n${1}}"),
	},

	// --- остановка во время запроса конфига ---
	// Ответа на GETCONF RequestConfig ждёт до 45 с и на отмену не смотрит, а
	// stopConn, снимающий соединение по отмене сеанса, ставится уже после
	// запроса. Остановка (Ctrl+C, перезапуск супервизором), пришедшая в это
	// время, ждала ответа, которого после отмены не будет: relay снят. Run
	// ждёт все воркеры, так что держалась вся остановка — до 45 с. Стенд
	// квоты (quota_stand_test.go) ловил это в двух прогонах из восьми.
	{
		File:    "session.go",
		Count:   1,
		Pattern: `(?m)^(\t+)// Запрос конфига$`,
		Replace: to("${1}// Отмена сеанса снимает соединение и во время запроса конфига:\n" +
			"${1}// ответа RequestConfig ждёт до 45 с, а stopConn ставится ниже.\n" +
			"${1}stopConfig := context.AfterFunc(sessCtx, func() {\n" +
			"${1}\t_ = activeConn.SetDeadline(time.Now())\n" +
			"${1}\t_ = activeConn.Close()\n" +
			"${1}})\n${0}"),
	},
	{
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
		File:    "session.go",
		Count:   1,
		Pattern: `(?m)^(\t+)_ = activeConn\.SetReadDeadline\(time\.Now\(\)\.Add\(sessionReadTimeout\)\)$`,
		Replace: to("${0}\n${1}if sessCtx.Err() != nil {\n${1}\treturn\n${1}}"),
	},

	// --- сброшенные пакеты ---
	// Ядро выбрасывает пакет, когда очередь полна, и в десктопном режиме молча:
	// единственный счётчик, tunDroppedCount, живёт под if d.tunFile != nil —
	// для rawtun Android. При поднятом туннеле терялось 5–20% UDP, и понять,
	// в ядре ли дело, было не по чему. Поведение не меняется — только счёт.
	{
		File:    "stats.go",
		Count:   1,
		Pattern: `(?m)^(\t+)ActiveConnections atomic\.Int32$`,
		Replace: to("${0}\n\n" +
			"${1}// DroppedUp — пакеты от WireGuard, выброшенные потому, что очереди\n" +
			"${1}// всех воркеров полны (Dispatcher.readLoop). DroppedDown — ответы\n" +
			"${1}// сервера, выброшенные из-за полной очереди к WireGuard (RunSession).\n" +
			"${1}DroppedUp   atomic.Uint64\n" +
			"${1}DroppedDown atomic.Uint64"),
	},
	{
		File:        "dispatcher.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)// Все workers перегружены — сдвигаем указатель, пакет дропается$`,
		InsertAfter: `d.stats.DroppedUp.Add(1)`,
	},
	{
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
		File:    "session.go",
		Count:   1,
		Pattern: `(?m)^(\t+)c, err := net\.DialUDP\("udp", nil, resolved\)$`,
		Replace: to(`${1}c, err := dialTURNUDP(resolved)`),
	},
	// 2. TURN по TCP.
	{
		File:    "session.go",
		Count:   1,
		Pattern: `(?m)^(\t+)d := net\.Dialer\{Timeout: 10 \* time\.Second\}$`,
		Replace: to(`${1}d := net.Dialer{Timeout: 10 * time.Second, Control: guardedControl}`),
	},
	// 3. Резолвер VK по UDP/TCP :53 (setupGlobalResolver).
	{
		File:        "creds.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)dialer := &net\.Dialer\{$`,
		InsertAfter: "\tControl:   guardedControl,",
	},
	// 4. Загрузочные соединения DoH (newDoHBootstrapDialer).
	{
		File:        "doh.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)return &net\.Dialer\{$`,
		InsertAfter: "\tControl:   guardedControl,",
	},
	// 5. HTTPS к VK с отпечатком Chrome (getTokenChain). Им же пользуется
	//    решатель капчи v2.
	{
		File:        "creds.go",
		Count:       1,
		Pattern:     `(?m)^(\t+)tlsclient\.WithCookieJar\(jar\),$`,
		InsertAfter: `tlsclient.WithDialer(guardedDialer()),`,
	},
	// 6. HTTPS к VK по пути vkcalls.
	{
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
var logReplacements = [][2]string{
	{"log.Printf(", "logf("},
	{"log.Println(", "logln("},
	{"log.Print(", "logln("},
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
var prunableImports = []string{"bufio", "flag", "log", "net", "os", "os/signal", "strconv", "sync", "syscall"}

// ================================================================================
