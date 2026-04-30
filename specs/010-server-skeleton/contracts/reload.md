# Contract — SIGHUP Atomic Reload

The chassis owns the SIGHUP-driven vault reload. This contract
locks the protocol.

---

## Trigger

A reload is triggered by either:

1. **External**: the `hush` process receives `SIGHUP` (e.g. from
   the operator running `hush secret rotate` on the vault host —
   see `docs/SPEC.md` FR-10).
2. **Internal**: a direct call to `(*Server).ReloadVault(ctx,
   newPath, key)`. Used by tests and (potentially) by SDD-14 or
   SDD-15 if they want programmatic rotation.

Both paths converge on the same `runReload` coordinator. There is
no other entry into reload semantics.

---

## State machine

```
              ┌───────────┐
              │   Idle    │◄──────────────────────────┐
              └─────┬─────┘                           │
        SIGHUP / ReloadVault                          │
                    ▼                                 │
              ┌───────────┐                           │
              │ Loading   │ ─── load fails ──────────►│ (error returned)
              └─────┬─────┘                           │
              load ok                                 │
                    ▼                                 │
              ┌───────────┐ atomic.Pointer[vault.Store].Swap
              │  Swapped  │
              └─────┬─────┘
                    ▼
              ┌───────────┐  drain timer expires OR shutdown deadline
              │ Draining  │
              └─────┬─────┘
              destroy old store
                    ▼
                  Idle
```

---

## Serialisation rule (FR-014)

`runReload` acquires `s.reloadMu` (a `sync.Mutex`) at entry and
holds it through `Loading → Swapped → Draining` — the mutex is
released only after `oldStore.Destroy()` returns. This satisfies
FR-014 literally: "the new reload does not begin until the
previous reload's swap and destroy have completed."

A SIGHUP that arrives while `reloadMu` is held queues on the
mutex. (The signal channel is buffered at 1; an additional SIGHUP
beyond that is coalesced — the OS does not enqueue duplicate
SIGHUPs to the same process.)

---

## Failure handling (FR-012, FR-013)

A reload that fails — file missing, decrypt failed, structurally
invalid — leaves the active vault pointer unchanged and returns a
typed error wrapping one of:

```go
ErrReloadFileMissing
ErrReloadDecryptFailed
ErrReloadInvalid
```

Each error is wrapped via `fmt.Errorf("...: %w", sentinel)` and
includes the failing path (FR-013) but **never** any byte from
the vault file's ciphertext or plaintext. The caller can match
the category via `errors.Is`.

A failed reload does not call `oldStore.Destroy()`. The previous
vault continues to serve every request as if SIGHUP had never
fired.

---

## Drain window

`s.cfg.ReloadDrainWindow` (default 30 s, configurable via
`[server].reload_drain_window`) is the time between
`vaultPtr.Swap` and `oldStore.Destroy`.

The drain runs *under the reload mutex* (see "Serialisation rule"
above) so the next reload cannot begin while the previous drain
is in flight.

The drain goroutine wakes on whichever fires first:

1. The 30 s timer (`time.After(cfg.ReloadDrainWindow)`).
2. The shutdown deadline channel — a chassis-internal channel
   closed during `Run`'s shutdown sequence so a still-active drain
   races to completion before `Run` returns (FR-025).

After either wake, `oldStore.Destroy()` runs. `Destroy` is
idempotent (`internal/vault/store.go:84`); the chassis still calls
it exactly once per swap.

---

## Shutdown precedence (FR-015, FR-025)

When `Run`'s context cancels:

1. `s.shuttingDown.Store(true)` — the SIGHUP signal-loop
   goroutine sees this and discards any further SIGHUP.
2. `httpServer.Shutdown(shutdownCtx)` runs with
   `cfg.ShutdownTimeout`.
3. `s.drainWG.Wait()` blocks until any pending drain finishes
   (FR-025) — the shutdown deadline channel is closed first so
   the drain goroutine wakes early and runs `Destroy`.
4. `Run` returns nil.

If a SIGHUP arrives between (1) and (3), it is dropped — `runReload`
checks `s.shuttingDown.Load()` after acquiring the mutex and
returns `ErrShuttingDown`.

---

## Race-freedom (SC-010)

`atomic.Pointer[vault.Store].Swap` is the only mutation point on
the live vault pointer. Handlers read via
`s.vaultPtr.Load()` and operate on a `vault.Store` interface
value that is immutable for the duration of the request. The
race detector run (`go test -race ./internal/server/...`) MUST
pass on the SIGHUP integration test (`TestVaultPointerSwap_NoRace`,
`TestSIGHUP_AtomicReload`).

---

## Audit emission

After a successful reload, the chassis emits:

```go
s.audit.Write(ctx, AuditEvent{
    Type: AuditVaultReloaded,
    At:   s.clock(),
    Detail: map[string]string{
        "from_path": "<sanitised>",
        "to_path":   newPath,
    },
})
```

The audit event is **not** retried on failure to write — audit-
writer transient errors are logged at WARN; a permanent failure
of the audit writer is a serious posture issue but does not break
the reload itself (the vault has already been swapped).

---

## Public API surface

```go
// ReloadVault loads a new vault from newPath using the supplied
// vault key, atomically swaps the in-memory pointer, and destroys
// the previous store after the configured drain window.
//
// Calls are serialised: a second call blocks until the first
// completes its swap and destroy. Returns a typed sentinel error
// (ErrReloadFileMissing, ErrReloadDecryptFailed, ErrReloadInvalid)
// on failure; the active vault pointer is unchanged on any error.
//
// During shutdown, returns ErrShuttingDown.
//
// The chassis's SIGHUP handler invokes ReloadVault internally
// using s.cfg's configured paths and the vault key supplied at
// Server construction time.
func (s *Server) ReloadVault(ctx context.Context, newPath string, key *securebytes.SecureBytes) error
```
