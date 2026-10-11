package yubikey

import (
	"testing"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/mrz1836/go-tumbler/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPassphraseKDF_DefaultIsPinnedProductionCost pins the passphrase-factor
// cost a real store records in new 2FA slots (t=4, m=256 MiB, p=4) without
// running a derivation, so the cheap KDF the rest of the suite uses can never
// mask a weakened production default.
func TestPassphraseKDF_DefaultIsPinnedProductionCost(t *testing.T) {
	store := NewStore(transport.NewFakeTransport(2, []byte("device-hmac-secret-20")), 2)

	kdf := store.passphraseKDF()
	require.Equal(t, tumbler.KDFArgon2id, kdf.ID())
	// time(4) || memoryKiB(4) || threads(1), big-endian.
	assert.Equal(t, []byte{0, 0, 0, 4, 0, 4, 0, 0, 4}, kdf.MarshalParams())
}

func TestSetKDFForTesting_OverridesPassphraseKDF(t *testing.T) {
	store := NewStore(transport.NewFakeTransport(2, []byte("device-hmac-secret-20")), 2)
	cheap := tumbler.NewArgon2idKDF(1, 64, 1)

	store.SetKDFForTesting(cheap)
	assert.Same(t, cheap, store.passphraseKDF())
}
