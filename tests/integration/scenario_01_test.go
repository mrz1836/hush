//go:build integration

// Scenario 1 — First Interactive Shell Request
// (docs/LIFECYCLE-SCENARIOS.md §1).
//
// A human runs `hush request`: the client derives its machine key and an
// ephemeral ECIES keypair, sends a signed /claim, the approver approves
// once, the server returns a scoped interactive JWT, and the client
// fetches each secret via /s/<name> + ECIES decrypt.
//
// Contracts:
//
//	A — The signed /claim is approved and a JWT is issued.
//	B — Exactly one approval is requested for the session.
//	C — The fetched secret decrypts to the expected plaintext.
//	D — The secret never leaks into logs / audit / alerts; the server
//	    audit chain is hash-linked and signature-valid.
//
// Sentinel: testutil.SentinelSecret(1).
package integration_test

import (
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/tests/integration/harness"
)

func Test_Scenario_01_InteractiveShellRequest(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(1),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	client := harness.NewClient(t, harness.ClientOpts{
		Vault:        vault,
		Server:       srv,
		MachineIndex: 1,
		MachineName:  "interactive-shell",
	})

	ctx := t.Context()

	// Contract A — signed interactive /claim is approved, JWT issued.
	jwt := client.Claim(t, ctx, []string{"ANTHROPIC_API_KEY"}, 5*time.Minute, "interactive")

	// Contract B — exactly one approval for the shell session.
	if calls := discord.Stub().Calls(); len(calls) != 1 {
		t.Errorf("scenario_01: approval requested %d time(s); want exactly 1", len(calls))
	}

	// Contract C — per-scope /s fetch + ECIES decrypt yields the plaintext.
	sb := client.FetchSecret(t, ctx, "ANTHROPIC_API_KEY", jwt)
	defer func() { _ = sb.Destroy() }()
	var got string
	if err := sb.Use(func(b []byte) { got = string(b) }); err != nil {
		t.Fatalf("scenario_01: SecureBytes.Use: %v", err)
	}
	if got != testutil.SentinelSecret(1) {
		t.Errorf("scenario_01: decrypted secret mismatch")
	}

	defer func() {
		if t.Failed() {
			t.Logf("captured logs:\n%s", logger.Bytes())
		}
	}()

	// Contract D — the secret never leaks into the operational streams.
	// The legitimately decrypted SecureBytes above is NOT a stream — only
	// logs, audit, status, and alerts are swept.
	harness.AssertSentinelAbsent(
		t,
		testutil.SentinelSecret(1),
		logger.Bytes(),
		srv.RawAudit(),
		nil, // no supervisor status socket in the interactive flow
		discord.AlertsRaw(),
		nil, nil,
	)
	srv.AssertAuditChain(t)
}
