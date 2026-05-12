package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// superviseFlags holds the parsed flag values for `hush supervise`.
// runSupervise applies the effective-projection rules per
// FR-023-11/12/13/14 before any side effect.
type superviseFlags struct {
	dryRun      bool
	graceWindow time.Duration
	noCache     bool
}

// claimPreview is the dry-run canonical-payload struct rendered by
// sign.CanonicalJSON. Field tag order is irrelevant — the canonical
// encoder sorts keys alphabetically.
type claimPreview struct {
	MachineIndex uint32   `json:"machine_index"`
	Name         string   `json:"name"`
	Reason       string   `json:"reason"`
	RequestedTTL string   `json:"requested_ttl"`
	Scope        []string `json:"scope"`
	SessionType  string   `json:"session_type"`
}

// realClock implements supervise.Clock against time.Now.
type realClock struct{}

// Now returns the current wall-clock time.
func (realClock) Now() time.Time { return time.Now() }

// orchestratorInputs implements supervise.StatusInputs via eight
// atomic pointer / bool fields. Safe for concurrent reads from any
// StatusServer handler goroutine (Constitution IX).
type orchestratorInputs struct {
	name             string
	childStartedAt   atomic.Pointer[time.Time]
	lastAuthFail     atomic.Pointer[time.Time]
	scopeHealthy     atomic.Pointer[[]string]
	scopeStale       atomic.Pointer[[]string]
	sessionExp       atomic.Pointer[time.Time]
	refreshNext      atomic.Pointer[time.Time]
	discordConnected atomic.Bool
}

// Name returns the supervisor's configured name.
func (o *orchestratorInputs) Name() string { return o.name }

// SessionExpiresAt returns the cached JWT session expiry instant.
func (o *orchestratorInputs) SessionExpiresAt() time.Time {
	if p := o.sessionExp.Load(); p != nil {
		return *p
	}
	return time.Time{}
}

// RefreshWindowNext returns the next scheduled refresh-window instant.
func (o *orchestratorInputs) RefreshWindowNext() time.Time {
	if p := o.refreshNext.Load(); p != nil {
		return *p
	}
	return time.Time{}
}

// ScopeHealthy returns the latest healthy-scope list.
func (o *orchestratorInputs) ScopeHealthy() []string {
	if p := o.scopeHealthy.Load(); p != nil {
		return *p
	}
	return nil
}

// ScopeStale returns the latest stale-scope list.
func (o *orchestratorInputs) ScopeStale() []string {
	if p := o.scopeStale.Load(); p != nil {
		return *p
	}
	return nil
}

// LastAuthFailure returns the timestamp of the last auth-failure
// transition, or nil if no auth failure has been recorded.
func (o *orchestratorInputs) LastAuthFailure() *time.Time {
	return o.lastAuthFail.Load()
}

// ChildUptime returns the duration since the current child was
// started, or 0 when no child is live.
func (o *orchestratorInputs) ChildUptime() time.Duration {
	p := o.childStartedAt.Load()
	if p == nil || p.IsZero() {
		return 0
	}
	return time.Since(*p)
}

// DiscordConnected returns whether the Discord transport is connected.
func (o *orchestratorInputs) DiscordConnected() bool {
	return o.discordConnected.Load()
}

// refreshFlight is one in-flight refresh attempt observed by every
// concurrent caller of refreshCoalescer.Handle (FR-023-22a).
type refreshFlight struct {
	done chan struct{}
	err  error
}

// errRefreshPerformNotWired is the static sentinel surfaced by
// refreshCoalescer.Handle when no perform closure has been wired —
// programmer error escape hatch (Constitution IX err113-compliant).
var errRefreshPerformNotWired = errors.New("hush: supervise: refresh perform not wired")

// refreshCoalescer is the single-flight gate for `hush client refresh`
// callbacks. Concurrent invocations share the same terminal result.
type refreshCoalescer struct {
	mu       sync.Mutex
	inflight *refreshFlight
	perform  func(ctx context.Context) error
}

// Handle is the refreshHandler wired into the StatusServer. Returns
// the terminal err observed by every caller of the in-flight refill.
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

// newSuperviseCmd constructs the `hush supervise <config-path>`
// subcommand. Side-effect-free constructor; the orchestrator wiring
// runs inside RunE.
func newSuperviseCmd() *cobra.Command {
	flags := superviseFlags{}
	cmd := &cobra.Command{
		Use:   "supervise <config-path>",
		Short: "Run a single supervisor in the foreground",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSupervise(cmd, args[0], flags)
		},
	}
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false,
		"Render the canonical /claim payload to stdout and exit 0")
	cmd.Flags().DurationVar(&flags.graceWindow, "grace-window", 0,
		"Override cfg.CacheGraceTTL for this run (must be >0 and ≤4h)")
	cmd.Flags().BoolVar(&flags.noCache, "no-cache", false,
		"Force cfg.CacheSecretsForRestart=false for this run")
	return cmd
}

// graceWindowCap is the FR-023-12 hard cap (4h) on --grace-window.
const graceWindowCap = 4 * time.Hour

// runSupervise is the supervise subcommand's main body. Loads the
// config, applies flag overrides, dispatches to the dry-run branch or
// the normal-start orchestration sequence per cli-supervise.md §6.
//
//nolint:gocognit,gocyclo,cyclop,funlen // sequential orchestration glue per cli-supervise.md §6; every step delegates to internal/supervise.
func runSupervise(cmd *cobra.Command, configPath string, flags superviseFlags) error {
	stderr := cmd.ErrOrStderr()

	cfg, err := config.Load(cmd.Context(), configPath)
	if err != nil {
		printSuperviseErr(stderr, err)
		return err
	}

	if flags.graceWindow != 0 {
		if flags.graceWindow < 0 || flags.graceWindow > graceWindowCap {
			wrapped := fmt.Errorf("%w: must be >0 and ≤4h, got %s", errInvalidGraceWindow, flags.graceWindow)
			printSuperviseErr(stderr, wrapped)
			return wrapped
		}
	}
	effectiveGraceTTL := cfg.CacheGraceTTL
	if flags.graceWindow != 0 {
		effectiveGraceTTL = flags.graceWindow
	}
	effectiveCacheEnabled := cfg.CacheSecretsForRestart
	if flags.noCache {
		effectiveCacheEnabled = false
	}

	if flags.dryRun {
		preview := claimPreview{
			MachineIndex: cfg.ClientMachineIndex,
			Name:         cfg.Name,
			Reason:       cfg.Reason,
			RequestedTTL: cfg.RequestedTTL.String(),
			Scope:        cfg.Scope,
			SessionType:  cfg.SessionType,
		}
		body, mErr := sign.CanonicalJSON(preview)
		if mErr != nil {
			return fmt.Errorf("hush: supervise: canonicalise: %w", mErr)
		}
		body = append(body, '\n')
		if _, wErr := cmd.OutOrStdout().Write(body); wErr != nil {
			return fmt.Errorf("hush: supervise: write payload: %w", wErr)
		}
		return nil
	}

	rootCtx, rootCancel := signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)
	defer rootCancel()

	pidfile, err := supervise.AcquirePidFile(cfg.PIDFile)
	if err != nil {
		if errors.Is(err, supervise.ErrPidLocked) {
			wrapped := fmt.Errorf("%w (pidfile=%s): %w", errDuplicateSupervisor, cfg.PIDFile, err)
			printSuperviseErr(stderr, wrapped)
			return wrapped
		}
		printSuperviseErr(stderr, err)
		return err
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelInfo}))
	store := supervise.NewStore(rootCtx, realClock{})
	grace := supervise.NewGrace(effectiveGraceTTL, effectiveCacheEnabled)

	inputs := &orchestratorInputs{name: cfg.Name}
	inputs.discordConnected.Store(true)

	statusServer := supervise.NewStatusServer(cfg.StatusSocket, store, logger)
	statusServer.AttachStatusInputs(inputs)

	coalescer := &refreshCoalescer{}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	refiller := supervise.NewRefiller(httpClient, store, logger)

	refresher := supervise.NewRefresher(cfg.RefreshWindow, cfg.RequestedTTL, func(ctx context.Context) error {
		return coalescer.Handle(ctx)
	}, logger)

	statusServer.AttachRefreshHandler(coalescer.Handle)

	coalescer.perform = func(ctx context.Context) error {
		return refiller.Refill(ctx, cfg.Scope)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				logger.Error("supervise: statusServer goroutine panic", "recover", r)
			}
		}()
		_ = statusServer.Run(rootCtx)
	}()
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				logger.Error("supervise: refresher goroutine panic", "recover", r)
			}
		}()
		_ = refresher.Run(rootCtx)
	}()

	_ = grace
	<-rootCtx.Done()
	wg.Wait()
	if relErr := pidfile.Release(); relErr != nil {
		logger.Warn("supervise: pidfile release", "err", relErr)
	}
	return nil
}

// printSuperviseErr writes err to stderr in the locked
// `hush: supervise: <msg>` shape from cli-supervise.md §8. Newlines in
// the message are replaced with spaces so the line stays one-line.
func printSuperviseErr(stderr io.Writer, err error) {
	if err == nil {
		return
	}
	msg := string(bytes.ReplaceAll([]byte(err.Error()), []byte("\n"), []byte(" ")))
	_, _ = fmt.Fprintf(stderr, "hush: supervise: %s\n", msg)
}

// Compile-time guard: orchestratorInputs implements
// supervise.StatusInputs.
var _ supervise.StatusInputs = (*orchestratorInputs)(nil)

// Compile-time guard: realClock implements supervise.Clock.
var _ supervise.Clock = realClock{}
