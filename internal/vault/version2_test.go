package vault

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSaveWithSaltAndVersion_V2RoundTrip(t *testing.T) {
	dir := makeTestDir(t)
	path := filepath.Join(dir, "v2.hush")
	key := makeVaultKey(t, 0x42)
	ctx := context.Background()

	salt := make([]byte, saltLen)
	sec := makeSecret(t, "api", "desc", []byte("value"))

	require.NoError(t, SaveWithSaltAndVersion(ctx, path, key, salt, version2, []Secret{sec}))

	// The header records version 0x02.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, version2, data[4])

	// It loads back under the same key (v2 body decrypts identically).
	store, err := Load(ctx, path, key)
	require.NoError(t, err)
	assert.Contains(t, store.Names(), "api")
}

func TestSaveWithSaltAndVersion_RejectsBadVersion(t *testing.T) {
	dir := makeTestDir(t)
	path := filepath.Join(dir, "bad.hush")
	key := makeVaultKey(t, 0x42)
	salt := make([]byte, saltLen)
	err := SaveWithSaltAndVersion(context.Background(), path, key, salt, 0x09, nil)
	assert.ErrorIs(t, err, ErrBadVersion)
}
