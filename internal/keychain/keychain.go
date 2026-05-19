package keychain

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Keychain is the platform-agnostic OS keychain operations contract.
// All operations are atomic at the OS layer; implementations do not
// add internal serialization. Callers should serialize per-(service,
// account) operations themselves when concurrency is involved.
type Keychain interface {
	// Store creates a new keychain item under (service, account)
	// with the supplied secret and the supplied per-binary ACL.
	// Returns ErrKeychainItemExists if an item already exists.
	Store(ctx context.Context, service, account string, value *securebytes.SecureBytes, acl string) error

	// Retrieve fetches the secret value for (service, account) into
	// a fresh *securebytes.SecureBytes that the caller owns and
	// must Destroy. Returns ErrKeychainItemNotFound if no item
	// exists; ErrKeychainPermissionDenied on OS-level denial.
	Retrieve(ctx context.Context, service, account string) (*securebytes.SecureBytes, error)

	// Delete removes the item at (service, account). Returns
	// ErrKeychainItemNotFound if no item exists; non-idempotent at
	// the call-site level.
	Delete(ctx context.Context, service, account string) error
}

// DedicatedKeychainManager is implemented by path-aware macOS
// keychains that can create/unlock their backing file on demand.
// Callers only use it for the explicit hush-keychain escape hatch.
type DedicatedKeychainManager interface {
	EnsureDedicatedKeychain(ctx context.Context) error
}

// Sentinel errors returned by Keychain implementations.
var (
	// ErrKeychainItemNotFound is returned by Retrieve and Delete
	// when no item exists for (service, account).
	ErrKeychainItemNotFound = errors.New("hush/keychain: item not found")

	// ErrKeychainItemExists is returned by Store when an item
	// already exists for (service, account).
	ErrKeychainItemExists = errors.New("hush/keychain: item already exists")

	// ErrKeychainPermissionDenied is returned when the OS keychain service
	// denied access (typically because the caller is not the binary named in
	// the item's ACL, or the user denied an access prompt).
	ErrKeychainPermissionDenied = errors.New("hush/keychain: permission denied")

	// ErrKeychainLocked is returned when the login keychain is locked or macOS
	// reports that the keychain passphrase was not accepted.
	ErrKeychainLocked = errors.New("hush/keychain: keychain locked")

	// ErrKeychainUnsupportedPlatform is returned by Store on
	// platforms without per-binary ACL semantics.
	ErrKeychainUnsupportedPlatform = errors.New("hush/keychain: per-binary ACL unsupported on this platform")
)

// New returns the platform-native Keychain implementation.
//
// logger is stored on the platform-specific struct and reserved for
// future audit hooks. Today the implementations do not log through it;
// no secret value is ever passed to the logger.
func New(logger *slog.Logger) (Keychain, error) {
	return NewAtPath(logger, "")
}

// NewAtPath returns the platform-native Keychain implementation and,
// on macOS, targets the supplied keychain file path when non-empty.
func NewAtPath(logger *slog.Logger, keychainPath string) (Keychain, error) {
	if logger == nil {
		logger = slog.Default()
	}
	return newPlatformKeychain(logger, keychainPath)
}

// PerBinaryACLSupported reports whether the current platform's
// keychain implementation honors the `acl` argument as a per-binary
// access restriction. Returns true on darwin, false otherwise.
//
// init.go MUST call this before any Store invocation and MUST refuse
// to write any keychain item, vault file, or config file when it
// returns false.
func PerBinaryACLSupported() bool {
	return runtime.GOOS == "darwin"
}

// storedItem is one record inside FakeKeychain.
type storedItem struct {
	value *securebytes.SecureBytes
	acl   string
}

// fakeKey identifies a (service, account) pair inside FakeKeychain.
type fakeKey struct {
	service string
	account string
}

// FakeKeychain is an in-process Keychain implementation backed by a
// map. Test-only — production code MUST NOT instantiate this type.
type FakeKeychain struct {
	mu    sync.Mutex
	items map[fakeKey]storedItem
}

// NewFake constructs an empty FakeKeychain.
func NewFake() *FakeKeychain {
	return &FakeKeychain{items: make(map[fakeKey]storedItem)}
}

// Store records value under (service, account) with the supplied
// ACL. Returns ErrKeychainItemExists if (service, account) is
// already populated.
func (f *FakeKeychain) Store(_ context.Context, service, account string, value *securebytes.SecureBytes, acl string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := fakeKey{service: service, account: account}
	if _, ok := f.items[k]; ok {
		return ErrKeychainItemExists
	}
	// Copy the secret bytes into a fresh *SecureBytes that the fake
	// owns. The original *SecureBytes ownership stays with the
	// caller per the Keychain contract.
	var copyErr error
	var fresh *securebytes.SecureBytes
	if useErr := value.Use(func(b []byte) {
		buf := make([]byte, len(b))
		copy(buf, b)
		fresh, copyErr = securebytes.New(buf)
	}); useErr != nil {
		return useErr
	}
	if copyErr != nil {
		return copyErr
	}
	f.items[k] = storedItem{value: fresh, acl: acl}
	return nil
}

// Retrieve returns a fresh *securebytes.SecureBytes containing a
// copy of the stored value. The caller owns the result and must
// Destroy it.
func (f *FakeKeychain) Retrieve(_ context.Context, service, account string) (*securebytes.SecureBytes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := fakeKey{service: service, account: account}
	rec, ok := f.items[k]
	if !ok {
		return nil, ErrKeychainItemNotFound
	}
	var out *securebytes.SecureBytes
	var newErr error
	if useErr := rec.value.Use(func(b []byte) {
		buf := make([]byte, len(b))
		copy(buf, b)
		out, newErr = securebytes.New(buf)
	}); useErr != nil {
		return nil, useErr
	}
	if newErr != nil {
		return nil, newErr
	}
	return out, nil
}

// Delete removes the stored item. Returns ErrKeychainItemNotFound
// when no item exists for (service, account).
func (f *FakeKeychain) Delete(_ context.Context, service, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := fakeKey{service: service, account: account}
	rec, ok := f.items[k]
	if !ok {
		return ErrKeychainItemNotFound
	}
	_ = rec.value.Destroy()
	delete(f.items, k)
	return nil
}

// Destroy zeroes every stored *securebytes.SecureBytes and clears
// the fake's storage. Safe to call more than once.
func (f *FakeKeychain) Destroy() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, rec := range f.items {
		_ = rec.value.Destroy()
		delete(f.items, k)
	}
}

// RecordedACL returns the ACL string supplied to the most recent
// Store call for (service, account). Returns "" if no item exists.
func (f *FakeKeychain) RecordedACL(service, account string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.items[fakeKey{service: service, account: account}]
	if !ok {
		return ""
	}
	return rec.acl
}
