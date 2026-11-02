---
kind: task
status: blocked
detailed: true
---

# The core against a fresh clone at the pinned commit

> **Blocked:** the coordinator could not start — the `programmer` and `reviewer`
> agents are not available in the session. Waiting for the user to restart the session.
> Nothing was changed yet.

## Task

Confirm that `internal/core` is exactly what `vendorcore` makes of the upstream at the pinned
commit (`internal/core/CORE_VERSION`, `fae121efc3ef57b633516601d3c0d6b1be1fde7c`), and make
`vendorcore -check` say plainly when the clone it is given cannot answer that question.

The first criterion of the stage ([`../plan.md`](../plan.md)). The check itself already
passes: while splitting the stage, `go run ./scripts/vendorcore -check -upstream <clone>`
against a fresh clone at `fae121e` printed `internal/core совпадает с апстримом fae121e…`
(base `42a4134`). Upstream `master` at `a296c57` has no change in `go_client` since
`fae121e` (`git log fae121e..origin/master -- go_client` is empty).

What is wrong is how `-check` answers when the clone is not a clean checkout of the pinned
commit (`scripts/vendorcore/main.go`, `run`, from line 163):

- **Local changes in the clone.** A clone whose `go_client` has uncommitted changes (for
  instance, every file rewritten with other line endings) gives `расходится: CORE_VERSION`
  and nothing more: the converted Go files still match, because `convertFile` normalises
  CRLF, but `CORE_VERSION` gets the `dirty = true` line. The reader sees a divergence with no
  cause. In write mode the same case already prints `ВНИМАНИЕ: в апстриме есть
  незакоммиченные правки go_client.` (`main.go:334`); `-check` prints nothing of the kind.
  This happened while planning: the clone `vendorcore` takes by default (next to the
  repository) had such changes.
- **Another commit.** A clone at a commit other than the pinned one is compared as if it
  were the pinned one: either a rule fails with `замена в … не сошлась` (as on `develop`), or
  a list of `расходится: …` files appears. Neither says that the clone is simply at another
  commit. `-check` answers "does `internal/core` match its pinned upstream", so the clone's
  commit must be compared with `CORE_VERSION` first.

The result: a `-check` that names the cause in both cases, and the stage's first criterion
confirmed and recorded. No change to `internal/core`.

## Steps

1. **Fresh clone outside the repository.** Clone
   `https://github.com/SpaceNeuroX/proxy-turn-vk-android` into a temporary directory
   (`/tmp/…`) or into `doc/runs/` (ignored by git), then
   `git -C <clone> checkout fae121efc3ef57b633516601d3c0d6b1be1fde7c`. Do not use the clone
   next to the repository that `-upstream` defaults to: it may carry local changes, which is
   exactly the case this task is about. The clone never goes into the repository; check
   `git status` before the commit. Run
   `go run ./scripts/vendorcore -check -upstream <clone>` on the unchanged code: it must say
   the core matches, exit 0. Keep the output for the outcome.

2. **Read the pinned commit.** In `scripts/vendorcore/main.go` add a small function that
   takes the text of `internal/core/CORE_VERSION` and returns the value of the
   `commit   = …` line (the format is written by `run`, `main.go:210–222`: `key = value`,
   key padded with spaces, `#` lines are comments, `dirty = true  # …` carries a trailing
   comment). A missing `CORE_VERSION` (the very first vendoring) or a missing `commit` line
   means "nothing pinned": no comparison, the current behaviour. Keep the parser pure
   (text in, value out) so it is tested without files.

3. **Compare before converting, in `-check` only.** In `run`, right after
   `getUpstreamVersion` (`main.go:175`) and before the conversion loop, when `check` is set:
   - the pinned commit is known, the clone's commit is known (not `unknown` — the clone has
     `.git`), and they differ (compare full hashes, case-insensitively) → print, in yellow,
     that the clone is at `<clone commit>` while `internal/core` is vendored from
     `<pinned>`, and what to do: `git -C <clone> checkout <pinned>` to check the core, or run
     without `-check` to move the core to the clone's commit. Return `1, nil` without
     converting: a rule failing on another commit would only hide the cause;
   - the clone has no `.git` (`Commit == "unknown"`) → one line that the commit cannot be
     verified, then compare as today;
   - the clone's `go_client` has uncommitted changes (`version.Dirty`) → one yellow line
     before the result that the comparison is against the working tree of the clone, not
     against commit `<commit>`, and that `git -C <clone> status -- go_client` shows what
     changed; then compare as today.
   Put the decision itself into a pure function (pinned commit and `upstreamVersion` in;
   whether to stop and the lines to print out), so the tests need neither git nor a clone.
   Write mode does not change.

4. **Tests** in `scripts/vendorcore/main_test.go`, without network and without git:
   - the parser: a `CORE_VERSION` text as `run` writes it, with and without the `dirty`
     line; a text without a `commit` line; an empty text;
   - the decision: same commit (no stop, no lines); another commit (stop, both commits and
     the `checkout` command in the message); `unknown` commit (no stop, the "cannot verify"
     line); dirty clone at the pinned commit (no stop, the warning line); nothing pinned (no
     stop, no lines).
   `TestRunMissingRuleFile` builds a fake upstream without `.git` and calls `run` in write
   mode; it must still pass unchanged.

5. **Comments.** Update the package comment at the top of `main.go` (`-check` paragraph,
   lines 26–28): with `-check` the clone must be at the commit from `CORE_VERSION`, otherwise
   the command says so and exits with 1; local changes in the clone are reported. New
   comments, test names and new output lines are in English, by `AGENTS.md` ("Language");
   the existing Russian lines stay as they are.

6. **Checks.** `gofmt -l scripts/vendorcore` (empty), `go vet ./...`,
   `GOOS=windows go vet ./scripts/vendorcore`, `go test ./scripts/vendorcore`. Then three
   live runs of `-check` against the fresh clone:
   - at `fae121e` — matches, exit 0, no warning;
   - at `origin/master` (`a296c57`) — exit 1, the commit message from step 3, no
     `расходится` list;
   - back at `fae121e` with one uncommitted change in `go_client` (for instance a blank line
     appended to `go_client/main.go`) — the warning from step 3, then the comparison; restore
     with `git -C <clone> checkout -- go_client` afterwards.

7. **Outcome.** In this file's `## Outcome`: the pinned commit, that the fresh clone passes,
   that `master` at `a296c57` has no `go_client` change since, and the new `-check`
   behaviour. No paths of the clone, no dates (`AGENTS.md`).

## Done when

- `go run ./scripts/vendorcore -check -upstream <fresh clone at fae121e>` prints that
  `internal/core` matches, with exit code 0.
- The same against the clone at `a296c57` exits with 1 and names both commits and the
  `git -C <clone> checkout <pinned>` command; it does not print a list of diverging files and
  does not fail on a rule.
- Against the clone at `fae121e` with an uncommitted change in `go_client`, `-check` prints
  the local-changes warning before its result.
- New unit tests for the parser and for the decision pass without network and without git;
  `go test ./scripts/vendorcore`, `go vet ./...` and `GOOS=windows go vet ./scripts/vendorcore`
  pass, `gofmt -l scripts/vendorcore` is empty.
- `git diff --stat` of the commit touches only `scripts/vendorcore/` and this `plan.md`;
  `internal/core` and `CORE_VERSION` are unchanged; no clone or run log is in the repository.

## Risks

- The default `-upstream` is the directory next to the repository. Its name, and anything
  about the machine, must not get into comments, tests or the outcome — use `<clone>`.
- `getUpstreamVersion` already runs `git status --porcelain -- go_client`; do not add a
  second git call for the same thing.
- The early return in step 3 must stay in `-check` only: in write mode moving to another
  commit is the whole point.
