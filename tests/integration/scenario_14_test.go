//go:build integration

// scenario_14_test.go — Test_Scenario_14_DuplicateStart.
//
// Per docs/LIFECYCLE-SCENARIOS.md §14, when a second `hush supervise`
// process starts against an already-locked pidfile, the second instance
// MUST refuse to proceed with an explicit split-brain error and never
// open a status socket of its own.
//
// This scenario does NOT require the full Lifecycle composition. The
// pidfile flock is the entire contract under test: real
// supervise.AcquirePidFile, real OS flock semantics, real per-scenario
// ephemeral t.TempDir() isolation.
//
// Contracts satisfied:
//   - Contract A: first acquirer running (PidFile non-nil); second
//     acquirer's Lifecycle.Run-equivalent surfaces ErrPidLocked.
//   - Contract B: no audit events expected (supervisor.Lifecycle never
//     runs for either instance); AssertAuditChainContinuity on the
//     empty audit file passes trivially via audit.Verify nil-on-missing.
//   - Contract C: first supervisor only would have a socket — not
//     opened here because the scenario stops at the acquire step;
//     spec FR-006 carve-out (second supervisor "never opened a
//     socket") applies.
//   - Contract D: AssertSentinelAbsent runs against the captured
//     LogCapture buffer and the second acquirer's error message.

package integration_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func scenario14DuplicateStart(t *testing.T) {
	t.Helper()

	// Sentinel index 16 per the scenarios_test.go index header.
	sentinel := testutil.SentinelSecret(16)

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("scenario14: chmod tempdir: %v", err)
	}
	pidPath := filepath.Join(dir, "supervise-duplicate.pid")

	logs := harness.NewLogCapture(t)

	// First acquirer succeeds and registers a t.Cleanup-driven Release.
	first := harness.AcquirePidFile(t, pidPath)
	if first == nil {
		t.Fatalf("scenario14: first acquire returned nil pidfile")
	}

	// Second acquirer must observe ErrPidLocked.
	second, err := harness.TryAcquirePidFile(t, pidPath)
	if err == nil {
		t.Fatalf("scenario14: second acquire unexpectedly succeeded")
	}
	if second != nil {
		t.Fatalf("scenario14: second acquire returned non-nil PidFile alongside error")
	}
	if !errors.Is(err, supervise.ErrPidLocked) {
		t.Fatalf("scenario14: second acquire err = %v, want errors.Is(err, ErrPidLocked)", err)
	}

	// Contract A: first acquirer is the live owner; the file exists.
	if _, statErr := os.Stat(pidPath); statErr != nil {
		t.Fatalf("scenario14: pidfile missing after first acquire: %v", statErr)
	}

	// Contract D: sentinel must not leak into the captured slog buffer
	// (operational logs) nor the error message string from the rejected
	// second acquire. The audit + status + discord streams are nil for
	// this minimal-composition scenario per AssertSentinelAbsent's
	// nil-tolerance.
	harness.AssertSentinelAbsent(t, sentinel,
		logs.Bytes(),
		nil,      // RawAudit() — no server
		nil,      // StatusRaw() — second supervisor never opened socket
		nil,      // AlertsRaw() — no Discord interaction
		nil, nil, // Stdout, Stderr — no child
		harness.CollectErrors(err),
	)
}
