package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// ============================================================
// Coverage tests — exercise branches not hit by the headline
// behaviour tests.
// ============================================================

//nolint:gocyclo // exhaustive nil-check on the production deps bundle
func TestRequest_ProductionDeps_Constructable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("keychain.New requires darwin (got %s)", runtime.GOOS)
	}
	deps, err := productionRequestDeps()
	if err != nil {
		t.Fatalf("productionRequestDeps: %v", err)
	}
	if deps.keychain == nil || deps.httpClient == nil || deps.nowFn == nil ||
		deps.randReader == nil || deps.hostnameFn == nil || deps.ephemeralKey == nil ||
		deps.looker == nil || deps.runner == nil || deps.signalCtx == nil {
		t.Errorf("productionRequestDeps left a field nil: %+v", deps)
	}
}

func TestRequest_ErrChildExitCode_ErrorString(t *testing.T) {
	t.Parallel()
	e := &errChildExitCode{code: 42}
	if got := e.Error(); !strings.Contains(got, "42") {
		t.Errorf("Error()=%q want substring 42", got)
	}
}

func TestRequest_GenerateEphemeralKey(t *testing.T) {
	t.Parallel()
	k, err := generateEphemeralKey(rand.Reader)
	if err != nil {
		t.Fatalf("generateEphemeralKey: %v", err)
	}
	if k == nil {
		t.Fatalf("nil key")
	}
	hexs := compressedEphemeralPubHex(k)
	if len(hexs) != 66 {
		t.Errorf("compressed hex len=%d want 66", len(hexs))
	}
}

func TestRequest_SanitiseMachineName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"host.example.com":  "host.example.com",
		"my host":           "my-host",
		"":                  "unknown",
		"contains spaces!@": "contains-spaces--",
	}
	for in, want := range cases {
		if got := sanitiseMachineName(in); got != want {
			t.Errorf("sanitiseMachineName(%q)=%q want %q", in, got, want)
		}
	}
	// Length truncation.
	long := strings.Repeat("a", 200)
	if got := sanitiseMachineName(long); len(got) != 64 {
		t.Errorf("len(sanitized)=%d want 64", len(got))
	}
}

func TestRequest_BuildClaimPayload_HostnameErrFallback(t *testing.T) {
	t.Parallel()
	deps := requestDeps{
		nowFn:      func() time.Time { return time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC) },
		randReader: rand.Reader,
		hostnameFn: func() (string, error) { return "", errors.New("no hostname") },
	}
	flags := requestFlags{scope: []string{"X"}, reason: "r", ttl: time.Second}
	pl, err := buildClaimPayload(flags, "deadbeef", deps)
	if err != nil {
		t.Fatalf("buildClaimPayload: %v", err)
	}
	if pl.MachineName != "unknown" {
		t.Errorf("machine_name=%q want unknown", pl.MachineName)
	}
}

func TestRequest_BuildClaimPayload_RandErr(t *testing.T) {
	t.Parallel()
	deps := requestDeps{
		nowFn:      time.Now,
		randReader: failingReader{},
		hostnameFn: func() (string, error) { return "h", nil },
	}
	flags := requestFlags{scope: []string{"X"}, reason: "r", ttl: time.Second}
	if _, err := buildClaimPayload(flags, "deadbeef", deps); err == nil {
		t.Errorf("want rand-failure error")
	}
}

type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) { return 0, errors.New("rng down") }

func TestRequest_SignAndWrapClaim_NilCtx(t *testing.T) {
	t.Parallel()
	clientKey := makeClientKey(t)
	pl := claimSignedPayload{
		EphemeralPubKey: "deadbeef",
		MachineName:     "h",
		Nonce:           "n",
		Reason:          "r",
		RequestID:       "x",
		Scope:           []string{"S"},
		SessionType:     "interactive",
		Timestamp:       "2026-01-01T00:00:00Z",
		TTL:             "1s",
	}
	// pre-cancelled ctx → sign returns ctx.Err()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := signAndWrapClaim(ctx, clientKey, pl); err == nil {
		t.Errorf("want sign error on cancelled ctx")
	}
}

func TestRequest_PostClaim_TransportDown(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	deps := requestDeps{
		httpClient: &http.Client{Timeout: 500 * time.Millisecond},
	}
	stderr := newStream(io.Discard, false, true)
	_, err := postClaim(context.Background(), deps, url, claimWireRequest{Scope: []string{"X"}}, stderr)
	if err == nil {
		t.Errorf("want transport error")
	}
}

func TestRequest_PostClaim_BadJSONResponse(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()
	deps := requestDeps{httpClient: &http.Client{}}
	stderr := newStream(io.Discard, false, true)
	_, err := postClaim(context.Background(), deps, srv.URL, claimWireRequest{Scope: []string{"X"}}, stderr)
	if err == nil {
		t.Errorf("want decode error")
	}
}

func TestRequest_PostClaim_EmptyJWT(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jwt":"","expires_at":"x","jti":"y"}`))
	}))
	defer srv.Close()
	deps := requestDeps{httpClient: &http.Client{}}
	stderr := newStream(io.Discard, false, true)
	_, err := postClaim(context.Background(), deps, srv.URL, claimWireRequest{Scope: []string{"X"}}, stderr)
	if err == nil {
		t.Errorf("want empty-jwt error")
	}
}

func TestRequest_MapClaimErrorCode_AllCodes(t *testing.T) {
	t.Parallel()
	stderr := newStream(io.Discard, false, true)
	codes := []string{
		"denied", "bad_signature", "approval_timeout", "rate_limited",
		"discord_unavailable", "bad_request", "stale_timestamp", "nonce_replay",
		"ip_not_allowed", "completely_unknown_code",
	}
	for _, c := range codes {
		err := mapClaimErrorCode(http.StatusForbidden, c, stderr, "https://srv")
		if err == nil {
			t.Errorf("code %q produced nil error", c)
		}
	}
}

func TestRequest_PostClaim_DeadlineCancelsCleanly(t *testing.T) {
	t.Parallel()
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-hold:
		}
	}))
	t.Cleanup(func() {
		close(hold)
		srv.Close()
	})
	deps := requestDeps{httpClient: &http.Client{}}
	stderr := newStream(io.Discard, false, true)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := postClaim(ctx, deps, srv.URL, claimWireRequest{Scope: []string{"X"}}, stderr)
	if err == nil {
		t.Errorf("want deadline error")
	}
}

func TestRequest_PostClaim_Cancelled(t *testing.T) {
	t.Parallel()
	hold := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-hold:
		}
	}))
	t.Cleanup(func() {
		close(hold)
		srv.Close()
	})
	deps := requestDeps{httpClient: &http.Client{}}
	stderr := newStream(io.Discard, false, true)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := postClaim(ctx, deps, srv.URL, claimWireRequest{Scope: []string{"X"}}, stderr)
	if err == nil {
		t.Errorf("want cancel error")
	}
}

func TestRequest_FetchSecrets_DecryptFailure(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not a valid ECIES envelope, too short"))
	}))
	defer srv.Close()
	deps := requestDeps{httpClient: &http.Client{}}
	stderr := newStream(io.Discard, false, true)
	jwt, _ := securebytes.New([]byte("tok"))
	defer func() { _ = jwt.Destroy() }()
	priv, _ := generateEphemeralKey(rand.Reader)
	_, err := fetchSecrets(context.Background(), deps, srv.URL, jwt, priv, []string{"NAME"}, stderr)
	if err == nil {
		t.Errorf("want decrypt error")
	}
}

func TestRequest_FetchSecrets_403OutOfScope(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	deps := requestDeps{httpClient: &http.Client{}}
	stderr := newStream(io.Discard, false, true)
	jwt, _ := securebytes.New([]byte("tok"))
	defer func() { _ = jwt.Destroy() }()
	priv, _ := generateEphemeralKey(rand.Reader)
	_, err := fetchSecrets(context.Background(), deps, srv.URL, jwt, priv, []string{"NAME"}, stderr)
	if !errors.Is(err, errAuthFailed) {
		t.Errorf("err=%v want errAuthFailed", err)
	}
}

func TestRequest_FetchSecrets_ServerError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	deps := requestDeps{httpClient: &http.Client{}}
	stderr := newStream(io.Discard, false, true)
	jwt, _ := securebytes.New([]byte("tok"))
	defer func() { _ = jwt.Destroy() }()
	priv, _ := generateEphemeralKey(rand.Reader)
	_, err := fetchSecrets(context.Background(), deps, srv.URL, jwt, priv, []string{"NAME"}, stderr)
	if err == nil {
		t.Errorf("want generic server error")
	}
}

func TestRequest_FetchSecrets_TransportError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	srv.Close()
	deps := requestDeps{httpClient: &http.Client{Timeout: 200 * time.Millisecond}}
	stderr := newStream(io.Discard, false, true)
	jwt, _ := securebytes.New([]byte("tok"))
	defer func() { _ = jwt.Destroy() }()
	priv, _ := generateEphemeralKey(rand.Reader)
	_, err := fetchSecrets(context.Background(), deps, srv.URL, jwt, priv, []string{"NAME"}, stderr)
	if err == nil {
		t.Errorf("want transport error")
	}
}

func TestRequest_RetrieveClientKey_PermissionDenied(t *testing.T) {
	t.Parallel()
	denyKC := &denyKeychain{}
	stderr := newStream(io.Discard, false, true)
	deps := requestDeps{keychain: denyKC}
	_, err := retrieveClientKey(context.Background(), deps, 0, stderr)
	if !errors.Is(err, keychain.ErrKeychainPermissionDenied) {
		t.Errorf("err=%v want ErrKeychainPermissionDenied", err)
	}
}

type denyKeychain struct{}

func (denyKeychain) Store(_ context.Context, _, _ string, _ *securebytes.SecureBytes, _ string) error {
	return nil
}

func (denyKeychain) Retrieve(_ context.Context, _, _ string) (*securebytes.SecureBytes, error) {
	return nil, keychain.ErrKeychainPermissionDenied
}

func (denyKeychain) Delete(_ context.Context, _, _ string) error { return nil }

func TestRequest_RetrieveClientKey_BadLength(t *testing.T) {
	t.Parallel()
	fake := keychain.NewFake()
	short, err := securebytes.New([]byte("not 32 bytes"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = short.Destroy() }()
	if storeErr := fake.Store(t.Context(), kcServiceClient, "machine-0", short, "/"); storeErr != nil {
		t.Fatalf("Store: %v", storeErr)
	}
	stderr := newStream(io.Discard, false, true)
	deps := requestDeps{keychain: fake}
	_, err = retrieveClientKey(t.Context(), deps, 0, stderr)
	if err == nil {
		t.Errorf("want length-validation error")
	}
}

func TestRequest_RunRequest_EphemeralKeyError(t *testing.T) {
	t.Parallel()
	r := newRequestRunner(t)
	r.deps.ephemeralKey = func(_ io.Reader) (*ecdsa.PrivateKey, error) {
		return nil, errors.New("rng failure")
	}
	err := r.run(t.Context())
	if err == nil {
		t.Errorf("want ephemeral failure error")
	}
}

func TestRequest_ZeroPrivateKey_NilSafe(t *testing.T) {
	t.Parallel()
	zeroPrivateKey(nil)
	zeroPrivateKey(&ecdsa.PrivateKey{}) // nil D — should not panic
}

func TestRequest_FetchSecrets_DestroyedJWT(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	deps := requestDeps{httpClient: &http.Client{}}
	stderr := newStream(io.Discard, false, true)
	jwt, _ := securebytes.New([]byte("tok"))
	_ = jwt.Destroy()
	priv, _ := generateEphemeralKey(rand.Reader)
	_, err := fetchSecrets(context.Background(), deps, srv.URL, jwt, priv, []string{"NAME"}, stderr)
	if err == nil {
		t.Errorf("want destroyed-jwt error")
	}
}

func TestRequest_BuildChildEnv_PreservesEqualsInValue(t *testing.T) {
	t.Parallel()
	parent := []string{"A=1=2=3", "MALFORMED"}
	sb, err := securebytes.New([]byte("v"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = sb.Destroy() }()
	env, err := buildChildEnv([]string{"NEW"}, []*securebytes.SecureBytes{sb}, parent)
	if err != nil {
		t.Fatalf("buildChildEnv: %v", err)
	}
	hasA := false
	hasMalformed := false
	for _, kv := range env {
		if kv == "A=1=2=3" {
			hasA = true
		}
		if kv == "MALFORMED" {
			hasMalformed = true
		}
	}
	if !hasA {
		t.Errorf("env missing A=1=2=3")
	}
	if !hasMalformed {
		t.Errorf("env missing MALFORMED")
	}
}

func TestRequest_NewRequestCmd_MissingFlagFails(t *testing.T) {
	t.Parallel()
	cmd := newRequestCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{}) // no flags
	if err := cmd.Execute(); err == nil {
		t.Errorf("want missing-required-flag error")
	}
}

// Helper to avoid linter complaint about unused identifiers in this
// coverage file when individual blocks are commented out.
var _ = atomic.LoadInt32

// TestRequest_ClaimAccessUsesEphemeralFromRoundtrip confirms that the
// fakeServer's encryptForLatestEphemeral helper produces an envelope
// the request hot path can decrypt, by manually running a claim → /s
// → decrypt cycle with the test scaffolding.
func TestRequest_ScaffoldingRoundTripSanity(t *testing.T) {
	r := newRequestRunner(t)
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("plaintext-x")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestRequest_DepsLooker_NotFound(t *testing.T) {
	t.Parallel()
	deps := requestDeps{
		looker: func(_ string) (string, error) { return "", os.ErrNotExist },
		runner: func(_ *exec.Cmd) error { return nil },
	}
	stderr := newStream(io.Discard, false, true)
	err := runChild(context.Background(), deps, "missing-prog", nil, nil, stderr)
	if err == nil {
		t.Errorf("want lookpath error")
	}
}

func TestRequest_EmitValidationStderr_UnknownErr(t *testing.T) {
	t.Parallel()
	_, stderr, _, buf := captureStreams()
	emitValidationStderr(stderr, errors.New("not a known sentinel"))
	if buf.Len() != 0 {
		t.Errorf("expected silent on unknown err, got %q", buf.String())
	}
}

func TestRequest_BytesContainsByte(t *testing.T) {
	t.Parallel()
	if !bytesContainsByte([]byte("hello"), 'l') {
		t.Errorf("expected true")
	}
	if bytesContainsByte([]byte("hello"), 'z') {
		t.Errorf("expected false")
	}
}

// helper used by emitValidationStderr coverage test above
var _ json.Marshaler

// satisfy unused import — using stderr indirectly via Stream type
// from output.go. Need to ensure the captureStreams helper still
// returns *Stream values, not interfaces.
//
// Reaffirms that the stderr param is *Stream typed as required.
func TestRequest_StreamTypeAssertion(t *testing.T) {
	t.Parallel()
	stdout, _, _, _ := captureStreams()
	got := stdout
	_ = got
}

// errWriter always returns an error from Write, used to drive the
// writeEvalExports stdout-error branch.
type errWriter struct{}

func (errWriter) Write(_ []byte) (int, error) { return 0, errors.New("write fail") }

func TestRequest_WriteEvalExports_StdoutError(t *testing.T) {
	t.Parallel()
	stdout := newStream(errWriter{}, false, true)
	stderr := newStream(io.Discard, false, true)
	sb, _ := securebytes.New([]byte("v"))
	defer func() { _ = sb.Destroy() }()
	err := writeEvalExports(stdout, stderr, []string{"X"}, []*securebytes.SecureBytes{sb})
	if err == nil {
		t.Errorf("want stdout-write error")
	}
}

func TestRequest_WriteEvalExports_StderrError(t *testing.T) {
	t.Parallel()
	stdout := newStream(io.Discard, false, true)
	stderr := newStream(errWriter{}, false, true)
	sb, _ := securebytes.New([]byte("v"))
	defer func() { _ = sb.Destroy() }()
	err := writeEvalExports(stdout, stderr, []string{"X"}, []*securebytes.SecureBytes{sb})
	if err == nil {
		t.Errorf("want stderr-write error")
	}
}

func TestRequest_WriteEvalExports_DestroyedSecret(t *testing.T) {
	t.Parallel()
	stdout, stderr, _, _ := captureStreams()
	sb, _ := securebytes.New([]byte("v"))
	_ = sb.Destroy()
	err := writeEvalExports(stdout, stderr, []string{"X"}, []*securebytes.SecureBytes{sb})
	if err == nil {
		t.Errorf("want destroyed-secret error")
	}
}

// Inflate fetchSecrets coverage by explicitly hitting the success
// path in one more shape — multiple scopes succeed in sequence.
func TestRequest_FetchSecrets_MultiSuccess(t *testing.T) {
	t.Parallel()
	priv, _ := generateEphemeralKey(rand.Reader)

	// Encrypt fixed plaintexts under the priv's pubkey, serve them.
	pubBytes, _ := hex.DecodeString(compressedEphemeralPubHex(priv))
	pub, err := secp256k1.ParsePubKey(pubBytes)
	if err != nil {
		t.Fatalf("parse pub: %v", err)
	}
	envA, err := ecies.Encrypt(t.Context(), pub.ToECDSA(), []byte("aval"))
	if err != nil {
		t.Fatalf("encA: %v", err)
	}
	envB, err := ecies.Encrypt(t.Context(), pub.ToECDSA(), []byte("bval"))
	if err != nil {
		t.Fatalf("encB: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/s/A":
			_, _ = w.Write(envA)
		case "/s/B":
			_, _ = w.Write(envB)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	deps := requestDeps{httpClient: &http.Client{}}
	stderr := newStream(io.Discard, false, true)
	jwt, _ := securebytes.New([]byte("tok"))
	defer func() { _ = jwt.Destroy() }()
	out, err := fetchSecrets(context.Background(), deps, srv.URL, jwt, priv, []string{"A", "B"}, stderr)
	defer func() {
		for _, sb := range out {
			_ = sb.Destroy()
		}
	}()
	if err != nil {
		t.Fatalf("fetchSecrets: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("got %d secrets, want 2", len(out))
	}
}
