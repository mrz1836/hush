package testutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mrz1836/hush/internal/vault"
)

func TestNewTestVault_RoundTrip(t *testing.T) {
	secrets := map[string]string{"foo": "bar", "baz": "qux"}
	path, vaultKey, _ := NewTestVault(t, secrets)

	store, err := vault.Load(context.Background(), path, vaultKey)
	if err != nil {
		t.Fatalf("vault.Load: %v", err)
	}
	defer func() { _ = store.Destroy() }()

	for name, want := range secrets {
		sb, err := store.Get(name)
		if err != nil {
			t.Errorf("store.Get(%q): %v", name, err)
			continue
		}
		var got []byte
		if err := sb.Use(func(b []byte) {
			got = make([]byte, len(b))
			copy(got, b)
		}); err != nil {
			t.Errorf("sb.Use(%q): %v", name, err)
			continue
		}
		if string(got) != want {
			t.Errorf("secret %q: got %q, want %q", name, got, want)
		}
	}
}

func TestNewTestVault_PathContainment(t *testing.T) {
	path, _, _ := NewTestVault(t, nil)
	if filepath.Base(path) != "test.vault" {
		t.Fatalf("expected path to end with test.vault, got %q", path)
	}
	if !strings.HasPrefix(filepath.Dir(path), os.TempDir()) {
		t.Fatalf("vault path %q is not inside temp dir %q", path, os.TempDir())
	}
}

func TestNewTestVault_KeyZeroed(t *testing.T) {
	_, vaultKey, cleanup := NewTestVault(t, nil)
	if vaultKey.Len() == 0 {
		t.Fatal("vault key should be non-zero before cleanup")
	}
	cleanup()
	if vaultKey.Len() != 0 {
		t.Fatal("vault key should be zeroed after cleanup")
	}
}

func TestNewTestVault_CleanupIdempotent(t *testing.T) {
	_, _, cleanup := NewTestVault(t, nil)
	cleanup()
	cleanup() // must not panic
}

func TestNewTestVault_EmptySecrets(t *testing.T) {
	path, vaultKey, _ := NewTestVault(t, map[string]string{})

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("vault file does not exist: %v", err)
	}

	store, err := vault.Load(context.Background(), path, vaultKey)
	if err != nil {
		t.Fatalf("vault.Load: %v", err)
	}
	defer func() { _ = store.Destroy() }()

	if names := store.Names(); len(names) != 0 {
		t.Fatalf("expected 0 secrets, got %d: %v", len(names), names)
	}
}

func TestNewTestVault_ParallelSubtests(t *testing.T) {
	const n = 8
	paths := make([]string, n)
	var mu sync.Mutex

	for i := range n {
		t.Run(fmt.Sprintf("sub%d", i), func(t *testing.T) {
			t.Parallel()
			path, _, _ := NewTestVault(t, map[string]string{fmt.Sprintf("key%d", i): fmt.Sprintf("val%d", i)})
			mu.Lock()
			paths[i] = path
			mu.Unlock()
		})
	}

	t.Cleanup(func() {
		seen := map[string]bool{}
		for _, p := range paths {
			if p == "" {
				continue
			}
			if seen[p] {
				t.Errorf("duplicate vault path: %q", p)
			}
			seen[p] = true
		}
	})
}

func TestNewTestVault_NoLeakAcrossTests(t *testing.T) {
	var capturedPath string

	t.Run("first", func(t *testing.T) {
		path, _, _ := NewTestVault(t, map[string]string{"k": "v"})
		capturedPath = filepath.Dir(path)
		if _, err := os.Stat(capturedPath); err != nil {
			t.Fatalf("temp dir should exist during test: %v", err)
		}
	})

	if _, err := os.Stat(capturedPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temp dir %q should be gone after subtest cleanup, got: %v", capturedPath, err)
	}
}
