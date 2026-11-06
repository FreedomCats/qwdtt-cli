---
kind: task
status: done
detailed: true
base: 3a76d81
---

# Update the captcha plan comment

## Task

Replace the obsolete E2 reference in the leading comment of `captchaPlan`
(`internal/core/run.go:405`) with an English description of the current `wv`, `rjs`, and
default paths. This function summarizes the selected path for the startup log; verify its
description against `solveCaptchaBySelectedMode` (`internal/core/creds.go:607`). Context:
[architecture.md](../../../../architecture.md), §02 and §08, and the completed
[diagnostic dispatcher task](../01-diagnostic-dispatcher/plan.md).

The coordinator makes this comment-only Go-file edit. `run.go` is project-owned
(`internal/core/run.go:1`, `scripts/vendorcore/rules.go:103`); `vendorcore` preserves files
outside its managed set (`scripts/vendorcore/main.go:346`). Edit the comment directly:
no transformation rule or re-vendoring is needed.

## Steps

1. Compare the three branches of `captchaPlan` (`internal/core/run.go:407`) with the current
   solver branches in `internal/core/creds.go:622`:
   - `wv`: immediately request external handling with event mode `selected`, without native
     Go v2 attempts.
   - `rjs`: up to two Go v2 attempts, then one WBV Auto request (`auto`) on failure, unless
     cancellation or a dead captcha session causes an earlier return.
   - Default (`auto`): up to two Go v2 attempts, up to two WBV Auto requests (`auto`), one
     final Go v2 attempt, then a `manual` request. Later stages are reached only if earlier
     attempts fail and the existing early-return checks allow continuation.
2. Replace only the two-line comment immediately above `captchaPlan` with a concise English
   function comment beginning with `captchaPlan` and describing these paths. Remove the E2
   timeline claim. Keep core mode names and request modes distinct: WBV Auto is a core
   request, not a guarantee that the dispatcher opens a visible window. Leave function
   bodies, returned log strings, and all other comments untouched, including existing
   Russian text elsewhere in the file.
3. Review `git diff -- internal/core/run.go` against both functions, checking the attempt
   counts, fallback order, and conditional progression. Run `git diff --check`. A
   comment-only diff needs no new tests, build or full test run, `vendorcore -check`, or
   live/browser/platform checks; no user input or external access is required.

## Done when

- The English comment describes all three current paths, including their request modes,
  attempt counts, and fallback order as verified against `captchaPlan` and
  `solveCaptchaBySelectedMode`; it does not imply unconditional progression or retain the
  obsolete E2 reference.
- The implementation diff changes only the leading comment of `captchaPlan` in
  `internal/core/run.go`. Function bodies, returned startup log strings, unrelated comments,
  the upstream pin, dependencies, and other implementation files remain unchanged.
- Focused diff review confirms the comment matches the current branches, and
  `git diff --check` passes. No tests asserting comment wording are added.

## Outcome

The leading comment of `captchaPlan` now describes `wv`, `rjs`, and default/`auto`,
including the attempt limits, fallback order, and request modes `selected`, `auto`, and
`manual`. It identifies WBV Auto as a request and makes progression conditional on failed
attempts and the existing early-return checks. The obsolete E2 claim is removed.

Only this function comment changed in the implementation. Function bodies, startup log
strings, unrelated comments, the upstream pin, dependencies, and vendorcore rules are
unchanged. The project-owned file needs no re-vendoring.

Independent focused review found no issues, and `git diff --check` passed. No wording tests
were added. The reviewer's additional build and full-test attempts stopped at a toolchain
compatibility check before compilation or test execution; neither counts as passed
validation. These checks were not required for the comment-only change and were not rerun.
No vendorcore, live, browser, or platform checks were performed. No confirmed problem was
left partially fixed or deferred because of cost.
