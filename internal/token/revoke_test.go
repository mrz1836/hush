package token

import (
	"errors"
	"testing"
	"time"
)

func TestStore_RevokedJTI_Refused(t *testing.T) {
	store := NewStore()
	tok, priv := issueAndAdd(t, store, nil)
	if err := store.Revoke(tok.JTI); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	_, err := Validate(t.Context(), tok.Encoded, &priv.PublicKey, store, "100.64.0.1", "FAKE_SECRET")
	if !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("got %v, want ErrTokenRevoked", err)
	}
}

func TestStore_RevokeIsIdempotent(t *testing.T) {
	store := NewStore()
	tok, _ := issueAndAdd(t, store, nil)
	if err := store.Revoke(tok.JTI); err != nil {
		t.Fatalf("Revoke 1: %v", err)
	}
	if err := store.Revoke(tok.JTI); err != nil {
		t.Fatalf("Revoke 2: %v", err)
	}
	if err := store.Revoke("never-issued"); err != nil {
		t.Fatalf("Revoke unknown: %v", err)
	}
}

func TestStore_RevokedSurvivesCleanup(t *testing.T) {
	ms := &memStore{
		live:    make(map[string]*Token),
		revoked: make(map[string]struct{}),
		tick:    time.Millisecond,
		nowFn:   time.Now,
	}
	tok := &Token{
		JTI:         "jti-survive",
		Encoded:     "x",
		ExpiresAt:   time.Now().Add(time.Hour),
		SessionType: SessionInteractive,
		MaxUses:     1,
	}
	if err := ms.Add(tok); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := ms.Revoke(tok.JTI); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Run a sweep that would otherwise clear an expired live record;
	// the revoked-set entry must remain.
	ms.mu.Lock()
	ms.nowFn = func() time.Time { return time.Now().Add(2 * time.Hour) }
	ms.mu.Unlock()
	ms.sweepExpired()

	if _, err := ms.Get(tok.JTI); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("Get post-cleanup: got %v, want ErrTokenRevoked", err)
	}
}
