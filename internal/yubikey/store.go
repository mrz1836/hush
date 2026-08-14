// Package yubikey wires hush's 64-byte master seed to the go-tumbler
// envelope. Because hush derives the master seed deterministically from the
// passphrase, YubiKey enrollment wraps a NEW random master seed (so the
// passphrase alone can no longer reconstruct it) and the vault is re-encrypted
// under that new seed. Raw YubiKey I/O is delegated to a tumbler transport
// (ykman in production, a fake in tests).
package yubikey

import (
	"context"
	"errors"
	"fmt"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/mrz1836/go-tumbler/securebytes"
	"github.com/mrz1836/go-tumbler/transport"
)

// Argon2id cost mirrors internal/keys/derive.go (t=4, m=256 MiB, p=4) so the
// passphrase factor keeps hush's established password-cracking resistance.
const (
	argon2Time    = 4
	argon2MemKiB  = 256 * 1024
	argon2Threads = 4
)

const defaultOTPSlot uint8 = 2

// Sentinel errors.
var (
	ErrPassphraseRequired = errors.New("yubikey: passphrase required for password-and-yubikey policy")
	ErrUnsupportedPolicy  = errors.New("yubikey: unsupported policy")
)

// Store builds and unlocks tumbler envelopes that wrap a hush master seed.
type Store struct {
	transport transport.Transport
	slot      uint8
	onTouch   func()
}

// NewStore builds a store from an explicit transport (tests inject a fake).
func NewStore(t transport.Transport, slot uint8) *Store {
	if slot == 0 {
		slot = defaultOTPSlot
	}
	return &Store{transport: t, slot: slot}
}

// SetTouchPrompt registers a callback fired at the exact moment the YubiKey
// starts blinking for a touch (after any passphrase prompt and key
// derivation), so the CLI can print "touch your key now" at the right time.
func (s *Store) SetTouchPrompt(fn func()) { s.onTouch = fn }

// NewStoreFromConfig builds a store backed by the real ykman CLI.
func NewStoreFromConfig(ykmanPath string, slot uint8) (*Store, error) {
	t, err := transport.NewYkmanTransport(ykmanPath)
	if err != nil {
		return nil, err
	}
	return NewStore(t, slot), nil
}

// EnrollOptions controls the primary factor and extra slots.
type EnrollOptions struct {
	// Passphrase is required for PolicyPasswordAndYubiKey, ignored for
	// PolicyYubiKeyOnly. The caller zeroes it after Enroll returns.
	Passphrase []byte
	// WithRecovery also enrolls a printed 256-bit recovery code.
	WithRecovery bool
}

// EnrollResult carries the new random master seed to re-encrypt the vault
// under, the marshaled envelope, and (if requested) the one-time recovery
// code. The caller MUST zero MasterSeed after re-encrypting the vault.
type EnrollResult struct {
	MasterSeed   []byte
	Envelope     []byte
	RecoveryCode string
}

// seedLen is hush's master-seed length (64 bytes).
const seedLen = 64

// Enroll generates a fresh random master seed, wraps it under policy, and
// returns the seed + marshaled envelope. The vault must then be re-encrypted
// under keys derived from EnrollResult.MasterSeed.
func (s *Store) Enroll(ctx context.Context, policy tumbler.Policy, opts EnrollOptions) (*EnrollResult, error) {
	dek, err := tumbler.GenerateDEK(seedLen)
	if err != nil {
		return nil, fmt.Errorf("yubikey: generate master seed: %w", err)
	}
	defer func() { _ = dek.Destroy() }()

	method, pwSB, err := s.primaryMethod(policy, opts.Passphrase)
	if err != nil {
		return nil, err
	}
	if pwSB != nil {
		defer func() { _ = pwSB.Destroy() }()
	}

	env, err := tumbler.NewEnvelope(ctx, dek, policy, method)
	if err != nil {
		return nil, fmt.Errorf("yubikey: build envelope: %w", err)
	}

	res := &EnrollResult{}
	if opts.WithRecovery {
		code, genErr := tumbler.GenerateRecoveryCode()
		if genErr != nil {
			return nil, genErr
		}
		defer func() { _ = code.Destroy() }()
		if addErr := env.AddSlot(ctx, dek, tumbler.NewRecoveryMethod(code)); addErr != nil {
			return nil, addErr
		}
		printed, fmtErr := tumbler.FormatRecoveryCode(code)
		if fmtErr != nil {
			return nil, fmtErr
		}
		res.RecoveryCode = printed
	}

	blob, err := env.Marshal()
	if err != nil {
		return nil, fmt.Errorf("yubikey: marshal envelope: %w", err)
	}
	res.Envelope = blob

	// Hand the new master seed back to the caller (fresh copy).
	if useErr := dek.Use(func(b []byte) {
		res.MasterSeed = cloneBytes(b)
	}); useErr != nil {
		return nil, useErr
	}
	return res, nil
}

// Unlock recovers the master seed from envelope using the primary factor for
// the envelope's effective policy. passphraseFn is invoked only when the
// policy needs a passphrase. The returned seed is a fresh []byte to zero.
func (s *Store) Unlock(ctx context.Context, envelope []byte, passphraseFn func() ([]byte, error)) ([]byte, error) {
	env, err := tumbler.ParseEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	method, pwSB, err := s.unlockMethod(env.EffectivePolicy(), passphraseFn)
	if err != nil {
		return nil, err
	}
	if pwSB != nil {
		defer func() { _ = pwSB.Destroy() }()
	}
	return unlockToSeed(ctx, env, method)
}

// UnlockWithRecovery recovers the master seed via a printed recovery code.
func (s *Store) UnlockWithRecovery(ctx context.Context, envelope []byte, recoveryCode string) ([]byte, error) {
	env, err := tumbler.ParseEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	code, err := tumbler.ParseRecoveryCode(recoveryCode)
	if err != nil {
		return nil, err
	}
	defer func() { _ = code.Destroy() }()
	return unlockToSeed(ctx, env, tumbler.NewRecoveryMethod(code))
}

// EffectivePolicy parses the envelope and returns its enforced policy.
func (s *Store) EffectivePolicy(envelope []byte) (tumbler.Policy, error) {
	env, err := tumbler.ParseEnvelope(envelope)
	if err != nil {
		return tumbler.PolicyInvalid, err
	}
	return env.EffectivePolicy(), nil
}

func (s *Store) primaryMethod(policy tumbler.Policy, passphrase []byte) (tumbler.Method, *securebytes.SecureBytes, error) {
	touch := tumbler.WithTouchAnnounce(s.onTouch)
	switch policy {
	case tumbler.PolicyYubiKeyOnly:
		return tumbler.NewYubiKeyMethod(s.transport, tumbler.YubiKeyConfig{Slot: s.slot}, nil, touch), nil, nil
	case tumbler.PolicyPasswordAndYubiKey:
		if len(passphrase) == 0 {
			return nil, nil, ErrPassphraseRequired
		}
		pw, err := securebytes.New(cloneBytes(passphrase))
		if err != nil {
			return nil, nil, err
		}
		m := tumbler.NewYubiKeyMethod(s.transport, tumbler.YubiKeyConfig{
			Slot: s.slot,
			KDF:  tumbler.NewArgon2idKDF(argon2Time, argon2MemKiB, argon2Threads),
		}, pw, touch)
		return m, pw, nil
	default:
		return nil, nil, fmt.Errorf("%w: %s", ErrUnsupportedPolicy, policy)
	}
}

func (s *Store) unlockMethod(policy tumbler.Policy, passphraseFn func() ([]byte, error)) (tumbler.Method, *securebytes.SecureBytes, error) {
	touch := tumbler.WithTouchAnnounce(s.onTouch)
	switch policy {
	case tumbler.PolicyYubiKeyOnly:
		return tumbler.NewYubiKeyMethod(s.transport, tumbler.YubiKeyConfig{}, nil, touch), nil, nil
	case tumbler.PolicyPasswordAndYubiKey:
		if passphraseFn == nil {
			return nil, nil, ErrPassphraseRequired
		}
		pw, err := passphraseFn()
		if err != nil {
			return nil, nil, err
		}
		pwSB, err := securebytes.New(cloneBytes(pw))
		zeroBytes(pw)
		if err != nil {
			return nil, nil, err
		}
		return tumbler.NewYubiKeyMethod(s.transport, tumbler.YubiKeyConfig{}, pwSB, touch), pwSB, nil
	default:
		return nil, nil, fmt.Errorf("%w: %s", ErrUnsupportedPolicy, policy)
	}
}

func unlockToSeed(ctx context.Context, env *tumbler.Envelope, method tumbler.Method) ([]byte, error) {
	seedSB, err := env.Unlock(ctx, method)
	if err != nil {
		return nil, err
	}
	defer func() { _ = seedSB.Destroy() }()
	var out []byte
	if useErr := seedSB.Use(func(b []byte) { out = cloneBytes(b) }); useErr != nil {
		return nil, useErr
	}
	return out, nil
}

func cloneBytes(b []byte) []byte {
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
