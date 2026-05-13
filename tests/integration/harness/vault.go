//go:build integration

package harness

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// TestVault is the per-scenario on-disk vault fixture. Built on top of
// testutil.NewTestVault (SDD-04), it owns the vault path, encryption key,
// state directory, clients.json registry path, and audit log path.
//
// Every fixture secret carries a per-scenario sentinel via
// testutil.SentinelSecret(N) at the call site — Constitution X requires
// the integration suite proves the redaction path against a tagged byte
// sequence that should never appear in any captured stream.
type TestVault struct {
	mu             sync.Mutex
	dir            string
	vaultPath      string
	vaultKey       *securebytes.SecureBytes
	clientRegistry string
	auditPath      string

	// secrets is the latest plaintext map; mirrored to disk via
	// vault.Save. Kept here so Rotate atomically rewrites the file.
	secrets map[string]string
}

// NewVault builds a fresh vault in t.TempDir() pre-populated with
// secrets. The vault is encrypted with a deterministic test key derived
// from testutil.NewTestKeys; the audit log path defaults to
// <dir>/audit.jsonl; the clients.json registry path defaults to
// <dir>/clients.json and is created empty (scenarios that exercise the
// /claim signature path MUST call RegisterClient before issuing a claim).
//
// Every secret value SHOULD be wrapped in testutil.SentinelSecret(N) at
// the call site for Constitution X coverage.
func NewVault(t *testing.T, secrets map[string]string) *TestVault {
	t.Helper()
	path, key, _ := testutil.NewTestVault(t, secrets)
	dir := filepath.Dir(path)
	auditPath := filepath.Join(dir, "audit.jsonl")
	registry := filepath.Join(dir, "clients.json")
	if err := os.WriteFile(registry, []byte("[]"), 0o600); err != nil {
		t.Fatalf("harness.NewVault: write clients.json: %v", err)
	}
	// Defensive copy of the plaintext map so Rotate can rewrite atomically.
	cp := make(map[string]string, len(secrets))
	for k, v := range secrets {
		cp[k] = v
	}
	return &TestVault{
		dir:            dir,
		vaultPath:      path,
		vaultKey:       key,
		clientRegistry: registry,
		auditPath:      auditPath,
		secrets:        cp,
	}
}

// Path returns the absolute path to the on-disk vault file.
func (v *TestVault) Path() string { return v.vaultPath }

// Dir returns the state directory containing the vault, clients.json,
// audit log, pidfile, and status socket. Scenarios point internal/server
// Cfg.Server.StateDir at this directory.
func (v *TestVault) Dir() string { return v.dir }

// Key returns the vault encryption key handle. Required by internal/server
// for SIGHUP reload paths.
func (v *TestVault) Key() *securebytes.SecureBytes { return v.vaultKey }

// AuditPath returns the absolute path to the audit JSONL file. Suitable
// input for audit.Verify and the Contract D raw-bytes sweep.
func (v *TestVault) AuditPath() string { return v.auditPath }

// RegistryPath returns the absolute path to the clients.json registry
// file. Suitable input for internal/server.Cfg.Server.ClientRegistry.
func (v *TestVault) RegistryPath() string { return v.clientRegistry }

// clientRegistryEntry mirrors internal/server's on-disk shape (kept
// private there). Re-declared here so the harness can write fixtures.
type clientRegistryEntry struct {
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
}

// RegisterClient appends a (fingerprint → SEC1-compressed pubkey) row to
// clients.json so the in-process server can verify /claim signatures
// from the supplied public key. machineIdx is captured for symmetry with
// production registration but currently unused; it remains in the API so
// future scenarios that need machine-indexed registrations can extend
// without breaking callers.
func (v *TestVault) RegisterClient(_ *testing.T, machineIdx uint32, pub *ecdsa.PublicKey) {
	_ = machineIdx
	v.mu.Lock()
	defer v.mu.Unlock()
	raw, err := os.ReadFile(v.clientRegistry)
	if err != nil {
		panic("harness.RegisterClient: read clients.json: " + err.Error())
	}
	entries := make([]clientRegistryEntry, 0, 1)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &entries)
	}
	fp := keys.PublicKeyFingerprint(pub)
	pubHex := compressedPubKeyHex(pub)
	entries = append(entries, clientRegistryEntry{Fingerprint: fp, PublicKey: pubHex})
	out, err := json.Marshal(entries)
	if err != nil {
		panic("harness.RegisterClient: marshal clients.json: " + err.Error())
	}
	if err := os.WriteFile(v.clientRegistry, out, 0o600); err != nil {
		panic("harness.RegisterClient: write clients.json: " + err.Error())
	}
}

// Rotate atomically rewrites the vault with the supplied (name, value)
// pair updated. Used by Scenario 13 (mid-session rotation).
func (v *TestVault) Rotate(t *testing.T, name, newValue string) {
	t.Helper()
	v.mu.Lock()
	v.secrets[name] = newValue
	pairs := make([]vault.Secret, 0, len(v.secrets))
	sbs := make([]*securebytes.SecureBytes, 0, len(v.secrets))
	for k, val := range v.secrets {
		sb, err := securebytes.New([]byte(val))
		if err != nil {
			v.mu.Unlock()
			t.Fatalf("harness.Rotate: securebytes.New: %v", err)
		}
		sbs = append(sbs, sb)
		pairs = append(pairs, vault.Secret{Name: k, Value: sb})
	}
	if err := vault.Save(context.Background(), v.vaultPath, v.vaultKey, pairs); err != nil {
		v.mu.Unlock()
		t.Fatalf("harness.Rotate: vault.Save: %v", err)
	}
	v.mu.Unlock()
	t.Cleanup(func() {
		for _, sb := range sbs {
			_ = sb.Destroy()
		}
	})
}

// compressedPubKeyHex returns the 66-hex-char SEC1-compressed form of pub.
func compressedPubKeyHex(pub *ecdsa.PublicKey) string {
	compressed := make([]byte, 33)
	// secp256k1 is not in crypto/ecdh (Go 1.26) so pub.Bytes() errors for
	// secp256k1 keys. .X / .Y are read-only here; the deprecation warning
	// is about mutation safety, which does not apply.
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .X/.Y are read-only
	if pub.Y.Bit(0) == 0 {
		compressed[0] = 0x02
	} else {
		compressed[0] = 0x03
	}
	//nolint:staticcheck // see above
	xBytes := pub.X.Bytes()
	copy(compressed[1+32-len(xBytes):], xBytes)
	return hex.EncodeToString(compressed)
}
