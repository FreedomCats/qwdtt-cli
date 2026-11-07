---
kind: task
status: in-progress
detailed: true
base: ac4afac
---

# Human captcha evidence

## Task

Supply independently reviewable upstream evidence for the existing human-window changes:
manual 60 to 180 seconds, selected 120 to 180 seconds, and a `wv` diagnostic without an
Android-settings claim. Keep the auto timeout at 10 seconds and preserve the captcha
request/response flow. Follow [the parent stage](../plan.md) and
[architecture sections R5, R7 and 08](../../../../../architecture.md), using the accepted
[runner](../../01-repro-runner/plan.md),
[counter](../01-drop-counters/plan.md) and
[Windows fixture repair](../../05-windows-runner-ci/plan.md) outcomes.

The intended artifacts are `doc/upstream-repro/6-captcha-timeouts.patch` and
`7-captcha-wv-log.patch`, both against pinned upstream `go_client/creds.go`. Extend
`doc/upstream-repro/repro_test.go` and the case/patch expectations and focused fixtures in
`scripts/upstreamrepro/{run.go,run_test.go}`. The two human deadlines form one coherent
patch; the diagnostic-only change stays separately identifiable. This task normally
touches five repository files and introduces no local runtime change. The shared runner
edits follow the counter task in execution order; the captcha semantics are independent.

At full pin `fae121efc3ef57b633516601d3c0d6b1be1fde7c`, upstream
`go_client/creds.go:604` rotates the profile before choosing a mode. Its `wv` branch
directly requests `selected` at `:622`; `manual` is reached only after native solver
attempts at `:703`. `requestWebViewCaptcha` drains the shared channel before printing the
stdout marker at `:726`, then passes its timeout parameter to `context.WithTimeout` at
`:729`. Profile rotation writes relative files (`go_client/profiles.go:47`).

Use compiled duration assertions, small AST checks of these actual call sites, and real
upstream request/response smoke checks. Execute the selected-mode path directly; exercise
the manual/auto request helper without executing the native chain. This proves configured
durations and their connection to the waiter, plus immediate response handling. It does
not claim to observe a real human-window expiry. No runtime seam or source injection is
needed for captcha evidence.

## Steps

1. Read those pinned functions through the prepared source's objects or the accepted
   private export, not its working tree or current HEAD. Compare
   `scripts/vendorcore/rules.go:227` and `internal/core/captcha_mode_test.go:13` for intent;
   do not copy the local event-based implementation. Keep the eight accepted reproduction
   functions, their assertion text, counter-only input and legacy deadline injection intact.
2. Add `6-captcha-timeouts.patch`, changing only the upstream manual and selected constants
   from 60/120 to 180 seconds. Add `7-captcha-wv-log.patch`, changing only the standard
   `log.Printf` call in the `wv` branch. Preserve its stream/captcha tags and attempt value;
   use English wording such as `wv mode: requesting captcha window (attempt %d)`, without
   the Android-settings claim. Neither patch changes branches, request arguments, stdout,
   response channels, profile rotation or lockout. Check and apply each patch alone to a
   separate disposable full-pin export. Inspect the applied diff and choose patch context
   that also passes repository whitespace checks; the accepted counter patch demonstrates
   a whitespace-safe representation of tab-indented upstream source.
3. In `doc/upstream-repro/repro_test.go`, add a small `go/parser`/`go/ast` helper restricted
   to `solveCaptchaBySelectedMode` and `requestWebViewCaptcha` in the tested `creds.go`.
   Parse the absolute source path before any `t.Chdir`. Require actual call expressions
   with four arguments, pairing `"manual"` with `captchaManualWebViewTimeout`, `"selected"`
   with `captchaSelectedWebViewTimeout`, and every `"auto"` request with
   `captchaAutoWebViewTimeout`. At this pin there are one manual, one selected and three
   auto call expressions, including the two `rjs` fallbacks. Require the selected call to
   be the direct return in the `wv` case. Also check that the request helper passes its
   timeout parameter to the context deadline consumed by its wait, with the existing
   nonpositive-timeout fallback to the auto constant. Missing, duplicate, mismatched or
   disconnected call sites are contract/setup failures, never the expected duration
   assertion. Match AST nodes, not comments, strings containing source, or a grep of
   declarations; no general call-graph/type-analysis framework is needed.
4. Add a shared test-only request/capture helper in that same file. Use a fresh isolated
   `CaptchaResultChan`, save/restore `getCaptchaMode()` through `setCaptchaMode`, and
   capture/restore `os.Stdout` and the standard logger's writer, flags and prefix. For
   `solveCaptchaBySelectedMode`, use `t.Chdir(t.TempDir())` so both `vk_profile.json` and
   `captcha_browser_fp` stay test-owned. Start draining an `os.Pipe` before invoking the
   function; after reading each `CAPTCHA_SOLVE|<mode>|<uri>|<session>` marker, send its
   synthetic immediate response to the test-owned channel. Do not prefill the channel:
   upstream would drain that answer. Use an `example.invalid` URI and made-up session/token
   values. Check the exact marker, request count and returned token; a `wv` invocation
   must emit exactly one `selected` request and no native-solver/auto-chain attempt logs.
   Supply a nil HTTP client on this branch, as the local mode test does, so it cannot
   contact a service. Keep all capture state synchronized and tests serial, without
   reproduction subtests or `t.Parallel`. Bound helper waits to a few seconds. On failure,
   stop/join the responder, close only the isolated response channel if needed to unblock
   the upstream background-context waiter, and join the invocation and pipe reader before
   restoring globals. A pipe, profile-write, parse or teardown failure has its own diagnostic.
5. Add these four top-level tests, running the call-site and response checks before the
   final duration/message assertion so baseline failures still exercise meaningful code:

   | Test | Actual path and assertion | Unpatched result |
   |---|---|---|
   | `TestReproCaptchaManualTimeout` | Manual call-site contract; real `requestWebViewCaptcha` with the compiled manual constant and immediate token; require 180 s | `manual captcha timeout: got 1m0s, want 3m0s` |
   | `TestReproCaptchaSelectedTimeout` | Selected call-site contract; real `solveCaptchaBySelectedMode` in `wv` with immediate token; require 180 s | `selected captcha timeout: got 2m0s, want 3m0s` |
   | `TestReproCaptchaAutoTimeout` | All three auto call sites and the request-helper deadline/fallback contract; real helper request with the compiled auto constant and immediate token; require 10 s | Pass |
   | `TestReproCaptchaWVLog` | Real `wv` path with immediate token; require the mode/window/attempt diagnostic and no Android claim | `wv diagnostic still claims Android settings` |

   The diagnostic test must pass with patch 7 alone, while selected remains 120 seconds;
   it must not require patch 6. Conversely, timeout tests must tolerate the old diagnostic
   when only patch 6 is applied. Validate other behaviour first, then emit the named
   baseline assertion only for the intended mismatch. Avoid raw `t.Log` capture dumps in
   expected-failing tests: unrelated assertion/setup lines must remain rejectable by the
   runner. All test code compiles against the unpatched upstream `package main` as well as
   either individual patch; do not reference CLI-only types or patch-added replacement APIs.
6. Extend `patches`, `cases` and `testRuns()` in `scripts/upstreamrepro/run.go`. Ordinary
   baseline expectations include the two exact duration failures, the exact diagnostic
   failure and the passing auto control; the complete patched input includes both new
   patches and expects passes. Add `patched-captcha-timeouts`, applying only patch 6 and
   selecting the manual/selected/auto tests, and `patched-captcha-wv-log`, applying only
   patch 7 and selecting the diagnostic test. Keep explicit per-input patch lists and
   anchored selection. Use case membership alongside the accepted counter grouping;
   retain all earlier ordinary/injected expectations and the counter-only input. Do not
   introduce another runner, public selection interface or fixed proposal count.
7. Extend `scripts/upstreamrepro/run_test.go` fixtures and `TestExpectationMatrix` for the
   exact captcha selections, patch isolation and always-passing control. Add
   `TestCaptchaResultValidation` to reject wrong/missing duration or diagnostic assertions,
   unexpected baseline passes, a failing auto control, missing tests, unexpected skips,
   patched failures, build/process timeouts, and capture/call-site/profile setup errors,
   including an extra setup error accompanying an otherwise correct baseline assertion.
   Update fixture diagnostics without broadening their allowlist. Preserve complete
   reproduction-file copying, pinned exports, offline environment, dirty-source cleanup,
   strict command-directory ownership and the Windows alias regression. In
   `TestCounterResultValidation` (`:115` before this task), replace its assumption that
   `runs[len(runs)-1]` is the counter-only input with lookup by name; adding new inputs
   must not silently move that coverage to captcha. Reuse the executor's exact patch-set
   checks instead of reverting to an all-or-no-patches assumption.
8. With installed Go 1.26.2 selected directly and first on PATH for subprocesses, apply
   `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off` before bootstrap and every Go
   invocation. Through `runInputs`, select a baseline restricted to the four captcha
   expectations and the two new individual-patch inputs, reusing the real private export,
   module preparation, complete tagged-file compilation and JSON validator. Confirm actual
   baseline assertions/control and individually patched passes; applicability or synthetic
   fixture results are insufficient. Run offline `go test -count=1 ./scripts/upstreamrepro`,
   `go test -race -count=1 ./scripts/upstreamrepro`, `go vet ./scripts/upstreamrepro`,
   `go build ./...`, formatting checks, `bash -n doc/upstream-repro/run.sh` and
   `git diff --check`. Focused runner checks include the new captcha validation, counter
   validation, expectation matrix and existing ownership/cleanup fixtures. Keep logs and
   any focused invocation harness private; no committed acceptance-only helper is needed.
   Report exact prerequisite/run gaps without fetching or downloading. The complete real
   retained, individual and combined matrix remains the final validation child.

## Done when

- Both patches apply individually to the full pin, compile in upstream `package main`,
  and use upstream constants, stdout/channel handling and the standard logger. They do
  not import local events, `CaptchaTimeout`, `Control`, `emit`, `logf` or CLI scaffolding.
- Fast checks distinguish the baseline manual/selected durations from the patched
  180-second durations with named assertions. A separate control passes on both inputs
  for the unchanged 10-second auto window. AST checks connect each mode's constant to
  its real call site and the request helper's deadline; actual upstream functions return
  the immediate synthetic token. No test sleeps through a human deadline or claims to
  have observed its real-time expiry.
- Diagnostic evidence fails specifically on the baseline Android-settings claim and
  passes with the `wv` message. The selected-mode path still requests one `selected`
  window directly. Any exercised request receives a synthetic immediate channel response;
  profile-file side effects, global mode/channel state and log/stdout capture are isolated
  and restored, with helper goroutines joined even on failure. No real captcha/browser,
  native solver invocation or request to VK is made.
- The runner's explicit expectations cover baseline failures, the always-passing auto
  control and patched passes. Timeout-only and diagnostic-only inputs select their
  corresponding checks; fixtures reject wrong assertions, missing tests, unexpected
  skips and process/setup errors without weakening the retained matrix. The diagnostic
  patch passes independently of the timeout patch; the timeout patch does not require
  the diagnostic. Counter selection and the accepted Windows ownership checks remain
  covered after adding the new inputs.
- Focused real baseline and individually patched checks use private pinned inputs and
  cached prerequisites. Focused runner tests/race/vet, offline project build and
  whitespace checks pass; missing prerequisites or unperformed checks are reported as
  gaps without downloads. Shared runner safety and the legacy deadline injection remain
  intact for final validation. Every selected input compiles the entire tagged test file
  before running only its anchored selection.
- Handoff records actual artifact/test names and outcomes for the documentation sibling.
  Proposal prose and the reproduction README remain outside this task; no dependency,
  response-channel redesign, local runtime fix or captcha-flow change is introduced.

## Risks and handoff

The manual fallback cannot be reached through the complete solver chain without invoking
the native solver. The bounded AST contract plus a real helper request is intentional;
do not substitute a fake runtime implementation, add a production hook, or claim this is
an end-to-end automatic-chain or elapsed-deadline check. A changed pinned source shape
must fail explicitly rather than falling back to constants-only evidence.

The waiter uses `context.Background()`, so cancelling the test's selected-mode context
does not release it. An answer sent before the stdout marker can also be discarded by
the channel drain. Keep the responder handshake and failure teardown small and explicit;
never close or drain the saved original channel. If bounded cleanup or call-site evidence
cannot stay within the five implementation files and small helpers, report the concrete
obstacle to the coordinator before expanding the task.

Pass the actual patch/test names, named baseline assertions, selected patch lists, results
and any gaps to `03-proposal-documentation` through the coordinator. Distinguish configured
duration/call-site evidence from real deadline expiry, and upstream stdout/standard logging
from CLI events and `CaptchaTimeout`. Leave complete patch composition and retained stand
runs to `03-pinned-validation`; native Windows runtime/green CI remains unconfirmed by the
accepted fixture repair. No user credential, service, browser, live access or new user
decision is needed. Publish only sanitized technical results, without execution dates,
source/cache paths or environment identifiers; all new/changed text is English.
