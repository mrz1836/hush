// Package cli — `hush vault` subcommand: root-key vault operations.
//
// Mounts on the cobra root via newVaultCmd(). This file currently
// provides the cobra surface only; the verb implementations are added
// in subsequent phases.
//
// Verbs:
//   - rekey — change the vault passphrase by re-deriving a fresh key
//     from a fresh salt. Requires the operator to know the current
//     passphrase. TTY-only on both stdin and stdout; the existing
//     `hush secret rotate` verb covers same-key vault re-encryption.
package cli

import (
	"errors"

	"github.com/spf13/cobra"
)

// errVaultRekeyNotImplemented is the stub returned by `hush vault
// rekey` until later phases land the TTY/auth/snapshot/rewrite flow.
// Surfaced through mapErr's catch-all ExitErr classification.
var errVaultRekeyNotImplemented = errors.New("hush: vault: rekey is not yet implemented")

// newVaultCmd builds the `hush vault` parent. No RunE — invoking
// `hush vault` without a verb prints help (default cobra behaviour),
// matching the `hush secret` and `hush keychain` parents.
func newVaultCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vault",
		Short: "Manage the vault root key (rekey)",
	}
	cmd.AddCommand(newVaultRekeyCmd())
	return cmd
}

// newVaultRekeyCmd builds the `hush vault rekey` leaf. The
// --update-keychain flag is wired here so the surface matches the
// plan's AC-9; the runtime behaviour is filled in by later phases.
func newVaultRekeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Change the vault passphrase and rewrite secrets.vault under a fresh key",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return errVaultRekeyNotImplemented
		},
	}
	cmd.Flags().Bool("update-keychain", false, "Also update the matching macOS Keychain item with the new passphrase (opt-in; no-op on unsupported platforms or if the item is missing)")
	return cmd
}
