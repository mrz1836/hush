# Implementation Plan: `hush supervise` + `hush client status` + `hush client refresh`

**Branch**: `023-cli-supervise-and-client` | **Date**: 2026-05-12 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification at `/Users/mrz/projects/hush/specs/023-cli-supervise-and-client/spec.md`
**SDD chunk**: [docs/sdd/SDD-23.md](../../docs/sdd/SDD-23.md)

## Summary

SDD-23 wires the supervisor building blocks delivered by chunks 18–22 into three
operator-facing cobra subcommands: `hush supervise` (foreground orchestrator),
`hush client status` (status-socket query, TTY-aware), and `hush client refresh`
(force an immediate refill via the status socket). The work is **orchestration
glue only** — every behavioural rule of the supervisor lifecycle (state-table,
exit-code mapping, refill/refresh/grace, pidfile + socket lifecycle) is already
owned by `internal/supervise`. The CLI files call into the package and translate
flags into inputs; they never reason about state edges or exit-codes.

Two artifacts compose the deliverable:

- `internal/cli/supervise.go` — `newSuperviseCmd()` mounted on the existing
  cobra root via `root.AddCommand(...)` in `Execute`. Parses `<config-path>`,
  `--dry-run`, `--grace-window <dur>`, `--no-cache`. On dry-run, renders the
  canonical `/claim` payload via `internal/transport/sign.CanonicalJSON` and
  exits 0. On normal start, sequences pidfile → state.Store →
  StatusServer goroutine → Refresher goroutine → initial Refill → Child →
  child-exit wait loop. On SIGTERM/SIGINT, cancels ctx, forwards to child,
  waits, releases pidfile.

- `internal/cli/client.go` — `newClientCmd()` parent with two leaf subcommands
  (`status`, `refresh`). Each resolves the supervisor socket path via the
  precedence rule `--socket` → `--supervisor NAME` → auto-detect-single-socket,
  dials the Unix socket with a verb-bounded read deadline, and maps the
  supervisor's terminal response to an exit-code via the existing `mapErr`
  taxonomy.

One discovery during planning: SDD-22's `socket.go` currently reads the request
line but ignores it (always renders the status document). SDD-23 must extend
that dispatch with verb-routing (`status` | `refresh`), wired via a new
package-private `attachRefreshHandler` hook on `*StatusServer` (mirrors the
existing `attach(StatusInputs)` precedent from SDD-21/SDD-22). This is the only
behavioural change to `internal/supervise` in this chunk; it is documented in
Complexity Tracking with the rationale.

## Technical Context

**Language/Version**: Go 1.24 (per `go.mod`), CGO disabled
**Primary Dependencies**:
- `github.com/spf13/cobra` (already wired by SDD-14 root)
- `internal/supervise` (config, state, child, refill, refresh, grace, pidfile, socket — all SDD-18/19/20/21/22)
- `internal/transport/sign` (`CanonicalJSON` for dry-run; no `Sign` call on the dry-run path — FR-023-9)
- `log/slog` (Go stdlib, project-standard)
- `golang.org/x/term` (TTY detection for `client status`)
**Storage**: N/A — no persistent state owned by this chunk; the supervise package owns the PID file and Unix socket
**Testing**: standard `go test -race`, table-driven unit tests per `.github/tech-conventions/testing-standards.md`; integration tests gated by `//go:build integration` use the chassis already established by `serve_integration_test.go` and `request_integration_test.go`
**Target Platform**: macOS + Linux (Tailscale-only — Constitution VI). No Windows. No per-OS branches in this chunk's files; platform-specific socket-path resolution stays in `internal/supervise/socket_{darwin,linux}.go`.
**Project Type**: Single Go module / CLI (`cmd/hush` thin shell → `internal/cli` orchestration)
**Performance Goals**:
- `hush supervise --dry-run` < 500 ms end-to-end on any valid config (SC-023-2)
- `hush client status` < 1 s round-trip for a healthy local supervisor (SC-023-3)
- `hush client refresh` bounded by FR-023-24 90 s ceiling (SC-023-4)
- Duplicate `hush supervise` start fails within 1 s with the duplicate-supervisor message (SC-023-7)
- Clean SIGTERM releases pidfile + socket within 5 s (SC-023-8)
**Constraints**:
- No HTTP, no TCP, no bearer/HMAC/signed-cookie auth in any path that this chunk introduces (Constitution V)
- No `runtime.GOOS` branches in `supervise.go` / `client.go` (Constitution VII + SDD anti-contract)
- No new `go.mod` direct dependencies (Constitution XI)
- No `string(decryptedBytes)` in any code path (Constitution X)
- All goroutines spawned by `hush supervise` MUST have a clear owner, cancellation path, termination condition, and a top-frame `recover()` (Constitution IX)
- `os.Exit` is forbidden inside any subcommand; subcommands return errors and `cli.Execute` maps via `mapErr` (already-locked SDD-14 convention)
**Scale/Scope**:
- ~600 LOC of net code split across `supervise.go` (~350) and `client.go` (~250)
- ~700 LOC of test code across `supervise_test.go` + `client_test.go` + one integration test
- ≥ 85 % statement coverage on the two production files (SC-023-10)
- The refresh-verb dispatch extension to `internal/supervise/socket.go` adds ~30 LOC there + ~80 LOC of new test coverage

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Applies? | How this chunk satisfies it |
|-----------|---------|------------------------------|
| **I. Zero files at rest on agent machines** | Indirect | This chunk's three subcommands never write a secret to disk. The supervisor's grace cache (SDD-21) holds plaintext in mlocked memory only; the dry-run path writes the canonical payload to stdout but **the payload contains no secret material** (just `name`, `scope`, `requested_ttl`, `session_type`). No new file on agent disk. |
| **II. Approval is human, approval is phone** | Indirect | The orchestrator drives the existing SDD-21 refill path which already calls `/claim`; the supervisor's `awaiting-approval` state is reached via the existing state-table only. Dry-run does NOT contact Discord (FR-023-9). |
| **III. Defense in depth through crypto layering** | No change | This chunk adds no crypto layers. It re-uses the already-validated `sign.CanonicalJSON` for the dry-run preview path. The `Sign` call itself is NOT made on dry-run (FR-023-9 prohibits it). |
| **IV. Supervisor for daemons, wrap-shell for humans** | **Load-bearing** | `hush supervise` IS the daemon access pattern from this principle. The TTL discipline (`requested_ttl ≤ max_supervisor_ttl`) is enforced by SDD-18's config loader — this chunk surfaces those errors verbatim via `mapErr`. The state machine that holds JWT + ECIES key across child restarts is owned by SDD-19/21; this chunk wires it. The grace cache TTL cap (4 h) is enforced both at config load (SDD-18) and at the `--grace-window` flag override (FR-023-12, this chunk). The principle's "supervisor MUST zero secret material after handoff to child, EXCEPT during the optional grace-window cache" is delivered by SDD-21's `Refiller.Refill` + `Grace`; this chunk delegates. |
| **V. Staleness is visible, failure is loud** | **Load-bearing** | The status socket is the operator-visible freshness API this principle mandates. `hush client status` is the read side, `hush client refresh` is the write side. JSON shape conforms exactly to `docs/CONFIG-SCHEMA.md §"Client status output schema"` — owned by SDD-22, consumed verbatim here. Exit code 78 propagation: the supervisor's wait loop observes child exit 78 → transitions via `EventChildExit78Stale` (SDD-19), the wait loop fetches `Refill` (SDD-21), surfaces `ErrJTIUnknown` as `EventFetchAuthRequired` → `StateAwaitingApproval`. `mapErr` does NOT touch ExitConfigStale (78) — that reserved code is used only when the supervisor surfaces a child's verbatim 78 to its parent (service manager). |
| **VI. Tailscale-only, never public** | Indirect | No network listener added by this chunk. The status socket is a Unix domain socket (Constitution V); no TCP/HTTP path is introduced. |
| **VII. CLI design standards** | **Load-bearing** | The three subcommands obey the noun-verb pattern: `hush supervise <path>`, `hush client status`, `hush client refresh`. Global flags (`--config/-c`, `--verbose/-v`, `--quiet/-q`, `--no-color`) honored via the existing `addPersistentFlags` (SDD-14). Output: TTY → human text, pipe → JSON (NFR-10) for `client status`; `--json` flag is explicit override (FR-023-17a). Exit codes mapped through the existing `mapErr` taxonomy (FR-023-26). The `ExitConfigStale = 78` constant is the child-supervisor contract — never raised by `client status` / `client refresh`, raised by `hush supervise` only when surfacing the child's verbatim exit. |
| **VIII. Testing discipline** | **Load-bearing** | Coverage target ≥ 85 % on `supervise.go` + `client.go` (SC-023-10). TDD-mandatory per chunk: every behaviour test (`TestSupervise_DryRunPrintsCanonicalPayload`, `TestSupervise_GraceWindowOverrideTakesPrecedence`, `TestClientStatus_TTYHumanSummary`, `TestClientRefresh_AckMapsToExitOK`, etc. — see SDD-23 §Prompt 4) is authored BEFORE the implementation it covers. Sentinel-leak tests assert FR-023-27 (no secret in stdout / stderr / logs) via the existing `internal/cli/test_sentinels_test.go` pattern. AC-MATRIX rows for AC-10 Scenarios 12 (status check) and 14 (duplicate supervisor refused) land in the integration test suite. |
| **IX. Idiomatic Go discipline** | **Load-bearing** | Context propagation: every I/O path takes `context.Context` from `cmd.Context()` (cobra wiring already injects); the supervisor wait loop derives its own ctx via `signal.NotifyContext`. Error handling: every error wrapped with `%w`; sentinel errors compared via `errors.Is`; no error-string comparisons. No new globals, no new `init()`. Panics reserved for `cmd/hush` startup (which already exists; this chunk adds none). Goroutines (4 spawned by `hush supervise` plus those owned by SDD-22's `StatusServer`): each documented with owner, cancellation path, termination condition, and top-frame `recover()`. Interfaces accepted at consumers (e.g. `StatusInputs` already; this chunk adds a minimal package-private `refreshHandler func(ctx) error` callback to the same attach precedent). Package layout unchanged — production code stays under `internal/cli`. Modules-only; no vendor. CGO disabled. Generics not used. |
| **X. Observability & Redaction** | **Load-bearing** | `log/slog` via the existing `cli` logger; secret values are NEVER logged. `Snapshot.Token` (the JWT bearer) never reaches stdout, stderr, or any log record — the status JSON DTO has no `token` field (already enforced by SDD-22's `statusJSON`). Error messages identify scope name / supervisor name / socket path / failure mode but never include the secret. Discord alert tiers are emitted by `internal/supervise` chunks (SDD-19/21); this chunk doesn't introduce new alerts. |
| **XI. Native-first, minimal dependencies, ephemeral vault** | **Load-bearing** | No new `go.mod` direct dependency. `cobra`, `slog`, `golang.org/x/term`, and `internal/transport/sign` are already in the module. The dry-run preview uses the existing canonicaliser; no new JSON library or canonicalisation primitive added. `govulncheck` and `gitleaks` already gate via pre-commit. |

**Initial gate**: PASS with one Complexity Tracking entry (refresh-verb dispatch
extension to SDD-22's `socket.go`). All other principles are satisfied without
deviation.

## Project Structure

### Documentation (this feature)

```text
specs/023-cli-supervise-and-client/
├── plan.md              # This file (Phase 0–1 output of /speckit-plan)
├── spec.md              # Feature specification (already authored by /speckit-specify)
├── research.md          # Phase 0 output: 10 research notes addressing every NEEDS CLARIFICATION
├── data-model.md        # Phase 1: orchestrator state inventory, flag inputs, status doc projection
├── quickstart.md        # Phase 1: operator walkthrough (init → dry-run → supervise → status → refresh)
├── contracts/
│   ├── cli-supervise.md      # supervise subcommand flag schema, exit codes, signal contract
│   ├── cli-client.md         # client status + client refresh flag schema, exit codes
│   └── socket-protocol.md    # status-socket verb dispatch (status | refresh) — extends SDD-22
└── tasks.md             # Phase 2 output (/speckit-tasks — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
cmd/hush/                                 # unchanged
└── main.go                               # calls cli.Execute(ctx); no edits

internal/cli/                             # CHUNK SCOPE
├── root.go                               # MOD: register newSuperviseCmd() + newClientCmd()
├── supervise.go                          # NEW: supervise orchestrator subcommand (~350 LOC)
├── supervise_test.go                     # NEW: unit tests for flag wiring, dry-run path, signal handling, orchestration-delegation grep test
├── client.go                             # NEW: client {status, refresh} subcommands (~250 LOC)
├── client_test.go                        # NEW: unit tests + fake-supervisor socket round-trip
├── supervise_integration_test.go         # NEW (build tag `integration`): full dry-run + DiscordStub round-trip
└── exit_codes.go                         # MOD: add sentinels for socket-unreachable, supervisor-refused-refresh, dup-supervisor (each mapped via mapErr)

internal/supervise/                       # MINIMAL EXTENSION SCOPE
├── socket.go                             # MOD: parse first line as verb; dispatch status|refresh; refresh routes to attachRefreshHandler callback (~30 LOC delta)
├── socket_darwin.go                      # MOD: add SocketPathForSupervisor(name) string; add EnumerateSupervisorSockets() ([]string, error) — both pure path-derivation (no syscalls beyond os.Stat for enumeration)
├── socket_linux.go                       # MOD: same two helpers, Linux-specific path scheme
└── socket_test.go                        # MOD: tests for verb dispatch + refresh-error propagation; tests for the two new path-derivation helpers
```

**Structure Decision**: This chunk extends the existing single-module Go layout
(`cmd/hush` thin shell → `internal/cli` orchestration → `internal/supervise`
domain). Three new files in `internal/cli/`, one modification each to
`internal/cli/root.go` (register subcommands) and `internal/cli/exit_codes.go`
(new sentinels). Minimal extension of `internal/supervise/socket.go` +
platform shims for verb dispatch and path enumeration. **No new packages.**
**No new exported package-level symbols** in `internal/cli` (the cobra command
tree IS the contract — same convention locked at SDD-14/15/16/17).

## Complexity Tracking

> One deviation from "pure orchestration glue, zero supervise-package edits".

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| **Minor extension to `internal/supervise/socket.go`**: parse the first line of every accepted connection as a verb (`status\n` \| `refresh\n`), route `refresh` to a new package-private `refreshHandler func(context.Context) error` callback wired post-construction via `(*StatusServer).attachRefreshHandler(...)`. Existing `status` path is the default branch (unrecognised verb also → status, preserving SDD-22 §2.5 "request payload is advisory" backward-compatibility). Adds ~30 LOC to `socket.go` + ~80 LOC of test coverage. | **FR-023-20 requires the supervisor to handle a `refresh` command sent over the status socket.** The supervisor's existing socket (SDD-22) reads the request line and discards it — every accepted connection currently renders status, regardless of the payload. Without a verb dispatch, `hush client refresh` cannot reach the supervisor. The dispatch logic is single-line state inspection (`switch verb { case "refresh\n": ...; default: renderStatus }`) — it is not state-machine business logic, just request-routing. Coalescing of concurrent refresh requests (FR-023-22a) is implemented inside the orchestrator's `refreshHandler` (a single-flight `sync.Mutex` + `sync.Cond` pair, or `golang.org/x/sync/singleflight` if already in module; **research.md R-7** confirms no new dependency). | **Alternative 1: separate socket per verb.** Would double the FS-perms surface (two sockets per supervisor + two parent-dir checks), violate the "one socket per supervisor" model documented in FR-12 and DAEMONS.md §7, and require a second `net.Listen` + cleanup + `Run` lifecycle in the orchestrator. Rejected as more complex AND less aligned with the documented model. **Alternative 2: HTTP-over-Unix-socket with paths `/status` and `/refresh`.** Would import `net/http` into `internal/supervise`, violating Constitution V's "no HTTP" and SDD-22's locked anti-contract (`NEVER add HTTP`). Rejected. **Alternative 3: skip refresh entirely, rely on the refresh-window scheduler.** Would violate FR-023-20 and SC-023-4 outright. Rejected. |

The Complexity-Tracking row above is the entire deviation surface; every other
behaviour in this chunk is pure orchestration over already-locked
`internal/supervise` symbols.
