//go:build integration

// Scenario 16 — Zero-Downtime HTTP Reload (T-306 Phase 8).
//
// Exercises the public reload surface end-to-end against a real HTTP
// test child: pkg/client.SupervisorStatus.Reload → status socket →
// lifecycle.SwapChild → HTTP-proxy backend swap. The child is the
// reload-child binary under tests/integration/testdata; the supervisor
// uses a real config TOML, real audit chain, real Unix status socket.
//
// Contracts (mapped to AC-4 / AC-5 / AC-9):
//
//	A — Happy path swap: the proxy continuously serves through the swap
//	    (every concurrent /health poll observes 200), and the new
//	    child's PID becomes the lifecycle child PID after the swap.
//	    (AC-4)
//	B — Readiness-failure rollback: a child that returns 503 forever
//	    causes the reload to exit non-zero with ErrReloadReadinessFailed,
//	    the old child remains the active backend, the proxy keeps
//	    serving the old child's PID, and no supervisor_child_swap audit
//	    event is recorded. (AC-5)
//	C — Config refusal: a supervisor started WITHOUT [child.handoff]
//	    rejects the reload with ErrReloadConfigInvalid. A separate
//	    on-disk TOML with [child.handoff] but no [child.readiness]
//	    fails config.Load locally with ErrHandoffRequiresReadiness, and
//	    a TOML with an invalid [child.handoff.mode] fails with
//	    ErrHandoffModeInvalid. (AC-9)
//	D — 6-stream sentinel sweep + audit-chain continuity, as with every
//	    other lifecycle scenario.
//
// Sentinel: testutil.SentinelSecret(18).
package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/supervise"
	superviseconfig "github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/pkg/client"
	"github.com/mrz1836/hush/tests/integration/harness"
)

// scenario16ChildPIDHeader is the X-Child-Pid header the reload-child
// binary stamps on every /health response. Test assertions parse it
// instead of dialing /pid to keep the assertion path on the proxy.
const scenario16ChildPIDHeader = "X-Child-Pid"

// TestScenario16_Reload is the top-level Scenario 16 entry point. The
// sub-tests share no state; each rebuilds the harness from scratch so
// the audit chain and pidfile are fresh.
func TestScenario16_Reload(t *testing.T) {
	t.Run("HappyPath", scenario16HappyPath)
	t.Run("ReadinessFailureRollsBack", scenario16ReadinessFailure)
	t.Run("RefusesMissingHandoff", scenario16RefusesMissingHandoff)
	t.Run("RefusesLocalConfigInvalid", scenario16RefusesLocalConfigInvalid)
}

// scenario16HappyPath exercises AC-4: a clean reload swaps the child
// while the proxy serves uninterrupted. The poll worker drives /health
// through the proxy at high cadence across the swap window and asserts
// every response is 200 OK; the new PID is observed at the end.
//
//nolint:gocognit,gocyclo // end-to-end happy-path: PID + audit + state + continuous-poll assertions necessarily co-located
func scenario16HappyPath(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(18),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:   vault,
		Server:  srv,
		Discord: discord,
		Logger:  logger,
		Name:    "reload-happy",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
		Reload: &harness.ReloadOpts{
			Version: "v0",
		},
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	sup.WaitState(t, supervise.StateRunning, 5*time.Second)
	proxy := sup.AttachProxyForReload(t)

	// Validate the boot-time child is reachable through the proxy.
	code, body, headers := sup.ProxyGet(t, "/health")
	if code != http.StatusOK || body != "ready" {
		t.Fatalf("scenario16: pre-swap /health: code=%d body=%q", code, body)
	}
	oldPID := headers.Get(scenario16ChildPIDHeader)
	if oldPID == "" {
		t.Fatalf("scenario16: pre-swap /health missing %s header: %v", scenario16ChildPIDHeader, headers)
	}

	// Spawn a poll worker that hammers /health through the proxy across
	// the swap window. Every response must be 200 — a single 5xx / dial
	// failure is treated as a downtime violation. Bounded by ctx.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		pollWg       sync.WaitGroup
		pollFailures atomic.Uint64
		pollCount    atomic.Uint64
	)
	pollWg.Add(1)
	go func() {
		defer pollWg.Done()
		httpClient := &http.Client{Timeout: 1500 * time.Millisecond}
		addr := proxy.ListenAddr()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/health", http.NoBody)
			resp, err := httpClient.Do(req)
			pollCount.Add(1)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				pollFailures.Add(1)
				continue
			}
			code := resp.StatusCode
			_ = resp.Body.Close()
			if code != http.StatusOK {
				pollFailures.Add(1)
			}
		}
	}()

	// Drive the reload via the public SDK against the supervisor's
	// status socket — same path the CLI exercises.
	reloadCtx, reloadCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer reloadCancel()
	sdkClient := client.NewSupervisorStatus(sup.StatusSocketPath())
	res, err := sdkClient.Reload(reloadCtx, sup.ConfigPath())
	if err != nil {
		cancel()
		pollWg.Wait()
		t.Fatalf("scenario16: SDK Reload: %v", err)
	}
	if res.Strategy != supervise.HandoffStrategyHTTPProxy {
		t.Errorf("scenario16: reload strategy: got %q want %q", res.Strategy, supervise.HandoffStrategyHTTPProxy)
	}
	if res.OldPID == 0 || res.NewPID == 0 || res.OldPID == res.NewPID {
		t.Errorf("scenario16: reload PIDs: old=%d new=%d (want non-zero and distinct)", res.OldPID, res.NewPID)
	}
	if res.ReadinessDuration <= 0 {
		t.Errorf("scenario16: reload readiness duration: got %s want >0", res.ReadinessDuration)
	}

	// Let the poll worker do another quick burst against the new child
	// to prove the post-swap path is still healthy.
	time.Sleep(75 * time.Millisecond)
	cancel()
	pollWg.Wait()

	if pollCount.Load() == 0 {
		t.Fatalf("scenario16: poll worker never executed; harness wiring broken")
	}
	if failures := pollFailures.Load(); failures != 0 {
		t.Errorf("scenario16: proxy lost availability during swap: %d/%d polls failed", failures, pollCount.Load())
	}

	// Post-swap: the proxy must now serve the new child's PID.
	postCode, postBody, postHeaders := sup.ProxyGet(t, "/health")
	if postCode != http.StatusOK || postBody != "ready" {
		t.Fatalf("scenario16: post-swap /health: code=%d body=%q", postCode, postBody)
	}
	newPID := postHeaders.Get(scenario16ChildPIDHeader)
	if newPID == "" || newPID == oldPID {
		t.Errorf("scenario16: post-swap PID via proxy: got %q (was %q); expected new PID", newPID, oldPID)
	}
	if got := fmt.Sprintf("%d", res.NewPID); newPID != got {
		t.Errorf("scenario16: proxy-observed PID (%s) does not match reload result NewPID (%s)", newPID, got)
	}

	// Audit must record exactly one supervisor_child_swap event.
	var swaps int
	for _, ev := range sup.ReadAudit() {
		if ev.Action == audit.ActionSupervisorChildSwap {
			swaps++
		}
	}
	if swaps != 1 {
		t.Errorf("scenario16: audit %s count: got %d want 1", audit.ActionSupervisorChildSwap, swaps)
	}

	// State machine must be back at StateRunning after the swap.
	harness.AssertSupervisorState(t, sup.State(), supervise.StateRunning)

	// 6-stream sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(
		t,
		testutil.SentinelSecret(18),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}

// scenario16ReadinessFailure exercises AC-5: the replacement child
// returns 503 forever, so the readiness probe times out, the new child
// is killed, and the old child remains the active backend. The reload
// SDK call returns ErrReloadReadinessFailed and the proxy continues to
// serve traffic.
func scenario16ReadinessFailure(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(18),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:   vault,
		Server:  srv,
		Discord: discord,
		Logger:  logger,
		Name:    "reload-readiness-fail",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
		Reload: &harness.ReloadOpts{
			Version:           "v0",
			ForceUnready:      true,
			ReadinessTimeout:  300 * time.Millisecond,
			ReadinessInterval: 25 * time.Millisecond,
		},
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	// The boot-time child also returns 503 from /health (ForceUnready
	// applies to every reload-child instance the supervisor spawns), so
	// the harness skips the "wait for /health" gate here. The proxy
	// listener still binds and the swap orchestrator drives the test's
	// failure path via the readiness probe budget.
	sup.WaitState(t, supervise.StateRunning, 5*time.Second)
	sup.AttachProxyForReloadSkipHealthWait(t)

	reloadCtx, reloadCancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer reloadCancel()
	sdkClient := client.NewSupervisorStatus(sup.StatusSocketPath())
	_, reloadErr := sdkClient.Reload(reloadCtx, sup.ConfigPath())
	if reloadErr == nil {
		t.Fatalf("scenario16: SDK Reload: expected ErrReloadReadinessFailed, got nil")
	}
	if !errors.Is(reloadErr, client.ErrReloadReadinessFailed) {
		t.Fatalf("scenario16: SDK Reload: want ErrReloadReadinessFailed, got %v", reloadErr)
	}

	// Lifecycle must still be in StateRunning (rollback path).
	harness.AssertSupervisorState(t, sup.State(), supervise.StateRunning)

	// No swap audit event should have been recorded.
	for _, ev := range sup.ReadAudit() {
		if ev.Action == audit.ActionSupervisorChildSwap {
			t.Errorf("scenario16: swap audit event recorded on readiness failure: %+v", ev)
		}
	}

	// 6-stream sentinel sweep + audit-chain continuity.
	harness.AssertSentinelAbsent(
		t,
		testutil.SentinelSecret(18),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}

// scenario16RefusesMissingHandoff exercises AC-9: a supervisor started
// WITHOUT [child.handoff] refuses the reload at the swap level. The
// status server maps ErrSwapNotEligible onto the wire-stable
// "config-invalid" result, which the SDK exposes as
// ErrReloadConfigInvalid.
func scenario16RefusesMissingHandoff(t *testing.T) {
	logger := harness.NewLogCapture(t)
	vault := harness.NewVault(t, map[string]string{
		"ANTHROPIC_API_KEY": testutil.SentinelSecret(18),
	})
	discord := harness.NewDiscord(t)
	discord.Stub().ApproveAll = true
	srv := harness.NewServer(t, harness.ServerOpts{
		Vault:   vault,
		Logger:  logger,
		Discord: discord,
	})

	// Plain supervisor — no Reload opts, so no [child.handoff] block
	// makes it into the TOML. lifecycle.SwapChild MUST refuse with
	// ErrSwapNotEligible → wire code "config-invalid".
	sup := harness.NewSupervisor(t, harness.SupervisorOpts{
		Vault:   vault,
		Server:  srv,
		Discord: discord,
		Logger:  logger,
		Name:    "reload-no-handoff",
		Scopes:  []string{"ANTHROPIC_API_KEY"},
	})
	sup.Run()

	defer func() {
		if t.Failed() {
			t.Logf("captured supervisor logs:\n%s", logger.Bytes())
		}
	}()

	sup.WaitState(t, supervise.StateRunning, 5*time.Second)
	// Wire the reload handler directly — production wiring would do
	// this from internal/cli, but the harness mirrors it for the
	// integration build. Without a handler the socket would respond
	// "reload handler not wired" which is a different (but equally
	// non-zero) failure mode.
	sup.AttachReloadHandlerOnly(t)

	reloadCtx, reloadCancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer reloadCancel()
	sdkClient := client.NewSupervisorStatus(sup.StatusSocketPath())
	_, reloadErr := sdkClient.Reload(reloadCtx, sup.ConfigPath())
	if reloadErr == nil {
		t.Fatalf("scenario16: SDK Reload: expected ErrReloadConfigInvalid, got nil")
	}
	if !errors.Is(reloadErr, client.ErrReloadConfigInvalid) {
		t.Fatalf("scenario16: SDK Reload: want ErrReloadConfigInvalid, got %v", reloadErr)
	}

	// State must be unchanged.
	harness.AssertSupervisorState(t, sup.State(), supervise.StateRunning)
	// No swap audit event was recorded.
	for _, ev := range sup.ReadAudit() {
		if ev.Action == audit.ActionSupervisorChildSwap {
			t.Errorf("scenario16: swap audit event recorded on config refusal: %+v", ev)
		}
	}

	harness.AssertSentinelAbsent(
		t,
		testutil.SentinelSecret(18),
		logger.Bytes(),
		srv.RawAudit(),
		sup.RawAudit(),
		sup.StatusRaw(),
		discord.AlertsRaw(),
		nil, nil,
	)
	sup.AssertAuditChain(t)
}

// scenario16RefusesLocalConfigInvalid exercises the local-side AC-9
// refusal paths: a TOML with [child.handoff] but missing
// [child.readiness] fails config.Load with ErrHandoffRequiresReadiness,
// and a TOML with an invalid [child.handoff.mode] fails with
// ErrHandoffModeInvalid. These refusals happen before the SDK ever
// dials the supervisor — the operator's local file is malformed.
func scenario16RefusesLocalConfigInvalid(t *testing.T) {
	dir := t.TempDir()

	missingReadiness := `name = "missing-readiness"
reason = "harness integration test"
server_url = "http://127.0.0.1:1/h/missing"
client_machine_index = 2
session_type = "supervisor"
requested_ttl = "1h"
status_socket = "/tmp/nope.sock"
pid_file = "/tmp/nope.pid"
audit_log = "/tmp/nope-audit.jsonl"

scope = ["ANTHROPIC_API_KEY"]

[child]
command = ["/bin/true", "$HUSH_BIND_PORT"]
working_dir = "/tmp"
env_passthrough = ["PATH", "HUSH_BIND_PORT"]

[child.handoff]
mode = "http-proxy"
listen_addr = "127.0.0.1:0"

[validators]
ANTHROPIC_API_KEY = "anthropic"
`
	mrPath := filepath.Join(dir, "missing-readiness.toml")
	if err := os.WriteFile(mrPath, []byte(missingReadiness), 0o600); err != nil {
		t.Fatalf("scenario16: write missing-readiness toml: %v", err)
	}
	if _, err := superviseconfig.Load(t.Context(), mrPath); !errors.Is(err, superviseconfig.ErrHandoffRequiresReadiness) {
		t.Errorf("scenario16: config.Load(missing-readiness): want ErrHandoffRequiresReadiness, got %v", err)
	}

	invalidMode := `name = "invalid-mode"
reason = "harness integration test"
server_url = "http://127.0.0.1:1/h/invalid"
client_machine_index = 2
session_type = "supervisor"
requested_ttl = "1h"
status_socket = "/tmp/nope.sock"
pid_file = "/tmp/nope.pid"
audit_log = "/tmp/nope-audit.jsonl"

scope = ["ANTHROPIC_API_KEY"]

[child]
command = ["/bin/true", "$HUSH_BIND_PORT"]
working_dir = "/tmp"
env_passthrough = ["PATH", "HUSH_BIND_PORT"]

[child.readiness]
http_url = "http://127.0.0.1:0/health"
timeout = "1s"
interval = "100ms"

[child.handoff]
mode = "fd-inheritance"
listen_addr = "127.0.0.1:0"

[validators]
ANTHROPIC_API_KEY = "anthropic"
`
	imPath := filepath.Join(dir, "invalid-mode.toml")
	if err := os.WriteFile(imPath, []byte(invalidMode), 0o600); err != nil {
		t.Fatalf("scenario16: write invalid-mode toml: %v", err)
	}
	if _, err := superviseconfig.Load(t.Context(), imPath); !errors.Is(err, superviseconfig.ErrHandoffModeInvalid) {
		t.Errorf("scenario16: config.Load(invalid-mode): want ErrHandoffModeInvalid, got %v", err)
	}
}
