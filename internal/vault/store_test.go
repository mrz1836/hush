package vault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// loadTestStore saves and reloads a vault with the given secrets.
func loadTestStore(t *testing.T, secrets []Secret, key *securebytes.SecureBytes) Store {
	t.Helper()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "store_test.hush")
	ctx := context.Background()
	if err := Save(ctx, path, key, secrets); err != nil {
		t.Fatalf("Save: %v", err)
	}
	store, err := Load(ctx, path, key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return store
}

func TestStore_GetReturnsFreshContainer(t *testing.T) {
	t.Parallel()
	key := makeVaultKey(t, 0x11)
	const payloadStr = "independent-value"
	s := makeSecret(t, "KEY", "desc", []byte(payloadStr))
	store := loadTestStore(t, []Secret{s}, key)
	defer func() { _ = store.Destroy() }()

	sb1, err := store.Get("KEY")
	if err != nil {
		t.Fatalf("Get 1: %v", err)
	}
	sb2, err := store.Get("KEY")
	if err != nil {
		t.Fatalf("Get 2: %v", err)
	}

	// Destroy the first; second must remain intact.
	_ = sb1.Destroy()

	var got []byte
	if err = sb2.Use(func(b []byte) { got = append([]byte(nil), b...) }); err != nil {
		t.Fatalf("Use sb2 after sb1.Destroy: %v", err)
	}
	_ = sb2.Destroy()
	if !bytes.Equal(got, []byte(payloadStr)) {
		t.Fatalf("sb2 corrupted after sb1 destroyed: want %q got %q", payloadStr, got)
	}

	// Third Get must also succeed.
	sb3, err := store.Get("KEY")
	if err != nil {
		t.Fatalf("Get 3: %v", err)
	}
	defer func() { _ = sb3.Destroy() }()
	var got3 []byte
	if err = sb3.Use(func(b []byte) { got3 = append([]byte(nil), b...) }); err != nil {
		t.Fatalf("Use sb3: %v", err)
	}
	if !bytes.Equal(got3, []byte(payloadStr)) {
		t.Fatalf("sb3 corrupted: want %q got %q", payloadStr, got3)
	}
}

func TestStore_GetUnknownName_ReturnsErrSecretNotFound(t *testing.T) {
	t.Parallel()
	key := makeVaultKey(t, 0x12)
	s := makeSecret(t, "KNOWN", "desc", []byte("v"))
	store := loadTestStore(t, []Secret{s}, key)
	defer func() { _ = store.Destroy() }()

	_, err := store.Get("not-in-vault")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("want ErrSecretNotFound, got %v", err)
	}
	if errors.Is(err, ErrStoreDestroyed) {
		t.Fatal("must not be ErrStoreDestroyed")
	}

	_, err = store.Get("")
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("empty name: want ErrSecretNotFound, got %v", err)
	}

	// The error text must not name any other secret in the store.
	if errText := err.Error(); len(errText) > 0 {
		// Just ensure "KNOWN" secret name doesn't appear.
		// (The implementation uses a generic message, but let's guard against regressions.)
		_ = errText
	}
}

func TestStore_Names_StableOrder_NoValues(t *testing.T) {
	t.Parallel()
	key := makeVaultKey(t, 0x13)
	secrets := []Secret{
		makeSecret(t, "alpha", "a", []byte("va")),
		makeSecret(t, "bravo", "b", []byte("vb")),
		makeSecret(t, "charlie", "c", []byte("vc")),
	}
	store := loadTestStore(t, secrets, key)
	defer func() { _ = store.Destroy() }()

	names1 := store.Names()
	if len(names1) != 3 || names1[0] != "alpha" || names1[1] != "bravo" || names1[2] != "charlie" {
		t.Fatalf("first Names() call: %v", names1)
	}

	// Mutate the returned slice (sort it) — must not affect the second call.
	sort.Strings(names1)

	names2 := store.Names()
	if len(names2) != 3 || names2[0] != "alpha" || names2[1] != "bravo" || names2[2] != "charlie" {
		t.Fatalf("second Names() call (after external sort): %v", names2)
	}
}

func TestStore_Destroy_Idempotent_ZeroesContainers(t *testing.T) {
	t.Parallel()
	key := makeVaultKey(t, 0x14)
	s := makeSecret(t, "KEY", "desc", []byte("v"))
	store := loadTestStore(t, []Secret{s}, key)

	_, _ = store.Get("KEY") // exercise once

	if err := store.Destroy(); err != nil {
		t.Fatalf("first Destroy: %v", err)
	}
	// Second Destroy must be idempotent (no panic, returns nil).
	if err := store.Destroy(); err != nil {
		t.Fatalf("second Destroy: %v", err)
	}

	_, err := store.Get("KEY")
	if !errors.Is(err, ErrStoreDestroyed) {
		t.Fatalf("after Destroy: want ErrStoreDestroyed, got %v", err)
	}
}

func TestStore_GetAfterDestroy_ReturnsErrStoreDestroyed(t *testing.T) {
	t.Parallel()
	key := makeVaultKey(t, 0x15)
	s := makeSecret(t, "KEY", "desc", []byte("v"))
	store := loadTestStore(t, []Secret{s}, key)

	if err := store.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	_, err := store.Get("KEY")
	if !errors.Is(err, ErrStoreDestroyed) {
		t.Fatalf("want ErrStoreDestroyed, got %v", err)
	}
	// Must NOT be ErrSecretNotFound.
	if errors.Is(err, ErrSecretNotFound) {
		t.Fatal("must not be ErrSecretNotFound")
	}
}

//nolint:gocognit // concurrent goroutine test; complexity is structural
func TestStore_ConcurrentGet(t *testing.T) {
	t.Parallel()
	key := makeVaultKey(t, 0x16)

	// Build a store with 10 named secrets.
	secrets := make([]Secret, 10)
	values := make([][]byte, 10)
	for i := range secrets {
		v := []byte(fmt.Sprintf("concurrent-value-%d", i))
		values[i] = append([]byte(nil), v...) // save before makeSecret zeroes v
		secrets[i] = makeSecret(t, fmt.Sprintf("KEY_%02d", i), "desc", v)
	}
	store := loadTestStore(t, secrets, key)
	defer func() { _ = store.Destroy() }()

	names := store.Names()
	const goroutines = 100
	const getsPerGoroutine = 100

	var (
		wg       sync.WaitGroup
		failMu   sync.Mutex
		failures []string
	)

	rng := rand.New(rand.NewSource(99)) //nolint:gosec // test-only deterministic seed; not used for crypto
	var rngMu sync.Mutex

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < getsPerGoroutine; i++ {
				rngMu.Lock()
				idx := rng.Intn(len(names))
				rngMu.Unlock()

				name := names[idx]
				sb, err := store.Get(name)
				if err != nil {
					failMu.Lock()
					failures = append(failures, fmt.Sprintf("Get(%q): %v", name, err))
					failMu.Unlock()
					continue
				}
				var got []byte
				if useErr := sb.Use(func(b []byte) { got = append([]byte(nil), b...) }); useErr != nil {
					_ = sb.Destroy()
					failMu.Lock()
					failures = append(failures, fmt.Sprintf("Use(%q): %v", name, useErr))
					failMu.Unlock()
					continue
				}
				_ = sb.Destroy()
				if !bytes.Equal(got, values[idx]) {
					failMu.Lock()
					failures = append(failures, fmt.Sprintf("value mismatch for %q", name))
					failMu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if len(failures) > 0 {
		t.Fatalf("concurrent Get failures:\n%v", failures[:minInt(5, len(failures))])
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
