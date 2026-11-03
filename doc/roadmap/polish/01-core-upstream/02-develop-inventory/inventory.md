# Inventory: upstream `develop` against the pinned core

## Scope and evidence

This inventory compares the project core pinned at `fae121e` with upstream `go_client` at
`d19a628`. The recorded trial says the core was vendored into a trial copy and adapted there.
That copy and its logs are not available for independent inspection now. The results below
are therefore recorded findings, not checks repeated for this inventory. No live run or
external action was performed.

The available evidence consists of the trial summary in this node's `plan.md`, the stage
summary in `../plan.md`, the roadmap journal, the current `vendorcore` rules, and the current
shell definitions. The trial summary records 11 changed `go_client` files (+994/-128), but
only names nine: `creds.go`, `main.go`, `session.go`, `group.go`, `dispatcher.go`,
`flow_pin.go`, `flow_pin_test.go`, `turn_endpoint.go`, and `turn_endpoint_test.go`. The two
other changed files and their individual effects cannot be identified from the available
evidence. This list is consequently not a complete per-file inventory; it must not be filled
by guessing. The same evidence says `vendorcore` copies 23 files instead of 19, including
both new source files.

## Recorded upstream changes

The recorded summary describes changes to `creds.go`, `main.go`, `session.go`, `group.go`,
and `dispatcher.go`, but does not preserve a per-file diff or enough detail to characterize
their individual changes. It does establish that the replacement rules for `main.go`,
`group.go`, `creds.go`, and `dispatcher.go` still match. The main entry-point replacement
cuts `func main()` as a whole. The changes to `session.go` include moving `dialTURNConn` to
`turn_endpoint.go`.

| File | Recorded effect relevant to this client |
| --- | --- |
| `creds.go` | Changed; its `vendorcore` rules still match. Per-file behavior is not preserved in the available trial notes. |
| `main.go` | Changed; its replacement rule still matches because `func main()` is cut as a whole. |
| `session.go` | Changed; `dialTURNConn` moved to `turn_endpoint.go`. |
| `group.go` | Changed; its replacement rules still match. Per-file behavior is not preserved in the available trial notes. |
| `dispatcher.go` | Changed; its replacement rules still match. Per-file behavior is not preserved in the available trial notes. |
| `flow_pin.go` | New; flow pinning requires `Dispatcher.tunFile`, set only in `-mode rawtun`, which this client excludes in `excludedFiles` (B6). Inert in the supported desktop path. |
| `flow_pin_test.go` | New upstream test for flow pinning; the feature's `rawtun` prerequisite makes it inapplicable to this client path. |
| `turn_endpoint.go` | New; contains `dialTURNConn(ctx, turnTarget)`, TCP and TLS dialers, each using `net.Dialer{Timeout: turnDialTimeout}`, and TLS via uTLS with a Chrome ClientHello. |
| `turn_endpoint_test.go` | New upstream test file for TURN endpoint behavior; its individual cases are not recorded in the available evidence. |
| Two unnamed changed files | The recorded count includes two more changed files, but neither filename nor effect is available. No claim about their contents is made. |

The recorded change adds `Transport string` values `udp`, `tcp`, and `tls` in place of
`TCPTransport bool`. `TurnParams.Host` becomes `Hosts []string`. An empty shell `TurnHost`
must map to `nil`, not `[]string{""}`.

## Vendoring rules

The recorded trial says 2 of 42 replacement rules stop matching: netguard points 1 and 2
(`rules.go`), previously matching the UDP and TCP TURN dial sites in `session.go`. The
`dialTURNConn` implementation moved to `turn_endpoint.go`; TCP now uses the new target-based
signature, and there is an additional TLS dialer. The recorded adaptation points those rules
at `turn_endpoint.go` and sets the TCP pattern count to 2; vendoring and `-check` were recorded
as passing after that trial-only adaptation. These changes are not applied in this repository.

The recorded summary says the rules for `main.go`, `group.go`, `creds.go`, and `dispatcher.go`
continue to match. The full set of other rule outcomes and the resulting trial `rules.go` are
not available for independent review.

## Project files requiring adaptation

| Project file | Required change if adopting this upstream core |
| --- | --- |
| `internal/core/run.go` | Map `TurnHost` to `Hosts`, using `nil` when empty; map the old TCP boolean to `Transport` (`udp` or `tcp`). Any `tls` mapping depends on the user's transport decision. |
| `internal/core/api.go` | Reconcile the shell's `Config.TurnHost` and `TurnParams` construction with `Hosts`. `Config.TurnHost` exists, but the shell currently does not set it. Exposing custom TURN addresses requires a separate user decision and shell setting design. |
| `internal/core/netguard_hook_test.go` | Move the two coverage entries to the new dialer locations and account for the repeated TCP pattern without making one source line satisfy a test requiring each entry exactly once. The recorded notes identify distinct lines or a counted entry as ways to handle the repeated pattern; the exact trial implementation is unavailable. Update UDP/TCP dial subtests to the new signature and add a TLS subtest if TLS dial coverage is adopted. |
| `go.mod` | Promote `github.com/bogdanfinn/utls` from indirect to direct, as recorded for `go mod tidy`. No dependency change is made here. |

`TestNetguardCoverage` currently ensures every configured netguard target is found exactly
once. The old `session.go` entry no longer finds the moved TURN dialers, and duplicate table
entries cannot both match the same line under that invariant. The recorded trial reports the
coverage test failure before adaptation. Whether a TLS dial subtest under the netguard hook
was attempted is not present in the available notes; its status is unknown, not “passed” or
“not attempted”.

## Behavior and open decisions

- **Flow pinning:** `flow_pin.go` is conditional on `Dispatcher.tunFile` and therefore on
  `-mode rawtun`. This client excludes that mode, so the feature is inert for its supported
  path; this is not a reason by itself to change the core.
- **`turns:` filtering:** `turnURLsToAddresses` now preserves the `turns:` prefix, and
  `selectTurnTarget` gives `udp`/`tcp` sessions only entries without that prefix. Previously
  `turns:` entries were stripped and used as plain addresses. Whether VK sends `turns:` URLs
  is unknown. A live observation is required before assessing the user-visible consequence.
- **TLS profile and flags:** should `tls` be accepted in `turn_transport` and exposed in its
  flags? `internal/config/validate.go` currently allows `udp` and `tcp`. This is a user
  decision, not answered here.
- **Custom TURN addresses:** should custom TURN addresses become a profile setting? The core
  has `Config.TurnHost`, but the shell currently does not set it. The inventory does not
  choose an interface or behavior.
- **`turns:` compatibility:** should the changed filtering behavior be accepted? The answer
  depends on observed upstream inputs and is left open.

## Delivery estimate

The recorded trial estimated one or two implementation tasks after the user's decision. A
reasonable split is one core/vendor adaptation task and, only if chosen, one shell/profile
task for TLS and/or custom TURN settings. Expected files include `internal/core` vendored
files (notably `turn_endpoint.go`, `flow_pin.go`, and the upstream files that differ),
`scripts/vendorcore/rules.go`, `internal/core/run.go`, `internal/core/api.go`,
`internal/core/netguard_hook_test.go`, `go.mod` and likely `go.sum`; exposing profile or flag
options would also require the relevant config validation, CLI, example configuration, and
documentation. The exact complete upstream file set remains unknown until the missing two
files are identified from a diff.

Required automated checks for implementation: vendoring, `vendorcore -check` at the chosen
commit, `go build ./...`, `go vet ./...`, `GOOS=windows go vet ./...`, and `go test ./...`.
The recorded trial summary says the remaining `internal/core`, `internal/supervisor`, and
`internal/cli` tests passed on the trial core, but does not provide command output or establish
the result of each whole-project check. Each command's actual execution and result cannot be
independently confirmed from available artifacts. No live run is established by the available
evidence. Before shipping the changed TURN behavior, live checks should cover the selected
transport(s), including a TLS dial through the netguard hook if TLS is exposed, and establish
whether the service supplies `turns:` URLs. Those checks remain unperformed here.
