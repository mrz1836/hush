package cli

import (
	"context"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/vault/securebytes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testSeed() []byte {
	s := make([]byte, touchTokenSeedLen)
	for i := range s {
		s[i] = byte(i + 1)
	}
	return s
}

func testBind(b byte) [32]byte {
	var out [32]byte
	for i := range out {
		out[i] = b
	}
	return out
}

// captureWarn returns a warn func plus a pointer to the accumulated output.
func captureWarn() (func(string, ...any), *[]string) {
	var msgs []string
	fn := func(format string, args ...any) { msgs = append(msgs, fmt.Sprintf(format, args...)) }
	return fn, &msgs
}

func newTestTouchCache(kc keychain.Keychain, bind [32]byte, ttl time.Duration, nowRef *time.Time, ykOnly bool, warn func(string, ...any)) *touchCache {
	return &touchCache{
		kc:          kc,
		service:     touchCacheService,
		account:     touchCacheAccount,
		bindTag:     bind,
		ttl:         ttl,
		now:         func() time.Time { return *nowRef },
		binaryPath:  func() (string, error) { return "/test/hush", nil },
		yubikeyOnly: ykOnly,
		warn:        warn,
	}
}

// ---- codec ----

func TestTouchToken_CodecRoundTrip(t *testing.T) {
	t.Parallel()
	seed := testSeed()
	bind := testBind(0xAB)
	now := int64(1_700_000_000)

	tok, err := encodeTouchToken(seed, now+3600, bind)
	require.NoError(t, err)
	require.Len(t, tok, touchTokenLen)
	require.Len(t, tok, 109)

	got, err := decodeTouchToken(tok, bind, now)
	require.NoError(t, err)
	assert.Equal(t, testSeed(), got)
}

func TestTouchToken_Decode_Rejections(t *testing.T) {
	t.Parallel()
	seed := testSeed()
	bind := testBind(0x11)
	now := int64(1_700_000_000)
	valid, err := encodeTouchToken(seed, now+3600, bind)
	require.NoError(t, err)

	t.Run("expired", func(t *testing.T) {
		_, err := decodeTouchToken(valid, bind, now+7200)
		assert.ErrorIs(t, err, errTouchTokenExpired)
	})
	t.Run("bind mismatch", func(t *testing.T) {
		_, err := decodeTouchToken(valid, testBind(0x22), now)
		assert.ErrorIs(t, err, errTouchTokenBindMismatch)
	})
	t.Run("truncated", func(t *testing.T) {
		_, err := decodeTouchToken(valid[:100], bind, now)
		assert.ErrorIs(t, err, errTouchTokenMalformed)
	})
	t.Run("bad magic", func(t *testing.T) {
		bad := append([]byte(nil), valid...)
		bad[0] = 'X'
		_, err := decodeTouchToken(bad, bind, now)
		assert.ErrorIs(t, err, errTouchTokenMalformed)
	})
	t.Run("bad version", func(t *testing.T) {
		bad := append([]byte(nil), valid...)
		bad[4] = 0x99
		_, err := decodeTouchToken(bad, bind, now)
		assert.ErrorIs(t, err, errTouchTokenMalformed)
	})
}

func TestTouchToken_Encode_BadSeedLen(t *testing.T) {
	t.Parallel()
	_, err := encodeTouchToken(make([]byte, 32), 0, testBind(0))
	assert.ErrorIs(t, err, errTouchTokenMalformed)
}

func TestComputeBindTag_Deterministic(t *testing.T) {
	t.Parallel()
	a := computeBindTag([]byte("envelope"), []byte("salt-16-bytes-xx"))
	b := computeBindTag([]byte("envelope"), []byte("salt-16-bytes-xx"))
	c := computeBindTag([]byte("envelope"), []byte("different-salt-x"))
	assert.Equal(t, a, b)
	assert.NotEqual(t, a, c)
}

// ---- touchCache ----

func TestTouchCache_StoreThenLookup_Hit(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	defer kc.Destroy()
	now := time.Unix(1_700_000_000, 0)
	warn, msgs := captureWarn()
	c := newTestTouchCache(kc, testBind(1), time.Hour, &now, false, warn)

	c.store(context.Background(), testSeed())
	got, ok := c.lookup(context.Background())
	require.True(t, ok, "valid token must hit")
	assert.Equal(t, testSeed(), got)
	assert.Equal(t, "/test/hush", kc.RecordedACL(touchCacheService, touchCacheAccount))
	assert.Empty(t, *msgs, "no warnings for a healthy password+yubikey cache")
}

func TestTouchCache_Lookup_ColdMiss(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	defer kc.Destroy()
	now := time.Unix(1_700_000_000, 0)
	warn, _ := captureWarn()
	c := newTestTouchCache(kc, testBind(1), time.Hour, &now, false, warn)

	got, ok := c.lookup(context.Background())
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestTouchCache_Lookup_Expired_DropsAndMisses(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	defer kc.Destroy()
	now := time.Unix(1_700_000_000, 0)
	warn, _ := captureWarn()
	c := newTestTouchCache(kc, testBind(1), time.Hour, &now, false, warn)

	c.store(context.Background(), testSeed())
	now = now.Add(2 * time.Hour) // past the 1h TTL
	got, ok := c.lookup(context.Background())
	assert.False(t, ok)
	assert.Nil(t, got)
	// Stale item was deleted.
	_, err := kc.Retrieve(context.Background(), touchCacheService, touchCacheAccount)
	assert.ErrorIs(t, err, keychain.ErrKeychainItemNotFound)
}

func TestTouchCache_Lookup_BindMismatch_DropsAndMisses(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	defer kc.Destroy()
	now := time.Unix(1_700_000_000, 0)
	warn, _ := captureWarn()

	// Store under bind A.
	storer := newTestTouchCache(kc, testBind(0xA), time.Hour, &now, false, warn)
	storer.store(context.Background(), testSeed())

	// Look up under bind B (e.g. after a rekey changed the envelope).
	looker := newTestTouchCache(kc, testBind(0xB), time.Hour, &now, false, warn)
	got, ok := looker.lookup(context.Background())
	assert.False(t, ok)
	assert.Nil(t, got)
	_, err := kc.Retrieve(context.Background(), touchCacheService, touchCacheAccount)
	assert.ErrorIs(t, err, keychain.ErrKeychainItemNotFound)
}

func TestTouchCache_Lookup_Corrupt_DropsAndMisses(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	defer kc.Destroy()
	now := time.Unix(1_700_000_000, 0)
	warn, _ := captureWarn()

	garbage, err := securebytes.New([]byte("not-a-valid-token"))
	require.NoError(t, err)
	require.NoError(t, kc.Store(context.Background(), touchCacheService, touchCacheAccount, garbage, "/test/hush"))
	_ = garbage.Destroy()

	c := newTestTouchCache(kc, testBind(1), time.Hour, &now, false, warn)
	got, ok := c.lookup(context.Background())
	assert.False(t, ok)
	assert.Nil(t, got)
	_, rerr := kc.Retrieve(context.Background(), touchCacheService, touchCacheAccount)
	assert.ErrorIs(t, rerr, keychain.ErrKeychainItemNotFound)
}

func TestTouchCache_TTLCap(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	defer kc.Destroy()
	now := time.Unix(1_700_000_000, 0)
	warn, _ := captureWarn()

	// Request a 10h TTL — the token must be capped to now+4h.
	c := newTestTouchCache(kc, testBind(1), 10*time.Hour, &now, false, warn)
	c.store(context.Background(), testSeed())

	blob, err := kc.Retrieve(context.Background(), touchCacheService, touchCacheAccount)
	require.NoError(t, err)
	defer func() { _ = blob.Destroy() }()
	require.NoError(t, blob.Use(func(raw []byte) {
		expiry := int64(binary.BigEndian.Uint64(raw[5:13]))
		assert.Equal(t, now.Add(4*time.Hour).Unix(), expiry, "expiry must be capped at now+4h")
	}))
}

// failingStoreKeychain wraps a fake and forces Store to fail.
type failingStoreKeychain struct {
	*keychain.FakeKeychain

	storeErr error
}

func (f failingStoreKeychain) Store(ctx context.Context, service, account string, value *securebytes.SecureBytes, acl string) error {
	if f.storeErr != nil {
		return f.storeErr
	}
	return f.FakeKeychain.Store(ctx, service, account, value, acl)
}

func TestTouchCache_StoreFailure_NonFatal(t *testing.T) {
	t.Parallel()
	fake := keychain.NewFake()
	defer fake.Destroy()
	kc := failingStoreKeychain{FakeKeychain: fake, storeErr: keychain.ErrKeychainPermissionDenied}
	now := time.Unix(1_700_000_000, 0)
	warn, msgs := captureWarn()
	c := newTestTouchCache(kc, testBind(1), time.Hour, &now, false, warn)

	// Must not panic; emits a non-fatal warning; nothing cached.
	c.store(context.Background(), testSeed())
	require.NotEmpty(t, *msgs)
	assert.Contains(t, (*msgs)[len(*msgs)-1], "store failed")

	got, ok := c.lookup(context.Background())
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestTouchCache_YubiKeyOnly_WarnsOnStoreAndHit(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	defer kc.Destroy()
	now := time.Unix(1_700_000_000, 0)
	warn, msgs := captureWarn()
	c := newTestTouchCache(kc, testBind(1), time.Hour, &now, true, warn)

	c.store(context.Background(), testSeed())
	require.NotEmpty(t, *msgs, "yubikey-only store must warn")
	assert.Contains(t, (*msgs)[len(*msgs)-1], "yubikey-only")

	_, ok := c.lookup(context.Background())
	require.True(t, ok)
	assert.Contains(t, (*msgs)[len(*msgs)-1], "yubikey-only touch cache HIT")
}
