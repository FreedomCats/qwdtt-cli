---
kind: task
status: done
detailed: true
base: 2aed1a1
---

# Dispatch captcha requests from diagnostics

## Task

Make every diagnostic path that can request a captcha use the same `captcha.Dispatcher`
and solver setup as `connect`, so `qwdtt-cli ping`, `qwdtt-cli hashes`, and the menu's hash
check cannot leave a core worker waiting without a response. Keep the existing diagnostic
results and command behavior; do not introduce a browser requirement. The dispatcher and
solver are assembled in `internal/cli/connect.go`; core diagnostics emit the same
`core.EventCaptchaRequired` through their event sink (`internal/core/run.go` and
`internal/core/main.go`). The menu currently calls `core.RunHashCheck` directly from
`internal/cli/menu_edit.go`.

## Steps

1. In `internal/cli/diag.go`, wire both `diagPlan.ping` and `diagPlan.hashes` to a shared
   diagnostic captcha lifecycle: create `core.Control`, build the existing solver with
   `captchaSolver`, create `captcha.New` with the command context as `Stop`, observe captcha
   events while preserving existing event printing/counting, and close the dispatcher on
   every return path. Preserve `DialControl` setup for netguard. Do not add a datapath or
   start a tunnel for diagnostics.
2. In `internal/cli/menu_edit.go`, give `menu.checkHashes` the same dispatcher behavior while
   retaining the current event handling, dead-hash filtering, Ctrl+C cancellation, and the
   `env.hashCheck` seam used by existing menu tests. Put shared assembly in the smallest
   sensible helper in `internal/cli` if this avoids divergent dispatcher setup; keep
   `connectPlan.run` behavior unchanged.
3. Add focused tests in `internal/cli/diag_test.go` and `internal/cli/menu_edit_test.go` (and
   a small new test file only if useful). Exercise captcha events from `hashes`, `ping`, and
   the menu hash check with an injected solver/control path and no browser process. Assert
   the dispatcher answers the event promptly, regular diagnostic events/results are still
   delivered, cancellation closes the dispatcher, and no diagnostic waits for the core's
   180-second `wv` timeout. Ensure test fixtures use made-up hashes and documentation IPs.
4. Review the diff for duplicate output, dispatcher cleanup on errors/cancellation, and
   preserved netguard/event-sink behavior. Run `go test ./internal/cli ./internal/core`,
   `go test ./...`, `go build ./...`, and `git diff --check`.

## Done when

- `ping`, `hashes`, and the menu's hash check route `core.EventCaptchaRequired` through a
  dispatcher backed by the existing solver, or otherwise answer it immediately; none can
  leave a captcha request unanswered until the core timeout.
- Tests exercise all three paths without launching a browser and verify the captcha response
  and ordinary diagnostic behavior.
- `go test ./internal/cli ./internal/core`, `go test ./...`, `go build ./...`, and
  `git diff --check` pass.
- Dispatcher shutdown is reached on success, returned error, and context cancellation; the
  existing menu test seam and netguard setup remain effective.

## Outcome

`ping`, `hashes`, and the menu's hash check now share a diagnostic captcha lifecycle. It
creates `core.Control` and a dispatcher backed by the existing `captchaSolver`, observes
captcha requests alongside ordinary diagnostic events, and closes the dispatcher before
returning. The command context is passed as `Stop`, so cancellation sends a refusal even
when the core worker waits for a captcha response without observing that context.

The command paths retain `DialControl` and pass its dialer to the solver setup. The menu
retains `env.hashCheck`, its Ctrl+C handling, and removal of only confirmed dead hashes;
menu and captcha messages share the printer lock without duplicating diagnostic rows.
Diagnostics still do not start a datapath or tunnel, and the existing manual fallback
remains available without a browser. `connectPlan.run`, the pinned core, and dependencies
are unchanged.

Tests inject the solver and diagnostic runners to exercise captcha responses and ordinary
results for all three paths without a browser or live captcha. The lifecycle cases cover
success, returned error, and cancellation, including refusal delivery to a core waiter that
does not observe its context, waiting for solver cleanup, and preserving the menu's parent
context and signal handling. The process-wide captcha response channel is consumed and
cleared between tests.

Independent review found that the manual fallback originally had its own stdin reader:
it could miss lines already buffered by the menu, and its unfinished read could consume
the next menu answer after Ctrl+C. CLI prompts and manual captcha now share one input
owner. Canceling a waiter retains the pending read and its result for the next prompt;
repeated cancellation does not start competing reads. Hidden input has exclusive access,
and terminal reads do not buffer beyond the current line. `ManualOptions.ReadLine` is
optional, preserving standalone callers while letting the CLI share its input lifecycle.
Regression tests use pipes and the real manual solver to verify both failure scenarios,
repeated cancellation, EOF, and handoff to hidden input without changing terminal settings.

Validation passed:

- `go test ./internal/cli ./internal/core`;
- `go test ./...`, including a fresh run with `-count=1`;
- `go build ./...`;
- `go test -race ./internal/cli -run 'TestDiagnosticCaptcha|TestMenuCheckHashesDispatches' -count=20`;
- `go test -race ./... -count=1`;
- `go test -race ./internal/cli ./internal/captcha -run 'TestLineInput|TestMenuManualCaptcha|TestManualSharedReadLine|TestDiagnosticCaptcha' -count=20 -timeout=60s`;
- formatting checks for the changed Go files and `git diff --check`.

The full suite ran with loopback available and outbound networking disabled. Three
existing netguard live tests (`TestDualStackListener`, `TestLoopbackIndexBlocks`, and
`TestPhysicalIndexPasses`) skipped with `network is unreachable`; `TestUnknownIndexRefuses`
passed. Browser, WebView, and privileged network test opt-ins remained disabled. No live
captcha or external network checks were performed.

Independent re-review accepted the stdin correction with no remaining findings. Fresh
focused and full tests, the full race suite, build, and `go vet ./...` passed. Independent
pipe and buffered-input reproductions, including deadline followed by a new solver on the
same CLI environment, passed with the race detector in repeated runs.

Actual terminal echo switching, Windows and macOS runtime checks, browser and WebView
runtime checks, live captcha, and privileged network tests were not performed. The terminal
input handoff was checked with a pipe and by code review. No confirmed problem was left
partially fixed or deferred because of cost.
