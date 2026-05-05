package supervise

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

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

// Event is the closed vocabulary of lifecycle events the state
// machine recognizes (FR-019-21). The string forms are part of
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

// Clock is the wall-clock source the Store consults to stamp
// LastTransitionAt on every successful transition (FR-019-20).
// Production wires a real-time impl backed by time.Now(); tests
// wire a fake. Single-method interface; defined at the consumer
// per Constitution IX.
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
// post-init; sentinel-class equivalent to var Err... = errors.New(...)
// per Constitution IX (R-002, R-005).
var reasons = map[Event]string{ //nolint:gochecknoglobals // sentinel-class read-only map; locked at package init per Constitution IX (R-002, R-005)
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
	EventStopRequested:         "stop requested",
}

// transitions encodes every legal (State, Event) -> State edge.
// 19 cells from contracts/state-table.md; outer keys are the 5
// states and inner-map values are drawn from the same closed set.
// Read-only post-init (R-002).
var transitions = map[State]map[Event]State{ //nolint:gochecknoglobals // sentinel-class read-only state-table; locked at package init per Constitution IX (R-002)
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
		EventStopRequested:         StateStopped,
	},
	StateAwaitingApproval: {
		EventApprovalGranted: StateFetching,
		EventStopRequested:   StateStopped,
	},
	StateGraceRestart: {
		EventGraceRestartOK: StateRunning,
		EventGraceExpired:   StateAwaitingApproval,
		EventStopRequested:  StateStopped,
	},
	StateStopped: {
		EventStopRequested: StateStopped,
	},
}

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

// Snapshot returns a defensive-copy point-in-time view of the
// store's observable fields (FR-019-7, FR-019-8). The returned
// value's Token field, if non-nil, is a pointer to the same
// *securebytes.SecureBytes the store holds — borrow-only access
// per SDD-02. Mutating any field of the returned value does NOT
// affect the store. A snapshot taken concurrently with a
// transition observes either the pre or the post state in full
// (FR-019-14).
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
// audit emitter) need. Renders Token as "[redacted]" through
// slog (Constitution X).
type Snapshot struct {
	State            State
	ChildPID         int
	LastTransitionAt time.Time
	Token            *securebytes.SecureBytes
	Reason           string
}

// setTokenForTest is a package-private seam used by state_test.go;
// production token writes are owned by SDD-21.
func (s *Store) setTokenForTest(tok *securebytes.SecureBytes) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = tok
}
