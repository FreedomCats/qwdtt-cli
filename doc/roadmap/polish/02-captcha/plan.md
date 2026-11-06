---
kind: stage
status: in-progress
base: 2aed1a1
---

# Captcha leftovers

Goal: a captcha never leaves the user without a way to solve it, and the code and comments
around it tell the truth about the `wv` mode.

Three leftovers from the autoclick work (`doc/autoclick-captcha.md`, tasks A1–A4):

- `qwdtt-cli hashes`, `qwdtt-cli ping` and the hash check in the menu build the core config
  and wait for a captcha without a dispatcher: in `wv` the core waits the full 180 s for an
  answer nobody gives. Before `wv` the wait was about 200 s. `checkCtx` with its 90 s does not
  interrupt the wait (`requestWebViewCaptcha` waits on `context.Background()`).
- `captchaPlan` in `internal/core/run.go` still carries a comment about E2 that is no longer
  true.
- On Windows, starting `qwdtt-cli.exe` as a hidden process breaks the autoclick; the cause
  has not been found.

## Done when

- `hashes`, `ping` and the menu's hash check either open the captcha window through the
  dispatcher or do not reach a captcha at all; the wait without a window is impossible.
- The comment of `captchaPlan` describes the current modes.
- On Windows the hidden-process case is either fixed with a test, or recorded as a known
  limitation in README and in `architecture.md` with what was tried.

## Outline

1. The shared path: a diag command that may meet a captcha takes the same solver as
   `connect`.
2. Tests for `hashes` and `ping` meeting a captcha, without a browser.
3. The comment of `captchaPlan`.
4. The hidden-process run on Windows — needs the user's machine.
