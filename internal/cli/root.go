package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Execute constructs the root command, dispatches the subcommand
// matching os.Args, and returns the resolved exit code. The caller
// (cmd/hush/main.go) is responsible for os.Exit; Execute itself
// performs no os.Exit so tests can exercise it directly.
//
// ctx is propagated to every subcommand. SIGINT/SIGTERM handling for
// long-running subcommands (currently only `serve`) is layered on
// inside the subcommand via signal.NotifyContext rather than at the
// root level — most subcommands are short-lived HTTP one-shots that
// honor ctx via their own client timeouts.
func Execute(ctx context.Context) int {
	if err := ctx.Err(); err != nil {
		return ExitErr
	}

	stdout := streamFor(os.Stdout, false)
	stderr := streamFor(os.Stderr, false)

	root := newRootCmd(&outputContext{stdout: stdout, stderr: stderr}) //nolint:contextcheck // ctx is attached via SetContext below; subcommands read it via cmd.Context().
	root.SetContext(ctx)

	err := root.ExecuteContext(ctx)
	return mapErr(err)
}

// newRootCmd builds the cobra root command. Constructed fresh on
// every Execute call (Constitution IX — no mutable package-level
// state).
func newRootCmd(initialOut *outputContext) *cobra.Command {
	root := &cobra.Command{
		Use:           "hush",
		Short:         "hush — Tailscale-only ephemeral secret-claim service",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	addPersistentFlags(root)

	root.PersistentPreRunE = persistentPreRun
	_ = initialOut // initialOut is only used as a pre-parse fallback

	root.AddCommand(newServeCmd())
	root.AddCommand(newHealthCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newRevokeCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newRequestCmd())
	root.AddCommand(newSecretCmd())
	root.AddCommand(newSuperviseCmd())
	root.AddCommand(newClientCmd())

	return root
}

// persistentPreRun runs before every subcommand: validates the
// global-flag conflict gate and re-derives the per-stream output
// context with --no-color factored in.
func persistentPreRun(cmd *cobra.Command, _ []string) error {
	flags := readGlobalFlags(cmd)
	if flags.verbose && flags.quiet {
		return errFlagConflict
	}
	stdout := streamFor(os.Stdout, flags.noColor)
	stderr := streamFor(os.Stderr, flags.noColor)
	ctx := context.WithValue(cmd.Context(), outputCtxKey{}, &outputContext{stdout: stdout, stderr: stderr})
	cmd.SetContext(ctx)
	return nil
}

// outputFromCmd returns the *outputContext attached to cmd's Context
// by PersistentPreRunE. Falls back to a fresh per-stream pair built
// against os.Stdout / os.Stderr if no context value is present (this
// path is exercised only by tests that bypass PersistentPreRunE).
func outputFromCmd(cmd *cobra.Command) *outputContext {
	if v, ok := cmd.Context().Value(outputCtxKey{}).(*outputContext); ok && v != nil {
		return v
	}
	return &outputContext{
		stdout: streamFor(os.Stdout, false),
		stderr: streamFor(os.Stderr, false),
	}
}

// printErr writes err to stderr in the contract-locked shape:
// "<subcommand>: <message>" — single line, no stack, no echo of
// input bytes beyond the message itself. Used by RunE wrappers when
// the caller wants to render an error message before returning.
func printErr(stderr *Stream, message string, args ...any) {
	_ = stderr.WriteText("hush: "+message, args...)
}

// fmtError wraps a sentinel with a contextual message preserving the
// errors.Is chain. Convenience helper used by subcommand error
// returns.
func fmtError(sentinel error, format string) error {
	return fmt.Errorf("%w: %s", sentinel, format)
}
