package cli

import (
	"strings"

	selfupdate "github.com/mrz1836/go-selfupdate"
	"github.com/mrz1836/go-selfupdate/cobracmd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// newUpdateConfig returns hush's production self-update config. The running
// binary's compiled-in version is threaded in explicitly so a binary run from
// outside PATH still updates itself; the release source, HTTP client, and
// target path are left to the library's defaults.
func newUpdateConfig() selfupdate.Config {
	return selfupdate.Config{ //nolint:gosec // G101 false positive: TokenEnvVar is an environment variable name, not a credential
		Owner:          "mrz1836",
		Repo:           "hush",
		BinaryName:     "hush",
		CurrentVersion: Version,
		TokenEnvVar:    "HUSH_GITHUB_TOKEN",
	}
}

// attachUpdateCommand registers hush's self-update command on root and wires
// the passive "a new version is available" notice, both derived from cfg
// (production passes [newUpdateConfig]). cfg is injected so tests can supply a
// fake release source, transport, and target path. The registered command is
// returned with its RunE wrapped so a failure is reported on stderr.
func attachUpdateCommand(root *cobra.Command, cfg selfupdate.Config) *cobra.Command {
	// One call registers the `update` command (alias `upgrade`, flags
	// --check/--force/--verbose) and the passive "a new version is available"
	// banner, both derived from this single config. hush installs only from its
	// GitHub release archives — verifying the SHA-256 checksum and atomically
	// replacing the binary — and refuses to overwrite a binary owned by
	// `go install` or Homebrew. AppName derives from BinaryName, giving the
	// HUSH_ env prefix (opt out with HUSH_NO_UPDATE_CHECK; the shared
	// NO_UPDATE_CHECK and CI also disable it). The deprecated --use-binary flag
	// is kept, hidden and inert, so old invocations do not error now that a
	// release archive is the only install route.
	cmd := cobracmd.Attach(root, cfg, cobracmd.WithDeprecatedUseBinaryFlag())

	// The library's command returns its errors without printing them, and the
	// root silences cobra's error output, so report a failure here. Only RunE
	// is swapped: the command keeps the library's annotation, so the passive
	// banner still stays quiet during an update.
	run := cmd.RunE
	cmd.RunE = func(c *cobra.Command, args []string) error {
		err := run(c, args)
		printUpdateErr(outputFromCmd(c).stderr, err)
		return err
	}

	// hush's global --config already owns the -c shorthand; the library's
	// --check flag ships -c too. Strip --check's shorthand so cobra can merge the
	// inherited --config/-c without a pflag shorthand-redefine panic at run time.
	// The long --check form is unaffected.
	dropShorthand(cmd, flagCheck)

	// Match hush's help conventions with a rendered Examples section.
	cmd.Example = `  hush update            # download & install the latest release
  hush update --check    # report whether a newer version is available
  hush update --force    # reinstall the latest even if already current`

	return cmd
}

// printUpdateErr writes an update failure to stderr as one line. The root
// command silences cobra's error printing, and the go-selfupdate command
// returns its errors without printing them, so without this a failed update
// (a blocked checksum download, a refused install directory) exits with only
// a status code. The library's message is kept verbatim behind an
// "update" prefix — even when invoked as `upgrade` — so it stays greppable.
// Usage errors print nothing: the root command has already reported them.
// The caller returns err unwrapped, so exit codes are unchanged.
func printUpdateErr(stderr *Stream, err error) {
	if err == nil || asUsageError(err) != nil {
		return
	}
	printErr(stderr, "update: %s", strings.ReplaceAll(err.Error(), "\n", " "))
}

// flagCheck is the library's dry-run flag whose default -c shorthand collides
// with hush's global --config.
const flagCheck = "check"

// dropShorthand clears the single-letter shorthand on cmd's named flag by
// rebuilding the flag set without it. pflag exposes no shorthand-removal API,
// so every flag is re-added to a fresh set (ResetFlags) with the target's
// shorthand cleared; each flag's value binding and hidden state are preserved.
func dropShorthand(cmd *cobra.Command, name string) {
	var flags []*pflag.Flag
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Name == name {
			f.Shorthand = ""
		}
		flags = append(flags, f)
	})
	cmd.ResetFlags()
	for _, f := range flags {
		cmd.Flags().AddFlag(f)
	}
}
