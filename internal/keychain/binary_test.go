package keychain

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeFixedBinary_RawPayload(t *testing.T) {
	t.Parallel()
	raw := []byte{
		0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
		0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
		0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
		0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
	}
	got, err := DecodeFixedBinary(raw, 32)
	require.NoError(t, err)
	require.Equal(t, raw, got)
}

func TestDecodeFixedBinary_HexLowercase(t *testing.T) {
	t.Parallel()
	// 32 zero bytes hex-encoded.
	hex32 := []byte("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	got, err := DecodeFixedBinary(hex32, 32)
	require.NoError(t, err)
	require.Len(t, got, 32)
	require.Equal(t, byte(0x01), got[0])
	require.Equal(t, byte(0x20), got[31])
}

func TestDecodeFixedBinary_HexUppercase(t *testing.T) {
	t.Parallel()
	hex32 := []byte("FFFEABCD01020304414141414141414141414141414141414141414141414141")
	got, err := DecodeFixedBinary(hex32, 32)
	require.NoError(t, err)
	require.Len(t, got, 32)
	require.Equal(t, byte(0xff), got[0])
	require.Equal(t, byte(0xfe), got[1])
	require.Equal(t, byte('A'), got[8])
}

func TestDecodeFixedBinary_HexMixedCase(t *testing.T) {
	t.Parallel()
	// 64 chars, mixed case — must round-trip to the expected 32 raw bytes.
	hex32 := []byte("aAbBcCdDeEfF0011223344556677889900112233445566778899aabbccddeeff")
	got, err := DecodeFixedBinary(hex32, 32)
	require.NoError(t, err)
	require.Len(t, got, 32)
	require.Equal(t, byte(0xaa), got[0])
	require.Equal(t, byte(0xbb), got[1])
}

func TestDecodeFixedBinary_HexOddLengthRejected(t *testing.T) {
	t.Parallel()
	// 63 chars — neither raw 32 nor 64 hex.
	odd := []byte("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f2")
	_, err := DecodeFixedBinary(odd, 32)
	require.ErrorIs(t, err, ErrBinaryPayloadLength)
}

func TestDecodeFixedBinary_WrongLength(t *testing.T) {
	t.Parallel()
	_, err := DecodeFixedBinary([]byte("too short"), 32)
	require.ErrorIs(t, err, ErrBinaryPayloadLength)
}

func TestDecodeFixedBinary_DoubleLengthButNotHex(t *testing.T) {
	t.Parallel()
	// 64 bytes, but contains non-hex characters — must NOT be silently treated as hex.
	payload := make([]byte, 64)
	for i := range payload {
		payload[i] = 'z'
	}
	_, err := DecodeFixedBinary(payload, 32)
	require.ErrorIs(t, err, ErrBinaryPayloadLength)
}

func TestDecodeFixedBinary_ZeroWantRejected(t *testing.T) {
	t.Parallel()
	_, err := DecodeFixedBinary([]byte{}, 0)
	require.ErrorIs(t, err, ErrBinaryPayloadLength)
}

func TestDecodeFixedBinary_NegativeWantRejected(t *testing.T) {
	t.Parallel()
	_, err := DecodeFixedBinary([]byte("aa"), -1)
	require.ErrorIs(t, err, ErrBinaryPayloadLength)
}

func TestDecodeFixedBinary_PreservesInputOnRawPath(t *testing.T) {
	t.Parallel()
	raw := []byte{0x01, 0x02, 0x03, 0x04}
	got, err := DecodeFixedBinary(raw, 4)
	require.NoError(t, err)
	got[0] = 0xff
	require.Equal(t, byte(0x01), raw[0], "DecodeFixedBinary must return a copy, not alias the input")
}

func TestDecodeFixedBinary_ErrorWrappingIsStable(t *testing.T) {
	t.Parallel()
	_, err := DecodeFixedBinary([]byte("00"), 32)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrBinaryPayloadLength))
}
