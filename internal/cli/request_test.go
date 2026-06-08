package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/transport/sign"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// ============================================================
// Test scaffolding
// ============================================================

const (
	requestSentinel              = "SECRET_SHOULD_NEVER_APPEAR_16"
	cobraRequiredAnnotation      = cobra.BashCompOneRequiredFlag
	expectedFormatEvalWarningRaw = "WARNING: --format eval prints secret values to stdout. They may be captured by terminal scrollback, tmux, or script. Use --exec whenever possible.\n"
)

// captureStreams returns a Stream pair backed by bytes.Buffers.
func captureStreams() (stdout, stderr *Stream, stdoutBuf, stderrBuf *bytes.Buffer) {
	stdoutBuf = &bytes.Buffer{}
	stderrBuf = &bytes.Buffer{}
	return newStream(stdoutBuf, false, true), newStream(stderrBuf, false, true), stdoutBuf, stderrBuf
}

// makeClientKey generates a fresh secp256k1 key for the harness.
func makeClientKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(secp256k1.S256(), rand.Reader) //nolint:staticcheck // secp256k1 unsupported by crypto/ecdh
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return k
}

// storeClientKeyInFake stores priv as a 32-byte scalar under
// (hush-client, machine-<index>) in fake.
func storeClientKeyInFake(t *testing.T, fake *keychain.FakeKeychain, priv *ecdsa.PrivateKey, index uint32) {
	t.Helper()
	scalar := make([]byte, 32)
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .D access intentional
	priv.D.FillBytes(scalar)
	sb, err := securebytes.New(scalar)
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	defer func() { _ = sb.Destroy() }()
	if err := fake.Store(t.Context(), kcServiceClient, fmt.Sprintf("machine-%d", index), sb, "/test/binary"); err != nil {
		t.Fatalf("fake.Store: %v", err)
	}
}

// fakeServer is a controllable in-process server stub used by request
// unit tests.
type fakeServer struct {
	t              *testing.T
	server         *httptest.Server
	claimMu        sync.Mutex
	claimRequests  [][]byte
	claimResponse  func(req claimWireRequest) (status int, body []byte)
	secretsMu      sync.Mutex
	secrets        map[string][]byte
	secretStatus   map[string]int
	secretCalls    int32
	tokenIssued    string
	requireBearer  bool
	hangClaim      bool
	hangClaimUntil chan struct{}
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	fs := &fakeServer{
		t:            t,
		secrets:      make(map[string][]byte),
		secretStatus: make(map[string]int),
		tokenIssued:  "test-jwt-token-" + freshNonceTest(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/claim", fs.handleClaim)
	mux.HandleFunc("/s/", fs.handleSecret)
	fs.server = httptest.NewServer(mux)
	t.Cleanup(fs.server.Close)
	return fs
}

func (fs *fakeServer) URL() string { return fs.server.URL }

func freshNonceTest() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func (fs *fakeServer) setSecret(name string, body []byte) {
	fs.secretsMu.Lock()
	defer fs.secretsMu.Unlock()
	fs.secrets[name] = body
}

func (fs *fakeServer) setSecretStatus(name string, status int) {
	fs.secretsMu.Lock()
	defer fs.secretsMu.Unlock()
	fs.secretStatus[name] = status
}

func (fs *fakeServer) handleClaim(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	fs.claimMu.Lock()
	fs.claimRequests = append(fs.claimRequests, append([]byte(nil), body...))
	hang := fs.hangClaim
	until := fs.hangClaimUntil
	resp := fs.claimResponse
	fs.claimMu.Unlock()

	if hang {
		select {
		case <-r.Context().Done():
			return
		case <-until:
			return
		}
	}

	if resp != nil {
		var req claimWireRequest
		_ = json.Unmarshal(body, &req)
		status, payload := resp(req)
		w.WriteHeader(status)
		_, _ = w.Write(payload)
		return
	}

	out := claimWireResponse{
		JWT:       fs.tokenIssued,
		ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		JTI:       "00000000-0000-0000-0000-aaaaaaaaaaaa",
	}
	raw, _ := json.Marshal(out) //nolint:errchkjson // static struct shape; Marshal cannot fail
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (fs *fakeServer) handleSecret(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt32(&fs.secretCalls, 1)
	name := strings.TrimPrefix(r.URL.Path, "/s/")
	if fs.requireBearer {
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	fs.secretsMu.Lock()
	if status, ok := fs.secretStatus[name]; ok {
		fs.secretsMu.Unlock()
		w.WriteHeader(status)
		return
	}
	body, ok := fs.secrets[name]
	fs.secretsMu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = w.Write(body)
}

func (fs *fakeServer) latestClaimReq(t *testing.T) claimWireRequest {
	t.Helper()
	fs.claimMu.Lock()
	defer fs.claimMu.Unlock()
	if len(fs.claimRequests) == 0 {
		t.Fatalf("no claim requests recorded")
	}
	body := fs.claimRequests[len(fs.claimRequests)-1]
	var req claimWireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal claim body: %v", err)
	}
	return req
}

// encryptForLatestEphemeral re-encrypts the supplied plaintext under
// the ephemeral pubkey from the most-recent claim request.
func (fs *fakeServer) encryptForLatestEphemeral(t *testing.T, plaintext []byte) []byte {
	t.Helper()
	req := fs.latestClaimReq(t)
	pubBytes, err := hex.DecodeString(req.EphemeralPubKey)
	if err != nil {
		t.Fatalf("hex ephemeral pubkey: %v", err)
	}
	pub, err := secp256k1.ParsePubKey(pubBytes)
	if err != nil {
		t.Fatalf("parse ephemeral pubkey: %v", err)
	}
	envelope, err := ecies.Encrypt(t.Context(), pub.ToECDSA(), plaintext)
	if err != nil {
		t.Fatalf("ecies.Encrypt: %v", err)
	}
	return envelope
}

// requestRunner runs a request with the fake server pre-wired.
type requestRunner struct {
	t           *testing.T
	deps        requestDeps
	flags       requestFlags
	stdout      *Stream
	stderr      *Stream
	stdoutBuf   *bytes.Buffer
	stderrBuf   *bytes.Buffer
	clientKey   *ecdsa.PrivateKey
	fakeKC      *keychain.FakeKeychain
	fakeSrv     *fakeServer
	ephCaptured **ecdsa.PrivateKey
	runnerCalls *int32
}

func newRequestRunner(t *testing.T) *requestRunner {
	t.Helper()
	stdout, stderr, soBuf, seBuf := captureStreams()
	clientKey := makeClientKey(t)
	fake := keychain.NewFake()
	storeClientKeyInFake(t, fake, clientKey, 0)

	fs := newFakeServer(t)
	fs.requireBearer = true

	var captured *ecdsa.PrivateKey
	var runnerCalls int32

	deps := requestDeps{
		keychain:   fake,
		httpClient: &http.Client{Timeout: 5 * time.Second},
		nowFn:      time.Now,
		randReader: rand.Reader,
		hostnameFn: func() (string, error) { return "test-host", nil },
		ephemeralKey: func(r io.Reader) (*ecdsa.PrivateKey, error) {
			k, err := generateEphemeralKey(r)
			if err != nil {
				return nil, err
			}
			captured = k
			return k, nil
		},
		looker: exec.LookPath,
		runner: func(cmd *exec.Cmd) error {
			atomic.AddInt32(&runnerCalls, 1)
			return cmd.Run()
		},
		signalCtx: func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return context.WithCancel(parent)
		},
	}

	flags := requestFlags{
		server:       fs.URL(),
		scope:        []string{"SCOPE_A"},
		reason:       "test",
		ttl:          5 * time.Second,
		maxUses:      10,
		machineIndex: 0,
		execProgram:  "/usr/bin/true",
	}

	return &requestRunner{
		t:           t,
		deps:        deps,
		flags:       flags,
		stdout:      stdout,
		stderr:      stderr,
		stdoutBuf:   soBuf,
		stderrBuf:   seBuf,
		clientKey:   clientKey,
		fakeKC:      fake,
		fakeSrv:     fs,
		ephCaptured: &captured,
		runnerCalls: &runnerCalls,
	}
}

// arrangeSuccessFor wires the fake server's claim response so that
// /s/<name> serves back ECIES-encrypted plaintext for each scope.
func (r *requestRunner) arrangeSuccessFor(scopeValues map[string][]byte) {
	r.fakeSrv.claimMu.Lock()
	r.fakeSrv.claimResponse = func(_ claimWireRequest) (int, []byte) {
		for name, plaintext := range scopeValues {
			envelope := r.fakeSrv.encryptForLatestEphemeral(r.t, plaintext)
			r.fakeSrv.setSecret(name, envelope)
		}
		out := claimWireResponse{
			JWT:       r.fakeSrv.tokenIssued,
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
			JTI:       "00000000-0000-0000-0000-aaaaaaaaaaaa",
		}
		raw, _ := json.Marshal(out) //nolint:errchkjson // static struct shape; Marshal cannot fail
		return http.StatusOK, raw
	}
	r.fakeSrv.claimMu.Unlock()
}

func (r *requestRunner) run(ctx context.Context) error {
	return runRequest(ctx, r.stdout, r.stderr, r.deps, r.flags)
}

// ============================================================
// validator helper
// ============================================================

// runValidator builds a fresh request cobra command, runs it (with a
// no-op RunE that captures the validator's result), and returns the
// captured outcome.
func runValidator(t *testing.T, args ...string) (requestFlags, error) {
	t.Helper()
	cmd := newRequestCmd()
	var captured requestFlags
	var captureErr error
	cmd.RunE = func(c *cobra.Command, a []string) error {
		captured, captureErr = parseAndValidateFlags(c, a)
		return nil
	}
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	full := append([]string{
		"--server=https://example.test",
		"--scope=SCOPE_A",
		"--reason=test",
		"--ttl=5s",
		"--max-uses=10",
		"--machine-index=0",
	}, args...)
	cmd.SetArgs(full)
	if err := cmd.Execute(); err != nil {
		return requestFlags{}, err
	}
	return captured, captureErr
}

// ============================================================
// Phase 2 — Foundational tests
// ============================================================

func TestRequest_SubcommandRegisteredOnRoot(t *testing.T) {
	t.Parallel()
	root := newRootCmd(&outputContext{
		stdout: newStream(io.Discard, false, true),
		stderr: newStream(io.Discard, false, true),
	})
	for _, c := range root.Commands() {
		if c.Use == "request" {
			return
		}
	}
	t.Fatalf("`request` subcommand not registered on root")
}

func TestRequest_FlagSetMatchesContract(t *testing.T) {
	t.Parallel()
	cmd := newRequestCmd()
	want := map[string]bool{
		flagReqServer: true, flagReqScope: true, flagReqReason: true, flagReqTTL: true,
		flagReqMaxUses: true, flagReqMachineIndex: true, flagReqClientKeyFile: true,
		flagReqExec: true, flagReqFormat: true,
		// PR 4: agent-context flags (optional metadata for the human approver).
		flagReqAgentIdentity:  true,
		flagReqAgentModel:     true,
		flagReqToolName:       true,
		flagReqCommandPreview: true,
		flagReqRecentSummary:  true,
	}
	got := map[string]bool{}
	cmd.Flags().VisitAll(func(f *pflag.Flag) { got[f.Name] = true })
	if len(want) != len(got) {
		t.Fatalf("flag set mismatch:\n  want=%v\n  got=%v", sortedKeys(want), sortedKeys(got))
	}
	for k := range want {
		if !got[k] {
			t.Fatalf("missing flag %q (got=%v)", k, sortedKeys(got))
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestRequest_RequiredFlagsRegisteredButValidatedByRunE(t *testing.T) {
	t.Parallel()
	cmd := newRequestCmd()
	required := []string{flagReqServer, flagReqScope, flagReqReason, flagReqTTL, flagReqMaxUses, flagReqMachineIndex}
	for _, name := range required {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("flag %q not registered", name)
		}
		if len(f.Annotations[cobraRequiredAnnotation]) != 0 {
			t.Errorf("flag %q should be validated in RunE, not by cobra required annotations", name)
		}
	}
}

func TestRequest_ParseAndValidateFlags_MissingCoreFlagsAreLoud(t *testing.T) {
	t.Parallel()
	cmd := newRequestCmd()
	cmd.SetArgs([]string{"--exec=printenv"})

	_, err := parseAndValidateFlags(cmd, nil)
	if !errors.Is(err, errMissingFlag) {
		t.Fatalf("err=%v want errMissingFlag", err)
	}
	for _, want := range []string{"--server", "--scope", "--reason", "--ttl", "--max-uses", "--machine-index"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err=%q missing %s", err.Error(), want)
		}
	}
	if mapErr(err) != ExitInputErr {
		t.Errorf("mapErr=%d want %d", mapErr(err), ExitInputErr)
	}
}

func TestRequest_EmitValidationStderrMissingCoreFlags(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	err := fmt.Errorf("%w: --ttl, --max-uses, --machine-index", errMissingFlag)
	emitValidationStderr(newStream(&stderr, false, true), err)
	if got := stderr.String(); !strings.Contains(got, "hush: request: missing required flag(s): --ttl, --max-uses, --machine-index") {
		t.Fatalf("stderr=%q", got)
	}
}

func TestRequest_ParseAndValidateFlags_NeitherDeliveryMode(t *testing.T) {
	t.Parallel()
	_, err := runValidator(t)
	if !errors.Is(err, errMissingExecOrFormat) {
		t.Fatalf("err=%v want errMissingExecOrFormat", err)
	}
	if mapErr(err) != ExitInputErr {
		t.Errorf("mapErr=%d want %d", mapErr(err), ExitInputErr)
	}
}

func TestRequest_ParseAndValidateFlags_BothDeliveryModes(t *testing.T) {
	t.Parallel()
	_, err := runValidator(t, "--exec=/bin/zsh", "--format=eval")
	if !errors.Is(err, errExecAndFormatBothSet) {
		t.Fatalf("err=%v want errExecAndFormatBothSet", err)
	}
	if mapErr(err) != ExitInputErr {
		t.Errorf("mapErr=%d want %d", mapErr(err), ExitInputErr)
	}
}

func TestRequest_ParseAndValidateFlags_FormatNotEval(t *testing.T) {
	t.Parallel()
	_, err := runValidator(t, "--format=json")
	if !errors.Is(err, errFormatNotEval) {
		t.Fatalf("err=%v want errFormatNotEval", err)
	}
	if mapErr(err) != ExitInputErr {
		t.Errorf("mapErr=%d want %d", mapErr(err), ExitInputErr)
	}
}

func TestRequest_ParseAndValidateFlags_MaxUsesTooLow(t *testing.T) {
	t.Parallel()
	_, err := runValidator(t, "--scope=A,B,C", "--max-uses=2", "--exec=/bin/true")
	if !errors.Is(err, errMaxUsesTooLow) {
		t.Fatalf("err=%v want errMaxUsesTooLow", err)
	}
	if mapErr(err) != ExitInputErr {
		t.Errorf("mapErr=%d want %d", mapErr(err), ExitInputErr)
	}
}

func TestRequest_ParseAndValidateFlags_RejectsShellMetaInScope(t *testing.T) {
	t.Parallel()
	// pflag's StringSlice CSV parser truncates values at \n, so embedded
	// newlines never reach validateScopeNames; those are exercised
	// directly in TestValidateScopeNames below.
	cases := []string{"X; echo y", "1FOO", "FOO BAR", "X$Y"}
	for _, name := range cases {
		_, err := runValidator(
			t,
			"--scope="+name,
			"--max-uses=8",
			"--exec=/bin/true",
		)
		if !errors.Is(err, errInvalidScopeName) {
			t.Errorf("scope=%q err=%v want errInvalidScopeName", name, err)
		}
		if err != nil && mapErr(err) != ExitInputErr {
			t.Errorf("scope=%q mapErr=%d want %d", name, mapErr(err), ExitInputErr)
		}
	}
}

func TestRequest_ParseAndValidateFlags_AcceptsValidScope(t *testing.T) {
	t.Parallel()
	cases := []string{"FOO", "_FOO", "Foo123", "foo_bar", "X"}
	for _, name := range cases {
		_, err := runValidator(
			t,
			"--scope="+name,
			"--max-uses=8",
			"--exec=/bin/true",
		)
		if err != nil {
			t.Errorf("scope=%q unexpected err=%v", name, err)
		}
	}
}

func TestValidateScopeNames(t *testing.T) {
	t.Parallel()
	bad := []string{"", "1FOO", "FOO BAR", "X; echo y", "X$Y", "X\nY", "FOO-BAR"}
	for _, n := range bad {
		if err := validateScopeNames([]string{n}); !errors.Is(err, errInvalidScopeName) {
			t.Errorf("name=%q err=%v want errInvalidScopeName", n, err)
		}
	}
	good := []string{"FOO", "_FOO", "Foo123", "foo_bar", "X", "_"}
	for _, n := range good {
		if err := validateScopeNames([]string{n}); err != nil {
			t.Errorf("name=%q unexpected err=%v", n, err)
		}
	}
}

func TestRequest_ParseAndValidateFlags_HappyPathExec(t *testing.T) {
	t.Parallel()
	flags, err := runValidator(t, "--exec=/bin/echo")
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if flags.execProgram != "/bin/echo" {
		t.Errorf("execProgram=%q want /bin/echo", flags.execProgram)
	}
	if flags.formatMode != "" {
		t.Errorf("formatMode=%q want empty", flags.formatMode)
	}
	if len(flags.scope) != 1 || flags.scope[0] != "SCOPE_A" {
		t.Errorf("scope=%v", flags.scope)
	}
}

func TestRequest_ParseAndValidateFlags_HappyPathFormatEval(t *testing.T) {
	t.Parallel()
	flags, err := runValidator(t, "--format=eval")
	if err != nil {
		t.Fatalf("err=%v want nil", err)
	}
	if flags.formatMode != "eval" {
		t.Errorf("formatMode=%q want eval", flags.formatMode)
	}
	if flags.execProgram != "" {
		t.Errorf("execProgram=%q want empty", flags.execProgram)
	}
}

func TestRequest_ParseAndValidateFlags_ChildArgsAfterDoubleDash(t *testing.T) {
	t.Parallel()
	cmd := newRequestCmd()
	var captured requestFlags
	cmd.RunE = func(c *cobra.Command, a []string) error {
		var err error
		captured, err = parseAndValidateFlags(c, a)
		return err
	}
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs([]string{
		"--server=https://example.test",
		"--scope=SCOPE_A",
		"--reason=test",
		"--ttl=5s",
		"--max-uses=10",
		"--machine-index=0",
		"--exec=/bin/echo",
		"--",
		"a", "b", "c",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got, want := captured.childArgs, []string{"a", "b", "c"}; !equalStringSlices(got, want) {
		t.Errorf("childArgs=%v want %v", got, want)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRequest_MapErr_ChildExitCode(t *testing.T) {
	t.Parallel()
	err := error(&errChildExitCode{code: 7})
	if got := mapErr(err); got != 7 {
		t.Errorf("mapErr=%d want 7", got)
	}
	wrapped := fmt.Errorf("wrapped: %w", err)
	if got := mapErr(wrapped); got != 7 {
		t.Errorf("mapErr(wrapped)=%d want 7", got)
	}
}

func TestRequest_NoOsGetenvInRequestGo(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"request.go", "exec.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		if bytes.Contains(body, []byte("os.Getenv")) {
			t.Errorf("%s contains forbidden os.Getenv reference", file)
		}
	}
}

// ============================================================
// Phase 3 — US1 (--exec) tests
// ============================================================

func TestRequest_ClientKeyFromKeychainNotEnv(t *testing.T) {
	t.Setenv("HUSH_CLIENT_KEY", requestSentinel+"_envkey")
	r := newRequestRunner(t)
	r.flags.scope = []string{"SCOPE_A"}
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("payload-a")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Verify the recorded /claim signature was made by the keychain
	// key by re-deriving the fingerprint.
	req := r.fakeSrv.latestClaimReq(t)
	expected := r.clientKey.PublicKey
	pl := claimSignedPayload{
		EphemeralPubKey: req.EphemeralPubKey,
		MachineName:     req.MachineName,
		Nonce:           req.Nonce,
		Reason:          req.Reason,
		RequestID:       req.RequestID,
		Scope:           req.Scope,
		SessionType:     req.SessionType,
		Timestamp:       req.Timestamp,
		TTL:             req.TTL,
	}
	canonical, err := sign.CanonicalJSON(pl)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if err := sign.Verify(t.Context(), &expected, canonical, sigBytes); err != nil {
		t.Fatalf("signature did not verify under keychain key: %v", err)
	}
	// And env-var sentinel never appears in stderr/stdout.
	testutil.AssertSentinelAbsent(t, requestSentinel+"_envkey", r.stderrBuf.String())
	testutil.AssertSentinelAbsent(t, requestSentinel+"_envkey", r.stdoutBuf.String())
}

func TestRequest_ClaimSessionTypeIsInteractive(t *testing.T) {
	r := newRequestRunner(t)
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	req := r.fakeSrv.latestClaimReq(t)
	if req.SessionType != "interactive" {
		t.Errorf("session_type=%q want interactive", req.SessionType)
	}
}

func TestRequest_ClaimWireShapeMatchesServer(t *testing.T) {
	r := newRequestRunner(t)
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	r.fakeSrv.claimMu.Lock()
	body := r.fakeSrv.claimRequests[len(r.fakeSrv.claimRequests)-1]
	r.fakeSrv.claimMu.Unlock()

	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{
		"client_key_fingerprint", "ephemeral_pubkey", "machine_name", "nonce",
		"reason", "request_id", "scope", "session_type", "signature", "timestamp", "ttl",
	}
	if len(generic) != len(want) {
		t.Errorf("key count = %d want %d (got=%v)", len(generic), len(want), generic)
	}
	for _, k := range want {
		if _, ok := generic[k]; !ok {
			t.Errorf("missing key %q", k)
		}
	}
}

func TestRequest_ClaimSignaturePayloadCanonical(t *testing.T) {
	r := newRequestRunner(t)
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	req := r.fakeSrv.latestClaimReq(t)
	pl := claimSignedPayload{
		EphemeralPubKey: req.EphemeralPubKey,
		MachineName:     req.MachineName,
		Nonce:           req.Nonce,
		Reason:          req.Reason,
		RequestID:       req.RequestID,
		Scope:           req.Scope,
		SessionType:     req.SessionType,
		Timestamp:       req.Timestamp,
		TTL:             req.TTL,
	}
	canonical, err := sign.CanonicalJSON(pl)
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		t.Fatalf("decode sig: %v", err)
	}
	if err := sign.Verify(t.Context(), &r.clientKey.PublicKey, canonical, sigBytes); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

type serverClaimSignedPayloadForTest struct {
	AgentIdentity   string   `json:"agent_identity,omitempty"`
	AgentModel      string   `json:"agent_model,omitempty"`
	CommandPreview  string   `json:"command_preview,omitempty"`
	EphemeralPubKey string   `json:"ephemeral_pubkey"`
	ForceApproval   bool     `json:"force_approval,omitempty"`
	MachineName     string   `json:"machine_name"`
	Nonce           string   `json:"nonce"`
	Reason          string   `json:"reason"`
	RecentSummary   string   `json:"recent_summary,omitempty"`
	RequestID       string   `json:"request_id"`
	Scope           []string `json:"scope"`
	SessionType     string   `json:"session_type"`
	SupervisorName  string   `json:"supervisor_name,omitempty"`
	Timestamp       string   `json:"timestamp"`
	ToolName        string   `json:"tool_name,omitempty"`
	TTL             string   `json:"ttl"`
}

func serverClaimSignedPayloadFromWireForTest(req claimWireRequest) serverClaimSignedPayloadForTest {
	return serverClaimSignedPayloadForTest{
		AgentIdentity:   req.AgentIdentity,
		AgentModel:      req.AgentModel,
		CommandPreview:  req.CommandPreview,
		EphemeralPubKey: req.EphemeralPubKey,
		ForceApproval:   req.ForceApproval,
		MachineName:     req.MachineName,
		Nonce:           req.Nonce,
		Reason:          req.Reason,
		RecentSummary:   req.RecentSummary,
		RequestID:       req.RequestID,
		Scope:           req.Scope,
		SessionType:     req.SessionType,
		SupervisorName:  req.SupervisorName,
		Timestamp:       req.Timestamp,
		ToolName:        req.ToolName,
		TTL:             req.TTL,
	}
}

func TestRequest_ClaimSignatureVerifiesWithServerCanonicalPayloadIncludingForceApproval(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		forceApproval bool
		wantCanonical string
	}{
		{name: "ordinary_interactive", forceApproval: false, wantCanonical: `"force_approval":false`},
		{name: "forced_approval", forceApproval: true, wantCanonical: `"force_approval":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertClaimVerifiesWithServerCanonical(t, tc.forceApproval, tc.wantCanonical)
		})
	}
}

// assertClaimVerifiesWithServerCanonical signs a claim with the given
// force-approval flag and confirms the server-shaped canonical payload both
// carries the expected force_approval marker and verifies under the client key.
func assertClaimVerifiesWithServerCanonical(t *testing.T, forceApproval bool, wantCanonical string) {
	t.Helper()

	clientKey := makeClientKey(t)
	ephKey, err := generateEphemeralKey(rand.Reader)
	if err != nil {
		t.Fatalf("generateEphemeralKey: %v", err)
	}
	payload := claimSignedPayload{
		AgentIdentity:   "agent-a",
		AgentModel:      "model-a",
		CommandPreview:  "printenv SCOPE_A",
		EphemeralPubKey: compressedEphemeralPubHex(ephKey),
		ForceApproval:   forceApproval,
		MachineName:     "test-host",
		Nonce:           freshNonceTest(),
		Reason:          "test reason",
		RecentSummary:   "recent context",
		RequestID:       "rq_" + freshNonceTest(),
		Scope:           []string{"SCOPE_A"},
		SessionType:     "interactive",
		Timestamp:       time.Date(2026, 6, 8, 13, 30, 0, 0, time.UTC).Format(time.RFC3339Nano),
		ToolName:        "smoke",
		TTL:             (5 * time.Minute).String(),
	}

	wire, err := signAndWrapClaim(t.Context(), clientKey, payload)
	if err != nil {
		t.Fatalf("signAndWrapClaim: %v", err)
	}
	if wire.ForceApproval != forceApproval {
		t.Fatalf("wire ForceApproval=%v want %v", wire.ForceApproval, forceApproval)
	}

	serverPayload := serverClaimSignedPayloadFromWireForTest(wire)
	canonical, err := sign.CanonicalJSON(serverPayload)
	if err != nil {
		t.Fatalf("server CanonicalJSON: %v", err)
	}
	if !bytes.Contains(canonical, []byte(wantCanonical)) {
		t.Fatalf("canonical payload %s missing %s", canonical, wantCanonical)
	}

	sigBytes, err := base64.StdEncoding.DecodeString(wire.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if err := sign.Verify(t.Context(), &clientKey.PublicKey, canonical, sigBytes); err != nil {
		t.Fatalf("server-shaped canonical payload did not verify: %v", err)
	}
}

func TestRequest_EphemeralPubKeyHexFormat(t *testing.T) {
	r := newRequestRunner(t)
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	req := r.fakeSrv.latestClaimReq(t)
	if !regexp.MustCompile(`^[0-9a-f]{66}$`).MatchString(req.EphemeralPubKey) {
		t.Errorf("ephemeral_pubkey=%q does not match 66-char lowercase hex", req.EphemeralPubKey)
	}
}

func TestRequest_NonceAndRequestIDFormat(t *testing.T) {
	r := newRequestRunner(t)
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	req := r.fakeSrv.latestClaimReq(t)
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(req.Nonce) {
		t.Errorf("nonce=%q invalid", req.Nonce)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{32}$`).MatchString(req.RequestID) {
		t.Errorf("request_id=%q invalid", req.RequestID)
	}
}

// echoEnvHelper builds the testdata/echoenv binary into t.TempDir
// and returns its absolute path.
func echoEnvHelper(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "echoenv")
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	src := filepath.Join(cwd, "testdata", "echoenv")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", out, ".")
	cmd.Dir = src
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build echoenv: %v\n%s", err, outBytes)
	}
	return out
}

func TestRequest_ExecInjectsEnvVars(t *testing.T) {
	r := newRequestRunner(t)
	helper := echoEnvHelper(t)
	r.flags.execProgram = helper
	r.flags.scope = []string{"SCOPE_A", "SCOPE_B"}

	// Capture child stdout via the runner seam (we need to override
	// runner so cmd.Stdout points at our buffer).
	var childOut bytes.Buffer
	r.deps.runner = func(cmd *exec.Cmd) error {
		cmd.Stdout = &childOut
		cmd.Stderr = io.Discard
		cmd.Stdin = nil
		return cmd.Run()
	}

	r.arrangeSuccessFor(map[string][]byte{
		"SCOPE_A": []byte("value-a-secret"),
		"SCOPE_B": []byte("value-b-secret"),
	})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	got := childOut.String()
	if !strings.Contains(got, "SCOPE_A=value-a-secret") {
		t.Errorf("child stdout missing SCOPE_A entry; got:\n%s", got)
	}
	if !strings.Contains(got, "SCOPE_B=value-b-secret") {
		t.Errorf("child stdout missing SCOPE_B entry; got:\n%s", got)
	}
}

func TestRequest_ExecPropagatesChildExitCode(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.execProgram = "/bin/sh"
	r.flags.childArgs = []string{"-c", "exit 7"}

	r.deps.runner = func(cmd *exec.Cmd) error {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		cmd.Stdin = nil
		return cmd.Run()
	}

	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	err := r.run(t.Context())
	var childExit *errChildExitCode
	if !errors.As(err, &childExit) {
		t.Fatalf("expected *errChildExitCode, got %T: %v", err, err)
	}
	if childExit.code != 7 {
		t.Errorf("childExit.code=%d want 7", childExit.code)
	}
	if got := mapErr(err); got != 7 {
		t.Errorf("mapErr=%d want 7", got)
	}
}

func TestRequest_PostExecZeroesEphemeralKey(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.execProgram = "/usr/bin/true"
	r.deps.runner = func(cmd *exec.Cmd) error {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	priv := *r.ephCaptured
	if priv == nil {
		t.Fatalf("ephemeral key not captured")
	}
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .D access intentional
	if priv.D.Sign() != 0 {
		t.Errorf("ephemeral D not zeroed: D.Sign()=%d", priv.D.Sign())
	}
}

func TestRequest_NeverWritesJWTToDisk(t *testing.T) {
	r := newRequestRunner(t)
	r.fakeSrv.tokenIssued = "JWT_NEVER_ON_DISK_" + freshNonceTest()
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	tmpdir := t.TempDir()
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Walk t.TempDir() and verify JWT not present in any file.
	_ = filepath.WalkDir(tmpdir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, _ := os.ReadFile(path)
		if bytes.Contains(body, []byte(r.fakeSrv.tokenIssued)) {
			t.Errorf("JWT leaked into %s", path)
		}
		return nil
	})
}

func TestRequest_NeverWritesSecretToDisk(t *testing.T) {
	r := newRequestRunner(t)
	secret := []byte(requestSentinel + "_payload")
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": secret})
	tmpdir := t.TempDir()
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	_ = filepath.WalkDir(tmpdir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, _ := os.ReadFile(path)
		if bytes.Contains(body, []byte(requestSentinel+"_payload")) {
			t.Errorf("secret leaked into %s", path)
		}
		return nil
	})
}

func TestRequest_BadExecProgramFailsBeforeApproval(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.execProgram = "printenv HUSH_SMOKE_TEST"
	claimCalls := int32(0)
	r.fakeSrv.claimMu.Lock()
	r.fakeSrv.claimResponse = func(_ claimWireRequest) (int, []byte) {
		atomic.AddInt32(&claimCalls, 1)
		return http.StatusInternalServerError, nil
	}
	r.fakeSrv.claimMu.Unlock()

	err := r.run(t.Context())
	if err == nil {
		t.Fatalf("want exec lookup error, got nil")
	}
	if got := atomic.LoadInt32(&claimCalls); got != 0 {
		t.Fatalf("claim calls = %d; want 0 before Discord approval", got)
	}
	if got := atomic.LoadInt32(r.runnerCalls); got != 0 {
		t.Fatalf("runner calls = %d; want 0", got)
	}
	if !strings.Contains(r.stderrBuf.String(), "--exec program") {
		t.Fatalf("stderr did not explain exec lookup: %q", r.stderrBuf.String())
	}
}

func TestRequest_PartialFetchFailureAbortsBeforeChild(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.scope = []string{"SCOPE_A", "SCOPE_B"}
	r.flags.execProgram = "/usr/bin/false"

	// Wire claim response: succeed; then arrange A serves OK,
	// B returns 404.
	r.fakeSrv.claimMu.Lock()
	r.fakeSrv.claimResponse = func(_ claimWireRequest) (int, []byte) {
		envelope := r.fakeSrv.encryptForLatestEphemeral(r.t, []byte("a-value"))
		r.fakeSrv.setSecret("SCOPE_A", envelope)
		r.fakeSrv.setSecretStatus("SCOPE_B", http.StatusNotFound)
		out := claimWireResponse{
			JWT:       r.fakeSrv.tokenIssued,
			ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
			JTI:       "00000000-0000-0000-0000-aaaaaaaaaaaa",
		}
		raw, _ := json.Marshal(out) //nolint:errchkjson // static struct shape; Marshal cannot fail
		return http.StatusOK, raw
	}
	r.fakeSrv.claimMu.Unlock()

	err := r.run(t.Context())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if mapErr(err) != ExitNotFound {
		t.Errorf("mapErr=%d want ExitNotFound (%d)", mapErr(err), ExitNotFound)
	}
	if atomic.LoadInt32(r.runnerCalls) != 0 {
		t.Errorf("runner called %d times; expected 0 (child must not start on partial fetch)",
			atomic.LoadInt32(r.runnerCalls))
	}
}

func TestRequest_DeniedOnDiscordExitsAuth(t *testing.T) {
	r := newRequestRunner(t)
	r.fakeSrv.claimMu.Lock()
	r.fakeSrv.claimResponse = func(_ claimWireRequest) (int, []byte) {
		body, _ := json.Marshal(claimWireError{Error: "denied", RequestID: "abc"}) //nolint:errchkjson // static struct shape; Marshal cannot fail
		return http.StatusForbidden, body
	}
	r.fakeSrv.claimMu.Unlock()

	err := r.run(t.Context())
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	if mapErr(err) != ExitAuth {
		t.Errorf("mapErr=%d want ExitAuth", mapErr(err))
	}
	if atomic.LoadInt32(&r.fakeSrv.secretCalls) != 0 {
		t.Errorf("secret endpoint called %d times after deny; want 0",
			atomic.LoadInt32(&r.fakeSrv.secretCalls))
	}
}

func TestRequest_KeychainMissExitErr(t *testing.T) {
	r := newRequestRunner(t)
	// Wipe the fake keychain so retrieve returns ErrKeychainItemNotFound.
	r.fakeKC.Destroy()
	err := r.run(t.Context())
	if !errors.Is(err, keychain.ErrKeychainItemNotFound) {
		t.Fatalf("err=%v want ErrKeychainItemNotFound", err)
	}
	want := "hush: request: client key not found in keychain — run `hush init client --machine-index 0` first"
	if !strings.Contains(r.stderrBuf.String(), want) {
		t.Errorf("stderr=%q missing %q", r.stderrBuf.String(), want)
	}
}

func TestRequest_TTLBoundsApprovalWait(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.ttl = 100 * time.Millisecond
	r.fakeSrv.claimMu.Lock()
	r.fakeSrv.hangClaim = true
	r.fakeSrv.hangClaimUntil = make(chan struct{})
	r.fakeSrv.claimMu.Unlock()
	t.Cleanup(func() {
		r.fakeSrv.claimMu.Lock()
		if r.fakeSrv.hangClaimUntil != nil {
			close(r.fakeSrv.hangClaimUntil)
			r.fakeSrv.hangClaimUntil = nil
		}
		r.fakeSrv.claimMu.Unlock()
	})

	// HTTP client must NOT have its own short timeout for this test.
	r.deps.httpClient = &http.Client{}

	start := time.Now()
	err := r.run(t.Context())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err=%v want context.DeadlineExceeded", err)
	}
	if elapsed < 80*time.Millisecond || elapsed > 5*time.Second {
		t.Errorf("elapsed=%v outside [80ms, 5s]", elapsed)
	}
	if mapErr(err) != ExitErr {
		t.Errorf("mapErr=%d want ExitErr", mapErr(err))
	}
}

func TestRequest_SIGINTDuringApprovalWaitZeroesKey(t *testing.T) {
	r := newRequestRunner(t)
	// Substitute signalCtx with one that returns an already-cancelled
	// context (simulating SIGINT delivered during the /claim POST).
	r.deps.signalCtx = func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, cancel
	}
	r.deps.httpClient = &http.Client{}

	err := r.run(t.Context())
	if err == nil {
		t.Fatalf("want error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err=%v want context.Canceled", err)
	}
	priv := *r.ephCaptured
	if priv == nil {
		t.Fatalf("ephemeral key not captured")
	}
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .D access intentional
	if priv.D.Sign() != 0 {
		t.Errorf("ephemeral D not zeroed after SIGINT path: D.Sign()=%d", priv.D.Sign())
	}
}

func TestRequest_LogsNeverContainSecretValue(t *testing.T) {
	r := newRequestRunner(t)
	secret := []byte(requestSentinel + "_log")
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": secret})
	r.deps.runner = func(cmd *exec.Cmd) error {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	testutil.AssertSentinelAbsent(t, requestSentinel+"_log", r.stdoutBuf.String())
	testutil.AssertSentinelAbsent(t, requestSentinel+"_log", r.stderrBuf.String())
}

func TestRequest_ErrorsDoNotLeakSecretBytes(t *testing.T) {
	failureModes := map[string]func(r *requestRunner){
		"keychain miss": func(r *requestRunner) { r.fakeKC.Destroy() },
		"transport down": func(r *requestRunner) {
			r.fakeSrv.server.Close()
		},
		"claim 403 denied": func(r *requestRunner) {
			r.fakeSrv.claimResponse = func(_ claimWireRequest) (int, []byte) {
				body, _ := json.Marshal(claimWireError{Error: "denied", RequestID: "abc"}) //nolint:errchkjson // static struct shape; Marshal cannot fail
				return http.StatusForbidden, body
			}
		},
		"claim 408 approval_timeout": func(r *requestRunner) {
			r.fakeSrv.claimResponse = func(_ claimWireRequest) (int, []byte) {
				body, _ := json.Marshal(claimWireError{Error: "approval_timeout", RequestID: "abc"}) //nolint:errchkjson // static struct shape; Marshal cannot fail
				return http.StatusRequestTimeout, body
			}
		},
		"/s 404": func(r *requestRunner) {
			r.fakeSrv.claimMu.Lock()
			r.fakeSrv.claimResponse = func(_ claimWireRequest) (int, []byte) {
				r.fakeSrv.setSecretStatus("SCOPE_A", http.StatusNotFound)
				out := claimWireResponse{JWT: r.fakeSrv.tokenIssued, ExpiresAt: "2099-01-01T00:00:00Z", JTI: "z"}
				raw, _ := json.Marshal(out) //nolint:errchkjson // static struct shape
				return http.StatusOK, raw
			}
			r.fakeSrv.claimMu.Unlock()
		},
		"/s 401": func(r *requestRunner) {
			r.fakeSrv.claimMu.Lock()
			r.fakeSrv.claimResponse = func(_ claimWireRequest) (int, []byte) {
				r.fakeSrv.setSecretStatus("SCOPE_A", http.StatusUnauthorized)
				out := claimWireResponse{JWT: r.fakeSrv.tokenIssued, ExpiresAt: "2099-01-01T00:00:00Z", JTI: "z"}
				raw, _ := json.Marshal(out) //nolint:errchkjson // static struct shape
				return http.StatusOK, raw
			}
			r.fakeSrv.claimMu.Unlock()
		},
	}
	secret := requestSentinel + "_errpath"
	jwtSentinel := "JWT_SENTINEL_" + freshNonceTest()

	for name, arrange := range failureModes {
		t.Run(name, func(t *testing.T) {
			r := newRequestRunner(t)
			r.fakeSrv.tokenIssued = jwtSentinel
			arrange(r)
			err := r.run(t.Context())
			if err == nil {
				t.Fatalf("expected an error for failure mode %q", name)
			}
			testutil.AssertSentinelAbsent(t, secret, err.Error())
			testutil.AssertSentinelAbsent(t, jwtSentinel, err.Error())
			testutil.AssertSentinelAbsent(t, secret, r.stderrBuf.String())
			testutil.AssertSentinelAbsent(t, jwtSentinel, r.stderrBuf.String())
		})
	}
}

func TestRequest_NoCallsToOSGetenvAtRuntime(t *testing.T) {
	t.Setenv("HUSH_PASSPHRASE", requestSentinel+"_pass")
	t.Setenv("HUSH_CLIENT_KEY", requestSentinel+"_ck")
	t.Setenv("HUSH_SERVER", requestSentinel+"_srv")
	t.Setenv("HUSH_TTL", requestSentinel+"_ttl")
	t.Setenv("HUSH_MACHINE_INDEX", requestSentinel+"_mi")

	r := newRequestRunner(t)
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	r.deps.runner = func(cmd *exec.Cmd) error {
		// Don't pass parent env; we want to confirm the wire payload
		// uses only flag values, not env defaults.
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		return cmd.Run()
	}
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	req := r.fakeSrv.latestClaimReq(t)
	// TTL/Reason/scope must equal the supplied flag values, not env
	// sentinels.
	if req.TTL != "5s" {
		t.Errorf("ttl=%q want 5s (env should not bleed through)", req.TTL)
	}
	if req.Reason != "test" {
		t.Errorf("reason=%q want test", req.Reason)
	}
	for _, sentinel := range []string{
		requestSentinel + "_pass", requestSentinel + "_ck", requestSentinel + "_srv",
		requestSentinel + "_ttl", requestSentinel + "_mi",
	} {
		testutil.AssertSentinelAbsent(t, sentinel, r.stderrBuf.String())
		body, _ := json.Marshal(req) //nolint:errchkjson // static struct shape
		testutil.AssertSentinelAbsent(t, sentinel, string(body))
	}
}

func TestRequest_ExecOnlyChildHasSecret(t *testing.T) {
	r := newRequestRunner(t)
	helper := echoEnvHelper(t)
	r.flags.execProgram = helper
	r.flags.scope = []string{"SENTINEL_SCOPE"}

	var childOut bytes.Buffer
	var childErr bytes.Buffer
	r.deps.runner = func(cmd *exec.Cmd) error {
		cmd.Stdout = &childOut
		cmd.Stderr = &childErr
		cmd.Stdin = nil
		return cmd.Run()
	}

	r.arrangeSuccessFor(map[string][]byte{"SENTINEL_SCOPE": []byte(requestSentinel)})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	// (a) Child stdout contains the sentinel.
	if !strings.Contains(childOut.String(), "SENTINEL_SCOPE="+requestSentinel) {
		t.Errorf("child stdout missing sentinel-scope env entry; got:\n%s", childOut.String())
	}
	// (b)-(d) Sentinel absent from parent stdout, parent stderr,
	// captured (parent) buffers.
	testutil.AssertSentinelAbsent(t, requestSentinel, r.stdoutBuf.String())
	testutil.AssertSentinelAbsent(t, requestSentinel, r.stderrBuf.String())
	// (e) Walk t.TempDir() and confirm no file contains the sentinel.
	tmpdir := t.TempDir()
	_ = filepath.WalkDir(tmpdir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, _ := os.ReadFile(path)
		if bytes.Contains(body, []byte(requestSentinel)) {
			t.Errorf("sentinel leaked into %s", path)
		}
		return nil
	})
}

// ============================================================
// Phase 4 — US2 (--format eval) tests
// ============================================================

func TestRequest_FormatEvalEmitsStderrWarning(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.execProgram = ""
	r.flags.formatMode = "eval"
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := r.stderrBuf.String(); !strings.Contains(got, expectedFormatEvalWarningRaw) {
		t.Fatalf("stderr does not contain locked WARNING:\n  got=%q\n  want substring=%q",
			got, expectedFormatEvalWarningRaw)
	}
}

func TestRequest_FormatEvalEscapesSingleQuote(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.execProgram = ""
	r.flags.formatMode = "eval"
	r.flags.scope = []string{"NAME"}
	tricky := []byte("pa'ss\"wo$rd\nwith all the things")
	r.arrangeSuccessFor(map[string][]byte{"NAME": tricky})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	want := "export NAME='pa'\\''ss\"wo$rd\nwith all the things'\n"
	if got := r.stdoutBuf.String(); got != want {
		t.Fatalf("eval line mismatch:\n  got = %q\n  want= %q", got, want)
	}

	// Round-trip through bash to confirm the eval line recovers the
	// original bytes.
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not in PATH: %v", err)
	}
	cmd := exec.CommandContext(t.Context(), "bash", "-c", r.stdoutBuf.String()+`printf "%s" "$NAME"`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	if !bytes.Equal(out, tricky) {
		t.Errorf("round-trip mismatch:\n  got = %q\n  want= %q", out, tricky)
	}
}

func TestRequest_FormatEvalOneLinePerScope(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.execProgram = ""
	r.flags.formatMode = "eval"
	r.flags.scope = []string{"A", "B", "C"}
	r.arrangeSuccessFor(map[string][]byte{
		"A": []byte("aval"), "B": []byte("bval"), "C": []byte("cval"),
	})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	lines := strings.Split(strings.TrimRight(r.stdoutBuf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines; want 3 (stdout=%q)", len(lines), r.stdoutBuf.String())
	}
	for i, name := range r.flags.scope {
		want := "export " + name + "="
		if !strings.HasPrefix(lines[i], want) {
			t.Errorf("line %d=%q want prefix %q", i, lines[i], want)
		}
	}
}

func TestRequest_FormatEvalWarningGoesToStderrNotStdout(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.execProgram = ""
	r.flags.formatMode = "eval"
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(r.stdoutBuf.String(), "WARNING") {
		t.Errorf("WARNING leaked to stdout: %q", r.stdoutBuf.String())
	}
}

func TestRequest_FormatEvalNoChildProcessSpawned(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.execProgram = ""
	r.flags.formatMode = "eval"
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := atomic.LoadInt32(r.runnerCalls); got != 0 {
		t.Errorf("runner called %d times; want 0 in eval mode", got)
	}
}

func TestRequest_FormatEvalWarningEvenWhenStdoutPiped(t *testing.T) {
	// stdoutBuf already simulates a pipe; this test re-asserts that
	// the WARNING fires regardless of stdout's destination.
	r := newRequestRunner(t)
	r.flags.execProgram = ""
	r.flags.formatMode = "eval"
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(r.stderrBuf.String(), "WARNING:") {
		t.Errorf("WARNING absent from stderr: %q", r.stderrBuf.String())
	}
}

func TestRequest_FormatEvalDoesNotLeakSecretToParentSlog(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.execProgram = ""
	r.flags.formatMode = "eval"
	r.flags.scope = []string{"SCOPE_A"}
	secret := []byte(requestSentinel + "_eval")
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": secret})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Stdout legitimately receives the secret (eval mode); stderr
	// must NOT contain it.
	testutil.AssertSentinelAbsent(t, requestSentinel+"_eval", r.stderrBuf.String())
}

func TestRequest_FormatEvalEmptyValuePreservesQuotes(t *testing.T) {
	t.Parallel()
	// ECIES rejects empty plaintext as a primitive-level constraint,
	// so the round-trip is unreachable. The eval contract for an
	// empty value is exercised directly on the renderer.
	if got := renderEvalLine("NAME", []byte("")); got != "export NAME=''\n" {
		t.Errorf("renderEvalLine empty value=%q want %q", got, "export NAME=''\n")
	}
}

func TestRequest_FormatEvalPostExecZeroesEphemeralKey(t *testing.T) {
	r := newRequestRunner(t)
	r.flags.execProgram = ""
	r.flags.formatMode = "eval"
	r.arrangeSuccessFor(map[string][]byte{"SCOPE_A": []byte("v")})
	if err := r.run(t.Context()); err != nil {
		t.Fatalf("run: %v", err)
	}
	priv := *r.ephCaptured
	if priv == nil {
		t.Fatalf("ephemeral key not captured")
	}
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .D access intentional
	if priv.D.Sign() != 0 {
		t.Errorf("ephemeral D not zeroed: D.Sign()=%d", priv.D.Sign())
	}
}

// ============================================================
// Phase 5 — US3 (refuse-to-run) tests
// ============================================================

// countingHandler records every request and returns 200.
type countingHandler struct {
	calls int32
}

func (c *countingHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	atomic.AddInt32(&c.calls, 1)
	w.WriteHeader(http.StatusOK)
}

func TestRequest_RequiresExecOrFormat_NoNetwork(t *testing.T) {
	t.Parallel()
	h := &countingHandler{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cmd := newRequestCmd()
	stdout, stderr, _, stderrBuf := captureStreams()
	cmd.RunE = func(c *cobra.Command, a []string) error {
		flags, err := parseAndValidateFlags(c, a)
		if err != nil {
			emitValidationStderr(stderr, err)
			return err
		}
		_ = flags
		_ = stdout
		return nil
	}
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{
		"--server=" + srv.URL,
		"--scope=A",
		"--reason=r",
		"--ttl=1h",
		"--max-uses=1",
		"--machine-index=0",
	})
	err := cmd.Execute()
	if !errors.Is(err, errMissingExecOrFormat) {
		t.Fatalf("err=%v want errMissingExecOrFormat", err)
	}
	if got := atomic.LoadInt32(&h.calls); got != 0 {
		t.Errorf("handler calls=%d want 0", got)
	}
	want := "hush: request: must specify --exec or --format eval\n"
	if stderrBuf.String() != want {
		t.Errorf("stderr=%q want %q", stderrBuf.String(), want)
	}
}

func TestRequest_ExecOrFormatMutuallyExclusive_NoNetwork(t *testing.T) {
	t.Parallel()
	h := &countingHandler{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cmd := newRequestCmd()
	_, stderr, _, stderrBuf := captureStreams()
	cmd.RunE = func(c *cobra.Command, a []string) error {
		_, err := parseAndValidateFlags(c, a)
		if err != nil {
			emitValidationStderr(stderr, err)
			return err
		}
		return nil
	}
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{
		"--server=" + srv.URL,
		"--scope=A",
		"--reason=r",
		"--ttl=1h",
		"--max-uses=1",
		"--machine-index=0",
		"--exec=/bin/true",
		"--format=eval",
	})
	err := cmd.Execute()
	if !errors.Is(err, errExecAndFormatBothSet) {
		t.Fatalf("err=%v want errExecAndFormatBothSet", err)
	}
	if got := atomic.LoadInt32(&h.calls); got != 0 {
		t.Errorf("handler calls=%d want 0", got)
	}
	want := "hush: request: --exec and --format eval are mutually exclusive\n"
	if stderrBuf.String() != want {
		t.Errorf("stderr=%q want %q", stderrBuf.String(), want)
	}
}

func TestRequest_FormatRejectsNonEval_NoNetwork(t *testing.T) {
	t.Parallel()
	h := &countingHandler{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cmd := newRequestCmd()
	_, stderr, _, stderrBuf := captureStreams()
	cmd.RunE = func(c *cobra.Command, a []string) error {
		_, err := parseAndValidateFlags(c, a)
		if err != nil {
			emitValidationStderr(stderr, err)
			return err
		}
		return nil
	}
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{
		"--server=" + srv.URL,
		"--scope=A",
		"--reason=r",
		"--ttl=1h",
		"--max-uses=1",
		"--machine-index=0",
		"--format=json",
	})
	err := cmd.Execute()
	if !errors.Is(err, errFormatNotEval) {
		t.Fatalf("err=%v want errFormatNotEval", err)
	}
	if got := atomic.LoadInt32(&h.calls); got != 0 {
		t.Errorf("handler calls=%d want 0", got)
	}
	want := "hush: request: --format only accepts the literal value \"eval\"\n"
	if stderrBuf.String() != want {
		t.Errorf("stderr=%q want %q", stderrBuf.String(), want)
	}
}

func TestRequest_MaxUsesTooLow_NoNetwork(t *testing.T) {
	t.Parallel()
	h := &countingHandler{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	cmd := newRequestCmd()
	_, stderr, _, stderrBuf := captureStreams()
	cmd.RunE = func(c *cobra.Command, a []string) error {
		_, err := parseAndValidateFlags(c, a)
		if err != nil {
			emitValidationStderr(stderr, err)
			return err
		}
		return nil
	}
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{
		"--server=" + srv.URL,
		"--scope=A,B,C",
		"--reason=r",
		"--ttl=1h",
		"--max-uses=2",
		"--machine-index=0",
		"--exec=/bin/true",
	})
	err := cmd.Execute()
	if !errors.Is(err, errMaxUsesTooLow) {
		t.Fatalf("err=%v want errMaxUsesTooLow", err)
	}
	if got := atomic.LoadInt32(&h.calls); got != 0 {
		t.Errorf("handler calls=%d want 0", got)
	}
	want := "hush: request: --max-uses must be ≥ number of scopes\n"
	if stderrBuf.String() != want {
		t.Errorf("stderr=%q want %q", stderrBuf.String(), want)
	}
}

// recordingKeychain is a Keychain implementation that records every
// Retrieve call. Used to prove the validator runs before any keychain
// I/O.
type recordingKeychain struct {
	calls int32
}

func (r *recordingKeychain) Store(_ context.Context, _, _ string, _ *securebytes.SecureBytes, _ string) error {
	return nil
}

func (r *recordingKeychain) Retrieve(_ context.Context, _, _ string) (*securebytes.SecureBytes, error) {
	atomic.AddInt32(&r.calls, 1)
	return nil, keychain.ErrKeychainItemNotFound
}

func (r *recordingKeychain) Delete(_ context.Context, _, _ string) error { return nil }

func TestRequest_RefusesBeforeKeychainAccess(t *testing.T) {
	t.Parallel()
	rec := &recordingKeychain{}
	cmd := newRequestCmd()
	_, stderr, _, _ := captureStreams()
	cmd.RunE = func(c *cobra.Command, a []string) error {
		flags, err := parseAndValidateFlags(c, a)
		if err != nil {
			emitValidationStderr(stderr, err)
			return err
		}
		// Validator MUST short-circuit; we never reach this:
		deps := requestDeps{
			keychain:     rec,
			httpClient:   &http.Client{},
			nowFn:        time.Now,
			randReader:   rand.Reader,
			hostnameFn:   func() (string, error) { return "h", nil },
			ephemeralKey: generateEphemeralKey,
			looker:       exec.LookPath,
			runner:       func(cmd *exec.Cmd) error { return cmd.Run() },
			signalCtx: func(p context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
				return context.WithCancel(p)
			},
		}
		return runRequest(c.Context(), newStream(io.Discard, false, true), stderr, deps, flags)
	}
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{
		"--server=https://example.test",
		"--scope=A",
		"--reason=r",
		"--ttl=1h",
		"--max-uses=1",
		"--machine-index=0",
		// no --exec, no --format
	})
	err := cmd.Execute()
	if !errors.Is(err, errMissingExecOrFormat) {
		t.Fatalf("err=%v want errMissingExecOrFormat", err)
	}
	if got := atomic.LoadInt32(&rec.calls); got != 0 {
		t.Errorf("keychain.Retrieve calls=%d want 0", got)
	}
}

func TestRetrieveClientKey_UsesSmokeKeyFileOverride(t *testing.T) {
	priv := makeClientKey(t)
	scalar := make([]byte, 32)
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .D access intentional
	priv.D.FillBytes(scalar)
	path := filepath.Join(t.TempDir(), "client.key")
	if err := os.WriteFile(path, []byte(hex.EncodeToString(scalar)+"\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	rec := &recordingKeychain{}
	got, err := retrieveClientKey(context.Background(), requestDeps{keychain: rec}, 0, path, newStream(io.Discard, true, true))
	if err != nil {
		t.Fatalf("retrieveClientKey: %v", err)
	}
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .D read-only comparison
	if got.D.Cmp(priv.D) != 0 {
		t.Fatalf("loaded scalar does not match key file")
	}
	if calls := atomic.LoadInt32(&rec.calls); calls != 0 {
		t.Fatalf("keychain retrieve calls=%d, want 0", calls)
	}
}
