package sign

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
)

func TestVerify_WrongKeyFails(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	keyA := generateFuzzKey(t)
	keyB := generateFuzzKey(t)

	canonical, _ := CanonicalJSON(map[string]any{"x": 1})
	sig, err := Sign(ctx, keyA, canonical)
	if err != nil {
		t.Fatal(err)
	}

	err = Verify(ctx, &keyB.PublicKey, canonical, sig)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("expected ErrSignatureInvalid, got %v", err)
	}
}

func TestVerify_TamperedPayloadFails(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	priv := generateFuzzKey(t)

	canonical, _ := CanonicalJSON(map[string]any{"x": 1})
	sig, err := Sign(ctx, priv, canonical)
	if err != nil {
		t.Fatal(err)
	}

	tampered := bytes.Clone(canonical)
	tampered[0] ^= 0xff

	err = Verify(ctx, &priv.PublicKey, tampered, sig)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("expected ErrSignatureInvalid, got %v", err)
	}
}

func TestVerify_MalformedDERFails(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	priv := generateFuzzKey(t)
	canonical, _ := CanonicalJSON(map[string]any{"x": 1})

	err := Verify(ctx, &priv.PublicKey, canonical, []byte{0x30, 0x44, 0x02, 0x20, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c})
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("expected ErrSignatureInvalid for truncated DER, got %v", err)
	}
}

func TestVerify_RespectsCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	priv := generateFuzzKey(t)

	err := Verify(ctx, &priv.PublicKey, []byte("payload"), []byte("sig"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestVerify_NoLeakOnFuzzInput captures slog output across many random failed
// verifications and asserts no payload/sig bytes appear in the log buffer.
func TestVerify_NoLeakOnFuzzInput(t *testing.T) {
	ctx := t.Context()
	priv := generateFuzzKey(t)

	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })

	for i := range 1000 {
		payload := []byte{byte(i), byte(i + 1), byte(i + 2)}
		sig := []byte{byte(i + 100), byte(i + 101), byte(i + 102)}
		_ = Verify(ctx, &priv.PublicKey, payload, sig)
	}

	logged := buf.String()
	// Verify logs nothing (Verify has no slog calls)
	if len(logged) > 0 {
		t.Errorf("Verify emitted log output it should not: %s", logged)
	}
}

func TestVerify_EmptyInputsPanic(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	priv := generateFuzzKey(t)

	// Must not panic on empty inputs
	err := Verify(ctx, &priv.PublicKey, nil, nil)
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("expected ErrSignatureInvalid for nil inputs, got %v", err)
	}

	err = Verify(ctx, &priv.PublicKey, []byte{}, []byte{})
	if !errors.Is(err, ErrSignatureInvalid) {
		t.Errorf("expected ErrSignatureInvalid for empty inputs, got %v", err)
	}
}
