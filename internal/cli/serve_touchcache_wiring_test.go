package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/mrz1836/go-tumbler/transport"
	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/mrz1836/hush/internal/vault/securebytes"
	"github.com/mrz1836/hush/internal/yubikey"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingSeedUnlocker is a masterSeedUnlocker that returns a fixed seed and
// counts unlocks (each unlock == one "touch").
type countingSeedUnlocker struct {
	seed    []byte
	touches int
}

func (c *countingSeedUnlocker) Unlock(_ context.Context, _ []byte, _ func() ([]byte, error)) ([]byte, error) {
	c.touches++
	return append([]byte(nil), c.seed...), nil
}

// enrollKeyslots writes a real keyslots sidecar under dir and returns the
// enrolled random master seed. yubikey-only is used for a fast enroll (no
// Argon2id) unless a passphrase policy is requested.
func enrollKeyslots(t *testing.T, dir string, policy tumbler.Policy) []byte {
	t.Helper()
	store := yubikey.NewStore(transport.NewFakeTransport(2, []byte(enrolledFixtureFakeSecret)), 2)
	opts := yubikey.EnrollOptions{}
	if policy == tumbler.PolicyPasswordAndYubiKey {
		opts.Passphrase = []byte("enroll-pass")
	}
	res, err := store.Enroll(context.Background(), policy, opts)
	require.NoError(t, err)
	require.NoError(t, keyslots.Save(dir, res.Envelope, policy.String()))
	return res.MasterSeed
}

// touchWiringFixture bundles an enrolled state dir + cache-enabled config +
// injected seams for exercising serveRecoverMasterSeed on Linux CI.
type touchWiringFixture struct {
	cfg     *config.Server
	deps    serveDeps
	dir     string
	salt    []byte
	seed    []byte
	kc      *keychain.FakeKeychain
	counter *countingSeedUnlocker
	nowRef  *time.Time
}

func newTouchWiringFixture(t *testing.T, policy tumbler.Policy) *touchWiringFixture {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	seed := enrollKeyslots(t, dir, policy)

	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	now := time.Unix(1_700_000_000, 0)
	counter := &countingSeedUnlocker{seed: seed}

	cfg := &config.Server{}
	cfg.YubiKey.CacheTouch = true
	cfg.YubiKey.CacheTouchTTL = time.Hour

	deps := serveDeps{
		newUnlocker:     func(_ func()) (masterSeedUnlocker, error) { return counter, nil },
		keychainFactory: func() (keychain.Keychain, error) { return kc, nil },
		now:             func() time.Time { return now },
		platformACL:     func() bool { return true },
		binaryPath:      func() (string, error) { return "/test/hush", nil },
	}

	fx := &touchWiringFixture{cfg: cfg, deps: deps, dir: dir, salt: make([]byte, 16), seed: seed, kc: kc, counter: counter}
	fx.nowRef = &now
	return fx
}

func (fx *touchWiringFixture) recover(t *testing.T) (seed []byte, outcome string) {
	t.Helper()
	pass, err := securebytes.New([]byte("ignored-by-fake"))
	require.NoError(t, err)
	defer func() { _ = pass.Destroy() }()
	warn := func(string, ...any) {}
	s, _, out, err := serveRecoverMasterSeed(context.Background(), fx.cfg, fx.deps, fx.dir, fx.salt, pass, func() {}, warn)
	require.NoError(t, err)
	return s, out
}

func TestServeRecoverMasterSeed_ColdMissTouchesAndStores(t *testing.T) {
	fx := newTouchWiringFixture(t, tumbler.PolicyYubiKeyOnly)

	seed, outcome := fx.recover(t)
	assert.Equal(t, touchCacheOutcomeStored, outcome)
	assert.Equal(t, 1, fx.counter.touches)
	assert.Equal(t, fx.seed, seed)
	zeroBytes(seed)
}

func TestServeRecoverMasterSeed_HitSkipsTouch(t *testing.T) {
	fx := newTouchWiringFixture(t, tumbler.PolicyYubiKeyOnly)

	// Prime the cache.
	s1, _ := fx.recover(t)
	zeroBytes(s1)
	require.Equal(t, 1, fx.counter.touches)

	// Second recovery must hit the cache — no additional touch.
	s2, outcome := fx.recover(t)
	assert.Equal(t, touchCacheOutcomeHit, outcome)
	assert.Equal(t, 1, fx.counter.touches, "a cache hit must not touch the key")
	assert.Equal(t, fx.seed, s2)
	zeroBytes(s2)
}

func TestServeRecoverMasterSeed_ExpiredReTouchesAndReStores(t *testing.T) {
	fx := newTouchWiringFixture(t, tumbler.PolicyYubiKeyOnly)

	s1, _ := fx.recover(t)
	zeroBytes(s1)
	require.Equal(t, 1, fx.counter.touches)

	// Advance past the 1h TTL — the stale token is dropped and a fresh touch
	// re-stores.
	*fx.nowRef = fx.nowRef.Add(2 * time.Hour)
	s2, outcome := fx.recover(t)
	assert.Equal(t, touchCacheOutcomeStored, outcome)
	assert.Equal(t, 2, fx.counter.touches)
	zeroBytes(s2)
}

func TestServeRecoverMasterSeed_FlagOff_TouchesNoKeychain(t *testing.T) {
	fx := newTouchWiringFixture(t, tumbler.PolicyYubiKeyOnly)
	fx.deps.noCache = true // --no-cache

	seed, _, outcome, err := serveRecoverMasterSeed(context.Background(), fx.cfg, fx.deps, fx.dir, fx.salt,
		mustSB(t, "x"), func() {}, func(string, ...any) {})
	require.NoError(t, err)
	assert.Equal(t, touchCacheOutcomeUnlocked, outcome)
	assert.Equal(t, 1, fx.counter.touches)
	zeroBytes(seed)

	// The keychain was never written to.
	_, rerr := fx.kc.Retrieve(context.Background(), touchCacheService, touchCacheAccount)
	assert.ErrorIs(t, rerr, keychain.ErrKeychainItemNotFound)
}

func TestServeRecoverMasterSeed_StoreFailure_OutcomeStoreFailed(t *testing.T) {
	fx := newTouchWiringFixture(t, tumbler.PolicyYubiKeyOnly)
	fx.deps.keychainFactory = func() (keychain.Keychain, error) {
		return failingStoreKeychain{FakeKeychain: fx.kc, storeErr: keychain.ErrKeychainPermissionDenied}, nil
	}

	seed, outcome := fx.recover(t)
	assert.Equal(t, touchCacheOutcomeStoreFailed, outcome)
	assert.Equal(t, fx.seed, seed, "a store failure is non-fatal; the seed is still returned")
	zeroBytes(seed)
}

func TestServeRecoverMasterSeed_PasswordYubiKey_NoWeakestWarning(t *testing.T) {
	// A healthy password-and-yubikey posture caches without the yubikey-only
	// weakest-posture warning (also exercises the 2FA enroll path).
	fx := newTouchWiringFixture(t, tumbler.PolicyPasswordAndYubiKey)
	var msgs []string
	warn := func(f string, a ...any) { msgs = append(msgs, fmt.Sprintf(f, a...)) }

	seed, _, outcome, err := serveRecoverMasterSeed(context.Background(), fx.cfg, fx.deps, fx.dir, fx.salt,
		mustSB(t, "x"), func() {}, warn)
	require.NoError(t, err)
	assert.Equal(t, touchCacheOutcomeStored, outcome)
	zeroBytes(seed)
	assert.NotContains(t, strings.Join(msgs, ""), "yubikey-only", "2FA posture must not emit the weakest-posture warning")
}

// ---- newTouchCache gating ----

func TestNewTouchCache_Gating(t *testing.T) {
	base := newTouchWiringFixture(t, tumbler.PolicyYubiKeyOnly)
	noEnvDir := t.TempDir()
	require.NoError(t, os.Chmod(noEnvDir, 0o700))

	t.Run("disabled in config", func(t *testing.T) {
		cfg := &config.Server{}
		cfg.YubiKey.CacheTouch = false
		assert.Nil(t, newTouchCache(cfg, base.deps, base.dir, base.salt, func(string, ...any) {}))
	})
	t.Run("no-cache flag", func(t *testing.T) {
		deps := base.deps
		deps.noCache = true
		assert.Nil(t, newTouchCache(base.cfg, deps, base.dir, base.salt, func(string, ...any) {}))
	})
	t.Run("no keyslots envelope", func(t *testing.T) {
		assert.Nil(t, newTouchCache(base.cfg, base.deps, noEnvDir, base.salt, func(string, ...any) {}))
	})
	t.Run("unsupported platform warns", func(t *testing.T) {
		deps := base.deps
		deps.platformACL = func() bool { return false }
		var msgs []string
		warn := func(f string, a ...any) { msgs = append(msgs, f) }
		assert.Nil(t, newTouchCache(base.cfg, deps, base.dir, base.salt, warn))
		assert.Contains(t, strings.Join(msgs, ""), "unsupported")
	})
	t.Run("all gates pass", func(t *testing.T) {
		assert.NotNil(t, newTouchCache(base.cfg, base.deps, base.dir, base.salt, func(string, ...any) {}))
	})
}

func mustSB(t *testing.T, s string) *securebytes.SecureBytes {
	t.Helper()
	sb, err := securebytes.New([]byte(s))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Destroy() })
	return sb
}
