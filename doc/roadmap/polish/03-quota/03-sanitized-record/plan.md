---
kind: task
status: todo
depends: [02-live-run]
---

# Record sanitized live quota evidence

## Task

Turn the successful live-run evidence into a tracked, privacy-safe record. Update the existing quota discussion in `doc/architecture.md` so it distinguishes the loopback stand from the real exhausted-quota observation, describes the client's output, wait and retry behavior, and states whether the observation matches the intent of the per-worker configuration fix. Keep raw logs in ignored run storage and never publish their sensitive values.

## Done when

- The tracked quota record no longer presents the live `486` check as unperformed and summarizes the real-server result with sanitized names and relative durations only.
- The record states the observed client reaction: printed error or progress, delay, retry behavior, worker/session/configuration outcome, and cleanup result, without copying credentials, real endpoints, profile names, hashes, identities, or calendar dates.
- The record keeps the local stand evidence separate from the live evidence and does not claim that a network outage reproduces quota exhaustion.
- Any mismatch with the intended fix is stated explicitly for reconciliation; the documentation does not silently turn an unsuccessful or inconclusive run into acceptance.
- A privacy review of the tracked diff finds no secret or identifying value, and the raw live artifacts remain outside tracked documentation.
