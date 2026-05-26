package supervise

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// State is the supervisor's lifecycle state. Exactly six values
// are valid; see the constants below. The string forms
// are part of the operator-visible contract (status socket JSON,
// audit log) and MUST NOT be renamed without a SPEC amendment.
type State string

const (
	StateFetching         State = "fetching"
	StateRunning          State = "running"
	StateAwaitingApproval State = "awaiting-approval"
	StateGraceRestart     State = "grace-restart"
	StateSwapping         State = "swapping"
	StateStopped          State = "stopped"
)

// Event is the closed vocabulary of lifecycle events the state
// machine recognizes. The string forms are part of the audit-log
// contract.
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
	EventReloadRequested       Event = "reload-requested"
	EventSwapOK                Event = "swap-ok"
	EventSwapFailed            Event = "swap-failed"
	EventStopRequested         Event = "stop-requested"
)

// Clock is the wall-clock source the Store consults to stamp
// LastTransitionAt on every successful transition. Production wires
// a real-time impl backed by time.Now(); tests wire a fake.
// Single-method interface; defined at the consumer.
type Clock interface {
	Now() time.Time
}

// ErrInvalidTransition is returned (wrapped) by Transition when no
// edge exists for the (currentState, event) pair, when the event
// is outside the closed vocabulary, or when both. Identifiable
// via errors.Is.
var ErrInvalidTransition = errors.New("supervise: invalid transition")

// reasons is the closed event-to-phrase map populated at package
// init. The keyset equals the Event vocabulary exactly. Read-only
// post-init; sentinel-class equivalent to var Err... = errors.New(...).
var reasons = map[Event]string{ //nolint:gochecknoglobals // sentinel-class read-only map; locked at package init
	EventFetchOK:               "fetch succeeded",
	EventFetchAuthRequired:     "fetch rejected: re-approval required",
	EventClaimDenied:           "claim denied by operator",
	EventClaimUnavailable:      "claim unavailable: discord disconnected",
	EventValidatorFailed:       "validator rejected fetched secret",
	EventBootRetryExhausted:    "boot retry exhausted",
	EventChildExitClean:        "child exited cleanly",
	EventChildExitCrash:        "child crashed",
	EventChildExit78Stale:      "child reported stale credentials (exit 78)",
	EventRefreshRequested:      "refresh requested",
	EventGraceRestartTriggered: "entering grace restart",
	EventGraceRestartOK:        "grace restart succeeded",
	EventGraceExpired:          "grace window expired",
	EventApprovalGranted:       "operator approved",
	EventReloadRequested:       "reload requested",
	EventSwapOK:                "child swap succeeded",
	EventSwapFailed:            "child swap failed; rolled back to prior child",
	EventStopRequested:         "stop requested",
}

// transitions encodes every legal (State, Event) -> State edge.
// 23 cells; outer keys are the 6 states and inner-map values are
// drawn from the same closed set. Read-only post-init.
var transitions = map[State]map[Event]State{ //nolint:gochecknoglobals // sentinel-class read-only state-table; locked at package init
	StateFetching: {
		EventFetchOK:            StateRunning,
		EventFetchAuthRequired:  StateAwaitingApproval,
		EventClaimDenied:        StateAwaitingApproval,
		EventClaimUnavailable:   StateAwaitingApproval,
		EventValidatorFailed:    StateAwaitingApproval,
		EventBootRetryExhausted: StateStopped,
		EventStopRequested:      StateStopped,
	},
	StateRunning: {
		EventChildExitClean:        StateFetching,
		EventChildExitCrash:        StateFetching,
		EventChildExit78Stale:      StateAwaitingApproval,
		EventRefreshRequested:      StateFetching,
		EventGraceRestartTriggered: StateGraceRestart,
		EventReloadRequested:       StateSwapping,
		EventStopRequested:         StateStopped,
	},
	StateAwaitingApproval: {
		EventApprovalGranted: StateFetching,
		EventStopRequested:   StateStopped,
	},
	StateGraceRestart: {
		EventRefreshRequested: StateFetching,
		EventGraceRestartOK:   StateRunning,
		EventGraceExpired:     StateAwaitingApproval,
		EventStopRequested:    StateStopped,
	},
	StateSwapping: {
		EventSwapOK:        StateRunning,
		EventSwapFailed:    StateRunning,
		EventStopRequested: StateStopped,
	},
	StateStopped: {
		EventStopRequested: StateStopped,
	},
}

// Store is the supervisor's guarded state container. Safe for
// concurrent Transition and Snapshot from many goroutines.
// Construct via NewStore; the zero value is NOT usable.
//
// Owns no goroutines. Triggers no side-effects beyond in-memory
// mutation. All field writes happen under a write lock; all field
// reads happen under a read lock.
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
// LastTransitionAt set to clock.Now() at construction.
// ctx is accepted for parity with future expansion but is
// currently unused; passing context.Background() is acceptable.
// Passing a nil clock is a programmer error and panics at
// construction.
func NewStore(_ context.Context, clock Clock) *Store {
	if clock == nil {
		panic("supervise: NewStore requires a non-nil Clock")
	}
	return &Store{
		currentState:     StateFetching,
		lastTransitionAt: clock.Now(),
		reason:           "store constructed",
		clock:            clock,
	}
}

// Transition applies event under the write lock. On legal
// transitions the store updates currentState, lastTransitionAt
// (from the injected clock), reason (from the closed event-to-
// phrase map), and possibly clears or replaces the cached token.
// On illegal transitions the store is unchanged and the returned
// error wraps ErrInvalidTransition with both the current state
// and the rejected event named.
//
// EventStopRequested is legal from every state, including
// StateStopped (idempotent no-op-success).
//
// ctx is accepted for parity; the current implementation does no
// cancellable work.
func (s *Store) Transition(_ context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, ok := transitions[s.currentState][event]
	if !ok {
		return fmt.Errorf("supervise: %w (state=%s event=%s)", ErrInvalidTransition, s.currentState, event)
	}
	s.currentState = next
	s.lastTransitionAt = s.clock.Now()
	s.reason = reasons[event]
	return nil
}

// TransitionIf is a compare-and-transition: applies event only when the
// current state equals expected, atomically under the write lock. Used by
// callers that need the precondition check and the transition to be a single
// critical section (e.g. SwapChild's StateRunning → StateSwapping move,
// where a stale Snapshot() read followed by a non-atomic Transition would
// race the mainLoop dispatcher).
//
// Returns nil on success. Returns a wrapped ErrInvalidTransition naming both
// the observed state and the rejected event when the precondition fails OR
// when the transition itself is illegal from the observed state. Callers
// inspect via errors.Is.
//
// ctx is accepted for parity with Transition; the current implementation
// does no cancellable work.
func (s *Store) TransitionIf(_ context.Context, expected State, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentState != expected {
		return fmt.Errorf("supervise: %w (state=%s event=%s expected=%s)",
			ErrInvalidTransition, s.currentState, event, expected)
	}
	next, ok := transitions[s.currentState][event]
	if !ok {
		return fmt.Errorf("supervise: %w (state=%s event=%s)", ErrInvalidTransition, s.currentState, event)
	}
	s.currentState = next
	s.lastTransitionAt = s.clock.Now()
	s.reason = reasons[event]
	return nil
}

// Snapshot returns a defensive-copy point-in-time view of the
// store's observable fields. The returned value's Token field, if
// non-nil, is a pointer to the same *securebytes.SecureBytes the
// store holds — borrow-only access. Mutating any field of the
// returned value does NOT affect the store. A snapshot taken
// concurrently with a transition observes either the pre or the
// post state in full.
func (s *Store) Snapshot() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Snapshot{
		State:            s.currentState,
		ChildPID:         s.childPID,
		LastTransitionAt: s.lastTransitionAt,
		Token:            s.token,
		Reason:           s.reason,
	}
}

// Snapshot is the by-value view returned by Store.Snapshot().
// Carries exactly the fields downstream readers (status socket,
// audit emitter) need. Renders Token as "[redacted]" through slog.
type Snapshot struct {
	State            State
	ChildPID         int
	LastTransitionAt time.Time
	Token            *securebytes.SecureBytes
	Reason           string
}

// setToken is a package-private seam: the orchestrator writes the
// JWT here after a successful /claim. Production-path seam; tests
// also call it directly because they live in the same package. The
// method remains unexported.
//
// On replacement, the prior *SecureBytes is explicitly Destroy'd
// (Principle VI / Layer 5 — explicit zeroing on lifecycle transitions
// rather than waiting for the runtime finalizer). Passing the same
// pointer twice is a no-op; passing nil clears the slot.
//
// Serialization is per-Use, not per-snapshot: a reader currently
// inside the prior *SecureBytes' Use callback blocks Destroy on that
// SecureBytes' own mutex, so the in-flight Use completes before the
// buffer is zeroed. Callers that hold a Snapshot.Token reference
// across multiple Use calls (e.g. a multi-scope Refiller.Refill) MUST
// tolerate ErrDestroyed on subsequent Use invocations — between calls
// there is no protection against a concurrent setToken destroying the
// captured pointer. Re-Snapshot per iteration to narrow the window.
func (s *Store) setToken(tok *securebytes.SecureBytes) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.token
	s.token = tok
	if prev != nil && prev != tok {
		_ = prev.Destroy()
	}
}

// destroyToken explicitly zeroes and clears the stored token. Invoked
// by Lifecycle.runShutdown so the supervisor's SIGTERM path retires
// the current JWT plaintext that would otherwise outlive the
// orchestrator (the runtime finalizer does NOT run on process exit).
// Idempotent — calling it on an already-cleared Store is a silent
// no-op. Mirrors Grace.Destroy on the JWT side.
func (s *Store) destroyToken() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == nil {
		return
	}
	_ = s.token.Destroy()
	s.token = nil
}
