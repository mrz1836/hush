package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	// A synthetic config with multiple violations simultaneously:
	// loopback listen_addr + argon_memory_mb=128 + audit_log outside state_dir.
	content := "[server]\n" +
		"listen_addr = \"127.0.0.1:7743\"\n" +
		"path_prefix = \"a8k2f9\"\n" +
		"state_dir = \"" + dir + "\"\n" +
		"audit_log = \"/etc/passwd\"\n" +
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
