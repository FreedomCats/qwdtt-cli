---
kind: task
status: todo
depends: [02-reproduction-assets]
---

# Reconcile the proposal documentation

## Task

Bring `doc/upstream-proposals.md` and `doc/upstream-repro/README.md` into agreement with
the accepted upstream artifacts and safe runner. This is a Markdown-only task performed
directly by the lead, without a coordinator/reviewer cycle. Do not edit code, tests,
scripts or patch artifacts in this task.

Keep coherent proposals rather than one proposal per replacement. Preserve the existing
proposal identities and unchanged Russian paragraphs; write new or substantively changed
text in English, without translating the whole documents. Follow the parent scope
decision and architecture sections R7 and 08.

## Done when

- Every upstream-intended change has a coherent item explaining what changes, why, an
  offline reproduction and its expected result, and a link to the actual pinned patch.
  Coverage includes the existing five proposals, both human-window timeouts and the `wv`
  diagnostic. Related rules can share an item. Proposal identities/explicit anchors are
  unambiguous for the later automatic check, without a per-transformation proposal catalog.
- The counter item no longer says it has no reproduction or patch. Its explanation
  distinguishes the upstream statistics surface from CLI-only event delivery. The config
  proposal distinguishes the patched per-worker change from the local all-group wrapper
  behaviour; it does not claim the current patch changes upstream `main.go` when it does not.
- The artifact table, test names, patch links and expected baseline/patched results match
  child 02. Instructions describe prepared local input, offline prerequisites and disposable
  copies, not resetting the caller's tree or automatically cloning/downloading. The race
  demonstration explains its injected delay and intentional uninjected skip.
- The distinction between local adaptation, upstream changes and mixed-purpose rules is
  explained without proposing CLI extraction/scaffolding to upstream. Retain the rationale
  for not proposing a captcha response-channel redesign; no accepted decision is reopened.
- Statements distinguish local stand evidence from unperformed live checks; no VK, quota
  or platform verification is implied. Refer to `fae121e`, not an investigated/upgraded
  `develop`. No external submission is performed.
- Relative links resolve, documented local commands and expectations agree with the accepted
  runner, and `git diff --check` passes. No build/test run is needed for a Markdown-only
  diff; do not rerun outbound tests. Review changed text for the publication/privacy rules:
  no work dates, personal environment, home paths, real server/call/profile data or automatic
  attribution trailers.
