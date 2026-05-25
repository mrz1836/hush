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
