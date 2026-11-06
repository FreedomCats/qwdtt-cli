---
kind: task
status: todo
---

# Update the captcha plan comment

Goal: make the `captchaPlan` comment in `internal/core/run.go` describe the current captcha
modes and their actual paths, rather than referring to the obsolete E2 timeline.

## Done when

- The comment accurately describes the current `wv`, `rjs`, and default mode behavior as
  implemented by `captchaPlan` and `solveCaptchaBySelectedMode`.
- No behavior or unrelated comments are changed; the focused diff is reviewed and
  `git diff --check` passes.
