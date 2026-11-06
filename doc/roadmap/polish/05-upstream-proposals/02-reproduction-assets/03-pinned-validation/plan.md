---
kind: task
status: todo
depends: [01-drop-counters, 02-captcha-evidence]
---

# Pinned patch-set validation

## Task

Complete and execute the safe runner's real pinned matrix after both new asset sets are
accepted. Deliver reproducible compatibility evidence for every patch alone and for all
patches together, including the retained closed-call, baton, config/quota and cancellation
reproductions. Follow [the parent stage](../plan.md) and the
[safe runner outcome](../../01-repro-runner/plan.md).

The normal implementation area is `scripts/upstreamrepro/{run.go,run_test.go}`: complete
the per-input patch/test tables and their fixture coverage. Correct
`doc/upstream-repro/repro_test.go` or an existing patch only when a real pinned check
exposes an artifact defect, preserving the intended assertion and proposal scope. This
is one integration result, normally two runner files plus any narrowly justified asset
corrections, bounded by the current reproduction/patch set. Do not add another runtime
fix or broaden the proposal set to make a run pass.

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
- Offline `go test ./scripts/upstreamrepro`, its race variant, runner vet,
  `go build ./...`, wrapper syntax and whitespace checks pass. No ordinary full test
  suite or new platform/live acceptance is required; prior Windows/non-Unix runtime
  gaps remain explicit unless independently checked within an authorized task.
- The coordinator's sanitized handoff to `03-proposal-documentation` identifies each
  patch and test, selected patch combinations, baseline assertions, actual results and
  remaining validation gaps. It distinguishes upstream statistics from local event
  delivery and the per-worker config fix from the local all-group wrapper. Full logs
  remain private; proposal Markdown and automatic transformation coverage stay with
  their planned sibling tasks.
