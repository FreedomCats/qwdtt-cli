---
kind: task
status: todo
---

# Human captcha evidence

## Task

Supply independently reviewable upstream evidence for the existing human-window changes:
manual 60 to 180 seconds, selected 120 to 180 seconds, and a `wv` diagnostic without an
Android-settings claim. Keep the auto timeout at 10 seconds and preserve the captcha
request/response flow. Follow [the parent stage](../plan.md) and
[architecture sections R5 and 08](../../../../../architecture.md).

The intended artifacts are `doc/upstream-repro/6-captcha-timeouts.patch` and
`7-captcha-wv-log.patch`, both against pinned upstream `go_client/creds.go`. Extend
`doc/upstream-repro/repro_test.go` and the case/patch expectations and focused fixtures in
`scripts/upstreamrepro/{run.go,run_test.go}`. The two human deadlines form one coherent
patch; the diagnostic-only change stays separately identifiable. This task normally
touches five repository files and introduces no local runtime change. The shared runner
edits follow the counter task in execution order; the captcha semantics are independent.

## Done when

- Both patches apply individually to the full pin, compile in upstream `package main`,
  and use upstream constants, stdout/channel handling and the standard logger. They do
  not import local events, `CaptchaTimeout`, `Control`, `emit`, `logf` or CLI scaffolding.
- Fast checks distinguish the baseline manual/selected durations from the patched
  180-second durations with named assertions. A separate control passes on both inputs
  for the unchanged 10-second auto window. Checks cover the existing call-site use of
  these durations without sleeping through any human deadline.
- Diagnostic evidence fails specifically on the baseline Android-settings claim and
  passes with the `wv` message. The selected-mode path still requests one `selected`
  window directly. Any exercised request receives a synthetic immediate channel response;
  profile-file side effects, global mode/channel state and log/stdout capture are isolated
  and restored. No real captcha/browser or native solver request to VK is made.
- The runner's explicit expectations cover baseline failures, the always-passing auto
  control and patched passes. Timeout-only and diagnostic-only inputs select their
  corresponding checks; fixtures reject wrong assertions, missing tests, unexpected
  skips and process/setup errors without weakening the retained matrix.
- Focused real baseline and individually patched checks use private pinned inputs and
  cached prerequisites. Focused runner tests/race/vet, offline project build and
  whitespace checks pass; missing prerequisites or unperformed checks are reported as
  gaps without downloads. Shared runner safety and the legacy deadline injection remain
  intact for final validation.
- Handoff records actual artifact/test names and outcomes for the documentation sibling.
  Proposal prose and the reproduction README remain outside this task; no dependency,
  response-channel redesign, local runtime fix or captcha-flow change is introduced.
