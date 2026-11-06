---
kind: stage
status: blocked
---

# Windows autoclick under a hidden process

Goal: determine what fails when Windows autoclick runs with `qwdtt-cli.exe` as a hidden
process, then fix and test it or record the verified limitation.

This stage depends on a live run on the user's Windows machine. The failure cause is unknown;
do not start the run or spend a user session without the user's explicit agreement. The
current implementation is in `internal/captcha/hiddenwindow_windows.go`,
`internal/captcha/webview2_devtools_windows.go`, and the Windows WebView2 solver. Keep the
stable upstream policy on `master`; this work does not involve changing upstream branches.

## Done when

- With the user's agreement, the hidden-process case is reproduced and its cause is
  established on the user's Windows machine.
- The case is either fixed with an appropriate test, or recorded as a known limitation in
  `README.md` and `doc/architecture.md` with the verified attempts. Preserve existing Russian
  text; write only new or changed documentation lines in English.
- Relevant Windows-targeted tests/build checks pass. No live run is performed before the
  user agrees to spend a session on it.
