---
kind: stage
status: done
base: f810642
---

# Platforms

Goal: what the client does on environments the development machine does not have is either
checked or recorded as untested, with the reason.

- **Wayland**: the captcha window goes to the screen from the start, the autoclick is
  visible. Not checked at all.
- **Several monitors with different scaling** (Windows): the captcha window is placed to the
  right of all monitors in the native DPI of the monitor; not checked.
- **`linux/arm64`**: the release builds it and the unit tests pass under `qemu-aarch64`, but
  it has never run on real hardware.

## Done when

- Each item is either checked live or written into README (`architecture.md` for the
  internals) as untested, with what exactly is unknown.
- For Wayland, `architecture.md` §08 says what the user will see, so the behaviour is not a
  surprise.

## Outline

1. [Record platform verification boundaries](01-platform-documentation/plan.md) — update the
   user-facing and internal documentation with the current design, the evidence already
   available, and the exact unknowns for Wayland, Windows multi-monitor DPI, and native
   `linux/arm64` execution. This is a documentation-only closure; no live platform run is
   authorized or required for this child.

The stage is intentionally not split into live-run children. The existing criteria accept a
sanitized record of untested behavior, and the current user decision authorizes no Wayland,
Windows monitor, or ARM hardware session. A future live check, if authorized separately, must
replace the corresponding untested record rather than be implied by this stage.

## Outcome

The platform stage is complete through its documentation-only child. `README.md` and
`doc/architecture.md` now state the expected visible Wayland captcha behavior and label native
Wayland, heterogeneous-DPI multi-monitor Windows, and native ARM64 execution as untested with
their exact unknowns. Existing X11, WebView2, release-build and `qemu-aarch64` evidence stays
separate from native platform validation. The blocked hidden-process Windows investigation is
unchanged.

Only the two intended documentation files and this roadmap bookkeeping changed. Relative
links, publication/privacy review and whitespace checks passed. No live platform, browser,
privileged, hardware or CI check was run, and no confirmed problem was left incomplete because
of cost.
