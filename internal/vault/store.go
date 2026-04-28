package vault

import (
	"errors"
	"fmt"
	"sync"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// sbDestroyFn is the active SecureBytes destroy bridge; replaceable in tests
// to simulate munlock failures (same pattern as securebytes.mlockFn/munlockFn).
//
//nolint:gochecknoglobals // SecureBytes destroy bridge; test-hookable for munlock error-path coverage
var sbDestroyFn = (*securebytes.SecureBytes).Destroy

// sbNewFn is the securebytes.New bridge used by Get; replaceable in tests
// to simulate mlock-failure on the per-request copy allocation.
//
//nolint:gochecknoglobals // securebytes.New bridge; test-hookable for mlock-failure path coverage
var sbNewFn = securebytes.New

// memStore is the concrete implementation of Store returned by Load.
type memStore struct {
	mu        sync.RWMutex
	names     []string
	byName    map[string]*securebytes.SecureBytes
	destroyed bool
}

// newMemStore constructs a memStore from a slice of decoded wire secrets.
func newMemStore(wires []wireSecret) *memStore {
	s := &memStore{
		names:  make([]string, 0, len(wires)),
		byName: make(map[string]*securebytes.SecureBytes, len(wires)),
	}
	for _, w := range wires {
		s.names = append(s.names, w.Name)
		s.byName[w.Name] = w.Value.sb
	}
	return s
}

// Get returns a fresh, independently-owned *SecureBytes for the named secret.
func (s *memStore) Get(name string) (*securebytes.SecureBytes, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.destroyed {
		return nil, fmt.Errorf("vault: %w", ErrStoreDestroyed)
	}

	inner, ok := s.byName[name]
	if !ok {
		return nil, fmt.Errorf("vault: %w", ErrSecretNotFound)
	}

	// Copy the payload out of the inner container into a fresh one.
	// Use() returns ErrDestroyed or nil; any error maps to ErrStoreDestroyed
	// (concurrent Destroy raced with this Get).
	var buf []byte
	if err := inner.Use(func(b []byte) {
		buf = make([]byte, len(b))
		copy(buf, b)
	}); err != nil {
		return nil, fmt.Errorf("vault: %w", ErrStoreDestroyed)
	}

	fresh, err := sbNewFn(buf)
	if err != nil {
		return nil, fmt.Errorf("vault: securebytes.New: %w", err)
	}
	return fresh, nil
}

// Names returns a defensive copy of the secret names in their original load order.
func (s *memStore) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.names...)
}

// Destroy zeroes every internally-held *SecureBytes and marks the store destroyed.
func (s *memStore) Destroy() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.destroyed {
		return nil
	}
	s.destroyed = true

	var errs []error
	for _, sb := range s.byName {
		if err := sbDestroyFn(sb); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
