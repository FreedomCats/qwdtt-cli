---
kind: task
status: done
detailed: true
base: 07cf28f
---

# Safe reproduction runner

## Task

Make `doc/upstream-repro/run.sh` a trustworthy local evidence runner for the existing
patches and tests. Success must mean that the expected baseline failures were observed
and that the patched checks passed, not merely that some test output was printed. The
source supplied by the caller must remain untouched on success, failure and interruption.

The current script resets the supplied checkout, applies patches there and suppresses
both test exit statuses. Replace that implementation with a standard-library Go runner
under `scripts/upstreamrepro/`, retaining `run.sh` as a thin compatibility entry point for
the local-source invocation. This follows the script convention in
[`architecture.md`](../../../../architecture.md), section 03. It is developer tooling,
not a change to the application's CLI or vendoring behaviour.

This task establishes the harness for the four existing patches and five existing test
functions only. New counter/captcha assets belong to
[`02-reproduction-assets`](../02-reproduction-assets/plan.md); Markdown instructions belong
to [`03-proposal-documentation`](../03-proposal-documentation/plan.md).

## Steps

1. Add the Go entry point and runner implementation in
   `scripts/upstreamrepro/main.go` and `scripts/upstreamrepro/run.go`. Read the full pinned
   commit used by `doc/upstream-repro/run.sh` and keep `fae121e`; do not substitute the
   source's current HEAD. Require a prepared local source: missing source, missing pinned
   objects or missing local toolchain/dependencies must produce an actionable error, not
   a clone, fetch or download; disable Git's implicit lazy fetching as well. Retain the
   meaning of the existing positional source argument, including relative paths and paths
   containing spaces.
2. Build the baseline and patched inputs in runner-owned temporary directories from the
   pinned committed source, using a read-only export such as `git archive`. Ignore and
   preserve the source's working-tree changes. Do not use checkout, reset, clean, patch
   application, worktree registration or module commands in the caller's repository.
   Confine extraction, test copies, patch application, delay injection and cleanup to
   owned directories; reject escaping archive paths. Validate patch applicability and
   require the deadline-delay injection to match exactly once. Cover cleanup on ordinary
   failure and handled interruption without deleting or changing caller-owned paths.
3. Execute only the selected `TestRepro` tests from `doc/upstream-repro/repro_test.go`,
   with the `upstream_repro` tag, in those copies. Keep the four existing patches
   `1-closed-call.patch`, `2-baton.patch`, `3-config-quota.patch` and `5-stop.patch`.
   Use offline subprocess settings (`GOTOOLCHAIN=local`, `GOPROXY=off`, `GOSUMDB=off`,
   `GOWORK=off`); allow module-file preparation only inside the disposable input. Do not
   invoke unrelated upstream tests, VK, a browser, or any external server. The existing
   stand uses loopback only.
4. Parse structured `go test -json` results and compare an explicit per-test expectation
   matrix, including the process result. The ordinary baseline must fail the closed-call,
   baton, quota/config and stop-during-config tests; its uninjected deadline-race test
   must skip. The ordinary patched run must pass those four tests with only that same
   intentional skip. Run the deadline-race test separately with the injected delay and
   `REPRO_INJECT_DELAY=1`: baseline failure and patched success are required. Unexpected
   passes, failures, skips, missing tests, build errors, panics and process timeouts must
   make the runner fail. Match the intended failure assertions as well as test statuses:
   a loopback stand setup/bind error is not evidence of the upstream bug. Keep full
   diagnostics rather than filtering away setup errors.
   Make the case/patch expectations easy for child 02 to extend without adding a second
   runner or a frozen proposal count.
5. Replace the body of `doc/upstream-repro/run.sh` with the thin Go-runner invocation.
   Resolve the caller's source argument before changing directory, preserve arguments
   and exit status, and do not retain the automatic network-clone fallback or destructive
   exit trap. Apply the offline environment before the wrapper's own Go invocation, not
   only to subprocesses launched by the Go runner. Keep Bash-specific behaviour minimal;
   the testable safety and result-validation logic belongs to Go.
6. Add focused tests in `scripts/upstreamrepro/run_test.go` and, if needed, a small
   fixture/helper file in that package. Use temporary synthetic git repositories and a
   fake subprocess executor or fixture command; do not run the actual upstream script
   against an available caller clone. Test exact result matching and malformed/missing
   JSON, build failure, wrong baseline failure assertion, unexpected baseline success,
   patched failure, unexpected skip, timeout, patch/injection failure and cleanup. A dirty
   source fixture must preserve its
   HEAD, index, tracked edits and untracked sentinel after success and failure. Include
   the relative/path-with-spaces entry-point case and verify offline subprocess settings.

## Done when

- A caller's repository is only read as a pinned source. Fixture tests demonstrate no
  checkout/index/working-tree changes, even when setup, patching or test validation fails;
  cleanup affects only runner-owned temporary directories.
- Runner success requires the complete expected baseline/patched matrix, including the
  deliberately injected deadline-race demonstration. Compilation/setup errors cannot be
  accepted as an expected reproduction failure, and patched failures cannot be hidden by
  output filtering or `|| true`.
- Missing source, pinned objects, toolchain or cached dependencies fail explicitly without
  a network attempt. No existing reproduction or patch is changed to make this task pass.
- With the installed toolchain and cached dependencies, offline
  `go test ./scripts/upstreamrepro`, `go test -race ./scripts/upstreamrepro`,
  `go vet ./scripts/upstreamrepro` and `go build ./...` pass. Apply the same offline
  environment as above. `bash -n doc/upstream-repro/run.sh` and `git diff --check` pass.
- No live or outbound check, ordinary full test suite, or execution against a caller's
  existing clone is used for acceptance. Report fixture validation as fixture validation;
  the real pinned artifact runs are checked in child 02, not claimed here.

## Risks and handoff

An expected test failure is not an arbitrary nonzero process exit. In particular, the
ordinary deadline-race test intentionally skips; the injected variant must actually run.
The existing baseline includes long waits for the two cancellation bugs, so unit fixtures
must not reproduce those waits. Keep this change to the runner, its wrapper and focused
tests, normally four to six files; do not fold the missing proposal artifacts into it.

No user service, credential or live access is needed. Cached build prerequisites are
required; absence is a reported limitation, never permission to download them. The lead
updates the user-facing Markdown instructions in child 03 after the artifact set settles.

## Outcome

Implemented and accepted after independent review and one correction round.

- Added a standard-library runner in `scripts/upstreamrepro/` and a compatibility wrapper
  that resolves the local source before changing directories, applies the offline environment
  before building, and preserves runner exit statuses and handled interruptions.
- The full pinned commit remains `fae121efc3ef57b633516601d3c0d6b1be1fde7c`. Private Git
  metadata reads the caller's object store without its remotes, promisor configuration or
  local archive attributes. Each baseline/patched and ordinary/injected case uses its own
  archive extraction; patches, module preparation and the exact-once delay stay disposable.
- Explicit case and patch tables drive anchored test selection and structured process,
  package and test validation. Baseline failures require their original assertions;
  compilation, setup/bind errors, panics, timeouts and unexpected results cannot count as
  evidence. Complete command diagnostics are retained.
- Synthetic fixtures cover dirty source preservation, cleanup on success/error/interruption,
  archive containment, patch/injection errors, result validation, offline settings and
  relative paths containing spaces. The existing four patches and five reproductions
  remain unchanged.
- Both initial review findings were accepted and corrected: bootstrap compilation now
  runs in its own process group so compiler descendants are terminated before cleanup,
  and the executor timeout test expects the platform-specific termination status while
  requiring the context deadline error. A bootstrap-interruption fixture exercises
  descendants that would otherwise survive their parent and recreate deleted inputs.

Focused `go test ./scripts/upstreamrepro`, its race variant, runner vet, offline
`go build ./...`, Bash syntax and whitespace checks passed after the corrections, with
independent verification. The reviewer also confirmed bootstrap cleanup during a cold
build with both SIGINT and SIGTERM on a disposable project fixture: no tracked live
descendants or launch inputs remained. The Windows runner test binary cross-compiles
offline; Windows execution and non-Unix cancellation runtime behaviour remain unverified.
No review findings were rejected, and no confirmed problem was left partially fixed
because of implementation cost. No separate incidental old-code issue was reported.

No ordinary full test suite, real caller-clone reproduction, live/outbound or browser check
was run. Real pinned artifact validation and the additional counter/captcha evidence belong
to child 02; user-facing Markdown remains for child 03 and transformation accounting for
child 04. The existing reproduction README still describes the previous script until its
scheduled reconciliation.
