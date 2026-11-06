---
kind: stage
status: in-progress
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
