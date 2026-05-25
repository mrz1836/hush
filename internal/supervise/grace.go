package supervise

import (
	"sync"
	"time"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// graceMaxWindow is the hard cap on the grace cache TTL. Even when
// the operator configures a larger window, the effective TTL is
// min(window, 4h).
const graceMaxWindow = 4 * time.Hour

// Grace is the per-supervisor cache of last-decrypted *SecureBytes
// keyed by secret name. Lifecycle: NewGrace returns an empty cache;
// Refiller.Refill calls Set after each successful decrypt cycle; the
// orchestrator's restart path calls Get; the `hush client refresh`
// flow calls Evict.
//
// The cache is permanently empty when enabled=false or window<=0.
// Effective TTL is min(window, 4h).
type Grace struct {
	mu      sync.RWMutex
	entries map[string]graceEntry
	enabled bool
	window  time.Duration
	now     func() time.Time
}

type graceEntry struct {
	sb      *securebytes.SecureBytes
	expires time.Time
}

// NewGrace constructs a Grace cache. The window argument is hard-
// capped at 4 hours. Disabled mode (enabled=false) and
// zero-or-negative window both produce a permanently-empty cache.
//
// NewGrace owns no goroutines. Expired entries are destroyed lazily
// on the next Get call.
func NewGrace(window time.Duration, enabled bool) *Grace {
	if window > graceMaxWindow {
		window = graceMaxWindow
	}
	return &Grace{
		entries: make(map[string]graceEntry),
		enabled: enabled,
		window:  window,
		now:     time.Now,
	}
}

// Get returns the cached *SecureBytes for name. Returns (nil, false)
// when the entry is absent, expired, or when the cache is disabled.
// On expiry, Get atomically destroys the entry's *SecureBytes and
// removes the map slot before returning (lazy-evict).
//
// The returned *SecureBytes pointer is borrow-only — callers MUST NOT
// call Destroy on it. Grace retains ownership until the next Set,
// Evict, or expiry-on-Get.
func (g *Grace) Get(name string) (*securebytes.SecureBytes, bool) {
	g.mu.RLock()
	if !g.enabled || g.window == 0 {
		g.mu.RUnlock()
		return nil, false
	}
	entry, ok := g.entries[name]
	if !ok {
		g.mu.RUnlock()
		return nil, false
	}
	if !entry.expires.After(g.now()) {
		g.mu.RUnlock()
		// Lazy-evict: re-acquire write lock, re-check (LRU race), evict.
		g.mu.Lock()
		defer g.mu.Unlock()
		if cur, stillPresent := g.entries[name]; stillPresent && !cur.expires.After(g.now()) {
			_ = cur.sb.Destroy()
			delete(g.entries, name)
		}
		return nil, false
	}
	sb := entry.sb
	g.mu.RUnlock()
	return sb, true
}

// Set records (name, value) with expiry = now() + window. On
// overwrite, the prior entry's *SecureBytes is destroyed first.
// When the cache is disabled or window<=0, Set is a silent no-op and
// ownership of value remains with the caller.
func (g *Grace) Set(name string, value *securebytes.SecureBytes) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.enabled || g.window <= 0 {
		return
	}
	if prev, ok := g.entries[name]; ok {
		_ = prev.sb.Destroy()
	}
	g.entries[name] = graceEntry{sb: value, expires: g.now().Add(g.window)}
}

// Evict destroys the entry for name (if present) and removes the
// map slot. Calling Evict for an absent name is a silent no-op.
func (g *Grace) Evict(name string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if entry, ok := g.entries[name]; ok {
		_ = entry.sb.Destroy()
		delete(g.entries, name)
	}
}

// EvictAll destroys every cached SecureBytes and clears the map,
// preserving the enabled/window configuration so subsequent Set calls
// continue to cache. Used by the orchestrator when an authoritative
// rejection (e.g. vault returns unknown_jti) invalidates every cached
// plaintext from the now-revoked session — falling back to that
// plaintext would silently bypass the operator's revoke decision.
//
// Distinct from Destroy, which both zeroes the entries AND permanently
// disables further caching (process-exit semantics).
//
// Safe to call concurrently with Get/Set/Evict — all take g.mu.
func (g *Grace) EvictAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for name, entry := range g.entries {
		_ = entry.sb.Destroy()
		delete(g.entries, name)
	}
}

// Destroy zeroes every cached SecureBytes and clears the map. Invoked by
// Lifecycle.runShutdown so the supervisor's SIGTERM path explicitly retires
// plaintext that would otherwise outlive the orchestrator (the runtime
// finalizer does NOT run on process exit, and pure kernel page reclamation
// is not the explicit-zeroing discipline Principle VI mandates).
//
// Destroy is idempotent; calling it a second time is a silent no-op.
// Safe to call concurrently with Get/Set/Evict — they all take g.mu, and
// post-Destroy Get returns (nil, false), Set is a no-op, and Evict has no
// entries to act on.
func (g *Grace) Destroy() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for name, entry := range g.entries {
		_ = entry.sb.Destroy()
		delete(g.entries, name)
	}
	// Mark cache permanently empty so any further Set after Destroy is a
	// no-op (mirrors the disabled-mode contract).
	g.enabled = false
}

// Enabled reports whether the cache will actually retain entries —
// true only when retention was enabled at construction AND the
// effective window is positive. When false, Set is a no-op and the
// caller retains ownership of any value it would have cached.
func (g *Grace) Enabled() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.enabled && g.window > 0
}

// setClockForTest replaces the clock source used for expiry
// computation. The seam is unexported and only available to package-
// internal tests.
func (g *Grace) setClockForTest(now func() time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.now = now
}
