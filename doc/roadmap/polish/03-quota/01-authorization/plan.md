---
kind: task
status: blocked
detailed: true
---

# Authorize a safe exhausted-quota target

## Task

Obtain an explicit, user-approved and isolated way to make a live check receive TURN quota exhaustion. The existing loopback stand already covers the fixed control flow; this task supplies the missing real-server precondition without putting credentials, real endpoints, profile names, or other identifying values into repository files. No external connection or quota-exhaustion attempt may start before the user authorizes the selected method.

## Steps

1. Ask the user to select one safe source of the exhausted-quota condition: an already available disposable qWDTT/TURN server and profile whose quota is exhausted; a disposable server and profile with a bounded way to consume the quota; or another user-supplied mechanism that produces a server-confirmed `486 Allocation Quota Reached` or equivalent response. The target must not be a profile or allocation currently needed for normal use.
2. Obtain the user's explicit agreement to the selected target, method, scope, traffic or allocation bound, and cleanup/reset procedure. The lead records that decision and only sanitized parameters in the journal; secrets and real target details remain outside tracked files.
3. Confirm the handoff contract for the live-run child: how exhaustion will be recognized, how the client can load the approved configuration without committing it, what maximum duration and retry observation window apply, and which processes, allocations, or temporary files must be cleaned up.
4. If no safe target, bounded exhaustion method, or explicit authorization is available, keep this task blocked and do not start the live run.

## Done when

- The user has explicitly selected and authorized a safe live-run method and scope.
- The method explains how the server-side exhausted-quota response will be recognized and how the target and client state will be reset or cleaned up.
- No secret, real server name or address, profile name, identity, or credential value is written to tracked documentation.
- The live-run child has enough sanitized inputs to run with a bound and a cleanup path; otherwise this task remains blocked.
