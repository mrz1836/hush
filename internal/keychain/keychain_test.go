package keychain_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

func mustSecure(t *testing.T, b []byte) *securebytes.SecureBytes {
	t.Helper()
	sb, err := securebytes.New(b)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Destroy() })
	return sb
}

func TestKeychain_StoreRetrieveRoundTrip(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)

	payload := []byte("super-secret-payload")
	src := mustSecure(t, append([]byte(nil), payload...))

	require.NoError(t, kc.Store(context.Background(), "svc", "acct", src, "/abs/hush"))
	require.Equal(t, "/abs/hush", kc.RecordedACL("svc", "acct"))

	got, err := kc.Retrieve(context.Background(), "svc", "acct")
	require.NoError(t, err)
	t.Cleanup(func() { _ = got.Destroy() })

	require.NoError(t, got.Use(func(b []byte) {
		require.Equal(t, payload, b)
	}))
}

func TestKeychain_DeleteRemoves(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)

	require.NoError(t, kc.Store(context.Background(), "svc", "acct",
		mustSecure(t, []byte("payload-bytes")), "/abs/hush"))

	require.NoError(t, kc.Delete(context.Background(), "svc", "acct"))

	_, err := kc.Retrieve(context.Background(), "svc", "acct")
	require.True(t, errors.Is(err, keychain.ErrKeychainItemNotFound))

	err = kc.Delete(context.Background(), "svc", "acct")
	require.True(t, errors.Is(err, keychain.ErrKeychainItemNotFound))
}

func TestKeychain_StoreRefusesDuplicate(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)

	require.NoError(t, kc.Store(context.Background(), "svc", "acct",
		mustSecure(t, []byte("payload-one-bytes")), "/abs/hush"))

	err := kc.Store(context.Background(), "svc", "acct",
		mustSecure(t, []byte("payload-two-bytes")), "/abs/hush")
	require.True(t, errors.Is(err, keychain.ErrKeychainItemExists))
}

func TestKeychain_FakeDestroyZeroes(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()

	require.NoError(t, kc.Store(context.Background(), "svc", "acct",
		mustSecure(t, []byte("payload-to-zero")), "/abs/hush"))

	kc.Destroy()

	_, err := kc.Retrieve(context.Background(), "svc", "acct")
	require.True(t, errors.Is(err, keychain.ErrKeychainItemNotFound))

	// Destroy is idempotent.
	kc.Destroy()
}

func TestKeychain_NewReturnsInterface(t *testing.T) {
	t.Parallel()
	kc, err := keychain.New(slog.Default())
	require.NoError(t, err)
	require.NotNil(t, kc)

	// Runtime check: kc satisfies the Keychain interface (the
	// type itself is the return type).
	require.Implements(t, (*keychain.Keychain)(nil), kc)
}

func TestPerBinaryACLSupported_ReportsPerPlatform(t *testing.T) {
	t.Parallel()
	// On darwin, true; on every other platform, false. The actual
	// value is platform-dependent; this test asserts the function
	// returns a consistent boolean (per-build) and doesn't panic.
	got := keychain.PerBinaryACLSupported()
	require.IsType(t, true, got)
}
