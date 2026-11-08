# Предложения в апстрим (SpaceNeuroX/proxy-turn-vk-android)

Coherent changes intended for the upstream `go_client`, with symptoms, causes, fixes and
offline evidence. File and function names refer to the stable `master` source pinned at
`fae121efc3ef57b633516601d3c0d6b1be1fde7c` (`fae121e`), not an upgrade to `develop`.

The [reproduction set and launch instructions](upstream-repro/README.md) contain the patches,
tagged upstream `package main` tests and a safe local runner. It requires prepared local
source objects and dependencies, exports disposable pinned inputs and preserves the caller's
HEAD, index, configuration and working tree. It neither clones nor resets the caller's tree.
Success requires the exact baseline assertions and patched passes, with an always-passing
auto-timeout control and intentional ordinary deadline-test skips distinguished explicitly.

No VK or external service is used. Transport tests use a loopback Pion TURN server and a
DTLS qWDTT stand with synthetic cached credentials. This is local evidence, not a live
exhausted-quota, browser or platform check. Submission of these proposals remains separate.

| Proposal | Symptom or improvement | Offline evidence |
|---|---|---|
| [1. Closed-call status](#proposal-1-closed-call) | A closed call is reported as a generic error | Typed and wrapped errors |
| [2. Group baton](#proposal-2-baton) | Failed credentials leave the next group waiting | Cancelled credential path |
| [3. Config despite quota](#proposal-3-config-quota) | One worker's TURN quota failure prevents config delivery | Two-worker TURN/DTLS stand |
| [4. Drop counters](#proposal-4-dropped-packets) | Saturation drops are not observable | Both real queue branches and statistics output |
| [5. Stop promptly](#proposal-5-stop) | Config reads and overwritten deadlines delay cancellation | TURN/DTLS stand and injected deadline sequence |
| [6. Human captcha windows](#proposal-6-captcha-timeouts) | Manual and selected windows are shorter than the intended 180 seconds | Compiled constants, call-site contracts and immediate responses |
| [7. `wv` diagnostic](#proposal-7-captcha-wv-log) | The message incorrectly attributes mode selection to Android settings | Real selected-mode request with a synthetic response |

---

<a id="proposal-1-closed-call"></a>

## 1. Закрытый звонок не распознаётся как закрытый

**Patch:** [1-closed-call.patch](upstream-repro/1-closed-call.patch).
**Reproduction:** [repro_test.go](upstream-repro/repro_test.go), `TestReproClosedCallIsDead`;
isolated input `patched-closed-call`.

**Что видно.** Ссылка на звонок устарела — звонок закрыт. Проверка хешей показывает для него
общую ошибку с текстом VK вместо понятного «звонок закрыт».

**Почему.** VK отвечает на такую ссылку `Invalid join link (error_code=954)`. Ядро правильно
превращает ответ в ошибку типа `*CallUnavailableError` (`fatalCallError`). Но
`classifyHashCheckError` (`main.go`) на тип не смотрит — только ищет в тексте ошибки
`call not found`, `9000`, `callunavailable` и т. п. В «Invalid join link» нет ни одной из
этих строк, и статус выходит `error`, а не `dead`. Через путь vkcalls ошибка к тому же
завёрнута в `vkCallsFailure`.

**Как воспроизвести.** Проверить хеш закрытого звонка — или просто выдуманный: VK отвечает
на него тем же 954. Без сети:

```go
status, _ := classifyHashCheckError(&CallUnavailableError{Code: 954, Message: "Invalid join link"})
// status == "error", ожидается "dead"
```

**Как исправить.** В начало `classifyHashCheckError`, перед разбором текста, — проверка типа:

```go
if callErr, ok := asCallUnavailableError(err); ok {
	return "dead", fmt.Sprintf("Звонок не найден или закрыт (error_code=%d)", callErr.Code)
}
```

`errors.As` внутри `asCallUnavailableError` проходит и через `vkCallsFailure` (у него есть
`Unwrap`), и через `fmt.Errorf("…: %w", …)`.

---

<a id="proposal-2-baton"></a>

## 2. Мёртвый хеш первой группы останавливает все остальные

**Patch:** [2-baton.patch](upstream-repro/2-baton.patch).
**Reproduction:** [repro_test.go](upstream-repro/repro_test.go),
`TestReproGroupPassesBatonOnCredsFailure`; isolated input `patched-baton`.

**Что видно.** Больше 9 воркеров (например, 18) — значит, несколько групп со своими хешами.
Если хеш первой группы мёртвый, туннель не поднимается вовсе, хотя хеши остальных групп
рабочие. Клиент при этом и не завершается — просто ждёт.

**Почему.** Группы стартуют по очереди: каждая ждёт сигнала от предыдущей (`waitReady`), а
та подаёт его (`close(signalReady)`), когда получила креды. Если креды получить не удалось,
`WorkerGroup` (`group.go`) выходит, не подав сигнала, и следующая группа ждёт его вечно.

**Как воспроизвести.** Два хеша, первый выдуманный, 18 воркеров. Без сети: вызвать
`WorkerGroup` с уже отменённым контекстом (креды не получить) и своим каналом `signalReady` —
после возврата канал не закрыт.

**Как исправить.** В ветке ошибки кредов, перед `return`:

```go
if signalReady != nil {
	close(signalReady)
}
```

---

<a id="proposal-3-config-quota"></a>

## 3. Конфиг WireGuard держится на одном воркере — и тот может упереться в квоту TURN

**Patch:** [3-config-quota.patch](upstream-repro/3-config-quota.patch).
**Reproduction:** [repro_test.go](upstream-repro/repro_test.go), `TestReproConfigDespiteQuota`;
isolated input `patched-config-quota`.

**Symptom.** After network recovery, the tunnel can keep waiting for config even though
other workers have established sessions. A successful transport alone does not let a
worker reconsider its initial decision not to request config.

**Почему.** Конфиг WireGuard у сервера просит один воркер группы — тот, кто первым взял флаг
`configRequestInFlight`. Флаг берётся до входа в `RunSession`, а решение «просить конфиг или
нет» каждый воркер принимает один раз, в начале сеанса.

Если воркер с флагом получил от VK `486 Allocation Quota Reached` (квоту TURN держат
аллокации сеансов, умерших вместе с сетью), он отпускает флаг и ждёт 30–60 секунд до новой
попытки. Остальные воркеры к этому моменту уже вошли в сеансы, решив конфиг не просить. Пока
воркер с 486 не повторит попытку — и пока квота не освободится, — конфиг не просит никто, и
туннель без него не поднимается.

**Offline evidence.** The current stand uses two workers. Its TURN quota handler rejects
the config-owning allocation with error 486 while another worker can establish a DTLS
session. The compiled baseline reaches `конфиг не пришёл за 15 с` with positive 486 and
session counts and `GETCONF 0`. With patch 3 alone, the other worker requests and delivers
config. This does not require or establish a live exhausted-quota run.

**Fix.** Let every worker entering a session request config until config has been delivered;
remove `configRequestInFlight`. This patch changes only upstream `go_client/group.go`:

```go
getConf := shouldGetConfig && atomic.LoadInt32(&configSent) == 0
…
if getConf && configDelivered {
	atomic.StoreInt32(&configSent, 1)
}
```

Это безопасно: на повторный `GETCONF` сервер отдаёт тот же конфиг — ключи устройства
создаются один раз (`server/connections.go`), а лишний доставленный конфиг `RunSession` уже
отбрасывает («Конфиг уже был доставлен другим воркером»). Цена — `GETCONF` вместо `AUTH` у
воркеров, стартовавших до получения конфига.

**Related local behaviour.** Upstream `main.go` gives the config channel only to the first
group (`isFirst`). Passing it to every group is a separate local wrapper behaviour in
`internal/core/run.go`. The accepted upstream patch and reproduction cover per-worker
selection within one group; they do not patch upstream `main.go` or test an all-group wrapper.

Quota-test teardown closes the peer and observes completed sessions before cancelling and
joining the group. It stays independent of proposal 5's cancellation fix; the isolated
input does not apply patch 5.

---

<a id="proposal-4-dropped-packets"></a>

## 4. Пакеты, выброшенные ядром, нигде не считаются

**Patch:** [4-dropped-packets.patch](upstream-repro/4-dropped-packets.patch).
**Reproduction:** [repro_test.go](upstream-repro/repro_test.go), `TestReproDroppedUp`,
`TestReproDroppedDown`, `TestReproDroppedStatsOutput`; isolated input `patched-counters`.

Это не ошибка, а недостающий счётчик.

**Что видно.** При поднятом туннеле теряется 5–20% UDP, и понять, теряет ли их само ядро, не
по чему.

**Почему.** Ядро выбрасывает пакет, когда очередь полна, и в десктопном режиме молча:
«туда» — `readLoop` диспетчера, когда очереди всех воркеров полны (счётчик
`tunDroppedCount` есть, но только при `tunFile != nil`, то есть в rawtun на Android);
«обратно» — `RunSession`, ветка `default` у `d.ReturnCh <- pkt`, счётчика нет совсем.

**Как исправить.** Поля `DroppedUp`, `DroppedDown atomic.Uint64` в `Stats`;
`d.stats.DroppedUp.Add(1)` в ветке «все workers перегружены», `stats.DroppedDown.Add(1)` в
ветке `default`; числа — в строку `[СТАТИСТИКА]`. Поведение не меняется.

**Offline evidence.** Uplink counts once only after every eligible queue rejects a packet;
successful delivery, fallback and the no-worker path do not inflate the saturation count.
Downlink counts only the full-`ReturnCh` branch, not cancellation or keepalive filtering.
The tests exercise the real dispatcher/session branches, verify exact directional counts
and successful delivery after freeing a slot, and retain packet-delivery behaviour.

Baseline-compatible reflection lets the whole test file compile without the new fields.
The unpatched tests fail specifically with `missing drop counter: Stats.DroppedUp`,
`missing drop counter: Stats.DroppedDown`, and
`statistics output omits drop counters: want DroppedUp=7 and DroppedDown=11`.
The patched tests pass and observe `Dropped: ↑7 / ↓11` in normal `Stats.RunLoop` standard-log
output, alongside the existing traffic and worker information. CLI-only `EventStats` delivery
is local integration and is not part of this upstream patch.

---

<a id="proposal-5-stop"></a>

## 5. Остановка клиента зависает на 45 секунд или на 30 минут

**Patch:** [5-stop.patch](upstream-repro/5-stop.patch).
**Reproduction:** [repro_test.go](upstream-repro/repro_test.go),
`TestReproStopDuringConfigRequest` and `TestReproStopRightAfterSessionStart`;
isolated inputs `patched-stop` and `patched-stop-injected`, respectively.

**Что видно.** Остановка (Ctrl+C) или перезапуск после обрыва иногда не происходят сразу:
клиент висит, пока не нажать Ctrl+C ещё раз.

**Почему.** Клиент останавливается, только когда вышли все воркеры. Воркер узнаёт об
остановке так: `stopConn` (`context.AfterFunc` в `RunSession`) ставит его соединению дедлайн
«сейчас», и чтение из соединения тут же завершается. В двух местах этот механизм не
срабатывает.

*а) Остановка во время запроса конфига — до 45 секунд.* `stopConn` ставится после запроса
конфига, а `RequestConfig` ждёт ответа до 45 секунд. Если остановка пришла, пока воркер ждёт
ответа, ответа уже не будет — relay снят, — и воркер ждёт все 45 секунд.

*б) Остановка сразу после начала сеанса — до 30 минут.* Чтение сеанса перед каждым `Read`
ставит соединению свой дедлайн — +30 минут. Если остановка пришла раньше, чем чтение успело
его поставить, дедлайн «сейчас» от `stopConn` затирается дедлайном +30 минут, и `Read` ждёт
30 минут.

**Offline evidence.**

- The stop-during-config test waits until the stand receives `GETCONF`, then cancels without
  supplying config. The baseline reaches `WorkerGroup не вернулась за 5s после отмены`;
  patch 5 alone makes the group return within the test's bound.
- The deadline test deliberately skips in ordinary inputs with
  `нужна вставленная задержка: см. run.sh`. In separate disposable inputs, the runner inserts
  exactly one `time.Sleep(300 * time.Millisecond)` before the read-deadline anchor and sets
  `REPRO_INJECT_DELAY=1`. The test cancels 100 ms after config receipt. The baseline reaches
  both bounded no-return assertions; patch 5 alone and the complete patched set pass.

The injected sequence demonstrates the ordering bug; it is not a latency measurement or
an uninjected production trace. The original unfixed injected client may remain blocked
until its disposable test process exits. That retained bug diagnostic is distinct from
stand setup and teardown failures, which the runner rejects.

**Как исправить.**

а) На время запроса конфига — своя отмена, которая закрывает соединение (закрытие прерывает
чтение независимо от дедлайнов). Перед запросом конфига:

```go
stopConfig := context.AfterFunc(sessCtx, func() {
	_ = activeConn.SetDeadline(time.Now())
	_ = activeConn.Close()
})
```

и `stopConfig()` перед строкой `[READY] Туннель готов к работе`.

б) В чтении сеанса — проверить отмену после установки дедлайна:

```go
_ = activeConn.SetReadDeadline(time.Now().Add(sessionReadTimeout))
if sessCtx.Err() != nil {
	return
}
```

Гонки здесь больше нет: `stopConn` срабатывает только после отмены. Значит, либо проверка
увидит отмену, либо `stopConn` поставит дедлайн «сейчас» позже нашего +30 минут.

---

<a id="proposal-6-captcha-timeouts"></a>

## 6. Give human captcha windows 180 seconds

**Patch:** [6-captcha-timeouts.patch](upstream-repro/6-captcha-timeouts.patch).
**Reproduction:** [repro_test.go](upstream-repro/repro_test.go),
`TestReproCaptchaManualTimeout`, `TestReproCaptchaSelectedTimeout`,
`TestReproCaptchaAutoTimeout`; isolated input `patched-captcha-timeouts`.

**Symptom and cause.** Upstream allows only 60 seconds for the manual fallback and 120
seconds for the selected window. Both are human interaction paths; their configured windows
are shorter than the intended 180 seconds. Automatic attempts use a separate 10-second window.

**Fix.** Change `captchaManualWebViewTimeout` and `captchaSelectedWebViewTimeout` to
`180 * time.Second` in upstream `go_client/creds.go`. Keep `captchaAutoWebViewTimeout` at
10 seconds. Request modes, solver branches, attempt limits and response handling stay intact.

**Offline evidence.** The tests check compiled constants, bounded AST contracts for the
five actual manual/selected/auto request call sites and the request helper's consumed deadline.
They also call the real upstream request helper and direct `wv` selected path, supplying an
immediate synthetic response after the exact `CAPTCHA_SOLVE` marker. Profile side effects,
stdout/log capture, mode and response-channel state are isolated and restored.

The baseline fails with `manual captcha timeout: got 1m0s, want 3m0s` and
`selected captcha timeout: got 2m0s, want 3m0s`. The 10-second auto control passes. Patch 6
alone passes all three tests while leaving the old diagnostic intact. These checks establish
configured durations and their connection to the waiter, not elapsed human-window expiry.
They do not execute the manual native-solver chain, contact VK or open a browser.

---

<a id="proposal-7-captcha-wv-log"></a>

## 7. Describe the `wv` request without an Android-settings claim

**Patch:** [7-captcha-wv-log.patch](upstream-repro/7-captcha-wv-log.patch).
**Reproduction:** [repro_test.go](upstream-repro/repro_test.go), `TestReproCaptchaWVLog`;
isolated input `patched-captcha-wv-log`.

**Symptom and cause.** The selected-mode diagnostic says the mode came from Android settings,
although the same core can be used outside Android. The request itself already uses the
generic `selected` mode.

**Fix.** Replace only that standard-log message in `go_client/creds.go` with:

```text
[STREAM %d] [КАПЧА] WBV: wv mode: requesting captcha window (attempt %d)
```

Keep `[КАПЧА] WBV:`: the pinned Android `TunnelManager.kt` consumer uses it to route the
record into the ordinary captcha log. Removing it would hide this non-error message unless
detailed logging were enabled. The Android consumer and separate `CAPTCHA_SOLVE` request/
response handling remain unchanged; this patch changes no captcha flow or timeout.

**Offline evidence.** The real `wv` branch requests one selected window and receives an
immediate synthetic token. The baseline fails with `wv diagnostic still claims Android settings`.
Patch 7 alone passes while the selected timeout remains 120 seconds. Applied-source comparison
confirms one changed diagnostic line; the whitespace-safe patch's larger representation does
not imply wider source changes.

---

## Не предлагаем

- Канал ответа капчи: одновременных просьб нет — `fetchVkCredsSerialized` держит
  `vkRequestMu` на всё время, вместе с ожиданием капчи (см. [`architecture.md`](architecture.md), §08 и раздел задачи в §11).

## Для нас

The [vendorcore rules](../scripts/vendorcore/rules.go) combine upstream-intended changes with
local adaptation. Proposals describe coherent behavioural changes, not one item per source
replacement. Upstream patches use upstream facilities; local event delivery, package
extraction, CLI scaffolding and socket hooks do not need separate upstream proposals.

A transformation can be mixed: the group failure rule contains local `emitHashFailed`
integration and proposal 2's upstream baton handoff. Likewise, local `EventStats` and the
all-group config wrapper must not be mistaken for the upstream-native statistics patch or
proposal 3's per-worker patch. The
[automatic accounting task](roadmap/polish/05-upstream-proposals/04-transformation-coverage/plan.md)
will check explicit classification and proposal/artifact references across the transformation
inventory, without equating transformation and proposal counts.

The reproduction README records the exact input/test associations and validation limits.
The accepted complete matrix and the final targeted artifact checks are distinct evidence;
neither implies live VK/quota/platform verification or native Windows execution.
