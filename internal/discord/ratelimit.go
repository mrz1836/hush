package discord

// Sibling at internal/discord/alerts/ratelimit.go shares this shape;
// duplication is intentional because the alerts package is restricted
// to stdlib imports.

import (
	"sync"
	"time"

	"github.com/mrz1836/hush/internal/token"
)

// bucketKey identifies one rate-limit slot. SessionInteractive uses
// SupervisorName == "" and keys solely on ClientIP; SessionSupervisor
// keys on (SupervisorName, ClientIP).
type bucketKey struct {
	SupervisorName string
	ClientIP       string
}

// bucketState tracks the most recent successful delivery and any
// in-flight pending acquire for a given key. Both fields carry Go
// monotonic timestamps and survive wall-clock changes.
type bucketState struct {
	delivered time.Time
	pending   time.Time
}

// acquireResult captures the outcome of a rateBucket.Acquire call.
type acquireResult uint8

const (
	acquireGranted acquireResult = iota
	acquireDenied
)

// rateBucket is a per-key window-based gate. The map mutates under a
// single mutex; the contention model is operator-driven so finer-
// grained locking is unnecessary.
type rateBucket struct {
	mu      sync.Mutex
	window  time.Duration
	entries map[bucketKey]bucketState
}

func newRateBucket(window time.Duration) *rateBucket {
	return &rateBucket{
		window:  window,
		entries: make(map[bucketKey]bucketState),
	}
}

// Acquire takes a pending slot for key when the window has elapsed
// since the last delivery and no concurrent acquire is in flight.
// Callers MUST follow every acquireGranted with exactly one Commit or
// Refund (no leaks).
func (b *rateBucket) Acquire(key bucketKey, now time.Time) acquireResult {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.entries[key]
	if !state.pending.IsZero() {
		return acquireDenied
	}
	if !state.delivered.IsZero() && now.Sub(state.delivered) < b.window {
		return acquireDenied
	}
	state.pending = now
	b.entries[key] = state
	return acquireGranted
}

// Commit promotes the pending slot for key to delivered. It is a
// no-op if no pending slot is held.
func (b *rateBucket) Commit(key bucketKey) {
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

// Refund clears the pending slot without touching delivered.
func (b *rateBucket) Refund(key bucketKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.entries[key]
	state.pending = time.Time{}
	b.entries[key] = state
}

// makeKey derives the bucketKey for a given ApprovalRequest per the
// spec: interactive requests key on ClientIP only; supervisor
// requests key on (SupervisorName, ClientIP).
func makeKey(req ApprovalRequest) bucketKey {
	if req.SessionType == token.SessionSupervisor {
		return bucketKey{SupervisorName: req.SupervisorName, ClientIP: req.ClientIP}
	}
	return bucketKey{SupervisorName: "", ClientIP: req.ClientIP}
}
