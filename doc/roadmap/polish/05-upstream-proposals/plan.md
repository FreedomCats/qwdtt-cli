---
kind: stage
status: todo
---

# Upstream proposals

Goal: `doc/upstream-proposals.md` describes every change the `vendorcore` rules make to the
upstream code, so a reader of the upstream can reproduce and judge each one.

Today the rules raise `captchaSelectedWebViewTimeout` to 180 s and rewrite the `[КАПЧА] WBV`
log line, and neither is written down in the proposals. The proposals cover the earlier core
fixes (954, the TURN quota cancel, the read deadline race) with reproduction tests under
`doc/upstream-repro/`.

Sending the proposals upstream is the user's business and is not part of the roadmap.

## Done when

- Every rule in `scripts/vendorcore/rules.go` has a matching item in
  `doc/upstream-proposals.md`: what changes, why, how to reproduce, the patch.
- The count of rules and the count of items agree, and a test or a script keeps them in
  agreement.

## Outline

1. List the rules and compare them with the proposals.
2. Write the missing items.
3. Decide whether the agreement is kept by hand or by a check.
