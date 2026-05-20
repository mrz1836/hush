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
			Timeout: 30 * time.Second,
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
		if len(b) != 32 {
			uErr = fmt.Errorf("%w: %d, want 32", errSuperviseClientKeyLength, len(b))
			return
		}
		scalar := make([]byte, 32)
		copy(scalar, b)
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
	return lc.Run(rootCtx)
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
	a.logger.LogAttrs(ctx, slog.LevelWarn, "supervisor alert",
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
	go func() { _ = wd.Run(ctx) }()
	go watchdog.DrainToAlerts(ctx, events, alerts)
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
