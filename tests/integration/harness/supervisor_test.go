//go:build integration

package harness

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
)

// newSupervisorFixture composes a full server + long-lived-child supervisor
// (no scripted Child → the default `/bin/sh while-true` loop) ready to Run.
func newSupervisorFixture(t *testing.T, name string) *TestSupervisor {
	t.Helper()
	logger := NewLogCapture(t)
	vault := NewVault(t, map[string]string{"ANTHROPIC_API_KEY": testutil.SentinelSecret(0)})
	discord := NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := NewServer(t, ServerOpts{Vault: vault, Logger: logger, Discord: discord})
	return NewSupervisor(t, SupervisorOpts{
		Vault:   vault,
		Server:  srv,
		Discord: discord,
		Logger:  logger,
		Name:    name,
		Scopes:  []string{"ANTHROPIC_API_KEY"},
	})
}

// TestSupervisorLifecycle composes a real supervisor, runs it to
// StateRunning, and exercises the snapshot/audit observability surface.
func TestSupervisorLifecycle(t *testing.T) {
	sup := newSupervisorFixture(t, "lifecycle")

	// StatusRaw before Run: the socket does not exist yet → nil.
	assert.Nil(t, sup.StatusRaw())

	sup.Run()
	sup.Run() // idempotent

	sup.WaitState(t, supervise.StateRunning, 5*time.Second)
	assert.Equal(t, supervise.StateRunning, sup.State())
	assert.Equal(t, supervise.StateRunning, sup.Snapshot().State)

	sup.WaitAudit(t, "supervisor_session_claimed", 5*time.Second)
	assert.True(t, sup.HasAudit("supervisor_session_claimed"))
	assert.False(t, sup.HasAudit("a-bogus-action-never-emitted"))

	assert.NotEmpty(t, sup.AuditPath())
	assert.NotEmpty(t, sup.RawAudit())
	assert.NotEmpty(t, sup.ReadAudit())
	require.NotNil(t, sup.AuditKey())

	// StatusRaw against the live socket.
	_ = sup.StatusRaw()

	sup.AssertAuditChain(t)
	sup.Stop() // idempotent
}

// TestSupervisorTriggerRefresh mirrors Scenario 13: an operator refresh
// keeps the child running and records a silent refill.
func TestSupervisorTriggerRefresh(t *testing.T) {
	sup := newSupervisorFixture(t, "trigger-refresh")
	sup.Run()
	sup.WaitState(t, supervise.StateRunning, 5*time.Second)

	require.NoError(t, sup.TriggerRefresh(context.Background()))
	sup.WaitState(t, supervise.StateRunning, 5*time.Second)
	sup.WaitAudit(t, "supervisor_silent_refill", 5*time.Second)
}

// TestSupervisorTriggerWindowRefresh mirrors Scenario 8: the daytime
// refresh-window claim swap records a session_refreshed event.
func TestSupervisorTriggerWindowRefresh(t *testing.T) {
	sup := newSupervisorFixture(t, "window-refresh")
	sup.Run()
	sup.WaitState(t, supervise.StateRunning, 5*time.Second)

	sup.TriggerWindowRefresh(context.Background())
	sup.WaitAudit(t, "supervisor_session_refreshed", 5*time.Second)
}

// TestSupervisorRefreshViaSocket covers the status-socket Refresh verb,
// including the dial-failure branch before the socket exists.
func TestSupervisorRefreshViaSocket(t *testing.T) {
	sup := newSupervisorFixture(t, "socket-refresh")

	// Before Run the socket is absent → dial error.
	assert.Error(t, sup.Refresh(context.Background()))

	sup.Run()
	sup.WaitState(t, supervise.StateRunning, 5*time.Second)
	// Live socket: the refresh verb is acknowledged.
	assert.NoError(t, sup.Refresh(context.Background()))
}

// TestSupervisorWaitStateTimeout covers the WaitState deadline branch.
func TestSupervisorWaitStateTimeout(t *testing.T) {
	sup := newSupervisorFixture(t, "wait-state-timeout") // never Run → never Running
	expectFatal(t, "state-timeout", func(ft *testing.T) {
		sup.WaitState(ft, supervise.StateRunning, 30*time.Millisecond)
	})
}

// TestSupervisorWaitAuditTimeout covers the WaitAudit deadline branch.
func TestSupervisorWaitAuditTimeout(t *testing.T) {
	sup := newSupervisorFixture(t, "wait-audit-timeout")
	expectFatal(t, "audit-timeout", func(ft *testing.T) {
		sup.WaitAudit(ft, "never-emitted", 30*time.Millisecond)
	})
}

// TestNewSupervisorReloadChildMutualExclusion covers the guard rejecting
// both Reload and Child.
func TestNewSupervisorReloadChildMutualExclusion(t *testing.T) {
	logger := NewLogCapture(t)
	vault := NewVault(t, map[string]string{"ANTHROPIC_API_KEY": testutil.SentinelSecret(0)})
	discord := NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := NewServer(t, ServerOpts{Vault: vault, Logger: logger, Discord: discord})
	child := NewChild(t, logger, ChildOpts{ExitCode: 0, Lifetime: time.Millisecond})

	expectFatal(t, "reload-and-child", func(ft *testing.T) {
		NewSupervisor(ft, SupervisorOpts{
			Vault:   vault,
			Server:  srv,
			Discord: discord,
			Logger:  logger,
			Name:    "mutual-exclusion",
			Scopes:  []string{"ANTHROPIC_API_KEY"},
			Child:   child,
			Reload:  &ReloadOpts{Version: "v0"},
		})
	})
}

// TestNewSupervisorRequiresCoreDeps covers the required-field guard.
func TestNewSupervisorRequiresCoreDeps(t *testing.T) {
	expectFatal(t, "missing-deps", func(ft *testing.T) {
		NewSupervisor(ft, SupervisorOpts{})
	})
}

// TestPidFileAcquireAndCollision covers AcquirePidFile (happy + fatal on
// collision) and TryAcquirePidFile (happy + ErrPidLocked).
func TestPidFileAcquireAndCollision(t *testing.T) {
	dir := testutil.ShortTempDir(t, "hpid-")
	path := dir + "/test.pid"

	pid := AcquirePidFile(t, path)
	require.NotNil(t, pid)

	// A second acquire of the same path fatals (AcquirePidFile path).
	expectFatal(t, "collision-fatal", func(ft *testing.T) {
		_ = AcquirePidFile(ft, path)
	})

	// TryAcquirePidFile returns ErrPidLocked rather than fataling.
	_, err := TryAcquirePidFile(t, path)
	assert.ErrorIs(t, err, supervise.ErrPidLocked)

	// TryAcquirePidFile on a fresh path succeeds.
	pid2, err := TryAcquirePidFile(t, dir+"/fresh.pid")
	require.NoError(t, err)
	require.NotNil(t, pid2)
}

// TestAssertSupervisorState covers the matching and mismatching arms.
func TestAssertSupervisorState(t *testing.T) {
	AssertSupervisorState(t, supervise.StateRunning, supervise.StateRunning)
	failed := runIsolated(func(ft *testing.T) {
		AssertSupervisorState(ft, supervise.StateRunning, supervise.StateAwaitingApproval)
	})
	assert.True(t, failed)
}

// TestAssertAuditSubsequence covers the present and missing arms.
func TestAssertAuditSubsequence(t *testing.T) {
	recorded := []audit.Event{{Action: "a"}, {Action: "x"}, {Action: "b"}, {Action: "c"}}
	AssertAuditSubsequence(t, recorded, []string{"a", "b", "c"})

	failed := runIsolated(func(ft *testing.T) {
		AssertAuditSubsequence(ft, recorded, []string{"a", "z"})
	})
	assert.True(t, failed)
}

// TestAssertAuditChainContinuityNilKeySkips covers the nil-key early return.
func TestAssertAuditChainContinuityNilKeySkips(t *testing.T) {
	assert.NotPanics(t, func() {
		AssertAuditChainContinuity(t, "/nonexistent/path/audit.jsonl", nil)
	})
}

// TestAssertAuditChainContinuityDetectsCorruption covers the verify-error
// arm: a non-chain file under a real key fails verification.
func TestAssertAuditChainContinuityDetectsCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("not-a-valid-audit-chain\n"), 0o600))
	key := NewECDSAKey(t)

	failed := runIsolated(func(ft *testing.T) {
		AssertAuditChainContinuity(ft, path, &key.PublicKey)
	})
	assert.True(t, failed)
}

// TestNewECDSAKey covers the secp256k1 key generation helper.
func TestNewECDSAKey(t *testing.T) {
	k := NewECDSAKey(t)
	require.NotNil(t, k)
	// A usable secp256k1 key encodes to a 66-hex-char compressed form.
	assert.Len(t, compressedPubKeyHex(&k.PublicKey), 66)
}

// TestGoroutineSnapshotAndAssertNoLeak covers the clean and leaking arms.
func TestGoroutineSnapshotAndAssertNoLeak(t *testing.T) {
	pre := GoroutineSnapshot()
	assert.Positive(t, pre)
	// Generous headroom → no leak detected.
	AssertNoLeak(t, pre+50, 5)

	// preCount below the live count → reported as a leak.
	failed := runIsolated(func(ft *testing.T) {
		AssertNoLeak(ft, 0, 3)
	})
	assert.True(t, failed)
}

// TestDefaultHzProbe covers the 2xx, 5xx, dial-error, and build-error arms.
func TestDefaultHzProbe(t *testing.T) {
	srv, _, _ := newServerFixture(t, map[string]string{"K": "v"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 2xx: the live chassis /hz.
	assert.NoError(t, defaultHzProbe(ctx, srv.URL()))

	// 5xx: a validator endpoint that always 500s.
	bad := srv.MockValidator(t, "boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	assert.Error(t, defaultHzProbe(ctx, bad))

	// dial error: nothing listening on port 1.
	assert.Error(t, defaultHzProbe(ctx, "http://127.0.0.1:1"))

	// request-build error: malformed URL.
	assert.Error(t, defaultHzProbe(ctx, "http://%zz"))
}

// TestRandomNonceAndRequestID covers the hex token generators.
func TestRandomNonceAndRequestID(t *testing.T) {
	hexOnly := regexp.MustCompile(`^[0-9a-f]+$`)

	nonce := randomNonce()
	assert.Len(t, nonce, 32) // 16 bytes hex
	assert.Regexp(t, hexOnly, nonce)
	assert.NotEqual(t, randomNonce(), randomNonce())

	reqID := randomRequestID()
	assert.Len(t, reqID, 16) // 8 bytes hex
	assert.Regexp(t, hexOnly, reqID)
}

// TestRealClockNow covers the wall-clock supervise.Clock implementation.
func TestRealClockNow(t *testing.T) {
	before := time.Now()
	got := realClock{}.Now()
	assert.WithinDuration(t, before, got, time.Second)
}

// TestNewFakeClock covers the FakeClock constructor + NowFn wiring.
func TestNewFakeClock(t *testing.T) {
	anchor := time.Date(2026, 6, 7, 9, 30, 0, 0, time.UTC)
	clock := NewFakeClock(anchor)
	assert.Equal(t, anchor.UnixNano(), clock.Now().UnixNano())
	assert.Equal(t, anchor.UnixNano(), clock.NowFn()().UnixNano())
}

// TestJSONUnmarshal covers the decoder wrapper, success and failure.
func TestJSONUnmarshal(t *testing.T) {
	var out struct {
		A int `json:"a"`
	}
	require.NoError(t, jsonUnmarshal([]byte(`{"a":7}`), &out))
	assert.Equal(t, 7, out.A)
	assert.Error(t, jsonUnmarshal([]byte("{not-json"), &out))
}
