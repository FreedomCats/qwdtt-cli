---
kind: task
status: done
detailed: true
depends: [05-windows-runner-ci]
base: 9b64b7c
---

# Portable patch-link fixture

## Task

Make `TestDirtySourceAndCleanup/patch_link` exercise unsafe-link rejection independently
of Git's default symlink mode. Deliver a bounded test-only repair in
`scripts/upstreamrepro/run_test.go`, implemented and independently reviewed through the
coordinator. Execute this priority child before the unfinished
integration acceptance of [the reproduction assets](../02-reproduction-assets/plan.md).

Preserve [the safe runner's](../01-repro-runner/plan.md) source protection and rejection
contract and [the accepted ownership repair](../05-windows-runner-ci/plan.md). This follows
[the parent stage](../plan.md) and [architecture R7 and section 03](../../../../architecture.md).
Production runner/application code, vendoring, patch/reproduction artifacts, case and input
matrices, dependencies, supported platforms and workflows remain unchanged. At most one
small additional `_test.go` helper file in the same package is needed. Report a concrete
obstacle to lead before expanding this boundary.

## Evidence and diagnosis

The following code locations refer to this task's base:

- `scripts/upstreamrepro/run_test.go:1174` replaces the first ordinary patch with a patch
  creating `go_client/escape` in mode `120000`, targeting a synthetic source sentinel.
  That replacement no longer changes `fix0.go` to `// after`.
- `scripts/upstreamrepro/run_test.go:978` delegates Git commands to the real executor,
  after offline and directory-ownership checks. It does not specify a symlink mode.
  Its later patch-state assertion at `:1031` still requires `fix0.go` to be patched.
- `scripts/upstreamrepro/run.go:428` checks and applies patches, then calls `checkInput`
  before injection, reproduction-file copying and Go module preparation. `checkInput`
  at `:489` rejects actual links and other unsafe entries by filesystem type; ordinary
  files are allowed.

Git documents `core.symlinks=false` as representing symbolic links with small ordinary
files containing their target text. A plain target-text file is not a filesystem link.
If the fixture's link patch is materialized that way, the rejection premise is absent,
and execution can reach the unrelated `fix0.go` assertion. The concrete defect is the
fixture's dependence on an implicit link-creation mode. The exact Git setting or native
link capability behind a particular failure has not been established. Confirm the
plain-file mechanism with explicit Git modes on disposable synthetic inputs; do not
present that cross-check as native Windows execution.

## Steps

1. Follow `newFixture`, `fixture.executor`, `checkPreserved` and the patch/apply/check path
   in `runInputs`. Re-read the prior ownership task's Outcome; keep its canonical descendant
   check before all interception. Use the existing `patched-closed-call` input, selected
   with `fixtureRun` and `runInputs`, for new patch-link checks. Use only temporary synthetic
   repositories and the existing fake Go executor, not a prepared upstream source or the
   complete reproduction matrix. First characterize the problematic path with explicit
   `-c core.symlinks=false` on real Git apply commands: require a valid patch, successful
   check/application, `os.Lstat` identifying an ordinary file, and exact target-text bytes.
   Inspect this control at the apply/filesystem boundary, before the fake Go executor's
   ordinary patch-state assertion. Confirm the sentinel and unchanged `fix0.go` content.
   Its plain-file outcome characterizes fallback; it is not rejection evidence. If this
   mechanism does not reproduce, report the evidence before prescribing a broader fix.
2. In the patch-link fixture's test-only interception, explicitly request
   `-c core.symlinks=true` for both `git apply --check` and actual `git apply`. Keep the
   override before the Git subcommand and after any conflicting command-line configuration,
   so the effective setting is explicit. A small test helper may prepare these commands
   and call the real `execute`; retain its full `processResult`. Restrict this policy to
   the malicious-link scenario and its focused regression. Do not change `gitCommand`,
   `checkInput` or any production default, and do not emulate rejection by returning an
   invented subprocess failure. Keep the replacement patch intentionally unsafe and the
   executor's ordinary patch-state assertions intact.
3. Have the `patch link` cleanup scenario run only the named single-patch input. Record
   actual apply completion, the materialized entry type when present, and any attempted
   module/test commands. Require rejection before any Go module preparation or selected
   Go test in that input. If real application succeeds, require an actual symlink and the
   runner's `unsafe patched input entry` error. If Git refuses native link creation, require
   a genuine application failure for this otherwise applicable patch, with diagnostics;
   missing Git, malformed patches and unrelated setup failures do not count. A successful
   ordinary-file fallback must never satisfy this strict rejection scenario. Keep the
   other ten cleanup modes and their expectations enabled.
4. Add a focused `TestPatchLinkModes` regression covering the explicit plain-file fallback
   and the strict-link path. Exercise the strict fixture helper with a command that already
   contains `-c core.symlinks=false`, proving that the effective override prevents that
   fallback. On a system permitting native links, successful application must produce a
   symlink that the real `checkInput` rejects. Where native creation is refused, verify the
   actual apply failure and lack of subsequent Go commands; report that branch accurately
   rather than claiming the filesystem guard ran. The fallback characterization must run
   without requiring symlink privileges and must expose the original fixture assumption
   offline. Do not skip the whole patch-link scenario on Windows, relax rejection of actual
   links, remove patch-state assertions, or change matrix expectations to hide the failure.
5. Preserve safety evidence on both outcomes. Register cleanup/preservation assertions so
   they also run after an assertion aborts the test. Retain `checkPreserved` checks of the
   synthetic source's HEAD, raw index, staged/unstaged contents, metadata and untracked
   sentinel, plus empty runner-owned temporary inputs. After preparing the deliberately
   changed fixture patch, snapshot the synthetic project's reproduction/patch files and
   verify their contents and modes are unchanged by execution. Keep every runner-issued
   command in an owned descendant and all offline checks active, including commands
   delegated through the new interception. Retain the archive link/special-entry rejection
   tests, which do not depend on native symlink creation.
6. Select the installed compatible Go 1.26.2 executable directly and put its bin directory
   first on PATH for subprocesses. Before every Go invocation set
   `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off`; use only the local offline cache.
   Run:

   ```text
   go test -count=1 -run '^(TestPatchLinkModes|TestDirtySourceAndCleanup|TestCommandDirectoryOwnership|TestWindowsTempParentAlias|TestArchiveContainment)$' ./scripts/upstreamrepro
   go test -count=1 ./scripts/upstreamrepro
   go test -race -count=1 ./scripts/upstreamrepro
   go vet ./scripts/upstreamrepro
   go build ./...
   ```

   Also use `GOOS=windows GOARCH=amd64 CGO_ENABLED=0` for runner vet, project build, and
   `go test -c -o <validation-dir>/upstreamrepro.test.exe ./scripts/upstreamrepro`, with the
   binary outside the repository. Check Go formatting and `git diff --check`. Missing
   cached prerequisites are reported, never downloaded. Distinguish fresh fixture checks
   from historical task Outcomes, and cross-compilation from native runtime/green CI.

## Done when

- The focused offline regression demonstrates ordinary target-text fallback with an
  explicit false setting, and the strict fixture overrides that setting. Removing the
  override makes the new regression fail under that explicit false setting. The cleanup
  scenario reaches an identified link-related rejection before any patched module/test
  command, rather than the unrelated ordinary patch-state assertion.
- A successfully created native symlink is rejected by the unchanged filesystem guard.
  An unavailable native creation path is identified as Git refusal, with no claim that
  `checkInput` rejected an entry it never received. Actual-link rejection is never replaced
  with acceptance of a plain file or a Windows-wide skip.
- Source HEAD, raw index, edits, metadata and sentinel remain unchanged, synthetic project
  assets retain their post-setup bytes and modes, and owned temporary inputs are removed.
  The ownership/alias regressions, all eleven cleanup modes and archive link rejection
  remain selected by the existing workflow.
- Focused/full runner tests, race tests, runner vet, project build, formatting and whitespace
  checks pass offline. Windows runner vet, project cross-build and test-binary compilation
  pass with cgo disabled, or a concrete missing prerequisite is reported. Native Windows
  runtime and subsequent CI results remain explicitly unverified unless separately observed.
- The independently reviewed implementation stays within the test-only boundary and leaves
  parent/root goals and criteria intact. The reproduction-assets stage remains in progress
  pending its separate integration acceptance; completed tasks are not reopened.

## Risks and handoff

Forcing link creation may expose native permission or filesystem refusal. That proves an
apply-time rejection only; it is not interchangeable with successful creation followed by
the runner's guard. Conversely, a regular target-text file does not redirect filesystem
operations and cannot prove that an unsafe link was rejected. Keep these observations
separate and preserve the exact reason for each result.

The implementation should remain one small fixture change and its regression coverage.
If explicit modes do not explain the failure, or fixing it needs production/workflow/asset
changes, return the concrete finding to lead before broadening this task. No credential,
user service or new access is required. Existing historical Outcomes remain historical;
unavailable temporary evidence is not a fresh run or replay.

Do not run the ordinary full project/outbound suite, real upstream reproductions or the
complete real matrix for this fixture. Native Windows acquisition, browser/live/VK checks,
implicit downloads, push, manual CI reruns, tags, releases and upstream submission are out
of scope. Keep run identifiers, execution details and private evidence in the ignored
journal, which lead maintains. Resume the reproduction-stage integration acceptance only
after this separate priority repair is accepted.

## Outcome

Implemented the bounded test-only repair in `scripts/upstreamrepro/run_test.go` and
`scripts/upstreamrepro/patch_link_test.go`, accepted after independent review without
correction requests.

- The explicit `core.symlinks=false` control uses `newFixture`, the existing
  `patched-closed-call` input and `runInputs`. Real Git check and application succeed,
  and `os.Lstat` identifies an ordinary file containing the exact target text. The
  synthetic source sentinel and unchanged `fix0.go` are checked at the apply/filesystem
  boundary. The control stops its dedicated goroutine with `runtime.Goexit` only after
  these observations, unwinding runner cleanup before any fake Go module/test command
  or ordinary patch-state assertion. It does not manufacture a subprocess failure or
  count the plain file as rejection evidence.
- The strict interceptor requests `core.symlinks=true` before both Git apply commands,
  after an explicit conflicting false setting. It delegates to the real executor and
  retains the complete process results. Both the focused strict regression and the
  cleanup scenario created an actual symlink, which the unchanged `checkInput` rejected
  with `unsafe patched input entry` before any Go module preparation or selected test.
- The strict outcome assertions distinguish successful native-link creation from a
  diagnosed Git refusal after a successful applicability check. They reject ordinary
  files, missing Git, process errors and unrelated application failures. Diagnostic
  classification has positive permission/privilege controls and negative unrelated-error
  controls; actual native-link refusal was not observed in the fixture runs.
- The `patch link` cleanup scenario selects only `patched-closed-call`. All eleven cleanup
  modes, the exact ordinary patch-state assertions, archive link/special-entry rejection,
  canonical command-directory ownership and offline/no-fetch checks remain enabled.
  Git commands delegated by the new interceptor retain the original Git assertions.
- Cleanup assertions run after fatal assertions as well as normal completion. They retain
  the source HEAD, raw index, staged and unstaged contents, metadata and untracked sentinel,
  require empty runner-owned temporary inputs, and compare reproduction/patch asset bytes
  and modes with snapshots taken after the deliberate fixture patch replacement.

Removing the effective true override while retaining explicit false mode made the focused
strict regression fail on the behavioral path: real Git created an ordinary target-text
file and the original `fix0.go` patch-state assertion failed. Restoring the helper recovered
its exact pre-mutation bytes and passing checks.

The requested focused tests, complete runner package, race tests, runner vet, project build,
formatting and whitespace checks passed offline with the installed Go 1.26.2 executable
selected directly and first on PATH. Windows/amd64 runner vet, project cross-build and
runner test-binary compilation also passed with cgo disabled. Independent review repeated
the bounded validation set and confirmed the mutation evidence and restoration without
findings. The Windows-only alias regression skipped outside Windows. Native Windows
runtime, actual Git link-creation refusal there and a subsequent green CI result remain
unverified; cross-compilation is not runtime evidence. The original failing worker's exact
Git configuration and native-link capability remain unknown.

Production runner/application code, artifacts, matrices, dependencies, supported platforms,
workflows and completed tasks are unchanged. The reproduction-assets stage remains in
progress pending its separate integration acceptance; its historical Outcomes are not
claimed as fresh reproduction or matrix runs. No ordinary whole-project/outbound test
suite, real upstream reproduction/matrix, live or browser check, download, manual CI rerun,
push, tag, release or upstream submission was included in this repair's acceptance.

No independent review finding required correction or was rejected. No separate incidental
old-code problem was reported, and no confirmed problem was left partially fixed because
of implementation cost.
