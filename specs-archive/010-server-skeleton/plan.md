# Implementation Plan: internal/server — HTTP server skeleton, ordered startup checks, and SIGHUP atomic vault reload

**Branch**: `010-server-skeleton` | **Date**: 2026-04-30 | **Spec**: [spec.md](spec.md)
**Input**: Feature specification from `/specs/010-server-skeleton/spec.md`

## Summary

Deliver `internal/server`: the HTTP chassis on which SDD-12 (claim) and
SDD-13 (secret/revoke/health) attach handlers and SDD-14 (`cmd/hush serve`)
runs as the lifecycle owner. The chassis owns four observable properties:

1. **Refuse-to-start on a misconfigured host.** Four ordered startup
   checks — `clock_sync → file_modes → tailscale_bind → state_dir` — run
   before any socket is bound. Each failure returns a typed sentinel
   error and exits non-zero.
2. **Atomic SIGHUP vault reload with a drain window.** A reload swaps
   `*atomic.Pointer[vault.Store]` and `Destroy`s the old store after
   `cfg.ReloadDrainWindow` (default 30 s). In-flight requests that
   captured the old pointer complete safely; reloads are serialised.
3. **Middleware chain that never leaks request bodies.** Order is
   request ID → IP allow-list → panic recover → handler. Recover logs
   `panic value + debug.Stack() + request_id` at ERROR; the request
   body is never part of the log entry. Allow-list rejects with
   `403 Forbidden` before any handler runs. The chassis assigns the
   request ID itself; client-supplied headers are ignored.
4. **Graceful shutdown.** `Run(ctx)` blocks until the context cancels,
   then triggers `http.Server.Shutdown` with `cfg.ShutdownTimeout`
   (default 30 s) and waits for any pending reload's drain to finish
   so vault memory is not leaked across the process exit.

The package also declares the `Approver` interface (with
`ApprovalRequest` / `Decision` value types) — SDD-11's Discord-backed
implementation will satisfy it without modifying the chassis.

**Net new dependencies:** none. The package is built on `net/http`,
`log/slog`, `crypto/rand`, `runtime/debug`, `os/signal`, `syscall`,
`sync/atomic`, and the existing `internal/{config,vault,token,
logging,vault/securebytes}` packages. Constitution XI: stdlib first.

## Technical Context

**Language/Version**: Go (toolchain pinned in `go.mod`; floor — do not use newer language features)
**Primary Dependencies**: stdlib only — `net/http`, `log/slog`, `sync/atomic`, `os/signal`, `syscall`, `runtime/debug`, `crypto/rand`. Internal: `internal/config`, `internal/vault`, `internal/vault/securebytes`, `internal/token`, `internal/logging`. No third-party HTTP router (no chi, gorilla/mux, echo) per Constitution XI.
**Storage**: N/A — chassis holds no persistent state; vault file is consumed via `internal/vault.Load` during `ReloadVault`.
**Testing**: `go test -race`, table-driven unit tests, `//go:build integration` integration tests, coverage via `go test -cover`. Naming per `.github/tech-conventions/testing-standards.md`.
**Target Platform**: darwin/linux, amd64/arm64 (CGO disabled — pure Go).
**Project Type**: Go library package under `internal/` (no public Go API surface; `cmd/hush` is the only consumer-binary boundary).
**Performance Goals**: ≥ 100 concurrent secret fetches without degradation (NFR-6). Cold start < 500 ms excluding KDF (NFR-4) — chassis must not block on I/O outside the lifecycle entry point (FR-027).
**Constraints**: 64 KiB request-body cap (`docs/ARCHITECTURE.md` §5.3) enforced via `http.MaxBytesReader` before any handler runs. NTP drift cap 60 s (`docs/SPEC.md` FR-17). Drain window default 30 s; shutdown timeout default 30 s. No request body in panic logs ever. Race detector must run clean.
**Scale/Scope**: One chassis per `hush serve` process; lifetime = process lifetime; reloads serialised; no clustering, no multi-tenant.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-checked after Phase 1 design.*

| Principle | Applies? | How this plan honours it |
|-----------|----------|---------------------------|
| **III. Defense in Depth Through Crypto Layering** | Yes | Chassis is the mounting surface for every downstream layer (signature verify, IP allow-list, JWT validate, ECIES encrypt). The chassis itself adds the IP allow-list as an *additional* application-layer perimeter on top of Tailscale ACL (independent layer). The chassis never weakens any layer; reloads consume only `internal/vault`'s already-validated `Store`. |
| **VI. Tailscale-Only, Never Public** | Yes | `tailscale_bind` startup check refuses any listen address that is not in Tailscale CGNAT (`100.64.0.0/10`). `0.0.0.0`, empty host, loopback, and non-Tailscale public addresses are explicit failures with the named sentinel. The check re-runs at every launch — `config.Validate` is TOML-time only; the chassis re-verifies the host interface table at start-time. |
| **VIII. Testing Discipline** | Yes | Coverage target 95 % (matches the chunk contract and the "High" tier of the constitution's test priority — vault chassis sits between Critical and High in scope). Every named test from SDD-10 (`TestStartupChecks_*`, `TestMiddleware_*`, `TestVaultPointerSwap_NoRace`, integration `TestSIGHUP_AtomicReload`) is mapped to a concrete file in §Project Structure. Race-detector run is mandatory. AC→test mapping: AC-1 (chassis Run), AC-2 (SIGHUP reload), AC-8 (refuse-to-start). |
| **IX. Idiomatic Go Discipline** | Yes | No `init()`. No mutable package-level globals (sentinel errors are `var Err... = errors.New(...)` exported package-level constants — the constitution's idiomatic exception). `context.Context` is never stored in a struct field; it flows through `Run(ctx)` and the request handler context. Errors wrap with `%w`; sentinels compared via `errors.Is`. `Approver` is a single-method interface accepted by the chassis (consumer side); the chassis does not return interfaces. Every spawned goroutine (drain, signal listener, reload coordinator) `recover()`s at its top frame and has a documented termination path. |
| **X. Observability & Redaction** | Yes | All logging via `*slog.Logger` injected through `Deps`. Recover middleware emits a structured log containing the panic value, `debug.Stack()`, and `request_id` — and **no** part of the request body, decoded or otherwise (FR-019, FR-020; SC-006 sentinel-leak test). Operational log carries IP-allow-list rejections; the audit log is consumed via the `AuditWriter` consumer-side interface and remains the source of truth for security events. No `string`-typed secret material crosses the chassis API. |
| **XI. Native-First, Minimal Dependencies, Ephemeral Vault** | Yes | Router is `net/http.ServeMux`. Zero new third-party dependencies. `ReloadVault` consumes `internal/vault.Load` directly; no separate vault decoder. The chassis treats the vault file as ephemeral — on reload failure the previous in-memory `Store` continues to serve, on shutdown the `Destroy` of the active store happens during drain so no plaintext lingers across exit (FR-025). |

**Result:** PASS. No violations. Complexity Tracking is empty.

The chunk contract's anti-contracts ("never bind to `0.0.0.0`", "no
`init()`", "no `Context` in struct field") are enforced in the test
plan and in the package layout — not just in implementation.

## Project Structure

### Documentation (this feature)

```text
specs/010-server-skeleton/
├── plan.md              # this file
├── spec.md              # already authored
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/
│   ├── api-routes.md    # /h/<prefix>/... mount surface (consumed by SDD-12, SDD-13)
│   ├── approver.md      # Approver interface, ApprovalRequest, Decision
│   ├── reload.md        # SIGHUP atomic-swap + drain protocol
│   └── startup-checks.md# Ordered checks, sentinel error names, exit semantics
└── tasks.md             # Phase 2 output (NOT created here — /speckit-tasks)
```

### Source Code (repository root)

```text
internal/server/
├── server.go              # Server struct, New(deps Deps) (*Server, error), Run(ctx) error, Deps
├── router.go              # http.ServeMux setup, /h/<prefix>/... mount, route registration hooks
├── middleware.go          # request ID → IP allow-list → panic recover; body cap via http.MaxBytesReader
├── startup_checks.go      # ordered checks: clock_sync → file_modes → tailscale_bind → state_dir; sentinel errors
├── reload.go              # SIGHUP handler, ReloadVault(ctx, newPath, key), serialised swaps + drain goroutine
├── approver.go            # Approver interface, ApprovalRequest, Decision (consumed by SDD-12, implemented by SDD-11)
├── errors.go              # Exported sentinel error vars (one per startup check + reload categories)
├── doc.go                 # package overview
├── server_test.go         # New() validation; Run(ctx) shutdown; lifecycle contract
├── router_test.go         # mount path, prefix handling
├── middleware_test.go     # TestMiddleware_RequestIDStable, TestMiddleware_IPAllowListBlocks, TestMiddleware_RecoverLogsStackNoBody (sentinel-leak), recover-in-recover edge case
├── startup_checks_test.go # TestStartupChecks_RefusesPublicBind, TestStartupChecks_RefusesLooseFileMode, TestStartupChecks_RefusesUnsyncedClock, TestStartupChecks_RefusesUnsafeStateDir, TestStartupChecks_OrderedExecution
├── reload_test.go         # serialised reload, failed reload leaves pointer unchanged, drain timer correctness, TestVaultPointerSwap_NoRace (-race)
└── integration_test.go    # //go:build integration — TestSIGHUP_AtomicReload (vault A → SIGHUP → in-flight sees A, new sees B, A zeroed at drain), TestRun_GracefulShutdown_DrainsInflight, TestStartupChecks_FullStackRefuse
```

**Structure Decision**: single Go package under `internal/`; no
sub-packages. Five non-test source files mirror the chunk contract;
two additional files (`approver.go`, `errors.go`, `doc.go`) keep the
public surface readable without inflating the file count beyond the
SDD-10 directive. All ten test files (including integration) live in
the same directory per Go test layout. Tests that need to advance
time use the injected `Clock func() time.Time` from `Deps`; tests
that need to fake the host clock-sync probe inject a
`ClockSyncProbe` function (see contracts/startup-checks.md).

## Locked Exported API (this chunk)

This is the API the chassis hands to its consumers. Once SDD-10 is
merged, this surface MUST NOT change without a follow-up SDD chunk.
SDD-12 and SDD-13 attach handlers via the route-registration hook
documented in `contracts/api-routes.md`.

```go
package server

import (
    "context"
    "log/slog"
    "net/http"
    "sync/atomic"
    "time"

    "github.com/mrz1836/hush/internal/config"
    "github.com/mrz1836/hush/internal/token"
    "github.com/mrz1836/hush/internal/vault"
    "github.com/mrz1836/hush/internal/vault/securebytes"
)

// Server is the chassis. Constructed once via New, run once via Run,
// shut down once via the cancellation of Run's context.
type Server struct { /* unexported */ }

// Deps is the dependency-injection bundle for the chassis.
type Deps struct {
    Cfg          *config.Server                  // required; validated TOML server config
    VaultPtr     *atomic.Pointer[vault.Store]    // required; chassis swaps via ReloadVault
    TokenStore   token.Store                     // required; passed to handlers via context
    Approver     Approver                        // required; SDD-11 supplies BotApprover
    Logger       *slog.Logger                    // required
    AuditWriter  AuditWriter                     // required; consumer-side interface (see below)
    Clock        func() time.Time                // optional; defaults to time.Now
    ClockSyncProbe func(context.Context) (synced bool, drift time.Duration, err error) // optional; defaults to host probe
}

// AuditWriter is the consumer-side interface the chassis uses to emit
// security-relevant events. The audit writer's concrete implementation
// lives in internal/discord/audit.go (per docs/PACKAGE-MAP.md).
type AuditWriter interface {
    Write(ctx context.Context, event AuditEvent) error
}

// Approver is the consumer-side interface SDD-12 calls to seek the
// operator's approval. SDD-11 supplies the Discord-backed implementation.
type Approver interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// ApprovalRequest, Decision, AuditEvent — see contracts/approver.md.

func New(deps Deps) (*Server, error)
func (s *Server) Run(ctx context.Context) error
func (s *Server) ReloadVault(ctx context.Context, newPath string, key *securebytes.SecureBytes) error
```

**Observable invariants of the locked API:**

- `New` returns a typed error (never panics) when any required
  dependency is nil (`ErrMissingConfig`, `ErrMissingVaultPtr`,
  `ErrMissingTokenStore`, `ErrMissingApprover`, `ErrMissingLogger`,
  `ErrMissingAuditWriter`).
- `New` performs zero I/O — no socket bind, no file read, no NTP
  query, no signal registration (FR-027). Those happen inside `Run`.
- `Run(ctx)` returns nil on graceful shutdown; returns the first
  startup-check sentinel on a refuse-to-start; returns
  `http.ErrServerClosed`-equivalent only when the underlying listener
  is closed by Shutdown.
- `ReloadVault` is idempotent and serialised. Concurrent calls are
  queued; an in-flight reload's drain MUST complete before the next
  reload's swap.
- `Run` does NOT store `ctx` in a struct field. The cancellation path
  is the closure captured by the inner goroutine that calls
  `http.Server.Shutdown` (Constitution IX).

## Phase 0 — Research

Resolved decisions — full rationale in [research.md](research.md):

1. **Router**: `net/http.ServeMux` (Go ≥ 1.22 method-aware mux).
   Constitution XI; sufficient for `/h/<prefix>/...` route shape.
2. **Body cap**: `http.MaxBytesReader(w, r.Body, 64 << 10)` applied
   *inside* the chassis-mounted handler wrapper, before SDD-12/13
   handlers see the body. The recover-middleware redaction property
   is independent — it never reads the body at all.
3. **Request-ID generation**: 16 random bytes from `crypto/rand`,
   hex-encoded. Stored in request context via a private
   context-key type. Never sourced from headers. Mirrors the
   constitution's "no client-supplied IDs" stance.
4. **NTP probe**: shell out to `systemsetup -getusingnetworktime`
   (darwin) or `timedatectl show` (linux), parsed via a
   per-platform helper file (build-tag-gated). The probe is injected
   via `Deps.ClockSyncProbe` so tests do not exec the real binary.
5. **Tailscale CGNAT verification**: at startup, enumerate
   `net.InterfaceAddrs` and assert the configured `ListenAddr.IP()`
   resolves to a `100.64.0.0/10` address that belongs to a local
   interface — re-verifying what `config.validateTailscaleAddrPort`
   already checked at TOML time, because the host interface state
   can change between TOML parse and process start.
6. **File-mode probe**: `filepath.WalkDir` over `Cfg.Server.StateDir`;
   any regular file with mode > 0600 or any directory with mode >
   0700 fails the check with the offending path category in the
   error (never the file's contents, per FR-005).
7. **State-dir check**: `os.Stat` + `syscall.Stat_t` for ownership
   (`Sys().(*syscall.Stat_t).Uid` against `os.Getuid()`); regular
   directory required.
8. **SIGHUP signalling**: `signal.NotifyContext`-style pattern with
   an explicit `make(chan os.Signal, 1)` buffered channel and a
   single goroutine fan-out to a serialised reload coordinator (a
   `chan struct{}` plus a `sync.Mutex` guarding the drain timer).
9. **Drain implementation**: after `atomic.Pointer.Store(newStore)`,
   a goroutine `time.Sleep(cfg.ReloadDrainWindow)` then calls
   `oldStore.Destroy()`; the goroutine recovers from any panic in
   `Destroy` and logs it. A `sync.WaitGroup` tracks the active
   drain so shutdown can wait for it (FR-025).
10. **Recover-middleware second-level panic**: defer-recover wraps
    the recover handler itself; on second-level panic, log and
    return `500` for that single request (FR-019 edge case 4 in
    spec.md).

No NEEDS CLARIFICATION remain.

## Phase 1 — Design & Contracts

Artifacts authored in this phase:

- **[data-model.md](data-model.md)**: entities — `Server`, `Deps`,
  `Approver`, `ApprovalRequest`, `Decision`, `AuditEvent`,
  `AuditWriter`, `StartupCheck`, sentinel errors, `Reload`
  coordinator state machine, `Drain` ledger, `RequestID` context key.
- **[contracts/api-routes.md](contracts/api-routes.md)**: route
  surface mounted under `/h/<prefix>/...` for SDD-12 (claim) and
  SDD-13 (s/{name}, revoke/{jti}, hz). The chassis registers the
  prefix mount; SDD-12/13 register their handlers via the
  registration hook (`func (s *Server) Mount(method, path string,
  h http.Handler)`).
- **[contracts/approver.md](contracts/approver.md)**: `Approver`,
  `ApprovalRequest`, `Decision` shape (no Discord-specific fields;
  SDD-11 will subclass the request via composition or extend a
  metadata map).
- **[contracts/reload.md](contracts/reload.md)**: SIGHUP atomic-swap
  state machine — Idle → Loading → Swapped → Draining → Idle —
  with serialisation and shutdown-precedence rules.
- **[contracts/startup-checks.md](contracts/startup-checks.md)**:
  the four ordered checks, their sentinel error names, the
  short-circuit rule, and the dependency-injection points
  (`Deps.ClockSyncProbe`).
- **[quickstart.md](quickstart.md)**: how `cmd/hush serve`
  (SDD-14) wires the chassis — sample 30-line wiring snippet,
  signal handling, exit-code mapping.

**Agent context update**: `CLAUDE.md` carries an active plan
reference between `<!-- SPECKIT START -->` / `<!-- SPECKIT END -->`
markers. Updated at end of Phase 1 to point to this plan file.

**Constitution Re-check (post-design)**: PASS — the contracts files
do not introduce any new dependency, no `init()`, no global mutable
state, no client-supplied request IDs, no body bytes in any log
sink. The route-mount hook keeps the chassis dependency-direction
correct: SDD-12/13 import `internal/server`; nothing in
`internal/server` imports SDD-12/13.

## Complexity Tracking

> No constitution violations — section intentionally empty.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| _none_    | _n/a_      | _n/a_                               |
