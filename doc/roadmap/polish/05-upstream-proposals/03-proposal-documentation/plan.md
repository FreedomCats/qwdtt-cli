---
kind: task
status: done
detailed: true
depends: [02-reproduction-assets]
base: f425e0a
---

# Reconcile the proposal documentation

## Task

Bring `doc/upstream-proposals.md` and `doc/upstream-repro/README.md` into agreement with
the accepted upstream artifacts and safe runner. This is a Markdown-only task performed
directly by the lead, without a coordinator/reviewer cycle. Do not edit code, tests,
scripts or patch artifacts in this task.

Keep coherent proposals rather than one proposal per replacement. Preserve the existing
proposal identities and unchanged Russian paragraphs; write new or substantively changed
text in English, without translating the whole documents. Follow the
[parent scope decision](../plan.md) and
[architecture sections R7 and 08](../../../../architecture.md).

Use the accepted [assets stage Outcome](../02-reproduction-assets/plan.md#outcome) as the
final artifact authority, together with its three child Outcomes and the accepted
[safe runner](../01-repro-runner/plan.md#outcome),
[Windows ownership repair](../05-windows-runner-ci/plan.md#outcome) and
[patch-link fixture repair](../06-patch-link-fixture/plan.md#outcome). The stage Outcome
supersedes the captcha child's historical unmarked diagnostic. The lead-owned
`doc/local/journal.md` is read-only context; do not publish its execution history or paths.

## Steps

1. Reconcile only the two documents against the accepted artifacts before editing prose.
   Read `doc/upstream-repro/{run.sh,repro_test.go,*.patch}` and the relevant declarations
   in `scripts/upstreamrepro/run.go`: `pinnedCommit`, `patches`, `cases`, `testRuns`,
   `singlePatchRun`, `runInputs`, `offlineEnv` and `injectDelay`. These are reference inputs,
   not implementation targets. Use the full pin
   `fae121efc3ef57b633516601d3c0d6b1be1fde7c` and stable `master` policy. Preserve the
   existing five proposal numbers, topics, headings and unaffected Russian passages;
   replace inaccurate passages in English and add the two captcha proposals in English.
   Do not translate intact sections incidentally or copy private journal/log content.
2. Give each proposal in `doc/upstream-proposals.md` an explicit HTML anchor immediately
   before its heading, using `<a id="proposal-1-closed-call"></a>` as the shape. Use the
   identities below, retaining 1–5 and appending 6–7. Update the overview to link to these
   anchors, and give each proposal English `**Patch:**` and `**Reproduction:**` lines with
   a relative patch link, a link to `upstream-repro/repro_test.go` and the exact test names.
   In `doc/upstream-repro/README.md`, link back as
   `../upstream-proposals.md#proposal-1-closed-call` and use the corresponding identities
   for the other rows. Keep these ordinary Markdown references easy for
   [the later accounting task](../04-transformation-coverage/plan.md) to check; do not add
   accounting metadata, a parser or a fixed-count guard here.

   | Anchor ID | Patch in `doc/upstream-repro/` | Reproduction names | Isolated runner input(s) |
   |---|---|---|---|
   | `proposal-1-closed-call` | `1-closed-call.patch` | `TestReproClosedCallIsDead` | `patched-closed-call` |
   | `proposal-2-baton` | `2-baton.patch` | `TestReproGroupPassesBatonOnCredsFailure` | `patched-baton` |
   | `proposal-3-config-quota` | `3-config-quota.patch` | `TestReproConfigDespiteQuota` | `patched-config-quota` |
   | `proposal-4-dropped-packets` | `4-dropped-packets.patch` | `TestReproDroppedUp`, `TestReproDroppedDown`, `TestReproDroppedStatsOutput` | `patched-counters` |
   | `proposal-5-stop` | `5-stop.patch` | `TestReproStopDuringConfigRequest`, `TestReproStopRightAfterSessionStart` | `patched-stop`, `patched-stop-injected`, respectively |
   | `proposal-6-captcha-timeouts` | `6-captcha-timeouts.patch` | `TestReproCaptchaManualTimeout`, `TestReproCaptchaSelectedTimeout`, `TestReproCaptchaAutoTimeout` | `patched-captcha-timeouts` |
   | `proposal-7-captcha-wv-log` | `7-captcha-wv-log.patch` | `TestReproCaptchaWVLog` | `patched-captcha-wv-log` |

   These seven identities describe the current coherent artifacts; several transformations
   may reference one proposal, and future coverage must not depend on this count.
3. Correct the existing proposal explanations and evidence in `doc/upstream-proposals.md`.
   Retain the closed-call typed-error/wrapping and failed-credentials baton rationale,
   linking their actual tests and patches. For proposal 3, replace the obsolete four-worker
   reproduction and fixed result timings with the current two-worker TURN/DTLS stand:
   baseline allocation error 486, a positive session count and no `GETCONF` within 15 s;
   patch 3 alone lets another worker request and deliver config. State that the patch
   changes only upstream `go_client/group.go`. Passing config to every group is related
   local behaviour in `internal/core/run.go`; there is no accepted upstream `main.go`
   patch for that wrapper. Do not present this stand as live exhausted-quota verification.
   For proposal 4, replace the missing-reproduction/patch claims with the atomic
   `Stats.DroppedUp`/`DroppedDown` patch and its three tests. Explain saturation-only
   counting and unchanged packet delivery: uplink counts after all eligible queues reject
   a packet; downlink counts only a full `ReturnCh`. Baseline-compatible reflection reaches
   `missing drop counter: Stats.DroppedUp`, `missing drop counter: Stats.DroppedDown` and
   `statistics output omits drop counters: want DroppedUp=7 and DroppedDown=11`, rather than
   a compilation failure. The patched observation is normal `Stats.RunLoop` standard-log
   output containing `Dropped: ↑7 / ↓11`, not CLI-only `EventStats`.
4. Add coherent proposals 6 and 7 to `doc/upstream-proposals.md`, each explaining the symptom,
   reason, exact change, offline evidence, expected baseline/patched results and patch.
   Proposal 6 groups manual 60 to 180 s and selected 120 to 180 s; auto stays 10 s. Describe
   compiled constants, bounded AST contracts for all five actual request call sites and
   the wait deadline, plus immediate synthetic responses through actual upstream helpers
   and the direct `wv` selected path. Manual evidence does not execute the native-solver
   chain, and no check observes elapsed human expiry or opens a browser. Its baseline
   assertions are `manual captcha timeout: got 1m0s, want 3m0s` and
   `selected captcha timeout: got 2m0s, want 3m0s`; auto passes on both inputs. Proposal 7
   documents the final diagnostic exactly as
   `[STREAM %d] [КАПЧА] WBV: wv mode: requesting captcha window (attempt %d)`.
   Explain that `[КАПЧА] WBV:` preserves ordinary Android log routing while removing the
   incorrect Android-settings claim. The Android consumer and separate `CAPTCHA_SOLVE`
   request/response flow are unchanged. Baseline fails with
   `wv diagnostic still claims Android settings`; patch 7 alone passes while selected
   remains 120 s. Patch 6 alone passes the timeout checks with the old diagnostic. The
   large whitespace-safe patch representation is not evidence of a wider source change.
5. Replace the stale artifact and launch instructions in `doc/upstream-repro/README.md`.
   List linked patches 1–7, the tagged upstream `package main` reproduction file, `run.sh`
   and the Go runner under `scripts/upstreamrepro/`, with proposal/test associations from
   step 2. Document Bash, Git, a directly selected installed/cached Go 1.26.2 toolchain,
   full-pin local Git objects and prepared dependencies as prerequisites. From the project
   root, use this command shape with placeholders replaced by prepared local paths:

   ```sh
   export PATH="<go1.26.2-bin>:$PATH"
   export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off
   export GOMODCACHE="<prepared-module-cache>"
   export TMPDIR="<temporary-parent>"
   bash doc/upstream-repro/run.sh "<prepared-local-source>"
   ```

   The cache and existing writable temporary parent must be outside both repositories.
   Put the toolchain directory first on PATH before bootstrap and explain that the wrapper
   also enforces offline settings. The source argument is mandatory and may name a dirty
   repository: its HEAD, index, configuration and working tree stay untouched. Private Git
   metadata exports the pinned committed `go_client`, independently of current HEAD; all
   patch checks/application, test copying, delay injection and `go mod tidy` happen in
   runner-owned disposable inputs. Cleanup removes only owned inputs. Missing objects,
   toolchain or cached modules are explicit prerequisite errors, with no implicit clone,
   fetch, toolchain/module download or reset. Remove the no-argument auto-clone example,
   caller-tree reset/manual mutation instructions, automatic toolchain-selection advice
   and fixed total-runtime claim. Do not introduce a public focused-input command or an
   alternative manual workflow that bypasses the runner's protections.
6. Reconcile result expectations and limits across both documents. In the README, describe
   ordinary `baseline`/`patched`, deadline-only `baseline-injected`/`patched-injected`, and
   every isolated input in step 2 with its exact selected patch/test set. The complete
   patched inputs apply the declared patches in order; isolated inputs apply only their
   named patch. All inputs compile the entire tagged test file before anchored selection.
   Baseline evidence must match `cases` assertions, including the existing closed-call
   wrappers, baton assertion and quota counters; setup/bind/handshake/capture/teardown
   errors, panics, compilation failures, process timeouts and unexpected passes/skips do
   not count. Runner success is a zero exit with
   `All selected baseline and patched expectations matched.`, even though expected baseline
   test failures appear in its output; it does not suppress patched failures.

   Update proposal 5 and the README's race explanation together: the ordinary deadline
   test deliberately skips with its existing reason, and the runner injects exactly one
   300 ms sleep before `SetReadDeadline` plus `REPRO_INJECT_DELAY=1` only into disposable
   injected inputs. The test cancels 100 ms after config receipt. The injected baseline
   reaches both bounded no-return assertions; patch 5 alone and the complete injected
   patch set pass. Stop-during-config first reaches `GETCONF`, then checks return within
   5 s. Remove stale observed pass latencies; retain actual bug deadlines and injection
   parameters as mechanisms, not performance promises.

   Describe the accepted full wrapper matrix as historical evidence: 12 inputs, 38 cells,
   25 passes, 11 exact baseline failures and two original ordinary deadline skips. Final
   artifact acceptance added targeted diagnostic/full-set captcha, isolated quota/counter,
   retained cancellation and shared stand/dispatcher checks; it did not rerun or replay
   the complete matrix from unavailable logs. Explain only useful limits: quota teardown
   closes transport and observes inactive sessions before cancellation/group completion,
   keeping patch 3 independent of patch 5; stand/dispatcher waits are bounded, not a
   synchronous join guarantee for every private Pion loop. The original unfixed injected
   client can remain blocked until its disposable test process exits; this is separate
   from new teardown errors, which are rejected. Distinguish passed host runner/race/vet/
   build and Windows cross-checks from unperformed native Windows execution/current green
   CI, live VK/quota/platform, browser, native captcha-chain and elapsed human-expiry checks.
   Keep source snapshots, private paths and execution chronology out of the public text.
7. Explain local/upstream/mixed purposes in `doc/upstream-proposals.md` without enumerating
   every transformation. Upstream proposals describe coherent behavioural changes; package
   extraction, CLI scaffolding, event delivery and socket hooks remain local adaptation.
   Identify the existing group rule that combines local `emitHashFailed` with proposal 2's
   upstream baton handoff as mixed. Retain the no-response-channel-redesign rationale:
   `fetchVkCredsSerialized` holds `vkRequestMu` through captcha waiting. Keep the local
   config-wrapper distinction from step 3 and refer to the later accounting task without
   implementing its inventory/check or adding extraction/scaffolding patches.
8. Review the two-document diff against the accepted stage Outcome, actual artifact names,
   `cases`/`testRuns` selections and wrapper settings. Resolve every new relative link and
   explicit anchor, and verify that old inaccurate overview/table/end-section claims do
   not contradict the corrected sections. Check unchanged Russian passages remain intact
   and all new or changed prose is English, allowing verbatim existing diagnostic markers
   and assertions as technical evidence. Review publication/privacy rules and run only
   `git diff --check`. Do not run commands shown for readers, reproductions, a full matrix,
   tests, builds or live/platform checks for this Markdown-only task. Lead records completion
   separately; leave code, patches, scripts, completed roadmap nodes and journal unchanged.

## Done when

- The two documents consistently cover the coherent proposals in step 2 with their purpose,
  exact change, offline evidence and baseline/patched expectations. Existing identities 1–5
  and unchanged Russian text remain; new/changed prose is English. Every proposal has one
  unique explicit anchor and linked patch/reproduction references; overview and README
  backlinks resolve. Task 04 can reference these identities without equal rule/proposal counts.
- Proposal 3 and the README describe the two-worker 486/DTLS/config evidence and patch 3's
  `group.go` scope, separately from local all-group `internal/core/run.go`. Neither claims an
  upstream `main.go` wrapper patch or obsolete four-worker/patched-latency results. Proposal 4
  and the tables link all three real counter tests and patch 4, identify the named compiled
  baseline assertions and `Stats.RunLoop` output `Dropped: ↑7 / ↓11`, and distinguish local
  `EventStats`. No missing-counter-artifact claim remains.
- Both human deadlines, the unchanged auto control and their actual compiled/AST/immediate-
  response evidence are explicit. Patch 7's documented string retains `[КАПЧА] WBV:` and
  English mode/window/attempt wording, preserving ordinary Android log routing and the separate
  `CAPTCHA_SOLVE` flow. Patch-6-only and patch-7-only expectations remain independent. No
  elapsed human-expiry, native manual-chain or browser check is implied.
- README artifact/test/input associations agree with `patches`, `cases` and `testRuns`.
  Ordinary deadline skips, injected baseline assertions and isolated/combined patched passes
  are distinguished; setup errors and timeouts are not treated as bug evidence. The accepted
  12-input/38-cell result remains historical, and final targeted checks are not presented as
  a fresh full matrix or a replay. Useful teardown bounds and the private-Pion join limit
  are stated without publishing execution chronology or private logs.
- Launch instructions use the mandatory prepared-local-source argument, directly selected
  Go 1.26.2 and offline settings before bootstrap, external prepared cache and temporary
  parent, full-pin disposable exports and source HEAD/index/dirty-tree preservation. There
  is no auto-clone/fetch/download/reset, caller-tree manual mutation recipe, invented focused
  runner interface or fixed execution-time promise. These are instructions for readers;
  no reproduction or command example is executed in this documentation task.
- Local adaptation, upstream intent and the mixed `emitHashFailed`/baton rule are explained
  without a per-transformation catalog or new extraction/scaffolding patches. The serialized
  captcha response-channel rationale stays intact. Full pin/stable `master`, scope and
  existing user decisions remain unchanged. Host checks and Windows cross-checks do not
  imply native Windows/current green CI or live VK/quota/platform verification.
- Only `doc/upstream-proposals.md` and `doc/upstream-repro/README.md` change for the result,
  apart from lead's task-plan bookkeeping. Relative links and anchors resolve,
  `git diff --check` passes, and changed text passes the publication/privacy review: no work
  dates, environment identifiers, actual source/cache/home paths, real server/call/profile
  data or automatic attribution. No code, patch, script, dependency, completed node or journal
  is edited; no test/build, external submission or other external action is needed.

## Risks and handoff

The main risk is overstating evidence while reconciling stale prose: the captcha child's
unmarked message is superseded, the complete matrix is historical, and the quota/stand
correction has only the affected final-byte checks described in the accepted stage Outcome.
Use that Outcome and final files rather than inferring new runs from earlier wording. Keep
limitations concise and technical, without retelling integration review or private execution.

This remains one coherent Markdown result across two existing documents. No new test,
build, credential, service access or user decision is required. Give lead/task 04 the stable
anchor/patch/test/input mapping and any unresolved documentation mismatch. A mismatch that
would require changing an accepted artifact or scope must be reported, not repaired in this
task; the automatic transformation inventory and its code/tests belong to task 04.

## Outcome

Reconciled `doc/upstream-proposals.md` and `doc/upstream-repro/README.md` with the accepted
artifact set. Preserved proposal numbers 1–5, their topics and unchanged Russian passages;
new and substantively changed prose is English. Added coherent captcha timeout and
diagnostic proposals and unique explicit `proposal-1-closed-call` through
`proposal-7-captcha-wv-log` anchors, with linked patches, reproduction names and isolated
input associations for the later accounting check.

The documents now describe the three counter tests and upstream statistics output,
two-worker per-worker config evidence separately from the local all-group wrapper, the
final WBV-preserving diagnostic, and the actual configured-duration/call-site/immediate-
response captcha evidence. The serialized response-channel rationale is retained, and
local, upstream and mixed transformation purposes are distinguished without a separate
proposal for extraction or CLI scaffolding.

Replaced automatic-clone and caller-tree reset instructions with the mandatory prepared
local-source argument, directly selected installed toolchain, offline prerequisites and
disposable full-pin inputs. The README records ordinary, injected, isolated and complete
patch selections, exact baseline evidence and rejection rules. Historical complete-matrix
results are distinct from final targeted checks; platform/live, elapsed-deadline and private
Pion-join limits remain explicit. No documented reproduction command was executed here.

All 56 local links in the two documents and task/parent plans resolve, and explicit anchors
are unique. Whitespace and publication/privacy review passed. This was a Markdown-only
change, so tests and builds were not run. No unresolved documentation mismatch or confirmed
problem deferred because of cost remains. Full transformation classification and its
negative-fixture guard remain with task 04.
