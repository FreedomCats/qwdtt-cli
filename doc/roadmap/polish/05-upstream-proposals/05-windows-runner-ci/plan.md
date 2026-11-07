---
kind: task
status: in-progress
detailed: true
depends: [01-repro-runner]
base: c32868b
---

# Windows runner CI regression

## Task

Restore the accepted reproduction runner's portable fixture ownership check so
`TestDirtySourceAndCleanup` can exercise its existing preservation and cleanup scenarios
on Windows. The user explicitly requested fixing the failed CI job first: execute this
task before resuming [the reproduction assets](../02-reproduction-assets/plan.md), despite
the appended node number. Deliver one independently reviewed repair, separate from counter
acceptance and its eventual commit.

This restores the contract of [the safe runner](../01-repro-runner/plan.md), within
[the parent stage](../plan.md) and [architecture section 03](../../../../architecture.md).
It does not change the parent/root goal, scope or completion criteria, the CLI, supported
platforms, upstream pin, dependencies, workflow configuration or reproduction artifacts.
The expected implementation is confined to `scripts/upstreamrepro/run_test.go`; at most
one small additional `_test.go` helper file in that package is needed.

## Evidence and diagnosis

The lead's read-only CI inspection at committed base `c32868b` found that Windows
`go build ./...` and `go vet ./...` passed, while all eleven
`TestDirtySourceAndCleanup` subcases failed at `command cwd is not owned`. Linux CI for
the same base passed. `.github/workflows/windows.yml:46` already selects this package
with `go test -count=1 ./...`; the failure is in a test assertion, not compilation.

Code locations in this diagnosis refer to the committed base; pending counter changes
shift their line numbers:

- `scripts/upstreamrepro/run_test.go:256` stores the fixture's original temporary-parent
  spelling. Its executor at `:342` uses a case-sensitive `strings.HasPrefix` against that
  string before calling any mode-specific interceptor.
- `scripts/upstreamrepro/run.go:247` resolves a local copy of that parent with
  `filepath.EvalSymlinks`, creates the work directory below it at `:261`, and issues the
  first `go env GOMODCACHE` command from that work directory at `:276`. The fixture's
  stored parent is not updated by the value-receiver call.
- Go 1.26.2's standard-library `src/path/filepath/symlink_windows.go` implements
  `evalSymlinks` through `toNorm`, `normVolumeName` and `normBase`: it normalizes the drive
  letter and filesystem component names as well as resolving links. The caller's spelling
  and the resolved spelling of the same directory need not share a byte prefix.

The concrete portability defect is comparing a resolved cwd with an unresolved parent
as raw strings. This can fail before every scenario reaches its intended branch and is
consistent with the CI observations. The available evidence does not identify the exact
spelling difference on the failing worker; do not claim a particular case change, short
name or junction was observed. Use controlled test-owned paths to demonstrate the defect.

## Steps

1. Isolate implementation and acceptance from the suspended counter work. Read
   `git diff HEAD`, `git diff --cached` and `git diff`, and record private fingerprints of
   both the index entries and working-tree bytes for these five existing changes:
   `02-reproduction-assets/01-drop-counters/plan.md` under this stage,
   `doc/upstream-repro/4-dropped-packets.patch`, `doc/upstream-repro/repro_test.go`, and
   `scripts/upstreamrepro/{run.go,run_test.go}`. The counter plan and patch have distinct
   staged/unstaged versions; the staged plan prematurely says done. Preserve every version
   without correcting counter status or accepting that patch here. Have the coordinator
   prepare an unregistered disposable export of committed base `c32868b` from local Git
   objects, for example with `git archive`, and implement, review and test the repair there.
   Do not copy pending counter files into that export, stage from the existing index, or
   use stash, reset, clean, a branch switch or worktree registration in the original tree.
2. In the disposable base, follow `newFixture`, `fixture.executor` and `runner.run` through
   temporary-parent resolution and the first command. Check the workflow selection and
   the standard-library Windows normalization behaviour above. Keep this a fixture repair:
   the production runner's canonicalization, protected-source checks and cleanup ownership
   remain intact. If evidence instead requires a runtime or workflow change, report the
   concrete obstacle before expanding this task.
3. Replace the raw-prefix assertion in `scripts/upstreamrepro/run_test.go` with a small
   test-only ownership predicate. Require existing absolute directory paths, resolve both
   parent and cwd consistently with `filepath.EvalSymlinks`, then use `filepath.Rel` to
   require a strict descendant: no error, `rel != "."`, and `filepath.IsLocal(rel)`.
   Fail closed on resolution/relative-path errors. Keep the check before interception so
   every recorded command, including setup failures, is checked. Preserve a useful
   failure diagnostic. Do not lowercase all paths, use substring matching, remove the
   assertion, skip the eleven Windows scenarios, or weaken the production safeguards.
4. Add focused regression coverage in that same test file, using synthetic directories
   only. A table-driven `TestCommandDirectoryOwnership` must accept owned work/module
   descendants, including paths with spaces, and reject the parent itself, a similarly
   prefixed sibling, an outside/source/project directory, an existing path escaping via
   `..`, and nonexistent paths. Add `TestWindowsTempParentAlias`: on Windows, create a
   mixed-case test-owned component and give the fixture an alternate case spelling of
   that component. Verify with `os.Stat`/`os.SameFile` that it is the same directory and
   demonstrate that the old raw-prefix comparison would reject the resolved command cwd.
   Run the fixture's success path through its real ownership check, then call
   `checkPreserved`. This must exercise the normalization difference without relying on
   the worker's actual directory layout, short-name support, links or elevated rights.
   Keep all eleven existing `TestDirtySourceAndCleanup` modes and their preservation,
   cleanup, offline, cancellation and result-validation assertions enabled and unchanged
   in meaning. A Windows-specific alias test may skip on other operating systems; the
   existing cleanup scenarios must continue to run there.
5. Validate the isolated base with the installed Go 1.26.2 executable selected directly,
   also putting its bin directory first on PATH for subprocesses. Apply
   `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off` before every Go invocation.
   Run the focused ownership/cleanup tests and then the complete runner package:
   `go test -count=1 -run '^(TestCommandDirectoryOwnership|TestWindowsTempParentAlias|TestDirtySourceAndCleanup)$' ./scripts/upstreamrepro`,
   `go test -count=1 ./scripts/upstreamrepro`,
   `go test -race -count=1 ./scripts/upstreamrepro`,
   `go vet ./scripts/upstreamrepro` and `go build ./...`.
   With `GOOS=windows GOARCH=amd64 CGO_ENABLED=0`, also run runner vet, project build,
   and `go test -c -o <validation-dir>/upstreamrepro.test.exe ./scripts/upstreamrepro`,
   writing the test binary outside the repository. Check formatting and the repair diff's
   whitespace. Missing local prerequisites are reported, never downloaded. Do not run
   the ordinary full project test suite, outbound reproductions or a manual CI rerun.
6. The coordinator integrates and commits the independently reviewed base-only repair,
   with this task's status and outcome, through an isolated index based on the current
   HEAD. Do not include any suspended counter changes in that commit. Before integration,
   compare all five files' working-tree fingerprints and index entries with step 1; both
   layers must still match. For the overlapping test file, prepare and check the repair
   against each saved layer separately, preserving the exact pending counter differences.
   After the separate CI commit, reconcile that file's real index entry and working-tree
   version with the accepted repair so neither layer accidentally reverts it. Keep the
   other four counter files and their index entries byte-for-byte unchanged. Verify both
   counter deltas remain intact and run the relevant runner checks on the combined tree.
   Do not accept the counter patch or its premature staged task status. Record unavailable
   Windows execution explicitly: cross-compilation and host tests do not prove Windows
   runtime success or a green workflow. Report the base-only commit, integration checks,
   preserved counter state and next counter acceptance steps to lead.

## Done when

- The fixture accepts a command under the same resolved temporary parent despite an
  alternate Windows spelling. Its focused regression exposes the old raw-prefix defect
  using test-owned paths and is selected by the existing Windows workflow.
- Ownership checking still rejects the parent itself, similarly prefixed siblings,
  outside/escaping paths and resolution errors. All eleven cleanup modes retain their
  dirty-source HEAD, raw index, staged/unstaged content, untracked-sentinel and disposable
  cleanup checks, without a Windows-wide skip or relaxed result expectations.
- The isolated repair passes the focused runner tests, full runner package tests, race
  tests, vet, project build, formatting and whitespace checks offline. Windows runner
  test-binary cross-compilation, runner vet and project cross-build pass with cgo disabled,
  or a concrete unavailable prerequisite is reported. Any unperformed native Windows
  regression/CI execution is clearly identified rather than inferred from compilation.
- The reviewed implementation stays within the bounded test-only repair. Production
  runner behaviour, patch/case matrices, artifacts, dependencies and workflows are unchanged.
  Its separate commit excludes pending counters. Fingerprints confirm all five original
  counter files and their index entries remain untouched until integration; afterwards
  only the overlapping test file incorporates the reviewed repair in both layers, with
  the pending counter deltas preserved. Relevant combined-tree runner checks also pass.
- Parent and root goals, scope and completion criteria remain unchanged. No counter
  acceptance, source-clone mutation, download, push, browser/live action, CI rerun or
  upstream submission is included.

## Risks and handoff

The main risk is hiding an ownership violation while fixing a false rejection. Resolve
both sides and retain strict component containment and negative cases. A case-insensitive
comparison applied indiscriminately would accept distinct directories on case-sensitive
filesystems. The Windows alias regression must prove directory identity, not merely use
two similar strings.

The other risk is accepting the staged counter work accidentally: `run_test.go` overlaps,
and the suspended task's staged plan/patch do not represent its final working-tree state.
Validation on the dirty combined tree cannot establish that the CI repair works alone.
Keep implementation and review on the committed-base export. The coordinator handles code
integration and a separate commit; lead does not write or merge the code itself.

The early assertion prevented all eleven Windows scenarios from reaching their later
branches. Further Windows-specific failures have not been ruled out; report any newly
exposed independent failure before expanding the bounded repair.

No credentials, user service, native Windows access or new user decision is needed to
prepare this repair. Native Windows execution is a separately reported validation gap
until an already authorized runtime or subsequent CI result is available; this task does
not authorize obtaining that runtime, triggering CI or changing workflow coverage.
