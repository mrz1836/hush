# Quickstart — SDD-24 Supervisor Orchestrator

**Audience**: implementing agent (Phase 2 tasks + Phase 3 implement);
SDD-25 harness owner once this chunk lands.

This quickstart documents the developer workflow for the orchestrator:
how to construct a Lifecycle in a test, how to drive the boot path,
how to inject controllable Validator / Alerts / Watchdog triples, and
how to run the gated suites.

---

## 1. Construct a Lifecycle in a unit test (sketch)

```go
//go:build !integration

package supervise

import (
    "context"
    "log/slog"
    "net/http"
    "net/http/httptest"
    "testing"

    "github.com/mrz1836/hush/internal/audit"
    "github.com/mrz1836/hush/internal/supervise/config"
)

func newTestLifecycle(t *testing.T) (*Lifecycle, *recordingAlerts) {
    t.Helper()

    // 1. Mock vault server (httptest)
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Test-specific handlers: /hz, /claim, /s/{name}.
    }))
    t.Cleanup(srv.Close)

    // 2. Supervisor config (use t.TempDir() for every path).
    cfg := &config.Supervisor{
        Name:                   "test-daemon",
        ServerURL:              srv.URL,
        Scope:                  []string{"ANTHROPIC_API_KEY"},
        BootRetryTimeout:       100 * time.Millisecond,
        RefreshWindow:          "09:00-10:00",
        RequestedTTL:           20 * time.Hour,
        CacheSecretsForRestart: false,
        StatusSocket:           filepath.Join(t.TempDir(), "status.sock"),
        PIDFile:                filepath.Join(t.TempDir(), "test-daemon.pid"),
        // ... other required fields ...
    }

    // 3. Acquire pidfile (mirrors cli shim's responsibility).
    pidfile, err := AcquirePidFile(cfg.PIDFile)
    require.NoError(t, err)
    t.Cleanup(func() { _ = pidfile.Release() })

    // 4. Construct test seams.
    alerts := &recordingAlerts{}
    deps := Deps{
        Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
        HTTPClient:      srv.Client(),
        Clock:           realClockForTest{},  // or a controllable fake
        ClaimSigningKey: testECDSAKey(t),
        DecryptKey:      testECDSAKey(t),
        AuditWriter:     audit.NewTestWriter(t),
        PidFile:         pidfile,
        Validators:      nil, // → no-op for every scope
        Alerts:          alerts,
        Watchdog:        nil, // → no-op
        TailscaleProbe:  func(context.Context) error { return nil }, // always-up
        VaultHzProbe:    nil, // → default; uses HTTPClient.Get(srv.URL + "/hz")
    }

    return NewLifecycle(context.Background(), cfg, deps), alerts
}
```

## 2. Drive the boot path (Test 1 — happy path)

```go
func TestLifecycle_BootSubmitsClaim(t *testing.T) {
    lc, alerts := newTestLifecycle(t)
    ctx, cancel := context.WithCancel(context.Background())
    t.Cleanup(cancel)

    // Run in a goroutine; cancel after the boot path completes.
    runDone := make(chan error, 1)
    go func() { runDone <- lc.Run(ctx) }()

    // Wait for StateRunning via bounded poll (NO time.Sleep).
    require.Eventually(t, func() bool {
        return lc.store.Snapshot().State == StateRunning
    }, 5*time.Second, 10*time.Millisecond)

    // Assert: one /claim call recorded by the mock vault.
    require.Equal(t, 1, mockVault.ClaimCount())
    // Assert: zero alerts on happy path.
    require.Equal(t, 0, len(alerts.events))

    cancel()
    require.NoError(t, <-runDone)
}
```

## 3. Inject a controllable triple

```go
type controllableValidator struct {
    failures map[string]error // scope name → error to return; missing → nil
}
func (c *controllableValidator) Validate(_ context.Context, scope string, _ *securebytes.SecureBytes) error {
    if err, found := c.failures[scope]; found { return err }
    return nil
}

func TestLifecycle_ValidatorFailureBlocksChildStart(t *testing.T) {
    lc, alerts := newTestLifecycle(t)
    lc.deps.Validators = map[string]Validator{
        "ANTHROPIC_API_KEY": &controllableValidator{
            failures: map[string]error{"ANTHROPIC_API_KEY": errors.New("401 stale")},
        },
    }
    // ... drive run, assert StateAwaitingApproval, assert one
    //     AlertClassValidatorFailure event, assert child PID is 0 ...
}
```

## 4. Boot probes — controllable

```go
func TestLifecycle_BootRetryTimeoutExhausted(t *testing.T) {
    lc, _ := newTestLifecycle(t)
    lc.deps.TailscaleProbe = func(context.Context) error {
        return errors.New("tailscale not ready")
    }
    // Override BootRetryTimeout in cfg to a tiny window so the test runs fast.

    ctx := context.Background()
    err := lc.Run(ctx)
    require.ErrorIs(t, err, ErrBootTimeout)
    // Assert: ActionSupervisorBootTimeout in audit chain.
}
```

## 5. Refresher tick — controllable (without `Refresher.Run`)

The Refresher's tick loop is owned by SDD-21 and has its own test
coverage. For the orchestrator's refresh-window behaviour, the test
calls the orchestrator's internal `claimRefreshLoop` directly via the
`refreshTickCh` channel — bypassing the Refresher's tick timing.
Tests live in `package supervise` so they can post on the channel.

```go
func TestLifecycle_RefresherTickSubmitsFreshClaim(t *testing.T) {
    lc, _ := newTestLifecycle(t)
    // ... drive run to StateRunning ...

    // Trigger a refresh tick:
    lc.refreshTickCh <- struct{}{}

    // Wait for mainLoop to consume and update the store:
    require.Eventually(t, func() bool {
        return mockVault.ClaimCount() == 2
    }, 5*time.Second, 10*time.Millisecond)

    // Assert: child PID unchanged.
    require.Equal(t, originalPID, lc.child.PID())
}
```

## 6. Running the suites

```bash
# Unit (fast, race-clean, default invocation):
magex test:race

# Coverage report (≥85% gate on lifecycle*.go):
magex test:coverage

# Integration (slower, requires httptest server scaffolding):
magex test:race -tags=integration

# Full pre-commit gates (Constitution §Development Workflow):
magex format:fix && magex lint && magex test:race
```

## 7. Cli shim authoring (~80 LOC target)

After SDD-24 lands, `internal/cli/supervise.go` shrinks from ~335 LOC
to ~80 LOC:

```go
func runSupervise(cmd *cobra.Command, configPath string, flags superviseFlags) error {
    stderr := cmd.ErrOrStderr()

    cfg, err := config.Load(cmd.Context(), configPath)
    if err != nil { printSuperviseErr(stderr, err); return err }

    if err := applyFlagOverrides(cfg, flags); err != nil { /* dry-run branch unchanged */ }
    if flags.dryRun { return renderDryRun(cmd, cfg) }

    rootCtx, rootCancel := signal.NotifyContext(cmd.Context(),
        syscall.SIGTERM, syscall.SIGINT)
    defer rootCancel()

    pidfile, err := supervise.AcquirePidFile(cfg.PIDFile)
    if err != nil { /* duplicate-supervisor mapping unchanged */ }
    defer func() { _ = pidfile.Release() }()

    deps := supervise.Deps{
        Logger:          newOperatorLogger(cmd, flags),
        HTTPClient:      &http.Client{Timeout: 30 * time.Second},
        Clock:           realClock{},
        ClaimSigningKey: loadClientSigningKey(cfg),
        DecryptKey:      loadEphemeralECIESKey(),
        AuditWriter:     loadAuditWriter(cfg),
        PidFile:         pidfile,
        // Validators / Alerts / Watchdog left nil — SDD-26/27/28 wire later.
    }
    lc := supervise.NewLifecycle(rootCtx, cfg, deps)
    return lc.Run(rootCtx)
}
```

The `loadClientSigningKey` / `loadEphemeralECIESKey` / `loadAuditWriter`
helpers are existing helpers in the cli package or new thin wrappers
around `internal/keys`, `internal/transport/ecies`, and
`internal/audit`.

## 8. What SDD-25 (lifecycle harness) needs after this chunk

Once SDD-24 ships:

- The 14 SDD-25 scenarios that depend on the orchestrator
  (`02 DaemonBootstrap` through `15 LogPatternAlert`, excluding
  Scenario 1 / Scenario 10 Interactive) compose the orchestrator as a
  black box via `supervise.NewLifecycle` + `supervise.Lifecycle.Run`.
- SDD-25's existing `tests/integration/harness/supervisor.go` is the
  only place that imports `supervise.Lifecycle`; the rest of the
  harness composes against the orchestrator only via channels (status
  socket, Discord stub, validator HTTP mocks).
- The pause banner at the top of
  `specs/025-lifecycle-harness/tasks.md` is removed at SDD-24
  implement-phase completion. SDD-25 implement resumes from T001.

## 9. Known invariants for SDD-26 / SDD-27 / SDD-28 maintainers

| Chunk | Surface SDD-24 has locked | What you implement |
|-------|---------------------------|---------------------|
| SDD-26 (validators) | `Validator` interface, `Deps.Validators map[string]Validator` | concrete validators that implement `Validator`; register them in cli shim |
| SDD-27 (watchdog) | `Watchdog` interface, `Deps.Watchdog`, line-splitting writer wiring | concrete log-pattern matcher; emits `AlertClassLogPatternMatch` via the same `Deps.Alerts` handle |
| SDD-28 (alerts) | `Alerts` interface, `AlertClass` enum (LOCKED at 10 values), `AlertPayload` struct | concrete renderer that turns each `(class, payload)` into a Discord DM / channel post |

None of these chunks may add fields to `Deps`, mutate the `AlertClass`
enum, or change any orchestrator behaviour. They fill the interfaces
SDD-24 defines.
