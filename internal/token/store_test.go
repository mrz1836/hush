package token

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewStore_Defaults(t *testing.T) {
	if s := NewStore(); s == nil {
		t.Fatal("NewStore returned nil")
	}
	if s := NewStoreWithTick(time.Millisecond); s == nil {
		t.Fatal("NewStoreWithTick returned nil")
	}
	// Empty-store calls should not panic.
	s := NewStore()
	if _, err := s.Get("missing"); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("Get missing: got %v, want ErrTokenRevoked", err)
	}
	if err := s.ConsumeUse("missing"); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("ConsumeUse missing: got %v, want ErrTokenRevoked", err)
	}
	if err := s.Revoke("missing"); err != nil {
		t.Fatalf("Revoke missing: got %v, want nil", err)
	}
}

func issueInteractive(t *testing.T, store Store, maxUses int) *Token {
	t.Helper()
	priv := freshKey(t)
	params := defaultIssueParams(time.Now())
	params.MaxUses = maxUses
	tok, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := store.Add(tok); err != nil {
		t.Fatalf("Add: %v", err)
	}
	return tok
}

func TestStore_ExhaustedInteractive_Refused(t *testing.T) {
	store := NewStore()
	tok := issueInteractive(t, store, 3)

	for range 3 {
		if err := store.ConsumeUse(tok.JTI); err != nil {
			t.Fatalf("ConsumeUse: %v", err)
		}
	}
	if err := store.ConsumeUse(tok.JTI); !errors.Is(err, ErrTokenExhausted) {
		t.Fatalf("4th ConsumeUse: got %v, want ErrTokenExhausted", err)
	}
}

func TestStore_SupervisorIgnoresMaxUses(t *testing.T) {
	store := NewStore()
	priv := freshKey(t)
	params := defaultIssueParams(time.Now())
	params.SessionType = SessionSupervisor
	params.MaxUses = 99
	tok, err := Issue(t.Context(), priv, params)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := store.Add(tok); err != nil {
		t.Fatalf("Add: %v", err)
	}

	for i := 0; i < 1000; i++ {
		if err := store.ConsumeUse(tok.JTI); err != nil {
			t.Fatalf("ConsumeUse iter %d: got %v", i, err)
		}
	}
}

func TestStore_AddOnRevokedJTI_Refused(t *testing.T) {
	store := NewStore()
	tok := issueInteractive(t, store, 3)
	if err := store.Revoke(tok.JTI); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := store.Add(tok); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("Add after revoke: got %v, want ErrTokenRevoked", err)
	}
}

func TestStore_ConsumeUse_ExpiredRecord(t *testing.T) {
	ms := &memStore{
		live:    make(map[string]*Token),
		revoked: make(map[string]struct{}),
		tick:    time.Hour,
		nowFn:   time.Now,
	}
	tok := &Token{
		JTI:         "expired-jti",
		Encoded:     "fake",
		ExpiresAt:   time.Now().Add(-time.Minute),
		SessionType: SessionInteractive,
		MaxUses:     5,
	}
	if err := ms.Add(tok); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := ms.ConsumeUse(tok.JTI); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("ConsumeUse expired: got %v, want ErrTokenExpired", err)
	}
}

func TestStore_ConcurrentDecrement(t *testing.T) {
	const N = 100
	store := NewStore()
	tok := issueInteractive(t, store, N)

	var success, failure atomic.Int64
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			if err := store.ConsumeUse(tok.JTI); err != nil {
				failure.Add(1)
			} else {
				success.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := success.Load(); got != N {
		t.Fatalf("success count: got %d, want %d", got, N)
	}
	if got := failure.Load(); got != 0 {
		t.Fatalf("failure count: got %d, want 0", got)
	}
	if err := store.ConsumeUse(tok.JTI); !errors.Is(err, ErrTokenExhausted) {
		t.Fatalf("post-burst ConsumeUse: got %v, want ErrTokenExhausted", err)
	}
}

func TestStore_CleanupRemovesExpired(t *testing.T) {
	ms := &memStore{
		live:    make(map[string]*Token),
		revoked: make(map[string]struct{}),
		tick:    time.Millisecond,
		nowFn:   time.Now,
	}
	now := time.Now()
	expired := &Token{
		JTI:         "expired",
		Encoded:     "x",
		ExpiresAt:   now.Add(-time.Hour),
		SessionType: SessionInteractive,
		MaxUses:     1,
	}
	live := &Token{
		JTI:         "live",
		Encoded:     "y",
		ExpiresAt:   now.Add(time.Hour),
		SessionType: SessionInteractive,
		MaxUses:     1,
	}
	if err := ms.Add(expired); err != nil {
		t.Fatalf("Add expired: %v", err)
	}
	if err := ms.Add(live); err != nil {
		t.Fatalf("Add live: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		ms.Cleanup(ctx)
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ms.mu.RLock()
		_, expiredStill := ms.live[expired.JTI]
		_, liveStill := ms.live[live.JTI]
		ms.mu.RUnlock()
		if !expiredStill && liveStill {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}

	cancel()
	<-done

	ms.mu.RLock()
	defer ms.mu.RUnlock()
	if _, ok := ms.live[expired.JTI]; ok {
		t.Errorf("expired record still in live map")
	}
	if _, ok := ms.live[live.JTI]; !ok {
		t.Errorf("live record was removed")
	}
}

func TestStore_CleanupConcurrentWithValidate(t *testing.T) {
	store := NewStoreWithTick(time.Millisecond)
	tokens := make([]*Token, 0, 5)
	priv := freshKey(t)
	for i := 0; i < 5; i++ {
		params := defaultIssueParams(time.Now())
		params.MaxUses = 10000
		tok, err := Issue(t.Context(), priv, params)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if err := store.Add(tok); err != nil {
			t.Fatalf("Add: %v", err)
		}
		tokens = append(tokens, tok)
	}

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan struct{})
	go func() {
		store.Cleanup(ctx)
		close(done)
	}()

	deadline := time.Now().Add(100 * time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tok := tokens[idx%len(tokens)]
			for time.Now().Before(deadline) {
				_ = store.ConsumeUse(tok.JTI)
			}
		}(i)
	}
	wg.Wait()
	cancel()
	<-done
}

func TestStore_CleanupNeverTouchesRevoked(t *testing.T) {
	ms := &memStore{
		live:    make(map[string]*Token),
		revoked: make(map[string]struct{}),
		tick:    time.Millisecond,
		nowFn:   time.Now,
	}
	tok := &Token{
		JTI:         "revoked-expired",
		Encoded:     "x",
		ExpiresAt:   time.Now().Add(-time.Hour),
		SessionType: SessionInteractive,
		MaxUses:     1,
	}
	if err := ms.Add(tok); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := ms.Revoke(tok.JTI); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	ms.sweepExpired()

	if _, err := ms.Get(tok.JTI); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("Get after revoke+sweep: got %v, want ErrTokenRevoked", err)
	}
}

func TestStore_CleanupReturnsOnContextDone(t *testing.T) {
	store := NewStoreWithTick(time.Hour)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	store.Cleanup(ctx)
}

func TestStore_ConsumeUse_RevokedSetHit(t *testing.T) {
	ms := &memStore{
		live:    make(map[string]*Token),
		revoked: make(map[string]struct{}),
		tick:    time.Hour,
		nowFn:   time.Now,
	}
	ms.revoked["forced-jti"] = struct{}{}
	if err := ms.ConsumeUse("forced-jti"); !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("got %v, want ErrTokenRevoked", err)
	}
}
