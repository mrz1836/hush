//go:build linux

package keychain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zalando/go-keyring"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// keyringBackend abstracts the zalando/go-keyring package functions
// so tests can inject a fake.
type keyringBackend interface {
	Set(service, account, value string) error
	Get(service, account string) (string, error)
	Delete(service, account string) error
}

// realKeyring is the production backend wrapping zalando/go-keyring.
type realKeyring struct{}

func (realKeyring) Set(service, account, value string) error {
	return keyring.Set(service, account, value)
}

func (realKeyring) Get(service, account string) (string, error) {
	return keyring.Get(service, account)
}
func (realKeyring) Delete(service, account string) error { return keyring.Delete(service, account) }

// linuxKeychain is the freedesktop.org Secret Service implementation.
// The acl argument is intentionally discarded — Linux Secret Service
// has no per-binary ACL primitive. Production callers MUST gate via
// PerBinaryACLSupported() (which returns false on linux) before
// invoking Store.
type linuxKeychain struct {
	logger  *slog.Logger
	backend keyringBackend
}

func newPlatformKeychain(logger *slog.Logger) (Keychain, error) {
	return &linuxKeychain{logger: logger, backend: realKeyring{}}, nil
}

// Store stores the secret value via the Secret Service backend. The
// `acl` argument is documented as discarded; production code never
// reaches Store on Linux because init refuses up-front.
func (k *linuxKeychain) Store(_ context.Context, service, account string, value *securebytes.SecureBytes, _ string) error {
	var setErr error
	if useErr := value.Use(func(b []byte) {
		// String conversion confined to this stack-local; never
		// logged (Constitution X residual-risk note in
		// docs/SECURITY.md §6).
		setErr = k.backend.Set(service, account, string(b))
	}); useErr != nil {
		return useErr
	}
	if setErr != nil {
		return fmt.Errorf("hush/keychain: keyring set: %w", setErr)
	}
	return nil
}

// Retrieve fetches the stored secret via the Secret Service backend.
func (k *linuxKeychain) Retrieve(_ context.Context, service, account string) (*securebytes.SecureBytes, error) {
	v, err := k.backend.Get(service, account)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return nil, ErrKeychainItemNotFound
		}
		return nil, fmt.Errorf("hush/keychain: keyring get: %w", err)
	}
	return securebytes.New([]byte(v))
}

// Delete removes the keychain item via the Secret Service backend.
func (k *linuxKeychain) Delete(_ context.Context, service, account string) error {
	if err := k.backend.Delete(service, account); err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return ErrKeychainItemNotFound
		}
		return fmt.Errorf("hush/keychain: keyring delete: %w", err)
	}
	return nil
}
