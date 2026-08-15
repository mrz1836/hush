package cli

import (
	"context"
	"fmt"

	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/mrz1836/hush/internal/vault/securebytes"
	"github.com/mrz1836/hush/internal/yubikey"
)

// Default ykman transport settings for hush. ykman is resolved on $PATH once;
// slot 2 is the conventional HMAC-SHA1 challenge-response slot.
const (
	defaultYkmanPath       = "ykman"
	defaultYkmanSlot uint8 = 2
)

// masterSeedUnlocker recovers the 64-byte master seed from a keyslots
// envelope. Implemented by *yubikey.Store; a fake is injected in tests.
type masterSeedUnlocker interface {
	Unlock(ctx context.Context, envelope []byte, passphraseFn func() ([]byte, error)) ([]byte, error)
}

// unlockMasterSeed is the single seed boundary shared by every command that
// needs the master seed. If a keyslots envelope is enrolled it recovers the
// (random) seed via the YubiKey/2FA envelope; otherwise it falls back to the
// legacy passphrase derivation, byte-identical to hush's original behavior.
//
// newUnlocker is invoked ONLY when an envelope exists, so password-only vaults
// never construct a ykman transport. It returns an error (e.g. ykman missing)
// which surfaces to the user rather than silently degrading.
func unlockMasterSeed(
	ctx context.Context,
	stateDir string,
	passphrase *securebytes.SecureBytes,
	salt []byte,
	newUnlocker func() (masterSeedUnlocker, error),
) ([]byte, error) {
	if !keyslots.Exists(stateDir) {
		return legacyDeriveMasterSeed(ctx, passphrase, salt)
	}

	envelope, _, err := keyslots.Load(stateDir)
	if err != nil {
		return nil, err
	}
	unlocker, err := newUnlocker()
	if err != nil {
		return nil, fmt.Errorf("this vault requires a YubiKey (is ykman installed?): %w", err)
	}

	// The store calls passphraseFn only when the envelope's policy needs it
	// (skipped entirely for yubikey-only). We hand it a fresh copy of the
	// passphrase bytes each call; the store zeroes them after use.
	passphraseFn := func() ([]byte, error) {
		var out []byte
		if useErr := passphrase.Use(func(b []byte) {
			out = make([]byte, len(b))
			copy(out, b)
		}); useErr != nil {
			return nil, useErr
		}
		return out, nil
	}
	return unlocker.Unlock(ctx, envelope, passphraseFn)
}

// legacyDeriveMasterSeed is the original passphrase -> Argon2id -> 64-byte seed
// path, unchanged. Kept as a named helper so the seam is obvious.
func legacyDeriveMasterSeed(ctx context.Context, passphrase *securebytes.SecureBytes, salt []byte) ([]byte, error) {
	var (
		seed []byte
		err  error
	)
	if useErr := passphrase.Use(func(b []byte) {
		seed, err = keys.DeriveMasterSeed(ctx, b, salt)
	}); useErr != nil {
		return nil, useErr
	}
	return seed, err
}

// ykmanUnlockerFactory returns a factory that builds a real ykman-backed
// unlocker from server config. onTouch (if non-nil) is fired at the exact
// moment the key blinks for a touch, so the caller can prompt the operator
// (ykman's own prompt is captured by the transport and not shown).
func ykmanUnlockerFactory(ykmanPath string, slot uint8, onTouch func()) func() (masterSeedUnlocker, error) {
	return func() (masterSeedUnlocker, error) {
		s, err := yubikey.NewStoreFromConfig(ykmanPath, slot)
		if err != nil {
			return nil, err
		}
		if onTouch != nil {
			s.SetTouchPrompt(onTouch)
		}
		return s, nil
	}
}
