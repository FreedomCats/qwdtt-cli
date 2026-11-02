---
kind: stage
status: todo
depends: [03-develop-decision]
---

# Carry out the decision about `develop`

Goal: the core, the `vendorcore` rules and the shell are where the user's decision in
[`03-develop-decision`](../03-develop-decision/plan.md) puts them, and the documents say so.

Split only after the decision: what the children are depends on it entirely. The inventory
([`02-develop-inventory`](../02-develop-inventory/plan.md), `inventory.md`) is the input.

## Done when

If the core stays at `fae121e` (`master`, or waiting for `develop` to be released):

- `doc/architecture.md` (R7, or the risk row about the upstream in §12) states which
  upstream branch the core follows, and what triggers the move to `develop`.
- `internal/core` and `CORE_VERSION` are unchanged.

If the core moves to `develop`:

- `CORE_VERSION` names the chosen `develop` commit, and
  `go run ./scripts/vendorcore -check -upstream <fresh clone at that commit>` passes.
- `go build ./...`, `go vet ./...` (also with `GOOS=windows`) and `go test ./...` pass;
  `TestNetguardCoverage` knows every TURN dialer, including TLS, and each has a live dial
  subtest under the hook.
- The shell maps the new `TurnParams` fields, and whatever the user decided about `tls` in
  `turn_transport` and about custom TURN addresses is done (profile, flags, validation,
  README) or explicitly left out.
- `doc/upstream-proposals.md` and `doc/upstream-repro/` name the commit they are written
  against, and the repro run still shows the four fixes.
- `doc/architecture.md` describes the TURN transports the client now has.
