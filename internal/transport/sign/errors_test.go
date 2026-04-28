package sign

import (
	"errors"
	"strings"
	"testing"
)

var allSentinels = []error{ //nolint:gochecknoglobals // test-only sentinel list
	ErrSignatureInvalid,
	ErrNonceReplay,
	ErrNonceEncoding,
	ErrNonceTTLInvalid,
	ErrTimestampStale,
	ErrCanonicalUnsupported,
}

// TestSentinels_AreDistinct asserts no two sentinels match each other via errors.Is.
func TestSentinels_AreDistinct(t *testing.T) {
	t.Parallel()
	for i, a := range allSentinels {
		for j, b := range allSentinels {
			if i == j {
				continue
			}
			if errors.Is(a, b) {
				t.Errorf("errors.Is(%v, %v) = true — sentinels must be distinct", a, b)
			}
		}
	}
}

// TestSentinels_MessagePrefix asserts all sentinels start with the package prefix.
func TestSentinels_MessagePrefix(t *testing.T) {
	t.Parallel()
	const prefix = "hush/transport/sign: "
	for _, s := range allSentinels {
		if !strings.HasPrefix(s.Error(), prefix) {
			t.Errorf("sentinel %q does not start with %q", s.Error(), prefix)
		}
	}
}

// TestSentinels_WrappedAreMatchable confirms that a wrapped sentinel still
// matches via errors.Is (the Verify return path wraps ErrSignatureInvalid).
func TestSentinels_WrappedAreMatchable(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	priv := generateFuzzKey(t)
	err := Verify(ctx, &priv.PublicKey, []byte("payload"), []byte("badsig"))
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("wrapped ErrSignatureInvalid not matchable via errors.Is: %v", err)
	}
}
