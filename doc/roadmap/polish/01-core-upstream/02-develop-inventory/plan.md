---
kind: task
status: todo
---

# The difference against upstream `develop`

## Task

Describe exactly what moving `internal/core` to upstream `develop` would take, so the user
can choose in [`03-develop-decision`](../03-develop-decision/plan.md) knowing the cost. This
is the second criterion of the stage ([`../plan.md`](../plan.md)): which files change, which
`vendorcore` rules break, what the shell has to change.

The method is a trial run outside the repository: a copy of the working tree (for instance
`git archive HEAD` unpacked into a temporary directory) and a fresh clone of the upstream at
`origin/develop`, `go run ./scripts/vendorcore -upstream <clone>` in the copy, then the rules
and the shell patched there until `go build ./...`, `go vet ./...` and `go test ./...` pass.
Nothing of the trial is committed; the clone and the copy never go into the repository. What
is committed is the written inventory and nothing else: no change to `internal/core`,
`scripts/vendorcore` or the shell.

The inventory goes into `inventory.md` next to this `plan.md` (a file of the node, not a
node), in English, without paths of the machine, dates or anything `AGENTS.md` forbids. It
is what `lead` hands to the user in `03` and what `04-carry-out` is split from.

### Already known from the planning trial

A trial while splitting the stage (base `42a4134`, upstream `develop` at `d19a628`) gave the
following; the task confirms it and fills the gaps, it does not start from nothing:

- `go_client` on `develop` against `fae121e`: 11 files, +994 −128. New: `flow_pin.go`,
  `turn_endpoint.go` and their `_test.go`, which `vendorcore` copies too (19 → 23 files).
- Of the 42 replacement rules, 2 stop matching: netguard points 1 and 2 (`rules.go`, "TURN
  по UDP" and "TURN по TCP"). `dialTURNConn` moved from `session.go` to `turn_endpoint.go`,
  takes `(ctx, turnTarget)`, and now has two `net.Dialer{Timeout: turnDialTimeout}` — TCP and
  the new TLS (`dialTURNTLS`, uTLS with a Chrome ClientHello). With the rules pointed at
  `turn_endpoint.go` and the TCP pattern at `Count: 2`, vendoring passes and `-check` against
  `d19a628` passes. The rules for `main.go`, `group.go`, `creds.go` and `dispatcher.go`
  still match.
- `internal/core/run.go` stops compiling: `TurnParams.Host` is now `Hosts []string`,
  `TCPTransport bool` is now `Transport string` (`udp`, `tcp`, `tls`). An empty `TurnHost`
  must map to `nil`, not to `[]string{""}`.
- `TestNetguardCoverage` fails: the TURN dialers in `turn_endpoint.go` are new places, and
  the `session.go` entry finds nothing. The table cannot hold the same line twice (the test
  wants each entry found exactly once), so the two dialers need distinct lines or a counted
  entry. The live dial subtests "TURN UDP" and "TURN TCP" use the old signature; TLS has no
  subtest yet.
- `go mod tidy` moves `github.com/bogdanfinn/utls` from indirect to direct.
- The rest of `internal/core`, `internal/supervisor` and `internal/cli` tests pass on the
  trial core.
- Flow pinning (`flow_pin.go`) acts only when `Dispatcher.tunFile` is set, that is in
  `-mode rawtun`, which this client excludes (`excludedFiles`, B6). On the desktop it is
  inert code.
- Behaviour change even without TLS: `turnURLsToAddresses` now keeps the `turns:` prefix,
  and `selectTurnTarget` gives `udp`/`tcp` sessions only the entries without it. Before,
  `turns:` entries were stripped and used as plain addresses. Whether VK actually sends
  `turns:` URLs is not known.
- The four patches in `doc/upstream-repro/` apply to `develop` with `git apply --check`.

## Done when

- `inventory.md` lists, against `fae121e`: every changed and new `go_client` file with what
  it changes; every `vendorcore` rule that breaks, with the new pattern and the reason;
  every file of ours that must change (`run.go`, `api.go`, the netguard table and tests,
  `go.mod`) and how; what of `develop` is inert for this client and why.
- It lists the user-visible questions a move raises, without answering them: whether
  `turn_transport` gains `tls` in the profile and the flags (`internal/config/validate.go`
  allows `udp` and `tcp` today); whether custom TURN addresses become a profile setting
  (`Config.TurnHost` exists in `internal/core/api.go` but nothing in the shell sets it); the
  `turns:` filtering above.
- It states what the trial ran and its result: vendoring, `-check`, `go build ./...`,
  `go vet ./...` (also with `GOOS=windows`), `go test ./...`, and whether a TLS dial subtest
  under the netguard hook was tried.
- It gives an estimate of the move in the terms of the roadmap: how many tasks, which
  files, what needs a live run.
- The commit contains `inventory.md` and this `plan.md` only.
