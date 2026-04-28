package testutil

import (
	"bytes"
	"sync"
	"testing"
)

func TestNewTestKeys_Length(t *testing.T) {
	seed := NewTestKeys(t)
	if len(seed) != 64 {
		t.Fatalf("expected 64-byte seed, got %d bytes", len(seed))
	}
}

func TestNewTestKeys_Determinism(t *testing.T) {
	a := NewTestKeys(t)
	b := NewTestKeys(t)
	if !bytes.Equal(a, b) {
		t.Fatal("two invocations of NewTestKeys returned different bytes")
	}
}

func TestNewTestKeys_PassphraseProvenance(t *testing.T) {
	passphrase := string(testPassphrase)
	const marker = "NEVER-USE-IN-PROD"
	if !contains(passphrase, marker) {
		t.Fatalf("testPassphrase does not contain %q (got %q)", marker, passphrase)
	}
}

func TestNewTestKeys_Concurrent(t *testing.T) {
	const n = 100
	reference := NewTestKeys(t)
	results := make([][]byte, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			results[idx] = NewTestKeys(t)
		}(i)
	}
	wg.Wait()
	for i, r := range results {
		if !bytes.Equal(r, reference) {
			t.Errorf("goroutine %d: result differs from reference", i)
		}
	}
}

func TestNewTestKeys_DefensiveCopy(t *testing.T) {
	a := NewTestKeys(t)
	original := make([]byte, len(a))
	copy(original, a)
	for i := range a {
		a[i] = 0xFF
	}
	b := NewTestKeys(t)
	if !bytes.Equal(b, original) {
		t.Fatal("mutating returned slice poisoned the cache")
	}
}

// contains is a simple substring check used only in tests.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (s == sub || len(s) > 0 && searchSubstring(s, sub)))
}

func searchSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
