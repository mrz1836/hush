package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/logging"
	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/internal/transport/sign"
)

// parseLogLevel maps cfg.LogLevel (validated against logLevelAllowList in
// SDD-18) to slog.Level. Unknown values default to LevelInfo — config
// validation already rejected them before reaching this code path.
func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

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

	logger := logging.New(logging.Options{
		Level:  parseLogLevel(cfg.LogLevel),
		Format: logging.FormatAuto,
		Out:    stderr,
	})

	// Project flag overrides onto the in-memory config so Lifecycle sees
	// the effective values (--grace-window / --no-cache override TOML).
	cfg.CacheGraceTTL = effectiveGraceTTL
	cfg.CacheSecretsForRestart = effectiveCacheEnabled

	lifecycleErr := runLifecycle(rootCtx, cfg, pidfile, logger)
	if lifecycleErr != nil && !errors.Is(lifecycleErr, context.Canceled) {
		logger.Error("supervise: lifecycle exited with error", "err", lifecycleErr)
	}

	if relErr := pidfile.Release(); relErr != nil {
		logger.Error("supervise: pidfile release", "err", relErr)
		if lifecycleErr == nil {
			return fmt.Errorf("hush: supervise: pidfile release: %w", relErr)
		}
	}

	if lifecycleErr != nil && !errors.Is(lifecycleErr, context.Canceled) {
		return lifecycleErr
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

// Compile-time guard: realClock implements supervise.Clock.
var _ supervise.Clock = realClock{}
