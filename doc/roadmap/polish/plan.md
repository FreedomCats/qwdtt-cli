---
kind: root
status: in-progress
lang: en
source: doc/architecture.md
branch: main
base: af8db04
---

# polish

The remaining open work of qwdtt-cli after stages E1–E5 and the tasks outside the stages
are closed. The roadmap gathers what was left scattered across `doc/local/resume.md`,
`doc/architecture.md` §11 and §12, and the notes in the stage logs: the vendored core
against the moving upstream, captcha paths that never reach the dispatcher, the live run
under an exhausted TURN quota, the platforms that were never checked, and the upstream
proposals.

How it is built, and the decisions behind the client — [`doc/architecture.md`](../../architecture.md).
That document holds the stages E1–E5 and the history of the decisions; this roadmap holds
what is left and in which order.

## Done when

- `go run ./scripts/vendorcore -check` against a fresh clone of the upstream commit passes,
  and the decision about the upstream `develop` branch is recorded.
- A captcha never leaves the user waiting without a window: `hashes`, `ping` and the hash
  check in the menu go through the captcha dispatcher like `connect` does.
- No open item from the old lists is missing without a decision: each one is either done,
  dropped with a reason, or recorded as a decision in the journal.
- The TURN quota behaviour is checked against a server whose quota is exhausted, not only
  against the stand.
- What cannot be checked on the development machine (Windows autoclick under a hidden
  process, Wayland, several monitors, `linux/arm64`) is either checked live or recorded as
  untested with the reason.
- `doc/upstream-proposals.md` covers every patch the `vendorcore` rules apply, not only the
  first ones.

## Out of the roadmap

These belong to the user, not to an agent, and are not nodes:

- sending the proposals upstream (`doc/upstream-proposals.md`, `doc/upstream-repro/`);
- release tags (`v0.2.0` and later) — only on a direct request;
- pushing: commits are made locally, the user pushes.

## Decisions recorded instead of nodes

- **NSS under `sudo` for a domain user** (`architecture.md` §12) — not fixed by the user's
  decision: no domain users on Linux yet. The fix, if it is ever needed: uid/gid from
  `SUDO_UID`/`SUDO_GID`, and `getent passwd` / `id -G` for the groups.

## Stages

1. [Core against upstream](01-core-upstream/plan.md) — where the vendored core stands, and
   whether to move to the upstream `develop` branch.
2. [Captcha leftovers](02-captcha/plan.md) — the paths that wait for a captcha without a
   window, the stale comment, the hidden-process Windows run.
3. [TURN quota](03-quota/plan.md) — the live run under an exhausted quota.
4. [Platforms](04-platforms/plan.md) — Wayland, several monitors, `linux/arm64` on hardware.
5. [Upstream proposals](05-upstream-proposals/plan.md) — every rule we apply, written down.

The order puts the largest unknown first: the core update may change the code the later
stages touch.
