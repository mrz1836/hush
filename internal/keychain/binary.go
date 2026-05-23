package keychain

import (
	"encoding/hex"
	"errors"
	"fmt"
)

// ErrBinaryPayloadLength is returned by DecodeFixedBinary when the
// keychain payload is neither the expected raw length nor a hex
// encoding of that length.
var ErrBinaryPayloadLength = errors.New("hush/keychain: binary payload length")

// DecodeFixedBinary returns the keychain payload as raw bytes of the
// expected length, tolerating one platform-quirk encoding.
//
// macOS `/usr/bin/security ... -w` is the wire underneath the Darwin
// keychain implementation. When the stored password contains any byte
// outside the printable ASCII range (0x20–0x7E), the tool emits the
// value as bare hex characters with no `0x` prefix (and 2x the byte
// length on the wire). Any payload Hush stores that is full-entropy
// binary — e.g. a 32-byte secp256k1 scalar — round-trips through that
// path as 2*N hex characters rather than N raw bytes. Callers that
// expect a fixed binary size therefore have to be tolerant of both
// shapes; DecodeFixedBinary centralizes that tolerance so each call
// site does not reinvent it.
//
// Returns:
//   - len(payload) == want                     → payload as-is
//   - len(payload) == 2*want, all hex digits   → hex.DecodeString result
//   - anything else                            → ErrBinaryPayloadLength
//
// payload is read but not modified; callers retain ownership.
func DecodeFixedBinary(payload []byte, want int) ([]byte, error) {
	if want <= 0 {
		return nil, fmt.Errorf("%w: want=%d must be positive", ErrBinaryPayloadLength, want)
	}
	switch len(payload) {
	case want:
		out := make([]byte, want)
		copy(out, payload)
		return out, nil
	case 2 * want:
		if !isHexBytes(payload) {
			return nil, fmt.Errorf("%w: %d bytes (not hex-encoded)", ErrBinaryPayloadLength, len(payload))
		}
		out := make([]byte, want)
		if _, err := hex.Decode(out, payload); err != nil {
			return nil, fmt.Errorf("%w: hex decode: %w", ErrBinaryPayloadLength, err)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: %d, want %d or %d", ErrBinaryPayloadLength, len(payload), want, 2*want)
	}
}

// isHexBytes reports whether every byte of b is a valid lowercase or
// uppercase hex digit. Empty input returns false (no payload).
func isHexBytes(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	for _, c := range b {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}
