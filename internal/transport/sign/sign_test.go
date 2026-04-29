package sign

import (
	"context"
	"errors"
	"testing"
)

func TestSign_VerifyRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	priv := generateFuzzKey(t)

	canonical, err := CanonicalJSON(map[string]any{"action": "claim", "nonce": "abc12345"})
	if err != nil {
		t.Fatal(err)
	}

	sig, err := Sign(ctx, priv, canonical)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if err := Verify(ctx, &priv.PublicKey, canonical, sig); err != nil {
		t.Errorf("Verify round-trip: %v", err)
	}
}

func TestSign_RespectsCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	priv := generateFuzzKey(t)

	_, err := Sign(ctx, priv, []byte("payload"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestSign_RejectsNilKey(t *testing.T) {
	t.Parallel()
	_, err := Sign(t.Context(), nil, []byte("payload"))
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("expected ErrSignatureInvalid, got %v", err)
	}
}

func TestVerify_RejectsNilKey(t *testing.T) {
	t.Parallel()
	err := Verify(t.Context(), nil, []byte("payload"), []byte("sig"))
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("expected ErrSignatureInvalid, got %v", err)
	}
}
