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
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/supervise"
	superviseconfig "github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/internal/supervise/watchdog"
	"github.com/mrz1836/hush/internal/testutil"
)

// jsonUnmarshal is a tiny wrapper so callers can swap to a faster decoder
// later without touching call sites.
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// FakeClock is the harness's injectable monotonic clock, aliased to the
// canonical testutil implementation. Used by Scenarios 8 / 9 / 11 to
// drive documented transitions without time.Sleep.
type FakeClock = testutil.FakeClock

// NewFakeClock builds a FakeClock anchored at the supplied instant.
func NewFakeClock(t time.Time) *FakeClock {
	return testutil.NewFakeClock(t)
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
	auditPubKey   *ecdsa.PublicKey
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
	cfgPath       string
	proxy         *supervise.Proxy
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

	// Child, when non-nil, replaces the default `/bin/sh while-true` child
	// command with the scripted TestChild's argv (Scenarios 3/4/5/9/13/15).
	Child *TestChild
	// CacheSecretsForRestart, when non-nil, overrides the supervisor config's
	// cache_secrets_for_restart flag (default true). Scenario 9a sets false to
	// exercise strict overnight-expiry; Scenario 9b leaves it true for grace.
	CacheSecretsForRestart *bool
	// Validators is wired straight into supervise.Deps.Validators — a per-scope
	// pre-flight credential check (Scenario 6).
	Validators map[string]supervise.Validator
	// WatchdogPatterns, when non-empty, builds a real log-pattern watchdog;
	// the harness runs its matcher loop and bridges matches into the Discord
	// alert log as AlertClassLogPatternMatch (Scenario 15).
	WatchdogPatterns []watchdog.Pattern
	// Reload, when non-nil, builds a reload-eligible supervisor: the
	// supervisor's child command is overridden with the testdata
	// reload-child binary, the TOML is augmented with [child.readiness] /
	// [child.shutdown] / [child.handoff], and HUSH_BIND_PORT is wired
	// through env_passthrough. See reload.go for the field-level knobs
	// (forced unreadiness, ignored SIGTERM, omitted sections, ...).
	// Mutually exclusive with Child — supplying both is a programming
	// error and the harness fatals at NewSupervisor.
	Reload *ReloadOpts
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

	if opts.Reload != nil && opts.Child != nil {
		t.Fatal("harness.NewSupervisor: SupervisorOpts.Reload is mutually exclusive with SupervisorOpts.Child")
	}

	// 2. Build a supervisor config TOML pointing at the test server URL
	//    plus per-supervisor pidfile / status-socket / audit-log paths.
	cfg, cfgPath := buildSupervisorConfig(t, opts)

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

	alerts := opts.Discord.AsSuperviseAlerts()

	deps := supervise.Deps{
		Logger:          opts.Logger.Logger(),
		HTTPClient:      &http.Client{Timeout: 5 * time.Second},
		Clock:           clock,
		ClaimSigningKey: signKey,
		DecryptKey:      decryptKey,
		AuditWriter:     auditWriter,
		PidFile:         pidfile,
		Validators:      opts.Validators,
		Alerts:          alerts,
		TailscaleProbe:  tailscaleProbe,
		VaultHzProbe:    vaultHzProbe,
		NowFn:           nowFn,
		NonceFn:         randomNonce,
		RequestIDFn:     randomRequestID,
	}

	runCtx, runCancel := context.WithCancel(context.Background())

	// 6. Build the log-pattern watchdog when the scenario supplies patterns.
	//    The harness owns the matcher loop and the Event→Alerts bridge,
	//    mirroring the production wiring in internal/cli/supervise_run.go.
	if len(opts.WatchdogPatterns) > 0 {
		events := make(chan watchdog.Event, 64)
		wd, wdErr := watchdog.NewWatchdog(opts.WatchdogPatterns, events, opts.Logger.Logger())
		if wdErr != nil {
			t.Fatalf("harness.NewSupervisor: watchdog.NewWatchdog: %v", wdErr)
		}
		deps.Watchdog = wd
		go func() { _ = wd.Run(runCtx) }()
		go watchdog.DrainToAlerts(runCtx, events, alerts)
	}

	lifecycle := supervise.NewLifecycle(runCtx, cfg, deps)
	// Neutralize the Refresher's wall-clock window so the only refresh a
	// scenario observes is the one it drives explicitly via
	// TriggerWindowRefresh — otherwise a run inside 09:00-10:00 local time
	// would inject a spurious mid-boot claim swap.
	lifecycle.PrimeRefresherForTest()

	ts := &TestSupervisor{
		cfg:           cfg,
		deps:          deps,
		lifecycle:     lifecycle,
		auditWriter:   auditWriter,
		auditCancel:   auditCancel,
		auditDone:     auditDone,
		auditPubKey:   &auditKey.PublicKey,
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
		cfgPath:       cfgPath,
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
// Safe to call even when Run was never invoked: runDone is only closed by
// the Run goroutine, so draining it is gated on startedAt to avoid
// blocking forever on a supervisor that was built but never started.
func (s *TestSupervisor) Stop() {
	if s == nil {
		return
	}
	if s.runCancel != nil {
		s.runCancel()
		if s.startedAt != (time.Time{}) {
			<-s.runDone
		}
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

// HasAudit reports whether the on-disk supervisor audit log contains at
// least one event with the given action.
func (s *TestSupervisor) HasAudit(action string) bool {
	for _, ev := range s.ReadAudit() {
		if ev.Action == action {
			return true
		}
	}
	return false
}

// WaitAudit polls the supervisor audit log until an event with the given
// action appears or the deadline expires. Bounded poll — never sleeps on the
// hot path beyond a short cadence.
func (s *TestSupervisor) WaitAudit(t *testing.T, action string, deadline time.Duration) {
	t.Helper()
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		if s.HasAudit(action) {
			return
		}
		runtime.Gosched()
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("harness.WaitAudit: action %q absent after %s", action, deadline)
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

// TriggerRefresh drives an operator-style `hush client refresh` (stop child,
// refetch, restart) via the integration-only seam (Scenarios 7 / 13).
func (s *TestSupervisor) TriggerRefresh(ctx context.Context) error {
	return s.lifecycle.TriggerRefreshForTest(ctx)
}

// TriggerWindowRefresh drives the daytime refresh-window claim swap (a fresh
// JWT for the next session window; child keeps running) via the
// integration-only seam (Scenario 8).
func (s *TestSupervisor) TriggerWindowRefresh(ctx context.Context) {
	s.lifecycle.TriggerWindowRefreshForTest(ctx)
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
	return s.auditPubKey
}

// AssertAuditChain stops the supervisor (draining the audit writer) and
// verifies the on-disk audit chain is hash-linked and signature-valid under
// the supervisor's audit key. Call once at scenario end — Stop is idempotent
// so the t.Cleanup-registered Stop remains a harmless no-op afterwards.
func (s *TestSupervisor) AssertAuditChain(t *testing.T) {
	t.Helper()
	s.Stop()
	AssertAuditChainContinuity(t, s.AuditPath(), s.AuditKey())
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
	for range maxIters {
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
func buildSupervisorConfig(t *testing.T, opts SupervisorOpts) (*superviseconfig.Supervisor, string) {
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

	// Child command: the scripted TestChild's argv when supplied, the
	// reload-child binary when reload-eligible mode is requested, else a
	// long-lived no-op loop.
	childCmd := []string{"/bin/sh", "-c", "while true; do sleep 1; done"}
	envPassthrough := `["PATH"]`
	var reloadFrag reloadTOMLFragments
	if opts.Child != nil {
		childCmd = opts.Child.Cmd().Command
	}
	if opts.Reload != nil {
		reloadFrag = buildReloadTOMLFragments(t, opts.Reload)
		childCmd = reloadFrag.commandArgv
		// HUSH_BIND_PORT must be reachable inside the child so it can bind
		// 127.0.0.1:<port>. Passing through PATH preserves binary lookup.
		envPassthrough = `["PATH", "HUSH_BIND_PORT"]`
	}
	cmdToml := "["
	for i, a := range childCmd {
		if i > 0 {
			cmdToml += ", "
		}
		cmdToml += fmt.Sprintf("%q", a)
	}
	cmdToml += "]"

	// cache_secrets_for_restart defaults to true (grace mode); Scenario 9a
	// overrides it to false for strict overnight-expiry.
	cacheEnabled := true
	if opts.CacheSecretsForRestart != nil {
		cacheEnabled = *opts.CacheSecretsForRestart
	}
	cacheToml := "cache_secrets_for_restart = false"
	if cacheEnabled {
		cacheToml = "cache_secrets_for_restart = true\ncache_grace_ttl = \"1h\""
	}

	body := fmt.Sprintf(
		`name = %q
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
%s

scope = %s

[child]
command = %s
working_dir = "/tmp"
env_passthrough = %s

%s
%s
%s
%s
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
		cacheToml,
		scopesToml,
		cmdToml,
		envPassthrough,
		reloadFrag.envBlock,
		reloadFrag.readiness,
		reloadFrag.shutdown,
		reloadFrag.handoff,
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
	return cfg, cfgPath
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
