package alerts

// Sibling at internal/discord/ratelimit.go shares this shape;
// duplication is intentional because this package is restricted to
// stdlib imports (see .github/CLAUDE.md R-016).

import (
	"sync"
	"time"
)

// ratebucket is the per-key minimum-interval debounce primitive
// (R-009). One Router holds two independent ratebucket instances:
// one keyed by SupervisorName, one keyed by Pattern (with class-name
// fallback per FR-011a).
//
// Concurrency model: a single sync.Mutex guards the entire entries
// map and per-key transitions. acquire/commit/refund each take the
// mutex once. No goroutines spawned.
//
// Time source: now is injectable so tests drive fake clocks; the
// production wiring passes time.Now whose returned values carry a
// monotonic reading. time.Sub uses the monotonic component, so
// wall-clock manipulation (NTP/DST) cannot shorten or extend the
// debounce window (FR-015).
type ratebucket struct {
	mu      sync.Mutex
	window  time.Duration
	entries map[string]bucketState
	now     func() time.Time
}

// bucketState records the last successful delivery and any in-flight
// reservation for a single key. pending is the zero value when no
// reservation is held; both fields are monotonic time.Time values.
type bucketState struct {
	delivered time.Time
	pending   time.Time
}

// newRatebucket constructs a ratebucket with the supplied window and
// clock. The window MUST be > 0 (NewRouter applies DefaultBucketWindow
// when the caller's value is non-positive).
func newRatebucket(window time.Duration, now func() time.Time) *ratebucket {
	return &ratebucket{
		window:  window,
		entries: make(map[string]bucketState),
		now:     now,
	}
}

// acquire reserves the bucket for key. Returns true if the
// reservation was granted (no pending reservation AND the last
// delivered timestamp is older than window); false if the call must
// be rejected with ErrAlertRateLimited.
//
// On success, the bucketState gains a pending timestamp; the caller
// MUST follow up with commit (after a successful Sender call) or
// refund (after a transport failure or per-pattern denial that
// requires releasing the per-supervisor reservation).
func (b *ratebucket) acquire(key string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.entries[key]
	if !state.pending.IsZero() {
		return false
	}
	now := b.now()
	if !state.delivered.IsZero() && now.Sub(state.delivered) < b.window {
		return false
	}
	state.pending = now
	b.entries[key] = state
	return true
}

// commit promotes a pending reservation to a delivered timestamp.
// Idempotent if no pending reservation exists (the state is left
// unchanged).
func (b *ratebucket) commit(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.entries[key]
	if state.pending.IsZero() {
		return
	}
	state.delivered = state.pending
	state.pending = time.Time{}
	b.entries[key] = state
}

// refund clears a pending reservation without touching delivered.
// Used when the per-pattern acquire denies and we need to release the
// per-supervisor reservation, and when the Sender returns a transport
// error (commit-on-success per FR-012a).
func (b *ratebucket) refund(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.entries[key]
	state.pending = time.Time{}
	b.entries[key] = state
}
