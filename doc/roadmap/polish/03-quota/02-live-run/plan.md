---
kind: task
status: todo
depends: [01-authorization]
---

# Run the live exhausted-quota check

## Task

Run the client for a bounded period against the user-approved real qWDTT/TURN target while its quota is exhausted. Use a short ignored script under `doc/runs/` with explicit cleanup, and capture the client's printed error, wait time, retry cadence, worker/session state, and shutdown. The old `turn-outage` run is not sufficient: it exercised network outages and recorded no `486`.

## Done when

- Raw run evidence shows that the real target rejected an allocation because its quota was exhausted, using `486 Allocation Quota Reached` or the target's equivalent server-confirmed indication rather than a generic timeout or network outage.
- The evidence captures what the client prints, how long it waits before useful progress or failure, whether it retries, whether another worker obtains a session and configuration, and how the client exits.
- The run script bounds the observation, handles interruption, removes its temporary state, and leaves no client, helper, or deliberately created server process running.
- Raw logs and configuration remain ignored or outside the repository and are not copied into tracked files with secrets, real endpoints, profile names, hashes, identities, or dates.
- A run that does not establish exhausted-quota behavior is reported as an unsuccessful attempt and does not satisfy this child or the parent stage; the authorization/method must then be revisited.
