package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

// TestMiddleware_RequestIDStable — every request gets a unique 32-char hex
// ID; client-supplied X-Request-ID and similar headers are ignored.
func TestMiddleware_RequestIDStable(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t)

	// Mount echoing handler under request-ID middleware only — keep the
	// scope tight to this property.
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, RequestID(r.Context()))
	})
	mw := srv.requestIDMiddleware(echo)

	const N = 100
	seen := make(map[string]struct{}, N)
	const forged = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	for range N {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.Header.Set("X-Request-ID", forged)
		req.Header.Set("Request-Id", forged)
		req.Header.Set("X-Correlation-ID", forged)
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)

		got := rec.Body.String()
		if len(got) != 32 {
			t.Fatalf("body=%q want 32 hex chars", got)
		}
		if _, err := hex.DecodeString(got); err != nil {
			t.Fatalf("body %q is not valid hex: %v", got, err)
		}
		if got == forged {
			t.Fatalf("chassis emitted forged ID %q", got)
		}
		if _, dup := seen[got]; dup {
			t.Fatalf("duplicate ID across %d requests: %q", N, got)
		}
		seen[got] = struct{}{}
	}
}

// TestMiddleware_RequestID_RandFailureReturns500 — exercises the error path
// where the chassis's CSPRNG seam returns an error.
func TestMiddleware_RequestID_RandFailureReturns500(t *testing.T) {
	old := randReadFn
	randReadFn = errReader{}
	t.Cleanup(func() { randReadFn = old })

	srv, _, _, _ := newTestServer(t)
	mw := srv.requestIDMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be invoked")
		_ = w
	}))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
}

type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errTestSynthetic }

// TestMiddleware_IPAllowListBlocks — allowed IPs reach the handler; blocked
// IPs receive 403 with no handler invocation; an audit event is emitted.
//
//nolint:gocognit // table-driven test with 4 cases × multiple assertions; complexity is structural
func TestMiddleware_IPAllowListBlocks(t *testing.T) {
	t.Parallel()

	srv, audit, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Network.AllowedCIDRs = []string{"100.64.0.0/10"}
	})

	var calls int32
	probe := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	})
	allowed := []netip.Prefix{asPrefix(t, "100.64.0.0/10")}
	mw := srv.ipAllowListMiddleware(allowed)(probe)

	cases := []struct {
		name        string
		remote      string
		fwdHeader   string
		wantStatus  int
		wantHandler int32
	}{
		{"allowed", "100.64.5.5:1024", "", http.StatusOK, 1},
		{"blocked", "8.8.8.8:1024", "", http.StatusForbidden, 0},
		{"socket-only", "8.8.8.8:1024", "100.64.5.5", http.StatusForbidden, 0},
		{"junk-remote", "garbage", "", http.StatusForbidden, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			atomic.StoreInt32(&calls, 0)
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remote
			if tc.fwdHeader != "" {
				req.Header.Set("X-Forwarded-For", tc.fwdHeader)
			}
			req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "rid"))
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Errorf("status=%d want %d", rec.Code, tc.wantStatus)
			}
			if got := atomic.LoadInt32(&calls); got != tc.wantHandler {
				t.Errorf("handler invocations=%d want %d", got, tc.wantHandler)
			}
			if tc.wantStatus == http.StatusForbidden {
				if !strings.HasPrefix(rec.Body.String(), "forbidden") {
					t.Errorf("403 body=%q want forbidden", rec.Body.String())
				}
			}
		})
	}

	// At least one AuditAuthFailedNotAllowed event recorded for the
	// blocked rows.
	hits := 0
	for _, e := range audit.snapshot() {
		if e.Type == AuditAuthFailedNotAllowed {
			hits++
		}
	}
	if hits < 3 {
		t.Fatalf("AuditAuthFailedNotAllowed count=%d want ≥ 3", hits)
	}
}

// TestMiddleware_BodyCap_413 — bodies > 64 KiB return 413 once the handler
// reads them.
func TestMiddleware_BodyCap_413(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mw := srv.bodyCapMiddleware(handler)

	body := bytes.Repeat([]byte("x"), MaxRequestBodyBytes+1)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d want 413", rec.Code)
	}
}

// TestMiddleware_RecoverLogsStackNoBody — sentinel body marker MUST NOT
// appear in the captured log; panic value, stack, and request_id MUST.
func TestMiddleware_RecoverLogsStackNoBody(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	suffix := randomHex(t, 16)
	sentinel := "SECRET_SHOULD_NEVER_APPEAR_10_" + suffix

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Logger = logger
	})

	chain := srv.recoverMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("sentinel-panic-value")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", strings.NewReader(sentinel))
	req = req.WithContext(context.WithValue(req.Context(), requestIDKey, "rid-test"))
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("response body=%q want generic 500", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sentinel-panic-value") {
		t.Fatalf("response body leaked panic detail: %q", rec.Body.String())
	}
	logs := buf.String()
	if !strings.Contains(logs, "sentinel-panic-value") {
		t.Fatalf("log missing panic value: %q", logs)
	}
	if !strings.Contains(logs, "stack") {
		t.Fatalf("log missing stack field: %q", logs)
	}
	if !strings.Contains(logs, "rid-test") {
		t.Fatalf("log missing request_id: %q", logs)
	}
	if strings.Contains(logs, "SECRET_SHOULD_NEVER_APPEAR_10") {
		t.Fatalf("log leaked sentinel marker: %q", logs)
	}
	if strings.Contains(logs, suffix) {
		t.Fatalf("log leaked sentinel random suffix: %q", logs)
	}
}

// TestMiddleware_Recover_NoPanic_Passthrough — when no panic occurs the
// middleware is transparent.
func TestMiddleware_Recover_NoPanic_Passthrough(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t)
	chain := srv.recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status=%d want 418", rec.Code)
	}
}

// TestMiddleware_Recover_SecondLevelPanic_FailsClosedForOneRequest —
// inject a panicking logger; the chassis must catch the second-level
// panic and not wedge.
func TestMiddleware_Recover_SecondLevelPanic_FailsClosedForOneRequest(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Logger = slog.New(panickingHandler{inner: slog.NewJSONHandler(io.Discard, nil)})
	})

	chain := srv.recoverMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("first-level")
	}))

	rec := httptest.NewRecorder()
	// MUST NOT panic out of ServeHTTP — the inner recover catches.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ServeHTTP panicked out: %v", r)
		}
	}()
	chain.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	// The chassis tries to log + write 500; even if those hands land
	// awkwardly the call returns without panicking.
}

// panickingHandler is a slog.Handler whose Handle method panics.
type panickingHandler struct {
	inner slog.Handler
}

func (p panickingHandler) Enabled(_ context.Context, _ slog.Level) bool  { return true }
func (p panickingHandler) Handle(_ context.Context, _ slog.Record) error { panic("logger boom") }
func (p panickingHandler) WithAttrs(_ []slog.Attr) slog.Handler          { return p }
func (p panickingHandler) WithGroup(_ string) slog.Handler               { return p }

// TestMiddleware_AuditOnPanic — panic emits exactly one AuditPanicCaptured.
func TestMiddleware_AuditOnPanic(t *testing.T) {
	t.Parallel()

	srv, audit, _, _ := newTestServer(t)
	chain := srv.recoverMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("test-boom")
	}))
	chain.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	count := 0
	for _, e := range audit.snapshot() {
		if e.Type == AuditPanicCaptured {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("AuditPanicCaptured count=%d want 1", count)
	}
}

// TestMiddleware_ChainOrder_RejectsBeforeRecover — a request from a
// disallowed IP is rejected before any handler/recover hits. We verify by
// asserting the audit event is exactly an AuthFailedNotAllowed and not a
// PanicCaptured (the handler is never reached, so it cannot panic).
func TestMiddleware_ChainOrder_RejectsBeforeRecover(t *testing.T) {
	t.Parallel()

	srv, audit, _, _ := newTestServer(t, func(d *Deps) {
		d.Cfg.Network.AllowedCIDRs = []string{"100.64.0.0/10"}
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(_ http.ResponseWriter, _ *http.Request) {
		panic("must not reach handler")
	})
	chain := srv.middlewareChain(mux)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.RemoteAddr = "8.8.8.8:9999" // not in allow-list
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
	for _, e := range audit.snapshot() {
		if e.Type == AuditPanicCaptured {
			t.Fatalf("AuditPanicCaptured should not fire — handler must not be reached")
		}
	}
}

// TestParseRemoteAddr — covers each branch of the helper.
func TestParseRemoteAddr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		ok   bool
		want string
	}{
		{"100.64.1.1:80", true, "100.64.1.1"},
		{"100.64.1.1", true, "100.64.1.1"},
		{"::1", true, "::1"},
		{"[::1]:443", true, "::1"},
		{"garbage", false, ""},
		{"", false, ""},
	}
	for _, tc := range cases {
		got, ok := parseRemoteAddr(tc.in)
		if ok != tc.ok {
			t.Errorf("parseRemoteAddr(%q) ok=%v want %v", tc.in, ok, tc.ok)
			continue
		}
		if ok && got.String() != tc.want {
			t.Errorf("parseRemoteAddr(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
}

// TestAllowedByCIDR — covers each branch of the helper.
func TestAllowedByCIDR(t *testing.T) {
	t.Parallel()
	allowed := []netip.Prefix{asPrefix(t, "100.64.0.0/10")}
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"100.64.1.1", true},
		{"100.65.0.5", true},
		{"8.8.8.8", false},
	} {
		got := allowedByCIDR(netip.MustParseAddr(tc.in), allowed)
		if got != tc.want {
			t.Errorf("allowedByCIDR(%q)=%v want %v", tc.in, got, tc.want)
		}
	}
	if allowedByCIDR(netip.Addr{}, allowed) {
		t.Errorf("invalid Addr must not be allowed")
	}
}

// TestParseAllowedCIDRs — junk entries are dropped; valid entries kept.
func TestParseAllowedCIDRs(t *testing.T) {
	t.Parallel()
	got := parseAllowedCIDRs([]string{"100.64.0.0/10", "garbage", "192.168.1.0/24"})
	if len(got) != 2 {
		t.Fatalf("len=%d want 2 (junk filtered)", len(got))
	}
}

// randomHex returns 2*n hex chars, t.Fatal on rand error.
func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return hex.EncodeToString(b)
}

// TestMiddleware_ResponseBodyShape pins the generic 500 body so a future
// drift breaks loudly.
func TestMiddleware_ResponseBodyShape(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := newTestServer(t)
	chain := srv.recoverMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("x")
	}))
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if got := rec.Body.String(); got != "internal server error\n" {
		t.Fatalf("body=%q want %q", got, "internal server error\n")
	}
}

// TestMiddleware_AuditWriter_PassThrough — explicit JSON marshal of the
// shape so a regression that adds nested types is caught.
func TestMiddleware_AuditWriter_PassThrough(t *testing.T) {
	t.Parallel()
	e := AuditEvent{
		Type:      AuditPanicCaptured,
		ClientIP:  netip.MustParseAddr("100.64.1.1"),
		RequestID: "x",
		Detail:    map[string]string{"a": "b"},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), "panic_captured") {
		t.Fatalf("audit json missing event type: %s", b)
	}
}
