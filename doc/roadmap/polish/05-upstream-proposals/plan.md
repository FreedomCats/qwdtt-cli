---
kind: stage
status: done
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
5. [Windows runner CI regression](05-windows-runner-ci/plan.md) — restore portable fixture
   ownership checks for the accepted runner, independently of the pending counter changes.
6. [Portable patch-link fixture](06-patch-link-fixture/plan.md) — make the unsafe-link
   fixture exercise rejection explicitly, with offline coverage of Git's plain-file fallback.

All six planned children are complete. The ownership and patch-link repairs in children 05 and
06 are included in the accepted runner coverage. Child 02's final assets retain the Android
diagnostic marker and isolate quota-test cleanup; child 03 reconciles the public documentation;
and child 04 accounts for the complete transformation and pipeline inventory. The integrated
stage review confirmed that these results preserve the stage goal and completion criteria.

## Scope decision

The user chose coherent upstream proposals with automatic classification of all
transformations instead of a per-transformation proposal catalog. There are 42 replacement
entries as well as other transformation tables; proposals already group related fixes, and
one replacement combines a local event hook with an upstream baton fix. A classification
may therefore link several transformations to one proposal and identify mixed-purpose rules.

## Execution boundaries

The coordinator implements code, tests, scripts and patch artifacts in children 01, 02, 04,
05 and 06. The lead handles the Markdown-only child 03 directly, without a coordinator cycle.
Child 02 remains a stage because counters and captcha evidence are independently
acceptable results; its children and integration acceptance are complete.

Keep the pinned `fae121e` source and the stable `master` policy. Do not investigate or
upgrade to `develop`, modify the runtime behaviour of the CLI, add dependencies, or create
proposals and patches for local extraction/scaffolding. No upstream submission, live
captcha/quota/platform run, browser, external service or implicit download is part of these
children. Run focused offline tests, not the ordinary full test suite with outbound tests.

## Outcome

The stage is complete. The full range `07cf28f..09e580a` was reviewed as one result, including
the runner, seven upstream patches, tagged reproductions, proposal documents, transformation
accounting and all six child Outcomes. Seven explicit proposal anchors agree with their patch
artifacts, reproduction declarations and isolated runner inputs. The documentation and README
describe coherent upstream changes while keeping extraction, CLI scaffolding, event delivery
and socket hooks as local adaptation. The mixed credentials-failure rule retains both its local
event operation and proposal 2's upstream baton handoff.

The runner uses pinned `fae121e` source objects and disposable inputs, preserves a dirty caller
tree, rejects unsafe archive and patch entries, applies offline/no-fetch settings before
bootstrap, and validates exact process, package and test results. The explicit patch-link
fixture distinguishes ordinary target-text fallback from an actual link rejected by the existing
filesystem guard. The asset set preserves `[КАПЧА] WBV:` in patch 7, keeps patch 3's per-worker
config behavior independent of patch 5, and uses bounded transport, stand and dispatcher
teardown checks. The task 04 source contract includes the accepted split/tuple `plan.text`
alias correction without introducing general dataflow analysis.

Fresh final checks passed with the directly selected Go 1.26.2 toolchain and offline settings:
the vendorcore and upstreamrepro package tests, race tests, vet, project build, formatting,
runner wrapper syntax and repository whitespace checks. Focused accounting, reference,
matrix, ownership, cleanup and patch-link fixtures passed, and local Markdown links and anchors
were checked. The accepted historical pinned matrix remains 12 inputs and 38 cells, with 25
passes, 11 exact baseline failures and two ordinary deadline skips; it was not replayed during
this integration acceptance and is not presented as a fresh run.

The accepted decisions are the stable `master` policy at pinned `fae121e`, coherent proposals
with automatic complete classification, and preservation of the WBV marker with only its
Android-settings wording corrected. The rejected alternatives remain a per-transformation
proposal catalog, reduced Android log visibility or a wider consumer change, general dataflow
analysis, and an unconditional dependency overhaul for an undemonstrated private Pion-loop
leak. The final integration review found no additional concrete findings, so no code or test
correction was required. Native Windows execution and current green CI, ordinary outbound or
fresh complete upstream replay, live/browser/VK/quota/platform checks, and external actions
remain intentionally unperformed.

### Real problems not fully fixed because of cost

None. No confirmed problem was accepted and left partially corrected because of implementation
cost.
