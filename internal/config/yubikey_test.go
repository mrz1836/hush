package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const baseYubiKeyTOML = `[server]
listen_addr = "100.96.10.4:7743"
path_prefix = "a8k2f9"
state_dir = "__STATE_DIR__"
audit_log = "__STATE_DIR__/audit.jsonl"
discord_owner_id = "test-owner-id"

[discord]
bot_token_keychain_item = "hush-discord"
application_id = "345678901234567890"
`

// loadYubiKeyConfig writes the base config plus an optional extra section and
// loads it through the full LoadServer pipeline.
func loadYubiKeyConfig(t *testing.T, extra string) (*Server, error) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	content := strings.ReplaceAll(baseYubiKeyTOML+extra, "__STATE_DIR__", dir)
	dst := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(dst, []byte(content), 0o600)) //nolint:gosec // test config in temp dir
	return LoadServer(context.Background(), dst)
}

func TestServer_YubiKey_Defaults(t *testing.T) {
	t.Parallel()
	s, err := loadYubiKeyConfig(t, "")
	require.NoError(t, err)
	assert.False(t, s.YubiKey.CacheTouch, "cache must be off by default")
	assert.Equal(t, DefaultYubiKeyCacheTouchTTL, s.YubiKey.CacheTouchTTL)
	assert.Equal(t, 60*time.Minute, s.YubiKey.CacheTouchTTL)
}

func TestServer_YubiKey_Enabled(t *testing.T) {
	t.Parallel()
	s, err := loadYubiKeyConfig(t, "\n[yubikey]\ncache_touch = true\ncache_touch_ttl = \"2h\"\n")
	require.NoError(t, err)
	assert.True(t, s.YubiKey.CacheTouch)
	assert.Equal(t, 2*time.Hour, s.YubiKey.CacheTouchTTL)
}

func TestServer_YubiKey_TTLOverCapRejected(t *testing.T) {
	t.Parallel()
	_, err := loadYubiKeyConfig(t, "\n[yubikey]\ncache_touch = true\ncache_touch_ttl = \"5h\"\n")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrYubiKeyCacheTTLOutOfRange)
}

func TestServer_YubiKey_TTLZeroRejected(t *testing.T) {
	t.Parallel()
	_, err := loadYubiKeyConfig(t, "\n[yubikey]\ncache_touch = true\ncache_touch_ttl = \"0s\"\n")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrYubiKeyCacheTTLOutOfRange)
}

func TestServer_YubiKey_TTLNotValidatedWhenDisabled(t *testing.T) {
	t.Parallel()
	// A latent >4h TTL is tolerated while the cache is off; it is re-validated
	// the moment cache_touch flips on.
	s, err := loadYubiKeyConfig(t, "\n[yubikey]\ncache_touch = false\ncache_touch_ttl = \"5h\"\n")
	require.NoError(t, err)
	assert.False(t, s.YubiKey.CacheTouch)
	assert.Equal(t, 5*time.Hour, s.YubiKey.CacheTouchTTL)
}

func TestServer_YubiKey_TTLBoundaryAt4hAccepted(t *testing.T) {
	t.Parallel()
	s, err := loadYubiKeyConfig(t, "\n[yubikey]\ncache_touch = true\ncache_touch_ttl = \"4h\"\n")
	require.NoError(t, err)
	assert.Equal(t, MaxYubiKeyTouchTTL, s.YubiKey.CacheTouchTTL)
}
