---
kind: stage
status: todo
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

1. Wayland: an X11-only fallback is not an option; decide whether to check it live or to
   document the current behaviour.
2. Windows monitors: needs the user's machine with two monitors of different scaling, if
   there is one.
3. `linux/arm64`: needs hardware; CI already covers the build.
