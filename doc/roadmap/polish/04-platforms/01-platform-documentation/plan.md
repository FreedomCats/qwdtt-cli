---
kind: task
status: done
base: f810642
detailed: true
---

# Record platform verification boundaries

## Task

Close the platforms stage through a single privacy-safe documentation result. Update
`README.md` and the relevant parts of `doc/architecture.md` so that users can distinguish
the current design from observed platform evidence for Wayland, Windows monitors with
different scaling, and native `linux/arm64` execution. The task must not run a live platform
session, acquire hardware, start a server, or change code, tests, scripts, or configuration.

The existing architecture describes the intended Wayland and Windows window behavior, while
the stage record says that Wayland and heterogeneous-DPI monitor behavior were not checked and
that `linux/arm64` has only release-build and `qemu-aarch64` test evidence, not real hardware
execution. Preserve those bounded facts; do not turn them into native support claims or add
results that were not already established.

## Steps

1. Read the current captcha and release-platform passages in `README.md`, `doc/architecture.md`
   §08, and §09а together with this stage plan. Preserve existing Russian text unless a line
   must change; write all new or substantively changed repository prose in English. Do not
   investigate live machines or infer a result from a cross-build, emulator, Xvfb run, or
   architecture-level code inspection.
2. Record the Wayland boundary in `doc/architecture.md` §08 and the user-facing captcha
   section of `README.md`: the current design has no usable window coordinates on Wayland,
   `--window-position` cannot hide the window there, and the autoclick therefore runs in a
   visible captcha window with the existing instruction not to touch it for a few seconds.
   State explicitly that this is the expected design behavior, not a live Wayland result;
   Wayland compositor/browser behavior and the absence of an X11-only fallback remain
   unverified. Keep the existing X11/Xvfb and native-X11 evidence separate from this gap.
3. Record the Windows multi-monitor boundary in the user-facing README and the internal
   Windows captcha/DPI description in §08. Preserve the implementation-level design that the
   hidden window is placed beyond the virtual screen and that its DPI is selected and resized
   according to the relevant monitor. Mark the case with two or more monitors at different
   scaling as untested, naming the unknowns: actual placement, rendering size/sharpness, and
   DPI behavior when the window is shown or moved between monitors. Do not claim that the
   existing single-monitor or WebView2-staging checks cover this case.
4. Record the `linux/arm64` boundary in the README release/platform material and the Linux
   platform notes in `doc/architecture.md` §09а. Keep the already established release-build
   and `qemu-aarch64` unit-test facts only as bounded non-native evidence, and state plainly
   that no real ARM64 hardware execution has been performed. Name what remains unknown for
   native hardware, including startup and the Linux datapath interactions that depend on the
   host kernel and system networking, without claiming that the release artifact is broken or
   unsupported.
5. Review both document diffs as one result. Ensure every stage item has either a checked
   result or an explicit `untested` label with a reason and exact unknowns; expected behavior
   is not phrased as observation; the hidden-process Windows gap remains in its existing
   blocked node; and no live, browser, privileged, CI, or hardware check is implied. Check
   relative links and run `git diff --check`. Leave code, tests, scripts, configuration,
   completed nodes, and `doc/local/journal.md` unchanged.

## Done when

- `README.md` tells users what to expect for a Wayland captcha window, labels the Wayland
  result as untested, and does not present an X11-only fallback as available.
- `README.md` identifies Windows multi-monitor setups with different scaling as untested and
  states the exact placement, rendering, and shown-window DPI questions that remain open.
- `README.md` identifies the `linux/arm64` release and `qemu-aarch64` evidence as non-native,
  states that real ARM64 hardware has not been run, and does not imply native hardware support
  from the artifact or emulation.
- `doc/architecture.md` §08 explains the expected Wayland user-visible behavior and records
  the unverified Wayland and heterogeneous-DPI cases; §09а records the distinction between
  Linux ARM64 build/emulation evidence and native hardware validation.
- The documentation distinguishes observed X11, WebView2, release-build, and emulated-test
  evidence from untested platform behavior. No new live result, native-support claim, or
  hidden-process conclusion is introduced.
- Only the intended documentation and this task's roadmap bookkeeping change; existing
  Russian text that is not substantively edited remains intact, all changed lines comply with
  the English-new-text rule, publication/privacy review passes, and `git diff --check` passes.
  No code, tests, builds, live sessions, external access, or journal edits are needed.

## Risks and handoff

The main risk is conflating implementation intent with platform observation. Keep the wording
explicitly qualified wherever Wayland or multi-monitor behavior is derived from the code and
existing design notes. Treat `qemu-aarch64` as architecture-level test evidence only; it does
not validate native kernel, device, or desktop integration. Do not broaden this task to the
blocked Windows hidden-process investigation or to any user-facing platform-support decision.

## Outcome

Recorded the verification boundaries for Wayland, Windows heterogeneous-DPI multi-monitor
setups and native `linux/arm64` execution in `README.md` and `doc/architecture.md`. The
documents distinguish expected design behavior from observed X11, WebView2, release-build
and emulated-test evidence. They identify the exact unknowns for native Wayland compositor
and browser behavior, multi-monitor placement/rendering/DPI transitions, and ARM64 hardware
startup and host networking integration.

No live, browser, privileged, hardware or CI check was run. The separate hidden-process
Windows node remains blocked, and no native-support claim was added. Relative links,
publication/privacy review and `git diff --check` passed; code, tests, scripts, configuration
and the journal were not changed.
