---
kind: stage
status: todo
depends: [03-sanitized-record]
---

# Reconcile live quota behavior

Goal: compare the sanitized live observation with the intent of the quota fix and close any behavioral gap without weakening the stage's real-server criterion.

## Done when

- The result is classified against the intended behavior: a quota-blocked worker does not prevent a worker with a usable session from requesting and delivering configuration, and the client exposes bounded, understandable progress or failure.
- If the live behavior matches the intent, the existing stand tests and the sanitized record are accepted as mutually consistent, with no unnecessary code change.
- If the live behavior differs, the discrepancy is either fixed with a focused regression test and a repeat of the approved live check, or explicitly accepted with its reason and any required user decision; it is never omitted from the record.
- Any implementation change passes the affected Go tests and the relevant broader checks, while another live run still uses only the approved isolated target and the existing cleanup contract.
- The parent stage can be marked complete only after the live evidence, sanitized documentation, and reconciliation all agree that the real exhausted-quota behavior has been checked.
