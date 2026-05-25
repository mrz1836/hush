package config

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- listen_addr family (US3) -----------------------------------------------

func TestServer_RejectsLoopback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/loopback.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrListenLoopback)
	require.ErrorIs(t, err, ErrTailscaleBindRequired)
}

func TestServer_RejectsUnspecified(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/unspecified.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrListenUnspecified)
	require.ErrorIs(t, err, ErrTailscaleBindRequired)
}

func TestServer_RejectsPublic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/public.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrListenPublic)
	require.ErrorIs(t, err, ErrTailscaleBindRequired)
}

func TestServer_RejectsMalformedListenAddr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/malformed-listen.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrListenMalformed)
}

func TestServer_RejectsMissingListenAddr(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/missing-listen-addr.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrMissingRequiredField)
}

func TestServer_AcceptsTailscaleCGNAT(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		listenAddr string
	}{
		{"mid-range CGNAT", "100.96.10.4:7743"},
		{"lower boundary", "100.64.0.1:7743"},
		{"upper boundary", "100.127.255.254:7743"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.Chmod(dir, 0o700))
			content := "[server]\n" +
				"listen_addr = \"" + tc.listenAddr + "\"\n" +
				"path_prefix = \"a8k2f9\"\n" +
				"state_dir = \"" + dir + "\"\n" +
				"audit_log = \"" + dir + "/audit.jsonl\"\n" +
				"discord_owner_id = \"123456789012345678\"\n" +
				"\n[discord]\n" +
				"bot_token_keychain_item = \"hush-discord\"\n" +
				"application_id = \"345678901234567890\"\n"
			cfg := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(cfg, []byte(content), 0o600))
			s, err := LoadServer(context.Background(), cfg)
			require.NoError(t, err)
			require.NotNil(t, s)
		})
	}
}

func TestServer_RejectsRequireTailscaleFalse(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/tailscale-required.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrTailscaleRequired)
}

func TestServer_AcceptsRequireTailscaleTrue(t *testing.T) {
	t.Parallel()
	// Explicit true — loads cleanly.
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	content := "[server]\n" +
		"listen_addr = \"100.96.10.4:7743\"\n" +
		"path_prefix = \"a8k2f9\"\n" +
		"state_dir = \"" + dir + "\"\n" +
		"audit_log = \"" + dir + "/audit.jsonl\"\n" +
		"discord_owner_id = \"123456789012345678\"\n" +
		"\n[discord]\n" +
		"bot_token_keychain_item = \"hush-discord\"\n" +
		"application_id = \"345678901234567890\"\n" +
		"\n[network]\n" +
		"require_tailscale = true\n"
	cfg := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0o600))
	s, err := LoadServer(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, s)
}

// ---- health_bind family (US3) -----------------------------------------------

func TestServer_HealthBindRejectsLoopback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/health-bind-loopback.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrListenLoopback)
	require.ErrorIs(t, err, ErrTailscaleBindRequired)
}

func TestServer_HealthBindRejectsPublic(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/health-bind-public.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrListenPublic)
}

func TestServer_HealthBindRejectsMalformed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/health-bind-malformed.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrListenMalformed)
}

// ---- path_prefix (US2) -------------------------------------------------------

func TestServer_RejectsPathPrefixTooShort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/path-prefix-too-short.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrPathPrefixInvalid)
}

func TestServer_RejectsPathPrefixTooLong(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/path-prefix-too-long.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrPathPrefixInvalid)
}

func TestServer_RejectsPathPrefixBadCharset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/path-prefix-bad-charset.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrPathPrefixInvalid)
}

func TestServer_AcceptsValidPathPrefix(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"a8k2f9", "valid_prefix-1", "UPPER-lower_123456"} {
		t.Run(prefix, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			require.NoError(t, os.Chmod(dir, 0o700))
			content := "[server]\n" +
				"listen_addr = \"100.96.10.4:7743\"\n" +
				"path_prefix = \"" + prefix + "\"\n" +
				"state_dir = \"" + dir + "\"\n" +
				"audit_log = \"" + dir + "/audit.jsonl\"\n" +
				"discord_owner_id = \"123456789012345678\"\n" +
				"\n[discord]\n" +
				"bot_token_keychain_item = \"hush-discord\"\n" +
				"application_id = \"345678901234567890\"\n"
			cfg := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(cfg, []byte(content), 0o600))
			s, err := LoadServer(context.Background(), cfg)
			require.NoError(t, err)
			require.NotNil(t, s)
		})
	}
}

// ---- Argon2id floors (US4) --------------------------------------------------

func TestServer_RejectsArgonMemoryUnder256(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/argon-memory-low.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrArgonMemoryTooLow)
}

func TestServer_AcceptsArgonMemoryAt256(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	content := "[server]\n" +
		"listen_addr = \"100.96.10.4:7743\"\n" +
		"path_prefix = \"a8k2f9\"\n" +
		"state_dir = \"" + dir + "\"\n" +
		"audit_log = \"" + dir + "/audit.jsonl\"\n" +
		"discord_owner_id = \"123456789012345678\"\n" +
		"\n[discord]\n" +
		"bot_token_keychain_item = \"hush-discord\"\n" +
		"application_id = \"345678901234567890\"\n" +
		"\n[crypto]\n" +
		"argon_memory_mb = 256\n"
	cfg := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0o600))
	s, err := LoadServer(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Equal(t, uint32(256), s.Crypto.ArgonMemoryMB)
}

func TestServer_DefaultsArgonMemoryTo256(t *testing.T) {
	t.Parallel()
	s, err := loadWithStateDir(t, "testdata/valid/minimal-valid.toml")
	require.NoError(t, err)
	assert.Equal(t, uint32(256), s.Crypto.ArgonMemoryMB)
}

func TestServer_RejectsArgonTimeUnder4(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/argon-time-low.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrArgonTimeTooLow)
}

func TestServer_RejectsArgonThreadsUnder4(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/argon-threads-low.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrArgonThreadsTooLow)
}

// ---- Argon2id ceilings (H3) -------------------------------------------------

// argonCfgWithOverrides materializes a valid minimal config with the given
// argon_* overrides written into the [crypto] table.
func argonCfgWithOverrides(t *testing.T, overrides string) (*Server, error) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	content := "[server]\n" +
		"listen_addr = \"100.96.10.4:7743\"\n" +
		"path_prefix = \"a8k2f9\"\n" +
		"state_dir = \"" + dir + "\"\n" +
		"audit_log = \"" + dir + "/audit.jsonl\"\n" +
		"discord_owner_id = \"123456789012345678\"\n" +
		"\n[discord]\n" +
		"bot_token_keychain_item = \"hush-discord\"\n" +
		"application_id = \"345678901234567890\"\n" +
		"\n[crypto]\n" + overrides
	cfg := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0o600))
	return LoadServer(context.Background(), cfg)
}

func TestServer_RejectsArgonMemoryAboveCeiling(t *testing.T) {
	t.Parallel()
	s, err := argonCfgWithOverrides(t, "argon_memory_mb = 8192\n")
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrArgonMemoryTooHigh)
}

func TestServer_RejectsArgonTimeAboveCeiling(t *testing.T) {
	t.Parallel()
	s, err := argonCfgWithOverrides(t, "argon_time = 32\n")
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrArgonTimeTooHigh)
}

func TestServer_RejectsArgonThreadsAboveCeiling(t *testing.T) {
	t.Parallel()
	s, err := argonCfgWithOverrides(t, "argon_threads = 200\n")
	require.Nil(t, s)
	require.ErrorIs(t, err, ErrArgonThreadsTooHigh)
}

func TestServer_AcceptsArgonAtCeilings(t *testing.T) {
	t.Parallel()
	s, err := argonCfgWithOverrides(t, "argon_memory_mb = 4096\nargon_time = 16\nargon_threads = 128\n")
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Equal(t, uint32(4096), s.Crypto.ArgonMemoryMB)
	assert.Equal(t, uint32(16), s.Crypto.ArgonTime)
	assert.Equal(t, uint8(128), s.Crypto.ArgonThreads)
}

// ---- max_supervisor_ttl bounds (US4) ----------------------------------------

func TestServer_RejectsSupervisorTTLBelowJWT(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/supervisor-ttl-below-jwt.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrSupervisorTTLOutOfRange)
}

func TestServer_RejectsSupervisorTTLAboveCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/supervisor-ttl-above-cap.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrSupervisorTTLOutOfRange)
}

// ---- audit_log containment (US5) --------------------------------------------

func TestServer_RejectsAuditLogParentTraversal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/audit-log-traversal.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrAuditLogEscape)
}

func TestServer_AcceptsAuditLogUnderStateDir(t *testing.T) {
	t.Parallel()
	s, err := loadWithStateDir(t, "testdata/valid/minimal-valid.toml")
	require.NoError(t, err)
	require.NotNil(t, s)
	// audit_log must be under state_dir
	assert.True(t, strings.HasPrefix(s.Server.AuditLog, s.Server.StateDir+string(filepath.Separator)),
		"audit_log %q must be under state_dir %q", s.Server.AuditLog, s.Server.StateDir)
}

func TestServer_AuditLogContainmentRejectsDriveLetterFalsePositive(t *testing.T) {
	t.Parallel()
	// state_dir = /tmp/foo, audit_log = /tmp/foobar/audit.jsonl
	// String-prefix match would pass but lexical-containment check must reject.
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "foo")
	auditDir := filepath.Join(dir, "foobar")
	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	require.NoError(t, os.MkdirAll(auditDir, 0o700))

	content := "[server]\n" +
		"listen_addr = \"100.96.10.4:7743\"\n" +
		"path_prefix = \"a8k2f9\"\n" +
		"state_dir = \"" + stateDir + "\"\n" +
		"audit_log = \"" + filepath.Join(auditDir, "audit.jsonl") + "\"\n" +
		"discord_owner_id = \"123456789012345678\"\n" +
		"\n[discord]\n" +
		"bot_token_keychain_item = \"hush-discord\"\n" +
		"application_id = \"345678901234567890\"\n"

	cfg := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0o600))

	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrAuditLogEscape)
}

// ---- Multi-violation join (US9 / cross-cutting) ----------------------------

func TestValidate_MultiViolationJoinsErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	// Use a separate 0o700 directory for audit_log so it passes the
	// parent-mode guard (which runs before Validate and would otherwise
	// short-circuit), leaving the containment check to fire in Validate
	// alongside the other rules under test.
	otherDir := t.TempDir()
	require.NoError(t, os.Chmod(otherDir, 0o700))
	auditLog := filepath.Join(otherDir, "audit.jsonl")

	// A synthetic config with multiple violations simultaneously:
	// loopback listen_addr + argon_memory_mb=128 + audit_log outside state_dir.
	content := "[server]\n" +
		"listen_addr = \"127.0.0.1:7743\"\n" +
		"path_prefix = \"a8k2f9\"\n" +
		"state_dir = \"" + dir + "\"\n" +
		"audit_log = \"" + auditLog + "\"\n" +
		"discord_owner_id = \"123456789012345678\"\n" +
		"\n[discord]\n" +
		"bot_token_keychain_item = \"hush-discord\"\n" +
		"application_id = \"345678901234567890\"\n" +
		"\n[crypto]\n" +
		"argon_memory_mb = 128\n"

	cfg := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0o600))

	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrListenLoopback)
	require.ErrorIs(t, err, ErrArgonMemoryTooLow)
	require.ErrorIs(t, err, ErrAuditLogEscape)
}

// ---- Additional duration + path_prefix coverage tests ----------------------

func TestServer_RejectsBadMaxInteractiveTTL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/bad-duration-max-interactive-ttl.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrInvalidDuration)
}

func TestServer_RejectsBadMaxSupervisorTTLDuration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/bad-duration-max-supervisor-ttl.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrInvalidDuration)
}

func TestServer_RejectsBadNonceTTL(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/bad-duration-nonce-ttl.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrInvalidDuration)
}

func TestServer_RejectsBadClockSkew(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/bad-duration-clock-skew.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrInvalidDuration)
}

func TestServer_RejectsBadMaxClockDrift(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/bad-duration-max-clock-drift.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrInvalidDuration)
}

func TestServer_RejectsMissingPathPrefix(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := injectStateDir(t, "testdata/invalid/missing-path-prefix.toml", dir)
	s, err := LoadServer(context.Background(), cfg)
	assert.Nil(t, s)
	require.ErrorIs(t, err, ErrMissingRequiredField)
}

// ---- H5: rule-order regression test ----------------------------------------

// TestValidate_RuleOrderDeterministic constructs a *Server that violates
// every rule simultaneously and asserts the joined error sequence matches
// the documented order in validate.go. The test bypasses LoadServer's
// materialize step (which would short-circuit on a missing state_dir
// before reaching Validate) and exercises Validate directly.
//
// Documented order (validate.go):
//  1. require_tailscale gate
//     2a. Argon2id floors (memory, time, threads)
//     2b. Argon2id ceilings (memory, time, threads) — only if floors not tripped
//  3. listen_addr family
//  4. health_bind family (when explicitly set)
//  5. path_prefix
//  6. audit_log containment
//  7. max_supervisor_ttl bounds
//  8. claim_approval_timeout bounds
//  9. nonce_ttl ≥ 2 × clock_skew (only fires when clock_skew > 0)
//
// Operators rely on this ordering to triage multi-violation configs
// ("fix the first error first"); a silent reorder would break that workflow.
//
// Rule 9 does NOT fire in this test because the input leaves clock_skew
// at zero; a dedicated test in TestServer_RejectsNonceTTLBelowReplayWindow
// covers the rule directly with both fields populated.
func TestValidate_RuleOrderDeterministic(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	auditOutside := filepath.Join(t.TempDir(), "elsewhere", "audit.jsonl")

	listenRaw := "0.0.0.0:7743"
	healthRaw := "1.2.3.4:8080"
	listenAP := netip.MustParseAddrPort(listenRaw)
	healthAP := netip.MustParseAddrPort(healthRaw)

	s := &Server{
		Network: NetworkSection{
			RequireTailscale: false, // trips rule 1
			HealthBind:       healthAP,
			AllowedCIDRs:     []string{"100.64.0.0/10"},
		},
		Crypto: CryptoSection{
			ArgonMemoryMB:    100, // < 256 → rule 2a-mem-low
			ArgonTime:        2,   // < 4   → rule 2a-time-low
			ArgonThreads:     2,   // < 4   → rule 2a-threads-low
			JWTDefaultTTL:    8 * time.Hour,
			MaxSupervisorTTL: 1 * time.Hour, // < jwt → rule 7
		},
		Server: ServerSection{
			ListenAddr: listenAP,
			PathPrefix: "bad!", // contains '!' → rule 5
			StateDir:   stateDir,
			AuditLog:   auditOutside, // outside state_dir → rule 6
		},
		rawListenAddr: listenRaw, // 0.0.0.0 → rule 3 (unspecified)
		rawHealthBind: healthRaw, // 1.2.3.4 → rule 4 (public)
	}

	err := s.Validate()
	require.Error(t, err)

	// errors.Join (Go 1.20+) returns an error with Unwrap() []error.
	type unwrapper interface{ Unwrap() []error }
	uw, ok := err.(unwrapper)
	require.True(t, ok, "Validate must return errors.Join (Unwrap() []error)")
	leaves := uw.Unwrap()

	expectedOrder := []error{
		ErrTailscaleRequired,              // rule 1
		ErrArgonMemoryTooLow,              // rule 2a-mem
		ErrArgonTimeTooLow,                // rule 2a-time
		ErrArgonThreadsTooLow,             // rule 2a-threads
		ErrListenUnspecified,              // rule 3
		ErrListenPublic,                   // rule 4 (health_bind 1.2.3.4 is public)
		ErrPathPrefixInvalid,              // rule 5
		ErrAuditLogEscape,                 // rule 6
		ErrSupervisorTTLOutOfRange,        // rule 7
		ErrClaimApprovalTimeoutOutOfRange, // rule 8 (zero-value timeout)
	}

	require.Len(t, leaves, len(expectedOrder),
		"got %d leaves, want %d", len(leaves), len(expectedOrder))

	for i, want := range expectedOrder {
		require.ErrorIsf(t, leaves[i], want,
			"leaves[%d] = %v, want errors.Is(_, %v)", i, leaves[i], want)
	}
}

// ---- rule 9: nonce_ttl ≥ 2 × clock_skew (replay-window invariant) ----------

// TestServer_RejectsNonceTTLBelowReplayWindow drives rule 9 directly through
// Validate() with a Server that satisfies every other rule. Each table row
// represents one (nonce_ttl, clock_skew) combination plus the expected
// outcome — pinning both the positive (defaults pass) and negative
// (operator tweaks open a replay window) sides of the invariant.
//
// The invariant: nonce_ttl ≥ 2 × clock_skew. The factor of 2 is the
// timestamp acceptance window (±clock_skew = 2 × clock_skew total). With
// the production defaults (60s, 30s), the bound is satisfied EXACTLY —
// any operator tweak that narrows nonce_ttl or widens clock_skew without
// preserving the 2× relationship opens a window in which a captured
// request's nonce can expire from the cache while its timestamp is still
// fresh, enabling replay.
func TestServer_RejectsNonceTTLBelowReplayWindow(t *testing.T) {
	t.Parallel()

	// validCryptoBase returns a CryptoSection that passes every Crypto-
	// related rule EXCEPT rule 9, leaving NonceTTL/ClockSkew to each
	// case to vary.
	validCryptoBase := func(nonceTTL, clockSkew time.Duration) CryptoSection {
		return CryptoSection{
			ArgonMemoryMB:        DefaultArgonMemoryMB,
			ArgonTime:            DefaultArgonTime,
			ArgonThreads:         DefaultArgonThreads,
			JWTDefaultTTL:        DefaultJWTTTL,
			MaxInteractiveTTL:    DefaultMaxInteractiveTTL,
			MaxSupervisorTTL:     DefaultMaxSupervisorTTL,
			ClaimApprovalTimeout: DefaultClaimApprovalTimeout,
			NonceTTL:             nonceTTL,
			ClockSkew:            clockSkew,
		}
	}

	cases := []struct {
		name      string
		nonceTTL  time.Duration
		clockSkew time.Duration
		wantErr   bool
	}{
		{
			name:      "defaults (60s, 30s) satisfy exactly",
			nonceTTL:  DefaultNonceTTL,
			clockSkew: DefaultClockSkew,
			wantErr:   false,
		},
		{
			name:      "boundary equal: 60s >= 60s",
			nonceTTL:  60 * time.Second,
			clockSkew: 30 * time.Second,
			wantErr:   false,
		},
		{
			name:      "boundary just-passing: 61s >= 60s",
			nonceTTL:  61 * time.Second,
			clockSkew: 30 * time.Second,
			wantErr:   false,
		},
		{
			name:      "wider TTL passes: 120s >= 60s",
			nonceTTL:  120 * time.Second,
			clockSkew: 30 * time.Second,
			wantErr:   false,
		},
		{
			name:      "boundary just-failing: 59s < 60s",
			nonceTTL:  59 * time.Second,
			clockSkew: 30 * time.Second,
			wantErr:   true,
		},
		{
			name:      "tightened skew with default TTL: 60s < 80s",
			nonceTTL:  DefaultNonceTTL,
			clockSkew: 40 * time.Second,
			wantErr:   true,
		},
		{
			name:      "narrowed TTL with default skew: 10s < 60s",
			nonceTTL:  10 * time.Second,
			clockSkew: DefaultClockSkew,
			wantErr:   true,
		},
		{
			name:      "zero TTL with default skew fails: 0 < 60s",
			nonceTTL:  0,
			clockSkew: DefaultClockSkew,
			wantErr:   true,
		},
		{
			name:      "zero skew skips rule (vacuous): 0s would pass 0",
			nonceTTL:  0,
			clockSkew: 0,
			wantErr:   false,
		},
		{
			name:      "zero skew skips rule even with positive TTL",
			nonceTTL:  60 * time.Second,
			clockSkew: 0,
			wantErr:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			listenRaw := "100.64.1.1:7743"
			listenAP := netip.MustParseAddrPort(listenRaw)
			stateDir := t.TempDir()
			require.NoError(t, os.Chmod(stateDir, 0o700))

			s := &Server{
				Network: NetworkSection{
					RequireTailscale: true,
					AllowedCIDRs:     []string{"100.64.0.0/10"},
				},
				Crypto: validCryptoBase(tc.nonceTTL, tc.clockSkew),
				Server: ServerSection{
					ListenAddr: listenAP,
					PathPrefix: "abcdef",
					StateDir:   stateDir,
					AuditLog:   filepath.Join(stateDir, "audit.jsonl"),
				},
				rawListenAddr: listenRaw,
			}

			err := s.Validate()
			if tc.wantErr {
				require.Error(t, err, "expected validation failure for (nonce_ttl=%s, clock_skew=%s)", tc.nonceTTL, tc.clockSkew)
				require.ErrorIs(t, err, ErrNonceTTLBelowReplayWindow,
					"error chain must carry ErrNonceTTLBelowReplayWindow for triage")
			} else {
				require.NoError(t, err, "unexpected validation failure for (nonce_ttl=%s, clock_skew=%s): %v", tc.nonceTTL, tc.clockSkew, err)
			}
		})
	}
}

// TestServer_DefaultsSatisfyReplayWindowInvariant pins the contract that the
// shipped defaults satisfy rule 9. A defaults regression here (e.g. someone
// later bumping DefaultClockSkew without bumping DefaultNonceTTL) would
// silently re-open the replay window for every operator who relied on
// defaults. The test goes through LoadServer so it exercises the full
// parse-then-validate path that production uses.
func TestServer_DefaultsSatisfyReplayWindowInvariant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	// Minimal config — all crypto fields take defaults.
	content := "[server]\n" +
		"listen_addr = \"100.64.1.1:7743\"\n" +
		"path_prefix = \"abcdef\"\n" +
		"state_dir = \"" + dir + "\"\n" +
		"audit_log = \"" + dir + "/audit.jsonl\"\n" +
		"discord_owner_id = \"123456789012345678\"\n" +
		"\n[discord]\n" +
		"bot_token_keychain_item = \"hush-discord\"\n" +
		"application_id = \"345678901234567890\"\n"
	cfg := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(cfg, []byte(content), 0o600))

	s, err := LoadServer(context.Background(), cfg)
	require.NoError(t, err, "default config must satisfy rule 9 — replay-window invariant")
	require.NotNil(t, s)
	// Sanity-check the loaded values match the documented defaults.
	require.Equal(t, DefaultNonceTTL, s.Crypto.NonceTTL)
	require.Equal(t, DefaultClockSkew, s.Crypto.ClockSkew)
	require.GreaterOrEqual(t, s.Crypto.NonceTTL, 2*s.Crypto.ClockSkew,
		"defaults regression: nonce_ttl=%s < 2 × clock_skew=%s", s.Crypto.NonceTTL, s.Crypto.ClockSkew)
}
