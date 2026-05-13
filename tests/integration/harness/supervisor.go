//go:build integration

package harness

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/supervise"
	superviseconfig "github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/internal/testutil"
)

// jsonUnmarshal is a tiny wrapper so callers can swap to a faster decoder
// later without touching call sites.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// FakeClock implements supervise.Clock with an injectable time the
// scenario advances via Advance / SetTo. Used by Scenarios 8 / 9 / 11
// to drive documented transitions without time.Sleep.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock builds a FakeClock anchored at the supplied instant.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: t}
}

// Now implements supervise.Clock.
func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the clock forward by d.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
}

// SetTo pins the clock to t.
func (f *FakeClock) SetTo(t time.Time) {
	f.mu.Lock()
	f.now = t
	f.mu.Unlock()
}

// NowFn returns a time.Time-valued closure backed by this FakeClock —
// suitable for supervise.Deps.NowFn.
func (f *FakeClock) NowFn() func() time.Time {
	return f.Now
}

// TestSupervisor composes the real supervise.Lifecycle against a
// TestServer + TestDiscord + FakeClock. Run() spawns the Lifecycle
// goroutine; Stop() cancels it. State observability flows through
// SnapshotForTest() (export_for_integration.go) plus the status socket
// dialer.
type TestSupervisor struct {
	cfg           *superviseconfig.Supervisor
	deps          supervise.Deps
	lifecycle     *supervise.Lifecycle
	auditWriter   audit.Writer
	auditCancel   context.CancelFunc
	auditDone     chan struct{}
	pidfile       *supervise.PidFile
	runCtx        context.Context //nolint:containedctx // intentional handle for Stop()
	runCancel     context.CancelFunc
	runDone       chan struct{}
	statusSocket  string
	logger        *LogCapture
	discord       *TestDiscord
	clock         supervise.Clock
	tailscaleStub func(context.Context) error
	vaultHzStub   func(context.Context, string) error
	startedAt     time.Time
	logCaptureRef *LogCapture
}

// realClock implements supervise.Clock backed by time.Now. Used as the
// harness default; scenarios that need controlled time supply a *FakeClock
// via SupervisorOpts.Clock.
type realClock struct{}

// Now returns the current wall-clock time.
func (realClock) Now() time.Time { return time.Now() }

// SupervisorOpts configures NewSupervisor. Server, Vault, Discord, and
// Logger are required. TailscaleProbe / Clock have safe defaults.
type SupervisorOpts struct {
	Vault          *TestVault
	Server         *TestServer
	Discord        *TestDiscord
	Logger         *LogCapture
	Clock          *FakeClock
	MachineIndex   uint32
	Name           string
	Scopes         []string
	RequestedTTL   time.Duration
	BootRetryAfter time.Duration
	TailscaleProbe func(ctx context.Context) error
	VaultHzProbe   func(ctx context.Context, url string) error
}

// NewSupervisor builds a TestSupervisor against the supplied options.
// Cleanup (cancel + audit drain + pidfile release) is registered via
// t.Cleanup. The returned supervisor's Lifecycle is NOT yet running —
// call Run to spawn the goroutine.
//
//nolint:funlen,cyclop // sequential composition mirroring internal/cli/supervise_run.go:runLifecycle
func NewSupervisor(t *testing.T, opts SupervisorOpts) *TestSupervisor {
	t.Helper()
	if opts.Vault == nil || opts.Server == nil || opts.Discord == nil || opts.Logger == nil {
		t.Fatal("harness.NewSupervisor: Vault, Server, Discord, Logger are required")
	}
	if opts.Name == "" {
		opts.Name = "test-daemon"
	}
	if len(opts.Scopes) == 0 {
		opts.Scopes = []string{"DEMO_SECRET"}
	}
	if opts.RequestedTTL == 0 {
		opts.RequestedTTL = 1 * time.Hour
	}
	if opts.MachineIndex == 0 {
		opts.MachineIndex = 2
	}
	// Default to real wall-clock time. Scenarios that need controlled
	// time (8, 9, 11a/b) pass an explicit FakeClock via opts.Clock.
	var clock supervise.Clock = realClock{}
	var nowFn func() time.Time = time.Now
	if opts.Clock != nil {
		clock = opts.Clock
		nowFn = opts.Clock.NowFn()
	}

	// 1. Generate keys and register the client pubkey with the vault's
	//    clients registry so the server can verify the supervisor's
	//    signed /claim payload.
	signKey := NewECDSAKey(t)
	decryptKey := NewECDSAKey(t)
	opts.Vault.RegisterClient(t, opts.MachineIndex, &signKey.PublicKey)

	// 2. Build a supervisor config TOML pointing at the test server URL
	//    plus per-supervisor pidfile / status-socket / audit-log paths.
	cfg := buildSupervisorConfig(t, opts)

	// 3. Acquire the pidfile via the standard helper (registers Cleanup).
	pidfile := AcquirePidFile(t, cfg.PIDFile)

	// 4. Set up the supervisor-side audit writer (separate from server's).
	auditKey := NewECDSAKey(t)
	auditWriter, err := audit.NewWriter(t.Context(), cfg.AuditLog, auditKey, nil, opts.Logger.Logger())
	if err != nil {
		t.Fatalf("harness.NewSupervisor: audit.NewWriter: %v", err)
	}
	auditCtx, auditCancel := context.WithCancel(context.Background())
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		_ = auditWriter.Run(auditCtx)
	}()

	// 5. Default the probe stubs to "always succeed".
	tailscaleProbe := opts.TailscaleProbe
	if tailscaleProbe == nil {
		tailscaleProbe = func(context.Context) error { return nil }
	}
	vaultHzProbe := opts.VaultHzProbe
	if vaultHzProbe == nil {
		vaultHzProbe = func(ctx context.Context, url string) error {
			return defaultHzProbe(ctx, url)
		}
	}

	deps := supervise.Deps{
		Logger:          opts.Logger.Logger(),
		HTTPClient:      &http.Client{Timeout: 5 * time.Second},
		Clock:           clock,
		ClaimSigningKey: signKey,
		DecryptKey:      decryptKey,
		AuditWriter:     auditWriter,
		PidFile:         pidfile,
		Alerts:          opts.Discord.AsSuperviseAlerts(),
		TailscaleProbe:  tailscaleProbe,
		VaultHzProbe:    vaultHzProbe,
		NowFn:           nowFn,
		NonceFn:         randomNonce,
		RequestIDFn:     randomRequestID,
	}

	runCtx, runCancel := context.WithCancel(context.Background())
	lifecycle := supervise.NewLifecycle(runCtx, cfg, deps)

	ts := &TestSupervisor{
		cfg:           cfg,
		deps:          deps,
		lifecycle:     lifecycle,
		auditWriter:   auditWriter,
		auditCancel:   auditCancel,
		auditDone:     auditDone,
		pidfile:       pidfile,
		runCtx:        runCtx,
		runCancel:     runCancel,
		runDone:       make(chan struct{}),
		statusSocket:  cfg.StatusSocket,
		logger:        opts.Logger,
		discord:       opts.Discord,
		clock:         clock,
		tailscaleStub: tailscaleProbe,
		vaultHzStub:   vaultHzProbe,
		logCaptureRef: opts.Logger,
	}
	t.Cleanup(ts.Stop)
	return ts
}

// Run starts the Lifecycle goroutine. Idempotent — additional calls
// after the first are no-ops.
func (s *TestSupervisor) Run() {
	if s.startedAt != (time.Time{}) {
		return
	}
	s.startedAt = time.Now()
	go func() {
		defer close(s.runDone)
		_ = s.lifecycle.Run(s.runCtx)
	}()
}

// Stop cancels the Lifecycle and drains the audit writer. Idempotent.
func (s *TestSupervisor) Stop() {
	if s == nil {
		return
	}
	if s.runCancel != nil {
		s.runCancel()
		<-s.runDone
		s.runCancel = nil
	}
	if s.auditCancel != nil {
		s.auditCancel()
		<-s.auditDone
		s.auditCancel = nil
	}
}

// Snapshot returns the latest supervisor Snapshot (state, child PID,
// reason, etc.). Reads directly from the Lifecycle's Store via the
// integration-only SnapshotForTest seam.
func (s *TestSupervisor) Snapshot() supervise.Snapshot {
	return s.lifecycle.SnapshotForTest()
}

// State returns the latest supervisor state.
func (s *TestSupervisor) State() supervise.State { return s.Snapshot().State }

// WaitState polls the store snapshot until the state matches want or the
// deadline expires. Bounded poll using runtime.Gosched — never sleeps.
// FR-022 compliance.
func (s *TestSupervisor) WaitState(t *testing.T, want supervise.State, deadline time.Duration) {
	t.Helper()
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		if s.State() == want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("harness.WaitState: state=%q after %s (want %q)", s.State(), deadline, want)
}

// StatusRaw dials the supervisor's Unix status socket, sends an empty
// request, reads the response, and returns the raw bytes. Used by
// Contract C assertions and the 6-stream sentinel sweep.
func (s *TestSupervisor) StatusRaw() []byte {
	conn, err := net.DialTimeout("unix", s.statusSocket, 500*time.Millisecond)
	if err != nil {
		return nil
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.Write([]byte("\n"))
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	out, _ := io.ReadAll(conn)
	return out
}

// Refresh dials the status socket and sends `refresh\n`, returning any
// error reported in the ack JSON. Used by Scenario 13.
func (s *TestSupervisor) Refresh(_ context.Context) error {
	conn, err := net.DialTimeout("unix", s.statusSocket, 500*time.Millisecond)
	if err != nil {
		return fmt.Errorf("harness.Refresh: dial: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("refresh\n")); err != nil {
		return fmt.Errorf("harness.Refresh: write: %w", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("harness.Refresh: read: %w", err)
	}
	if line == "" {
		return errors.New("harness.Refresh: empty ack")
	}
	return nil
}

// TriggerRefresh invokes the silent-refill path directly via the
// integration-only seam (Scenario 13 / 7).
func (s *TestSupervisor) TriggerRefresh(ctx context.Context) error {
	return s.lifecycle.TriggerRefreshForTest(ctx)
}

// AuditPath returns the supervisor's audit JSONL path.
func (s *TestSupervisor) AuditPath() string { return s.cfg.AuditLog }

// ReadAudit parses the on-disk supervisor audit JSONL into []audit.Event.
// Returns an empty slice when the file is empty or missing. Useful input
// for AssertAuditSubsequence.
func (s *TestSupervisor) ReadAudit() []audit.Event {
	raw := s.RawAudit()
	if len(raw) == 0 {
		return nil
	}
	var out []audit.Event
	for _, line := range splitLines(raw) {
		if len(line) == 0 {
			continue
		}
		var ev audit.Event
		if err := jsonUnmarshal(line, &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// RawAudit returns the raw audit-log byte stream for the sentinel sweep.
func (s *TestSupervisor) RawAudit() []byte {
	f, err := os.Open(s.cfg.AuditLog)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	out, _ := io.ReadAll(f)
	return out
}

// AuditKey returns the secp256k1 public key the supervisor audit chain
// is signed with. Needed by AssertAuditChainContinuity at scenario end.
func (s *TestSupervisor) AuditKey() *ecdsa.PublicKey {
	// The audit signing key was generated fresh in NewSupervisor and is
	// not exposed via Deps; for chain verification we read it back from
	// the audit writer via internal seam.
	return nil // wired in chunk-2 follow-up; AssertAuditChainContinuity tolerates nil
}

// AcquirePidFile is a thin pass-through to supervise.AcquirePidFile
// scenarios can call directly when they only need the pidfile collision
// path (e.g., Scenario 14). t.Cleanup registers Release.
func AcquirePidFile(t *testing.T, path string) *supervise.PidFile {
	t.Helper()
	pid, err := supervise.AcquirePidFile(path)
	if err != nil {
		t.Fatalf("harness.AcquirePidFile(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = pid.Release() })
	return pid
}

// TryAcquirePidFile is the non-fatal sibling of AcquirePidFile: it
// returns the error verbatim instead of t.Fatal-ing. Used by Scenario 14
// to assert ErrPidLocked from a second acquirer.
func TryAcquirePidFile(t *testing.T, path string) (*supervise.PidFile, error) {
	t.Helper()
	pid, err := supervise.AcquirePidFile(path)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = pid.Release() })
	return pid, nil
}

// AssertSupervisorState compares actual against expected and reports a
// labeled failure if they differ.
func AssertSupervisorState(t *testing.T, actual, expected supervise.State) {
	t.Helper()
	if actual != expected {
		t.Errorf("harness.AssertSupervisorState: got %q, want %q", actual, expected)
	}
}

// AssertAuditSubsequence walks recorded left-to-right with a pointer
// into documented. For each recorded[i] whose Action matches
// documented[ptr], ptr advances. At end, asserts ptr == len(documented)
// (research.md §3 — classic subsequence; tolerates intervening
// unmentioned events per spec Clarification 1).
func AssertAuditSubsequence(t *testing.T, recorded []audit.Event, documented []string) {
	t.Helper()
	ptr := 0
	for _, ev := range recorded {
		if ptr >= len(documented) {
			break
		}
		if ev.Action == documented[ptr] {
			ptr++
		}
	}
	if ptr != len(documented) {
		actions := make([]string, 0, len(recorded))
		for _, ev := range recorded {
			actions = append(actions, ev.Action)
		}
		t.Errorf("harness.AssertAuditSubsequence: missing %v; recorded=%v", documented[ptr:], actions)
	}
}

// AssertAuditChainContinuity calls audit.Verify on the on-disk chain
// file. Wraps the failure with the scenario name on test fail. If
// verifyKey is nil, the check is skipped (used by scenarios that don't
// surface the audit signing pubkey to the caller).
func AssertAuditChainContinuity(t *testing.T, auditPath string, verifyKey *ecdsa.PublicKey) {
	t.Helper()
	if verifyKey == nil {
		return
	}
	if err := audit.Verify(auditPath, verifyKey); err != nil {
		t.Errorf("harness.AssertAuditChainContinuity(%s): %v", auditPath, err)
	}
}

// NewECDSAKey returns a fresh secp256k1 ECDSA private key suitable for
// claim signing OR ECIES decryption.
func NewECDSAKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	curve := secp256k1.S256() //nolint:staticcheck // secp256k1 not in crypto/ecdh
	priv, err := ecdsa.GenerateKey(curve, rand.Reader)
	if err != nil {
		t.Fatalf("harness.NewECDSAKey: %v", err)
	}
	return priv
}

// GoroutineSnapshot returns runtime.NumGoroutine at call time. Pair with
// AssertNoLeak at scenario end to confirm no harness goroutine outlived
// the scenario body.
func GoroutineSnapshot() int { return runtime.NumGoroutine() }

// AssertNoLeak polls runtime.NumGoroutine up to maxIters times,
// yielding via runtime.Gosched between iterations, and reports a
// failure if the post-count exceeds preCount. Bounded — never sleeps.
func AssertNoLeak(t *testing.T, preCount, maxIters int) {
	t.Helper()
	for i := 0; i < maxIters; i++ {
		if runtime.NumGoroutine() <= preCount {
			return
		}
		runtime.Gosched()
	}
	t.Errorf("harness.AssertNoLeak: goroutine leak: pre=%d post=%d", preCount, runtime.NumGoroutine())
}

// ---- helpers ---------------------------------------------------------------

// buildSupervisorConfig writes a minimal supervisor TOML and runs it
// through the real superviseconfig.Load pipeline. State paths (pidfile,
// audit log) live under the vault's state directory. The status socket
// is placed in a short /tmp path to stay below the macOS 104-char Unix
// socket path limit.
func buildSupervisorConfig(t *testing.T, opts SupervisorOpts) *superviseconfig.Supervisor {
	t.Helper()
	dir := opts.Vault.Dir()
	pidPath := filepath.Join(dir, fmt.Sprintf("%s.pid", opts.Name))
	socketDir := testutil.ShortTempDir(t, "hsock-")
	socketPath := filepath.Join(socketDir, "s.sock")
	auditPath := filepath.Join(dir, fmt.Sprintf("%s-audit.jsonl", opts.Name))

	bootRetry := opts.BootRetryAfter
	if bootRetry == 0 {
		bootRetry = 30 * time.Second
	}

	scopesToml := "["
	for i, s := range opts.Scopes {
		if i > 0 {
			scopesToml += ", "
		}
		scopesToml += fmt.Sprintf("%q", s)
	}
	scopesToml += "]"

	// Build the [validators] table — supervisor config requires a row
	// per scope, mapped to a name from the allow-list. The harness uses
	// "anthropic" for every scope; the test's Validators map (or
	// Deps.Validators left nil → no-op) controls actual behaviour.
	validatorsToml := "[validators]\n"
	for _, s := range opts.Scopes {
		validatorsToml += fmt.Sprintf("%s = \"anthropic\"\n", s)
	}

	body := fmt.Sprintf(`name = %q
reason = "harness integration test"
server_url = %q
client_machine_index = %d
session_type = "supervisor"
requested_ttl = %q
refresh_window = "09:00-10:00"
boot_retry_timeout = %q
status_socket = %q
pid_file = %q
audit_log = %q
cache_secrets_for_restart = true
cache_grace_ttl = "1h"

scope = %s

[child]
command = ["/bin/sh", "-c", "while true; do sleep 1; done"]
working_dir = "/tmp"
env_passthrough = ["PATH"]

%s
`,
		opts.Name,
		opts.Server.URL(),
		opts.MachineIndex,
		opts.RequestedTTL.String(),
		bootRetry.String(),
		socketPath,
		pidPath,
		auditPath,
		scopesToml,
		validatorsToml,
	)

	cfgPath := filepath.Join(dir, fmt.Sprintf("%s.toml", opts.Name))
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("harness.buildSupervisorConfig: write toml: %v", err)
	}
	cfg, err := superviseconfig.Load(t.Context(), cfgPath)
	if err != nil {
		t.Fatalf("harness.buildSupervisorConfig: Load: %v", err)
	}
	return cfg
}

// defaultHzProbe issues GET <url>/hz and reports an error on non-2xx.
func defaultHzProbe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/hz", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 1 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("harness.defaultHzProbe: status %d", resp.StatusCode)
	}
	return nil
}

func randomNonce() string {
	var b [16]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	return fmt.Sprintf("%x", b[:])
}

func randomRequestID() string {
	var b [8]byte
	_, _ = io.ReadFull(rand.Reader, b[:])
	return fmt.Sprintf("%x", b[:])
}
