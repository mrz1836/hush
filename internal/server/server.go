package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/token"
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
	// produce its response. Hardening default for http.Server.
	DefaultWriteTimeout = 30 * time.Second

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
// directory. The chassis owns this convention — SDD-15's rotate command
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

// Deps is the dependency-injection bundle for the chassis. The first six
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

	// Approver is the approval interface. SDD-11 supplies the
	// Discord-backed implementation in production; tests supply fakes.
	Approver Approver

	// Logger is the redacting structured logger.
	Logger *slog.Logger

	// AuditWriter emits security-relevant events.
	AuditWriter AuditWriter

	// Optional.

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

	// ReloadDrainWindow overrides DefaultReloadDrainWindow.
	ReloadDrainWindow time.Duration

	// ShutdownTimeout overrides DefaultShutdownTimeout.
	ShutdownTimeout time.Duration
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
	cfg               *config.Server
	vaultPtr          *atomic.Pointer[vault.Store]
	tokenStore        token.Store
	approverImpl      Approver
	logger            *slog.Logger
	audit             AuditWriter
	clock             func() time.Time
	clockProbe        func(ctx context.Context) (bool, time.Duration, error)
	interfaceLister   func() ([]net.Addr, error)
	listener          net.Listener
	vaultKey          *securebytes.SecureBytes
	loadVault         func(ctx context.Context, path string, key *securebytes.SecureBytes) (vault.Store, error)
	reloadDrainWindow time.Duration
	shutdownTimeout   time.Duration

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
func New(deps Deps) (*Server, error) {
	if err := validateDeps(deps); err != nil {
		return nil, err
	}

	s := &Server{
		cfg:               deps.Cfg,
		vaultPtr:          deps.VaultPtr,
		tokenStore:        deps.TokenStore,
		approverImpl:      deps.Approver,
		logger:            deps.Logger,
		audit:             deps.AuditWriter,
		clock:             deps.Clock,
		clockProbe:        deps.ClockSyncProbe,
		interfaceLister:   deps.InterfaceLister,
		listener:          deps.Listener,
		vaultKey:          deps.VaultKey,
		loadVault:         deps.LoadVaultFn,
		reloadDrainWindow: deps.ReloadDrainWindow,
		shutdownTimeout:   deps.ShutdownTimeout,
		shutdownDoneCh:    make(chan struct{}),
	}

	if s.clock == nil {
		s.clock = time.Now
	}
	if s.clockProbe == nil {
		s.clockProbe = defaultClockSyncProbe
	}
	if s.interfaceLister == nil {
		s.interfaceLister = func() ([]net.Addr, error) { return defaultInterfaceLister() }
	}
	if s.loadVault == nil {
		s.loadVault = vault.Load
	}
	if s.reloadDrainWindow <= 0 {
		s.reloadDrainWindow = DefaultReloadDrainWindow
	}
	if s.shutdownTimeout <= 0 {
		s.shutdownTimeout = DefaultShutdownTimeout
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

// Run executes the chassis lifecycle: startup checks → bind → serve →
// shutdown. Run blocks until ctx cancels or a startup check fails.
//
// On success Run returns nil; on a startup-check failure Run returns the
// matching sentinel wrapped error. Run may only be called once per
// Server; subsequent calls return [ErrAlreadyRun].
//
// ctx is never stored in the struct; cancellation flows through the
// closure that calls http.Server.Shutdown.
//
//nolint:gocyclo,cyclop // sequential lifecycle: startup → bind → serve → drain → shutdown; complexity is structural
func (s *Server) Run(ctx context.Context) error {
	if !s.runCalled.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}

	if err := s.runStartupChecks(ctx); err != nil {
		s.emitStartupAudit(ctx, "refused", failedCheckName(err))
		s.logger.ErrorContext(ctx, "startup check failed",
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

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && runErr == nil {
		s.logger.ErrorContext(ctx, "http server shutdown", "err", err.Error())
	}
	s.drainWG.Wait()
	<-sighupDone

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
