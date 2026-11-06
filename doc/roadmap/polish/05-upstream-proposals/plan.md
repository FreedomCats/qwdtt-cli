---
kind: stage
status: in-progress
base: 07cf28f
---

# Upstream proposals

Goal: `doc/upstream-proposals.md` describes each change intended for upstream as a coherent
proposal that a reader can reproduce and judge. Automatically account for every vendorcore
transformation, distinguishing local adaptation from changes linked to upstream proposals.

Today the rules raise `captchaSelectedWebViewTimeout` to 180 s and rewrite the `[КАПЧА] WBV`
log line, and neither is written down in the proposals. The proposals cover the earlier core
fixes (954, the TURN quota cancel, the read deadline race) with reproduction tests under
`doc/upstream-repro/`.

Sending the proposals upstream is the user's business and is not part of the roadmap.

## Done when

- Every change intended for upstream has a coherent item in `doc/upstream-proposals.md`:
  what changes, why, how to reproduce, and the patch. Related rules may share a proposal.
- A test or script checks that every vendorcore transformation is explicitly classified as
  local adaptation, an upstream change linked to a proposal, or a combination of both. It
  detects missing classification and broken proposal links without requiring equal counts
  of rules and proposals.
- Local adaptation is accounted for without separate upstream proposals or patches for the
  CLI extraction and scaffolding. Upstream reproduction checks distinguish expected failures
  before fixes from successful checks after fixes, and do not reset a caller's working tree.

## Outline

1. [Safe reproduction runner](01-repro-runner/plan.md) — make the existing evidence runner
   reject unexpected results and operate only in disposable copies, with offline fixture
   tests before extending the reproduction set.
2. [Complete reproduction assets](02-reproduction-assets/plan.md) — a stage for the missing
   counter and captcha evidence and patches, retaining the existing reproductions and
   integrating their expected results with the runner.
3. [Reconcile the proposal documentation](03-proposal-documentation/plan.md) — document
   coherent upstream changes, their actual artifacts and limitations, and the safe local
   reproduction procedure.
4. [Automatic transformation accounting](04-transformation-coverage/plan.md) — classify
   the actual transformation inventory and check its proposal and artifact references
   without equating the number of transformations with the number of proposals.

## Scope decision

The user chose coherent upstream proposals with automatic classification of all
transformations instead of a per-transformation proposal catalog. There are 42 replacement
entries as well as other transformation tables; proposals already group related fixes, and
one replacement combines a local event hook with an upstream baton fix. A classification
may therefore link several transformations to one proposal and identify mixed-purpose rules.

## Execution boundaries

The coordinator implements code, tests, scripts and patch artifacts in children 01, 02 and
04. The lead handles the Markdown-only child 03 directly, without a coordinator cycle.
Child 02 remains a stage because counters and captcha evidence are independently
acceptable results; its children will be planned after the runner is accepted.

Keep the pinned `fae121e` source and the stable `master` policy. Do not investigate or
upgrade to `develop`, modify the runtime behaviour of the CLI, add dependencies, or create
proposals and patches for local extraction/scaffolding. No upstream submission, live
captcha/quota/platform run, browser, external service or implicit download is part of these
children. Run focused offline tests, not the ordinary full test suite with outbound tests.
