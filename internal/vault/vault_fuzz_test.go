package vault

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// fuzzVaultKey is the fixed 32-byte AES key used by FuzzVaultDecode.
// It is a package-level var so corpus seeds can call the real Save path.
//
//nolint:gochecknoglobals // package-level fuzz key; required for corpus seed callbacks
var fuzzVaultKey = func() *securebytes.SecureBytes {
	key := make([]byte, 32)
	for i := range key {
		key[i] = 0xAB
	}
	sb, err := securebytes.New(key)
	if err != nil {
		panic("fuzz: securebytes.New: " + err.Error())
	}
	return sb
}()

//nolint:gocognit,gocyclo // fuzz harness with multiple seed paths and assertions; complexity is structural
func FuzzVaultDecode(f *testing.F) {
	ctx := context.Background()

	// Seed corpus: produce real envelopes via Save.
	seedDir := f.TempDir()

	// Seed 1: empty vault.
	seed1Path := filepath.Join(seedDir, "seed1.hush")
	if err := os.MkdirAll(seedDir, 0o700); err == nil {
		_ = Save(ctx, seed1Path, fuzzVaultKey, []Secret{})
		if data, err := os.ReadFile(seed1Path); err == nil { //nolint:gosec // test-controlled path
			f.Add(data)
		}
	}

	// Seed 2: 1-secret vault.
	seed2Path := filepath.Join(seedDir, "seed2.hush")
	sb1, _ := securebytes.New([]byte("fuzz-seed-value"))
	if sb1 != nil {
		_ = Save(ctx, seed2Path, fuzzVaultKey, []Secret{{Name: "KEY", Description: "d", Value: sb1}})
		_ = sb1.Destroy()
		if data, err := os.ReadFile(seed2Path); err == nil { //nolint:gosec // test-controlled path
			f.Add(data)
		}
	}

	// Seed 3: 5-secret vault.
	seed3Path := filepath.Join(seedDir, "seed3.hush")
	var seed3Secrets []Secret
	for i := 0; i < 5; i++ {
		v, _ := securebytes.New([]byte{byte(i), byte(i + 1)})
		if v != nil {
			seed3Secrets = append(seed3Secrets, Secret{
				Name:        "KEY_" + string(rune('A'+i)),
				Description: "desc",
				Value:       v,
			})
		}
	}
	if len(seed3Secrets) == 5 {
		_ = Save(ctx, seed3Path, fuzzVaultKey, seed3Secrets)
		for _, s := range seed3Secrets {
			_ = s.Value.Destroy()
		}
		if data, err := os.ReadFile(seed3Path); err == nil { //nolint:gosec // test-controlled path
			f.Add(data)
		}
	}

	// Curated truncated variants.
	f.Add([]byte{})
	f.Add([]byte{0x48, 0x55, 0x53, 0x48})                   // magic only
	f.Add([]byte{0x48, 0x55, 0x53, 0x48, 0x01})             // magic + version
	f.Add([]byte{0x57, 0x52, 0x4F, 0x4E, 0x47, 0x00, 0x00}) // wrong magic
	f.Add(make([]byte, headerLen+16))                       // header + tag zeros (ErrAuthFailed or ErrShortHeader)

	// The known sentinel errors.
	allSentinels := []error{
		ErrBadMagic, ErrBadVersion, ErrShortHeader, ErrAuthFailed,
		ErrFilePermsLoose, ErrFileTooLarge, ErrSecretNotFound,
		ErrStoreDestroyed, ErrDuplicateName, ErrInvalidName,
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Write data to a temp file with correct permissions.
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0700 is correct for directories
			t.Skip("chmod dir: " + err.Error())
		}

		// Skip inputs that are too large (> 50 MiB ceiling for fuzz harness).
		const fuzzMaxBytes = 50 * 1024 * 1024
		if len(data) > fuzzMaxBytes {
			return
		}

		path := filepath.Join(dir, "fuzz.hush")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Skip("write: " + err.Error())
		}

		var msBefore, msAfter runtime.MemStats
		runtime.ReadMemStats(&msBefore)

		store, err := Load(ctx, path, fuzzVaultKey)

		runtime.ReadMemStats(&msAfter)

		if store != nil {
			_ = store.Destroy()
		}

		// Assert 1: no panic (implicit — the fuzz framework enforces this).

		// Assert 2: if err != nil, it wraps one of the typed sentinels.
		//nolint:nestif // sentinel-matching loop with context-error fallback; complexity is structural
		if err != nil {
			matched := false
			for _, sentinel := range allSentinels {
				if errors.Is(err, sentinel) {
					matched = true
					break
				}
			}
			if !matched {
				// Also allow context errors.
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("untyped error from Load: %v", err)
				}
			}
		}

		// Assert 3: allocation did not exceed 50 MiB.
		if msAfter.TotalAlloc > msBefore.TotalAlloc {
			delta := msAfter.TotalAlloc - msBefore.TotalAlloc
			if delta > fuzzMaxBytes {
				t.Fatalf("Load allocated %d bytes (> 50 MiB limit)", delta)
			}
		}
	})
}

// FuzzVaultSaveLoadRoundTrip exercises the encoder side: arbitrary
// (name, description, value) tuples are pushed through Save → Load and the
// recovered bytes must equal the originals. Catches encoder/validator
// divergence — i.e. any input that Save accepts but Load can't decode, or
// any input that Save rejects with an untyped error.
//
//nolint:gocognit,gocyclo // fuzz harness with branched validation; complexity is structural
func FuzzVaultSaveLoadRoundTrip(f *testing.F) {
	ctx := context.Background()

	// Seed corpus: representative valid + boundary + invalid inputs.
	f.Add("KEY", "description", []byte("value"))
	f.Add("A", "", []byte{})
	f.Add("", "x", []byte("v"))                                        // invalid: empty name
	f.Add("name with space", "desc", []byte("v"))                      // valid: name allows 0x20
	f.Add("name\x00null", "desc", []byte("v"))                         // invalid: name has 0x00
	f.Add("name", "desc\x00null", []byte("v"))                         // invalid: desc has 0x00
	f.Add("name", "desc", make([]byte, 1024))                          // medium value
	f.Add(string(make([]byte, 256)), "desc", []byte("v"))              // invalid: zero-byte name
	f.Add("ok", string(bytes.Repeat([]byte{0x20}, 4097)), []byte("v")) // invalid: desc too long

	// Permissible Save/Load error sentinels.
	saveSentinels := []error{
		ErrInvalidName, ErrDuplicateName,
		ErrFileTooLarge, ErrFilePermsLoose,
	}
	loadSentinels := []error{
		ErrBadMagic, ErrBadVersion, ErrShortHeader, ErrAuthFailed,
		ErrFilePermsLoose, ErrFileTooLarge,
	}

	f.Fuzz(func(t *testing.T, name, description string, value []byte) {
		// Cap value size to keep fuzz inputs bounded.
		const maxValueBytes = 64 * 1024
		if len(value) > maxValueBytes {
			return
		}

		dir := t.TempDir()
		if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0700 correct for dirs
			t.Skip("chmod dir: " + err.Error())
		}
		path := filepath.Join(dir, "rt.hush")

		// Snapshot BEFORE securebytes.New — New zeros its source buffer as a
		// security feature, so `value` is unusable for assertions afterward.
		expected := append([]byte(nil), value...)

		sb, sbErr := securebytes.New(value)
		if sbErr != nil {
			// Allocator failure (mlock pressure) — skip, not a Save bug.
			return
		}
		defer func() { _ = sb.Destroy() }()

		secrets := []Secret{{Name: name, Description: description, Value: sb}}
		saveErr := Save(ctx, path, fuzzVaultKey, secrets)

		if saveErr != nil {
			// Save rejected: error MUST wrap a typed sentinel or a context error.
			if !errorMatchesSentinelOrCtx(saveErr, saveSentinels) {
				t.Fatalf("Save returned untyped error: %v (name=%q desc=%q valueLen=%d)",
					saveErr, name, description, len(value))
			}
			return
		}

		// Save accepted: Load MUST succeed AND recover byte-equal value.
		store, loadErr := Load(ctx, path, fuzzVaultKey)
		if loadErr != nil {
			// Untyped Load error after successful Save is a critical bug.
			matched := false
			for _, s := range loadSentinels {
				if errors.Is(loadErr, s) {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("Save accepted but Load failed: %v (name=%q)", loadErr, name)
			}
			t.Fatalf("Save accepted but Load returned typed error: %v (name=%q) — encoder/validator divergence",
				loadErr, name)
		}
		defer func() { _ = store.Destroy() }()

		got, getErr := store.Get(name)
		if getErr != nil {
			t.Fatalf("Save accepted but Get(%q) failed: %v", name, getErr)
		}
		defer func() { _ = got.Destroy() }()

		gotBytes, useErr := useSecureBytes(got)
		if useErr != nil {
			t.Skip("read SecureBytes: " + useErr.Error())
		}
		if !bytes.Equal(gotBytes, expected) {
			t.Fatalf("round-trip value mismatch: name=%q got %d bytes want %d bytes",
				name, len(gotBytes), len(expected))
		}
	})
}

// errorMatchesSentinelOrCtx returns true if err wraps any of the given
// sentinels, or is one of the standard context errors. Used by fuzz harnesses
// to assert that every error is typed (no naked dynamic errors leaking out).
func errorMatchesSentinelOrCtx(err error, sentinels []error) bool {
	for _, s := range sentinels {
		if errors.Is(err, s) {
			return true
		}
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// useSecureBytes copies the plaintext out of a SecureBytes for assertion.
// Caller assumes responsibility for not leaking the copy.
func useSecureBytes(sb *securebytes.SecureBytes) ([]byte, error) {
	var out []byte
	err := sb.Use(func(b []byte) {
		out = make([]byte, len(b))
		copy(out, b)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
