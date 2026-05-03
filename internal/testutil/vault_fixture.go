package testutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// VaultEntry is a (Name, Description, Value) triple suitable for
// pre-populating a vault via NewTestVaultDetailed. The Description
// field is preserved through Save → Load round-trips so tests can
// exercise the `hush secret list` rendering paths.
type VaultEntry struct {
	Name        string
	Description string
	Value       string
}

// NewTestVaultDetailed creates a real HUSH-format vault file inside t.TempDir()
// containing the supplied entries (with descriptions). Returns the absolute
// path, the vault encryption key wrapped in *securebytes.SecureBytes, and a
// cleanup func. The cleanup is also registered via t.Cleanup.
func NewTestVaultDetailed(t *testing.T, entries []VaultEntry) (path string, vaultKey *securebytes.SecureBytes, cleanup func()) {
	t.Helper()

	seed := NewTestKeys(t)

	rawKey, err := keys.DeriveVaultEncKey(seed)
	if err != nil {
		t.Fatalf("hush/testutil: NewTestVaultDetailed: DeriveVaultEncKey: %v", err)
	}

	vaultKey, err = securebytes.New(rawKey)
	if err != nil {
		t.Fatalf("hush/testutil: NewTestVaultDetailed: securebytes.New(vaultKey): %v", err)
	}

	valueSBs := make([]*securebytes.SecureBytes, 0, len(entries))
	vaultSecrets := make([]vault.Secret, 0, len(entries))
	for _, e := range entries {
		bval := []byte(e.Value)
		sb, sbErr := securebytes.New(bval)
		if sbErr != nil {
			t.Fatalf("hush/testutil: NewTestVaultDetailed: securebytes.New(%q): %v", e.Name, sbErr)
		}
		valueSBs = append(valueSBs, sb)
		vaultSecrets = append(vaultSecrets, vault.Secret{
			Name:        e.Name,
			Description: e.Description,
			Value:       sb,
		})
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0700 required by vault.Save parent-directory permission check
		t.Fatalf("hush/testutil: NewTestVaultDetailed: chmod temp dir: %v", err)
	}
	path = filepath.Join(dir, "test.vault")

	if saveErr := vault.Save(context.Background(), path, vaultKey, vaultSecrets); saveErr != nil {
		t.Fatalf("hush/testutil: NewTestVaultDetailed: vault.Save: %v", saveErr)
	}

	cleanup = func() {
		_ = vaultKey.Destroy()
		for _, sb := range valueSBs {
			_ = sb.Destroy()
		}
	}
	t.Cleanup(cleanup)

	return path, vaultKey, cleanup
}

// NewTestVault creates a real HUSH-format vault file inside t.TempDir() containing the
// supplied secrets. It returns the absolute path to the file, the vault encryption key
// wrapped in *securebytes.SecureBytes, and an explicit cleanup func. The cleanup is also
// registered via t.Cleanup, so callers do not need to invoke it manually.
func NewTestVault(t *testing.T, secrets map[string]string) (path string, vaultKey *securebytes.SecureBytes, cleanup func()) {
	t.Helper()

	seed := NewTestKeys(t)

	rawKey, err := keys.DeriveVaultEncKey(seed)
	if err != nil {
		t.Fatalf("hush/testutil: NewTestVault: DeriveVaultEncKey: %v", err)
	}

	vaultKey, err = securebytes.New(rawKey)
	if err != nil {
		t.Fatalf("hush/testutil: NewTestVault: securebytes.New(vaultKey): %v", err)
	}

	valueSBs := make([]*securebytes.SecureBytes, 0, len(secrets))
	vaultSecrets := make([]vault.Secret, 0, len(secrets))
	for name, value := range secrets {
		bval := []byte(value)
		sb, sbErr := securebytes.New(bval)
		if sbErr != nil {
			t.Fatalf("hush/testutil: NewTestVault: securebytes.New(%q): %v", name, sbErr)
		}
		valueSBs = append(valueSBs, sb)
		vaultSecrets = append(vaultSecrets, vault.Secret{Name: name, Description: "", Value: sb})
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0700 required by vault.Save parent-directory permission check
		t.Fatalf("hush/testutil: NewTestVault: chmod temp dir: %v", err)
	}
	path = filepath.Join(dir, "test.vault")

	if saveErr := vault.Save(context.Background(), path, vaultKey, vaultSecrets); saveErr != nil {
		t.Fatalf("hush/testutil: NewTestVault: vault.Save: %v", saveErr)
	}

	cleanup = func() {
		_ = vaultKey.Destroy()
		for _, sb := range valueSBs {
			_ = sb.Destroy()
		}
	}
	t.Cleanup(cleanup)

	return path, vaultKey, cleanup
}
