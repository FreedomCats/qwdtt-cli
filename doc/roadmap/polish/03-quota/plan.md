---
kind: stage
status: todo
---

# TURN quota

Goal: the core's behaviour when the TURN quota is exhausted is checked against a real server,
not only against the stand.

The stand (`internal/core/quota_stand_test.go`, commit `c3b39bb`) reproduces the failure with
a pion TURN server and a stub qWDTT. The live error 486 never reproduced, so the fix has only
been checked in the stand.

This stage needs something from the user: a server with an exhausted quota, or a way to
create one without disturbing the profiles in use.

## Done when

- A live run against a server with an exhausted quota shows the client's reaction: what it
  prints, how long it waits, whether it retries.
- The observed behaviour matches the intent of the fix, or the difference is recorded and
  either fixed or accepted.

## Outline

1. Agree with the user on how to get an exhausted quota.
2. The live run; a script under `doc/runs/`, short, with a cleanup.
3. Record the result in the node of the fix and in `architecture.md` if the behaviour changed.
