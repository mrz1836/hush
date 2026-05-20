//go:build integration

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
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// integrationFakeServer is a more realistic in-process server that
// verifies the client signature, accepts an approval decision from a
// stub Approver, encrypts each requested secret with ECIES under the
// supplied ephemeral pubkey, and serves them via /s. It exists so the
// integration test exercises the full canonical-JSON + signature +
// ECIES pipeline that production uses, without dragging in the
// chassis (which would couple this test to far more code).
type integrationFakeServer struct {
	t          *testing.T
	server     *httptest.Server
	clientPub  *ecdsa.PublicKey
	stub       *testutil.DiscordStub
	mu         sync.Mutex
	jwtIssued  string
	secrets    map[string][]byte // plaintext per scope
	ephPubHex  string
	scopeOrder []string
}

func newIntegrationServer(t *testing.T, clientPub *ecdsa.PublicKey, secrets map[string][]byte) *integrationFakeServer {
	t.Helper()
	s := &integrationFakeServer{
		t:         t,
		clientPub: clientPub,
		stub:      testutil.NewDiscordStub(t),
		secrets:   secrets,
		jwtIssued: "integration-jwt-" + freshNonceTest(),
	}
	s.stub.ApproveAll = true
	mux := http.NewServeMux()
	mux.HandleFunc("/claim", s.handleClaim)
	mux.HandleFunc("/s/", s.handleSecret)
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *integrationFakeServer) URL() string { return s.server.URL }

func (s *integrationFakeServer) handleClaim(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64*1024))
	var req claimWireRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// Verify signature.
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
		http.Error(w, "canonical", http.StatusInternalServerError)
		return
	}
	sigBytes, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil {
		http.Error(w, "sig decode", http.StatusBadRequest)
		return
	}
	if err := sign.Verify(r.Context(), s.clientPub, canonical, sigBytes); err != nil {
		http.Error(w, "bad signature", http.StatusForbidden)
		return
	}

	// Driving the DiscordStub: ApproveAll yields DecisionApprove.
	stubReq := testutil.ApprovalRequest{
		RequesterHost: req.MachineName,
		Scopes:        req.Scope,
		SessionType:   req.SessionType,
	}
	dec, derr := s.stub.RequestApproval(r.Context(), stubReq)
	if derr != nil {
		body, _ := json.Marshal(claimWireError{Error: "denied", RequestID: req.RequestID})
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(body)
		return
	}
	if dec == testutil.DecisionDeny {
		body, _ := json.Marshal(claimWireError{Error: "denied", RequestID: req.RequestID})
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write(body)
		return
	}

	// Capture the ephemeral pub for later /s encryption.
	s.mu.Lock()
	s.ephPubHex = req.EphemeralPubKey
	s.scopeOrder = append([]string(nil), req.Scope...)
	s.mu.Unlock()

	out := claimWireResponse{
		JWT:       s.jwtIssued,
		ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
		JTI:       fmt.Sprintf("00000000-0000-0000-0000-%012d", time.Now().UnixNano()%1e12),
	}
	raw, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func (s *integrationFakeServer) handleSecret(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	s.mu.Lock()
	want := s.jwtIssued
	ephHex := s.ephPubHex
	s.mu.Unlock()
	if token != want {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	name := strings.TrimPrefix(r.URL.Path, "/s/")
	plaintext, ok := s.secrets[name]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	pubBytes, err := hex.DecodeString(ephHex)
	if err != nil {
		http.Error(w, "bad eph", http.StatusInternalServerError)
		return
	}
	pub, err := secp256k1.ParsePubKey(pubBytes)
	if err != nil {
		http.Error(w, "parse eph", http.StatusInternalServerError)
		return
	}
	envelope, err := ecies.Encrypt(r.Context(), pub.ToECDSA(), plaintext)
	if err != nil {
		http.Error(w, "enc", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(envelope)
}

// makeIntegrationDeps builds a requestDeps wired to the integration
// server.
func makeIntegrationDeps(t *testing.T, fake *keychain.FakeKeychain, capturedKey **ecdsa.PrivateKey, runnerCalls *int32) requestDeps {
	t.Helper()
	return requestDeps{
		keychain:   fake,
		httpClient: &http.Client{Timeout: 10 * time.Second},
		nowFn:      time.Now,
		randReader: rand.Reader,
		hostnameFn: func() (string, error) { return "integration-host", nil },
		ephemeralKey: func(r io.Reader) (*ecdsa.PrivateKey, error) {
			k, err := generateEphemeralKey(r)
			if err != nil {
				return nil, err
			}
			*capturedKey = k
			return k, nil
		},
		looker: exec.LookPath,
		runner: func(cmd *exec.Cmd) error {
			atomic.AddInt32(runnerCalls, 1)
			return cmd.Run()
		},
		signalCtx: func(parent context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return context.WithCancel(parent)
		},
	}
}

// TestRequest_FullFlowWithDiscordStubApproveAll exercises the
// end-to-end --exec path: client signs, server verifies, DiscordStub
// approves, ECIES round-trip delivers two scopes, child receives env
// entries, parent leaks no secret.
func TestRequest_FullFlowWithDiscordStubApproveAll(t *testing.T) {
	clientKey := makeClientKey(t)
	fake := keychain.NewFake()
	storeClientKeyInFake(t, fake, clientKey, 0)

	const sentinelValue = "SECRET_SHOULD_NEVER_APPEAR_16"
	const otherValue = "OTHER-VAL-1234"
	srv := newIntegrationServer(t, &clientKey.PublicKey, map[string][]byte{
		"SENTINEL_SCOPE": []byte(sentinelValue),
		"OTHER_SCOPE":    []byte(otherValue),
	})

	helper := echoEnvHelper(t)

	var captured *ecdsa.PrivateKey
	var runnerCalls int32
	deps := makeIntegrationDeps(t, fake, &captured, &runnerCalls)

	var childOut bytes.Buffer
	deps.runner = func(cmd *exec.Cmd) error {
		atomic.AddInt32(&runnerCalls, 1)
		cmd.Stdout = &childOut
		cmd.Stderr = io.Discard
		cmd.Stdin = nil
		return cmd.Run()
	}

	flags := requestFlags{
		server:       srv.URL(),
		scope:        []string{"SENTINEL_SCOPE", "OTHER_SCOPE"},
		reason:       "integration test",
		ttl:          5 * time.Second,
		maxUses:      10,
		machineIndex: 0,
		execProgram:  helper,
	}

	stdout, stderr, soBuf, seBuf := captureStreams()
	start := time.Now()
	err := runRequest(t.Context(), stdout, stderr, deps, flags)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("runRequest: %v; stderr=%q", err, seBuf.String())
	}
	if elapsed > 5*time.Second {
		t.Errorf("elapsed=%v exceeded 5s budget", elapsed)
	}

	got := childOut.String()
	if !strings.Contains(got, "SENTINEL_SCOPE="+sentinelValue) {
		t.Errorf("child stdout missing SENTINEL_SCOPE=%s; got:\n%s", sentinelValue, got)
	}
	if !strings.Contains(got, "OTHER_SCOPE="+otherValue) {
		t.Errorf("child stdout missing OTHER_SCOPE=%s; got:\n%s", otherValue, got)
	}

	// Parent stdout/stderr leak-free.
	testutil.AssertSentinelAbsent(t, sentinelValue, soBuf.String())
	testutil.AssertSentinelAbsent(t, sentinelValue, seBuf.String())

	// JWT leak-free in tempdir.
	tmpdir := t.TempDir()
	_ = filepath.Walk(tmpdir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		body, _ := os.ReadFile(path)
		if bytes.Contains(body, []byte(srv.jwtIssued)) {
			t.Errorf("JWT leaked into %s", path)
		}
		if bytes.Contains(body, []byte(sentinelValue)) {
			t.Errorf("secret leaked into %s", path)
		}
		return nil
	})

	// Ephemeral key zeroed.
	if captured == nil {
		t.Fatalf("ephemeral key not captured")
	}
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .D access intentional
	if captured.D.Sign() != 0 {
		t.Errorf("ephemeral D not zeroed")
	}

	// At least one approval call happened.
	if got := len(srv.stub.Calls()); got != 1 {
		t.Errorf("DiscordStub got %d approval calls, want 1", got)
	}
}

// TestRequest_FullFlowFormatEvalIntegration exercises the eval path
// end-to-end and asserts the bash round-trip recovers each secret.
func TestRequest_FullFlowFormatEvalIntegration(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not in PATH: %v", err)
	}
	clientKey := makeClientKey(t)
	fake := keychain.NewFake()
	storeClientKeyInFake(t, fake, clientKey, 0)

	tricky := "abc'def\"$ghi"
	srv := newIntegrationServer(t, &clientKey.PublicKey, map[string][]byte{
		"NAME_A": []byte("simple-a"),
		"NAME_B": []byte(tricky),
	})

	var captured *ecdsa.PrivateKey
	var runnerCalls int32
	deps := makeIntegrationDeps(t, fake, &captured, &runnerCalls)

	flags := requestFlags{
		server:       srv.URL(),
		scope:        []string{"NAME_A", "NAME_B"},
		reason:       "eval integration",
		ttl:          5 * time.Second,
		maxUses:      10,
		machineIndex: 0,
		formatMode:   "eval",
	}

	stdout, stderr, soBuf, seBuf := captureStreams()
	if err := runRequest(t.Context(), stdout, stderr, deps, flags); err != nil {
		t.Fatalf("runRequest: %v; stderr=%q", err, seBuf.String())
	}

	if !strings.Contains(seBuf.String(), expectedFormatEvalWarningRaw) {
		t.Errorf("WARNING missing from stderr: %q", seBuf.String())
	}

	// Bash round-trip.
	cmd := exec.Command("bash", "-c", soBuf.String()+`printf "%s\n%s" "$NAME_A" "$NAME_B"`)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	want := "simple-a\n" + tricky
	if string(out) != want {
		t.Errorf("bash recovery mismatch:\n  got = %q\n  want= %q", string(out), want)
	}

	if got := atomic.LoadInt32(&runnerCalls); got != 0 {
		t.Errorf("runner called %d times in eval mode; want 0", got)
	}

	// JWT not on disk.
	tmpdir := t.TempDir()
	_ = filepath.Walk(tmpdir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		body, _ := os.ReadFile(path)
		if bytes.Contains(body, []byte(srv.jwtIssued)) {
			t.Errorf("JWT leaked into %s", path)
		}
		return nil
	})

	if captured == nil {
		t.Fatalf("ephemeral key not captured")
	}
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .D access intentional
	if captured.D.Sign() != 0 {
		t.Errorf("ephemeral D not zeroed")
	}

	// Sanity: at least one stub approval call.
	if got := len(srv.stub.Calls()); got == 0 {
		t.Errorf("DiscordStub never called")
	}
}

// guard against unused imports if a test branch above is removed.
var _ = errors.Is
