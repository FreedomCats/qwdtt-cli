---
kind: task
status: in-progress
detailed: true
base: eb7dfdb
---

# Upstream packet-drop counters

## Task

Finish proposal 4 as one independently reviewable upstream patch and its reproduction
checks. Count packets discarded at the existing upstream queue-saturation sites and expose
the totals through upstream statistics output. Preserve packet delivery, queue sizes,
scheduling, cancellation and existing raw-TUN diagnostics.

Follow [the parent stage](../plan.md),
[architecture sections R7 and 06](../../../../../architecture.md), and the accepted
[safe runner outcome](../../01-repro-runner/plan.md). The local implementation is a
reference for counter semantics, not a patch source that can be copied verbatim:
`scripts/vendorcore/rules.go:348` delivers CLI events, while
`scripts/vendorcore/rules.go:423` adds the counters and increments. Upstream uses
`package main` and `log.Printf`; it has no `EventStats`, `emit` or `logf`.

Keep the implementation normally within four repository files:
`doc/upstream-repro/4-dropped-packets.patch`, `doc/upstream-repro/repro_test.go`, and
`scripts/upstreamrepro/{run.go,run_test.go}`. The patch itself changes only upstream
`go_client/{stats.go,dispatcher.go,session.go}`. Captcha assets belong to the next child;
the full individual/combined patch inventory is completed in the final validation child.

## Steps

1. Inspect the pinned versions of upstream `stats.go`, `dispatcher.go` and `session.go`
   through read-only source objects or the runner's private export. Compare the two drop
   branches with `internal/core/dropped_test.go:12`,
   `internal/core/dispatcher.go:377` and `internal/core/session.go:658`. The uplink counter
   counts one packet only after every eligible worker queue rejects it; a successful
   priority/ordinary fallback is not a drop. The downlink counter belongs only to the
   full-`ReturnCh` default branch, not the cancellation branch or keepalive filtering.
2. Add `doc/upstream-repro/4-dropped-packets.patch` against the full pin. Add
   `DroppedUp`/`DroppedDown atomic.Uint64` to `Stats`, increment them at those two existing
   branches, and include both cumulative values in the existing statistics log line using
   atomic loads. Retain its existing traffic/active-worker information and cadence. Use
   upstream facilities and English new comments/diagnostic text; do not introduce a new
   logging API, CLI event shim, extraction change, dependency or runtime test hook.
3. Append English, tagged `package main` reproductions to
   `doc/upstream-repro/repro_test.go`. Introduce a small reflection adapter for locating
   and loading the optional `Stats` counter fields without referring to them directly in
   code that must compile on the baseline. Require addressable `atomic.Uint64` fields;
   report a missing field with a stable, named assertion such as
   `missing drop counter: Stats.DroppedUp`. A wrong field type or setup failure must have
   a different diagnostic. Do not supply baseline replacement fields or redefine upstream
   types. Keep the existing five test functions and their assertion text intact. Avoid
   reproduction subtests and `t.Parallel`: the current JSON validator expects explicitly
   named tests, and logging/stand state is shared.
4. Add `TestReproDroppedUp` in that file, exercising the real `Dispatcher.readLoop` with
   a test-only controlled `net.PacketConn` and registered `WorkerSlot` queues. A small,
   known batch of large and priority-sized packets rejected by full queues must increment
   `DroppedUp` exactly once per packet and leave `DroppedDown` at zero. Also exercise a
   free queue, fallback to another worker and the no-worker path: none may inflate the
   saturation count. Synchronize processing with bounded waits/barriers rather than
   inferring delivery from a sleep or an unreliable UDP flood. On cleanup, cancel and
   unblock `ReadFrom` before waiting for `Dispatcher.Shutdown`; cancellation alone cannot
   wake its blocking read.
5. Add `TestReproDroppedDown`, using the real upstream `RunSession` and the existing
   loopback TURN/DTLS stand. Add a narrowly scoped server helper capable of sending a
   controlled batch after session/config setup. Supply a test-owned dispatcher whose
   `ReturnCh` is prefilled and has no draining `writeLoop`; `RunSession` can still register
   its worker normally. Verify one downlink increment per rejected payload, no uplink
   increment, and successful delivery without another increment after freeing a slot.
   Establish session readiness and packet processing explicitly before cancellation;
   the counter-only patch must not require the stop/config patches to make this test
   meaningful. Use only synthetic credentials and loopback endpoints. Preserve the
   legacy stand helpers' behaviour, bound every wait, close connections and join owned
   goroutines. Bind, handshake or transport errors are setup failures, not evidence of
   missing counters.
6. Add `TestReproDroppedStatsOutput`, capturing the standard logger while `Stats.RunLoop`
   produces its normal statistics output. Populate different nonzero counter values via
   reflection when the fields exist, and assert that both directional values appear
   without losing the existing statistics information. On the baseline, allow field
   absence during setup and fail specifically because output omits the drop counts.
   Do not make this test fail at field lookup instead of checking the output surface.
   Use a synchronized capture, a bounded wait for the existing ticker, and restore the
   logger and stop/join the loop on every exit. No production ticker or formatting seam
   is needed.
7. Extend the patch/case tables and fixtures in `scripts/upstreamrepro/run.go` and
   `run_test.go`. Register the new patch and all three test names, with compiled baseline
   failures tied to their precise missing-field/output assertions and patched passes.
   Preserve the existing ordinary/injected inputs and their expectations. Make the
   patch list explicit per input so a counter-only patched input selects just the counter
   tests and applies only `4-dropped-packets.patch`; the final child will extend this
   mechanism to the other individual patches. Reuse the same export, module preparation,
   test selection and `validateResult` path, retaining the prepared-source invocation.
   Update the synthetic executor's current all-or-no-patches assumption
   (`scripts/upstreamrepro/run_test.go:390`) to check the exact patch set for each input;
   keep unrelated synthetic fixes unpatched in the counter-only input.
   Fixtures must verify patch isolation, copying/selection of the new tests, exact JSON
   expectations and rejection of wrong assertions, missing tests, unexpected skips,
   compilation/setup failures and timeouts. Keep all source-preservation, offline and
   cleanup safeguards.
8. Run the focused counter evidence on disposable full-pin inputs with the installed
   Go 1.26.2 toolchain and prepared dependencies. Confirm the named baseline assertions
   and the counter-only patched passes, including both real code branches and log output;
   patch applicability alone is insufficient. Run offline
   `go test ./scripts/upstreamrepro`, `go test -race ./scripts/upstreamrepro`,
   `go vet ./scripts/upstreamrepro`, `go build ./...` and `git diff --check`.
   Apply `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off`, with no implicit download.
   Distinguish counter acceptance from the later complete artifact matrix; record exact
   validation gaps if prerequisites or unrelated retained evidence prevent a full run.

## Done when

- `4-dropped-packets.patch` applies alone to the pinned source and compiles with the tagged
  reproductions in upstream `package main`. Its only runtime additions are the two atomic
  fields, the two saturation increments and observation in upstream statistics output.
- The baseline compiles and reaches named missing-counter/output assertions. Missing
  fields are never represented as build errors, arbitrary process failures or skips.
- Patched reproductions exercise the real uplink and downlink drop branches, verify exact
  counts and opposite-direction isolation, and demonstrate that successful queue delivery
  and fallback do not count as drops. A full return queue does not block the session
  reader. The upstream statistics check observes two distinct nonzero directional values.
- The runner includes the new exact expectations and a counter-only patched input. Its
  fixtures retain dirty-source preservation, pinned exports, offline settings, cleanup
  and the original injected/uninjected deadline distinctions. New result mismatches are
  rejected without weakening legacy assertion checks.
- Focused real baseline/counter-only runs and the offline runner tests, race tests, vet,
  project build and whitespace checks pass. Any unavailable prerequisite or unperformed
  retained-suite check is reported explicitly, with no network fallback or synthetic
  fixture result presented as real pinned evidence.
- The documentation handoff identifies the patch, tests, exact baseline evidence and
  actual checks performed, and explains upstream log output versus CLI-only `EventStats`.
  No proposal prose, reproduction README, local runtime code or vendorcore rule is edited.

## Risks and handoff

The downlink branch has no existing counter reproduction; its controlled stand is the
largest part of this bounded task. Test it through `RunSession`, without extracting a new
runtime helper or relying on the local CLI wrapper. Avoid coupling its teardown to the
legacy read-deadline race, which has its own retained injected demonstration. If this
cannot stay within the stated files and a small test helper, report the concrete obstacle
to the coordinator instead of adding a runtime fix or weakening the evidence.

Globals used by logger capture and the existing stand require serial tests and complete
restoration. The entire tagged file must compile before test selection, so every new
reference to patch-added fields needs the baseline-compatible adapter. Existing Russian
comments/assertions are not translated incidentally; new or changed text is English.

No user service, credential, browser, VK access or live run is needed. Missing prepared
source/cache/toolchain is a validation gap, never authorization to obtain it. Pass the
asset names and sanitized evidence to the documentation sibling through the coordinator;
do not publish work dates, source/cache paths or environment identifiers.
