package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/upgrade"
)

// releaseSourceForTests, when non-nil, overrides the production
// release source (gh CLI with REST API fallback) inside newUpgradeCmd.
// Set only by upgrade_test.go so the cobra layer can exercise the full
// pipeline without touching the network.
//
//nolint:gochecknoglobals // test-only seam; never set outside _test.go
var releaseSourceForTests upgrade.ReleaseSource

// execPathForTests, when non-empty, overrides Config.ExecPath inside
// newUpgradeCmd so tests can target a writable temp directory instead
// of the real binary.
//
//nolint:gochecknoglobals // test-only seam; never set outside _test.go
var execPathForTests string

// upgradeFlagCheck, upgradeFlagForce, upgradeFlagChannel are the
// public flag names for `hush upgrade`. Centralized for the tests
// that read flag values back via cmd.Flags().GetX.
const (
	upgradeFlagCheck   = "check"
	upgradeFlagForce   = "force"
	upgradeFlagChannel = "channel"
)

func newUpgradeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade hush in place from a GitHub release",
		Long: `Upgrade replaces the running hush binary with the latest release
published on GitHub. The matching platform tarball is downloaded,
verified against the published SHA-256 checksums file, extracted, and
swapped into place atomically (copy → <dst>.new → rename) so a
running ` + "`hush serve`" + ` is not corrupted mid-execution.

Channel selection follows the UPDATE_CHANNEL environment variable
(stable | beta | edge, case-insensitive; default stable). The
--channel flag overrides UPDATE_CHANNEL when both are set.

The install target is the resolved path of the currently running
hush binary (os.Executable with symlinks evaluated). If that
directory is not writable the command exits with a clear error
naming the directory — it never silently installs elsewhere.`,
		Example: `  # Check whether a newer release is available (no install).
  hush upgrade --check

  # Upgrade to the latest stable release.
  hush upgrade

  # Force a reinstall of the current version.
  hush upgrade --force

  # Pick a non-default channel.
  hush upgrade --channel beta
  UPDATE_CHANNEL=edge hush upgrade`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			check, _ := cmd.Flags().GetBool(upgradeFlagCheck)
			force, _ := cmd.Flags().GetBool(upgradeFlagForce)
			channelFlag, _ := cmd.Flags().GetString(upgradeFlagChannel)
			return runUpgrade(cmd.Context(), out.stdout, out.stderr, upgradeOptions{
				check:       check,
				force:       force,
				channelFlag: channelFlag,
			})
		},
	}
	cmd.Flags().Bool(upgradeFlagCheck, false, "Check for an available upgrade without downloading or installing")
	cmd.Flags().Bool(upgradeFlagForce, false, "Reinstall the latest release even when already current")
	cmd.Flags().String(upgradeFlagChannel, "", "Release channel: stable | beta | edge (overrides UPDATE_CHANNEL)")
	return cmd
}

// upgradeOptions bundles the parsed cobra flags so runUpgrade has a
// single argument that's easy to construct from tests.
type upgradeOptions struct {
	check       bool
	force       bool
	channelFlag string
}

// runUpgrade drives the upgrade.Check or upgrade.Install pipeline,
// renders human-readable output to stdout, and funnels every error
// through printErr so the locked "hush: upgrade: <message>" shape is
// preserved. The underlying error is returned verbatim so mapErr can
// classify it by sentinel.
func runUpgrade(ctx context.Context, stdout, stderr *Stream, opts upgradeOptions) error {
	cfg := upgrade.Config{
		ReleaseSource:  releaseSourceForTests,
		Channel:        resolveChannel(opts.channelFlag, lookupEnvString),
		CurrentVersion: Version,
		Force:          opts.force,
		Stdout:         stdout.w,
	}
	if execPathForTests != "" {
		cfg.ExecPath = execPathForTests
	}

	if isDevVersion(Version) {
		_ = stderr.WriteText("hush: upgrade: warning: running a dev build (%s); proceeding with upgrade", Version)
	}

	if opts.check {
		info, err := upgrade.Check(ctx, cfg)
		if err != nil {
			printErr(stderr, "upgrade: %s", formatUpgradeErr(err))
			return err
		}
		return renderCheckInfo(stdout, info)
	}

	if err := upgrade.Install(ctx, cfg); err != nil {
		printErr(stderr, "upgrade: %s", formatUpgradeErr(err))
		return err
	}
	return nil
}

// lookupEnvString returns the value of the named env var (empty when
// unset). Wrapping os.LookupEnv lets resolveChannel stay a pure
// function of a getenv lambda — the Constitution forbids direct
// os.Getenv references inside internal/cli (TestServe_NeverReadsEnv).
func lookupEnvString(key string) string {
	v, _ := os.LookupEnv(key)
	return v
}

// resolveChannel turns the --channel flag (if any) plus the
// UPDATE_CHANNEL env into the upgrade.Channel the driver consumes.
// The flag wins when both are set.
func resolveChannel(flagVal string, getenv func(string) string) upgrade.Channel {
	if flagVal != "" {
		switch strings.ToLower(strings.TrimSpace(flagVal)) {
		case "beta":
			return upgrade.Beta
		case "edge":
			return upgrade.Edge
		default:
			return upgrade.Stable
		}
	}
	return upgrade.GetChannel(getenv)
}

// isDevVersion reports whether v is a placeholder (empty or "dev") so
// the cobra layer can emit a warning before invoking the driver. The
// driver itself treats both cases as older-than-any-real-semver.
func isDevVersion(v string) bool {
	trimmed := strings.TrimSpace(v)
	return trimmed == "" || trimmed == "dev"
}

// renderCheckInfo prints a TTY-friendly summary of upgrade.Check
// output, or the same Info as a JSON document on a non-TTY pipe.
func renderCheckInfo(stdout *Stream, info *upgrade.Info) error {
	var b strings.Builder
	fmt.Fprintf(&b, "channel:           %s\n", info.Channel)
	fmt.Fprintf(&b, "current version:   %s\n", info.CurrentVersion)
	fmt.Fprintf(&b, "latest version:    %s\n", info.LatestVersion)
	fmt.Fprintf(&b, "update available:  %t", info.UpdateAvailable)
	if info.AssetName != "" {
		fmt.Fprintf(&b, "\nasset:             %s", info.AssetName)
	}
	if info.ChecksumSHA256 != "" {
		fmt.Fprintf(&b, "\nchecksum sha256:   %s", info.ChecksumSHA256)
	}
	return stdout.Auto(b.String(), info)
}

// formatUpgradeErr collapses the wrapped sentinel into a single
// human-readable line. The exact prefix ("hush/upgrade: …") from the
// sentinel is stripped because printErr already adds "hush: upgrade:".
func formatUpgradeErr(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Sentinels are emitted as "hush/upgrade: <category>: <detail>";
	// strip the package prefix so printErr's "hush: upgrade:" prefix
	// doesn't duplicate the leading "hush:".
	msg = strings.TrimPrefix(msg, "hush/upgrade: ")
	// If multiple wrapped errors stacked the prefix, trim recursively.
	for strings.Contains(msg, "hush/upgrade: ") {
		msg = strings.ReplaceAll(msg, "hush/upgrade: ", "")
	}
	if errors.Is(err, upgrade.ErrInstallDirNotWritable) {
		msg += " (try `sudo hush upgrade` or copy the new binary into place manually)"
	}
	return msg
}
