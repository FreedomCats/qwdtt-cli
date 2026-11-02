---
kind: stage
status: in-progress
base: 42a4134
---

# Core against upstream

Goal: know exactly where the vendored `internal/core` stands against the upstream qWDTT, and
decide with the user whether the project moves to the upstream `develop` branch, which brings
TURN over TLS, custom TURN addresses and UDP flow pinning.

The core is vendored at `fae121ef` (2026-08-26, `internal/core/CORE_VERSION`). Upstream
`master` has moved twice since (`d90930e`, `a296c57`) with no change to `go_client`.
Upstream `develop` (`5b023bd`, and `d19a628` for CI) changes `go_client` substantially:
`creds.go`, `main.go`, `session.go`, `group.go`, `dispatcher.go` and the new `flow_pin.go`
and `turn_endpoint.go`, about 1000 lines. The captcha path
(`solveCaptchaBySelectedMode`, `requestWebViewCaptcha`, the window timeouts) and the `wv`
mode are untouched there; the parser of the captcha page (`captcha init json not found`) is
not fixed.

The `vendorcore` rules were written against `fae121e`. The first estimate was that on
`develop` the rules for `main.go`, `session.go` and `group.go` would have to be rewritten. A
trial run while splitting the stage narrows that: against `d19a628` only 2 of the 42
replacement rules stop matching — the two netguard points of the TURN dialer, which moved
from `session.go` to the new `turn_endpoint.go`. The rules for `main.go` and `group.go` still
match, because `func main()` is cut out whole. The rest falls on our own files: `run.go`
(`Host` → `Hosts`, `TCPTransport` → `Transport`), the netguard coverage test and `go.mod`.
Taking stock properly is [`02-develop-inventory`](02-develop-inventory/plan.md).

Also worth taking while the core is being touched: the comment of `captchaPlan` in `run.go`
is stale since E2 — it is in the "Done when" of [`02-captcha`](../02-captcha/plan.md) and
stays there. The `[КАПЧА] WBV: режим из настроек Android` line is already replaced by a
`vendorcore` rule (`rules.go`, the `wv` mode line) and needs nothing more.

## Done when

- A fresh clone of the upstream at the pinned commit passes `vendorcore -check`.
- The difference between our core and the upstream `develop` is described: which files,
  which rules of `vendorcore` break, what the shell has to change.
- The user has decided between staying on `master` and moving to `develop`, and the decision
  is in the journal with its reasons and the rejected option.

## Outline

1. Check the core against a fresh clone (`vendorcore -check -upstream <clone>`).
2. Take stock of the `develop` difference against the rules and the shell.
3. Put the choice to the user with the cost of each option.
4. Carry out the decision: either record it and stop, or update the core, the rules and the
   shell.

## Children

1. [`01-pinned-check`](01-pinned-check/plan.md) — task: `vendorcore -check` against a fresh
   clone at the pinned commit, and a `-check` that says so when the clone is at another
   commit or has local changes.
2. [`02-develop-inventory`](02-develop-inventory/plan.md) — task: the difference against
   upstream `develop`, written down from a trial run outside the repository.
3. [`03-develop-decision`](03-develop-decision/plan.md) — the user's choice between `master`
   and `develop`; `lead` puts it to the user, it is not a coordinator task.
4. [`04-carry-out`](04-carry-out/plan.md) — stage: carry out the decision; split once the
   decision is known.
