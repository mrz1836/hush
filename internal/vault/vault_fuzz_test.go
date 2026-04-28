package vault

import (
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
