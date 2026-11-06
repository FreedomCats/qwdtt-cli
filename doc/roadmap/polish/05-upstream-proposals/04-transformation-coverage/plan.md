---
kind: task
status: todo
depends: [03-proposal-documentation]
---

# Automatic transformation accounting

## Task

Add a focused offline check that accounts for the actual vendorcore transformations and
validates their relationships to the accepted coherent proposals. Use private tooling
metadata and the standard library; do not add a dependency or a public application
interface. This is coordinator-owned code/test work in `scripts/vendorcore/`, not a rewrite
of the proposals or a change to the vendored core.

The implementation area is `scripts/vendorcore/rules.go`, the conversion pipeline in
`convert.go`/`main.go`, and focused accounting tests/helpers. Choose the smallest metadata
representation that can be checked against the real entries and pipeline operations,
without a second manually maintained list that silently omits new transformations.

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
- A focused test in the normal `scripts/vendorcore` test package compares the metadata
  with the actual transformation inventory and proposal anchors. It rejects missing or
  duplicate accounting, invalid classifications, absent required proposal references and
  broken proposal/patch/reproduction references. Many transformations may reference the
  same proposal. Neither a literal count of 42 nor equal rule/proposal counts is the guard.
- Negative fixture/mutation tests cover an unclassified addition in each transformation
  family, missing cross-cutting accounting, the mixed-purpose case, and deleted or renamed
  proposal/asset references. Tests do not need an upstream checkout, network access or a
  live reproduction run, and do not mutate published documents to simulate failures.
- Existing vendorcore conversions and generated contents remain unchanged; existing fixture
  conversion tests pass. Offline `go test ./scripts/vendorcore ./scripts/upstreamrepro`,
  their focused race/vet checks, `go build ./...` and `git diff --check` pass with cached
  prerequisites. Do not run the ordinary full test suite with outbound tests.
- The accepted documentation and artifacts satisfy the parent coverage criteria together
  with this guard. The handoff records the check command and any remaining unperformed
  validation without changing the pin, branch policy, runtime behaviour or other roadmaps.
