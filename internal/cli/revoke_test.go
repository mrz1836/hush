package cli

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/transport/sign"
)

const validJTI = "8f3a2c1e-9d4b-4f0a-b6e8-2d5e6c7f8a9b"

func newRevokeKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("GeneratePrivateKey: %v", err)
	}
	return priv.ToECDSA()
}

// TestRevoke_SignedRequestPosted asserts the canonical-JSON payload
// is correctly signed and POSTed.
func TestRevoke_SignedRequestPosted(t *testing.T) {
	t.Parallel()
	key := newRevokeKey(t)

	var captured revokeRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode envelope: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	deps := revokeDeps{signKey: key, now: func() time.Time { return time.Unix(1714564800, 0).UTC() }}
	if err := runRevoke(t.Context(), out, errStream, deps, srv.URL, validJTI); err != nil {
		t.Fatalf("runRevoke: %v", err)
	}

	// Decode the canonical payload bytes the server received.
	var got revokePayload
	if err := json.Unmarshal(captured.Payload, &got); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got.JTI != validJTI {
		t.Errorf("JTI = %q, want %q", got.JTI, validJTI)
	}
	if len(got.Nonce) != 64 {
		t.Errorf("Nonce hex length = %d, want 64", len(got.Nonce))
	}
	if got.Timestamp == "" {
		t.Errorf("Timestamp empty")
	}

	// Re-canonicalise + verify the signature.
	canonical, err := sign.CanonicalJSON(got)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(captured.Signature)
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if vErr := sign.Verify(t.Context(), &key.PublicKey, canonical, sig); vErr != nil {
		t.Errorf("signature did not verify against test public key: %v", vErr)
	}
}

// TestRevoke_BadStatusMapsToExitCode table-drives the locked
// HTTP-status → exit-code map.
func TestRevoke_BadStatusMapsToExitCode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status int
		want   int
	}{
		{http.StatusOK, ExitOK},
		{http.StatusUnauthorized, ExitAuth},
		{http.StatusForbidden, ExitAuth},
		{http.StatusNotFound, ExitNotFound},
		{http.StatusInternalServerError, ExitErr},
		{http.StatusServiceUnavailable, ExitErr},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("%d", c.status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
			}))
			t.Cleanup(srv.Close)
			var stdout, stderr bytes.Buffer
			out := newStream(&stdout, false, true)
			errStream := newStream(&stderr, false, true)
			deps := revokeDeps{signKey: newRevokeKey(t)}
			err := runRevoke(t.Context(), out, errStream, deps, srv.URL, validJTI)
			if got := mapErr(err); got != c.want {
				t.Errorf("status %d: mapErr = %d, want %d (err=%v)", c.status, got, c.want, err)
			}
		})
	}
}

// TestRevoke_MissingFlags_ExitInputErr exercises the cobra-level
// missing-flag branch via the constructed command.
func TestRevoke_MissingFlags_ExitInputErr(t *testing.T) {
	t.Parallel()
	t.Run("missing --server", func(t *testing.T) {
		root := newRootCmd(&outputContext{stdout: newStream(&bytes.Buffer{}, false, true), stderr: newStream(&bytes.Buffer{}, false, true)})
		root.SetArgs([]string{"revoke", "--jti", validJTI})
		root.SetContext(t.Context())
		err := root.Execute()
		if got := mapErr(err); got != ExitInputErr {
			t.Errorf("mapErr = %d, want ExitInputErr", got)
		}
	})
	t.Run("missing --jti", func(t *testing.T) {
		root := newRootCmd(&outputContext{stdout: newStream(&bytes.Buffer{}, false, true), stderr: newStream(&bytes.Buffer{}, false, true)})
		root.SetArgs([]string{"revoke", "--server", "http://127.0.0.1:1"})
		root.SetContext(t.Context())
		err := root.Execute()
		if got := mapErr(err); got != ExitInputErr {
			t.Errorf("mapErr = %d, want ExitInputErr", got)
		}
	})
}

// TestRevoke_MalformedJTI_ExitInputErr asserts a malformed --jti
// returns errInvalidJTI.
func TestRevoke_MalformedJTI_ExitInputErr(t *testing.T) {
	t.Parallel()
	root := newRootCmd(&outputContext{stdout: newStream(&bytes.Buffer{}, false, true), stderr: newStream(&bytes.Buffer{}, false, true)})
	root.SetArgs([]string{"revoke", "--server", "http://127.0.0.1:1", "--jti", "deadbeef"})
	root.SetContext(t.Context())
	err := root.Execute()
	if got := mapErr(err); got != ExitInputErr {
		t.Errorf("mapErr = %d, want ExitInputErr", got)
	}
}

// TestRevoke_ConnectionRefused_ExitErr asserts closed port → ExitErr
// with the literal-text classifier.
func TestRevoke_ConnectionRefused_ExitErr(t *testing.T) {
	t.Parallel()
	closedURL := "http://127.0.0.1:1"
	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	deps := revokeDeps{signKey: newRevokeKey(t)}
	err := runRevoke(t.Context(), out, errStream, deps, closedURL, validJTI)
	if got := mapErr(err); got != ExitErr {
		t.Errorf("mapErr = %d, want ExitErr", got)
	}
	if !strings.Contains(stderr.String(), "connection refused") {
		t.Errorf("stderr missing classifier: %q", stderr.String())
	}
}

// TestRevoke_5xxBodyExcerptSanitized asserts control characters in
// a 5xx body are replaced with '?'.
func TestRevoke_5xxBodyExcerptSanitized(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("boom\x00\x01control"))
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	deps := revokeDeps{signKey: newRevokeKey(t)}
	_ = runRevoke(t.Context(), out, errStream, deps, srv.URL, validJTI)
	if !strings.Contains(stderr.String(), "boom??control") {
		t.Errorf("expected sanitized excerpt, got %q", stderr.String())
	}
}

// TestRevoke_OutputNoSentinel asserts the SECRET sentinel never
// leaks via the success path: when the JTI value contains the
// sentinel marker, the success rendering echoes it (the JTI is not
// secret) — but no other path may emit signature, key, or
// passphrase bytes. Here we plant the sentinel as a custom
// server-response *header* and assert it never bleeds into stderr's
// classifier message.
func TestRevoke_OutputNoSentinel(t *testing.T) {
	t.Parallel()
	sentinel := testutil.SentinelSecret(14)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Test-Sentinel", sentinel)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	deps := revokeDeps{signKey: newRevokeKey(t)}
	_ = runRevoke(t.Context(), out, errStream, deps, srv.URL, validJTI)
	// stderr must NEVER contain the sentinel — the classifier
	// surfaces only categorical strings, never server-controlled
	// header values.
	testutil.AssertSentinelAbsent(t, sentinel, stderr.String())
}

// TestRevoke_TTYSuccessMessage_NonTTY_JSONShape asserts the success
// rendering shape contract.
func TestRevoke_TTYSuccessMessage_NonTTY_JSONShape(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	t.Run("TTY", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		out := newStream(&stdout, true, false)
		errStream := newStream(&stderr, false, true)
		deps := revokeDeps{signKey: newRevokeKey(t)}
		if err := runRevoke(t.Context(), out, errStream, deps, srv.URL, validJTI); err != nil {
			t.Fatalf("runRevoke: %v", err)
		}
		want := "revoked jti=" + validJTI
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("TTY stdout = %q, want %q", stdout.String(), want)
		}
	})

	t.Run("non-TTY", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		out := newStream(&stdout, false, true)
		errStream := newStream(&stderr, false, true)
		deps := revokeDeps{signKey: newRevokeKey(t)}
		if err := runRevoke(t.Context(), out, errStream, deps, srv.URL, validJTI); err != nil {
			t.Fatalf("runRevoke: %v", err)
		}
		want := `{"revoked":"` + validJTI + `"}`
		if got := strings.TrimSpace(stdout.String()); got != want {
			t.Errorf("non-TTY stdout = %q, want %q", got, want)
		}
	})
}

// TestRevoke_NonceUniquePerCall asserts two consecutive calls
// generate distinct nonces.
func TestRevoke_NonceUniquePerCall(t *testing.T) {
	t.Parallel()
	var captured []revokePayload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env revokeRequest
		if err := json.Unmarshal(body, &env); err != nil {
			t.Errorf("decode env: %v", err)
		}
		var p revokePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		captured = append(captured, p)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		out := newStream(&stdout, false, true)
		errStream := newStream(&stderr, false, true)
		deps := revokeDeps{signKey: newRevokeKey(t)}
		if err := runRevoke(t.Context(), out, errStream, deps, srv.URL, validJTI); err != nil {
			t.Fatalf("runRevoke %d: %v", i, err)
		}
	}
	if len(captured) != 2 {
		t.Fatalf("captured %d, want 2", len(captured))
	}
	if captured[0].Nonce == captured[1].Nonce {
		t.Errorf("nonces collided: %s", captured[0].Nonce)
	}
}

// TestRevoke_NeverPrintsSigningKey asserts no signature byte appears
// in any output stream — verbose tracing prints the canonical JSON
// only, never the signature itself.
func TestRevoke_NeverPrintsSigningKey(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	key := newRevokeKey(t)
	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	deps := revokeDeps{signKey: key}
	if err := runRevoke(t.Context(), out, errStream, deps, srv.URL, validJTI); err != nil {
		t.Fatalf("runRevoke: %v", err)
	}
	// Snapshot the raw scalar via the deprecated D field for
	// leak-detection only (we never mutate it).
	keyHex := fmt.Sprintf("%x", key.D.Bytes()) //nolint:staticcheck // SA1019: read-only access for leak assertion
	if strings.Contains(stdout.String()+stderr.String(), keyHex) {
		t.Errorf("private-key bytes leaked into output streams")
	}
}
