---
kind: stage
status: todo
depends: [01-repro-runner]
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
