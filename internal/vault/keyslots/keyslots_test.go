package keyslots_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stateDir returns a fresh 0700 state directory.
func stateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	return dir
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := stateDir(t)
	env := []byte{0x54, 0x4D, 0x42, 0x4C, 0x01, 0x02, 0x03}

	assert.False(t, keyslots.Exists(dir))
	require.NoError(t, keyslots.Save(dir, env, "password-and-yubikey"))
	assert.True(t, keyslots.Exists(dir))

	gotEnv, gotPolicy, err := keyslots.Load(dir)
	require.NoError(t, err)
	assert.Equal(t, env, gotEnv)
	assert.Equal(t, "password-and-yubikey", gotPolicy)
}

func TestLoad_NotFound(t *testing.T) {
	_, _, err := keyslots.Load(stateDir(t))
	assert.ErrorIs(t, err, keyslots.ErrNotFound)
}

func TestSave_Perms0600(t *testing.T) {
	dir := stateDir(t)
	require.NoError(t, keyslots.Save(dir, []byte("env"), "yubikey-only"))
	info, err := os.Stat(keyslots.Path(dir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestLoad_RejectsLoosePerms(t *testing.T) {
	dir := stateDir(t)
	require.NoError(t, keyslots.Save(dir, []byte("env"), ""))
	require.NoError(t, os.Chmod(keyslots.Path(dir), 0o644))
	_, _, err := keyslots.Load(dir)
	assert.ErrorIs(t, err, keyslots.ErrFilePermsLoose)
}

func TestSave_RejectsLooseDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o755)) // too loose
	err := keyslots.Save(dir, []byte("env"), "")
	assert.ErrorIs(t, err, keyslots.ErrFilePermsLoose)
}

func TestSave_EmptyEnvelopeRejected(t *testing.T) {
	err := keyslots.Save(stateDir(t), nil, "")
	require.Error(t, err)
}

func TestSave_Atomic_NoTmpLeftBehind(t *testing.T) {
	dir := stateDir(t)
	require.NoError(t, keyslots.Save(dir, []byte("env"), ""))
	_, err := os.Stat(filepath.Join(dir, "keyslots.json.tmp"))
	assert.True(t, os.IsNotExist(err), "temp file must not linger")
}

func TestRemove(t *testing.T) {
	dir := stateDir(t)
	require.NoError(t, keyslots.Save(dir, []byte("env"), ""))
	require.True(t, keyslots.Exists(dir))
	require.NoError(t, keyslots.Remove(dir))
	assert.False(t, keyslots.Exists(dir))
	// Idempotent.
	assert.NoError(t, keyslots.Remove(dir))
}

func TestLoad_TooLarge(t *testing.T) {
	dir := stateDir(t)
	big := make([]byte, (1<<20)+10)
	require.NoError(t, os.WriteFile(keyslots.Path(dir), big, 0o600))
	_, _, err := keyslots.Load(dir)
	assert.ErrorIs(t, err, keyslots.ErrTooLarge)
}
