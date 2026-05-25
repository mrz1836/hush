package sign

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultNonceCacheMaxEntries is the hard upper bound on live entries in the
// production [NonceCache]. Sized for ~80 MB worst-case at ~80 B/entry; orders
// of magnitude above any realistic single-operator Tailscale-perimeter traffic
// rate but low enough that a single misbehaving (or compromised) client
// cannot exhaust the host's memory through the replay cache before the audit
// chain emits a saturation event. Operators who legitimately need higher
// throughput can wire a custom cap via [NewNonceCacheWithCap].
const DefaultNonceCacheMaxEntries = 1_000_000

// NonceCache accepts nonces exactly once within their TTL, defeating replay attacks.
type NonceCache interface {
	// Add stores nonce with the given TTL and returns (true, nil) the first time
	// a nonce is seen. Returns (false, [ErrNonceReplay]) for a duplicate within
	// TTL. Returns (false, [ErrNonceEncoding]) if len(nonce) ∉ [8,128].
	// Returns (false, [ErrNonceTTLInvalid]) if ttl ≤ 0.
	// Returns (false, [ErrNonceCacheFull]) when the cache's max-entries cap is
	// reached AND the candidate nonce is not already present (a replay of an
	// already-cached nonce still resolves as [ErrNonceReplay], regardless of
	// cap state, so attackers cannot use saturation to mask replays).
	// Returns (false, ctx.Err()) if ctx is already canceled.
	Add(ctx context.Context, nonce string, ttl time.Duration) (firstSeen bool, err error)

	// Run sweeps expired entries every sweepInterval until ctx is canceled.
	// The caller MUST invoke this inside their own goroutine: go cache.Run(ctx).
	Run(ctx context.Context)
}

// NewNonceCache returns a [NonceCache] with a 30-second sweep interval and
// the [DefaultNonceCacheMaxEntries] cap. Spawns no goroutines; call Run to
// start the sweep.
func NewNonceCache() NonceCache {
	return NewNonceCacheWithCap(DefaultNonceCacheMaxEntries)
}

// NewNonceCacheWithCap returns a [NonceCache] with a custom hard entry cap.
// A non-positive maxEntries is treated as [DefaultNonceCacheMaxEntries]
// (the API is fail-closed: callers cannot disable the cap).
func NewNonceCacheWithCap(maxEntries int) NonceCache {
	if maxEntries <= 0 {
		maxEntries = DefaultNonceCacheMaxEntries
	}
	return &nonceCache{
		sweepInterval: 30 * time.Second,
		maxEntries:    int64(maxEntries),
	}
}

type nonceCache struct {
	entries       sync.Map
	sweepInterval time.Duration
	maxEntries    int64
	entryCount    atomic.Int64
}

// Add implements [NonceCache.Add]. See the interface doc for the full
// contract.
//
// Ordering is deliberate: the cap check runs AFTER the "is this nonce
// already known?" probe so a replay attempt against an existing entry
// always resolves as [ErrNonceReplay], even when the cache is saturated.
// This prevents an attacker from using cache-full to launder a replay
// into a "different" error code.
func (c *nonceCache) Add(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if l := len(nonce); l < 8 || l > 128 {
		return false, ErrNonceEncoding
	}
	if ttl <= 0 {
		return false, ErrNonceTTLInvalid
	}

	now := nowFn()
	expiry := now.Add(ttl)

	// Fast path for already-known nonces. Must come BEFORE the cap check so
	// replay detection is preserved under saturation.
	if existing, loaded := c.entries.Load(nonce); loaded {
		return c.handleExisting(nonce, existing, now, expiry)
	}

	// Cap gate. Racy by design — two goroutines may both observe
	// count < cap and both insert; the resulting drift is bounded by the
	// number of concurrent inserts (single-digit thousands at most on
	// realistic hardware). Loud-failure sentinel returned ON the rejecting
	// path so the caller's audit fires (Constitution VI).
	if c.entryCount.Load() >= c.maxEntries {
		return false, ErrNonceCacheFull
	}

	actual, loaded := c.entries.LoadOrStore(nonce, expiry)
	if !loaded {
		c.entryCount.Add(1)
		return true, nil
	}
	// Lost the race with another concurrent insert (rare on this path).
	return c.handleExisting(nonce, actual, now, expiry)
}

func (c *nonceCache) Run(ctx context.Context) {
	t := time.NewTicker(c.sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("hush/transport/sign: nonce cache sweep stopped",
				slog.String("reason", ctx.Err().Error()))
			return
		case <-t.C:
			c.sweep()
		}
	}
}

func (c *nonceCache) sweep() {
	now := nowFn()
	c.entries.Range(func(k, v any) bool {
		if expiry, ok := v.(time.Time); ok && now.After(expiry) {
			if c.entries.CompareAndDelete(k, expiry) {
				c.entryCount.Add(-1)
			}
		}
		return true
	})
}

// handleExisting resolves the outcome when [sync.Map.Load] or
// [sync.Map.LoadOrStore] returned an already-present entry. A still-valid
// entry resolves as [ErrNonceReplay]; an expired entry races to CAS-replace
// with the fresh expiry (one goroutine wins, the others see replay).
//
// Count is unchanged on either branch: the replay branch adds no entry, and
// the CAS-replace branch reuses an existing slot.
func (c *nonceCache) handleExisting(nonce string, existing any, now, expiry time.Time) (bool, error) {
	prev, ok := existing.(time.Time)
	if !ok || !now.After(prev) {
		return false, ErrNonceReplay
	}
	if c.entries.CompareAndSwap(nonce, prev, expiry) {
		return true, nil
	}
	return false, ErrNonceReplay
}

// len reports the current live-entry count. Test-only accessor; the public
// API does not expose cache size by design (operators read it from the audit
// stream when the cap fires).
func (c *nonceCache) len() int64 {
	return c.entryCount.Load()
}
