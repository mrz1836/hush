package client

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// EventType is the discriminator for Event.
type EventType string

const (
	// EventInitial is the first event emitted on every successful
	// Watch subscription, carrying the snapshot at subscribe time.
	// Lets the caller render its initial UI / decide whether to
	// proceed without waiting for the first state change.
	EventInitial EventType = "initial"

	// EventStateChange fires when the supervisor's State field
	// transitions (e.g. "running" → "awaiting-approval").
	EventStateChange EventType = "state_change"

	// EventScopeHealthChange fires when the set of healthy or stale
	// scopes changes — typically immediately after a rotation, but
	// also when a validator promotes / demotes a scope's health.
	EventScopeHealthChange EventType = "scope_change"

	// EventSessionRenewed fires when the supervisor's SessionJTI
	// changes, signaling a fresh approval was received. Resets the
	// expires-soon threshold tracker so a new session re-fires the
	// warning ladder.
	EventSessionRenewed EventType = "session_renewed"

	// EventExpiresSoon fires once per configured threshold as
	// SessionExpiresAt approaches now. The Threshold field on the
	// Event identifies which warning is firing (e.g. 5*time.Minute).
	// Cooperative agents should treat the first event as "checkpoint
	// in-flight work" and the last as "exit cleanly NOW."
	EventExpiresSoon EventType = "expires_soon"

	// EventError fires when a poll fails. Transient failures (e.g. a
	// supervisor restart) emit Error and continue polling; the Watch
	// channel is NOT closed. Permanent failures (ctx cancel) close
	// the channel without an EventError.
	EventError EventType = "error"
)

// errNilSupervisor is returned by Watch when called on a nil
// *SupervisorStatus receiver. Programmer-error class; surfaced as
// an error rather than a panic so library users get a clean failure.
var errNilSupervisor = errors.New("hush/client: Watch called on nil SupervisorStatus")

// Event is one notification on a Watch channel.
type Event struct {
	Type      EventType
	At        time.Time
	Status    *Status       // last good snapshot — populated for all non-Error events
	Threshold time.Duration // populated for EventExpiresSoon: which threshold fired
	Err       error         // populated for EventError
}

// WatchOptions tunes Watch.
//
// PollInterval controls how often the SDK polls the supervisor status
// socket. Defaults to 30s when zero or negative. Minimum effective
// interval is 1s to prevent accidental DoS of the supervisor.
//
// ExpiryThresholds is the list of warning durations BEFORE
// SessionExpiresAt at which EventExpiresSoon fires. Defaults to
// {15m, 5m, 1m, 30s}. Each threshold fires at most once per session;
// EventSessionRenewed resets the tracker.
//
// Buffer is the channel buffer size. Defaults to 16. When the buffer
// is full a slow consumer causes new events to be dropped silently;
// consumers should drain promptly or run Watch in its own goroutine.
type WatchOptions struct {
	PollInterval     time.Duration
	ExpiryThresholds []time.Duration
	Buffer           int
}

// Watch returns a read-only channel of lifecycle Events derived from
// the supervisor's status socket. The channel is closed when ctx is
// cancelled.
//
// Watch is intended for cooperative agents that want reactive
// notification of state changes and pre-expiry warnings without
// hand-rolling a polling loop.
//
// Each Watch call spawns one goroutine. Multiple concurrent Watch
// calls on the same SupervisorStatus are supported but independent —
// they each poll, each fire their own thresholds, and consume one
// Unix-socket connection per poll.
//
// Implementation note: PR 3 ships a polling implementation for
// simplicity and minimum protocol risk. The Event surface is
// future-compatible with a server-pushed stream that may replace
// polling in a later release; callers should not depend on poll
// timing for security-critical decisions.
func (s *SupervisorStatus) Watch(ctx context.Context, opts WatchOptions) (<-chan Event, error) {
	if s == nil {
		return nil, errNilSupervisor
	}
	opts = opts.withDefaults()
	ch := make(chan Event, opts.Buffer)
	go s.watchLoop(ctx, opts, ch)
	return ch, nil
}

// withDefaults returns a copy of opts with all zero-value fields
// populated with their defaults.
func (o WatchOptions) withDefaults() WatchOptions {
	out := o
	if out.PollInterval <= 0 {
		out.PollInterval = 30 * time.Second
	}
	if out.PollInterval < time.Second {
		out.PollInterval = time.Second
	}
	if len(out.ExpiryThresholds) == 0 {
		out.ExpiryThresholds = []time.Duration{
			15 * time.Minute,
			5 * time.Minute,
			time.Minute,
			30 * time.Second,
		}
	}
	if out.Buffer <= 0 {
		out.Buffer = 16
	}
	// Sort thresholds descending so the largest (earliest warning)
	// fires first as time approaches expiry.
	thresholds := append([]time.Duration(nil), out.ExpiryThresholds...)
	slices.SortFunc(thresholds, func(a, b time.Duration) int {
		switch {
		case a > b:
			return -1
		case a < b:
			return 1
		default:
			return 0
		}
	})
	out.ExpiryThresholds = thresholds
	return out
}

// watchLoop is the goroutine body. Owns the channel; closes it on
// ctx.Done.
func (s *SupervisorStatus) watchLoop(ctx context.Context, opts WatchOptions, ch chan<- Event) {
	defer close(ch)

	prev := s.initialSnapshot(ctx, ch)

	poll := time.NewTicker(opts.PollInterval)
	defer poll.Stop()

	fired := map[time.Duration]bool{}
	for {
		if s.watchOnce(ctx, opts, ch, &prev, fired, poll.C) {
			return
		}
	}
}

// initialSnapshot performs the first Snapshot call and emits either
// EventInitial (on success) or EventError. Returns the snapshot for
// loop state, or nil when the snapshot failed.
func (s *SupervisorStatus) initialSnapshot(ctx context.Context, ch chan<- Event) *Status {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		send(ctx, ch, Event{Type: EventError, At: time.Now(), Err: err})
		return nil
	}
	send(ctx, ch, Event{Type: EventInitial, At: time.Now(), Status: snap})
	return snap
}

// watchOnce runs one iteration of the watch loop. Returns true when
// the loop should terminate (ctx cancelled). The prev pointer is
// updated in place so the caller's loop state evolves across calls.
func (s *SupervisorStatus) watchOnce(
	ctx context.Context, opts WatchOptions, ch chan<- Event,
	prev **Status, fired map[time.Duration]bool, pollC <-chan time.Time,
) bool {
	threshDelay, threshLabel := nextThresholdDelay(*prev, opts.ExpiryThresholds, fired, time.Now())
	if threshDelay == 0 && threshLabel > 0 {
		// Already crossed — fire immediately and re-iterate.
		fireExpiresSoon(ctx, ch, *prev, threshLabel, fired)
		return false
	}
	var threshC <-chan time.Time
	var threshTimer *time.Timer
	if threshDelay > 0 {
		threshTimer = time.NewTimer(threshDelay)
		threshC = threshTimer.C
		defer threshTimer.Stop()
	}
	select {
	case <-ctx.Done():
		return true
	case <-pollC:
		s.handlePoll(ctx, ch, prev, fired)
	case <-threshC:
		fireExpiresSoon(ctx, ch, *prev, threshLabel, fired)
	}
	return false
}

// handlePoll performs one Snapshot and dispatches any transition
// events. Transient snapshot failures emit EventError without
// terminating the loop.
func (s *SupervisorStatus) handlePoll(ctx context.Context, ch chan<- Event, prev **Status, fired map[time.Duration]bool) {
	snap, err := s.Snapshot(ctx)
	if err != nil {
		send(ctx, ch, Event{Type: EventError, At: time.Now(), Err: err})
		return
	}
	emitTransitionEvents(ctx, ch, *prev, snap, fired, time.Now())
	*prev = snap
}

// fireExpiresSoon marks the threshold as fired and emits the event.
func fireExpiresSoon(ctx context.Context, ch chan<- Event, status *Status, threshold time.Duration, fired map[time.Duration]bool) {
	fired[threshold] = true
	send(ctx, ch, Event{
		Type: EventExpiresSoon, At: time.Now(),
		Status: status, Threshold: threshold,
	})
}

// emitTransitionEvents compares prev and snap and emits any
// state/scope/session events. Resets the fired-threshold tracker on
// session renewal.
func emitTransitionEvents(ctx context.Context, ch chan<- Event, prev, snap *Status, fired map[time.Duration]bool, t time.Time) {
	if prev == nil {
		send(ctx, ch, Event{Type: EventInitial, At: t, Status: snap})
		return
	}
	if prev.SessionJTI != snap.SessionJTI && snap.SessionJTI != "" {
		// New session — reset expiry thresholds before announcing.
		for k := range fired {
			delete(fired, k)
		}
		send(ctx, ch, Event{Type: EventSessionRenewed, At: t, Status: snap})
	}
	if prev.State != snap.State {
		send(ctx, ch, Event{Type: EventStateChange, At: t, Status: snap})
	}
	if !scopesEqual(prev.ScopeHealthy, snap.ScopeHealthy) || !scopesEqual(prev.ScopeStale, snap.ScopeStale) {
		send(ctx, ch, Event{Type: EventScopeHealthChange, At: t, Status: snap})
	}
}

// nextThresholdDelay returns the delay until the next un-fired
// threshold and the threshold itself. Returns (0, threshold) when a
// threshold is already overdue at now; returns (-1, 0) when there is
// nothing pending (no session, or all thresholds already fired, or
// expiry already passed).
func nextThresholdDelay(prev *Status, thresholds []time.Duration, fired map[time.Duration]bool, now time.Time) (time.Duration, time.Duration) {
	if prev == nil || prev.SessionExpiresAt.IsZero() {
		return -1, 0
	}
	remaining := prev.SessionExpiresAt.Sub(now)
	if remaining <= 0 {
		return -1, 0
	}
	// Largest-first iteration so we pick the next applicable warning.
	// thresholds is pre-sorted descending in withDefaults.
	for _, th := range thresholds {
		if fired[th] {
			continue
		}
		fireAt := prev.SessionExpiresAt.Add(-th)
		delay := fireAt.Sub(now)
		if delay <= 0 {
			// Already crossed but not yet fired — fire immediately.
			return 0, th
		}
		return delay, th
	}
	return -1, 0
}

// scopesEqual reports whether two scope-name slices have identical
// contents in identical order. The supervisor preserves stable order,
// so order-sensitive comparison is correct here and avoids the cost
// of building maps for typically-small slices.
func scopesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// send delivers ev to ch, dropping silently when the buffer is full
// or when ctx is cancelled. Drop-on-overflow keeps the watcher
// goroutine non-blocking — a slow consumer never starves the timer
// machinery.
func send(ctx context.Context, ch chan<- Event, ev Event) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	default:
		// Buffer full; drop the event to keep the loop responsive.
		// Callers that need lossless delivery should size Buffer
		// generously or drain promptly.
		_ = fmt.Sprintf // intentionally no log; SDK avoids embedding a logger.
	}
}
