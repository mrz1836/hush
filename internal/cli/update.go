package cli

import (
	selfupdate "github.com/mrz1836/go-selfupdate"
	"github.com/mrz1836/go-selfupdate/cobracmd"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// attachUpdateCommand registers hush's self-update command on root and wires
// the passive "a new version is available" notice, both derived from a single
// self-update config. The running binary's compiled-in version is threaded in
// explicitly so a binary run from outside PATH still updates itself.
func attachUpdateCommand(root *cobra.Command) {
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
	cmd := cobracmd.Attach(root, selfupdate.Config{ //nolint:gosec // G101 false positive: TokenEnvVar is an environment variable name, not a credential
		Owner:          "mrz1836",
		Repo:           "hush",
		BinaryName:     "hush",
		CurrentVersion: Version,
		TokenEnvVar:    "HUSH_GITHUB_TOKEN",
	}, cobracmd.WithDeprecatedUseBinaryFlag())

	// hush's global --config already owns the -c shorthand; the library's
	// --check flag ships -c too. Strip --check's shorthand so cobra can merge the
	// inherited --config/-c without a pflag shorthand-redefine panic at run time.
	// The long --check form is unaffected.
	dropShorthand(cmd, flagCheck)

	// Match hush's help conventions with a rendered Examples section.
	cmd.Example = `  hush update            # download & install the latest release
  hush update --check    # report whether a newer version is available
  hush update --force    # reinstall the latest even if already current`
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
