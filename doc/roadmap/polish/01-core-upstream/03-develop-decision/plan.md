---
kind: task
status: blocked
depends: [02-develop-inventory]
---

# Decision: stay on `master` or move to `develop`

## Task

Not a coordinator task. This node is the user's choice, and `lead` puts it to the user once
[`02-develop-inventory`](../02-develop-inventory/plan.md) is done; it is `blocked` on the
user from the start. The third criterion of the stage ([`../plan.md`](../plan.md)): the
decision is in the journal with its reasons and the rejected options.

What `lead` does here:

1. Read `../02-develop-inventory/inventory.md` and its outcome; update the costs below if
   the inventory changed them.
2. Ask the user (`lead` rules, section 7), with the options and the cost of each.
3. Write the answer into `doc/local/journal.md`: the option taken, the reasons, the options
   rejected. If the user also answered the user-visible questions of the inventory (`tls` in
   `turn_transport`, custom TURN addresses), record those too — they shape
   [`04-carry-out`](../04-carry-out/plan.md).
4. Write a short `## Outcome` here — the decision and its reason, without dates — and set
   `status: done`.

## The question

Upstream `master` has no `go_client` change since the pinned `fae121e`; upstream `develop`
adds TURN over TLS, custom TURN addresses and UDP flow pinning. Which upstream does
`internal/core` follow?

Options, with the cost known before the inventory (the inventory replaces these numbers):

- **Stay on `master`.** Nothing changes now. The core stays at `fae121e` until `master`
  moves; when `develop` reaches `master`, the same move comes back, at whatever cost it has
  then. No TURN over TLS for the users. `04-carry-out` becomes: record and stop.
- **Move to `develop` now** (a pinned `develop` commit in `CORE_VERSION`). From the planning
  trial: 2 of 42 rules to point at `turn_endpoint.go`, the netguard table and its dial tests,
  the `TurnParams` mapping in `run.go`, `go.mod` — a task or two. TLS and custom addresses
  are not visible to the user unless the shell exposes them, which is a further choice. The
  pinned commit is on an unreleased branch that upstream may still rewrite; the UDP/TCP path
  changes too (`turns:` entries are no longer used as plain addresses).
- **Wait for `develop` to be released to `master`, then move.** Like the first option now,
  with a trigger: a check of upstream `master` before each release of this client.

Flow pinning works only in `-mode rawtun`, which this client excludes, so it is no reason to
move either way.

Not to be decided by an agent: this sets which upstream the vendored core tracks, a
long-term dependency, and whether users get a new transport.
