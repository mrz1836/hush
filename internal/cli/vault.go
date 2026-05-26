// Package cli — `hush vault` subcommand: root-key vault operations.
//
// Mounts on the cobra root via newVaultCmd(). Verbs:
//   - rekey — change the vault passphrase by re-deriving a fresh key
//     from a fresh salt. Requires the operator to know the current
//     passphrase. TTY-only on both stdin and stdout; the existing
//     `hush secret rotate` verb covers same-key vault re-encryption.
package cli

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Locked stderr literals for `hush vault rekey`. Every byte is
// contract-asserted by tests so renames in messaging require an
// explicit test update. gosec G101 false positives on "passphrase"
// tokens: these are operator-facing diagnostic strings, not
// credentials.
//
//nolint:gosec // user-facing diagnostic messages, not credentials
const (
	vaultMsgNoTTY               = "hush: vault: this command requires an interactive TTY on stdin and stdout"
	vaultMsgPassphraseTooShort  = "hush: vault: new passphrase must be at least 12 bytes"
	vaultMsgPassphraseMismatch  = "hush: vault: new passphrase confirmation does not match"
	vaultMsgPassphraseUnchanged = "hush: vault: new passphrase must differ from the current passphrase"
)

// Locked prompt labels for the rekey flow. gosec G101 false positive
// on "passphrase" tokens: prompts displayed to the operator, not
// stored credentials.
//
//nolint:gosec // prompt labels, not credentials
const (
	promptVaultCurrentPassphrase = "Current vault passphrase: "
	promptVaultNewPassphrase     = "New vault passphrase: "
	promptVaultConfirmNew        = "Confirm new vault passphrase: "
)

// errPassphraseUnchanged surfaces a `vault rekey` invocation where the
// operator supplied the same passphrase for the new value. Wraps
// errMissingFlag → ExitInputErr through the existing classifier so no
// new sentinel needs to land in exit_codes.go.
var errPassphraseUnchanged = fmt.Errorf("hush: vault: new passphrase unchanged: %w", errMissingFlag)

// vaultDeps groups the testable seams threaded into the rekey flow.
// Mirrors secretDeps in spirit but is intentionally separate so the
// vault parent stays decoupled from secret-verb dependencies.
type vaultDeps struct {
	loadSecrets func(ctx context.Context, path string, key *securebytes.SecureBytes) ([]vault.Secret, error)
	saveVault   func(ctx context.Context, path string, key *securebytes.SecureBytes, salt []byte, secrets []vault.Secret) error

	promptPassphrase func(in *os.File, prompt io.Writer, label string) (*securebytes.SecureBytes, error)

	isStdinTTY  func(*os.File) bool
	isStdoutTTY func(*os.File) bool

	deriveMasterSeed func(ctx context.Context, passphrase, salt []byte) ([]byte, error)
	readVaultSalt    func(path string) ([]byte, error)

	kill        func(pid int, sig syscall.Signal) error
	readPIDFile func(path string) ([]byte, error)

	stateDirRoot string
	configPath   string
	logger       *slog.Logger
	nowFn        func() time.Time
}

// productionVaultDeps wires the real seams. Tests construct a custom
// vaultDeps directly.
func productionVaultDeps() *vaultDeps {
	return &vaultDeps{
		loadSecrets:      vault.LoadSecrets,
		saveVault:        vault.SaveWithSalt,
		promptPassphrase: readPassphraseTTY,
		isStdinTTY:       defaultIsTTY,
		isStdoutTTY:      defaultIsTTY,
		deriveMasterSeed: keys.DeriveMasterSeed,
		readVaultSalt:    readVaultSalt,
		kill:             syscall.Kill,
		readPIDFile:      os.ReadFile,
		stateDirRoot:     "",
		logger:           slog.Default(),
		nowFn:            time.Now,
	}
}

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
// plan's AC-9; the post-write Keychain branch is filled in by Phase 4.
func newVaultRekeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Change the vault passphrase and rewrite secrets.vault under a fresh key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			deps := productionVaultDeps()
			deps.configPath = readGlobalFlags(cmd).configPath
			return runVaultRekey(cmd.Context(), out.stdout, out.stderr, os.Stdin, os.Stdout, deps)
		},
	}
	cmd.Flags().Bool("update-keychain", false, "Also update the matching macOS Keychain item with the new passphrase (opt-in; no-op on unsupported platforms or if the item is missing)")
	return cmd
}

// runVaultRekey implements the `vault rekey` flow. Phase 2 lands the
// TTY gating, current-passphrase authentication, and new-passphrase
// validation paths. Snapshot + rewrite + PID + Keychain land in Phase
// 3/4. Returning successfully from this Phase-2 build is intentionally
// not yet possible: after validation succeeds the caller falls through
// to errVaultRekeyNotImplemented so a half-built rekey can never
// accidentally rewrite a real vault.
//
//nolint:gocognit,gocyclo // sequential rekey flow with TTY/auth/pass-validation dispatch
func runVaultRekey(ctx context.Context, _ *Stream, stderr *Stream, in, stdoutFile *os.File, deps *vaultDeps) error {
	if err := enforceRekeyTTY(ctx, in, stdoutFile, deps, stderr); err != nil {
		return err
	}

	vaultPath, err := resolveVaultRekeyPath(ctx, deps)
	if err != nil {
		return err
	}

	currentPass, err := deps.promptPassphrase(in, stderr.w, promptVaultCurrentPassphrase)
	if err != nil {
		return err
	}
	defer func() { _ = currentPass.Destroy() }()

	salt, err := deps.readVaultSalt(vaultPath)
	if err != nil {
		return err
	}

	oldKey, err := deriveVaultRekeyKey(ctx, deps, currentPass, salt)
	if err != nil {
		return err
	}
	defer func() { _ = oldKey.Destroy() }()

	secrets, err := deps.loadSecrets(ctx, vaultPath, oldKey)
	if err != nil {
		if errors.Is(err, vault.ErrAuthFailed) {
			auditVaultRekey(ctx, deps.logger, "passphrase_failed")
		}
		return err
	}
	defer destroyVaultRekeySecrets(secrets)

	newPass, err := deps.promptPassphrase(in, stderr.w, promptVaultNewPassphrase)
	if err != nil {
		return err
	}
	defer func() { _ = newPass.Destroy() }()

	if lenErr := enforceNewPassphraseLen(ctx, deps, stderr, newPass); lenErr != nil {
		return lenErr
	}

	confirmPass, err := deps.promptPassphrase(in, stderr.w, promptVaultConfirmNew)
	if err != nil {
		return err
	}
	defer func() { _ = confirmPass.Destroy() }()

	if cmpErr := enforceNewPassphraseConfirmation(ctx, deps, stderr, newPass, confirmPass); cmpErr != nil {
		return cmpErr
	}

	if changedErr := enforceNewPassphraseChanged(ctx, deps, stderr, currentPass, newPass); changedErr != nil {
		return changedErr
	}

	// Phase 2 stops here. Snapshot + rewrite + PID + Keychain land in
	// later phases; the supervisor sequences those into separate
	// commits.
	return errVaultRekeyNotImplemented
}

// errVaultRekeyNotImplemented is the stub returned by `hush vault
// rekey` after Phase 2's validation succeeds but before Phase 3 lands
// the snapshot and rewrite. Mapped to ExitErr via the catch-all so
// operators see a clear failure instead of a silently-incomplete
// command if they invoke an interim build.
var errVaultRekeyNotImplemented = errors.New("hush: vault: rekey is not yet implemented")

// enforceRekeyTTY refuses if stdin or stdout is not an interactive
// terminal. Emits the locked stderr line and a `vault_rekey_tty_refused`
// audit record so an operator monitoring stderr sees the refusal AND
// the audit log captures the attempt.
func enforceRekeyTTY(ctx context.Context, in, stdoutFile *os.File, deps *vaultDeps, stderr *Stream) error {
	if deps.isStdinTTY(in) && deps.isStdoutTTY(stdoutFile) {
		return nil
	}
	_ = stderr.WriteText(vaultMsgNoTTY)
	auditVaultRekey(ctx, deps.logger, "tty_refused")
	return errNoTTY
}

// resolveVaultRekeyPath returns the absolute path to the on-disk vault
// file based on deps.stateDirRoot (test override) or the loaded server
// config (production). Mirrors resolveVaultPath in secret.go but is
// kept separate so the vault parent does not depend on secretDeps.
func resolveVaultRekeyPath(ctx context.Context, deps *vaultDeps) (string, error) {
	if deps.stateDirRoot != "" {
		return filepath.Join(deps.stateDirRoot, secretsVaultFilename), nil
	}
	configPath := deps.configPath
	if configPath == "" {
		configPath = "~/.hush/config.toml"
	}
	expanded, err := expandTilde(configPath)
	if err != nil {
		return "", err
	}
	cfg, err := config.LoadServer(ctx, expanded)
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg.Server.StateDir, secretsVaultFilename), nil
}

// deriveVaultRekeyKey runs the passphrase → master seed → vault
// encryption key derivation. Returns the AES-GCM key wrapped in
// *SecureBytes; the caller owns it and MUST Destroy. Mirrors
// deriveVaultKey in secret.go.
func deriveVaultRekeyKey(ctx context.Context, deps *vaultDeps, passphrase *securebytes.SecureBytes, salt []byte) (*securebytes.SecureBytes, error) {
	var masterSeed []byte
	var deriveErr error
	if useErr := passphrase.Use(func(b []byte) {
		masterSeed, deriveErr = deps.deriveMasterSeed(ctx, b, salt)
	}); useErr != nil {
		return nil, useErr
	}
	if deriveErr != nil {
		return nil, deriveErr
	}
	defer zeroBytes(masterSeed)

	rawKey, err := keys.DeriveVaultEncKey(masterSeed)
	if err != nil {
		return nil, err
	}
	return securebytes.New(rawKey)
}

// enforceNewPassphraseLen rejects new passphrases shorter than
// minPassphraseLen. Emits the locked stderr message and a
// `vault_rekey_passphrase_too_short` audit record.
func enforceNewPassphraseLen(ctx context.Context, deps *vaultDeps, stderr *Stream, newPass *securebytes.SecureBytes) error {
	var tooShort bool
	if useErr := newPass.Use(func(b []byte) {
		tooShort = len(b) < minPassphraseLen
	}); useErr != nil {
		return fmt.Errorf("hush: vault: inspect new passphrase: %w", useErr)
	}
	if !tooShort {
		return nil
	}
	_ = stderr.WriteText(vaultMsgPassphraseTooShort)
	auditVaultRekey(ctx, deps.logger, "passphrase_too_short")
	return errPassphraseTooShort
}

// enforceNewPassphraseConfirmation rejects when the second prompt does
// not match the first. The comparison runs inside nested Use callbacks
// so both buffers stay mlocked through the constant-time check.
func enforceNewPassphraseConfirmation(ctx context.Context, deps *vaultDeps, stderr *Stream, newPass, confirmPass *securebytes.SecureBytes) error {
	equal, cmpErr := secureBytesEqual(newPass, confirmPass)
	if cmpErr != nil {
		return cmpErr
	}
	if equal {
		return nil
	}
	_ = stderr.WriteText(vaultMsgPassphraseMismatch)
	auditVaultRekey(ctx, deps.logger, "new_passphrase_mismatch")
	return errPassphraseMismatch
}

// enforceNewPassphraseChanged rejects when the new passphrase is byte
// identical to the current one. The comparison runs inside nested Use
// callbacks so neither buffer is destroyed before the constant-time
// check completes (AC-5 contract).
func enforceNewPassphraseChanged(ctx context.Context, deps *vaultDeps, stderr *Stream, currentPass, newPass *securebytes.SecureBytes) error {
	var (
		equal    int
		innerErr error
	)
	outerErr := currentPass.Use(func(cur []byte) {
		innerErr = newPass.Use(func(nw []byte) {
			equal = constantTimeSecureEqual(cur, nw)
		})
	})
	if outerErr != nil {
		return fmt.Errorf("hush: vault: compare passphrases: %w", outerErr)
	}
	if innerErr != nil {
		return fmt.Errorf("hush: vault: compare passphrases: %w", innerErr)
	}
	if equal == 0 {
		return nil
	}
	_ = stderr.WriteText(vaultMsgPassphraseUnchanged)
	auditVaultRekey(ctx, deps.logger, "new_passphrase_unchanged")
	return errPassphraseUnchanged
}

// constantTimeSecureEqual returns 1 if a and b have the same length and
// content, 0 otherwise. Unequal lengths short-circuit but still feed
// subtle.ConstantTimeCompare on a same-length copy so the comparison
// shape is uniform regardless of input lengths.
func constantTimeSecureEqual(a, b []byte) int {
	if len(a) != len(b) {
		// Burn a same-length compare to keep timing-shape uniform when
		// lengths differ. The result is discarded; the function returns
		// 0 (unequal) below.
		pad := make([]byte, len(a))
		_ = subtle.ConstantTimeCompare(a, pad)
		return 0
	}
	return subtle.ConstantTimeCompare(a, b)
}

// destroyVaultRekeySecrets mirrors destroySecrets in secret.go. Kept
// separate so the vault flow does not depend on secret-verb helpers
// that may evolve independently.
func destroyVaultRekeySecrets(secrets []vault.Secret) {
	for i := len(secrets) - 1; i >= 0; i-- {
		if secrets[i].Value != nil {
			_ = secrets[i].Value.Destroy()
		}
	}
}

// auditVaultRekey emits an early-failure `vault_rekeyed` record at
// WARN. Phase 2 only emits the refusal/validation outcomes (verb +
// outcome); Phase 4 will add a separate INFO emitter that carries
// `restart_required`, `keychain_updated`, and `snapshot_path` for the
// terminal success / success_partial records.
func auditVaultRekey(ctx context.Context, logger *slog.Logger, outcome string) {
	logger.Log(ctx, slog.LevelWarn, "vault_rekeyed", "verb", "rekey", "outcome", outcome)
}
