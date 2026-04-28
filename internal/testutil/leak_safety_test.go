package testutil

import (
	"bytes"
	"context"
	"os"
	"testing"
)

// capturedVaultPath is set by TestLeakSafety_AllFixtures_FirstRun and read by
// TestLeakSafety_AllFixtures_SecondRun. Sequential execution of top-level tests
// within a single `go test` process is guaranteed by the testing package.
//
//nolint:gochecknoglobals // test-coordination state: set once, read once, sequential tests only
var capturedVaultPath string

func TestLeakSafety_AllFixtures_FirstRun(t *testing.T) {
	seed := NewTestKeys(t)
	if len(seed) != 64 {
		t.Fatalf("NewTestKeys returned %d bytes, want 64", len(seed))
	}

	path, vaultKey, _ := NewTestVault(t, map[string]string{"k": "v"})
	capturedVaultPath = path

	sentinel := SentinelSecret(0)
	AssertSentinelAbsent(t, sentinel, "clean haystack")

	stub := NewDiscordStub(t)
	stub.ApproveAll = true
	if _, err := stub.RequestApproval(context.Background(), ApprovalRequest{RequesterHost: "h"}); err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}

	// vaultKey is live before cleanup
	if vaultKey.Len() == 0 {
		t.Error("vaultKey should be live before test exits")
	}
}

func TestLeakSafety_AllFixtures_SecondRun(t *testing.T) {
	// The temp dir from the first test should be gone.
	if capturedVaultPath != "" {
		dir := capturedVaultPath
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Errorf("vault path %q should not exist after first test cleanup, got: %v", dir, err)
		}
	}

	// A fresh stub has no state from the previous test.
	stub := NewDiscordStub(t)
	if calls := stub.Calls(); len(calls) != 0 {
		t.Errorf("fresh stub should have 0 calls, got %d", len(calls))
	}

	// The sync.Once-cached seed is intentionally stable across tests (SC-001).
	reference := NewTestKeys(t)
	second := NewTestKeys(t)
	if !bytes.Equal(reference, second) {
		t.Error("NewTestKeys cache not stable across sequential test invocations")
	}
}

func TestLeakSafety_NoExplicitCleanupRequired(t *testing.T) {
	_, vaultKey, _ := NewTestVault(t, map[string]string{"k": "v"})
	stub := NewDiscordStub(t)
	stub.ApproveAll = true

	// Don't invoke the returned cleanup — t.Cleanup should handle it.
	// After this test exits, t.Cleanup zeroes vaultKey.
	// We can only verify this from within the same test (before cleanup fires).
	if vaultKey.Len() == 0 {
		t.Error("vaultKey should be live before auto-cleanup fires")
	}
	_ = stub // prevent unused warning; cleanup will drain it automatically
}
