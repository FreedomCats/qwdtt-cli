---
kind: task
status: done
detailed: true
depends: [01-drop-counters, 02-captcha-evidence]
base: 9b238f9
---

# Pinned patch-set validation

## Task

Complete and execute the safe runner's real pinned matrix after both new asset sets are
accepted. Deliver reproducible compatibility evidence for every patch alone and for all
patches together, including the retained closed-call, baton, config/quota and cancellation
reproductions. Follow [the parent stage](../plan.md) and the
[safe runner](../../01-repro-runner/plan.md),
[counter](../01-drop-counters/plan.md), [captcha](../02-captcha-evidence/plan.md) and
[Windows fixture repair](../../05-windows-runner-ci/plan.md) outcomes, within
[architecture sections R7, 06 and 08](../../../../../architecture.md).

The normal implementation area is `scripts/upstreamrepro/{run.go,run_test.go}`: complete
the per-input patch/test tables and their fixture coverage. Correct
`doc/upstream-repro/repro_test.go` or an existing patch only when a real pinned check
exposes an artifact defect, preserving the intended assertion and proposal scope. This
is one integration result, normally two runner files plus any narrowly justified asset
corrections, bounded by the current reproduction/patch set. Do not add another runtime
fix or broaden the proposal set to make a run pass.

The accepted `testRuns()` (`scripts/upstreamrepro/run.go:128`) already has explicit patch
lists, complete ordinary/injected inputs, `patched-counters`, `patched-captcha-timeouts`
and `patched-captcha-wv-log`. The remaining implementation is the isolated inputs for the
retained patches and their fixture coverage; the remaining evidence is the real retained,
individual and combined matrix. Prior focused
counter/captcha passes do not establish that matrix. Keep the full pin
`fae121efc3ef57b633516601d3c0d6b1be1fde7c` and stable `master` policy.

## Steps

1. Confirm the accepted inputs and offline prerequisites before changing expectations.
   Read `patches`, `cases`, `testRuns`, `runInputs`, `injectDelay` and `validateResult` in
   `scripts/upstreamrepro/run.go`, their fixtures in `run_test.go`, and the actual artifacts
   under `doc/upstream-repro/`. Use the prepared source/cache locations from private evidence,
   not paths copied into published nodes. Select installed Go 1.26.2 directly and put its
   bin directory first on PATH for subprocesses. Apply
   `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off` before bootstrap and every Go
   invocation; retain the runner's stronger offline environment and disabled lazy fetching.
   For acceptance, use an unregistered disposable export of the accepted project runner/assets,
   carrying any final reviewed task edits into that export. Use the accepted private Git
   metadata/export path for fresh disposable upstream inputs. Read upstream source from its
   full-pin local objects, never its current HEAD or working-tree contents. Privately record
   the prepared source's HEAD, raw index, configuration, tracked/untracked contents and modes
   before real checks, using read-only Git with optional locks disabled. A dirty source does
   not need cleaning. Missing pin objects, toolchain or cached modules identify validation
   gaps; do not clone, fetch, download or rewrite either repository's module configuration.
   The existing `go mod tidy` preparation is allowed only inside disposable upstream inputs.
2. Complete the matrix in `scripts/upstreamrepro/run.go`. Associate each `reproCase` with
   its existing patch filename, replacing the three category booleans with a small patch
   identity field; retain `injected` for the deadline case. The passing auto control belongs
   to the timeout patch. Derive selected expectations from those associations while keeping
   each `testRun.patches` list explicit. Retain the existing input names and add these named
   isolated inputs, each exported afresh from the full pin:

   | Individual input | Exact patch list | Selected reproductions |
   |---|---|---|
   | `patched-closed-call` | `1-closed-call.patch` | `TestReproClosedCallIsDead` |
   | `patched-baton` | `2-baton.patch` | `TestReproGroupPassesBatonOnCredsFailure` |
   | `patched-config-quota` | `3-config-quota.patch` | `TestReproConfigDespiteQuota` |
   | `patched-counters` | `4-dropped-packets.patch` | `TestReproDroppedUp`, `TestReproDroppedDown`, `TestReproDroppedStatsOutput` |
   | `patched-stop` | `5-stop.patch` | `TestReproStopDuringConfigRequest` |
   | `patched-stop-injected` | `5-stop.patch` | `TestReproStopRightAfterSessionStart`, with delay injection and `REPRO_INJECT_DELAY=1` |
   | `patched-captcha-timeouts` | `6-captcha-timeouts.patch` | `TestReproCaptchaManualTimeout`, `TestReproCaptchaSelectedTimeout`, `TestReproCaptchaAutoTimeout` |
   | `patched-captcha-wv-log` | `7-captcha-wv-log.patch` | `TestReproCaptchaWVLog` |

   All selected individual checks must pass. Preserve `baseline` and `patched` with the
   complete ordinary selection, including the deadline test's intentional skip. Preserve
   `baseline-injected` and `patched-injected` selecting only the deadline reproduction:
   no patches on the former, the complete declared patch list on the latter. Only injected
   inputs receive both the source delay and environment flag. Keep the anchored `pattern`,
   full tagged-file copying/compilation, serial execution, existing process limits and JSON
   validator. Do not add a public selection interface, another runner, all pairwise patch
   combinations, a fixed proposal count or a general matrix framework.
3. Extend `TestExpectationMatrix`, `TestRejectResults`, the synthetic executor and related
   fixtures in `scripts/upstreamrepro/run_test.go`. Find inputs with `fixtureRun` by name,
   including `TestRejectResults` (`scripts/upstreamrepro/run_test.go:304`), which currently
   uses `runs[0]`/`runs[2]`; preserve the accepted name-based counter lookup. Assert unique
   input names, unique test names within each input, nonempty selections, exact ordered
   patch lists and exact test membership for every input above
   and for the ordinary/injected inputs. Derive inventory coverage from the declared
   patches/cases rather than a literal input/proposal count. Require every associated case
   in its intended single-patch mode, including patch 5's separate ordinary/injected inputs;
   reject missing, extra or duplicate input/test associations. The executor must continue
   checking every synthetic patch's actual applied state, the complete copied test file,
   exact Go arguments, injection marker count and environment flag. Matrix assertions must
   independently specify the intended patch/test
   associations so an incorrect association cannot validate itself.
   Exercise rejection for the retained cases as well as counters/captcha: wrong or missing
   assertions, unexpected baseline success, patched failure, unexpected/missing/wrong-reason
   skips, absent/extra tests, build errors, panics and process timeouts. Include a loopback
   handshake/setup diagnostic accompanying an otherwise correct expected failure. Recompute
   package/process results after mutating test statuses where necessary so a stale exit code
   does not mask which validation rejected the evidence. Preserve exact assertion allowlists,
   `TestDelayInjection`'s zero/duplicate/already-injected anchor rejection, offline bootstrap
   and subprocess checks, archive containment, all dirty-source/cleanup modes, strict resolved
   descendant ownership and the Windows alias regression. No fixture runs the real upstream.
4. Check artifact applicability early on disposable full-pin exports through the same
   private export and patch-application helpers. For each individual patch, require
   `git apply --check` and application alone; separately check and apply the complete set
   in the declared `patches` order. Inspect the actual applied source differences
   before module preparation, copied tests or injection. In particular, patches 2/3 both
   touch `group.go`, patches 4/5 touch `session.go`, and patches 6/7 touch `creds.go`.
   Confirm the counter/log additions, two human constants and one diagnostic line retain
   their accepted scope despite the whitespace-safe patch representation. If a real check
   exposes a reproduction/patch defect, correct only that artifact in
   `doc/upstream-repro/repro_test.go` or its existing patch, explain the cause, preserve the
   intended bug assertion and repeat affected checks on the final bytes. Do not modify CLI
   runtime, vendorcore, dependencies, extraction scaffolding or another proposal to repair
   the evidence. Report a larger defect or invalid independence premise before expanding
   this task; do not quietly add another fix or an extra dependency patch to an isolated input.
5. Execute the final normal entry point, `bash doc/upstream-repro/run.sh <prepared-local-source>`,
   from the final disposable project export with the direct toolchain on PATH and a temporary parent
   outside source/project. This must execute all `testRuns()` through the real `runInputs`
   preparation, patch checks/application, complete tagged-file compilation, anchored test
   selection and exact JSON validation. Rebuild the runner from the final implementation;
   old focused harness binaries or synthetic outputs cannot establish new matrix cells.
   Retain full private command/JSON/process diagnostics. Require the existing baseline
   assertion patterns, not merely failing process exits:

   | Baseline reproduction | Required ordinary baseline evidence |
   |---|---|
   | `TestReproClosedCallIsDead` | The three existing `статус "error" ... ожидался dead` diagnostics for `как есть`, `путь vkcalls` and `обёрнута в %w` |
   | `TestReproGroupPassesBatonOnCredsFailure` | `эстафета не передана: следующая группа ждала бы вечно` |
   | `TestReproConfigDespiteQuota` | The existing `конфиг не пришёл за 15 с` assertion with positive `486` and session counts and `GETCONF 0` |
   | `TestReproStopDuringConfigRequest` | `WorkerGroup не вернулась за 5s после отмены`, with only the existing elapsed-return diagnostic allowed |
   | `TestReproStopRightAfterSessionStart` | `SKIP` with `нужна вставленная задержка: см. run.sh` |
   | `TestReproDroppedUp` | `missing drop counter: Stats.DroppedUp` |
   | `TestReproDroppedDown` | `missing drop counter: Stats.DroppedDown` |
   | `TestReproDroppedStatsOutput` | `statistics output omits drop counters: want DroppedUp=7 and DroppedDown=11`, retaining the existing statistics-line allowlist |
   | `TestReproCaptchaManualTimeout` | `manual captcha timeout: got 1m0s, want 3m0s` |
   | `TestReproCaptchaSelectedTimeout` | `selected captcha timeout: got 2m0s, want 3m0s` |
   | `TestReproCaptchaAutoTimeout` | `PASS` for the unchanged 10-second control |
   | `TestReproCaptchaWVLog` | `wv diagnostic still claims Android settings` |

   The complete ordinary patched input must pass the same selection except for that exact
   deadline skip. In each injected input, insert exactly one 300 ms delay immediately before
   the existing read-deadline anchor, with the original marker and `REPRO_INJECT_DELAY=1`.
   `baseline-injected` must fail with both existing cancellation assertions: no return within
   5 seconds and no return after the subsequent bounded wait, allowing only the original
   cleanup diagnostic. `patched-injected` and patch-5-only injected must pass. Retain the
   real loopback TURN/DTLS stand: quota/config checks must actually reject an allocation and
   establish sessions; cancellation-during-config must actually reach `GETCONF`. Bind,
   handshake, setup, compilation, capture/teardown failures, panics and timeouts are not bug
   evidence. Unexpected passes/skips require investigation, not weaker expectations or
   repeated runs until one passes. Compare the prepared source snapshot after execution and
   verify runner-owned cleanup. Record each unavailable or unperformed input/test cell and
   its concrete prerequisite/error; do not replace it with fixture or apply-only evidence.
6. Run the bounded acceptance checks with the same direct toolchain/offline settings in the
   final disposable project export. Focus on the changed matrix and retained safeguards:

   ```sh
   go test -count=1 -run '^(TestExpectationMatrix|TestRejectResults|TestCounterResultValidation|TestCaptchaResultValidation|TestCommandDirectoryOwnership|TestWindowsTempParentAlias|TestDirtySourceAndCleanup|TestDelayInjection|TestOfflineEnvironment)$' ./scripts/upstreamrepro
   go test -count=1 ./scripts/upstreamrepro
   go test -race -count=1 ./scripts/upstreamrepro
   go vet ./scripts/upstreamrepro
   go build ./...
   gofmt -l scripts/upstreamrepro/run.go scripts/upstreamrepro/run_test.go
   bash -n doc/upstream-repro/run.sh
   git diff --check
   ```

   Require no formatting output; include any corrected Go artifact in the format check.
   Check the intended final repository diff/artifact whitespace as well as the applied source scope;
   an export without Git metadata does not replace the repository's `git diff --check`.
   These runner fixtures include wrapper/process cleanup but do not replace the real matrix
   in step 5. Do not run the ordinary full project/upstream test suite, native Windows,
   browser, VK, privileged or live checks, or manually rerun CI. The Windows-only alias skip
   and any cross-compilation result do not establish native Windows runtime or green CI.
7. Give the coordinator/lead a sanitized handoff for
   [`03-proposal-documentation`](../../03-proposal-documentation/plan.md): actual patch/test
   names, each input's exact patch list and selection, the full pin, compiled baseline
   assertions/control/skip, individual/combined results, source-preservation/cleanup checks
   and every remaining gap. Explain that counters are observed in upstream `Stats.RunLoop`
   standard-log output, including `Dropped: ↑7 / ↓11`, whereas CLI `EventStats` delivery is
   local integration. Patch 3 changes per-worker selection in upstream `group.go`; passing
   the config channel to every group lives in locally owned `internal/core/run.go` and is
   not patched in upstream `main.go`. Captcha evidence remains compiled duration and AST
   call-site/wait-deadline contracts plus immediate synthetic upstream responses, without
   elapsed human-window expiry or the manual native-solver chain. Keep full logs, source/cache
   paths and execution details private. Proposal prose and the reproduction README stay
   lead-owned in the documentation sibling; automatic transformation classification stays
   in its later sibling. Do not edit those documents, journal or completed roadmap nodes.

## Done when

- Every proposal patch is checked and applied alone to a fresh, isolated full-pin input,
  and its selected reproductions pass there. Explicit case/patch associations drive this
  matrix without a frozen proposal count or a second runner. In particular, proposal 3
  tests the existing per-worker `group.go` change and makes no claim to patch the local
  all-group `internal/core/run.go` wrapper or upstream `main.go` for that wrapper.
- The entire patch set applies together, and the complete ordinary/injected baseline and
  patched matrix runs against prepared local objects at
  `fae121efc3ef57b633516601d3c0d6b1be1fde7c`. All tagged reproductions compile on all
  selected inputs. Baseline failures match their exact bug/missing-feature assertions;
  always-passing controls pass on both inputs and all expected patched checks pass.
- `testRuns()` contains the named isolated selections above, including both patch-5 modes;
  the ordinary/injected complete inputs retain their exact semantics. Fixtures assert exact
  patch/test associations, inventory coverage and input identities without positional lookup
  assumptions, a frozen proposal count or an all-or-no-patches model.
- The existing loopback TURN/DTLS stand still exercises quota/config delivery and both
  cancellation bugs. The deadline test skips only in the ordinary inputs, with its
  original reason; exact-once delay injection and `REPRO_INJECT_DELAY=1` require a real
  baseline failure and patched pass in the injected inputs. Setup/bind/handshake errors,
  build failures, panics, timeouts or unexpected passes/skips are rejected, not counted
  as evidence.
- All source exports and mutations use the accepted private metadata/disposable-copy
  path. A prepared source may be dirty; its HEAD, index, files and local configuration
  remain untouched. Patch application, module preparation, injection and cleanup never
  run in the caller's tree. Extended fixture coverage preserves source/cleanup checks,
  offline bootstrap/subprocess settings and precise process/package/test validation.
- Real runs use the installed compatible toolchain and cached dependencies, with only
  loopback networking and no live services, VK, browser or outbound/download fallback.
  Missing prerequisites identify specific unperformed matrix cells; fixture tests and
  successful patch application alone are not reported as executed upstream evidence.
- Focused matrix/rejection/ownership/cleanup/injection checks, offline
  `go test -count=1 ./scripts/upstreamrepro`, its race variant, runner vet,
  `go build ./...`, formatting, wrapper syntax and whitespace checks pass. No ordinary full
  test suite or new platform/live acceptance is required; prior Windows/non-Unix runtime
  gaps remain explicit unless independently checked within an authorized task.
- The coordinator's sanitized handoff to `03-proposal-documentation` identifies each
  patch and test, selected patch combinations, baseline assertions, actual results and
  remaining validation gaps. It distinguishes upstream statistics from local event
  delivery and the per-worker config fix from the local all-group wrapper. Full logs
  remain private; proposal Markdown and automatic transformation coverage stay with
  their planned sibling tasks.

## Risks and handoff

The retained baseline cancellation demonstrations intentionally wait for bounded failures;
keep the existing test/process limits and allow for compilation rather than shortening the
checks or accepting a launcher timeout. This is one integration result, normally confined
to the two runner files. Only a reproduced artifact defect justifies an additional existing
test/patch correction. A runtime fix, changed proposal scope or non-independent patch would
invalidate that estimate and must be reported to lead.

The complete tagged file compiles even when only one reproduction is selected. Preserve
baseline-compatible counter reflection, serial global/capture handling and bounded joined
captcha teardown across the complete ordinary runs. Do not recast the accepted captcha
checks as elapsed expiry, native-chain or browser evidence. Keep original Russian assertions
and unchanged source text; all new or substantively changed prose/comments are English.

No user credential, external service, new dependency or user decision is needed. Prepared
objects and cache were usable for the accepted focused tasks; check them again without a
download fallback. A missing prerequisite is a named unvalidated matrix cell, not completed
compatibility evidence. Native Windows runtime, non-Unix cancellation and subsequent green
CI remain unconfirmed; source validation here does not close those platform gaps. No push,
tag, release, manual CI trigger, upstream submission or upgrade to `develop` is included.

## Outcome

Completed and independently reviewed the explicit matrix for the seven existing patches
against pinned upstream commit `fae121efc3ef57b633516601d3c0d6b1be1fde7c`. Each patch applies
and compiles alone with the complete tagged reproduction file; the ordered full patch set
also applies and passes its combined checks. Patch identities drive the case associations,
and each input declares its exact patch list and test selection.

The runner contains the ordinary `baseline` and `patched` selections, `baseline-injected` and
`patched-injected`, and the isolated inputs `patched-closed-call`, `patched-baton`,
`patched-config-quota`, `patched-counters`, `patched-stop`, `patched-stop-injected`,
`patched-captcha-timeouts` and `patched-captcha-wv-log`. The complete tagged file is compiled
for every input. Fixtures independently verify case/patch inventory, exact ordered patch and
test membership, unique names, anchored selections, patch application state, injection and
result validation. Rejection fixtures cover wrong or missing assertions, unexpected baseline
success, patched failure, missing or extra tests, wrong/missing skip reasons, setup/build
failures, panics and process timeouts, including the retained, counter and captcha cases.
Source preservation, offline subprocess settings, cleanup, delay-anchor validation, strict
directory ownership and the Windows alias regression remain covered.

The real normal wrapper completed all 12 inputs and 38 selected tests: 25 passed, 11 failed
with the required baseline assertions, and two skipped with the original ordinary deadline
reason. The baseline closed-call reproduction emitted all three original status diagnostics;
the baton and quota/config cases reached their named assertions; the stop-during-config case
reached `GETCONF` and exposed the cancellation delay. The baseline counter checks reported
the missing `Stats.DroppedUp` and `Stats.DroppedDown` fields and the missing statistics
output; the captcha checks reported the original manual and selected durations, the `wv`
diagnostic claim, and the passing 10-second auto control. The injected baseline emitted both
cancellation assertions with its existing cleanup diagnostic. Every isolated patch input,
the complete patched input and both injected patched inputs passed their selected checks;
only the ordinary deadline test retained its intentional skip.

The quota/config reproduction uses two workers: the baseline observes error 486, a positive
session count and `GETCONF 0`; with patch 3 alone, another worker establishes DTLS and delivers
the configuration. This confirms the upstream per-worker selection rule, not the local
all-group wrapper in `internal/core/run.go` or an upstream `main.go` change. The separate
stop-during-config reproduction reaches `GETCONF` before checking cancellation. Counter
evidence is observed in upstream `Stats.RunLoop` standard-log output, including
`Dropped: ↑7 / ↓11`; local `EventStats` delivery remains a separate integration detail.
Captcha checks establish configured durations, call-site/deadline contracts and immediate
synthetic responses; they do not wait out human deadlines or execute the native solver chain.

The cancelled baton reproduction now reaches the upstream cancellable credential-throttle
path before any credential fetch. A traced loopback-only run recorded no non-loopback
destinations or DNS/HTTP attempts. The final prepared-source manifest, including HEAD, index,
configuration, tracked and untracked contents and modes, matched before and after execution;
runner-owned temporary files were removed. The checked project export matched all runner and
reproduction asset files in the working tree.

With Go 1.26.2 and offline/cache-only settings, the focused runner fixtures, full runner tests,
race tests, runner vet, project build, formatting, wrapper syntax and repository whitespace
checks passed. The ordinary outbound project test suite was not run. Native Windows execution,
a subsequent green CI run, browser/live checks, VK access, native captcha solving and elapsed
human-deadline checks remain unperformed. No review findings were left open or rejected, no
separate incidental old-code problem was reported, and no confirmed problem remains partially
fixed because of implementation cost. Proposal prose and the reproduction README remain with
the documentation sibling; no runtime fix, dependency or additional patch was introduced.
