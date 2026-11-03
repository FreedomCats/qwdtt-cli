---
kind: task
status: dropped
detailed: true
---

# The difference against upstream `develop`

## Dropped

This inventory task is excluded from the roadmap: upstream `develop` is unstable and the
project will not upgrade to it. The inventory below is retained as historical documentation,
not as a completion requirement or recommendation to move branches.

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
is what `lead` hands to the user in `03`; `04-carry-out` records the resulting decision in the
architecture documentation.

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

## Steps

1. **Reconcile the recorded trial.** Use the planning trial results above and the stage and
   journal notes as the starting evidence; do not repeat the port from scratch. In the
   temporary trial copy, confirm the checked-out upstream commit and the recorded outputs for
   vendoring, `-check`, builds, vet and tests. Record any discrepancy for investigation rather
   than silently replacing the existing result. The trial clone and copy, their logs and any
   patched files stay outside the repository.
2. **Inventory upstream changes.** Compare `go_client` at `fae121e` and `d19a628`; list every
   changed and new file, including the two new source files and their tests, with a concise
   explanation of its effect relevant to this client. Distinguish copied files from changes
   that affect the shell or are inert in this project. Do not infer behaviour for `turns:`
   URLs that has not been observed.
3. **Map vendoring and shell work.** Inspect `scripts/vendorcore/rules.go` against the trial
   result and identify the two unmatched netguard replacements, their new locations and why
   the other rules continue to match. List each affected project file and the required
   adaptation: `internal/core/run.go`, `internal/core/api.go`,
   `internal/core/netguard_hook_test.go` and `go.mod`. Include the `TestNetguardCoverage`
   table/subtests and the empty-host-to-`nil` mapping; state the dependency change without
   making it in the repository.
4. **Separate applicable behaviour from irrelevant code.** Describe what flow pinning
   requires (`rawtun`) and why it is excluded here. Record the `turns:` filtering change and
   its possible consequence as an unverified behaviour, not a conclusion about what VK sends.
5. **Record open user decisions and delivery cost.** In `inventory.md`, list without
   resolving whether `tls` belongs in `turn_transport` and its flags, whether custom TURN
   addresses should become a profile setting, and whether the `turns:` filtering change is
   acceptable. Estimate the move in roadmap terms (tasks, affected files, and live checks
   needed), so `03-develop-decision` can present the choice and `04-carry-out` can record
   the user's decision in the architecture documentation.
6. **Write and verify the inventory.** Create only
   `doc/roadmap/polish/01-core-upstream/02-develop-inventory/inventory.md`, in English. State
   the trial's actual commands/checks and results, including whether a TLS dial subtest under
   the netguard hook was attempted. Check it against the criteria below and `AGENTS.md`: no
   personal data, machine paths, environment details, work dates, temporary-copy paths or
   trial artifacts. Do not edit code, tests, `doc/upstream-proposals.md`, or other roadmap
   nodes; the only repository files for this task are `inventory.md` and this `plan.md`.

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
  `go vet ./...`, `GOOS=windows go vet ./...`, `go test ./...`, and whether a TLS dial
  subtest under the netguard hook was tried. It distinguishes checks actually run from checks
  only proposed or inferred.
- It gives an estimate of the move in the terms of the roadmap: how many tasks, which
  files, what needs a live run.
- The commit contains `inventory.md` and this `plan.md` only.

## Risks

- The available evidence describes a trial at `d19a628`; do not imply it covers a newer
  `develop` commit. If the trial copy or its outputs are unavailable, report that gap and do
  not claim unverified checks passed.
- `turns:` acceptance, adding `tls` to profile settings/flags and exposing custom TURN
  addresses are user decisions. Inventory the consequences and leave each unanswered for
  `03-develop-decision`.
- Keep all trial changes and raw outputs outside the repository. Published inventory content
  must follow `AGENTS.md`, particularly its prohibitions on personal data, environment details
  and dates.

## Outcome

`inventory.md` records the available `d19a628` trial summary, the known upstream and shell
impact, the open user decisions, and an implementation estimate. The trial copy and logs were
unavailable for independent verification, and the two unnamed changed `go_client` files could
not be identified; recorded command outcomes and the TLS netguard subtest status are therefore
explicitly not asserted as independently checked. No live runs or external actions were
performed. The `tls`, custom TURN address, and `turns:` behavior decisions remain open for
`03-develop-decision`.
