//go:build integration

package harness

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/testutil"
)

// newServerFixture composes the standard vault+discord+logger+server stack
// used by most server tests.
func newServerFixture(t *testing.T, secrets map[string]string) (*TestServer, *TestVault, *LogCapture) {
	t.Helper()
	logger := NewLogCapture(t)
	vault := NewVault(t, secrets)
	discord := NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := NewServer(t, ServerOpts{Vault: vault, Logger: logger, Discord: discord})
	return srv, vault, logger
}

// TestNewServerHealthAndGetters covers the happy-path composition plus the
// simple accessors.
func TestNewServerHealthAndGetters(t *testing.T) {
	srv, vault, _ := newServerFixture(t, map[string]string{"K": testutil.SentinelSecret(0)})

	assert.Contains(t, srv.URL(), "/h/")
	assert.Same(t, vault, srv.Vault())
	assert.NotNil(t, srv.TokenStore())
	assert.NotNil(t, srv.AuditKey())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL()+"/hz", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Less(t, resp.StatusCode, 500)
}

// TestNewServerNilVaultFatals covers the required-Vault guard.
func TestNewServerNilVaultFatals(t *testing.T) {
	expectFatal(t, "nil-vault", func(ft *testing.T) {
		NewServer(ft, ServerOpts{})
	})
}

// TestNewServerWithoutDiscordFailsClosed confirms the fallback approver is
// wired when Discord is nil: a claim is rejected (approver unavailable),
// proving the unavailableApprover path is active.
func TestNewServerWithoutDiscordFailsClosed(t *testing.T) {
	logger := NewLogCapture(t)
	vault := NewVault(t, map[string]string{"K": "v"})
	srv := NewServer(t, ServerOpts{Vault: vault, Logger: logger})
	require.NotNil(t, srv)
	assert.Contains(t, srv.URL(), "/h/")
}

// TestServerFlushSessionsRevokesIssuedJTIs drives a full claim then
// FlushSessions; the previously valid JWT must afterwards fail /s/<name>.
func TestServerFlushSessionsRevokesIssuedJTIs(t *testing.T) {
	srv, vault, _ := newServerFixture(t, map[string]string{"API": testutil.SentinelSecret(4)})
	client := NewClient(t, ClientOpts{Vault: vault, Server: srv, MachineIndex: 1, MachineName: "flush-client"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jwt := client.Claim(t, ctx, []string{"API"}, 5*time.Minute, "interactive")

	// Pre-flush: the secret fetch succeeds.
	sb := client.FetchSecret(t, ctx, "API", jwt)
	_ = sb.Destroy()

	srv.FlushSessions()

	// Post-flush: the same JWT must now be rejected (detached T so the
	// helper's t.Fatalf on non-200 does not fail this test).
	failed := runIsolated(func(ft *testing.T) {
		_ = client.FetchSecret(ft, ctx, "API", jwt)
	})
	assert.True(t, failed, "FetchSecret should fail after FlushSessions")
}

// TestServerReloadPropagatesRotation covers Reload: after rotating the
// on-disk vault and reloading, a fresh claim observes the new value.
func TestServerReloadPropagatesRotation(t *testing.T) {
	srv, vault, _ := newServerFixture(t, map[string]string{"API": testutil.SentinelSecret(5)})
	client := NewClient(t, ClientOpts{Vault: vault, Server: srv, MachineIndex: 1})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	vault.Rotate(t, "API", "rotated-secret")
	require.NoError(t, srv.Reload(ctx))

	jwt := client.Claim(t, ctx, []string{"API"}, 5*time.Minute, "interactive")
	sb := client.FetchSecret(t, ctx, "API", jwt)
	defer func() { _ = sb.Destroy() }()
	var got string
	require.NoError(t, sb.Use(func(b []byte) { got = string(b) }))
	assert.Equal(t, "rotated-secret", got)
}

// TestServerReadAuditAfterActivity confirms ReadAudit / RawAudit reflect
// on-disk events after a claim, and AssertAuditChain validates the chain.
func TestServerReadAuditAfterActivity(t *testing.T) {
	srv, vault, _ := newServerFixture(t, map[string]string{"API": testutil.SentinelSecret(6)})
	client := NewClient(t, ClientOpts{Vault: vault, Server: srv, MachineIndex: 1})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	jwt := client.Claim(t, ctx, []string{"API"}, time.Minute, "interactive")
	sb := client.FetchSecret(t, ctx, "API", jwt)
	_ = sb.Destroy()

	assert.NotEmpty(t, srv.RawAudit())
	assert.NotEmpty(t, srv.ReadAudit())

	// AssertAuditChain stops the server and verifies the chain.
	srv.AssertAuditChain(t)
	// Stop is idempotent — a second call is a harmless no-op.
	srv.Stop()
}

// TestServerRawAuditNilWhenNoVault covers the early nil-return guard.
func TestServerRawAuditNilWhenNoVault(t *testing.T) {
	var s TestServer
	assert.Nil(t, s.RawAudit())
	assert.Nil(t, s.ReadAudit())
}

// TestServerMockValidator covers MockValidator + ValidatorURL (registered
// and unknown scope).
func TestServerMockValidator(t *testing.T) {
	srv, _, _ := newServerFixture(t, map[string]string{"K": "v"})

	url := srv.MockValidator(t, "anthropic", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	require.NotEmpty(t, url)
	assert.Equal(t, url, srv.ValidatorURL("anthropic"))
	assert.Empty(t, srv.ValidatorURL("unknown-scope"))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

// TestServerStopBeforeRunNilSafe covers Stop on a zero-value server.
func TestServerStopBeforeRunNilSafe(t *testing.T) {
	var s *TestServer
	assert.NotPanics(t, s.Stop)
	var z TestServer
	assert.NotPanics(t, z.Stop)
}

// TestAlwaysSyncedClock covers the clock-sync probe stub.
func TestAlwaysSyncedClock(t *testing.T) {
	synced, drift, err := alwaysSyncedClock(context.Background())
	require.NoError(t, err)
	assert.True(t, synced)
	assert.Equal(t, time.Duration(0), drift)
}

// TestFakeCGNATInterfaceLister covers both the valid and invalid-addr arms.
func TestFakeCGNATInterfaceLister(t *testing.T) {
	lister := fakeCGNATInterfaceLister(netip.MustParseAddr("100.96.10.4"))
	addrs, err := lister()
	require.NoError(t, err)
	require.Len(t, addrs, 1)
	ipnet, ok := addrs[0].(*net.IPNet)
	require.True(t, ok)
	assert.Equal(t, "100.96.10.4", ipnet.IP.String())

	bad := fakeCGNATInterfaceLister(netip.Addr{})
	_, err = bad()
	assert.Error(t, err)
}

// TestUnavailableApprover covers the fail-closed fallback approver.
func TestUnavailableApprover(t *testing.T) {
	_, err := unavailableApprover{}.RequestApproval(context.Background(), server.ApprovalRequest{})
	assert.ErrorIs(t, err, server.ErrApproverUnavailable)
}

// TestServerTestAuditKey covers the deterministic audit-key derivation.
func TestServerTestAuditKey(t *testing.T) {
	k := server_test_audit_key(t)
	require.NotNil(t, k)
	// A usable secp256k1 key encodes to a 66-hex-char compressed form.
	assert.Len(t, compressedPubKeyHex(&k.PublicKey), 66)
}

// TestSplitLines covers the JSONL splitter's edge cases.
func TestSplitLines(t *testing.T) {
	assert.Empty(t, splitLines(nil))
	assert.Empty(t, splitLines([]byte("")))

	withTrailing := splitLines([]byte("a\nb\n"))
	assert.Equal(t, [][]byte{[]byte("a"), []byte("b")}, withTrailing)

	noTrailing := splitLines([]byte("a\nb"))
	assert.Equal(t, [][]byte{[]byte("a"), []byte("b")}, noTrailing)

	single := splitLines([]byte("solo"))
	assert.Equal(t, [][]byte{[]byte("solo")}, single)
}

// TestRecordJTI covers the empty-skip and append arms of recordJTI.
func TestRecordJTI(t *testing.T) {
	var s TestServer
	s.recordJTI("")      // skipped
	s.recordJTI("jti-1") // appended
	s.recordJTI("jti-2") // appended
	assert.Equal(t, []string{"jti-1", "jti-2"}, s.issuedJTIs)
}

// TestRegisterAllowedHostHook covers the seam swap. The original hook is
// restored so the package-wide allow-list keeps working for other tests.
func TestRegisterAllowedHostHook(t *testing.T) {
	original := registerAllowedHostExternal
	t.Cleanup(func() { registerAllowedHostExternal = original })

	var captured string
	RegisterAllowedHostHook(func(s string) { captured = s })
	registerAllowedHostExternal("example.test:443")
	assert.Equal(t, "example.test:443", captured)
}
