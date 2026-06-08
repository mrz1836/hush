package server

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/transport/sign"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Chassis tunable defaults. These are package-level constants because they
// represent contract-locked behaviour (drain window, shutdown timeout, etc.)
// — operators tune them via [Deps] overrides, not at package level.
const (
	// DefaultReloadDrainWindow is the default time between vault swap and
	// destroy of the previous store. In-flight requests holding the old
	// pointer have this long to finish before their store is zeroed.
	DefaultReloadDrainWindow = 30 * time.Second

	// DefaultShutdownTimeout is the default deadline for graceful shutdown
	// — the time http.Server.Shutdown waits for in-flight requests to
	// finish before returning.
	DefaultShutdownTimeout = 30 * time.Second

	// DefaultReadHeaderTimeout caps how long the chassis waits for a
	// client to deliver request headers. Hardening default for
	// http.Server.
	DefaultReadHeaderTimeout = 10 * time.Second

	// DefaultReadTimeout caps how long the chassis waits for a complete
	// request (headers + body). Hardening default for http.Server.
	DefaultReadTimeout = 30 * time.Second

	// DefaultWriteTimeout caps how long the chassis allows a handler to
	// produce its response. It must exceed ClaimApprovalTimeout (60s default),
	// otherwise a human-approved claim can mint successfully after Discord
	// approval while the HTTP response has already been killed, surfacing as EOF
	// to the client.
	DefaultWriteTimeout = 90 * time.Second

	// DefaultIdleTimeout caps how long an idle keep-alive connection may
	// remain open. Hardening default for http.Server.
	DefaultIdleTimeout = 60 * time.Second

	// DefaultClockSyncTimeout bounds the time the chassis waits for the
	// host clock-sync probe to return.
	DefaultClockSyncTimeout = 5 * time.Second

	// MaxRequestBodyBytes is the chassis-wide request-body cap. Bodies
	// over this size return 413 Payload Too Large before any handler
	// sees the body.
	MaxRequestBodyBytes = 64 << 10

	// vaultFilename is the canonical filename of the on-disk vault inside
	// the configured state directory.
	vaultFilename = "secrets.vault"
)

// vaultPath derives the absolute vault file path from the configured state
// directory. The chassis owns this convention — the rotate command
// writes to the same path, and SIGHUP causes the chassis to reload from it.
func vaultPath(cfg *config.Server) string {
	return filepath.Join(cfg.Server.StateDir, vaultFilename)
}

// defaultInterfaceLister is the OS bridge used by the tailscale_bind startup
// check. Replaceable in tests to inject a deterministic interface table
// without depending on the host's Tailscale state.
//
//nolint:gochecknoglobals // OS bridge; test-hookable for tailscale_bind coverage
var defaultInterfaceLister = net.InterfaceAddrs

// TokenIssuer is the chassis-side seam through which the claim handler mints
// session tokens. Production wiring binds this to a closure capturing the
// process-resident JWT signing key (BIP32 m/44'/7743'/0'); tests inject a
// counting wrapper around [token.Issue] so call counts can be asserted.
//
// The handler invokes the issuer EXACTLY at one place — the success branch of
// the post-approval pipeline — so a non-nil return ALWAYS pairs with an
// HTTP 200 body and never with any non-200 outcome.
type TokenIssuer func(ctx context.Context, params token.IssueParams) (*token.Token, error)

// ClientKeyResolver looks up a registered client's public key by its
// fingerprint (16-char lowercase hex per [keys.PublicKeyFingerprint]). On a
// miss the resolver MUST return [ErrClientUnknown]; the handler maps that
// outcome to the same `bad_signature` (403) status as a verify failure to
// avoid leaking which fingerprints are registered (when a client supplies
// an unknown registered-client-key fingerprint).
type ClientKeyResolver func(fingerprint string) (*ecdsa.PublicKey, error)

// Deps is the dependency-injection bundle for the chassis. The first seven
// fields are required; the remainder default to host-platform implementations
// when nil.
type Deps struct {
	// Required.

	// Cfg is the validated TOML server config.
	Cfg *config.Server

	// VaultPtr is the atomic pointer to the active in-memory vault store.
	// The chassis swaps this on reload; handlers read it on every
	// request.
	VaultPtr *atomic.Pointer[vault.Store]

	// TokenStore is the JWT session-state repository.
	TokenStore token.Store

	// TokenIssuer mints fresh JWTs for approved claims. Production binds
	// this to [token.Issue] with the process-resident signing key
	// captured.
	TokenIssuer TokenIssuer

	// Approver is the approval interface. Production supplies the
	// Discord-backed implementation; tests supply fakes.
	Approver Approver

	// Logger is the redacting structured logger.
	Logger *slog.Logger

	// AuditWriter emits security-relevant events.
	AuditWriter AuditWriter

	// JWTVerifyKey is the ECDSA public key the chassis uses to verify
	// session JWTs at /s. Production wiring binds this to the public half
	// of the BIP32-derived JWT signing key (m/44'/7743'/0'). Tests inject
	// a key paired with their fake TokenIssuer. Required for /s to
	// validate a Bearer token; the chassis nil-checks at handler entry.
	JWTVerifyKey *ecdsa.PublicKey

	// Optional.

	// DiscordHealth is the connectivity probe surfaced via /hz's
	// `discord_connected` field. When nil the chassis reports
	// `discord_connected: false` (fail-closed).
	DiscordHealth func() bool

	// Clock is the chassis's wall-clock source. Defaults to time.Now.
	Clock func() time.Time

	// ClockSyncProbe queries the host's NTP-sync state. Defaults to a
	// platform-default helper (darwin / linux). Tests inject a
	// deterministic probe.
	ClockSyncProbe func(ctx context.Context) (synced bool, drift time.Duration, err error)

	// InterfaceLister enumerates local interface addresses for the
	// tailscale_bind check. Defaults to net.InterfaceAddrs.
	InterfaceLister func() ([]net.Addr, error)

	// Listener overrides the http.Server listener; when nil, the chassis
	// binds to Cfg.Server.ListenAddr via net.Listen("tcp", ...). Tests
	// supply a pre-built listener (e.g. an in-memory pipe) when they need
	// to exercise the lifecycle without a real TCP bind.
	Listener net.Listener

	// VaultKey is the long-lived vault encryption key captured at
	// construction. Required for SIGHUP-driven reloads. Tests that do
	// not exercise reload may leave this nil.
	VaultKey *securebytes.SecureBytes

	// LoadVaultFn loads and decrypts a vault file from disk. Defaults to
	// vault.Load. Tests inject a fake to drive each reload error
	// category.
	LoadVaultFn func(ctx context.Context, path string, key *securebytes.SecureBytes) (vault.Store, error)

	// ClientKeyResolver looks up registered client signing keys by
	// fingerprint. When nil, the chassis installs a default that loads
	// Cfg.Server.ClientRegistry once at construction time and serves an
	// in-memory map (lookup miss → [ErrClientUnknown]).
	ClientKeyResolver ClientKeyResolver

	// ReloadDrainWindow overrides DefaultReloadDrainWindow.
	ReloadDrainWindow time.Duration

	// ShutdownTimeout overrides DefaultShutdownTimeout.
	ShutdownTimeout time.Duration

	// NonceCache overrides the default [sign.NewNonceCache] instance. When
	// nil the chassis constructs the production cache with its 30s sweep
	// interval. Tests inject a short-interval cache so the lifecycle of the
	// chassis-owned sweep goroutine can be observed deterministically.
	NonceCache sign.NonceCache

	// AllowClockSkew downgrades a would-be clock-sync startup failure
	// to a logged warning + a single [AuditClockSkewOverride] audit
	// event. Set from `hush serve --allow-clock-skew`. hush never
	// auto-sudos to fix the clock; this flag is the only override path
	// on the serve side.
	AllowClockSkew bool

	// AllowClockProbeUnavailable downgrades only a probe-unavailable
	// startup failure (all clock providers timed out and cache fallback
	// was unavailable) to a warning. It does not downgrade confirmed
	// not-synchronised or drift-over-threshold clocks.
	AllowClockProbeUnavailable bool

	// ServerVersion is the semantic version reported by /me. Production
	// wiring passes the ldflags-injected `cli.Version`. Defaults to
	// "dev" when empty.
	ServerVersion string
}

// mountedRoute is one (method, path, handler) tuple captured before Run via
// [Server.Mount]. The chassis applies them to the *http.ServeMux during Run
// after startup checks pass.
type mountedRoute struct {
	method  string
	path    string
	handler http.Handler
}

// Server is the chassis instance. Constructed once via [New], run once via
// [Server.Run], and shut down once via cancellation of Run's context.
//
// All fields are unexported; callers interact with the chassis exclusively
// through the locked exported API ([New], [Server.Run], [Server.Mount],
// [Server.ReloadVault]).
type Server struct {
	cfg                        *config.Server
	vaultPtr                   *atomic.Pointer[vault.Store]
	tokenStore                 token.Store
	tokenIssuer                TokenIssuer
	jwtVerifyKey               *ecdsa.PublicKey
	approverImpl               Approver
	logger                     *slog.Logger
	audit                      AuditWriter
	discordHealthFn            func() bool
	clock                      func() time.Time
	clockProbe                 func(ctx context.Context) (bool, time.Duration, error)
	interfaceLister            func() ([]net.Addr, error)
	listener                   net.Listener
	vaultKey                   *securebytes.SecureBytes
	loadVault                  func(ctx context.Context, path string, key *securebytes.SecureBytes) (vault.Store, error)
	clientKeyResolver          ClientKeyResolver
	nonceCache                 sign.NonceCache
	reloadDrainWindow          time.Duration
	shutdownTimeout            time.Duration
	allowClockSkew             bool
	allowClockProbeUnavailable bool
	serverVersion              string

	runStartedAt time.Time
	clockInSync  atomic.Bool

	mu             sync.Mutex
	mountedRoutes  []mountedRoute
	httpServer     *http.Server
	listenerActual net.Listener
	shutdownDoneCh chan struct{}

	reloadMu     sync.Mutex
	drainWG      sync.WaitGroup
	shuttingDown atomic.Bool
	runCalled    atomic.Bool
}

// New constructs a chassis from deps. New performs zero I/O — no socket
// bind, no file read, no NTP query, no signal registration. Every
// blocking step happens inside [Server.Run].
//
// New returns a typed sentinel error (one of [ErrMissingConfig],
// [ErrMissingVaultPtr], [ErrMissingTokenStore], [ErrMissingApprover],
// [ErrMissingLogger], [ErrMissingAuditWriter]) for each missing required
// dependency. Optional fields default to host-platform helpers.
//
//nolint:cyclop,gocyclo,gocognit // sequential dependency wiring; complexity is structural
func New(deps Deps) (*Server, error) {
	if err := validateDeps(deps); err != nil {
		return nil, err
	}

	s := &Server{
		cfg:                        deps.Cfg,
		vaultPtr:                   deps.VaultPtr,
		tokenStore:                 deps.TokenStore,
		tokenIssuer:                deps.TokenIssuer,
		jwtVerifyKey:               deps.JWTVerifyKey,
		approverImpl:               deps.Approver,
		logger:                     deps.Logger,
		audit:                      deps.AuditWriter,
		discordHealthFn:            deps.DiscordHealth,
		clock:                      deps.Clock,
		clockProbe:                 deps.ClockSyncProbe,
		interfaceLister:            deps.InterfaceLister,
		listener:                   deps.Listener,
		vaultKey:                   deps.VaultKey,
		loadVault:                  deps.LoadVaultFn,
		clientKeyResolver:          deps.ClientKeyResolver,
		nonceCache:                 deps.NonceCache,
		reloadDrainWindow:          deps.ReloadDrainWindow,
		shutdownTimeout:            deps.ShutdownTimeout,
		allowClockSkew:             deps.AllowClockSkew,
		allowClockProbeUnavailable: deps.AllowClockProbeUnavailable,
		serverVersion:              deps.ServerVersion,
		shutdownDoneCh:             make(chan struct{}),
	}
	if s.serverVersion == "" {
		s.serverVersion = "dev"
	}

	if s.clock == nil {
		s.clock = time.Now
	}
	if s.clockProbe == nil {
		s.clockProbe = CachedClockSyncProbe(DefaultClockSyncProbe, deps.Cfg.Server.StateDir, s.clock, func(ctx context.Context, fb ClockSyncCacheFallback) {
			if writeErr := s.audit.Write(ctx, AuditEvent{
				Type: AuditClockSyncCacheFallback,
				At:   s.clock(),
				Detail: map[string]string{
					"age":         fb.Age.String(),
					"drift":       fb.Drift.String(),
					"measured_at": fb.MeasuredAt.Format(time.RFC3339Nano),
				},
			}); writeErr != nil {
				s.logger.WarnContext(ctx, "audit write clock_sync_cache_fallback failed", "err", writeErr.Error())
			}
		})
	}
	if s.interfaceLister == nil {
		s.interfaceLister = func() ([]net.Addr, error) { return defaultInterfaceLister() }
	}
	if s.loadVault == nil {
		s.loadVault = vault.Load
	}
	if s.clientKeyResolver == nil {
		s.clientKeyResolver = newDefaultClientKeyResolver(deps.Cfg)
	}
	if s.reloadDrainWindow <= 0 {
		s.reloadDrainWindow = DefaultReloadDrainWindow
	}
	if s.shutdownTimeout <= 0 {
		s.shutdownTimeout = DefaultShutdownTimeout
	}
	if s.nonceCache == nil {
		s.nonceCache = sign.NewNonceCache()
	}

	return s, nil
}

// validateDeps performs every nil-check prescribed by [New], returning the
// matching sentinel for the first failure. Pulled into a helper so [New]
// stays under the package's gocognit budget.
func validateDeps(deps Deps) error {
	if deps.Cfg == nil {
		return ErrMissingConfig
	}
	if deps.VaultPtr == nil || deps.VaultPtr.Load() == nil {
		return ErrMissingVaultPtr
	}
	if deps.TokenStore == nil {
		return ErrMissingTokenStore
	}
	if deps.TokenIssuer == nil {
		return ErrMissingTokenIssuer
	}
	if deps.Approver == nil {
		return ErrMissingApprover
	}
	if deps.Logger == nil {
		return ErrMissingLogger
	}
	if deps.AuditWriter == nil {
		return ErrMissingAuditWriter
	}
	return nil
}

// errCompressedPubKeyLen indicates a registry entry whose hex-decoded
// public_key field is not 33 bytes long. Surfaced to the operator via the
// resolver-error chain; the handler treats it as bad_signature (no
// enumeration leak).
var errCompressedPubKeyLen = errors.New("server: client registry: compressed pubkey not 33 bytes")

// clientRegistryEntry is the on-disk shape of one row in the JSON registry
// file. The file is a JSON array of these entries. Public-key bytes are
// 33-byte SEC1-compressed secp256k1 (66 hex chars).
type clientRegistryEntry struct {
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
}

// newDefaultClientKeyResolver returns a [ClientKeyResolver] that lazily loads
// cfg.Server.ClientRegistry on first invocation and caches the
// fingerprint→pubkey map for the lifetime of the chassis on success.
// Loader errors are returned to the caller and are NOT cached — the
// next call retries the load, so an operator who fixes a malformed
// registry file does not need to restart the chassis to pick up the
// fix. Handler logic translates resolver errors to [ErrClientUnknown]
// so the wire-facing response stays uniform.
//
// Lazy load (rather than eager load in [New]) preserves the chassis
// "zero I/O in New" invariant.
func newDefaultClientKeyResolver(cfg *config.Server) ClientKeyResolver {
	c := &lazyClientKeyCache{path: cfg.Server.ClientRegistry}
	return c.resolve
}

// lazyClientKeyCache is the shared mutable state for the default
// resolver. A successful load is cached for the chassis lifetime; a
// failed load is NOT cached, so an operator who repairs the registry
// file picks up the fix on the next request without restarting.
type lazyClientKeyCache struct {
	path   string
	mu     sync.RWMutex
	keys   map[string]*ecdsa.PublicKey
	loaded bool
}

// resolve is the [ClientKeyResolver] callback. Fast path takes a read
// lock; slow path acquires the write lock and attempts the load.
func (c *lazyClientKeyCache) resolve(fingerprint string) (*ecdsa.PublicKey, error) {
	c.mu.RLock()
	if c.loaded {
		pub, ok := c.keys[fingerprint]
		c.mu.RUnlock()
		return resolveLookup(pub, ok)
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.loaded {
		k, err := loadClientRegistry(c.path)
		if err != nil {
			return nil, err
		}
		c.keys = k
		c.loaded = true
	}
	pub, ok := c.keys[fingerprint]
	return resolveLookup(pub, ok)
}

// resolveLookup translates the (pub, ok) pair into the
// [ClientKeyResolver] return values.
func resolveLookup(pub *ecdsa.PublicKey, ok bool) (*ecdsa.PublicKey, error) {
	if !ok {
		return nil, ErrClientUnknown
	}
	return pub, nil
}

// loadClientRegistry parses the JSON registry file and returns a
// fingerprint→pubkey map. Missing file → empty map (every fingerprint
// resolves to ErrClientUnknown). Malformed JSON / hex → propagated error.
func loadClientRegistry(path string) (map[string]*ecdsa.PublicKey, error) {
	if path == "" {
		return map[string]*ecdsa.PublicKey{}, nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied registry path; chassis trusts the config layer
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]*ecdsa.PublicKey{}, nil
		}
		return nil, fmt.Errorf("server: load client registry: %w", err)
	}
	var entries []clientRegistryEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("server: parse client registry: %w", err)
	}
	out := make(map[string]*ecdsa.PublicKey, len(entries))
	for _, e := range entries {
		pub, err := decodeCompressedSecp256k1(e.PublicKey)
		if err != nil {
			return nil, fmt.Errorf("server: decode client registry entry %q: %w", e.Fingerprint, err)
		}
		out[e.Fingerprint] = pub
	}
	return out, nil
}

// decodeCompressedSecp256k1 parses a hex-encoded 33-byte SEC1-compressed
// secp256k1 public key into an [*ecdsa.PublicKey].
func decodeCompressedSecp256k1(s string) (*ecdsa.PublicKey, error) {
	raw, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("hex decode: %w", err)
	}
	if len(raw) != 33 {
		return nil, fmt.Errorf("%w: got %d", errCompressedPubKeyLen, len(raw))
	}
	pub, err := secp256k1.ParsePubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse secp256k1 pubkey: %w", err)
	}
	x, y := pub.X(), pub.Y()
	bigX := new(big.Int).SetBytes(x.Bytes()[:])
	bigY := new(big.Int).SetBytes(y.Bytes()[:])
	return &ecdsa.PublicKey{
		Curve: secp256k1.S256(), //nolint:staticcheck // secp256k1 unsupported by crypto/ecdh
		X:     bigX,
		Y:     bigY,
	}, nil
}

// Run executes the chassis lifecycle: startup checks → bind → launch
// background loops (SIGHUP, nonce-cache sweep) → serve → shutdown. Run
// blocks until ctx cancels or a startup check fails.
//
// On success Run returns nil; on a startup-check failure Run returns the
// matching sentinel wrapped error. Run may only be called once per
// Server; subsequent calls return [ErrAlreadyRun].
//
// ctx is never stored in the struct; cancellation flows through the
// closure that calls http.Server.Shutdown and through the derived sweep
// context that drives the nonce-cache eviction loop.
//
//nolint:gocyclo,cyclop // sequential lifecycle: startup → bind → serve → drain → shutdown; complexity is structural
func (s *Server) Run(ctx context.Context) error {
	if !s.runCalled.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}
	s.runStartedAt = s.clock()

	if err := s.runStartupChecks(ctx); err != nil {
		s.emitStartupAudit(ctx, "refused", failedCheckName(err))
		s.logger.ErrorContext(
			ctx, "startup check failed",
			"check", failedCheckName(err),
			"err", err.Error(),
		)
		return err
	}

	listener, err := s.acquireListener(ctx)
	if err != nil {
		return fmt.Errorf("server: listener: %w", err)
	}
	s.mu.Lock()
	s.listenerActual = listener
	s.mu.Unlock()

	mux := http.NewServeMux()
	s.applyMounts(mux)

	chain := s.middlewareChain(mux)

	httpServer := &http.Server{
		Handler:           chain,
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       DefaultReadTimeout,
		WriteTimeout:      DefaultWriteTimeout,
		IdleTimeout:       DefaultIdleTimeout,
	}
	s.mu.Lock()
	s.httpServer = httpServer
	s.mu.Unlock()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	sighupDone := make(chan struct{})
	go s.sighupLoop(ctx, sigCh, sighupDone)

	// Nonce-cache sweep — without this goroutine the sync.Map backing
	// the replay-defense cache grows monotonically with every accepted
	// request, leaking memory until OOM (Constitution V: failure must
	// be loud; silent growth is the inverse). A derived context lets
	// us cancel the sweep at shutdown even when the parent ctx wasn't
	// the trigger (e.g. httpServer.Serve returned a fatal error).
	sweepCtx, sweepCancel := context.WithCancel(ctx)
	defer sweepCancel()
	nonceSweepDone := make(chan struct{})
	go s.nonceSweepLoop(sweepCtx, nonceSweepDone)

	s.emitStartupAudit(ctx, "ok", "")

	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- httpServer.Serve(listener)
	}()

	var runErr error
	select {
	case <-ctx.Done():
	case err = <-serveErrCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
	}

	s.shuttingDown.Store(true)
	close(s.shutdownDoneCh)
	sweepCancel()

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && runErr == nil {
		s.logger.ErrorContext(ctx, "http server shutdown", "err", err.Error())
	}
	s.drainWG.Wait()
	<-sighupDone
	<-nonceSweepDone

	if writeErr := s.audit.Write(ctx, AuditEvent{
		Type: AuditServerStop,
		At:   s.clock(),
	}); writeErr != nil {
		s.logger.WarnContext(ctx, "audit write server_stop failed", "err", writeErr.Error())
	}

	return runErr
}

// acquireListener returns the listener configured on Deps (when non-nil) or
// binds a fresh TCP listener to Cfg.Server.ListenAddr.
func (s *Server) acquireListener(ctx context.Context) (net.Listener, error) {
	if s.listener != nil {
		return s.listener, nil
	}
	addr := s.cfg.Server.ListenAddr.String()
	var lc net.ListenConfig
	return lc.Listen(ctx, "tcp", addr)
}

// approver returns the chassis-stored Approver. Test-only accessor used by
// approver_test.go to verify the chassis stored the supplied dependency
// unchanged.
func (s *Server) approver() Approver {
	return s.approverImpl
}

// discordHealth reports whether the Discord WebSocket gateway is currently
// available, per the wired probe. Nil-safe: a nil Deps.DiscordHealth field
// reports `false` (fail-closed).
func (s *Server) discordHealth() bool {
	if s.discordHealthFn == nil {
		return false
	}
	return s.discordHealthFn()
}

// emitStartupAudit emits a single AuditServerStart event with status and (when
// non-empty) the failed check name.
func (s *Server) emitStartupAudit(ctx context.Context, status, check string) {
	detail := map[string]string{"status": status}
	if check != "" {
		detail["check"] = check
	}
	if err := s.audit.Write(ctx, AuditEvent{
		Type:   AuditServerStart,
		At:     s.clock(),
		Detail: detail,
	}); err != nil {
		s.logger.WarnContext(ctx, "audit write server_start failed", "err", err.Error())
	}
}

// nonceSweepLoop runs the nonce-cache sweep goroutine for the chassis
// lifetime. It blocks inside [sign.NonceCache.Run] until ctx cancels,
// then signals completion via done.
//
// Constitution IX requires every goroutine to recover(); the deferred
// recoverNonceSweepLoop satisfies that. Without this loop the sync.Map
// backing the cache grows monotonically and never evicts expired entries
// — a silent memory leak that defeats Layer 4 replay defense by
// exhausting RSS before any audit signal fires (Constitution V).
func (s *Server) nonceSweepLoop(ctx context.Context, done chan<- struct{}) {
	defer close(done)
	defer s.recoverNonceSweepLoop(ctx)
	s.nonceCache.Run(ctx)
}

// recoverNonceSweepLoop is the deferred panic recovery for
// [Server.nonceSweepLoop].
func (s *Server) recoverNonceSweepLoop(ctx context.Context) {
	if r := recover(); r != nil {
		s.logger.ErrorContext(ctx, "nonce sweep loop panic", "panic", fmt.Sprintf("%v", r))
	}
}

// sighupLoop runs as the chassis's signal handler goroutine. It exits when
// ctx cancels and signals completion via done.
func (s *Server) sighupLoop(ctx context.Context, sigCh <-chan os.Signal, done chan<- struct{}) {
	defer close(done)
	defer s.recoverSighupLoop(ctx)
	for {
		if !s.processOneSighup(ctx, sigCh) {
			return
		}
	}
}

// recoverSighupLoop is the deferred panic recovery for [Server.sighupLoop].
func (s *Server) recoverSighupLoop(ctx context.Context) {
	if r := recover(); r != nil {
		s.logger.ErrorContext(ctx, "sighup loop panic", "panic", fmt.Sprintf("%v", r))
	}
}

// processOneSighup blocks on the next event for the SIGHUP loop. It returns
// false when the loop should exit (ctx cancel, shutdownDoneCh closed, or
// signal channel closed).
func (s *Server) processOneSighup(ctx context.Context, sigCh <-chan os.Signal) bool {
	select {
	case <-ctx.Done():
		return false
	case <-s.shutdownDoneCh:
		return false
	case _, ok := <-sigCh:
		if !ok {
			return false
		}
		if !s.shuttingDown.Load() {
			s.handleSighup(ctx)
		}
		return true
	}
}

// handleSighup is the per-signal reload trigger. It uses the configured vault
// path and key; logs and returns silently on configuration gaps so the loop
// keeps running.
func (s *Server) handleSighup(ctx context.Context) {
	if s.vaultKey == nil {
		s.logger.ErrorContext(ctx, "SIGHUP received but no vault key configured")
		return
	}
	if err := s.runReload(ctx, vaultPath(s.cfg), s.vaultKey); err != nil {
		s.logger.ErrorContext(ctx, "vault reload failed", "err", err.Error())
	}
}
