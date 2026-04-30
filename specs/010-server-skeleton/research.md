# Phase 0 Research — internal/server

Resolutions for every implementation decision the plan defers from
the spec. Each entry: **Decision**, **Rationale**, **Alternatives
considered**.

---

## R1 — HTTP router

**Decision:** stdlib `net/http.ServeMux` (Go ≥ 1.22 method-aware
patterns).

**Rationale:** Constitution XI mandates stdlib first. `ServeMux`
since Go 1.22 supports `"POST /h/{prefix}/claim"` and path-segment
wildcards (`/h/{prefix}/s/{name}`), which covers every route in
`docs/API.md`. The chassis registers the prefix mount; SDD-12 and
SDD-13 register handlers via a `Mount(method, path, h)` hook that
the chassis exposes once per server instance.

**Alternatives considered:**
- `chi` — well-known, ergonomic. Rejected: third-party dep, adds
  middleware-chain machinery the chassis already owns, violates
  Constitution XI.
- `gorilla/mux` — feature-parity. Rejected: same as above; project
  is now archived.
- `echo` — full framework. Rejected: brings opinions about logging
  and error handling that conflict with `internal/logging` and the
  redaction contract (Constitution X).

---

## R2 — Request body cap (64 KiB)

**Decision:** `http.MaxBytesReader(w, r.Body, 64 << 10)` applied
inside a chassis-owned wrapper installed *between* the IP allow-list
middleware and the panic-recover middleware. Bodies > 64 KiB return
`413 Payload Too Large` before any handler runs.

**Rationale:** `docs/ARCHITECTURE.md` §5.3 caps bodies at 64 KiB.
The cap MUST be enforced before SDD-12/13 handlers see the request
body so a hostile peer cannot exhaust memory or stall the panic-
recover middleware's redaction property. Recover middleware is
independent: it never reads the body at all, so the redaction
property holds at every body size up to and beyond the cap.

**Alternatives considered:**
- Enforce in each handler — rejected: chassis-level enforcement is
  the only way the property holds for *every* future handler.
- Enforce at `http.Server.MaxHeaderBytes` — addresses headers, not
  bodies; not equivalent.

---

## R3 — Request ID generation

**Decision:** 16 random bytes from `crypto/rand`, hex-encoded
(32 chars). Stored in the request context under a package-private
context key (typed empty struct, not a string). Exposed via
`server.RequestID(ctx) string` (consumer-side accessor).

**Rationale:** Constitution III requires `crypto/rand` for any
identifier we depend on for security correlation. The constitution's
threat model treats client-supplied headers as untrusted
(`docs/ARCHITECTURE.md` §2), so the chassis ignores any incoming
header named `X-Request-ID` or similar — FR-017. A 128-bit ID gives
collision-free correlation across the entire process lifetime.

**Alternatives considered:**
- UUIDv4 (`github.com/google/uuid`) — rejected: third-party dep for
  what is `crypto/rand` + `hex.EncodeToString`.
- `math/rand` — rejected: Constitution III ("`crypto/rand` for
  entropy — never `math/rand`"). Even though request-ID collision
  isn't exploitable per se, the constitution rule is unconditional.
- Trust client header — rejected: FR-017 is an absolute prohibition.

---

## R4 — NTP / clock-sync probe

**Decision:** Inject the probe as `Deps.ClockSyncProbe func(ctx
context.Context) (synced bool, drift time.Duration, err error)`. The
default at production wiring time:
- darwin: exec `systemsetup -getusingnetworktime` and parse the
  yes/no response; drift derived from `sntp -t 5 time.apple.com`
  parse (with bounded timeout).
- linux: exec `timedatectl show` and parse `NTPSynchronized=yes`,
  with `timedatectl show --property=NTPSynchronized,TimeUSec` for
  drift estimation.

**Rationale:** `docs/SPEC.md` FR-17 specifies these exact commands.
Injection via `Deps` lets tests substitute a deterministic probe so
unit tests never shell out to real binaries (Constitution VIII).
The clock-sync sentinel `ErrClockUnsynchronised` is returned when:
the probe reports `synced=false`, the probe reports
`|drift| > cfg.Security.MaxClockDrift` (default 60 s), or the probe
returns a non-nil error within its bounded timeout.

**Alternatives considered:**
- `chrony` IPC — rejected: not present on macOS; SPEC pins the two
  documented commands.
- Skip the check and trust config — rejected: AC-8 is non-negotiable.
- Hardcode the binary call inside the chassis — rejected: untestable
  without exec-isolation; injection is the only clean test surface.

---

## R5 — Tailscale CGNAT verification at startup

**Decision:** At startup, the chassis enumerates
`net.InterfaceAddrs()` and asserts:
1. `Cfg.Server.ListenAddr.Addr()` is inside `100.64.0.0/10`
   (Tailscale CGNAT range — already validated at TOML time by
   `config.validateTailscaleAddrPort`).
2. The same address belongs to at least one of the local
   interfaces' addresses — meaning the chassis can actually bind
   to it on this host.

Failure produces `ErrBindNotOnTailscale`, never opens a socket.

**Rationale:** Constitution VI ("vault server MUST bind to the
Tailscale interface only") and `docs/SPEC.md` FR-8. The TOML-time
validation in `internal/config` is necessary but not sufficient: a
host may parse fine and still not have the configured IP bound to
any interface (machine moved between Tailscale identities, ACL
flapped, interface renamed). The chassis re-checks at every launch.

**Alternatives considered:**
- Trust config alone — rejected: not enough for a property this
  important. The chassis is the only place the actual host state
  meets the configured intent.
- Use Tailscale's `tailscale.com/client/local` library — rejected:
  third-party crypto-adjacent dep that pulls a large transitive
  surface. The CGNAT range check + interface-table check is
  sufficient and uses stdlib only.

---

## R6 — File-mode probe

**Decision:** `filepath.WalkDir(Cfg.Server.StateDir, ...)`; for
each `os.DirEntry`:
- regular files with `Mode().Perm() > 0600` → fail.
- directories with `Mode().Perm() > 0700` → fail.
- symlinks: not followed (WalkDir default); a symlink whose
  *containing directory* lives in the state dir is reported as a
  laxer-mode failure if its mode is loose.
- offending path is reported as a category ("regular file" vs
  "directory") in the error wrapping the sentinel; the file's
  *contents* are never read or logged.

**Rationale:** FR-005. The constitution-level Security Requirements
table mandates "Vault: 0600. Configs: 0640. Dirs: 0750." — but the
chassis's own state dir is held to the stricter 0600/0700
documented in this spec's User Story 1. The check fails fast on
the first offence and short-circuits the sequence.

**Alternatives considered:**
- `os.Stat` of just the vault file — rejected: FR-005 demands the
  *whole state directory* tree. A leaked config file at 0644 is
  still a leak.
- Recursive Stat — rejected: `WalkDir` is purpose-built and
  produces fewer syscalls.

---

## R7 — State-dir presence and ownership probe

**Decision:** `os.Stat(Cfg.Server.StateDir)` then assert:
1. `info.IsDir()`.
2. `info.Sys().(*syscall.Stat_t).Uid == uint32(os.Getuid())`.
3. The directory is not a symlink to a different filesystem (we
   use `Lstat` once at the top to refuse symlinks at the state-dir
   root — a symlink could be moved by an attacker between the
   Lstat and the WalkDir).

Failure produces `ErrStateDirUnsafe`.

**Rationale:** FR-007. Ownership check is platform-specific
(`syscall.Stat_t.Uid` is unix-only), but `darwin` and `linux` are
the only supported targets — this is acceptable.

**Alternatives considered:**
- Skip ownership check — rejected: `~/.hush/` owned by another user
  is a serious posture failure that the chassis must catch loudly.
- Delegate to config validation — rejected: `internal/config`
  already does a presence check (`ErrStateDirNotFound`,
  `ErrStateDirUnsafe`), but it does not re-validate at runtime.
  The chassis re-runs the check because conditions can change
  between config load and `Run`.

---

## R8 — SIGHUP signalling and reload serialisation

**Decision:** Inside `Run(ctx)`:

```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, syscall.SIGHUP)
defer signal.Stop(sigCh)

// reload coordinator goroutine
go func() {
    defer recoverAndLog(logger, "sighup-loop")
    for {
        select {
        case <-ctx.Done():
            return
        case <-sigCh:
            if shuttingDown.Load() { continue } // FR-015
            s.runReload(ctx) // serialised under reloadMu
        }
    }
}()
```

`runReload` takes a per-server `sync.Mutex` so two SIGHUPs can never
race; `ReloadVault` (the public entry, used by tests and by direct
callers in SDD-14 if needed) goes through the same coordinator.

**Rationale:** Constitution IX requires every goroutine to have a
clear owner, an explicit cancellation path, and a documented
termination condition. The signal-loop goroutine terminates when
`ctx` cancels; `defer signal.Stop(sigCh)` ensures the kernel stops
delivering signals to the closed channel.

**Alternatives considered:**
- `signal.NotifyContext` — rejected: it cancels the context on
  signal, which is the wrong semantics for SIGHUP (we want to
  reload, not cancel).
- One goroutine per signal — rejected: the only signal we care
  about is SIGHUP; cancellation comes from the parent context.

---

## R9 — Drain implementation

**Decision:** After atomic swap:

```go
oldStore := vaultPtr.Swap(newStore) // returns previous *vault.Store
s.drainWG.Add(1)
go func() {
    defer s.drainWG.Done()
    defer recoverAndLog(logger, "drain")
    select {
    case <-time.After(cfg.ReloadDrainWindow):
    case <-shutdownDeadline: // see R11
    }
    if err := oldStore.Destroy(); err != nil {
        logger.ErrorContext(ctx, "vault destroy after drain", "err", err)
    }
}()
```

`s.drainWG` is a `sync.WaitGroup` that `Run`'s shutdown path waits
on before returning, so no protected vault memory is leaked across
process exit (FR-025).

**Rationale:** The drain window is the contract that lets in-flight
requests holding `oldStore` finish. `vault.Store.Destroy` is
already idempotent (`internal/vault/store.go:84`); the chassis
calls it exactly once per swap. Concurrent draining is fine — each
swap creates an independent drain goroutine, but reloads
themselves are serialised so two concurrent drains never share the
same `oldStore`.

**Alternatives considered:**
- Drain via reference counting — rejected: requires every handler
  to acquire/release a token, which is a lot of new API surface
  and a footgun (a handler that forgets to release leaks a vault
  forever). The drain-window contract trades a bounded lingering
  for code simplicity.
- No drain (destroy immediately after swap) — rejected: an
  in-flight request holding `oldStore` would see use-after-destroy
  errors. AC-2 demands no in-flight failures.

---

## R10 — Reload error categories

**Decision:** Sentinel errors declared in `errors.go`:

```go
var (
    ErrReloadFileMissing  = errors.New("server: reload: vault file missing")
    ErrReloadDecryptFailed = errors.New("server: reload: vault decrypt failed")
    ErrReloadInvalid      = errors.New("server: reload: vault invalid")
    ErrReloadInProgress   = errors.New("server: reload: another reload is in progress")
    ErrShuttingDown       = errors.New("server: reload: server is shutting down")
)
```

Reload errors wrap these sentinels via `fmt.Errorf("...: %w", ...)`
and never embed any byte from the vault file or its plaintext
(FR-013). `ReloadVault`'s caller can `errors.Is(err, ErrReload...)`
to distinguish categories without parsing strings.

**Rationale:** Constitution IX: "wrap with `%w`, compare with
`errors.Is`". FR-013: no vault byte in any reload error.

**Alternatives considered:**
- Single composite `ErrReloadFailed` — rejected: violates IX's
  "declare sentinel errors" rule and gives callers no way to
  distinguish "file missing" from "decrypt failed" without
  string parsing.

---

## R11 — Graceful shutdown order

**Decision:** When `ctx` cancels, `Run`:

1. Sets `shuttingDown.Store(true)` so the SIGHUP loop ignores
   future signals (FR-015).
2. Calls `httpServer.Shutdown(shutdownCtx)` with a deadline of
   `cfg.ShutdownTimeout` (default 30 s) — this stops accepting
   new connections immediately and waits for in-flight requests
   up to the deadline.
3. Waits on `s.drainWG.Wait()` so any active drain finishes its
   `Destroy` (FR-025). The drain goroutines respect the
   shutdown deadline via the `shutdownDeadline` channel in R9.
4. Calls `signal.Stop(sigCh)`; closes the SIGHUP signal channel.
5. Returns nil (or the first non-`http.ErrServerClosed` error).

**Rationale:** FR-024 + FR-025. The chassis does not store `ctx`
in a struct field — `shutdownCtx` is derived from `ctx` and
captured by the closure that calls `Shutdown`.

**Alternatives considered:**
- Skip `drainWG.Wait` — rejected: would leak vault memory across
  the exit (FR-025).
- Skip `Shutdown` and just `Close` — rejected: forces drops on
  in-flight requests, breaks AC-1's "serves over Tailscale within
  5 seconds" implicit contract that requests don't get cut off
  arbitrarily.

---

## R12 — Recover-middleware second-level panic

**Decision:** The recover middleware itself is wrapped in a
deferred `recover()`:

```go
func RecoverMiddleware(logger *slog.Logger) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            defer func() {
                if r := recover(); r != nil {
                    defer func() {
                        if r2 := recover(); r2 != nil {
                            // second-level panic — log minimal, fail closed for this request
                            logger.ErrorContext(r.Context(), "recover middleware panicked",
                                "request_id", server.RequestID(r.Context()))
                            // single 500 already on the wire from outer recover or default; do nothing
                        }
                    }()
                    logger.ErrorContext(r.Context(), "handler panic",
                        "panic", fmt.Sprintf("%v", r),
                        "stack", string(debug.Stack()),
                        "request_id", server.RequestID(r.Context()))
                    http.Error(w, "internal server error", http.StatusInternalServerError)
                }
            }()
            next.ServeHTTP(w, r)
        })
    }
}
```

The second-level recover is deliberately minimal — it logs the
request ID only (no panic value, no stack — those are the things
that just panicked) and lets `http.Error` finish; if `Error`
itself panics, the goroutine dies but the server stays up because
`http.Server` per-connection isolation handles that.

**Rationale:** FR-019 edge case 4: "the recover layer is not a
single point of failure". Defense in depth at the chassis layer.

**Alternatives considered:**
- Trust outer `http.Server`'s default recover — rejected: the
  default recover does not log structured fields and does not
  carry our request ID.

---

## R13 — Sentinel-marker leak test (recover middleware)

**Decision:** `TestMiddleware_RecoverLogsStackNoBody` mounts a
panicking handler, sends a request with body
`SECRET_SHOULD_NEVER_APPEAR_10_<random32hex>`, captures the
`*slog.Logger`'s output (using a `bytes.Buffer`-backed
`slog.NewJSONHandler`), asserts:

- The log entry contains `"panic":` and `"stack":` fields.
- The log entry contains the request ID.
- The log entry **does not** contain the sentinel marker (full
  string match) **and** does not contain the random suffix
  (independently — guards against accidental partial inclusion).
- The HTTP response body is `"internal server error\n"` (or the
  default `http.Error` body) — no panic detail, no stack, no
  sentinel.

**Rationale:** SC-006. Marker uniqueness across runs (the random
suffix) catches a regression where a developer accidentally logs
`r.URL.RawQuery` or similar request-bound metadata.

**Alternatives considered:**
- Log inspection with regex — rejected: substring match is the
  property the constitution actually demands.

---

## R14 — Approver interface placeholder

**Decision:** `Approver` declared in `internal/server/approver.go`
as a single-method interface with a value-typed `ApprovalRequest`
and a value-typed `Decision`. SDD-11's `BotApprover` will satisfy
the interface. SDD-12 (claim handler) will be the first consumer.
Tests in this chunk supply a `fakeApprover` (file
`approver_fake_test.go` or inline in test files) that returns
scripted decisions.

**Rationale:** Constitution IX: "accept interfaces, return concrete
types. Define interfaces at the consumer." The chassis is the
consumer of approval (it embeds the interface in `Deps`); SDD-11
is the producer. Single-method interfaces are explicitly preferred.

**Alternatives considered:**
- Declare in `internal/discord` — rejected: violates the
  consumer-side rule and creates a dep-cycle risk
  (`internal/server` would import `internal/discord` and vice
  versa for testing fakes).
- Multi-method interface — rejected: violates the constitution and
  expands the surface for future approval channels.

---

## R15 — Coverage strategy

**Decision:** Target 95 %. Path coverage matrix:

| Code path                                  | Covered by |
|--------------------------------------------|------------|
| `New` happy path / each missing-dep error  | `server_test.go` (table-driven) |
| `Run` startup-check failures (each of 4)   | `startup_checks_test.go` (with fake probes) + `integration_test.go` for full-stack |
| `Run` graceful shutdown                    | `integration_test.go` |
| `Run` SIGHUP loop / shutdown precedence    | `reload_test.go` |
| `ReloadVault` happy path                   | `reload_test.go` + `integration_test.go` (TestSIGHUP_AtomicReload) |
| `ReloadVault` each error category          | `reload_test.go` (5 sentinels × 1 case each) |
| Drain goroutine: timer expires / shutdown beats timer | `reload_test.go` (with injected `Clock`) |
| Atomic-swap race                           | `reload_test.go` `TestVaultPointerSwap_NoRace` (`go test -race`) |
| Request ID middleware                      | `middleware_test.go` (forged-header + uniqueness across N≥100) |
| IP allow-list middleware                   | `middleware_test.go` (allow + deny + 403 status + handler-not-invoked probe) |
| Recover middleware                         | `middleware_test.go` (sentinel-leak + second-level panic) |
| Body cap (64 KiB)                          | `middleware_test.go` (413 at 65 KiB) |
| Router prefix mount                        | `router_test.go` |

**Rationale:** Constitution VIII test priority: "Server handlers,
supervisor state machine, validators" → 95 %. The chassis sits
in the same tier.

---

## All NEEDS CLARIFICATION resolved

The Phase 0 outline has no remaining unknowns. Phase 1 design
proceeds.
