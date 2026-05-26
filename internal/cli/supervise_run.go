package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/supervise"
	superviseconfig "github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/internal/supervise/validators"
	"github.com/mrz1836/hush/internal/supervise/watchdog"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// errSuperviseClientKeyLength is returned when the keychain payload for
// the configured machine index is not 32 bytes.
var errSuperviseClientKeyLength = errors.New("hush: supervise: client key length")

// superviseDepsSeam is the test-injectable factory for the dependencies
// runSupervise needs to drive a real Lifecycle. Production wiring uses
// productionSuperviseDeps; tests swap this pointer with a fake-keychain
// closure to bypass real Keychain ACL lookups.
//
//nolint:gochecknoglobals // single-purpose test seam, mirrors requestDeps pattern
var superviseDepsSeam = productionSuperviseDeps

// superviseRuntimeDeps carries the externally-supplied seams runSupervise
// needs in addition to the Lifecycle's Deps. Held narrowly so tests need
// only mock a handful of boundaries.
type superviseRuntimeDeps struct {
	keychain   keychain.Keychain
	httpClient *http.Client
}

// productionSuperviseDeps returns the locked production wiring.
func productionSuperviseDeps() (superviseRuntimeDeps, error) {
	kc, err := keychain.New(nil)
	if err != nil {
		return superviseRuntimeDeps{}, fmt.Errorf("hush: supervise: keychain: %w", err)
	}
	return superviseRuntimeDeps{
		keychain: kc,
		httpClient: &http.Client{
			// 15 minutes covers a fully humane operator reaction window
			// (server-side claim_approval_timeout caps at 10m) plus
			// headroom for slow Discord round-trips. Per-request ctx
			// further bounds individual calls; this is just the
			// absolute ceiling so a wedged remote doesn't pin a goroutine
			// indefinitely.
			Timeout: 15 * time.Minute,
			Transport: &http.Transport{
				DisableKeepAlives:   true,
				MaxIdleConnsPerHost: 1,
			},
		},
	}, nil
}

// loadSupervisorClientKey loads the per-machine client signing key from
// the keychain and reconstitutes a secp256k1 *ecdsa.PrivateKey. Mirrors
// retrieveClientKey in request.go but takes a bare keychain.Keychain so
// it works with the supervise dep seam.
func loadSupervisorClientKey(ctx context.Context, kc keychain.Keychain, machineIndex uint32, clientKeyFile string) (*ecdsa.PrivateKey, error) {
	if clientKeyFile != "" {
		return retrieveClientKeyFromFile(clientKeyFile)
	}
	account := fmt.Sprintf("machine-%d", machineIndex)
	sb, err := kc.Retrieve(ctx, kcServiceClient, account)
	if err != nil {
		return nil, fmt.Errorf("hush: supervise: load client key: %w", err)
	}
	defer func() { _ = sb.Destroy() }()

	var (
		priv *ecdsa.PrivateKey
		uErr error
	)
	if useErr := sb.Use(func(b []byte) {
		scalar, decErr := keychain.DecodeFixedBinary(b, 32)
		if decErr != nil {
			uErr = fmt.Errorf("%w: %d, want 32", errSuperviseClientKeyLength, len(b))
			return
		}
		k := secp256k1.PrivKeyFromBytes(scalar)
		priv = k.ToECDSA()
		for i := range scalar {
			scalar[i] = 0
		}
	}); useErr != nil {
		return nil, fmt.Errorf("hush: supervise: read client key: %w", useErr)
	}
	if uErr != nil {
		return nil, uErr
	}
	return priv, nil
}

// generateSuperviseEphemeralKey produces a fresh secp256k1 keypair used
// by the orchestrator as the ECIES decrypt key.
func generateSuperviseEphemeralKey() (*ecdsa.PrivateKey, error) {
	k, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("hush: supervise: generate ephemeral key: %w", err)
	}
	return k.ToECDSA(), nil
}

// deriveAuditSigningKey returns a fresh ECDSA secp256k1 key used by the
// audit.Writer to sign chain events. Each supervisor process gets its
// own signing key — audit verification only checks chain continuity, not
// long-term signer identity.
func deriveAuditSigningKey() (*ecdsa.PrivateKey, error) {
	k, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("hush: supervise: derive audit key: %w", err)
	}
	return k.ToECDSA(), nil
}

// runLifecycle wires the supervisor Lifecycle and blocks until it exits.
// All key handles created here are zeroed before return via SecureBytes
// destruction; the ecdsa.PrivateKey scalars are released when GC runs.
//
//nolint:cyclop,contextcheck // sequential dependency-wiring; audit drain outlives rootCtx by design (mirrors serve.go)
func runLifecycle(rootCtx context.Context, cfg *superviseconfig.Supervisor, pidfile *supervise.PidFile, logger *slog.Logger) error {
	logSupervisorRuntimeLimitations(logger)
	rt, err := superviseDepsSeam()
	if err != nil {
		return err
	}

	signKey, err := loadSupervisorClientKey(rootCtx, rt.keychain, cfg.ClientMachineIndex, cfg.ClientKeyFile)
	if err != nil {
		return err
	}
	decryptKey, err := generateSuperviseEphemeralKey()
	if err != nil {
		return err
	}
	auditKey, err := deriveAuditSigningKey()
	if err != nil {
		return err
	}

	// The audit writer outlives rootCtx by design — its Run goroutine
	// must drain in-flight events after rootCtx (the supervisor ctx) is
	// cancelled. We give it a derived background ctx so the drain step
	// in writerImpl.Run completes before this defer returns. Mirrors the
	// pattern in internal/cli/serve.go:229.
	auditWriter, err := audit.NewWriter(rootCtx, cfg.AuditLog, auditKey, nil, logger)
	if err != nil {
		return fmt.Errorf("hush: supervise: audit writer: %w", err)
	}
	auditCtx, auditCancel := context.WithCancel(context.Background())
	defer auditCancel()
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		defer func() {
			if r := recover(); r != nil {
				logger.Error("hush: supervise: audit drain goroutine panic", slog.Any("recover", r))
			}
		}()
		_ = auditWriter.Run(auditCtx)
	}()
	defer func() {
		auditCancel()
		<-auditDone
	}()

	deps := supervise.Deps{
		Logger:          logger,
		HTTPClient:      rt.httpClient,
		Clock:           realClock{},
		ClaimSigningKey: signKey,
		DecryptKey:      decryptKey,
		AuditWriter:     auditWriter,
		PidFile:         pidfile,
		Validators:      buildSuperviseValidators(cfg),
		Alerts:          loggingAlerts{logger: logger},
		Watchdog:        startSuperviseWatchdog(rootCtx, cfg, logger, loggingAlerts{logger: logger}),
		NowFn:           time.Now,
		NonceFn:         defaultNonceFn,
		RequestIDFn:     defaultRequestIDFn,
	}

	lc := supervise.NewLifecycle(rootCtx, cfg, deps)

	// When the supervisor config opts into [child.handoff] (HTTP-proxy
	// reload-eligibility), the CLI owns the proxy listener's lifetime: it
	// binds the public listen_addr before the first child accepts traffic,
	// stays bound across reloads, and is gracefully stopped when the
	// supervisor exits. Without this, [child.handoff] config is inert at
	// runtime: SwapChild has no proxy to swap, and the public port is
	// served by nothing — which is exactly the trap the 2026-05-25 16:34
	// cutover attempt fell into before this fix landed.
	proxy, err := startProxyIfHandoffConfigured(rootCtx, cfg, lc, logger)
	if err != nil {
		// runLifecycle's caller (runSupervise) already logs at error
		// level and maps the return to printSuperviseErr + non-zero exit.
		// Returning here means the lifecycle is never run — operators see
		// the bind failure at boot, not at first reload.
		return err
	}
	if proxy != nil {
		defer stopProxyGracefully(proxy, logger)
	}

	// Wire the reload handler so `hush supervise reload <toml>` from
	// another process can actually trigger SwapChild. Without this the
	// SDK request comes in over the status socket and the server
	// returns `reload handler not wired` — exactly the trap the
	// 2026-05-25 17:02 reload test hit before this wiring landed.
	wireReloadHandlerIfHandoffConfigured(cfg, lc, logger)

	return lc.Run(rootCtx)
}

// wireReloadHandlerIfHandoffConfigured registers a handler on the
// Lifecycle's status socket that dispatches the SDK's Reload SDK call
// to SwapChild. The handler ignores ReloadRequest.ConfigPath in v1 —
// the supervisor uses its already-loaded config for the actual swap;
// the operator-supplied path is for audit attribution only.
//
// No-op when [child.handoff] is not http-proxy: SwapChild itself would
// return ErrSwapNotEligible for non-handoff configs, but wiring the
// handler at all is misleading (it implies reload is supported).
//
// Safe to call exactly once per Lifecycle. AttachReloadHandler panics
// on a second call; this helper is invoked from runLifecycle which
// runs once per supervisor process.
func wireReloadHandlerIfHandoffConfigured(
	cfg *superviseconfig.Supervisor,
	attacher handoffAttacher,
	logger *slog.Logger,
) {
	if cfg.Child.Handoff == nil || cfg.Child.Handoff.Mode != superviseconfig.HandoffModeHTTPProxy {
		return
	}
	attacher.AttachReloadHandler(func(ctx context.Context, _ supervise.ReloadRequest) (supervise.SwapResult, error) {
		return attacher.SwapChild(ctx)
	})
	logger.Info("supervise: reload handler wired")
}

// proxyShutdownTimeout bounds the post-Run graceful Stop on the reload
// proxy. 5s is comfortable for in-flight requests to drain — the proxy's
// reverse-proxy transport already has its own per-request timeouts and the
// public listener is dropped immediately on Stop, so subsequent dials get
// connect-refused (the cleanest signal to upstream load balancers that
// the supervisor has exited).
const proxyShutdownTimeout = 5 * time.Second

// handoffAttacher is the narrow seam the reload-wiring helpers use
// against the Lifecycle. *supervise.Lifecycle satisfies it; tests can
// substitute an in-memory stub without instantiating a full Lifecycle
// (which requires many non-nil deps unrelated to proxy + reload wiring).
type handoffAttacher interface {
	AttachProxy(p *supervise.Proxy)
	AttachReloadHandler(handler func(ctx context.Context, req supervise.ReloadRequest) (supervise.SwapResult, error))
	SwapChild(ctx context.Context) (supervise.SwapResult, error)
}

// startProxyIfHandoffConfigured is the CLI-side wiring that closes the
// gap left by hush PR #48 (zero-downtime reload): the Lifecycle exposes
// AttachProxy + the SwapChild verb consumes an attached proxy, but the
// CLI runSupervise path never instantiated one. Returns (nil, nil) when
// the config has not opted into handoff — non-reload-eligible configs
// see the exact runtime shape they always did. When handoff IS opted
// into, this constructs the Proxy, calls Start (binding listen_addr),
// and attaches it to the Lifecycle so SwapChild can find it.
//
// On any error this path returns wrapped error (caller maps to
// printSuperviseErr + the standard non-zero exit). The Lifecycle is
// never run when proxy startup fails — the CLI exits before the child
// is spawned so an operator-fixable error (e.g. listen_addr port in use)
// surfaces immediately at boot rather than at first reload.
func startProxyIfHandoffConfigured(
	ctx context.Context,
	cfg *superviseconfig.Supervisor,
	attacher handoffAttacher,
	logger *slog.Logger,
) (*supervise.Proxy, error) {
	if cfg.Child.Handoff == nil {
		return nil, nil //nolint:nilnil // absent handoff is the expected "no proxy needed" signal
	}
	if cfg.Child.Handoff.Mode != superviseconfig.HandoffModeHTTPProxy {
		// The config loader already rejects unknown modes; defensive guard
		// for future modes (e.g. socket-activation) that don't route
		// through the HTTP proxy.
		return nil, nil //nolint:nilnil // absent handoff is the expected "no proxy needed" signal
	}
	listenAddr := cfg.Child.Handoff.ListenAddr
	proxy := supervise.NewProxy(listenAddr, logger)
	if err := proxy.Start(ctx); err != nil {
		return nil, fmt.Errorf("supervise: proxy start at %s: %w", listenAddr, err)
	}
	attacher.AttachProxy(proxy)
	logger.Info(
		"supervise: reload proxy bound",
		slog.String("listen_addr", listenAddr),
		slog.String("mode", cfg.Child.Handoff.Mode),
	)
	return proxy, nil
}

// stopProxyGracefully is the runSupervise defer body that idempotently
// stops the reload proxy after lc.Run returns (success, error, signal,
// or panic-unwind). The shutdown context is decoupled from rootCtx
// because rootCtx is already cancelled when lc.Run returns on a signal
// — using it for the shutdown would skip the graceful drain.
func stopProxyGracefully(proxy *supervise.Proxy, logger *slog.Logger) {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), proxyShutdownTimeout)
	defer cancel()
	if err := proxy.Stop(shutdownCtx); err != nil {
		logger.Warn("supervise: reload proxy stop", slog.Any("err", err))
	}
}

// loggingAlerts is the production supervise.Alerts sink. Each alert is
// surfaced as a WARN operational-log record carrying the closed-set class /
// scope / error-class / reason labels — never any secret material
// (Constitution X: AlertPayload is structurally non-secret).
type loggingAlerts struct {
	logger *slog.Logger
}

// Emit records one alert at WARN level.
func (a loggingAlerts) Emit(ctx context.Context, class supervise.AlertClass, p supervise.AlertPayload) {
	a.logger.LogAttrs(
		ctx, slog.LevelWarn, "supervisor alert",
		slog.String("class", class.String()),
		slog.String("scope", p.Scope),
		slog.String("error_class", p.ErrorClass),
		slog.String("reason", p.Reason),
	)
}

// scopedValidator adapts a validators.Validator (scope-agnostic) to the
// supervise.Validator interface (scope-aware). The wrapper names the failing
// scope without ever materializing the secret value.
type scopedValidator struct {
	name  string
	inner validators.Validator
}

// Validate runs the underlying probe and, on failure, wraps the error with
// the scope name.
func (v scopedValidator) Validate(ctx context.Context, scope string, secret *securebytes.SecureBytes) error {
	if err := v.inner.Validate(ctx, secret); err != nil {
		return fmt.Errorf("hush: supervise: validator %q rejected scope %q: %w", v.name, scope, err)
	}
	return nil
}

// buildSuperviseValidators maps the config's scope→validator-name table onto
// concrete supervise.Validator implementations from the builtin registry.
// Config load has already rejected unknown validator names, so every lookup
// resolves; an unexpected miss is skipped (no-op validator applies).
func buildSuperviseValidators(cfg *superviseconfig.Supervisor) map[string]supervise.Validator {
	if len(cfg.Validators) == 0 {
		return nil
	}
	registry := validators.NewRegistry(nil)
	out := make(map[string]supervise.Validator, len(cfg.Validators))
	for scope, name := range cfg.Validators {
		v, ok := registry.Get(string(name))
		if !ok {
			continue
		}
		out[scope] = scopedValidator{name: string(name), inner: v}
	}
	return out
}

// startSuperviseWatchdog builds the log-pattern watchdog from config, spawns
// its matcher loop plus the Event→Alerts bridge, and returns it for
// Deps.Watchdog. Returns nil when the watchdog is disabled or misconfigured —
// the orchestrator wires its no-op default for a nil Watchdog.
func startSuperviseWatchdog(ctx context.Context, cfg *superviseconfig.Supervisor, logger *slog.Logger, alerts supervise.Alerts) supervise.Watchdog {
	if !cfg.Watchdog.Enabled || len(cfg.Watchdog.Patterns) == 0 {
		return nil
	}
	patterns, err := watchdog.BuildPatterns(cfg.Watchdog.Patterns, cfg.Watchdog.MaxAlertsPerHour)
	if err != nil {
		logger.Warn("hush: supervise: watchdog disabled (pattern compile failed)", slog.Any("err", err))
		return nil
	}
	events := make(chan watchdog.Event, 64)
	wd, err := watchdog.NewWatchdog(patterns, events, logger)
	if err != nil {
		logger.Warn("hush: supervise: watchdog disabled (construction failed)", slog.Any("err", err))
		return nil
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("hush: supervise: watchdog goroutine panic", slog.Any("recover", r))
			}
		}()
		_ = wd.Run(ctx)
	}()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("hush: supervise: watchdog drain goroutine panic", slog.Any("recover", r))
			}
		}()
		watchdog.DrainToAlerts(ctx, events, alerts)
	}()
	return wd
}

// defaultNonceFn / defaultRequestIDFn produce small random tokens for
// /claim payloads. Both read crypto/rand directly; failure paths panic
// since these are startup-only and any rand failure is unrecoverable.
func defaultNonceFn() string {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic(fmt.Errorf("hush: supervise: nonce: %w", err))
	}
	return fmt.Sprintf("%x", b[:])
}

func defaultRequestIDFn() string {
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		panic(fmt.Errorf("hush: supervise: request id: %w", err))
	}
	return fmt.Sprintf("%x", b[:])
}

// _ avoids the unused-import warning when no test injects via these helpers.
var _ = securebytes.SecureBytes{}
