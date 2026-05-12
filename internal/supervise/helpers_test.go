package supervise

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"log/slog"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// fakeClock is shared with state_test.go; declared there. This file
// adds the HTTP / log / ECIES fixtures the new SDD-21 tests depend
// on.

// roundTripFunc adapts a func to an http.RoundTripper.
type roundTripFunc func(req *http.Request) (*http.Response, error)

// RoundTrip implements http.RoundTripper.
func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// recordingHandler is a slog.Handler whose records are JSON-encoded
// into a shared byte buffer guarded by a mutex. Used to assert that
// marker bytes never appear in operational logs.
type recordingHandler struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
	h   slog.Handler
}

func newRecordingLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	mu := &sync.Mutex{}
	rh := &recordingHandler{
		mu:  mu,
		buf: buf,
		h: slog.NewJSONHandler(buf, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}),
	}
	return slog.New(rh), buf
}

func (r *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (r *recordingHandler) Handle(ctx context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.h.Handle(ctx, rec)
}

func (r *recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &recordingHandler{mu: r.mu, buf: r.buf, h: r.h.WithAttrs(attrs)}
}

func (r *recordingHandler) WithGroup(name string) slog.Handler {
	return &recordingHandler{mu: r.mu, buf: r.buf, h: r.h.WithGroup(name)}
}

// newSecureBytes wraps b in a *SecureBytes for tests.
func newSecureBytes(t *testing.T, b []byte) *securebytes.SecureBytes {
	t.Helper()
	cp := make([]byte, len(b))
	copy(cp, b)
	sb, err := securebytes.New(cp)
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	return sb
}

// newECIESKey generates a fresh secp256k1 ECDSA keypair for tests.
func newECIESKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	curve := secp256k1.S256() //nolint:staticcheck // secp256k1 not in crypto/ecdh; S256() is the correct curve accessor
	d, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	return d
}

// encryptForTest returns a BIE1 envelope encrypting plaintext for the
// recipient.
func encryptForTest(t *testing.T, recipient *ecdsa.PrivateKey, plaintext []byte) []byte {
	t.Helper()
	env, err := ecies.Encrypt(context.Background(), &recipient.PublicKey, plaintext)
	if err != nil {
		t.Fatalf("ecies.Encrypt: %v", err)
	}
	return env
}

// storeClock is a Clock impl for tests; independent from fakeClock.
type storeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *storeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// newTestStoreWithToken returns a *Store seeded with the supplied
// JWT bytes wrapped in a *SecureBytes.
func newTestStoreWithToken(t *testing.T, jwt []byte) *Store {
	t.Helper()
	clk := &storeClock{now: time.Unix(1700000000, 0)}
	store := NewStore(context.Background(), clk)
	store.setToken(newSecureBytes(t, jwt))
	return store
}
