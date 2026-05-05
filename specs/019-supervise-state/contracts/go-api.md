# Locked Go API — `internal/supervise` (SDD-19)

**Branch**: `019-supervise-state` | **Date**: 2026-05-05

This is the contractual surface this chunk locks into the
codebase. The exact Go signatures, constant values, error
sentinels, and reason phrases below are non-negotiable for the
duration of v0.1.0 unless a SPEC amendment changes them.

Path: `github.com/mrz1836/hush/internal/supervise`

```go
// Package supervise owns the supervisor daemon's lifecycle state
// machine, child runner, refill/refresh/grace logic, status socket,
// and watchdog. SDD-19 ships the lifecycle state machine and
// snapshot store; subsequent chunks (SDD-20..22, SDD-25) add
// behaviour on top of this package without modifying the locked
// API below.
package supervise

import (
    "context"
    "errors"
    "fmt"
    "sync"
    "time"

    "github.com/mrz1836/hush/internal/vault/securebytes"
)

// ---------- State ----------

// State is the supervisor's lifecycle state. Exactly five values
// are valid (FR-019-1); see the constants below. The string forms
// are part of the operator-visible contract (status socket JSON,
// audit log) and MUST NOT be renamed without a SPEC amendment.
type State string

const (
    StateFetching         State = "fetching"
    StateRunning          State = "running"
    StateAwaitingApproval State = "awaiting-approval"
    StateGraceRestart     State = "grace-restart"
    StateStopped          State = "stopped"
)

// ---------- Event ----------

// Event is the closed vocabulary of lifecycle events the state
// machine recognises (FR-019-21). The string forms are part of
// the audit-log contract.
type Event string

const (
    EventFetchOK               Event = "fetch-ok"
    EventFetchAuthRequired     Event = "fetch-auth-required"
    EventClaimDenied           Event = "claim-denied"
    EventClaimUnavailable      Event = "claim-unavailable"
    EventValidatorFailed       Event = "validator-failed"
    EventBootRetryExhausted    Event = "boot-retry-exhausted"
    EventChildExitClean        Event = "child-exit-clean"
    EventChildExitCrash        Event = "child-exit-crash"
    EventChildExit78Stale      Event = "child-exit-78-stale"
    EventRefreshRequested      Event = "refresh-requested"
    EventGraceRestartTriggered Event = "grace-restart-triggered"
    EventGraceRestartOK        Event = "grace-restart-ok"
    EventGraceExpired          Event = "grace-expired"
    EventApprovalGranted       Event = "approval-granted"
    EventStopRequested         Event = "stop-requested"
)

// ---------- Clock seam ----------

// Clock is the wall-clock source the Store consults to stamp
// LastTransitionAt on every successful transition (FR-019-20).
// Production wires a real-time impl backed by time.Now(); tests
// wire a fake. Single-method interface; defined at the consumer
// per Constitution IX.
type Clock interface {
    Now() time.Time
}

// ---------- Store ----------

// Store is the supervisor's guarded state container. Safe for
// concurrent Transition and Snapshot from many goroutines.
// Construct via NewStore; the zero value is NOT usable.
//
// Owns no goroutines (FR-019-12). Triggers no side-effects beyond
// in-memory mutation (FR-019-13). All field writes happen under a
// write lock; all field reads happen under a read lock.
type Store struct {
    mu               sync.RWMutex
    currentState     State
    childPID         int
    lastTransitionAt time.Time
    token            *securebytes.SecureBytes
    reason           string
    clock            Clock
}

// NewStore returns a fresh Store in StateFetching, with
// LastTransitionAt set to clock.Now() at construction (FR-019-16).
// ctx is accepted for parity with future expansion but is
// currently unused; passing context.Background() is acceptable.
// Passing a nil clock is a programmer error and panics at
// construction (Constitution IX explicit-panic exemption for
// startup wiring).
func NewStore(ctx context.Context, clock Clock) *Store

// Transition applies event under the write lock. On legal
// transitions the store updates currentState, lastTransitionAt
// (from the injected clock), reason (from the closed event-to-
// phrase map), and possibly clears or replaces the cached token
// (per R-007). On illegal transitions the store is unchanged
// (FR-019-6) and the returned error wraps ErrInvalidTransition
// with both the current state and the rejected event named
// (FR-019-15).
//
// EventStopRequested is legal from every state, including
// StateStopped (idempotent no-op-success per FR-019-17).
//
// ctx is accepted for parity; the current implementation does no
// cancellable work.
func (s *Store) Transition(ctx context.Context, event Event) error

// Snapshot returns a defensive-copy point-in-time view of the
// store's observable fields (FR-019-7, FR-019-8). The returned
// value's Token field, if non-nil, is a pointer to the same
// *securebytes.SecureBytes the store holds — borrow-only access
// per SDD-02. Mutating any field of the returned value does NOT
// affect the store. A snapshot taken concurrently with a
// transition observes either the pre or the post state in full
// (FR-019-14).
func (s *Store) Snapshot() Snapshot

// ---------- Snapshot ----------

// Snapshot is the by-value view returned by Store.Snapshot().
// Carries exactly the fields downstream readers (status socket,
// audit emitter) need. Renders Token as "[redacted]" through
// slog (Constitution X).
type Snapshot struct {
    State            State
    ChildPID         int
    LastTransitionAt time.Time
    Token            *securebytes.SecureBytes
    Reason           string
}

// ---------- Sentinel errors ----------

// ErrInvalidTransition is returned (wrapped) by Transition when no
// edge exists for the (currentState, event) pair, when the event
// is outside the closed vocabulary, or when both. Identifiable
// via errors.Is.
var ErrInvalidTransition = errors.New("supervise: invalid transition")
```

## Wrapping form

The exact wrapping form for invalid-transition errors:

```go
fmt.Errorf("supervise: %w (state=%s event=%s)", ErrInvalidTransition, current, event)
```

Operators reading a log MUST be able to identify both the current
state and the rejected event without reading source code
(FR-019-15, SC-019-7). The names of the `State` and `Event`
constants are deliberately kebab-case strings to make log lines
self-explanatory.

## Closed event-to-phrase mapping (`Reason` derivation)

Locked as the package-internal `var reasons = map[Event]string{...}`
populated at package init. Tests assert the map's keyset equals
the closed event vocabulary exactly (no missing event, no extra
phrase). Phrases below are the v0.1.0 lock; amendments require a
SPEC change.

| Event | Reason phrase |
|-------|---------------|
| `EventFetchOK` | `"fetch succeeded"` |
| `EventFetchAuthRequired` | `"fetch rejected: re-approval required"` |
| `EventClaimDenied` | `"claim denied by operator"` |
| `EventClaimUnavailable` | `"claim unavailable: discord disconnected"` |
| `EventValidatorFailed` | `"validator rejected fetched secret"` |
| `EventBootRetryExhausted` | `"boot retry exhausted"` |
| `EventChildExitClean` | `"child exited cleanly"` |
| `EventChildExitCrash` | `"child crashed"` |
| `EventChildExit78Stale` | `"child reported stale credentials (exit 78)"` |
| `EventRefreshRequested` | `"refresh requested"` |
| `EventGraceRestartTriggered` | `"entering grace restart"` |
| `EventGraceRestartOK` | `"grace restart succeeded"` |
| `EventGraceExpired` | `"grace window expired"` |
| `EventApprovalGranted` | `"operator approved"` |
| `EventStopRequested` | `"stop requested"` |

The initial `Reason` set by `NewStore` (no event applied yet) is
the literal string `"store constructed"`.

## Anti-API (NOT exported, NOT added)

The following are explicitly **not** in the locked surface; adding
them would either violate the spec or pre-commit a contract that
a downstream chunk has the right to define:

- `func (s *Store) SetToken(*securebytes.SecureBytes)` — token
  write seam is deferred to SDD-21 (research R-009). The store
  exposes only `Transition` + `Snapshot` publicly.
- `func (s *Store) ChildPID() int` — readers go through
  `Snapshot()`. There is no per-field accessor.
- `func (s *Store) State() State` — same; use `Snapshot().State`.
- `func (s *Store) Reset()` / `func (s *Store) Stop()` — there is
  no escape hatch from `StateStopped`. Operators construct a fresh
  `Store` if they want a fresh supervisor. Idempotent stop is via
  `Transition(ctx, EventStopRequested)`.
- `var Now = time.Now` — clock seam is via the `Clock` interface,
  not a swappable package-level function (Constitution IX).
- A `LoadReader(io.Reader)` or any string-event entry point —
  events are typed Go constants, not parsed strings.
