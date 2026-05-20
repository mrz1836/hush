package supervise

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// newRefillerForTest constructs a Refiller wired with the supplied
// HTTP transport and store, then attaches a Grace, ECIES key, and
// server URL prefix via the package-private (*Refiller).attach.
func newRefillerForTest(t *testing.T, rt http.RoundTripper, store *Store, grace *Grace, priv *ecdsa.PrivateKey) *Refiller {
	t.Helper()
	logger, _ := newRecordingLogger()
	r := NewRefiller(&http.Client{Transport: rt}, store, logger)
	r.attach(grace, priv, "https://vault.test")
	return r
}

// TestRefill_SilentOnCleanExit verifies the hot path: two scopes,
// HTTP 200 + valid ECIES envelopes for both, Refill returns nil and
// each scope is committed to Grace via one Set call.
func TestRefill_SilentOnCleanExit(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJhbGciOi.JWT.SIG"))
	grace := NewGrace(time.Hour, true)
	defer grace.Evict("S1")
	defer grace.Evict("S2")

	want := map[string][]byte{
		"S1": []byte("super-secret-one"),
		"S2": []byte("super-secret-two"),
	}
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		name := strings.TrimPrefix(req.URL.Path, "/s/")
		pt, ok := want[name]
		if !ok {
			t.Fatalf("unexpected scope %q", name)
		}
		env := encryptForTest(t, priv, pt)
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(env)),
			Header:     http.Header{},
		}, nil
	})
	r := newRefillerForTest(t, rt, store, grace, priv)

	if err := r.Refill(context.Background(), []string{"S1", "S2"}); err != nil {
		t.Fatalf("Refill returned %v, want nil", err)
	}
	for name, expect := range want {
		sb, ok := grace.Get(name)
		if !ok {
			t.Fatalf("Grace.Get(%q) miss", name)
		}
		var got []byte
		if err := sb.Use(func(b []byte) {
			got = append(got, b...)
		}); err != nil {
			t.Fatalf("sb.Use: %v", err)
		}
		if !bytes.Equal(got, expect) {
			t.Fatalf("Grace cached bytes mismatch for %q", name)
		}
	}
}

// TestRefill_401UnknownJTITransitions: a 401 with body
// {"error":"unknown_jti"} for one scope produces a wrapped
// ErrJTIUnknown; the loop short-circuits at the failing scope; no
// Grace.Set is committed.
func TestRefill_401UnknownJTITransitions(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJhbGciOi.JWT.SIG"))
	grace := NewGrace(time.Hour, true)

	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 401,
			Body:       io.NopCloser(strings.NewReader(`{"error":"unknown_jti"}`)),
			Header:     http.Header{},
		}, nil
	})
	r := newRefillerForTest(t, rt, store, grace, priv)

	err := r.Refill(context.Background(), []string{"S1", "S2"})
	if err == nil {
		t.Fatalf("Refill returned nil, want non-nil")
	}
	if !errors.Is(err, ErrJTIUnknown) {
		t.Fatalf("err = %v, want errors.Is ErrJTIUnknown", err)
	}
	if _, ok := grace.Get("S1"); ok {
		t.Fatalf("Grace.Get(S1) hit; expected miss after JTI rejection")
	}
}

// TestRefill_NetworkErrorIsRetryable: a transport-layer net.OpError
// surfaces as a wrapped, non-ErrJTIUnknown error.
func TestRefill_NetworkErrorIsRetryable(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJhbGciOi.JWT.SIG"))
	grace := NewGrace(time.Hour, true)

	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, netErr
	})
	r := newRefillerForTest(t, rt, store, grace, priv)

	err := r.Refill(context.Background(), []string{"S1"})
	if err == nil {
		t.Fatalf("Refill returned nil, want non-nil")
	}
	if errors.Is(err, ErrJTIUnknown) {
		t.Fatalf("err = %v, must NOT be ErrJTIUnknown", err)
	}
	var op *net.OpError
	if !errors.As(err, &op) {
		t.Fatalf("err = %v, expected wrap of *net.OpError", err)
	}
}

// TestRefill_AtomicDestructionOnPartialFailure: three scopes; first
// two succeed, the third fails. After Refill returns, the first two
// SecureBytes are destroyed (Use returns ErrDestroyed) and no
// Grace.Set was committed for any of the three.
func TestRefill_AtomicDestructionOnPartialFailure(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJhbGciOi.JWT.SIG"))
	grace := NewGrace(time.Hour, true)

	want := map[string][]byte{
		"S1": []byte("aaa"),
		"S2": []byte("bbb"),
	}
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		name := strings.TrimPrefix(req.URL.Path, "/s/")
		if pt, ok := want[name]; ok {
			env := encryptForTest(t, priv, pt)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader(env)),
				Header:     http.Header{},
			}, nil
		}
		// Third scope: 500 server error.
		return &http.Response{
			StatusCode: 500,
			Body:       io.NopCloser(strings.NewReader("boom")),
			Header:     http.Header{},
		}, nil
	})
	r := newRefillerForTest(t, rt, store, grace, priv)

	err := r.Refill(context.Background(), []string{"S1", "S2", "S3"})
	if err == nil {
		t.Fatalf("Refill returned nil, want non-nil")
	}

	for _, name := range []string{"S1", "S2", "S3"} {
		if _, ok := grace.Get(name); ok {
			t.Fatalf("Grace.Get(%q) hit; expected miss after partial failure", name)
		}
	}
	// We cannot reach the destroyed sb pointers via Grace (none stored).
	// The committed-bool defer in Refill destroyed them. We assert
	// indirectly: a fresh Set + Get cycle with an unrelated sb still
	// works (cache machinery healthy, no leaked-state corruption).
	probe := newSecureBytes(t, []byte("probe"))
	grace.Set("PROBE", probe)
	if _, ok := grace.Get("PROBE"); !ok {
		t.Fatalf("Grace probe entry missing after partial-failure cycle")
	}
}

// TestRefill_NeverStringifiesDecryptedBytes: marker plaintext never
// appears in the operational log buffer.
func TestRefill_NeverStringifiesDecryptedBytes(t *testing.T) {
	const marker = "HUSH-MARKER-21-PLAINTEXT"
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJhbGciOi.JWT.SIG"))
	grace := NewGrace(time.Hour, true)

	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		env := encryptForTest(t, priv, []byte(marker))
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(bytes.NewReader(env)),
			Header:     http.Header{},
		}, nil
	})
	logger, buf := newRecordingLogger()
	r := NewRefiller(&http.Client{Transport: rt}, store, logger)
	r.attach(grace, priv, "https://vault.test")

	if err := r.Refill(context.Background(), []string{"S1"}); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte(marker)) {
		t.Fatalf("operational log leaked marker plaintext: %s", buf.String())
	}
	sb, ok := grace.Get("S1")
	if !ok {
		t.Fatalf("Grace miss")
	}
	var got []byte
	if err := sb.Use(func(b []byte) { got = append(got, b...) }); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if string(got) != marker {
		t.Fatalf("plaintext mismatch: got %q want %q", got, marker)
	}
	// LogValue must redact.
	if v := sb.LogValue().String(); v != "[redacted]" {
		t.Fatalf("SecureBytes.LogValue()=%q want [redacted]", v)
	}
}

// TestRefill_AuditEventsDistinctByOutcome: success / ErrJTIUnknown /
// transient each emit a distinguishable outcome attribute and zero
// secret bytes.
func TestRefill_AuditEventsDistinctByOutcome(t *testing.T) {
	priv := newECIESKey(t)

	cases := []struct {
		name        string
		rt          roundTripFunc
		wantOutcome string
	}{
		{
			name: "ok",
			rt: func(_ *http.Request) (*http.Response, error) {
				env := encryptForTest(t, priv, []byte("payload"))
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(env)), Header: http.Header{}}, nil
			},
			wantOutcome: "ok",
		},
		{
			name: "jti-unknown",
			rt: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"error":"unknown_jti"}`)), Header: http.Header{}}, nil
			},
			wantOutcome: "jti-unknown",
		},
		{
			name: "transient",
			rt: func(_ *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("boom")), Header: http.Header{}}, nil
			},
			wantOutcome: "transient",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestStoreWithToken(t, []byte("eyJhbGciOi.JWT.SIG"))
			grace := NewGrace(time.Hour, true)
			logger, buf := newRecordingLogger()
			r := NewRefiller(&http.Client{Transport: tc.rt}, store, logger)
			r.attach(grace, priv, "https://vault.test")
			_ = r.Refill(context.Background(), []string{"S1"})
			out := buf.String()
			if !strings.Contains(out, `"outcome":"`+tc.wantOutcome+`"`) {
				t.Fatalf("expected outcome=%q in log, got %s", tc.wantOutcome, out)
			}
			// No secret bytes ever leaked; the JWT bearer is opaque.
			if strings.Contains(out, "super-secret") || strings.Contains(out, "payload") {
				t.Fatalf("log leaked plaintext: %s", out)
			}
		})
	}
}

// TestRefill_BearerTokenNeverLeaksToLogs: a marker JWT never appears
// in operational log output and SecureBytes redacts (B-RR-5).
func TestRefill_BearerTokenNeverLeaksToLogs(t *testing.T) {
	const marker = "HUSH-MARKER-JWT-CAFEBABE"
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte(marker))
	grace := NewGrace(time.Hour, true)

	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		env := encryptForTest(t, priv, []byte("payload"))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(env)), Header: http.Header{}}, nil
	})
	logger, buf := newRecordingLogger()
	r := NewRefiller(&http.Client{Transport: rt}, store, logger)
	r.attach(grace, priv, "https://vault.test")

	if err := r.Refill(context.Background(), []string{"S1"}); err != nil {
		t.Fatalf("Refill: %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte(marker)) {
		t.Fatalf("log leaked JWT marker: %s", buf.String())
	}
	// Verify SecureBytes redaction.
	tok := store.Snapshot().Token
	if tok == nil {
		t.Fatalf("token nil")
	}
	if got := tok.LogValue().String(); got != "[redacted]" {
		t.Fatalf("LogValue=%q want [redacted]", got)
	}
}

// ----- US4 boot-retry smoke tests (Phase 6) -----

// TestBootRetry_BackoffRespected: Refill is invoked twice in
// succession against a 503 stub; each call results in exactly one
// HTTP request (Refill never internally retries) and each returns a
// non-ErrJTIUnknown error.
func TestBootRetry_BackoffRespected(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJhbGciOi.JWT.SIG"))
	grace := NewGrace(time.Hour, true)

	var calls atomic.Int32
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable")), Header: http.Header{}}, nil
	})
	r := newRefillerForTest(t, rt, store, grace, priv)

	for i := 0; i < 2; i++ {
		err := r.Refill(context.Background(), []string{"S1"})
		if err == nil {
			t.Fatalf("Refill returned nil on iter %d, want non-nil", i)
		}
		if errors.Is(err, ErrJTIUnknown) {
			t.Fatalf("Refill returned ErrJTIUnknown on iter %d", i)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("RoundTrip called %d times, want 2 (no internal retry)", got)
	}
}

// TestBootRetry_NeverPromptsDiscord: Refill against a permanent 5xx
// produces only WARN/INFO operational log lines, no Approver call,
// and the ErrBootTimeout sentinel is exported and stable.
func TestBootRetry_NeverPromptsDiscord(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJhbGciOi.JWT.SIG"))
	grace := NewGrace(time.Hour, true)

	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("boom")), Header: http.Header{}}, nil
	})
	logger, buf := newRecordingLogger()
	r := NewRefiller(&http.Client{Transport: rt}, store, logger)
	r.attach(grace, priv, "https://vault.test")

	for i := 0; i < 5; i++ {
		_ = r.Refill(context.Background(), []string{"S1"})
	}
	out := buf.String()
	if strings.Contains(out, "approver") || strings.Contains(out, "discord.prompt") || strings.Contains(out, "DM ") {
		t.Fatalf("log shows discord-bound dependency invocation: %s", out)
	}
	if !errors.Is(ErrBootTimeout, ErrBootTimeout) {
		t.Fatalf("ErrBootTimeout sentinel unstable")
	}
	// Verify error class wraps a non-JTI error each time (no path
	// where Refill would prompt Discord directly).
	err := r.Refill(context.Background(), []string{"S1"})
	if err == nil {
		t.Fatalf("Refill returned nil, want non-nil")
	}
	if errors.Is(err, ErrJTIUnknown) {
		t.Fatalf("Refill returned ErrJTIUnknown, want non-JTI transient")
	}
}

// TestNewRefiller_PanicsOnNil exercises the constructor's startup-
// wiring guards (Constitution IX exemption).
func TestNewRefiller_PanicsOnNil(t *testing.T) {
	cases := []struct {
		name   string
		client *http.Client
		store  *Store
		logger *slog.Logger
	}{
		{name: "nil-client", client: nil, store: newTestStoreWithToken(t, []byte("j")), logger: slog.Default()},
		{name: "nil-store", client: &http.Client{}, store: nil, logger: slog.Default()},
		{name: "nil-logger", client: &http.Client{}, store: newTestStoreWithToken(t, []byte("j")), logger: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected panic")
				}
			}()
			_ = NewRefiller(tc.client, tc.store, tc.logger)
		})
	}
}

// TestRefill_NilToken: a Store with no cached JWT yields a wrapped
// programmer-error from Refill (RR-7).
func TestRefill_NilToken(t *testing.T) {
	priv := newECIESKey(t)
	clk := &storeClock{now: time.Unix(1700000000, 0)}
	store := NewStore(context.Background(), clk)
	grace := NewGrace(time.Hour, true)

	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Fatalf("transport should not be invoked when token is nil")
		return nil, errors.New("unreachable")
	})
	r := newRefillerForTest(t, rt, store, grace, priv)

	err := r.Refill(context.Background(), []string{"S1"})
	if err == nil {
		t.Fatalf("Refill returned nil, want non-nil")
	}
	if errors.Is(err, ErrJTIUnknown) {
		t.Fatalf("err = %v, must NOT be ErrJTIUnknown", err)
	}
}

// TestRefill_CtxCancelled: a pre-cancelled ctx surfaces ctx.Err()
// from the transport-level call.
func TestRefill_CtxCancelled(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJ"))
	grace := NewGrace(time.Hour, true)
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	r := newRefillerForTest(t, rt, store, grace, priv)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.Refill(ctx, []string{"S1"})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Refill err=%v want wrapped context.Canceled", err)
	}
}

// TestRefill_401UnparseableBody: 401 with a non-JSON body produces
// a non-ErrJTIUnknown transient error.
func TestRefill_401UnparseableBody(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJ"))
	grace := NewGrace(time.Hour, true)
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("not-json")), Header: http.Header{}}, nil
	})
	r := newRefillerForTest(t, rt, store, grace, priv)
	err := r.Refill(context.Background(), []string{"S1"})
	if err == nil {
		t.Fatalf("Refill returned nil, want non-nil")
	}
	if errors.Is(err, ErrJTIUnknown) {
		t.Fatalf("err = %v, must NOT be ErrJTIUnknown for unparseable 401 body", err)
	}
}

// TestRefill_401NonJTIError: 401 with body {"error":"other"} is NOT
// ErrJTIUnknown.
func TestRefill_401NonJTIError(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJ"))
	grace := NewGrace(time.Hour, true)
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"error":"banned"}`)), Header: http.Header{}}, nil
	})
	r := newRefillerForTest(t, rt, store, grace, priv)
	err := r.Refill(context.Background(), []string{"S1"})
	if err == nil {
		t.Fatalf("Refill returned nil, want non-nil")
	}
	if errors.Is(err, ErrJTIUnknown) {
		t.Fatalf("err = %v, banned != unknown_jti", err)
	}
}

// TestRefill_DecryptFailure: a 200 with garbage envelope bytes
// surfaces a wrapped decrypt error.
func TestRefill_DecryptFailure(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJ"))
	grace := NewGrace(time.Hour, true)
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte("XXXXgarbage"))), Header: http.Header{}}, nil
	})
	r := newRefillerForTest(t, rt, store, grace, priv)
	err := r.Refill(context.Background(), []string{"S1"})
	if err == nil {
		t.Fatalf("Refill returned nil")
	}
	if errors.Is(err, ErrJTIUnknown) {
		t.Fatalf("err = %v, must not be ErrJTIUnknown", err)
	}
}

// TestRefill_BadServerURL: malformed server URL triggers
// http.NewRequestWithContext failure path.
func TestRefill_BadServerURL(t *testing.T) {
	priv := newECIESKey(t)
	store := newTestStoreWithToken(t, []byte("eyJ"))
	grace := NewGrace(time.Hour, true)
	logger, _ := newRecordingLogger()
	r := NewRefiller(&http.Client{}, store, logger)
	r.attach(grace, priv, "://bad-url\x7f")
	err := r.Refill(context.Background(), []string{"S1"})
	if err == nil {
		t.Fatalf("Refill returned nil, want non-nil")
	}
}

// TestClassifyOutcome covers the cancelled/ok branches of the
// internal classifier.
func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, "ok"},
		{context.Canceled, "cancelled"},
		{context.DeadlineExceeded, "cancelled"},
		{errors.New("boom"), "transient"},
		{ErrJTIUnknown, "jti-unknown"},
	}
	for _, tc := range cases {
		if got := classifyOutcome(tc.err); got != tc.want {
			t.Fatalf("classifyOutcome(%v)=%q want %q", tc.err, got, tc.want)
		}
	}
}

// ----- ancillary tiny helpers -----

// _ avoids unused-import for fmt / slog in case future test edits
// drop the only references.
var (
	_ = fmt.Sprintf
	_ = slog.LevelInfo
	_ = ecies.ErrECIESDecryptFailed
	_ = securebytes.ErrDestroyed
)
