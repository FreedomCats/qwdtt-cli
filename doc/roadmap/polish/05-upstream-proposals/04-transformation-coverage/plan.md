---
kind: task
status: in-progress
detailed: true
depends: [03-proposal-documentation]
base: c1de672
---

# Automatic transformation accounting

## Task

Add a focused offline check that accounts for the actual vendorcore transformations and
validates their relationships to the accepted coherent proposals. Use private tooling
metadata and the standard library; do not add a dependency or a public application
interface. This is coordinator-owned code/test work in `scripts/vendorcore/`, not a rewrite
of the proposals or a change to the vendored core.

Follow [architecture R7](../../../../architecture.md), the
[parent scope decision](../plan.md#scope-decision), the accepted
[documentation Outcome](../03-proposal-documentation/plan.md#outcome) and the final
[assets-stage Outcome](../02-reproduction-assets/plan.md#outcome). The latter supersedes
the captcha child's historical unmarked diagnostic. The lead-owned journal is read-only.

Keep this one reviewable result: the three existing implementation files in
`scripts/vendorcore/` (`rules.go`, `convert.go`, `main.go`), a small private `accounting.go`,
and focused tests such as `accounting_test.go`, `pipeline_accounting_test.go`,
`proposal_references_test.go` and `preservation_test.go`. Small golden fixtures and an adjustment to
`main_test.go` should keep the total within roughly 8–12 files. Existing documents, patches,
reproductions, runner implementation, application code, workflows and dependencies are
read-only inputs. Do not build a general inventory framework or another transformation list.

## Steps

1. Establish byte-preservation expectations in `preservation_test.go` before changing the
   table representation. Retain the exact-output `TestConvertMarkers`,
   `TestConvertCaptchaTimeouts`, `TestConvertGroupConfig` and `TestPruneImports` checks and
   the existing failure guards in `main_test.go`. Add a small synthetic `run` fixture using
   temporary source/target directories without `.git`: narrow and restore `replacements`
   with `t.Cleanup` where needed, supply the actual excluded filenames, and exercise a
   BOM/CRLF input, `listen.go`, both generated platform files and `CORE_VERSION`. Compare
   complete output bytes with independent expectations captured from the existing code,
   not values reconstructed from the newly annotated descriptors. Include absent and
   existing final LF, retained extra trailing newlines, excluded-file deletion, an untouched
   project-owned sentinel, repeat conversion and a successful fixture-only `-check` path.
   Use synthetic version values such as `unknown`; never copy an actual work date or path.
2. In `accounting.go`, define minimal private metadata with a stable ID, explicit
   `local`/`upstream`/`mixed` class, nonblank rationale and proposal anchor IDs. Attach it to
   each actual `rule` and `excludedFile` in `rules.go`. Give every `fileOverrides` and
   `generatedFiles` map value a text payload plus metadata; replace log pairs and import
   strings with small named records carrying their existing payload and metadata. Keep
   file keys/import paths/from-strings as family-qualified natural identities, respecting
   case-insensitive file-key uniqueness, and give replacements stable descriptive IDs
   rather than slice indices or regexp-derived identities.

   Update `lookup`, conversion loops and generation reads in `convert.go`/`main.go` only to
   select the payload. Preserve case-insensitive file lookup, replacement order, `Count`,
   patterns, insertions, raw templates, exclusion `Reason` diagnostics and the early
   override return. Collect accounting directly from every current table entry; there is
   no companion per-entry map, index range or frozen total. The validator may live entirely
   in test files: ordinary vendorcore execution must not start reading proposal documents.
3. Bind cross-cutting metadata to the live implementation in `convert.go`/`main.go` using
   explicit private pipeline IDs and a tiny identity marker such as
   `accountedText(pipelineID, value) string`. The marker returns its value unchanged; it does
   not dispatch, enable or disable transformations. Keep metadata keyed by these IDs in
   `accounting.go`, and derive their actual use sites from non-test Go source with a bounded
   `go/ast` check in `pipeline_accounting_test.go`:

   | Pipeline ID | Actual operation and binding |
   |---|---|
   | `strip-utf8-bom` | `readText` returning the existing `bytes.TrimPrefix` result |
   | `normalize-input-crlf` | `convertFile` normalizing CRLF before regexp application |
   | `rewrite-package-main` | `convertFile` applying `packageMain.ReplaceAllString` |
   | `generate-core-version` | `run` emitting the assembled header through `plan.set("CORE_VERSION", ...)` |
   | `normalize-output-crlf` | `run` normalizing every planned output, including override/generated contents |
   | `ensure-output-newline` | `run` appending LF only when the planned output has no final LF |

   All these operations are local adaptation. Account for both CRLF passes, not just the
   one in `convertFile`; BOM handling also applies when reading the target for comparison.
   Preserve `CORE_VERSION` header/field order, dirty handling and output bytes. Despite its
   current comment, the final-newline code does not collapse existing trailing newlines.

   Check the marker/descriptor relationship in both directions: each live ID has metadata,
   each descriptor has its real use site, and IDs/sites are not duplicated or unknown.
   Bind markers to the actual return, assignment or emission above, rather than merely
   searching for their names. Limit the source check to the known payload variables and
   sinks: `readText`'s return, `convertFile`'s `text` assignments/returns, `run`'s
   `text`/`converted` assignments, `plan.set`/`plan.text` writes and the `os.WriteFile`
   payload. Each must use an identified pipeline operation, the relevant annotated table
   loop/lookup, or an unchanged copy of the planned value; handle header assembly under
   the `generate-core-version` emission and error returns as explicit cases.
   An extra bare text rewrite or a bypassed marker must fail this focused source contract.
   Recognize only these small source shapes; do not add whole-program dataflow analysis,
   a Go interpreter, whole-function source snapshots or a generic transformation engine.

   Keep pure validation separate: regexp/count validation, `log.Fatal` rejection, missing
   excluded/rule-file checks, `checkPreamble` pin/dirty diagnostics and `-check`'s read-side
   CRLF-equivalence comparison are guards, not extra output transformations. Do not wrap
   these guards or require upstream proposals for them.
4. Classify the actual entries using their intent and the accepted artifacts. Explain every
   entry in English, allowing a shared rationale only when it still explains that entry.
   Use the following associations; they are design guidance, not a second runtime inventory:

   | Intent in the actual entries | Class and proposal |
   |---|---|
   | Exclusions, platform override/generated files, entry-point extraction/rename, stdout/stage events, rawtun removal, socket hooks, log forwarding and import pruning | `local` |
   | Typed closed-call classification in `main.go` | `upstream`, `proposal-1-closed-call` |
   | `group.go` credentials-failure insertion containing both `emitHashFailed` and `close(signalReady)` | `mixed`, `proposal-2-baton`; explain both purposes |
   | Removal of `configRequestInFlight` and the two per-worker config-selection/completion rewrites | `upstream`, `proposal-3-config-quota` |
   | `Stats` drop fields and the uplink/downlink saturation increments | `upstream`, `proposal-4-dropped-packets` |
   | `stopConfig` installation/stop call and the post-deadline cancellation check | `upstream`, `proposal-5-stop` |
   | Manual and selected timeout replacements | `upstream`, `proposal-6-captcha-timeouts` |
   | The `wv` diagnostic replacement | `upstream`, `proposal-7-captcha-wv-log` |

   `EventStats` insertion and the blanket log replacements are local event/sink integration;
   proposal 4 observes upstream standard logging. Extraction of `main()` is local, not an
   upstream all-group config fix: the owned `internal/core/run.go` wrapper is outside patch 3.
   The diagnostic shares proposal 7's intent without requiring identical local/upstream
   wording: patch 7 retains `[КАПЧА] WBV:` for Android compatibility, while the existing
   local replacement differs. Do not change either payload. Auto remains 10 seconds and
   the two human windows remain 180 seconds; there is no captcha-flow redesign.

   In `accounting_test.go`, validate nonempty/unique identities, allowed classes, meaningful
   nonblank rationales, and nonempty/unique proposal IDs for upstream and mixed entries.
   Local entries have no upstream references. Check the known mixed rule by its stable ID,
   both inserted operations and required proposal, so a syntactically valid downgrade to
   either pure class is rejected. Allow several entries to reference the same proposal.
5. In `proposal_references_test.go`, validate repository references offline using a small
   reader for the accepted document conventions. Metadata supplies proposal anchor IDs;
   `doc/upstream-proposals.md` supplies the unique explicit `<a id="proposal-...">` section
   and that section's `**Patch:**`/`**Reproduction:**` records. Read continuation lines of
   those records, including the multiple names for counters, stop and captcha. Do not
   infer identities from translated headings or limit proposal/rule counts.

   Resolve relative artifact paths against their actual document location and require real
   patch/reproduction files in the repository. Parse the referenced tagged `repro_test.go`
   with `go/parser` and check named top-level test declarations/signatures without compiling
   it or its upstream dependencies. Check the README artifact-table patch/proposal backlinks
   against these section records; its isolated-input rows provide the existing test-name
   cross-check. Keep extraction limited to these records/rows, not arbitrary Markdown or
   prose claims. `scripts/upstreamrepro/run.go` and its existing tests remain the authority
   for executable patch/case/input selection; do not duplicate or reinterpret the runner
   matrix, export its private state, or run it to validate links.

   Diagnose absent/duplicate anchors, missing/ambiguous records, missing or wrong artifact
   references and absent/renamed test declarations with the transformation/proposal ID and
   repository-relative source location. Expectations must come from independent inputs:
   actual entries/use sites, document records, artifact files, test AST and README links,
   not a second metadata copy that confirms itself. Read repository sources/documents from
   the package test directory with portable relative paths, without `script.Root`, a Git
   checkout query or a Go subprocess. Keep access injectable through `io/fs` or an equally
   small reader for negative fixtures.
6. Add fixture/mutation cases alongside the accounting and reference helpers. Clone inputs
   rather than mutating shared tables during accounting checks; do not use `t.Parallel`
   around the existing conversion tests that temporarily replace global `replacements`.
   Require each negative case to fail for its intended diagnostic, with an unchanged valid
   fixture as the positive control. Cover:

   - an otherwise valid new entry with zero accounting in each of `excludedFiles`,
     `fileOverrides`, `generatedFiles`, `replacements`, `logReplacements`, `prunableImports`;
   - missing metadata or a missing/bypassed marker for each cross-cutting operation, an
     added unregistered pipeline ID, an extra unmarked output rewrite and orphan metadata;
   - duplicate IDs/accounting, invalid class, empty/whitespace rationale, forbidden local
     refs, absent or duplicate required refs, and downgrading the known mixed rule;
   - deleted/renamed and duplicate anchors, absent records, missing/renamed patch or
     reproduction files, deleted/renamed test declarations, and inconsistent README links
     or reproduction associations;
   - valid local, upstream and mixed entries, and valid many-to-one proposal references.

   Run mutations through the real collectors/validators/source reader, not only a fabricated
   final inventory. Source/document mutations are in-memory or in temporary fixture trees;
   never edit published documents, artifacts or the real upstream tree to provoke failures.
7. Run the focused checks with the directly selected installed Go 1.26.2 first on PATH and
   `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOWORK=off` already set before any Go command:

   ```sh
   go test -count=1 ./scripts/vendorcore ./scripts/upstreamrepro
   go test -race -count=1 ./scripts/vendorcore ./scripts/upstreamrepro
   go vet ./scripts/vendorcore ./scripts/upstreamrepro
   go build ./...
   git diff --check
   ```

   Check formatting of changed Go files with `gofmt -l` and review the diff for unchanged
   transformation payloads and generated bytes. The new guard must run through ordinary
   `go test ./scripts/vendorcore`, without a build tag, prerequisite checkout or opt-in flag.
   Existing Linux `go test -race ./...` and Windows `go test ./...` selections already include
   it; no workflow edit or CI rerun is needed. Missing cached prerequisites are a reported
   validation gap, never permission to download. Do not run the ordinary full outbound
   suites, a real upstream reproduction matrix, clone/fetch, live-tree vendoring, browser,
   VK/live checks, push, tag, release or submission.

## Done when

- The inventory includes every `replacements` entry and the `excludedFiles`, `fileOverrides`,
  `generatedFiles`, `logReplacements` and `prunableImports` transformations. It also accounts
  explicitly for package rewriting, BOM/line-ending/trailing-newline normalization and
  generated `CORE_VERSION` metadata rather than treating the replacement slice as the whole
  pipeline. Pure validation guards are distinguished from source transformations.
- Each transformation has an explicit local/upstream/mixed classification and enough
  rationale to review its purpose. Upstream and mixed classifications link to existing
  proposal identities; the group rule combining `emitHashFailed` with the baton fix is
  identified as mixed. Local event, socket-hook and extraction scaffolding is accounted
  for, not forced into separate upstream proposals or patches.
- The normal `scripts/vendorcore` test package derives entries from the live tables and
  pipeline use sites, including every operation in step 3. It rejects missing, duplicate
  or orphan accounting, bypassed pipeline bindings, invalid classes/rationales, absent
  required proposal references and broken proposal/patch/reproduction references. Many
  transformations may reference the same proposal. Neither a literal count of 42, equal
  rule/proposal counts nor a separate hand-maintained transformation inventory is the guard.
- Negative fixture/mutation tests cover an unclassified addition in each transformation
  family, missing cross-cutting accounting, the mixed-purpose case, and deleted or renamed
  proposal/asset references. Tests do not need an upstream checkout, network access or a
  live reproduction run, and do not mutate published documents to simulate failures. They
  exercise the same collectors/source checks as the real inventory; valid controls and
  diagnostic-specific failure assertions demonstrate that the guard is actually reached.
- Accepted explicit proposal anchors and their linked patch/reproduction records resolve
  to real files and named test declarations. README artifact/backlink and isolated-input
  test associations agree. Checking these references does not parse arbitrary Markdown,
  copy the runner matrix, execute tagged reproductions or require identical upstream/local
  log strings. Pure guards remain distinct and the known hook/baton rule remains mixed.
- Existing vendorcore conversions and generated contents remain unchanged; existing fixture
  conversion tests pass. Offline `go test ./scripts/vendorcore ./scripts/upstreamrepro`,
  their focused race/vet checks, `go build ./...`, Go formatting and `git diff --check` pass
  with cached prerequisites and the offline/toolchain settings in step 7. The fixture also
  compares override/generated contents and `CORE_VERSION` bytes, LF/BOM handling and repeat
  conversion. Do not run the ordinary full test suite with outbound tests.
- The accepted documentation and artifacts satisfy the parent coverage criteria together
  with this guard. The handoff records the check command and any remaining unperformed
  validation without changing the pin, branch policy, runtime behaviour or other roadmaps.

## Risks and handoff

The main risk is a nominal inventory that checks only itself: table traversal must see new
entries, and pipeline metadata must be bound to actual text-producing source sites. Keep
the AST contract small and explicit about supported shapes; an unsupported new source
shape must fail visibly rather than be skipped. Classification still needs human review
of intent; a nonblank rationale alone cannot prove semantic correctness. Byte fixtures
and the unchanged-payload diff protect the early override path, case-insensitive matching,
the two normalization passes and the existing final-newline behaviour.

This task needs no user access, credentials or live decision. Report actual changed files,
all guarded families and cross-cutting sites, reference-input authorities, focused check
results and missing prerequisites. Keep native Windows execution/current green CI and
live/browser/VK/platform checks unverified. The accepted 12-input/38-cell upstream matrix
is historical evidence, not a check to repeat for metadata. Use sanitized assertions and
examples only; do not publish work dates, environment identifiers or actual home/source/
cache paths. The pin remains `fae121e` on the stable `master` policy.
