---
kind: task
status: done
detailed: true
depends: [03-develop-decision]
base: 698134d
---

# Record the upstream branch policy

## Task

Record the user's decision in [`doc/architecture.md`](../../../../architecture.md): the
vendored core follows stable upstream `master`, and reconsideration is triggered when upstream
`develop` reaches `master`. Keep `internal/core` and `CORE_VERSION` unchanged. The existing
Russian architecture text stays as written; add or change only the necessary lines in English.
Do not introduce a separate check before every client release.

The decision is recorded in
[`03-develop-decision`](../03-develop-decision/plan.md). The incomplete evidence is documented
in [`02-develop-inventory`](../02-develop-inventory/inventory.md): the trial copy and logs are
unavailable, two changed upstream files are unidentified, and not all checks are confirmed.
The user chose stable `master` with that evidence gap understood; do not repeat the inventory
or move to `develop` as part of this task.

## Steps

1. In `doc/architecture.md`, add concise English policy text under R7 and align the upstream
   risk row in §12 if needed. State that the core tracks stable `master`, and reconsider moving
   when `develop` reaches `master`. Preserve existing Russian prose; do not translate the
   document or add a per-release check requirement.
2. Review the diff to confirm it records the chosen policy and does not change
   `internal/core` or `CORE_VERSION`.

## Done when

- `doc/architecture.md` explicitly says the vendored core follows upstream `master` and
  identifies `develop` reaching `master` as the trigger to reconsider.
- Existing Russian architecture text is not translated; any new or changed policy lines are
  in English.
- `internal/core` and `CORE_VERSION` are unchanged, and the diff adds no separate per-release
  check requirement.
- The documentation diff is reviewed; no build or code tests are needed for this documentation-only change.

## Outcome

Recorded that the vendored core follows upstream's stable `master` branch and that moving to
`develop` should be reconsidered when `develop` reaches `master`. The existing Russian
architecture text and the §12 upstream risk row were left unchanged. `internal/core` and
`CORE_VERSION` remain unchanged; no per-release check requirement was added.
