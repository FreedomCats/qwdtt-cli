# Воспроизведение ошибок ядра для апстрима

Tests and patches for the coherent changes in
[`upstream-proposals.md`](../upstream-proposals.md), against upstream
SpaceNeuroX/proxy-turn-vk-android `go_client` at full pin
`fae121efc3ef57b633516601d3c0d6b1be1fde7c` (`fae121e`). The project follows stable `master`;
this set does not require investigating or upgrading to `develop`.

## Что внутри

| File | Purpose |
|---|---|
| [repro_test.go](repro_test.go) | Twelve top-level upstream `package main` tests, compiled with `upstream_repro` |
| [1-closed-call.patch](1-closed-call.patch) | [Proposal 1](../upstream-proposals.md#proposal-1-closed-call): typed closed-call error → `dead` |
| [2-baton.patch](2-baton.patch) | [Proposal 2](../upstream-proposals.md#proposal-2-baton): pass the group baton after credential failure |
| [3-config-quota.patch](3-config-quota.patch) | [Proposal 3](../upstream-proposals.md#proposal-3-config-quota): per-worker config selection in `group.go` |
| [4-dropped-packets.patch](4-dropped-packets.patch) | [Proposal 4](../upstream-proposals.md#proposal-4-dropped-packets): atomic uplink/downlink counters and upstream statistics output |
| [5-stop.patch](5-stop.patch) | [Proposal 5](../upstream-proposals.md#proposal-5-stop): cancellation during config reads and the read-deadline race |
| [6-captcha-timeouts.patch](6-captcha-timeouts.patch) | [Proposal 6](../upstream-proposals.md#proposal-6-captcha-timeouts): manual/selected 180-second windows, auto unchanged |
| [7-captcha-wv-log.patch](7-captcha-wv-log.patch) | [Proposal 7](../upstream-proposals.md#proposal-7-captcha-wv-log): accurate `wv` diagnostic retaining the Android log marker |
| [run.sh](run.sh) | Bash entry point that builds and runs the Go runner with offline settings |
| [scripts/upstreamrepro/](../../scripts/upstreamrepro/) | Disposable pinned exports, explicit patch/test inputs and exact JSON result validation |

Patches are relative to the pinned upstream repository root (`go_client/...`). The runner
checks and applies each patch alone and the complete set in order, always in fresh inputs.
All artifacts use upstream constants, channels, types and standard logging. They do not
require CLI-only `EventStats`, `emit`, `logf` or extraction scaffolding.

## Как запустить

Prepare Bash, Git, an installed/cached Go 1.26.2 toolchain, local upstream Git objects
containing the full pin and all required cached Go modules. The source argument is mandatory.
Missing objects, toolchain or modules are prerequisite errors; there is no clone, fetch,
implicit toolchain/module download or network fallback.

From the project root, replace the placeholders with prepared local paths:

```sh
export PATH="<go1.26.2-bin>:$PATH"
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off
export GOMODCACHE="<prepared-module-cache>"
export TMPDIR="<temporary-parent>"
bash doc/upstream-repro/run.sh "<prepared-local-source>"
```

The cache and existing writable temporary parent must be outside both repositories. Put
the selected toolchain's bin directory first on PATH before bootstrap; the wrapper also
enforces offline settings before building its runner and for subprocesses. The current
entry point requires Bash; Windows cross-compilation alone does not validate its runtime.

The prepared source may be dirty or have a different current HEAD. Private Git metadata
reads its object store and exports the pinned committed `go_client`, independently of the
working tree and without the source's remotes or lazy fetching. Its HEAD, index, configuration
and files stay untouched. Patch checks/application, test copying, module preparation
(`go mod tidy`) and delay injection occur only in runner-owned disposable directories.
Cleanup removes owned inputs, including after handled interruption; no caller-tree reset,
checkout, cleaning or manual test-file installation is needed.

The tests use loopback only: a local Pion TURN server, a DTLS qWDTT stand and synthetic cached
credentials. Captcha requests receive synthetic tokens. No VK, browser, user server or live
quota/platform action is part of this command. The runner selects only the tagged
reproductions, not the ordinary upstream or project test suites.

## Что должно получиться

Each input compiles the entire tagged file before running its anchored test selection.
The normal entry point runs this explicit matrix; the names below describe its internal
inputs, not a separate focused-input command-line interface.

| Input | Exact patches | Selected tests and expected result |
|---|---|---|
| `baseline` | None | All twelve tests: the exact failures below, passing auto control, ordinary deadline skip |
| `patched` | 1, 2, 3, 4, 5, 6, 7 in artifact-table order | All twelve tests pass except the same ordinary deadline skip |
| `baseline-injected` | None | `TestReproStopRightAfterSessionStart`: both bounded cancellation assertions |
| `patched-injected` | 1, 2, 3, 4, 5, 6, 7 in artifact-table order | `TestReproStopRightAfterSessionStart`: pass |
| `patched-closed-call` | `1-closed-call.patch` | `TestReproClosedCallIsDead`: pass |
| `patched-baton` | `2-baton.patch` | `TestReproGroupPassesBatonOnCredsFailure`: pass |
| `patched-config-quota` | `3-config-quota.patch` | `TestReproConfigDespiteQuota`: pass |
| `patched-counters` | `4-dropped-packets.patch` | `TestReproDroppedUp`, `TestReproDroppedDown`, `TestReproDroppedStatsOutput`: pass |
| `patched-stop` | `5-stop.patch` | `TestReproStopDuringConfigRequest`: pass |
| `patched-stop-injected` | `5-stop.patch` | `TestReproStopRightAfterSessionStart`: pass |
| `patched-captcha-timeouts` | `6-captcha-timeouts.patch` | `TestReproCaptchaManualTimeout`, `TestReproCaptchaSelectedTimeout`, `TestReproCaptchaAutoTimeout`: pass |
| `patched-captcha-wv-log` | `7-captcha-wv-log.patch` | `TestReproCaptchaWVLog`: pass |

The ordinary baseline must compile and produce these results:

| Test | Required baseline evidence |
|---|---|
| `TestReproClosedCallIsDead` | All three existing `статус "error" ... ожидался dead` diagnostics: direct, vkcalls and `%w`-wrapped errors |
| `TestReproGroupPassesBatonOnCredsFailure` | `эстафета не передана: следующая группа ждала бы вечно` |
| `TestReproConfigDespiteQuota` | `конфиг не пришёл за 15 с`, positive error-486 and session counts, `GETCONF 0` |
| `TestReproDroppedUp` | `missing drop counter: Stats.DroppedUp` |
| `TestReproDroppedDown` | `missing drop counter: Stats.DroppedDown` |
| `TestReproDroppedStatsOutput` | `statistics output omits drop counters: want DroppedUp=7 and DroppedDown=11` |
| `TestReproStopDuringConfigRequest` | `WorkerGroup не вернулась за 5s после отмены`, after the stand receives `GETCONF` |
| `TestReproStopRightAfterSessionStart` | Skip with `нужна вставленная задержка: см. run.sh` |
| `TestReproCaptchaManualTimeout` | `manual captcha timeout: got 1m0s, want 3m0s` |
| `TestReproCaptchaSelectedTimeout` | `selected captcha timeout: got 2m0s, want 3m0s` |
| `TestReproCaptchaAutoTimeout` | Pass for the unchanged 10-second window |
| `TestReproCaptchaWVLog` | `wv diagnostic still claims Android settings` |

Expected baseline failures are printed normally. Overall runner success is exit status zero
and `All selected baseline and patched expectations matched.` The validator checks process,
package and test results and exact assertion diagnostics. Wrong or missing assertions,
unexpected baseline passes, patched failures, extra/missing tests and unexpected skips fail
the runner. Compilation, bind/handshake, capture, profile, teardown or other setup errors,
panics and process timeouts cannot substitute for the intended upstream bug evidence.

## Про тест 5б

The race requires cancellation before the Reader installs its long deadline. Ordinary inputs
intentionally skip this test rather than treating an unforced pass as proof. Only injected
inputs receive exactly one `time.Sleep(300 * time.Millisecond)` immediately before the
read-deadline anchor and `REPRO_INJECT_DELAY=1`. The test cancels 100 ms after config receipt.
The injected baseline must emit both existing no-return assertions, including the subsequent
bounded wait; patch 5 alone and the complete injected patch set must pass.

Injection stays disposable and is not a production fix or a claim about observed latency.
The original unfixed injected client can retain its bounded blocked-client diagnostic until
the disposable test process exits. New stand teardown failures are not allowed as baseline
evidence, and successful patched groups must complete.

## Как сделано

The stand exercises actual upstream dispatcher/session/group paths with loopback transport.
The quota test uses two workers and patch 3's per-worker `group.go` change; the local
all-group config wrapper in `internal/core/run.go` is outside the patch. Quota cleanup closes
the peer, observes completed sessions, then cancels and joins the group, keeping it independent
of patch 5. Stand and dispatcher waits are bounded and close errors are checked. Observation
of terminal TURN reads does not claim a synchronous public join of every private Pion loop.

Counter reflection allows the whole tagged file to compile on the baseline. Patched checks
exercise both saturation branches, successful delivery/fallback and normal `Stats.RunLoop`
output containing `Dropped: ↑7 / ↓11`. The upstream evidence surface is standard logging;
CLI-only `EventStats` is separate local integration.

Captcha checks combine compiled constants, bounded AST contracts for all five request call
sites and the wait deadline, and immediate synthetic responses through real upstream helpers.
They do not wait through a human deadline or execute the manual native-solver chain. Patch 6
alone leaves the old diagnostic intact; patch 7 alone passes with the original 120-second
selected constant. The final message is
`[STREAM %d] [КАПЧА] WBV: wv mode: requesting captcha window (attempt %d)`.
The retained marker preserves ordinary Android log routing; the Android consumer and separate
`CAPTCHA_SOLVE` request/response flow are unchanged.

## Validation scope

The accepted complete normal-wrapper run covered 12 inputs and 38 selected test cells:
25 passes, 11 exact baseline failures and two intentional ordinary deadline skips. This is
historical accepted evidence. Final artifact acceptance added targeted checks of the chosen
diagnostic alone and in the complete captcha patch set, isolated quota ordinary/forced-delay
cleanup, counter/downlink, retained cancellation and shared stand/dispatcher paths. It did
not rerun the complete matrix or replay its unavailable original logs. Applied-source checks
confirmed the intended patch scope, and prepared-source preservation was checked.

Focused and complete runner tests, race tests, vet, offline project build, formatting and
wrapper syntax passed. Windows/amd64 cgo-disabled vet, cross-build and test-binary compilation
also passed. Those results do not establish native Windows execution, the actual Git
native-link-refusal branch, non-Unix cancellation runtime or a subsequent green CI run.
Live VK/exhausted-quota/platform runs, browser checks, native captcha solving and elapsed
human-window expiry remain unperformed. The ordinary outbound project/upstream suites were
not run for artifact acceptance.
