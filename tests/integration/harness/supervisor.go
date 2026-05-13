//go:build integration

package harness

import (
	"crypto/ecdsa"
	"crypto/rand"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/supervise"
)

// FakeClock implements supervise.Clock with an injectable time the
// scenario advances via Advance / SetTo. Used by Scenarios 8 / 9 / 11
// to drive documented transitions without time.Sleep.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock builds a FakeClock anchored at the supplied instant.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: t}
}

// Now implements supervise.Clock.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the clock forward by d.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// SetTo pins the clock to t.
func (f *FakeClock) SetTo(t time.Time) {
	f.mu.Lock()
	f.now = t
	f.mu.Unlock()
}

// NowFn returns a time.Time-valued closure backed by this FakeClock —
// suitable for supervise.Deps.NowFn.
func (f *FakeClock) NowFn() func() time.Time {
	return f.Now
}

// TestSupervisor is the per-scenario composition of the real
// *supervise.Lifecycle. The full composition (real Deps wired against a
// real internal/server URL, FakeClock-driven refresh / boot-retry, status
// socket reader, audit subsequence helper, goroutine-leak detector) is
// tracked under task T029 and remains outstanding in this SDD-25 chunk.
//
// The placeholder struct intentionally has no fields — chunk-2 will
// populate it with the real Deps and the Lifecycle handle. Scenarios
// that require the full composition currently mark themselves as
// pending-harness rather than silently passing.
type TestSupervisor struct{}

// SupervisorOpts is the placeholder option bag for future scenario
// wiring. Fields populate as chunk-2 implements scenarios 2/3/4/etc.
type SupervisorOpts struct {
	Vault   *TestVault
	Server  *TestServer
	Discord *TestDiscord
	Logger  *LogCapture
}

// AcquirePidFile is a thin pass-through to supervise.AcquirePidFile
// scenarios can call directly when they only need the pidfile collision
// path (e.g., Scenario 14). t.Cleanup registers Release.
func AcquirePidFile(t *testing.T, path string) *supervise.PidFile {
	t.Helper()
	pid, err := supervise.AcquirePidFile(path)
	if err != nil {
		t.Fatalf("harness.AcquirePidFile(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = pid.Release() })
	return pid
}

// TryAcquirePidFile is the non-fatal sibling of AcquirePidFile: it
// returns the error verbatim instead of t.Fatal-ing. Used by Scenario 14
// to assert ErrPidLocked from a second acquirer.
func TryAcquirePidFile(t *testing.T, path string) (*supervise.PidFile, error) {
	t.Helper()
	pid, err := supervise.AcquirePidFile(path)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = pid.Release() })
	return pid, nil
}

// AssertSupervisorState compares actual against expected and reports a
// labeled failure if they differ.
func AssertSupervisorState(t *testing.T, actual, expected supervise.State) {
	t.Helper()
	if actual != expected {
		t.Errorf("harness.AssertSupervisorState: got %q, want %q", actual, expected)
	}
}

// AssertAuditSubsequence walks recorded left-to-right with a pointer
// into documented. For each recorded[i] whose Action matches
// documented[ptr], ptr advances. At end, asserts ptr == len(documented)
// (research.md §3 — classic subsequence; tolerates intervening
// unmentioned events per spec Clarification 1).
func AssertAuditSubsequence(t *testing.T, recorded []audit.Event, documented []string) {
	t.Helper()
	ptr := 0
	for _, ev := range recorded {
		if ptr >= len(documented) {
			break
		}
		if ev.Action == documented[ptr] {
			ptr++
		}
	}
	if ptr != len(documented) {
		actions := make([]string, 0, len(recorded))
		for _, ev := range recorded {
			actions = append(actions, ev.Action)
		}
		t.Errorf("harness.AssertAuditSubsequence: missing %v; recorded=%v", documented[ptr:], actions)
	}
}

// AssertAuditChainContinuity calls audit.Verify on the on-disk chain
// file. Wraps the failure with the scenario name on test fail.
func AssertAuditChainContinuity(t *testing.T, auditPath string, verifyKey *ecdsa.PublicKey) {
	t.Helper()
	if err := audit.Verify(auditPath, verifyKey); err != nil {
		t.Errorf("harness.AssertAuditChainContinuity(%s): %v", auditPath, err)
	}
}

// NewECDSAKey returns a fresh secp256k1 ECDSA private key suitable for
// claim signing OR ECIES decryption.
func NewECDSAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	curve := secp256k1.S256() //nolint:staticcheck // secp256k1 not in crypto/ecdh
	priv, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("harness.NewECDSAKey: %v", err)
	}
	return priv
}

// GoroutineSnapshot returns runtime.NumGoroutine at call time. Pair with
// AssertNoLeak at scenario end to confirm no harness goroutine outlived
// the scenario body.
func GoroutineSnapshot() int { return runtime.NumGoroutine() }

// AssertNoLeak polls runtime.NumGoroutine up to maxIters times,
// yielding via runtime.Gosched between iterations, and reports a
// failure if the post-count exceeds preCount. Bounded — never sleeps.
func AssertNoLeak(t *testing.T, preCount, maxIters int) {
	t.Helper()
	for i := 0; i < maxIters; i++ {
		if runtime.NumGoroutine() <= preCount {
			return
		}
		runtime.Gosched()
	}
	t.Errorf("harness.AssertNoLeak: goroutine leak: pre=%d post=%d", preCount, runtime.NumGoroutine())
}
