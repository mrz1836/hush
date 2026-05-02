package cli

import "github.com/spf13/cobra"

// flagNames groups the four global persistent-flag identifiers.
// Centralized so subcommands look them up by symbolic name rather
// than free-form string literals.
const (
	flagConfig  = "config"
	flagVerbose = "verbose"
	flagQuiet   = "quiet"
	flagNoColor = "no-color"
)

// globalFlags carries the resolved values of the four persistent
// flags. Populated by the root command's PersistentPreRunE; read by
// every subcommand via flagsFromCmd.
type globalFlags struct {
	configPath string
	verbose    bool
	quiet      bool
	noColor    bool
}

// addPersistentFlags wires the four global flags onto cmd's
// PersistentFlags so every subcommand inherits them. Called once by
// newRootCmd.
func addPersistentFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringP(flagConfig, "c", defaultConfigPath(), "Path to configuration file")
	cmd.PersistentFlags().BoolP(flagVerbose, "v", false, "Add stderr trace of resolved config + step transitions")
	cmd.PersistentFlags().BoolP(flagQuiet, "q", false, "Suppress all non-error output")
	cmd.PersistentFlags().Bool(flagNoColor, false, "Force no ANSI color in output")
}

// readGlobalFlags pulls the resolved persistent-flag values from cmd.
func readGlobalFlags(cmd *cobra.Command) globalFlags {
	cfg, _ := cmd.Flags().GetString(flagConfig)
	verbose, _ := cmd.Flags().GetBool(flagVerbose)
	quiet, _ := cmd.Flags().GetBool(flagQuiet)
	noColor, _ := cmd.Flags().GetBool(flagNoColor)
	return globalFlags{
		configPath: cfg,
		verbose:    verbose,
		quiet:      quiet,
		noColor:    noColor,
	}
}

// defaultConfigPath returns the default --config value. Per
// docs/CONFIG-SCHEMA, operators use ~/.hush/config.toml; the path is
// expanded only when the loader runs (config.LoadServer handles
// tilde expansion downstream).
func defaultConfigPath() string {
	return "~/.hush/config.toml"
}
