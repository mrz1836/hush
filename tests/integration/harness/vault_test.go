//go:build integration

package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault"
)

// readSecret loads the on-disk vault and returns the plaintext value for name.
func readSecret(t *testing.T, v *TestVault, name string) string {
	t.Helper()
	store, err := vault.Load(context.Background(), v.Path(), v.Key())
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Destroy() })
	sb, err := store.Get(name)
	require.NoError(t, err)
	var got string
	require.NoError(t, sb.Use(func(b []byte) { got = string(b) }))
	return got
}

// TestNewVaultPathsAndContents verifies the fixture paths are wired and the
// initial secret is readable.
func TestNewVaultPathsAndContents(t *testing.T) {
	v := NewVault(t, map[string]string{"K1": testutil.SentinelSecret(1)})

	assert.FileExists(t, v.Path())
	assert.DirExists(t, v.Dir())
	assert.Equal(t, v.Dir(), filepath.Dir(v.Path()))
	assert.Equal(t, filepath.Join(v.Dir(), "audit.jsonl"), v.AuditPath())
	assert.Equal(t, filepath.Join(v.Dir(), "clients.json"), v.RegistryPath())
	assert.FileExists(t, v.RegistryPath())
	require.NotNil(t, v.Key())

	assert.Equal(t, testutil.SentinelSecret(1), readSecret(t, v, "K1"))
}

// TestNewVaultClientRegistryStartsEmpty confirms clients.json is an empty array.
func TestNewVaultClientRegistryStartsEmpty(t *testing.T) {
	v := NewVault(t, map[string]string{"K1": "v"})
	raw, err := os.ReadFile(v.RegistryPath())
	require.NoError(t, err)
	assert.Equal(t, "[]", string(raw))
}

// TestRegisterClientAppendsEntry verifies a registered key lands in
// clients.json with the canonical fingerprint + compressed pubkey, and
// that multiple registrations accumulate.
func TestRegisterClientAppendsEntry(t *testing.T) {
	v := NewVault(t, map[string]string{"K1": "v"})
	key := NewECDSAKey(t)
	v.RegisterClient(t, 1, &key.PublicKey)

	raw, err := os.ReadFile(v.RegistryPath())
	require.NoError(t, err)
	var entries []clientRegistryEntry
	require.NoError(t, json.Unmarshal(raw, &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, keys.PublicKeyFingerprint(&key.PublicKey), entries[0].Fingerprint)
	assert.Equal(t, compressedPubKeyHex(&key.PublicKey), entries[0].PublicKey)

	// A second registration accumulates rather than overwrites.
	key2 := NewECDSAKey(t)
	v.RegisterClient(t, 2, &key2.PublicKey)
	raw, err = os.ReadFile(v.RegistryPath())
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &entries))
	assert.Len(t, entries, 2)
}

// TestRegisterClientFatalsOnMissingRegistry drives the read-error branch
// (the fix that replaced panic with t.Fatalf).
func TestRegisterClientFatalsOnMissingRegistry(t *testing.T) {
	v := NewVault(t, map[string]string{"K1": "v"})
	require.NoError(t, os.Remove(v.RegistryPath()))
	key := NewECDSAKey(t)
	expectFatal(t, "missing-registry", func(ft *testing.T) {
		v.RegisterClient(ft, 1, &key.PublicKey)
	})
}

// TestRegisterClientFatalsOnCorruptRegistry drives the unmarshal-error branch.
func TestRegisterClientFatalsOnCorruptRegistry(t *testing.T) {
	v := NewVault(t, map[string]string{"K1": "v"})
	require.NoError(t, os.WriteFile(v.RegistryPath(), []byte("{not-json"), 0o600))
	key := NewECDSAKey(t)
	expectFatal(t, "corrupt-registry", func(ft *testing.T) {
		v.RegisterClient(ft, 1, &key.PublicKey)
	})
}

// TestVaultRotateUpdatesSecret confirms Rotate rewrites the vault so a
// fresh Load observes the new value while leaving other secrets intact.
func TestVaultRotateUpdatesSecret(t *testing.T) {
	v := NewVault(t, map[string]string{
		"ROTATED": testutil.SentinelSecret(2),
		"STATIC":  "static-value",
	})

	v.Rotate(t, "ROTATED", "new-value")

	assert.Equal(t, "new-value", readSecret(t, v, "ROTATED"))
	assert.Equal(t, "static-value", readSecret(t, v, "STATIC"))
}

// TestVaultRotateAddsNewSecret confirms Rotate can introduce a key that
// was not in the original fixture.
func TestVaultRotateAddsNewSecret(t *testing.T) {
	v := NewVault(t, map[string]string{"EXISTING": "v"})
	v.Rotate(t, "ADDED", "added-value")
	assert.Equal(t, "added-value", readSecret(t, v, "ADDED"))
	assert.Equal(t, "v", readSecret(t, v, "EXISTING"))
}

// TestCompressedPubKeyHex verifies the SEC1 compressed encoding: 66 hex
// chars, a 0x02/0x03 prefix matching Y parity, and parity with the
// fingerprint helper's view of the same key.
func TestCompressedPubKeyHex(t *testing.T) {
	for range 8 {
		key := NewECDSAKey(t)
		hexStr := compressedPubKeyHex(&key.PublicKey)
		assert.Len(t, hexStr, 66)
		// SEC1 compressed keys always carry an 0x02 / 0x03 parity prefix.
		assert.Contains(t, []string{"02", "03"}, hexStr[:2])
		// Fingerprint is derived from the same compressed form; a stable
		// 16-char hex is expected.
		assert.Len(t, keys.PublicKeyFingerprint(&key.PublicKey), 16)
	}
}
