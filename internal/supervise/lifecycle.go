// SDD-24 — Supervisor orchestration glue: production orchestrator.
//
// lifecycle.go declares the Lifecycle struct, Deps record, the single-shot
// NewLifecycle constructor, and the top-level Run dispatcher. The four
// sibling files (lifecycle_boot.go, lifecycle_child.go, lifecycle_refresh.go,
// lifecycle_audit.go) house the specialized helpers.
//
// State-table reasoning is owned by SDD-19 (state.go); exit-78 reasoning is
// owned by SDD-20 (child.go). This file references those packages' constants
// and APIs only — it never inlines a state-string literal, never inlines the
// `78` exit-code literal, and never branches on runtime.GOOS (spec FR-026-023).
//
// Goroutine inventory (Constitution IX — owner + ctx + termination + recover):
//   1. StatusServer.Run  (carried from SDD-22) — Lifecycle.wg
//   2. Refresher.Run     (carried from SDD-21) — Lifecycle.wg
//   3. mainLoop          (this file) — dispatches childExit / refreshDone /
//                                       refreshVerb / ctx.Done
//   4. childWaitLoop     (lifecycle_child.go) — invokes Child.Wait
//   5. claimRefreshLoop  (lifecycle_refresh.go) — performs the refresh /claim

package supervise

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/supervise/config"
)

// ErrLifecycleAlreadyRan is returned by Run on a second invocation of the
// same Lifecycle. Compare via errors.Is.
var ErrLifecycleAlreadyRan = errors.New("supervise: lifecycle already ran")

// ErrValidatorFailed is the sentinel orchestrator emits (wrapped with the
// scope name in the message) on any Validator.Validate non-nil return.
// Compare via errors.Is.
var ErrValidatorFailed = errors.New("supervise: validator failed")

// ErrRefillFailedPostRunning is the sentinel orchestrator emits (wrapped)
// when Refiller.Refill returns any non-ErrJTIUnknown error during a
// post-running silent refill. Compare via errors.Is.
var ErrRefillFailedPostRunning = errors.New("supervise: post-running refill failed")

// ErrClaimDenied is the sentinel emitted when /claim returns a terminal
// 4xx (denied / bad_signature / 401). Compare via errors.Is.
var ErrClaimDenied = errors.New("supervise: claim denied (terminal)")

// Lifecycle constants — see data-model.md §10. Package-level const blocks
// are not "mutable vars" per Constitution IX (R-005).

// bootBackoffInitial is the first interval before the first boot retry.
const bootBackoffInitial = 500 * time.Millisecond

// bootBackoffMultiplier doubles each subsequent interval.
const bootBackoffMultiplier = 2.0

// bootBackoffCap caps any single backoff interval (jittered).
const bootBackoffCap = 30 * time.Second

// bootProbeTimeout is the per-attempt HTTP/probe timeout (≤ 2s per A-026-2).
const bootProbeTimeout = 2 * time.Second

// shutdownGraceTimeout is the SIGTERM honor window before SIGKILL escalation.
const shutdownGraceTimeout = 10 * time.Second

// shutdownHardCeiling is the total Run-exit budget after ctx cancel.
const shutdownHardCeiling = 15 * time.Second

// stderrLineCap is the max bytes per emitted watchdog line; matches SDD-20.
const stderrLineCap = 64 * 1024

// Deps carries every injected dependency NewLifecycle requires. Nil fields
// with documented defaults remain nil-safe — the constructor wires defaults.
type Deps struct {
	// Logger is the operational logger. MUST be non-nil.
	Logger *slog.Logger
	// HTTPClient is the outgoing client toward the vault server. MUST be non-nil.
	HTTPClient *http.Client
	// Clock is the wall-clock seam (existing SDD-19 interface). MUST be non-nil.
	Clock Clock
	// ClaimSigningKey is the BIP32-derived ECDSA secp256k1 client key used to
	// sign /claim payloads (SDD-08). MUST be non-nil.
	ClaimSigningKey *ecdsa.PrivateKey
	// DecryptKey is the ephemeral ECIES private key the Refiller decrypts
	// per-scope bodies against. MUST be non-nil.
	DecryptKey *ecdsa.PrivateKey
	// AuditWriter emits supervisor-scope audit events. MUST be non-nil.
	AuditWriter audit.Writer
	// PidFile is the already-acquired exclusive lock the cli shim hands the
	// orchestrator. MUST be non-nil.
	PidFile *PidFile

	// Validators is keyed by scope name. nil map / missing key → no-op validator
	// for that scope (the call still runs and is logged).
	Validators map[string]Validator
	// Alerts sinks operator-visible alerts. nil → no-op default discards.
	Alerts Alerts
	// Watchdog observes child stderr lines. nil → no-op default discards.
	Watchdog Watchdog

	// TailscaleProbe returns nil when at least one Tailscale interface is
	// present on the host, else a typed error. nil → default impl wired by
	// the constructor (returns nil always).
	TailscaleProbe func(ctx context.Context) error
	// VaultHzProbe returns nil when GET <serverURL>/hz returns 200 within
	// bootProbeTimeout. nil → default impl wired by the constructor.
	VaultHzProbe func(ctx context.Context, serverURL string) error
	// MachineName, EphemeralPubKeyHex, ClientKeyFingerprint, RandReader,
	// NonceFn, RequestIDFn, NowFn are caller-provided seams used to build a
	// /claim payload. The defaults wired by NewLifecycle are randomized; tests
	// inject deterministic stubs.
	MachineName          string
	EphemeralPubKeyHex   string
	ClientKeyFingerprint string
	NowFn                func() time.Time
	NonceFn              func() string
	RequestIDFn          func() string
}

// statusInputs is the unexported StatusInputs implementation lifted out of
// internal/cli at SDD-24. Eight atomic fields read concurrently from the
// status server's per-connection handler goroutines.
type statusInputs struct {
	name             string
	childStartedAt   atomic.Pointer[time.Time]
	lastAuthFail     atomic.Pointer[time.Time]
	scopeHealthy     atomic.Pointer[[]string]
	scopeStale       atomic.Pointer[[]string]
	sessionExp       atomic.Pointer[time.Time]
	sessionJTI       atomic.Pointer[string]
	refreshNext      atomic.Pointer[time.Time]
	restartCount     atomic.Uint64
	discordConnected atomic.Bool
}

// Name returns the supervisor's configured name.
func (o *statusInputs) Name() string { return o.name }

// SessionExpiresAt returns the cached JWT expiry instant.
func (o *statusInputs) SessionExpiresAt() time.Time {
	if p := o.sessionExp.Load(); p != nil {
		return *p
	}
	return time.Time{}
}

// SessionJTI returns the current supervisor session identifier.
func (o *statusInputs) SessionJTI() string {
	if p := o.sessionJTI.Load(); p != nil {
		return *p
	}
	return ""
}

// RestartCount returns the number of successful child restarts in this process.
func (o *statusInputs) RestartCount() uint64 {
	return o.restartCount.Load()
}

// RefreshWindowNext returns the next refresh-window instant.
func (o *statusInputs) RefreshWindowNext() time.Time {
	if p := o.refreshNext.Load(); p != nil {
		return *p
	}
	return time.Time{}
}

// ScopeHealthy returns the latest healthy-scope list.
func (o *statusInputs) ScopeHealthy() []string {
	if p := o.scopeHealthy.Load(); p != nil {
		return *p
	}
	return nil
}

// ScopeStale returns the latest stale-scope list.
func (o *statusInputs) ScopeStale() []string {
	if p := o.scopeStale.Load(); p != nil {
		return *p
	}
	return nil
}

// LastAuthFailure returns the timestamp of the last auth-failure transition.
func (o *statusInputs) LastAuthFailure() *time.Time {
	return o.lastAuthFail.Load()
}

// ChildUptime returns the duration since the current child was started.
func (o *statusInputs) ChildUptime() time.Duration {
	p := o.childStartedAt.Load()
	if p == nil || p.IsZero() {
		return 0
	}
	return time.Since(*p)
}

// DiscordConnected returns whether the Discord transport is connected.
func (o *statusInputs) DiscordConnected() bool {
	return o.discordConnected.Load()
}

// Compile-time guard: statusInputs implements StatusInputs.
var _ StatusInputs = (*statusInputs)(nil)

// childExit is the message childWaitLoop emits per child instance.
type childExit struct {
	code   int
	signal syscall.Signal
	err    error
}

// refreshResult is the message claimRefreshLoop emits per swap attempt.
type refreshResult struct {
	err  error
	deny bool
}

// refreshVerb is the message the status-socket refresh handler posts when
// in awaiting-approval / running / grace-restart state.
type refreshVerb struct {
	ack chan error
}

// Lifecycle is the supervisor orchestrator. Construct via NewLifecycle;
// drive via Run(ctx). Single-shot — calling Run twice returns
// ErrLifecycleAlreadyRan.
type Lifecycle struct {
	deps   Deps
	config *config.Supervisor

	store        *Store
	grace        *Grace
	refiller     *Refiller
	refresher    *Refresher
	statusServer *StatusServer
	coalescer    *refreshCoalescer
	inputs       *statusInputs

	childExitCh   chan childExit
	refreshTickCh chan struct{}
	refreshDoneCh chan refreshResult
	refreshVerbCh chan refreshVerb

	runOnce sync.Once
	ran     atomic.Bool
	wg      sync.WaitGroup

	childMu      sync.Mutex
	child        *Child
	childStarted time.Time

	// sessionExp tracks the orchestrator's view of the issued JWT expiry.
	sessionMu  sync.Mutex
	sessionExp time.Time
	sessionJTI string

	// childRunning is set by initialRefillAndStart / silentRefillAndRestart
	// when a child is alive, cleared by mainLoop's childExit dispatch.
	childRunning atomic.Bool
}

// refreshFlight is one in-flight refresh attempt observed by every
// concurrent caller of refreshCoalescer.Handle.
type refreshFlight struct {
	done chan struct{}
	err  error
}

// errRefreshPerformNotWired is the sentinel surfaced by refreshCoalescer.Handle
// when no perform closure has been wired.
var errRefreshPerformNotWired = errors.New("supervise: refresh perform not wired")

// refreshCoalescer is the single-flight gate for `hush client refresh`
// callbacks invoked via the status socket. Mirrors the original cli-package
// coalescer; lifted into the orchestrator at SDD-24.
type refreshCoalescer struct {
	mu       sync.Mutex
	inflight *refreshFlight
	perform  func(ctx context.Context) error
}

// Handle is the refreshHandler wired into the StatusServer. Returns the
// terminal err observed by every caller of the in-flight refresh.
func (c *refreshCoalescer) Handle(ctx context.Context) error {
	c.mu.Lock()
	if c.inflight != nil {
		flight := c.inflight
		c.mu.Unlock()
		select {
		case <-flight.done:
			return flight.err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	flight := &refreshFlight{done: make(chan struct{})}
	c.inflight = flight
	perform := c.perform
	c.mu.Unlock()

	var err error
	if perform == nil {
		err = errRefreshPerformNotWired
	} else {
		err = perform(ctx)
	}
	c.mu.Lock()
	flight.err = err
	close(flight.done)
	c.inflight = nil
	c.mu.Unlock()
	return err
}

// NewLifecycle constructs a Lifecycle. Validates required Deps fields and
// panics on nil for any required dependency (Constitution IX startup-wiring
// exemption). Optional Deps fields receive their no-op defaults when nil.
//
// NewLifecycle constructs the SDD-19..22 primitives internally and wires
// post-construction seams (Refiller.attach, StatusServer.AttachStatusInputs,
// StatusServer.AttachRefreshHandler) before returning.
func NewLifecycle(ctx context.Context, cfg *config.Supervisor, deps Deps) *Lifecycle {
	if cfg == nil {
		panic("supervise: NewLifecycle requires a non-nil *config.Supervisor")
	}
	if deps.Logger == nil {
		panic("supervise: NewLifecycle requires Deps.Logger")
	}
	if deps.HTTPClient == nil {
		panic("supervise: NewLifecycle requires Deps.HTTPClient")
	}
	if deps.Clock == nil {
		panic("supervise: NewLifecycle requires Deps.Clock")
	}
	if deps.ClaimSigningKey == nil {
		panic("supervise: NewLifecycle requires Deps.ClaimSigningKey")
	}
	if deps.DecryptKey == nil {
		panic("supervise: NewLifecycle requires Deps.DecryptKey")
	}
	if deps.AuditWriter == nil {
		panic("supervise: NewLifecycle requires Deps.AuditWriter")
	}
	if deps.PidFile == nil {
		panic("supervise: NewLifecycle requires Deps.PidFile")
	}

	deps = wireDepsDefaults(deps)

	lc := &Lifecycle{
		deps:          deps,
		config:        cfg,
		childExitCh:   make(chan childExit, 1),
		refreshTickCh: make(chan struct{}, 1),
		refreshDoneCh: make(chan refreshResult, 1),
		refreshVerbCh: make(chan refreshVerb, 1),
		inputs:        &statusInputs{name: cfg.Name},
	}
	lc.inputs.discordConnected.Store(true)

	lc.store = NewStore(ctx, deps.Clock)
	lc.grace = NewGrace(cfg.CacheGraceTTL, cfg.CacheSecretsForRestart)
	lc.refiller = NewRefiller(deps.HTTPClient, lc.store, deps.Logger)
	lc.refiller.attach(lc.grace, deps.DecryptKey, cfg.ServerURL)

	lc.statusServer = NewStatusServer(cfg.StatusSocket, lc.store, deps.Logger)
	lc.statusServer.AttachStatusInputs(lc.inputs)

	lc.coalescer = &refreshCoalescer{}
	lc.coalescer.perform = func(ctx context.Context) error {
		// Coalesced refresh path = silent refill (validators + restart) under
		// the running session. Used by status-socket refresh in running /
		// grace-restart and by Refresher window-tick handoff.
		return lc.silentRefillAndRestart(ctx)
	}

	// Refresher's refill callback only NUDGES the refresh tick; the actual
	// /claim swap happens inside claimRefreshLoop so the tick anchor stays
	// accurate (per R-5).
	lc.refresher = NewRefresher(cfg.RefreshWindow, cfg.RequestedTTL, func(_ context.Context) error {
		select {
		case lc.refreshTickCh <- struct{}{}:
		default:
		}
		return nil
	}, deps.Logger)

	// AttachRefreshHandler binds the status-socket refresh verb to a closure
	// that posts on refreshVerbCh and blocks on ack. State-conditional
	// dispatch lives in lifecycle_refresh.go.
	lc.statusServer.AttachRefreshHandler(lc.handleStatusRefreshVerb)

	return lc
}

// wireDepsDefaults fills in the no-op / default seams for any nil-safe
// Deps field. Required fields are pre-validated.
//
//nolint:gocyclo // 11 sequential nil-checks; each branch wires one seam
func wireDepsDefaults(d Deps) Deps {
	if d.Alerts == nil {
		d.Alerts = noopAlerts{}
	}
	if d.Watchdog == nil {
		d.Watchdog = noopWatchdog{}
	}
	if d.TailscaleProbe == nil {
		d.TailscaleProbe = func(context.Context) error { return nil }
	}
	if d.VaultHzProbe == nil {
		d.VaultHzProbe = defaultVaultHzProbe(d.HTTPClient)
	}
	if d.NowFn == nil {
		d.NowFn = time.Now
	}
	if d.NonceFn == nil {
		d.NonceFn = defaultNonceFn
	}
	if d.RequestIDFn == nil {
		d.RequestIDFn = defaultRequestIDFn
	}
	if d.MachineName == "" {
		d.MachineName = "supervisor"
	}
	if d.EphemeralPubKeyHex == "" {
		d.EphemeralPubKeyHex = compressedEphemeralPubHex(&d.DecryptKey.PublicKey)
	}
	if d.ClientKeyFingerprint == "" {
		d.ClientKeyFingerprint = clientKeyFingerprintHex(&d.ClaimSigningKey.PublicKey)
	}
	return d
}

// Run drives the supervisor lifecycle. Blocks until ctx is cancelled OR a
// terminal failure (boot timeout, vault rejects /claim with a terminal 4xx).
// Returns nil on clean ctx-cancelled shutdown; returns a wrapped error on
// terminal failure. Single-shot.
func (l *Lifecycle) Run(ctx context.Context) error {
	if l.ran.Swap(true) {
		return ErrLifecycleAlreadyRan
	}
	var runErr error
	l.runOnce.Do(func() { runErr = l.run(ctx) })
	return runErr
}

// run is the top-level dispatcher. Spawns the StatusServer + Refresher,
// drives the boot path, spawns the goroutines, runs mainLoop, then runs
// the shutdown sequence.
func (l *Lifecycle) run(parentCtx context.Context) error {
	// Derive a child ctx so terminal failures can wake background goroutines.
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				l.deps.Logger.Error("supervise: statusServer goroutine panic", slog.Any("recover", r))
			}
		}()
		_ = l.statusServer.Run(ctx)
	}()

	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				l.deps.Logger.Error("supervise: refresher goroutine panic", slog.Any("recover", r))
			}
		}()
		_ = l.refresher.Run(ctx)
	}()

	// Claim refresh loop — handles refresher tick → fresh /claim.
	l.wg.Add(1)
	go l.claimRefreshLoop(ctx)

	// Boot path: probes + /claim + initial refill + validators + child start.
	if err := l.boot(ctx); err != nil {
		// Boot terminal failure — cancel the derived ctx so the bg goroutines
		// exit, then run shutdown which waits on them.
		cancel()
		l.runShutdown(ctx)
		return err
	}

	// mainLoop dispatches childExit / refreshDone / refreshVerb / ctx.Done.
	l.mainLoop(ctx)
	l.runShutdown(ctx)
	return nil
}

// mainLoop is the orchestrator's central dispatcher. It runs until ctx is
// cancelled or a terminal stop-event fires. Per spec FR-026-009, FR-026-011,
// FR-026-012, and Plan §10 the four arms are: childExit, refreshDone,
// refreshVerb, ctx.Done.
func (l *Lifecycle) mainLoop(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			l.deps.Logger.Error("supervise: mainLoop panic", slog.Any("recover", r))
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case exit := <-l.childExitCh:
			l.dispatchChildExit(ctx, exit)
		case res := <-l.refreshDoneCh:
			l.dispatchRefreshResult(ctx, res)
		case verb := <-l.refreshVerbCh:
			l.dispatchRefreshVerb(ctx, verb)
		}
	}
}

// runShutdown executes the SIGTERM → grace → SIGKILL → wg.Wait sequence
// per Plan §R-4 and FR-026-019. Pidfile release is owned by the cli shim's
// defer.
func (l *Lifecycle) runShutdown(parentCtx context.Context) {
	_ = parentCtx
	hardDeadline := time.Now().Add(shutdownHardCeiling)

	// Forward SIGTERM to the child if one is alive.
	l.childMu.Lock()
	child := l.child
	l.childMu.Unlock()
	if child != nil {
		_ = child.Forward(syscall.SIGTERM)
	}

	// Grace timer: 10s for the child to exit cleanly.
	graceTimer := time.NewTimer(shutdownGraceTimeout)
	defer graceTimer.Stop()

	exited := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(exited)
	}()

	select {
	case <-exited:
		return
	case <-graceTimer.C:
		// Escalate.
	}

	l.childMu.Lock()
	child = l.child
	l.childMu.Unlock()
	if child != nil {
		_ = child.Forward(syscall.SIGKILL)
	}

	// Hard ceiling.
	remain := time.Until(hardDeadline)
	if remain <= 0 {
		remain = 5 * time.Second
	}
	hardTimer := time.NewTimer(remain)
	defer hardTimer.Stop()
	select {
	case <-exited:
		return
	case <-hardTimer.C:
		l.deps.Logger.Warn("supervise: shutdown ceiling exceeded; goroutines may leak")
		return
	}
}

// transition is a thin convenience wrapper that logs and swallows
// ErrInvalidTransition (the orchestrator is the only state.Store driver in
// production and any invalid transition is a programmer error).
func (l *Lifecycle) transition(ctx context.Context, ev Event) {
	if err := l.store.Transition(ctx, ev); err != nil {
		l.deps.Logger.Warn("supervise: invalid transition", slog.String("event", string(ev)), slog.Any("err", err))
	}
}
