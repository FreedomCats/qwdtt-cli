---
kind: stage
status: todo
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

The `vendorcore` rules were written against `fae121e`. On `develop`, the rules for
`main.go`, `session.go` and `group.go` would have to be rewritten: the flags changed, and
`Transport` replaces `TCPTransport`. That is the main cost of the move.

Also worth taking while the core is being touched: the `[КАПЧА] WBV: режим из настроек
Android` line is printed with `-v` on every captcha (`creds.go:624`), and the comment of
`captchaPlan` in `run.go` is stale since E2.

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
