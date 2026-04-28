package sign

import (
	"crypto/ecdsa"
	"crypto/rand"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// setClockForTest replaces the package-private nowFn with a frozen clock and
// restores the original on t.Cleanup.
func setClockForTest(t *testing.T, fixed time.Time) {
	t.Helper()
	prev := nowFn
	nowFn = func() time.Time { return fixed }
	t.Cleanup(func() { nowFn = prev })
}

// advanceClockForTest advances the frozen clock by the given duration.
// setClockForTest must have been called first.
func advanceClockForTest(t *testing.T, by time.Duration) {
	t.Helper()
	current := nowFn()
	advanced := current.Add(by)
	prev := nowFn
	nowFn = func() time.Time { return advanced }
	t.Cleanup(func() { nowFn = prev })
}

// generateFuzzKey returns a fresh secp256k1 ECDSA private key for use in tests.
func generateFuzzKey(tb testing.TB) *ecdsa.PrivateKey {
	tb.Helper()
	key, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck // secp256k1 not in crypto/ecdh; S256() is the correct curve accessor
	if err != nil {
		tb.Fatalf("generateFuzzKey: %v", err)
	}
	return key
}

// newNonceCacheForTest returns a nonceCache with a configurable sweep interval
// for lifecycle and sweep tests.
func newNonceCacheForTest(sweepInterval time.Duration) *nonceCache {
	return &nonceCache{sweepInterval: sweepInterval}
}
