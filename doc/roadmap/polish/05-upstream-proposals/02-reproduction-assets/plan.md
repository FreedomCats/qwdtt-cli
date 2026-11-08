---
kind: stage
status: done
depends: [01-repro-runner]
base: eb7dfdb
---

# Complete reproduction assets

Goal: complete the independently verifiable upstream patch/reproduction sets for dropped
packet counters and the human captcha window changes, then integrate their expectations
with the safe runner. Retain the existing closed-call, baton, config/quota and cancellation
evidence at the pinned `fae121e` source.

The implementation area is `doc/upstream-repro/` (patches and tagged reproduction tests)
and the case/patch expectations in `scripts/upstreamrepro/`. These are coordinator tasks,
not Markdown-only work. Counters and captcha evidence must be split into reviewable
children before implementation; do not combine them into one large task. Proposal prose
and the reproduction README are handled separately in
[`03-proposal-documentation`](../03-proposal-documentation/plan.md).

## Done when

- Proposal 4 has an applicable upstream-native patch for `Stats.DroppedUp`/`DroppedDown`
  and both drop sites, with a way to observe the counts through the upstream's own
  statistics output. Reproduction checks exercise queue saturation locally and verify
  counting without changing packet-delivery behaviour. Missing fields on the baseline
  cause a named assertion failure, not a compilation failure mislabelled as reproduction.
- Applicable captcha patches and fast offline checks cover manual timeout 60 to 180 s,
  selected timeout 120 to 180 s, the unchanged 10 s auto timeout, and the `wv` diagnostic
  without an Android-settings claim. Evidence does not wait out the human deadlines or
  open a real captcha/browser, call the native solver against VK, or change the captcha
  flow. Related timeout rules may share one coherent patch/proposal; a diagnostic-only
  patch remains identifiable.
- All upstream artifacts compile in upstream `package main` and use upstream facilities,
  not CLI-only `EventStats`, `emit`, `logf` or extraction scaffolding. The difference between
  upstream statistics output and the local `EventStats` integration is explicit in the
  handoff to the documentation task.
- Existing patches/tests remain valid. Proposal 3's per-worker rule change is not confused
  with passing the config channel to all groups in the locally owned `internal/core/run.go`;
  this stage does not introduce another runtime fix or a patch for extracting that wrapper.
- Each proposal patch applies to an isolated pinned input, and the complete patch set also
  applies together. The runner's explicit matrix includes the new checks and rejects
  unexpected results. Compiled baseline failures and patched passes are confirmed locally
  using prepared disposable pinned inputs, including the existing loopback stand and the
  injected deadline race; nothing resets or edits a caller's source tree.
- Focused new test/runner checks, applicable patch checks and an offline project build pass.
  No full outbound test suite or live verification is required. If a prepared pinned input
  or cached dependency is unavailable, record the missing validation rather than obtaining
  it over the network or claiming an unperformed run.

## Outline

1. [Upstream packet-drop counters](01-drop-counters/plan.md) — complete proposal 4's
   upstream-native patch, exercise both saturated queues and statistics output, and add
   precise counter expectations to the accepted runner.
2. [Human captcha evidence](02-captcha-evidence/plan.md) — supply the two human-window
   timeout changes and an identifiable diagnostic-only patch, with fast baseline/patched
   checks and an unchanged auto-window control.
3. [Pinned patch-set validation](03-pinned-validation/plan.md) — complete individual-patch
   validation and run the complete artifact matrix together, retaining the loopback stand,
   ordinary deadline skip and injected deadline demonstration.

## Execution and handoff

All three children are coordinator tasks. The first two deliver independently reviewable
proposal assets; the last delivers their compatibility evidence with the retained proposals.
The captcha result does not depend on the counter semantics. The final child depends on
both accepted asset sets and completes the parent criteria for existing evidence and patch
composition. Shared reproduction/runner files are changed serially in the listed order.

Use `fae121efc3ef57b633516601d3c0d6b1be1fde7c`, not the prepared source's current HEAD or
working-tree contents. Reuse the runner's private Git metadata and disposable inputs; a
dirty prepared source is usable without cleaning it. Keep offline settings in force before
bootstrap and for every subprocess. Select the installed Go 1.26.2 toolchain directly or
through PATH, with `GOTOOLCHAIN=local`; do not rely on automatic toolchain selection.
Missing source objects, toolchain or cached dependencies are validation gaps, not permission
to clone, fetch or download. Networked reproductions use loopback only.

The handoff to `03-proposal-documentation` must identify actual artifact/test names,
baseline assertions, patched results and any unperformed checks. Explain the upstream
statistics log versus local `EventStats`, and proposal 3's per-worker patch versus the
local all-group wrapper. Keep full execution logs in private working notes; public outcomes
contain only sanitized technical results. Proposal prose, the reproduction README and
automatic transformation accounting remain with the already planned sibling nodes.

## Integration decision

Integration review confirmed that the diagnostic-only patch removes `[КАПЧА] WBV:`, which
the pinned Android `TunnelManager.kt` consumer uses to display the request diagnostic in
the ordinary log. Without the marker, the new non-error message is shown only with detailed
logging enabled. The separate `CAPTCHA_SOLVE` request and response flow is unchanged.

The user chose to retain `[КАПЧА] WBV:` while correcting the Android-settings wording.
Update patch 7 and its reproduction expectation with that marker, keeping ordinary Android
log visibility and the separate captcha request flow. Reject the reduced-visibility option
and a wider Android consumer change. This is a bounded compatibility correction within
integration acceptance; the original goal and completion criteria remain unchanged.

## Outcome

Accepted after completed independent review of the whole stage, its criteria, the final
artifact bytes and affected pinned evidence. The three completed child Outcomes remain
historical records. This section records the integration corrections and their final checks.

The artifact set contains patches 1 through 7 and twelve top-level reproductions at full
upstream pin `fae121efc3ef57b633516601d3c0d6b1be1fde7c`. Patch 4 adds upstream-native atomic
`DroppedUp` and `DroppedDown` counters at the existing saturation sites and observes them
through normal statistics output. Its baseline-compatible tests reach named missing-field
or output assertions rather than treating compilation failure as evidence. Patch 6 changes
the manual and selected constants from 60/120 to 180 seconds, with an unchanged 10-second
auto control. Patch 7 remains independently identifiable as a diagnostic-only change.
Retained patches cover closed-call classification, baton handoff, per-worker config/quota
selection and both cancellation bugs.

The accepted pinned-validation child ran the normal wrapper across 12 inputs and 38 test
cells: 25 passes, 11 exact baseline failures and two original ordinary deadline skips. Every
patch applied and ran alone, and the ordered complete set passed its ordinary and injected
patched checks. Those are historical accepted results. Their temporary upstream logs are
unavailable; this integration does not claim a replay or a fresh complete-matrix execution.

The user-selected final diagnostic is
`[STREAM %d] [КАПЧА] WBV: wv mode: requesting captcha window (attempt %d)`.
It explicitly supersedes the unmarked message recorded in the captcha child's historical
Outcome. The retained marker preserves ordinary Android log routing while the new wording
removes the Android-settings claim. The Android consumer, request arguments, stdout markers,
response channels, timeout choices and solver flow are unchanged.

The forced existing read-deadline delay exposed a quota-test teardown defect: cancellation
immediately after config receipt could race Reader initialization and hang with patch 3
alone. Ordinary repeated passes and a race-detector pass did not exclude that order. The
quota test now closes the peer while relay forwarding is still active, waits for the active
session count to reach zero, then cancels and joins the group. The pinned proxy, relay and
session WaitGroups finish before the deferred counter decrement; the group done channel
then confirms full group completion. The same transport-first path applies during quota
failure cleanup. Patch 5 is not added to the isolated input, and the original quota assertion
and cancellation-first reproductions are retained.

The DTLS stand stops acceptance, records late-accepted connections for closure and explicitly
closes registered connections. Its accept and handler completion waits share a finite
shutdown deadline, with explicit handshake/I/O deadlines and separate teardown diagnostics.
The owned dispatcher socket close is checked, and dispatcher shutdown is observed through
a completion channel with a local deadline. Cache invalidation requires both group and
dispatcher completion; a shutdown timeout is a teardown failure, not expected bug evidence.
The TURN stand closes recorded relay sockets and observes their terminal packet reads with
a bounded wait. These observations do not constitute a public synchronous join of every
private Pion goroutine. No persistent internal Pion leak has been demonstrated, and no
dependency fork or private-field inspection was introduced to claim such a guarantee.

The unfixed injected baseline intentionally retains its original bounded blocked-client
diagnostic until its disposable test process exits. Cancellation has already stopped its
relay-to-pipe forwarding, so a later peer close cannot reliably release that client Reader.
This original bug-demonstration limit is separate from the successful quota and patched
inputs, whose groups must complete. New stand teardown errors remain outside the exact
baseline allowlists.

Fresh implementation and independent checks on the final artifact bytes confirmed the exact
baseline diagnostic failure, patch-7-only pass and ordered full-set manual/selected/auto/WV
passes. Applied-source
comparison required exactly the single chosen diagnostic change. The quota baseline retained
its exact assertion; patch 3 alone passed ordinary and forced-delay checks. An original-bytes
negative control required the cancellation assertion, process timeout and Reader stack.
Retained cancellation selections and isolated counter checks also matched their expectations.
The checked disposable project's artifacts matched the worktree, and the prepared source
status and file hashes matched before and after execution. The final reviewer verified these
affected checks, the owned-resource exit paths and the complete stage criteria. Targeted
checks cover the changed diagnostic, shared stand callers and dispatcher correction; the
unchanged matrix inventory and other patch results retain their accepted child evidence.

Focused and complete runner tests, race tests, runner vet, offline project build, formatting,
Bash syntax and whitespace checks passed with the directly selected Go 1.26.2 toolchain and
offline settings. Windows/amd64 cgo-disabled vet, build and test-binary compilation also
passed. Runner code, the explicit matrix, assertion allowlists, injection/skip distinctions,
source protection, offline/no-fetch policy and canonical ownership checks are unchanged.
The strict patch-link override, explicit fallback control, all eleven cleanup modes and
source/project asset bytes-and-mode preservation remain enabled.

Native Windows execution, actual Git native-link refusal and a subsequent green CI result
remain unverified. Ordinary outbound project/upstream suites, browser/live/VK checks, native
captcha solving and elapsed human-window expiry were not performed. No runtime, dependency,
vendoring, pin, branch policy, workflow or proposal-scope change is included.

All accepted integration findings are corrected: Android marker compatibility, quota teardown
isolation, local DTLS stand shutdown bounds and checked, bounded dispatcher cleanup. Final
independent review found no further defect or incidental old-code problem. An unconditional
dependency overhaul for an undemonstrated private-loop leak was rejected because no concrete
failure justified it; the actual close and exit paths were checked instead. The user rejected
reduced ordinary-log visibility and the wider Android consumer change. No confirmed problem
has been left incompletely corrected because of implementation cost.

The documentation handoff distinguishes upstream `Stats.RunLoop` standard logging from CLI
`EventStats`, patch 3's per-worker `group.go` rule from local all-group `internal/core/run.go`,
and captcha compiled constants/AST deadline wiring/immediate responses from elapsed human
deadlines and the manual native-solver chain. There is no upstream `main.go` wrapper patch.
Proposal prose and the reproduction README remain with the documentation sibling, and
automatic transformation accounting remains with its separate node.
