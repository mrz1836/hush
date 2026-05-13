package supervise

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/internal/transport/ecies"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// recordedAlert captures one Alerts.Emit invocation.
type recordedAlert struct {
	class   AlertClass
	payload AlertPayload
}

// recordingAlerts captures every Emit call for assertion.
type recordingAlerts struct {
	mu     sync.Mutex
	events []recordedAlert
}

// Emit records (class, payload) under the mutex.
func (r *recordingAlerts) Emit(_ context.Context, class AlertClass, p AlertPayload) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedAlert{class, p})
}

// Events returns a defensive copy of recorded events.
func (r *recordingAlerts) Events() []recordedAlert {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedAlert, len(r.events))
	copy(out, r.events)
	return out
}

// CountClass returns how many times class was emitted.
func (r *recordingAlerts) CountClass(class AlertClass) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.class == class {
			n++
		}
	}
	return n
}

// controllableValidator returns the registered error for each scope.
type controllableValidator struct {
	failures map[string]error
}

// Validate returns the configured error or nil.
func (c *controllableValidator) Validate(_ context.Context, scope string, _ *securebytes.SecureBytes) error {
	if err, ok := c.failures[scope]; ok {
		return err
	}
	return nil
}

// recordingWatchdog captures every OnStderrLine call.
type recordingWatchdog struct {
	mu    sync.Mutex
	lines [][]byte
}

// OnStderrLine appends a copy of line.
func (r *recordingWatchdog) OnStderrLine(_ context.Context, line []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]byte, len(line))
	copy(cp, line)
	r.lines = append(r.lines, cp)
}

// Lines returns a defensive copy of recorded lines.
func (r *recordingWatchdog) Lines() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]byte, len(r.lines))
	copy(out, r.lines)
	return out
}

// recordingAudit captures every Append call for assertion in tests.
type recordingAudit struct {
	mu     sync.Mutex
	events []recordedAuditEvent
}

type recordedAuditEvent struct {
	action string
	data   map[string]any
}

// Append records one event. Always returns nil.
func (r *recordingAudit) Append(_ context.Context, action string, data map[string]any) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, recordedAuditEvent{action: action, data: data})
	return nil
}

// Run is a no-op for the recording writer.
func (r *recordingAudit) Run(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Has returns true when action appears at least once.
func (r *recordingAudit) Has(action string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if e.action == action {
			return true
		}
	}
	return false
}

// Actions returns a defensive copy of recorded action strings.
func (r *recordingAudit) Actions() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	for i, e := range r.events {
		out[i] = e.action
	}
	return out
}

// Compile-time guard: recordingAudit implements audit.Writer.
var _ audit.Writer = (*recordingAudit)(nil)

// testECDSAKey returns a fresh secp256k1 ECDSA private key.
func testECDSAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	curve := secp256k1.S256() //nolint:staticcheck // secp256k1 not in crypto/ecdh
	priv, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}
	return priv
}

// mockVaultBehaviour holds the per-test programmable state.
type mockVaultBehaviour struct {
	hzStatus   atomic.Int32      // 0 → 200; non-zero → that status
	claimQueue chan claimOutcome // FIFO of claim outcomes; consumed per request
	mu         sync.Mutex
	scopeValue map[string][]byte // plaintext per scope; encrypted per request
	scopeFail  map[string]int    // per-scope failure status (>0 → return it)
}

// claimOutcome describes one /claim response.
type claimOutcome struct {
	status int
	body   string // raw body when set; otherwise built from JWT
	jwt    string
	jti    string
	exp    time.Time
}

// mockVault is a programmable httptest-backed vault stub.
type mockVault struct {
	t          *testing.T
	srv        *httptest.Server
	ephPub     *ecdsa.PublicKey // orchestrator's DecryptKey public side
	claimCount atomic.Int32
	behaviour  *mockVaultBehaviour
}

// newMockVault constructs a mock vault stub bound to t.
func newMockVault(t *testing.T, ephPub *ecdsa.PublicKey) *mockVault {
	t.Helper()
	v := &mockVault{
		t:      t,
		ephPub: ephPub,
		behaviour: &mockVaultBehaviour{
			claimQueue: make(chan claimOutcome, 16),
			scopeValue: map[string][]byte{},
			scopeFail:  map[string]int{},
		},
	}
	v.srv = httptest.NewServer(http.HandlerFunc(v.handle))
	t.Cleanup(v.srv.Close)
	return v
}

// URL returns the test server URL.
func (m *mockVault) URL() string { return m.srv.URL }

// Client returns the test server's HTTP client.
func (m *mockVault) Client() *http.Client { return m.srv.Client() }

// ClaimCount returns the number of /claim calls served.
func (m *mockVault) ClaimCount() int { return int(m.claimCount.Load()) }

// SetHzStatus sets the /hz response status. 0 means 200.
func (m *mockVault) SetHzStatus(s int) { m.behaviour.hzStatus.Store(int32(s)) }

// SetScope plaintext for a given scope; encrypted per call.
func (m *mockVault) SetScope(name string, value []byte) {
	cp := make([]byte, len(value))
	copy(cp, value)
	m.behaviour.mu.Lock()
	m.behaviour.scopeValue[name] = cp
	m.behaviour.mu.Unlock()
}

// QueueClaim appends one outcome for the next /claim request.
func (m *mockVault) QueueClaim(o claimOutcome) {
	m.behaviour.claimQueue <- o
}

// QueueOK queues a default 200 claim with a generated JWT/JTI.
func (m *mockVault) QueueOK() {
	m.QueueClaim(claimOutcome{
		status: http.StatusOK,
		jwt:    "test-jwt-" + randomToken(8),
		jti:    "test-jti-" + randomToken(8),
		exp:    time.Now().Add(24 * time.Hour),
	})
}

// QueueDiscordUnavailable queues a 503 with the discord_unavailable code.
func (m *mockVault) QueueDiscordUnavailable() {
	m.QueueClaim(claimOutcome{
		status: http.StatusServiceUnavailable,
		body:   `{"error":"discord_unavailable","request_id":"test"}`,
	})
}

// QueueDenied queues a 403 denied response (terminal).
func (m *mockVault) QueueDenied() {
	m.QueueClaim(claimOutcome{
		status: http.StatusForbidden,
		body:   `{"error":"denied","request_id":"test"}`,
	})
}

// FailScopeUnauthorizedJTI configures /s/{scope} to return 401 unknown_jti.
func (m *mockVault) FailScopeUnauthorizedJTI(scope string) {
	m.behaviour.mu.Lock()
	m.behaviour.scopeFail[scope] = http.StatusUnauthorized
	m.behaviour.mu.Unlock()
}

// FailScopeStatus configures /s/{scope} to return any HTTP status.
func (m *mockVault) FailScopeStatus(scope string, status int) {
	m.behaviour.mu.Lock()
	m.behaviour.scopeFail[scope] = status
	m.behaviour.mu.Unlock()
}

// ResetScopeFailure clears any per-scope failure programming.
func (m *mockVault) ResetScopeFailure() {
	m.behaviour.mu.Lock()
	for k := range m.behaviour.scopeFail {
		delete(m.behaviour.scopeFail, k)
	}
	m.behaviour.mu.Unlock()
}

// handle is the http.Handler for every test endpoint.
func (m *mockVault) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/hz" && r.Method == http.MethodGet:
		if s := m.behaviour.hzStatus.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/claim" && r.Method == http.MethodPost:
		m.handleClaim(w, r)
	case len(r.URL.Path) > len("/s/") && r.URL.Path[:3] == "/s/":
		m.handleSecret(w, r)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// handleClaim consumes the next queued outcome.
func (m *mockVault) handleClaim(w http.ResponseWriter, _ *http.Request) {
	m.claimCount.Add(1)
	var o claimOutcome
	select {
	case o = <-m.behaviour.claimQueue:
	default:
		// Default — synthesize a successful claim.
		o = claimOutcome{
			status: http.StatusOK,
			jwt:    "default-jwt-" + randomToken(8),
			jti:    "default-jti-" + randomToken(8),
			exp:    time.Now().Add(24 * time.Hour),
		}
	}
	if o.body != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(o.status)
		_, _ = w.Write([]byte(o.body))
		return
	}
	if o.status != http.StatusOK {
		w.WriteHeader(o.status)
		_, _ = w.Write([]byte(`{"error":"unknown","request_id":"test"}`))
		return
	}
	resp := claimWireResponse{
		JWT:       o.jwt,
		ExpiresAt: o.exp.UTC().Format(time.RFC3339),
		JTI:       o.jti,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		m.t.Errorf("encode claim response: %v", err)
	}
}

// handleSecret returns the ECIES-encrypted scope value for /s/{name}.
func (m *mockVault) handleSecret(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Path[3:]
	m.behaviour.mu.Lock()
	status, hit := m.behaviour.scopeFail[name]
	plain, ok := m.behaviour.scopeValue[name]
	m.behaviour.mu.Unlock()
	if hit && status != 0 {
		if status == http.StatusUnauthorized {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unknown_jti"}`))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"server_error"}`))
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	env, err := ecies.Encrypt(r.Context(), m.ephPub, plain)
	if err != nil {
		m.t.Errorf("mockVault: encrypt: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(env)
}

// randomToken returns n random alphanumeric bytes; safe for tests.
func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = io.ReadFull(rand.Reader, b)
	for i := range b {
		b[i] = 'a' + (b[i] % 26)
	}
	return string(b)
}

// testLifecycle bundles a Lifecycle and its programmable test seams.
type testLifecycle struct {
	lc       *Lifecycle
	vault    *mockVault
	alerts   *recordingAlerts
	wd       *recordingWatchdog
	auditLog *recordingAudit
	cfg      *config.Supervisor
}

// newTestLifecycle constructs a Lifecycle suitable for unit tests. Caller
// supplies cmd (the child argv). The lifecycle's scope is fixed at
// "ANTHROPIC_API_KEY"; tests may mutate the returned cfg field directly
// before Run for further customization (e.g., BootRetryTimeout).
//
//nolint:unparam // opts kept reserved for future test customization hooks
func newTestLifecycle(t *testing.T, cmd []string, opts ...func(*config.Supervisor)) *testLifecycle {
	t.Helper()
	dir := t.TempDir()
	// PidFile + StatusServer require parent dir mode 0700.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod tempdir: %v", err)
	}
	decryptKey := testECDSAKey(t)
	claimKey := testECDSAKey(t)

	vault := newMockVault(t, &decryptKey.PublicKey)
	vault.SetScope("ANTHROPIC_API_KEY", []byte("sk-test-value-xxx"))

	cfg := &config.Supervisor{
		Name:                   "test-daemon",
		Reason:                 "test",
		ServerURL:              vault.URL(),
		ClientMachineIndex:     0,
		SessionType:            "supervisor",
		RequestedTTL:           24 * time.Hour,
		RefreshWindow:          "09:00-10:00",
		RefreshNudgeBefore:     30 * time.Minute,
		BootRetryTimeout:       2 * time.Second,
		CacheSecretsForRestart: true,
		CacheGraceTTL:          4 * time.Hour,
		StatusSocket:           filepath.Join(dir, "status.sock"),
		PIDFile:                filepath.Join(dir, "supervise.pid"),
		LogLevel:               "info",
		Scope:                  []string{"ANTHROPIC_API_KEY"},
		Child: config.Child{
			Command:        cmd,
			EnvPassthrough: []string{},
		},
		Discord:    config.DiscordRouting{DaemonLabel: "[TEST]"},
		Validators: map[string]config.Validator{},
		Watchdog: config.Watchdog{
			Enabled:          false,
			Patterns:         []string{},
			MaxAlertsPerHour: 1,
		},
	}
	for _, o := range opts {
		o(cfg)
	}

	pid, err := AcquirePidFile(cfg.PIDFile)
	if err != nil {
		t.Fatalf("AcquirePidFile: %v", err)
	}
	t.Cleanup(func() { _ = pid.Release() })

	alerts := &recordingAlerts{}
	wd := &recordingWatchdog{}
	auditLog := &recordingAudit{}

	deps := Deps{
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		HTTPClient:      vault.Client(),
		Clock:           realClockTest{},
		ClaimSigningKey: claimKey,
		DecryptKey:      decryptKey,
		AuditWriter:     auditLog,
		PidFile:         pid,
		Validators:      nil,
		Alerts:          alerts,
		Watchdog:        wd,
		TailscaleProbe:  func(context.Context) error { return nil },
		NowFn:           time.Now,
		NonceFn:         func() string { return randomToken(43) },
		RequestIDFn:     func() string { return randomToken(32) },
	}

	lc := NewLifecycle(context.Background(), cfg, deps)
	// Prevent the Refresher's initial tick from firing during boot when
	// the wall clock happens to fall inside cfg.RefreshWindow on the
	// developer machine. Tests that exercise the refresher push
	// refreshTickCh manually, so priming lastFiredDay=today is inert.
	lc.refresher.primeForTest(time.Now(), false)
	return &testLifecycle{
		lc:       lc,
		vault:    vault,
		alerts:   alerts,
		wd:       wd,
		auditLog: auditLog,
		cfg:      cfg,
	}
}

// realClockTest is the trivial Clock impl for tests.
type realClockTest struct{}

// Now returns time.Now().
func (realClockTest) Now() time.Time { return time.Now() }
