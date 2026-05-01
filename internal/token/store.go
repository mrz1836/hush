package token

import (
	"context"
	"sync"
	"time"
)

// Store is the session-state repository consulted by Validate.
// Implementations MUST be safe for concurrent use.
type Store interface {
	Add(t *Token) error
	Get(jti string) (*Token, error)
	ConsumeUse(jti string) error
	Revoke(jti string) error
	Cleanup(ctx context.Context)

	// ActiveCount returns the number of non-revoked, non-expired entries
	// currently in the store. Reported by the /hz handler.
	ActiveCount() int

	// RevokeIdempotent revokes the supplied jti.
	//   existed         — whether the jti was known (live OR previously
	//                     revoked) at call time. Unknown JTIs are mapped
	//                     by the /revoke handler to bad_signature
	//                     (anti-enumeration; FR-015).
	//   alreadyRevoked  — whether the jti was already revoked before the
	//                     call. Distinguishes the audit chain's
	//                     `revoke_succeeded` from
	//                     `revoke_idempotent_already_revoked` (HTTP body
	//                     is identical).
	RevokeIdempotent(jti string) (existed, alreadyRevoked bool)
}

const defaultTick = 30 * time.Second

type memStore struct {
	mu      sync.RWMutex
	live    map[string]*Token
	revoked map[string]struct{}
	tick    time.Duration
	nowFn   func() time.Time
}

// NewStore returns an in-memory Store with a 30 s Cleanup tick and a
// time.Now-based clock. Callers MUST eventually invoke
// Cleanup(ctx) from a goroutine to reclaim expired records.
func NewStore() Store {
	return &memStore{
		live:    make(map[string]*Token),
		revoked: make(map[string]struct{}),
		tick:    defaultTick,
		nowFn:   time.Now,
	}
}

// NewStoreWithTick returns an in-memory Store with a caller-supplied
// Cleanup tick interval. Reserved for tests that need deterministic
// sweep observation.
func NewStoreWithTick(d time.Duration) Store {
	return &memStore{
		live:    make(map[string]*Token),
		revoked: make(map[string]struct{}),
		tick:    d,
		nowFn:   time.Now,
	}
}

func (s *memStore) Add(t *Token) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, revoked := s.revoked[t.JTI]; revoked {
		return ErrTokenRevoked
	}
	s.live[t.JTI] = t
	return nil
}

func (s *memStore) Get(jti string) (*Token, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, revoked := s.revoked[jti]; revoked {
		return nil, ErrTokenRevoked
	}
	t, ok := s.live[jti]
	if !ok {
		return nil, ErrTokenRevoked
	}
	return t, nil
}

func (s *memStore) ConsumeUse(jti string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, revoked := s.revoked[jti]; revoked {
		return ErrTokenRevoked
	}
	t, ok := s.live[jti]
	if !ok {
		return ErrTokenRevoked
	}
	if !t.ExpiresAt.After(s.nowFn()) {
		return ErrTokenExpired
	}
	if t.SessionType == SessionSupervisor {
		return nil
	}
	if t.MaxUses == 0 {
		return ErrTokenExhausted
	}
	t.MaxUses--
	return nil
}

func (s *memStore) Cleanup(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepExpired()
		}
	}
}

// ActiveCount returns the number of live, non-expired entries currently in
// the store. Revoked entries are excluded; expired-but-not-yet-swept entries
// are excluded. Read under RLock — concurrent-safe.
func (s *memStore) ActiveCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.nowFn()
	n := 0
	for _, t := range s.live {
		if t.ExpiresAt.After(now) {
			n++
		}
	}
	return n
}

// RevokeIdempotent marks jti revoked. Returns (existed, alreadyRevoked):
//
//	existed=false → unknown JTI; the call is a no-op (no entry added to
//	  the revoked set; anti-enumeration is handled at the handler layer).
//	existed=true alreadyRevoked=false → first-time success.
//	existed=true alreadyRevoked=true  → idempotent re-revoke.
func (s *memStore) RevokeIdempotent(jti string) (existed, alreadyRevoked bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.revoked[jti]; ok {
		return true, true
	}
	if _, ok := s.live[jti]; ok {
		s.revoked[jti] = struct{}{}
		delete(s.live, jti)
		return true, false
	}
	return false, false
}

func (s *memStore) sweepExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	for jti, t := range s.live {
		if !t.ExpiresAt.After(now) {
			delete(s.live, jti)
		}
	}
}
