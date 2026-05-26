package client_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/pkg/client"
)

func newTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck // secp256k1 unsupported by crypto/ecdh
	require.NoError(t, err)
	return k
}

// fakeMeServer stands up an httptest.Server that responds to POST /me
// with the supplied status and body. Captures the last request body
// for assertions.
type fakeMeServer struct {
	srv       *httptest.Server
	gotMethod string
	gotPath   string
	gotBody   []byte
	gotBearer string
}

func newFakeMeServer(t *testing.T, status int, body string) *fakeMeServer {
	t.Helper()
	f := &fakeMeServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/h/abcd/me", func(w http.ResponseWriter, r *http.Request) {
		f.gotMethod = r.Method
		f.gotPath = r.URL.Path
		f.gotBearer = r.Header.Get("Authorization")
		f.gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// =============================================================
// Happy path
// =============================================================

func TestMe_OK_NoBearer(t *testing.T) {
	respBody := `{
		"schema_version":1,
		"server_version":"0.42.0",
		"scopes_available":["ANTHROPIC_API_KEY","OPENAI_API_KEY"]
	}`
	f := newFakeMeServer(t, http.StatusOK, respBody)

	got, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   f.srv.URL + "/h/abcd",
		ClientKey:   newTestKey(t),
		MachineName: "test.local",
	})
	require.NoError(t, err)
	assert.Equal(t, 1, got.SchemaVersion)
	assert.Equal(t, "0.42.0", got.ServerVersion)
	assert.Equal(t, []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"}, got.ScopesAvailable)
	assert.Nil(t, got.CurrentSession)
	assert.True(t, got.NextRefreshWindow.IsZero())

	// Wire-level assertions: POST, signed body, no Bearer header.
	assert.Equal(t, "POST", f.gotMethod)
	assert.Equal(t, "/h/abcd/me", f.gotPath)
	assert.Empty(t, f.gotBearer)
	var sentBody map[string]any
	require.NoError(t, json.Unmarshal(f.gotBody, &sentBody))
	for _, field := range []string{"nonce", "timestamp", "signature", "request_id", "machine_name", "client_key_fingerprint"} {
		assert.NotEmpty(t, sentBody[field], "field %q must be present", field)
	}
}

func TestMe_OK_WithBearer(t *testing.T) {
	respBody := `{
		"schema_version":1,
		"server_version":"0.42.0",
		"scopes_available":["A"],
		"current_session":{
			"jti":"abc-uuid",
			"expires_at":"2026-05-25T13:00:00Z",
			"scopes":["A"],
			"max_uses":3,
			"session_type":"interactive"
		}
	}`
	f := newFakeMeServer(t, http.StatusOK, respBody)

	got, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   f.srv.URL + "/h/abcd",
		ClientKey:   newTestKey(t),
		BearerJWT:   "fake.jwt.token",
		MachineName: "test.local",
	})
	require.NoError(t, err)
	require.NotNil(t, got.CurrentSession)
	assert.Equal(t, "abc-uuid", got.CurrentSession.JTI)
	assert.Equal(t, []string{"A"}, got.CurrentSession.Scopes)
	assert.Equal(t, 3, got.CurrentSession.MaxUses)
	assert.Equal(t, "interactive", got.CurrentSession.SessionType)
	assert.Equal(t, time.Date(2026, 5, 25, 13, 0, 0, 0, time.UTC), got.CurrentSession.ExpiresAt)

	assert.Equal(t, "Bearer fake.jwt.token", f.gotBearer)
}

// =============================================================
// Validation
// =============================================================

func TestMe_MissingServerURL(t *testing.T) {
	_, err := client.Me(context.Background(), client.MeRequest{
		ClientKey:   newTestKey(t),
		MachineName: "x",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse))
}

func TestMe_MissingClientKey(t *testing.T) {
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   "http://example",
		MachineName: "x",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse))
}

func TestMe_MissingMachineName(t *testing.T) {
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL: "http://example",
		ClientKey: newTestKey(t),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse))
}

func TestMe_BadServerURL(t *testing.T) {
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   "://not a url",
		ClientKey:   newTestKey(t),
		MachineName: "x",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse))
}

func TestMe_BadScheme(t *testing.T) {
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   "ftp://example/h/abc",
		ClientKey:   newTestKey(t),
		MachineName: "x",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse))
}

// =============================================================
// Network / status mapping
// =============================================================

func TestMe_Unauthenticated(t *testing.T) {
	f := newFakeMeServer(t, http.StatusForbidden, `{"error":"bad_signature","request_id":"r1"}`)
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   f.srv.URL + "/h/abcd",
		ClientKey:   newTestKey(t),
		MachineName: "x",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrUnauthenticated), "got %v", err)
}

func TestMe_Unauthorized401(t *testing.T) {
	f := newFakeMeServer(t, http.StatusUnauthorized, `{}`)
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   f.srv.URL + "/h/abcd",
		ClientKey:   newTestKey(t),
		MachineName: "x",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrUnauthenticated))
}

func TestMe_5xx_MapsToInvalidResponse(t *testing.T) {
	f := newFakeMeServer(t, http.StatusInternalServerError, `oops`)
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   f.srv.URL + "/h/abcd",
		ClientKey:   newTestKey(t),
		MachineName: "x",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse))
}

func TestMe_MalformedResponseJSON(t *testing.T) {
	f := newFakeMeServer(t, http.StatusOK, `not json at all`)
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   f.srv.URL + "/h/abcd",
		ClientKey:   newTestKey(t),
		MachineName: "x",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrInvalidResponse))
}

func TestMe_NetworkUnreachable(t *testing.T) {
	// Connect to a port nothing is listening on.
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   "http://127.0.0.1:1/h/abcd",
		ClientKey:   newTestKey(t),
		MachineName: "x",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, client.ErrSocketUnavailable), "got %v", err)
}

func TestMe_ContextCancelled(t *testing.T) {
	f := newFakeMeServer(t, http.StatusOK, `{"schema_version":1,"server_version":"x","scopes_available":[]}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Me(ctx, client.MeRequest{
		ServerURL:   f.srv.URL + "/h/abcd",
		ClientKey:   newTestKey(t),
		MachineName: "x",
	})
	require.Error(t, err)
}

// =============================================================
// Signed-payload contract
// =============================================================

func TestMe_RegeneratesNoncePerCall(t *testing.T) {
	// Two successive calls must use distinct nonces (CSPRNG must be live).
	f := newFakeMeServer(t, http.StatusOK, `{"schema_version":1,"server_version":"x","scopes_available":[]}`)
	key := newTestKey(t)

	captured := []string{}
	captureNonce := func() {
		var body map[string]any
		require.NoError(t, json.Unmarshal(f.gotBody, &body))
		captured = append(captured, body["nonce"].(string))
	}
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL: f.srv.URL + "/h/abcd", ClientKey: key, MachineName: "x",
	})
	require.NoError(t, err)
	captureNonce()
	_, err = client.Me(context.Background(), client.MeRequest{
		ServerURL: f.srv.URL + "/h/abcd", ClientKey: key, MachineName: "x",
	})
	require.NoError(t, err)
	captureNonce()
	require.Len(t, captured, 2)
	assert.NotEqual(t, captured[0], captured[1], "nonces must differ across calls")
}

func TestMe_PreservesPathPrefix(t *testing.T) {
	// ServerURL with a trailing slash should still resolve correctly.
	f := newFakeMeServer(t, http.StatusOK, `{"schema_version":1,"server_version":"x","scopes_available":[]}`)
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   f.srv.URL + "/h/abcd/",
		ClientKey:   newTestKey(t),
		MachineName: "x",
	})
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(f.gotPath, "/h/abcd/me"), "got path %q", f.gotPath)
}

// =============================================================
// Default deadline (K2: http.DefaultClient has no Timeout)
// =============================================================

// deadlineCaptor is a http.RoundTripper that records the request
// context's deadline before responding with a canned body.
type deadlineCaptor struct {
	deadline    time.Time
	hasDeadline bool
	respStatus  int
	respBody    string
}

func (d *deadlineCaptor) RoundTrip(r *http.Request) (*http.Response, error) {
	d.deadline, d.hasDeadline = r.Context().Deadline()
	return &http.Response{
		StatusCode: d.respStatus,
		Body:       io.NopCloser(strings.NewReader(d.respBody)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func TestMe_AppliesDefaultDeadlineWhenContextHasNone(t *testing.T) {
	captor := &deadlineCaptor{
		respStatus: http.StatusOK,
		respBody:   `{"schema_version":1,"server_version":"x","scopes_available":[]}`,
	}
	_, err := client.Me(context.Background(), client.MeRequest{
		ServerURL:   "http://127.0.0.1:1/h/abcd",
		ClientKey:   newTestKey(t),
		MachineName: "test",
		HTTPClient:  &http.Client{Transport: captor},
	})
	require.NoError(t, err)
	require.True(t, captor.hasDeadline, "Me must inject a default deadline when ctx has none")
	// Deadline should be approximately now + meDefaultTimeout.
	expected := time.Now().Add(client.MeDefaultTimeout)
	assert.WithinDuration(t, expected, captor.deadline, 5*time.Second,
		"injected deadline must be ~%s in the future", client.MeDefaultTimeout)
}

func TestMe_PreservesCallerDeadline(t *testing.T) {
	captor := &deadlineCaptor{
		respStatus: http.StatusOK,
		respBody:   `{"schema_version":1,"server_version":"x","scopes_available":[]}`,
	}
	callerDeadline := time.Now().Add(7 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), callerDeadline)
	defer cancel()
	_, err := client.Me(ctx, client.MeRequest{
		ServerURL:   "http://127.0.0.1:1/h/abcd",
		ClientKey:   newTestKey(t),
		MachineName: "test",
		HTTPClient:  &http.Client{Transport: captor},
	})
	require.NoError(t, err)
	require.True(t, captor.hasDeadline)
	assert.WithinDuration(t, callerDeadline, captor.deadline, 100*time.Millisecond,
		"caller deadline must not be overridden")
}

// =============================================================
// ensureDeadline unit tests
// =============================================================

func TestEnsureDeadline_AddsDeadlineWhenAbsent(t *testing.T) {
	parent := context.Background()
	_, hasDeadline := parent.Deadline()
	require.False(t, hasDeadline)

	ctx, cancel := client.EnsureDeadline(parent, 50*time.Millisecond)
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, time.Now().Add(50*time.Millisecond), deadline, 100*time.Millisecond)
}

func TestEnsureDeadline_PreservesExistingDeadline(t *testing.T) {
	parentDeadline := time.Now().Add(3 * time.Second)
	parent, parentCancel := context.WithDeadline(context.Background(), parentDeadline)
	defer parentCancel()

	ctx, cancel := client.EnsureDeadline(parent, 30*time.Second)
	defer cancel()
	got, ok := ctx.Deadline()
	require.True(t, ok)
	assert.WithinDuration(t, parentDeadline, got, 100*time.Millisecond,
		"existing deadline must be preserved, not replaced with fallback")
}

func TestEnsureDeadline_CancelIsSafeWhenNoop(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), time.Second)
	defer parentCancel()
	_, cancel := client.EnsureDeadline(parent, 10*time.Second)
	// The returned cancel for the noop path must be safe to call any
	// number of times without panicking.
	assert.NotPanics(t, func() { cancel() })
	assert.NotPanics(t, func() { cancel() })
}
