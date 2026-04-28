package sign

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// NonceCache accepts nonces exactly once within their TTL, defeating replay attacks.
type NonceCache interface {
	// Add stores nonce with the given TTL and returns (true, nil) the first time
	// a nonce is seen. Returns (false, [ErrNonceReplay]) for a duplicate within
	// TTL. Returns (false, [ErrNonceEncoding]) if len(nonce) ∉ [8,128].
	// Returns (false, [ErrNonceTTLInvalid]) if ttl ≤ 0.
	// Returns (false, ctx.Err()) if ctx is already canceled.
	Add(ctx context.Context, nonce string, ttl time.Duration) (firstSeen bool, err error)

	// Run sweeps expired entries every sweepInterval until ctx is canceled.
	// The caller MUST invoke this inside their own goroutine: go cache.Run(ctx).
	Run(ctx context.Context)
}

// NewNonceCache returns a NonceCache with a 30-second sweep interval.
// Spawns no goroutines; call Run to start the sweep.
func NewNonceCache() NonceCache {
	return &nonceCache{sweepInterval: 30 * time.Second}
}

type nonceCache struct {
	entries       sync.Map
	sweepInterval time.Duration
}

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

	actual, loaded := c.entries.LoadOrStore(nonce, expiry)
	if !loaded {
		return true, nil
	}

	existing, ok := actual.(time.Time)
	if !ok || !now.After(existing) {
		return false, ErrNonceReplay
	}
	// Entry exists but is expired; race to replace it.
	if c.entries.CompareAndSwap(nonce, existing, expiry) {
		return true, nil
	}
	return false, ErrNonceReplay
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
			c.entries.CompareAndDelete(k, expiry)
		}
		return true
	})
}
