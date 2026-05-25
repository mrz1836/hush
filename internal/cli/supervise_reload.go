package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/supervise/config"
	"github.com/mrz1836/hush/pkg/client"
)

// superviseReloadTimeout is the wall-clock ceiling on a single
// `hush supervise reload` round-trip. The supervisor's internal swap
// (start child → readiness probe → backend swap → old-child grace) is
// bounded by the config's [child.readiness] + [child.shutdown] grace,
// so 120s comfortably covers the default 30s grace + readiness budget
// while still failing fast on a stuck supervisor.
//
//nolint:gochecknoglobals // sentinel-class timeout knob; mutated only by tests via withTimeouts.
var superviseReloadTimeout = 120 * time.Second

// newSuperviseReloadCmd constructs the `hush supervise reload
// <config-path>` leaf. The positional config-path is loaded and
// validated locally so a malformed file is caught before any socket
// I/O; the supervisor uses its own already-loaded config for the
// actual swap, but the path is forwarded for audit attribution.
func newSuperviseReloadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reload <config-path>",
		Short: "Request a zero-downtime reload from a running supervisor",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSuperviseReload(cmd, args[0])
		},
	}
	return cmd
}

// runSuperviseReload loads the operator's config so it can resolve the
// supervisor's status-socket path locally, dials the supervisor via
// pkg/client.SupervisorStatus.Reload, prints the locked success line
// on success, and maps typed SDK errors onto the CLI's stable exit
// codes on failure. Stderr lines use the locked
// `hush: supervise: reload: <msg>` shape.
func runSuperviseReload(cmd *cobra.Command, configPath string) error {
	stderr := cmd.ErrOrStderr()

	cfg, err := config.Load(cmd.Context(), configPath)
	if err != nil {
		printSuperviseReloadErr(stderr, err)
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), superviseReloadTimeout)
	defer cancel()

	sup := client.NewSupervisorStatus(cfg.StatusSocket)
	res, sdkErr := sup.Reload(ctx, configPath)
	if sdkErr != nil {
		wrapped := wrapReloadSDKErr(sdkErr)
		printSuperviseReloadErr(stderr, wrapped)
		return wrapped
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(),
		"hush: supervise: reload: ok (readiness %s, strategy %s)\n",
		res.ReadinessDuration, res.Strategy)
	return nil
}

// wrapReloadSDKErr maps pkg/client typed reload errors onto the CLI's
// sentinel set. The SDK's error message (which already includes the
// supervisor's reason string) is preserved via %w so callers see both
// the local sentinel (for exit-code classification) and the upstream
// reason (for the human stderr line).
func wrapReloadSDKErr(err error) error {
	switch {
	case errors.Is(err, client.ErrReloadConfigInvalid):
		return fmt.Errorf("%w: %w", errReloadConfigInvalid, err)
	case errors.Is(err, client.ErrReloadReadinessFailed):
		return fmt.Errorf("%w: %w", errReloadReadinessFailed, err)
	case errors.Is(err, client.ErrReloadInFlight):
		return fmt.Errorf("%w: %w", errReloadInFlight, err)
	case errors.Is(err, client.ErrReloadFailed):
		return fmt.Errorf("%w: %w", errReloadFailed, err)
	case errors.Is(err, client.ErrSocketUnavailable),
		errors.Is(err, client.ErrInvalidResponse):
		return fmt.Errorf("%w: %w", errSocketUnreachable, err)
	}
	return fmt.Errorf("%w: %w", errReloadFailed, err)
}

// printSuperviseReloadErr writes err to stderr in the locked
// `hush: supervise: reload: <msg>` shape. Newlines in the message are
// replaced with spaces so the line stays one-line.
func printSuperviseReloadErr(stderr io.Writer, err error) {
	if err == nil {
		return
	}
	msg := strings.ReplaceAll(err.Error(), "\n", " ")
	_, _ = fmt.Fprintf(stderr, "hush: supervise: reload: %s\n", msg)
}
