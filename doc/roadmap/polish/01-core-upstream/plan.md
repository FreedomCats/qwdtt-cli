---
kind: stage
status: done
base: 42a4134
---

# Core against upstream

Goal: verify that the vendored `internal/core` matches its pinned upstream commit and record
the user's policy of following stable upstream `master`. The project will not move to the
unstable `develop` branch.

The core is vendored at `fae121ef` (2026-08-26, `internal/core/CORE_VERSION`). Upstream
`master` has moved twice since (`d90930e`, `a296c57`) with no change to `go_client`.
Upstream `develop` (`5b023bd`, and `d19a628` for CI) changes `go_client` substantially:
`creds.go`, `main.go`, `session.go`, `group.go`, `dispatcher.go` and the new `flow_pin.go`
and `turn_endpoint.go`, about 1000 lines. The captcha path
(`solveCaptchaBySelectedMode`, `requestWebViewCaptcha`, the window timeouts) and the `wv`
mode are untouched there; the parser of the captcha page (`captcha init json not found`) is
not fixed.

The pinned check is complete in [`01-pinned-check`](01-pinned-check/plan.md). The branch policy
is recorded in the architecture by [`04-carry-out`](04-carry-out/plan.md). Historical
`develop` investigation and decision nodes are not part of this stage's acceptance scope:
`develop` is unstable and no upgrade is planned.

Also worth taking while the core is being touched: the comment of `captchaPlan` in `run.go`
is stale since E2 — it is in the "Done when" of [`02-captcha`](../02-captcha/plan.md) and
stays there. The `[КАПЧА] WBV: режим из настроек Android` line is already replaced by a
`vendorcore` rule (`rules.go`, the `wv` mode line) and needs nothing more.

## Done when

- A fresh clone of the upstream at the pinned commit passes `vendorcore -check`.
- The architecture records that the vendored core follows stable upstream `master` and that
  reconsideration is triggered when `develop` reaches `master`.

## Outline

1. Confirm the pinned-core check against a fresh clone and accept the completed implementation
   and tests in `01-pinned-check`.
2. Accept the completed architecture record of the stable `master` policy in `04-carry-out`.

## Integration handoff

The integration coordinator must continue the existing review without restarting it or losing
its uncommitted test change in `scripts/vendorcore/main_test.go`. Bring that change to
review-ready state and commit it, then accept only this stage's revised scope: the pinned-core
check and the recorded stable-branch policy. Do not restore or repeat the `develop` inventory.

## Children

1. [`01-pinned-check`](01-pinned-check/plan.md) — task: `vendorcore -check` against a fresh
   clone at the pinned commit, and a `-check` that says so when the clone is at another
   commit or has local changes.
2. [`02-develop-inventory`](02-develop-inventory/plan.md) — dropped: `develop` is unstable and
   no upgrade is planned; its existing inventory is retained as historical documentation.
3. [`03-develop-decision`](03-develop-decision/plan.md) — completed historical user decision;
   retained as a record, not a pending choice or coordinator task.
4. [`04-carry-out`](04-carry-out/plan.md) — completed task: record the stable `master` policy
   and reconsideration trigger in the architecture documentation.

## Outcome

Confirmed the pinned-core check implementation and recorded the policy that the vendored core
follows stable upstream `master`, with reconsideration when `develop` reaches `master`. The
`develop` inventory task is dropped because `develop` is unstable and no upgrade is planned;
its existing inventory remains historical documentation, not a completion requirement.

The available upstream clone was dirty, so the pinned check could not be independently
reproduced against a fresh clean clone during integration acceptance. The implementation's
unit tests and the full project build, vet, and test suite passed; the pinned-clone confirmation
remains based on the recorded task outcome.
