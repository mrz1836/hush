//go:build integration

package harness

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
)

// TestClientClaimAndFetch is the end-to-end happy path mirroring Scenario 1:
// a signed /claim yields a JWT, and /s/<name> decrypts to the sentinel.
func TestClientClaimAndFetch(t *testing.T) {
	srv, vault, logger := newServerFixture(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(1),
	})
	client := NewClient(t, ClientOpts{
		Vault:        vault,
		Server:       srv,
		MachineIndex: 1,
		MachineName:  "interactive-shell",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	jwt := client.Claim(t, ctx, []string{"ANTHROPIC_API_KEY"}, 5*time.Minute, "interactive")
	require.NotEmpty(t, jwt)

	sb := client.FetchSecret(t, ctx, "ANTHROPIC_API_KEY", jwt)
	defer func() { _ = sb.Destroy() }()
	var got string
	require.NoError(t, sb.Use(func(b []byte) { got = string(b) }))
	assert.Equal(t, testutil.SentinelSecret(1), got)

	// The sentinel must never appear in logs or the audit stream.
	AssertSentinelAbsent(t, testutil.SentinelSecret(1), logger.Bytes(), srv.RawAudit())
}

// TestNewClientDefaultsMachineName confirms the empty-name default path.
func TestNewClientDefaultsMachineName(t *testing.T) {
	srv, vault, _ := newServerFixture(t, map[string]string{"K": "v"})
	client := NewClient(t, ClientOpts{Vault: vault, Server: srv})
	assert.Equal(t, "interactive-client", client.machineName)
}

// TestNewClientRequiresVaultAndServer covers both required-field guards.
func TestNewClientRequiresVaultAndServer(t *testing.T) {
	srv, vault, _ := newServerFixture(t, map[string]string{"K": "v"})
	expectFatal(t, "nil-vault", func(ft *testing.T) {
		NewClient(ft, ClientOpts{Server: srv})
	})
	expectFatal(t, "nil-server", func(ft *testing.T) {
		NewClient(ft, ClientOpts{Vault: vault})
	})
}

// TestClientFetchSecretRejectsUnknownScope covers FetchSecret's non-200
// branch: a JWT scoped to one secret cannot fetch another.
func TestClientFetchSecretRejectsUnknownScope(t *testing.T) {
	srv, vault, _ := newServerFixture(t, map[string]string{
		"ALLOWED": testutil.SentinelSecret(2),
		"DENIED":  testutil.SentinelSecret(3),
	})
	client := NewClient(t, ClientOpts{Vault: vault, Server: srv, MachineIndex: 1})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jwt := client.Claim(t, ctx, []string{"ALLOWED"}, time.Minute, "interactive")

	failed := runIsolated(func(ft *testing.T) {
		_ = client.FetchSecret(ft, ctx, "DENIED", jwt)
	})
	assert.True(t, failed, "FetchSecret for an out-of-scope secret should fail")
}

// TestRandomToken verifies length and base64url charset for several sizes.
func TestRandomToken(t *testing.T) {
	charset := regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	for _, n := range []int{16, 32, 43, 64} {
		tok := randomToken(n)
		assert.Len(t, tok, n)
		assert.Regexp(t, charset, tok)
	}
	// Two draws are overwhelmingly unlikely to collide.
	assert.NotEqual(t, randomToken(32), randomToken(32))
}
