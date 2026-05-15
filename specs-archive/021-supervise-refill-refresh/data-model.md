# Phase 1 Data Model — SDD-21 (`internal/supervise` refill + refresh + grace)

**Feature:** 021-supervise-refill-refresh
**Date:** 2026-05-10
**Spec:** [spec.md](./spec.md) · **Research:** [research.md](./research.md) · **Chunk doc:** [docs/sdd/SDD-21.md](../../docs/sdd/SDD-21.md)

This document is the locked structural inventory for the chunk's three
files. It pins struct shapes, field semantics, ownership rules, and
the per-entity invariants the tests will assert. Field types are
canonical Go; the implementation MUST NOT add unmentioned fields and
MUST NOT change exported field names. Unexported fields MAY be
renamed without notice.

---

## Entity overview

| Entity | File | Purpose | Owns |
|--------|------|---------|------|
| `Refiller` | `refill.go` | Per-supervisor refill orchestrator over `/s/<name>` | `*http.Client` (borrow), `*Store` (borrow), `*Grace` (borrow), `ECIESPrivKey` (borrow), `*slog.Logger` (borrow) |
| `Refresher` | `refresh.go` | Window + T-30 fallback scheduler | `time.Timer`, in-memory `lastFiredDay` + `t30Fired`, `refill` callback (borrow), `*slog.Logger` (borrow) |
| `Grace` | `grace.go` | Per-name `*SecureBytes` cache with TTL | `map[string]graceEntry` (owns), guarded by `sync.RWMutex` |
| `graceEntry` *(internal)* | `grace.go` | One name → `*SecureBytes` + expiry | `*securebytes.SecureBytes` (owns) |

**Borrow vs own.** A "borrow" reference is one whose lifetime exceeds
the entity's; the entity MUST NOT call `Destroy` / `Close` on it. An
"own" reference is one the entity Destroys / removes when its own
lifecycle ends (e.g. `Grace.Evict`, `Grace.Set`-overwrite, lazy
eviction in `Get`).

---

## 1. `Refiller`

### Struct shape (locked)

```go
// Refiller fetches and decrypts the per-supervisor scope set from
// the vault server. One Refiller is wired per supervisor at startup;
// the same instance is invoked once per refill cycle (clean exit,
// crash-restart, refresh, etc.). Safe for sequential calls; NOT safe
// for concurrent Refill invocations against the same instance — the
// orchestrator (SDD-23) serializes via the supervisor state machine.
type Refiller struct {
    client *http.Client                 // borrow; injected per Tailscale policy
    store  *Store                       // borrow; provides Snapshot() for JWT bearer
    grace  *Grace                       // borrow; receives committed *SecureBytes
    priv   *ecdsa.PrivateKey            // borrow; ECIES recipient key (SDD-09)
    logger *slog.Logger                 // borrow; operational log only
    server string                       // immutable; e.g. "https://vault.tailnet/h/<prefix>"
}
```

### Constructor

```go
func NewRefiller(client *http.Client, store *Store, logger *slog.Logger) *Refiller
```

**Locked signature** (per the Plan prompt). Two dependencies the
chunk needs but the constructor does NOT accept (`*Grace`, ECIES
private key, server URL) MUST be wired by the orchestrator after
construction via *unexported* setter methods used only inside the
package — OR the locked signature is honoured exactly and the
ECIES key + server URL + Grace handle are package-internal globals.
The latter is forbidden by Constitution IX (`gochecknoglobals`).

**Resolution.** The locked signature stays as listed. The missing
dependencies are passed via a single package-private wiring method:

```go
// (Refiller).attach is called once by the orchestrator (SDD-23) after
// construction. Splitting it out of NewRefiller honours the locked
// constructor signature in docs/sdd/SDD-21.md while giving the
// orchestrator a way to inject the post-init dependencies (ECIES key,
// grace cache handle, server URL prefix). attach is unexported; the
// orchestrator lives in the same package, so this is package-private.
func (r *Refiller) attach(grace *Grace, priv *ecdsa.PrivateKey, serverURL string)
```

The orchestrator (SDD-23) calls `attach` once at supervisor boot,
before any `Refill` call. Tests in `refill_test.go` call `attach`
directly (same package access). This is the same pattern used by
SDD-19's `setTokenForTest`.

Wait — the chunk doc says the orchestrator is SDD-23, not part of
this chunk. So `attach` belongs in `internal/supervise` but is invoked
by orchestrator code that this chunk does NOT write. Since both live
in the same Go package (`package supervise`), `attach` is callable.

**NEEDS-CLARIFICATION-RESOLVED:** Renaming `attach` to a
*near-public* name (still unexported, still in `internal/supervise`)
is fine; the chunk's test file imports `package supervise` and uses
`attach` directly, so no public surface is leaked.

### Method API

```go
// Refill fetches every name in scopes from the vault server using
// the JWT held in store.Snapshot().Token. On success, every decrypted
// *SecureBytes is handed to grace.Set(name, sb) and Refill returns
// nil. The orchestrator (SDD-23) is responsible for handing the same
// pointers to the child env builder.
//
// On any error, every successfully decrypted *SecureBytes from the
// current call is destroyed BEFORE Refill returns (FR-021-5). The
// returned error is one of:
//   - ErrJTIUnknown (wrapped) — server returned 401 with body
//     {"error":"unknown_jti"}; caller transitions state to
//     awaiting-approval (FR-021-3).
//   - any other error — wrapped underlying error from net / DNS /
//     timeout / non-401 HTTP / decode / ECIES (FR-021-4); caller
//     decides retry policy.
//
// Refill MUST NOT retry internally on any error class (caller-managed
// per chunk doc).
//
// Refill MUST NOT log any decrypted secret value (Constitution X).
func (r *Refiller) Refill(ctx context.Context, scopes []string) error
```

### Lifecycle / invariants

| ID | Invariant | Enforced where |
|----|-----------|---------------|
| RR-1 | Each `Refill` call is atomic w.r.t. decrypted material (FR-021-5) | `committed bool` defer pattern (R-007) |
| RR-2 | A 401-unknown-jti for any single scope short-circuits the loop | `for ... { if err != nil { return err } }` |
| RR-3 | Decrypted bytes never become a Go `string` (Constitution X) | `*SecureBytes` flow only; `Use(func(b []byte) {})` for any byte access |
| RR-4 | Bearer header materialization is bounded to a single closure | `snap.Token.Use(func(b []byte) { req.Header.Set("Authorization", "Bearer "+string(b)) })` |
| RR-5 | Response body size capped at 64 KiB | `io.LimitReader(resp.Body, 64*1024)` |
| RR-6 | Ciphertext bytes zeroed after ECIES.Decrypt | `for i := range raw { raw[i] = 0 }` |
| RR-7 | Server returns nil token → Refill returns wrapped error, not panic | `snap.Token == nil` check; programmer-error path |

### Error mapping (locked, FR-021-3 / FR-021-4)

| HTTP outcome | Body shape | Returned error |
|--------------|------------|---------------|
| 200 | binary ECIES envelope | `nil` |
| 401 | `{"error":"unknown_jti"}` | `fmt.Errorf("supervise/refill: %w", ErrJTIUnknown)` |
| 401 | other body | `fmt.Errorf("supervise/refill: status=401 body=<sanitized>")` |
| 5xx / 4xx (non-401) | n/a | `fmt.Errorf("supervise/refill: status=%d", status)` |
| network / DNS / TLS | n/a | `fmt.Errorf("supervise/refill: transport: %w", err)` |
| ctx cancelled | n/a | `fmt.Errorf("supervise/refill: %w", ctx.Err())` |
| ECIES decrypt failed | n/a | `fmt.Errorf("supervise/refill: decrypt: %w", err)` |
| JSON 401 body unparseable | n/a | `fmt.Errorf("supervise/refill: status=401 unparseable body")` (defaults to transient — caller does NOT transition) |

Note the last line: a 401 with an unparseable body is treated as
**transient**, not as `ErrJTIUnknown`. This honours the spec's
"distinguishable" requirement — only the exact `unknown_jti`
indicator triggers the state transition.

---

## 2. `Refresher`

### Struct shape (locked)

```go
// Refresher schedules at most one refresh callback fire per
// configured local-time window per calendar day, plus at most one
// T-30 fallback fire per session. Run blocks until ctx is cancelled.
type Refresher struct {
    window string                       // canonical "HH:MM-HH:MM"; immutable
    ttl    time.Duration                // session TTL from claim time; immutable
    refill func(ctx context.Context) error  // operator-supplied fire callback
    logger *slog.Logger                 // borrow; operational log only

    // unexported
    now          func() time.Time       // clock seam; default time.Now
    bornAt       time.Time              // ttl anchor; set by Run on entry
    lastFiredDay time.Time              // calendar date 00:00 local; FR-021-10
    t30Fired     bool                   // per-session flag; FR-021-8
}
```

### Constructor

```go
func NewRefresher(window string, ttl time.Duration, refill func(ctx context.Context) error, logger *slog.Logger) *Refresher
```

**Locked.** Five-arg constructor matches the chunk doc exactly. The
clock seam is unexported and defaults to `time.Now`; tests inject
via a package-private setter `(r *Refresher) setClockForTest(now
func() time.Time)`.

**Validation.** `NewRefresher` MUST panic if `refill == nil` or
`logger == nil` (Constitution IX startup-wiring exemption); pass-
through validates the window string by parsing it eagerly into the
four-int representation (start/end hour+minute) and panicking on
parse failure (programmer error — orchestrator pre-validates via
SDD-18).

### Method API

```go
// Run drives the scheduler tick loop. Returns ctx.Err() on
// cancellation, never any other error. Spawns NO goroutines beyond
// its own tick loop body. Safe to call exactly once per *Refresher;
// a second call returns an error immediately (sync.Once-guarded).
func (r *Refresher) Run(ctx context.Context) error
```

### Lifecycle / invariants

| ID | Invariant | Enforced where |
|----|-----------|---------------|
| RF-1 | At most one fire per (window, calendar-day) pair | `lastFiredDay` advance after every fire (R-002) |
| RF-2 | At most one T-30 fallback fire per session | `t30Fired bool` flag; reset only by next `Run` call (FR-021-8) |
| RF-3 | Run exits cleanly on ctx.Done() (FR-021-9) | top-level `select` with `<-ctx.Done()` arm |
| RF-4 | Backwards clock step does NOT cause double-fire (FR-021-11) | calendar-date key on `lastFiredDay` |
| RF-5 | Process restart inside window fires once on init (FR-021-10) | first-tick check before timer arm |
| RF-6 | Rate-limited fire is "issued" — no retry within window (FR-021-11a) | non-nil error from `refill` advances `lastFiredDay` regardless |
| RF-7 | Run() is single-shot — second call returns sentinel error | `sync.Once` guard at entry |
| RF-8 | No goroutines beyond Run's own body | no `go func()` anywhere; `refill` callback runs inline |

### Tick algorithm (R-002)

```text
loop:
    now := r.now().In(time.Local)
    today := dateOnly(now)                  // 00:00 local
    inWindow := windowContains(now, r.window)
    nextWindowStart := nextWindowStartFromToday(today, r.window, now)
    deadline := r.bornAt.Add(r.ttl)
    inT30 := deadline.Sub(now) < 30*time.Minute
    windowPassedToday := windowEndedBefore(now, r.window) && today.Equal(today)

    if !today.Equal(r.lastFiredDay) && inWindow:
        fire(); r.lastFiredDay = today
    else if !today.Equal(r.lastFiredDay) && windowPassedToday && inT30 && !r.t30Fired:
        fire(); r.lastFiredDay = today; r.t30Fired = true
    next := min(nextWindowStart, t30CheckAt(deadline))
    select:
        case <-ctx.Done(): return ctx.Err()
        case <-timer.C:    continue
```

`fire()` calls `r.refill(ctx)`; logs WARN on non-nil error; returns.
The error is NEVER propagated up to `Run`'s caller — the contract is
"exactly one fire per window crossing", not "abort scheduling on
fire-failure".

---

## 3. `Grace`

### Struct shape (locked)

```go
// Grace is the per-supervisor cache of last-decrypted *SecureBytes.
// Lifecycle: NewGrace returns a constructed cache that is empty.
// Refiller.Refill calls Set after a successful decrypt cycle. The
// orchestrator's restart path calls Get; on TTL elapse Get returns
// (nil, false) and lazy-evicts the entry inline. The orchestrator's
// `hush client refresh` flow calls Evict (FR-021-16). The cache is
// always-empty when enabled=false or window=0 (FR-021-14).
type Grace struct {
    mu      sync.RWMutex
    entries map[string]graceEntry
    enabled bool
    window  time.Duration            // capped at 4h via min(window, 4h)
    now     func() time.Time         // clock seam; default time.Now
}

// graceEntry is internal; never exported.
type graceEntry struct {
    sb      *securebytes.SecureBytes
    expires time.Time                // wall-clock instant
}
```

### Constructor

```go
func NewGrace(window time.Duration, enabled bool) *Grace
```

**Locked two-arg signature.** Per FR-021-14, the cache is
permanently empty when `enabled=false` OR `window<=0`. The
constructor records both and applies `window = min(window, 4*time.Hour)`
(FR-021-12) at construction. Tests inject the clock via a package-
private setter `(g *Grace) setClockForTest(now func() time.Time)`.

### Method API

```go
// Get returns the cached *SecureBytes for name. Returns (nil, false)
// when the entry is absent, expired, or when the cache is disabled.
// On expiry, Get atomically destroys the entry's *SecureBytes and
// removes the map slot before returning (R-008 lazy-evict). The
// returned *SecureBytes pointer is borrow-only — callers MUST NOT
// call Destroy on it; Grace retains ownership until the next Set,
// Evict, or expiry-on-Get.
func (g *Grace) Get(name string) (*securebytes.SecureBytes, bool)

// Set records the (name, value) pair with expiry = now() + window.
// When enabled=false or window=0, Set is a silent no-op and ownership
// of value remains with the caller (R-009). When an entry already
// exists for name, the prior entry's *SecureBytes is destroyed
// before the new one is recorded (FR-021-13). Set is concurrency-
// safe under the write lock.
func (g *Grace) Set(name string, value *securebytes.SecureBytes)

// Evict destroys the entry for name (if present) and removes the
// map slot. Calling Evict for an absent name is a silent no-op
// (Clarification 5 / FR-021-16). Evict is concurrency-safe under
// the write lock.
func (g *Grace) Evict(name string)
```

### Lifecycle / invariants

| ID | Invariant | Enforced where |
|----|-----------|---------------|
| GR-1 | Effective TTL is `min(window, 4h)` (FR-021-12) | `min(window, 4*time.Hour)` in `NewGrace` |
| GR-2 | Disabled cache stores nothing (FR-021-14) | `if !g.enabled || g.window == 0 { return }` in `Set` |
| GR-3 | Set-overwrite destroys the prior entry (FR-021-13) | `if prev, ok := g.entries[name]; ok { _ = prev.sb.Destroy() }` |
| GR-4 | Get on expired entry destroys + returns (nil, false) | lazy-evict path inside Get under write lock (R-008) |
| GR-5 | Evict is silent no-op on absent name (Clarification 5) | `if entry, ok := g.entries[name]; ok { ... }` |
| GR-6 | No goroutines (R-008 final) | zero `go func()` in this file |
| GR-7 | Cached values never become a Go string (Constitution X) | `*SecureBytes` flow only; type itself refuses string render |
| GR-8 | Concurrent Get/Set/Evict are race-clean | `sync.RWMutex` discipline; tests assert under `-race` |

### Get path detail — lazy eviction (R-008)

```go
func (g *Grace) Get(name string) (*securebytes.SecureBytes, bool) {
    g.mu.RLock()
    entry, ok := g.entries[name]
    if !ok || !g.enabled {
        g.mu.RUnlock()
        return nil, false
    }
    if entry.expires.Before(g.now()) {
        g.mu.RUnlock()
        // lazy-evict: re-acquire write lock, re-check (LRU race), evict.
        g.mu.Lock()
        defer g.mu.Unlock()
        if cur, stillPresent := g.entries[name]; stillPresent && cur.expires.Before(g.now()) {
            _ = cur.sb.Destroy()
            delete(g.entries, name)
        }
        return nil, false
    }
    sb := entry.sb
    g.mu.RUnlock()
    return sb, true
}
```

The lock-upgrade dance is necessary because `sync.RWMutex` does not
support upgrade. The double-check after re-acquiring the write lock
prevents two concurrent expiry-Gets from double-Destroying the same
entry (the second's `cur, stillPresent` returns `false` after the
first deletes).

---

## 4. Sentinel errors

```go
// ErrJTIUnknown is returned (wrapped) by Refill when the vault server
// returns 401 with body {"error":"unknown_jti"}. The orchestrator
// must transition the supervisor to StateAwaitingApproval and prompt
// the operator (FR-021-3).
//
// Compare via errors.Is(err, supervise.ErrJTIUnknown).
var ErrJTIUnknown = errors.New("supervise: vault rejected JWT (unknown jti)")

// ErrBootTimeout is the sentinel the orchestrator's boot-retry helper
// (SDD-23) returns when the boot_retry_timeout budget is exhausted.
// It is declared in this chunk because the locked exported API of
// SDD-21 includes it; this chunk does NOT produce it from any code
// path inside Refill / Run / Get / Set / Evict (R-010 / FR-021-20).
var ErrBootTimeout = errors.New("supervise: boot retry timeout exhausted")
```

Both sentinels are package-level `var Err... = errors.New(...)` per
Constitution IX (sentinel-class read-only globals are acceptable;
mutable globals are forbidden). Both are exported and listed in the
locked PACKAGE-MAP entry under "Exported API — locked at SDD-21".

---

## 5. Cross-entity ownership graph

```text
Orchestrator (SDD-23)
    │
    ├─ owns *Store (SDD-19)
    ├─ owns *Refiller        ←── borrows *Store, *Grace, http.Client, ECIES priv key
    ├─ owns *Refresher       ←── borrows refill callback (orchestrator's bound method)
    ├─ owns *Grace           ←── owns map[string]graceEntry, owns each *SecureBytes therein
    │
    └─ owns *Child (SDD-20)  ←── consumes *SecureBytes via env-builder (out of scope here)
```

Every `*SecureBytes` allocated by `ecies.Decrypt` flows
deterministically: Refiller → (Grace OR child env builder OR
destroyed-on-error). Grace's lifetime extends past the child's; on
child crash within TTL, the orchestrator queries Grace.Get and hands
the still-mlocked bytes to the new child. On TTL elapse OR explicit
Evict, the bytes are zeroed via `*SecureBytes.Destroy`.

---

## 6. Test-only seams (package-private)

| Seam | File | Purpose | Constitution-IX rationale |
|------|------|---------|--------------------------|
| `(*Refiller).attach(grace, priv, serverURL)` | `refill.go` | Post-construction wiring; orchestrator-only | Locked constructor signature exact; deps too late to wire at NewRefiller |
| `(*Refresher).setClockForTest(now func() time.Time)` | `refresh.go` | Inject fake clock | Honours locked 5-arg `NewRefresher`; clock seam is internal field, not global |
| `(*Grace).setClockForTest(now func() time.Time)` | `grace.go` | Inject fake clock | Honours locked 2-arg `NewGrace`; clock seam is internal field, not global |
| `roundTripFunc` *(test-only)* | `helpers_test.go` | Stub `http.RoundTripper` | Tests inject via `&http.Client{Transport: roundTripFunc(fn)}` |
| `fakeApprover` *(test-only)* | `helpers_test.go` | Records `refill` callback invocations | Refresher's `refill` callback is `func(ctx) error` — tests pass a closure |

All seams are unexported. None are package-level mutable state.

---

## 7. State-machine integration (read-only consumers)

This chunk does NOT call `Store.Transition`. The orchestrator (SDD-23)
inspects Refill's return error and emits the corresponding event:

| Refill outcome | SDD-19 Event |
|----------------|--------------|
| `nil` | `EventFetchOK` (StateFetching → StateRunning, FR-019) |
| `ErrJTIUnknown` | `EventFetchAuthRequired` (StateFetching → StateAwaitingApproval) |
| any other error | none — orchestrator may retry, escalate, or remain in StateFetching with WARN log |

This chunk's tests verify the error class returned; SDD-23's tests
verify the transition wiring.
