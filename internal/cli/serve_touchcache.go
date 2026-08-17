package cli

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	tumbler "github.com/mrz1836/go-tumbler"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// YubiKey touch-cache token layout (fixed 109 bytes):
//
//	magic("HYT1") ‖ version(1) ‖ expiry_unix(8, big-endian) ‖ bind_tag(32) ‖ seed(64)
//
// bind_tag = SHA-256(keyslots_envelope ‖ vault_salt) binds the token to the
// exact enrollment + salt it was minted under, so a token from a prior
// enrollment or a post-rekey envelope is rejected. The seed is the raw 64-byte
// master seed — an honest "seed-equivalent"; encrypting it under a key that also
// lives in the Keychain would be theater (same ACL boundary).
const (
	touchTokenMagic   = "HYT1"
	touchTokenVersion = byte(1)
	touchTokenSeedLen = 64
	touchTokenBindLen = 32
	touchTokenLen     = 4 + 1 + 8 + touchTokenBindLen + touchTokenSeedLen // 109

	// Dedicated Keychain item — separate from the operator-managed
	// hush-vault-passphrase item so the rekey runbook's delete/recreate never
	// clobbers it. Same per-binary ACL (os.Executable()).
	touchCacheService = "hush-vault-yubikey-token"
	touchCacheAccount = "hush-server"
)

// Token codec sentinels. Any of these on read → best-effort Delete + fall back
// to a real touch (fail-closed).
var (
	errTouchTokenMalformed    = errors.New("hush: serve: touch token malformed")
	errTouchTokenBindMismatch = errors.New("hush: serve: touch token bind mismatch")
	errTouchTokenExpired      = errors.New("hush: serve: touch token expired")
)

// computeBindTag returns SHA-256(envelope ‖ salt).
func computeBindTag(envelope, salt []byte) [32]byte {
	h := sha256.New()
	h.Write(envelope)
	h.Write(salt)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// encodeTouchToken builds the fixed 109-byte token. seed must be 64 bytes.
func encodeTouchToken(seed []byte, expiryUnix int64, bindTag [32]byte) ([]byte, error) {
	if len(seed) != touchTokenSeedLen {
		return nil, fmt.Errorf("%w: seed len %d", errTouchTokenMalformed, len(seed))
	}
	buf := make([]byte, 0, touchTokenLen)
	buf = append(buf, touchTokenMagic...)
	buf = append(buf, touchTokenVersion)
	buf = binary.BigEndian.AppendUint64(buf, uint64(expiryUnix)) //nolint:gosec // G115: expiry is now+ttl, always positive

	buf = append(buf, bindTag[:]...)
	buf = append(buf, seed...)
	return buf, nil
}

// decodeTouchToken validates a token against the expected bind tag and the
// current time, returning a fresh copy of the seed. Any structural, bind, or
// expiry failure returns an error so the caller drops the item and re-touches.
func decodeTouchToken(raw []byte, wantBind [32]byte, nowUnix int64) ([]byte, error) {
	if len(raw) != touchTokenLen {
		return nil, fmt.Errorf("%w: len %d", errTouchTokenMalformed, len(raw))
	}
	if string(raw[0:4]) != touchTokenMagic {
		return nil, fmt.Errorf("%w: magic", errTouchTokenMalformed)
	}
	if raw[4] != touchTokenVersion {
		return nil, fmt.Errorf("%w: version %d", errTouchTokenMalformed, raw[4])
	}
	expiry := int64(binary.BigEndian.Uint64(raw[5:13])) //nolint:gosec // G115: unix seconds fit int64
	var gotBind [32]byte
	copy(gotBind[:], raw[13:13+touchTokenBindLen])
	if subtle.ConstantTimeCompare(gotBind[:], wantBind[:]) != 1 {
		return nil, errTouchTokenBindMismatch
	}
	if expiry <= nowUnix {
		return nil, errTouchTokenExpired
	}
	seed := make([]byte, touchTokenSeedLen)
	copy(seed, raw[13+touchTokenBindLen:touchTokenLen])
	return seed, nil
}

// Touch-cache outcomes recorded in the startup audit event.
const (
	touchCacheOutcomeHit         = "hit"          // recovered from cache; no touch
	touchCacheOutcomeStored      = "stored"       // touched, then cached
	touchCacheOutcomeStoreFailed = "store_failed" // touched; caching the token failed (non-fatal)
	touchCacheOutcomeUnlocked    = "unlocked"     // touched; cache inactive (nil)
)

// recoverMasterSeed recovers the master seed for runServe, using the opt-in
// touch cache when active. A valid cached token skips the touch entirely;
// otherwise it unlocks via the (possibly injected) unlocker and best-effort
// caches the result. It returns the seed (caller must zero), the touch cache (or
// nil), and the outcome for the startup audit event.
func serveRecoverMasterSeed(
	ctx context.Context,
	cfg *config.Server,
	deps serveDeps,
	stateDir string,
	salt []byte,
	passphrase *securebytes.SecureBytes,
	onTouch func(),
	warn func(string, ...any),
) ([]byte, *touchCache, string, error) {
	tc := newTouchCache(cfg, deps, stateDir, salt, warn)
	if tc != nil {
		if seed, hit := tc.lookup(ctx); hit {
			return seed, tc, touchCacheOutcomeHit, nil
		}
	}

	unlockerFn := deps.newUnlocker
	if unlockerFn == nil {
		unlockerFn = func(t func()) (masterSeedUnlocker, error) {
			return ykmanUnlockerFactory(defaultYkmanPath, defaultYkmanSlot, t)()
		}
	}
	seed, err := unlockMasterSeed(ctx, stateDir, passphrase, salt,
		func() (masterSeedUnlocker, error) { return unlockerFn(onTouch) })
	if err != nil {
		return nil, tc, "", err
	}
	outcome := touchCacheOutcomeUnlocked
	if tc != nil {
		if tc.store(ctx, seed) {
			outcome = touchCacheOutcomeStored
		} else {
			outcome = touchCacheOutcomeStoreFailed
		}
	}
	return seed, tc, outcome, nil
}

// newTouchCache builds the touch cache for runServe, or returns nil when the
// cache must not run — in which case the caller never constructs a keychain and
// the original touch path runs verbatim. It is nil unless ALL hold:
//   - cfg.YubiKey.CacheTouch is true and --no-cache was not passed;
//   - a keyslots envelope is enrolled (a password-only vault has no touch);
//   - the platform supports a per-binary ACL (darwin only).
//
// A keychain-construction or envelope-load failure disables the cache (with a
// warning) rather than failing serve — fail-closed to the touch path.
func newTouchCache(cfg *config.Server, deps serveDeps, stateDir string, salt []byte, warn func(string, ...any)) *touchCache {
	if !cfg.YubiKey.CacheTouch || deps.noCache {
		return nil
	}
	if !keyslots.Exists(stateDir) {
		return nil
	}
	platformACL, kcFactory, nowFn, binPath := resolveTouchCacheSeams(deps)
	if !platformACL() {
		warn("hush: serve: touch cache requested but per-binary ACL is unsupported on this platform; caching disabled\n")
		return nil
	}

	envelope, _, err := keyslots.Load(stateDir)
	if err != nil {
		warn("hush: serve: touch cache disabled (keyslots unreadable): %v\n", err)
		return nil
	}

	kc, err := kcFactory()
	if err != nil || kc == nil {
		warn("hush: serve: touch cache disabled (keychain unavailable): %v\n", err)
		return nil
	}

	return &touchCache{
		kc:          kc,
		service:     touchCacheService,
		account:     touchCacheAccount,
		bindTag:     computeBindTag(envelope, salt),
		ttl:         cfg.YubiKey.CacheTouchTTL,
		now:         nowFn,
		binaryPath:  binPath,
		yubikeyOnly: envelopeIsYubiKeyOnly(envelope),
		warn:        warn,
	}
}

// resolveTouchCacheSeams fills the touch-cache seams with their production
// defaults where a test left them nil.
func resolveTouchCacheSeams(deps serveDeps) (func() bool, func() (keychain.Keychain, error), func() time.Time, func() (string, error)) {
	platformACL := deps.platformACL
	if platformACL == nil {
		platformACL = keychain.PerBinaryACLSupported
	}
	kcFactory := deps.keychainFactory
	if kcFactory == nil {
		kcFactory = func() (keychain.Keychain, error) { return keychain.New(slog.Default()) }
	}
	nowFn := deps.now
	if nowFn == nil {
		nowFn = time.Now
	}
	binPath := deps.binaryPath
	if binPath == nil {
		binPath = os.Executable
	}
	return platformACL, kcFactory, nowFn, binPath
}

// envelopeIsYubiKeyOnly reports the authoritative posture, never trusting the
// sidecar's advisory policy hint.
func envelopeIsYubiKeyOnly(envelope []byte) bool {
	env, err := tumbler.ParseEnvelope(envelope)
	if err != nil {
		return false
	}
	return env.EffectivePolicy() == tumbler.PolicyYubiKeyOnly
}

// touchCache is the opt-in per-binary-ACL Keychain cache of the master seed. It
// lets a `serve` restart skip the YubiKey touch until an absolute TTL. It is
// constructed only when the operator has opted in AND the platform supports a
// per-binary ACL (see newTouchCache); when disabled the whole object is nil and
// the original touch path runs verbatim.
type touchCache struct {
	kc          keychain.Keychain
	service     string
	account     string
	bindTag     [32]byte
	ttl         time.Duration
	now         func() time.Time
	binaryPath  func() (string, error)
	yubikeyOnly bool // weakest posture: seed protected only by the host ACL
	warn        func(format string, args ...any)
}

// lookup returns the cached master seed (a fresh []byte the caller must zero) if
// a valid, bound, unexpired token exists. On any miss/failure it best-effort
// deletes the stale item and returns (nil, false) so the caller falls back to a
// real touch. A hit performs NO touch and NO ykman call.
func (c *touchCache) lookup(ctx context.Context) ([]byte, bool) {
	blob, err := c.kc.Retrieve(ctx, c.service, c.account)
	if err != nil {
		return nil, false // not found (cold start) or read error → touch
	}
	defer func() { _ = blob.Destroy() }()

	var seed []byte
	_ = blob.Use(func(raw []byte) {
		decoded, derr := decodeTouchToken(raw, c.bindTag, c.now().Unix())
		if derr != nil {
			return // seed stays nil → drop + fall back below
		}
		seed = decoded
	})
	if seed == nil {
		c.bestEffortDelete(ctx) // corrupt / expired / bind-mismatch
		return nil, false
	}
	if c.yubikeyOnly {
		c.warn("hush: serve: ⚠ yubikey-only touch cache HIT — for the TTL window the master seed is protected only by this binary's Keychain ACL (no second factor). Prefer password-and-yubikey.\n")
	}
	return seed, true
}

// store best-effort upserts the token for the given seed and reports whether it
// was cached. A Delete/Store failure is a non-fatal warning (the daemon still
// runs; the next restart just touches).
func (c *touchCache) store(ctx context.Context, seed []byte) bool {
	binPath, err := c.resolveBinaryPath()
	if err != nil {
		c.warn("hush: serve: touch cache store skipped: %v\n", err)
		return false
	}

	expiry := c.now().Add(min(c.ttl, config.MaxYubiKeyTouchTTL)).Unix()
	token, err := encodeTouchToken(seed, expiry, c.bindTag)
	if err != nil {
		c.warn("hush: serve: touch cache encode failed (non-fatal): %v\n", err)
		return false
	}
	tokenSB, err := securebytes.New(token) // copies + zeroes the plaintext token
	if err != nil {
		c.warn("hush: serve: touch cache wrap failed (non-fatal): %v\n", err)
		return false
	}
	defer func() { _ = tokenSB.Destroy() }()

	c.bestEffortDelete(ctx) // upsert: Store rejects an existing item
	if serr := c.kc.Store(ctx, c.service, c.account, tokenSB, binPath); serr != nil {
		c.warn("hush: serve: touch cache store failed (non-fatal): %v\n", serr)
		return false
	}
	if c.yubikeyOnly {
		c.warn("hush: serve: ⚠ yubikey-only master seed cached — protected only by this binary's Keychain ACL for up to %s. Prefer password-and-yubikey.\n", min(c.ttl, config.MaxYubiKeyTouchTTL))
	}
	return true
}

// resolveBinaryPath returns the per-binary ACL path (os.Executable in
// production). An empty binaryPath seam yields "" (no ACL restriction).
func (c *touchCache) resolveBinaryPath() (string, error) {
	if c.binaryPath == nil {
		return "", nil
	}
	return c.binaryPath()
}

// bestEffortDelete removes the cached item, treating a missing item as success.
func (c *touchCache) bestEffortDelete(ctx context.Context) {
	if err := c.kc.Delete(ctx, c.service, c.account); err != nil && !errors.Is(err, keychain.ErrKeychainItemNotFound) {
		c.warn("hush: serve: touch cache delete failed (non-fatal): %v\n", err)
	}
}
