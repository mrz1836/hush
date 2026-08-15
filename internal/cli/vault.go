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
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/mrz1836/hush/internal/vault/securebytes"
	"github.com/mrz1836/hush/internal/yubikey"
)

// vaultSaltLen is the on-disk salt width expected by vault.SaveWithSalt
// (matches the unexported saltLen constant in internal/vault). Pinned
// locally so the rekey path does not import a private vault constant.
const vaultSaltLen = 16

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

	// Post-write PID-probe stderr lines. Phase 4 surfaces server-state
	// hints without sending any signal; pidPresent prints the AC-8
	// restart-required line, the other three states get tolerant notes.
	vaultMsgPidPresentFmt  = "hush: vault: running server detected (pid=%d) — restart it to pick up the new passphrase"
	vaultMsgPidAbsent      = "hush: vault: no running server detected"
	vaultMsgPidStale       = "hush: vault: stale PID file detected; no running server"
	vaultMsgPidNotOurUser  = "hush: vault: PID file is owned by another user; not probed"
	vaultMsgPidUnreadable  = "hush: vault: PID file unreadable; cannot determine server state"
	vaultMsgRekeyedFmt     = "hush: vault: rekey complete; snapshot=%s"
	vaultMsgKcUnsupported  = "hush: vault: --update-keychain: per-binary ACL unsupported on this platform; skipping (no Keychain mutation)"
	vaultMsgKcItemMissing  = "hush: vault: --update-keychain: existing Keychain item not found; skipping (no Keychain mutation)"
	vaultMsgKcUpdated      = "hush: vault: --update-keychain: Keychain item updated"
	vaultMsgPartialFailFmt = "hush: vault: vault rekey SUCCEEDED but Keychain update FAILED — manual follow-up required: %v"

	// Enveloped (v2) rekey copy. An enveloped rekey re-wraps only the
	// passphrase factor: the master seed, data key, and vault key are all
	// unchanged, so no server restart is needed.
	vaultMsgRekeyNoPassphraseFactor = "hush: vault: this vault is yubikey-only; there is no passphrase to rekey (nothing changed)"
	vaultMsgRekeyTouchTwice         = "hush: vault: re-wrapping the passphrase — you'll touch your YubiKey TWICE (unlock, then re-enroll)..."
	vaultMsgRekeyEnvelopedFmt       = "hush: vault: passphrase re-wrapped; seed and vault unchanged (no restart needed); snapshot=%s"
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

// errVaultRekeyPartial surfaces the post-write partial-failure outcome:
// the vault was rewritten successfully but the opt-in Keychain update
// failed (Retrieve/Delete/Store error after the vault rename). The
// snapshot remains in place as the operator's rollback artifact and
// the new vault stays live. mapErr's catch-all returns ExitErr (1) —
// hush has no separate `ExitInternalErr` symbol; the plan's reference
// to that name maps to the catch-all internal code.
var errVaultRekeyPartial = errors.New("hush: vault: rekey succeeded but Keychain update failed")

// passphraseRewrapper re-wraps an envelope's password-and-yubikey slot under a
// new passphrase in place (O(1), no seed rotation). Implemented by
// *yubikey.Store; a fake is injected in tests.
type passphraseRewrapper interface {
	RewrapPassphrase(ctx context.Context, envelope []byte, oldPassphraseFn func() ([]byte, error), newPassphrase []byte) ([]byte, error)
}

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

	// newRewrapper builds the envelope-passphrase rewrapper for enrolled (v2)
	// vaults; onTouch fires the moment the key blinks. Invoked ONLY for the
	// enveloped rekey branch, so v1 vaults never construct a ykman transport.
	newRewrapper func(onTouch func()) (passphraseRewrapper, error)

	kill        func(pid int, sig syscall.Signal) error
	readPIDFile func(path string) ([]byte, error)

	// randReader is the source for the fresh 16-byte salt minted during
	// the vault rewrite. Defaults to crypto/rand.Reader in production;
	// tests override to capture or fail salt generation.
	randReader io.Reader

	// Keychain seams — only exercised when updateKeychain=true.
	// platformACL gates the whole opt-in branch; if it returns false
	// the path is a warning/no-op even when keychain != nil.
	keychain    keychain.Keychain
	binaryPath  func() (string, error)
	platformACL func() bool

	// updateKeychain mirrors the --update-keychain CLI flag. Default
	// false → zero Keychain calls. Set by RunE before runVaultRekey.
	updateKeychain bool

	stateDirRoot string
	configPath   string
	logger       *slog.Logger
	nowFn        func() time.Time
}

// productionVaultDeps wires the real seams. Tests construct a custom
// vaultDeps directly. keychain.New can theoretically fail; production
// swallows the error and leaves deps.keychain nil so the opt-in path
// falls into the unsupported/no-op branch with a clear stderr warning
// (it never silently drops a Store).
func productionVaultDeps() *vaultDeps {
	kc, _ := keychain.New(slog.Default())
	return &vaultDeps{
		loadSecrets:      vault.LoadSecrets,
		saveVault:        vault.SaveWithSalt,
		promptPassphrase: readPassphraseTTY,
		isStdinTTY:       defaultIsTTY,
		isStdoutTTY:      defaultIsTTY,
		deriveMasterSeed: keys.DeriveMasterSeed,
		readVaultSalt:    readVaultSalt,
		newRewrapper: func(onTouch func()) (passphraseRewrapper, error) {
			s, err := yubikey.NewStoreFromConfig(defaultYkmanPath, defaultYkmanSlot)
			if err != nil {
				return nil, err
			}
			if onTouch != nil {
				s.SetTouchPrompt(onTouch)
			}
			return s, nil
		},
		kill:         syscall.Kill,
		readPIDFile:  os.ReadFile,
		randReader:   rand.Reader,
		keychain:     kc,
		binaryPath:   os.Executable,
		platformACL:  keychain.PerBinaryACLSupported,
		stateDirRoot: "",
		logger:       slog.Default(),
		nowFn:        time.Now,
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
	cmd.AddCommand(newVaultEnrollYubiKeyCmd())
	return cmd
}

// newVaultRekeyCmd builds the `hush vault rekey` leaf. The
// --update-keychain flag is wired here; Phase 4 lit it up to drive the
// post-write opt-in Keychain Retrieve→Delete→Store flow.
func newVaultRekeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rekey",
		Short: "Change the vault passphrase and rewrite secrets.vault under a fresh key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			deps := productionVaultDeps()
			deps.configPath = readGlobalFlags(cmd).configPath
			deps.updateKeychain, _ = cmd.Flags().GetBool("update-keychain")
			return runVaultRekey(cmd.Context(), out.stdout, out.stderr, os.Stdin, os.Stdout, deps)
		},
	}
	cmd.Flags().Bool("update-keychain", false, "Also update the matching macOS Keychain item with the new passphrase (opt-in; no-op on unsupported platforms or if the item is missing)")
	return cmd
}

// runVaultRekey implements the `vault rekey` flow. Phases 2–3 landed
// the TTY gating, current-passphrase authentication, new-passphrase
// validation, snapshot of the old encrypted vault, and rewrite under a
// fresh salt + new-passphrase-derived key. Phase 4 adds the read-only
// PID probe, optional Keychain update, the locked success/partial
// stderr+stdout copy, and the terminal `vault_rekeyed` audit event.
//
//nolint:gocognit,gocyclo // sequential rekey flow with TTY/auth/pass-validation dispatch
func runVaultRekey(ctx context.Context, stdout, stderr *Stream, in, stdoutFile *os.File, deps *vaultDeps) error {
	if err := enforceRekeyTTY(ctx, in, stdoutFile, deps, stderr); err != nil {
		return err
	}

	vaultPath, err := resolveVaultRekeyPath(ctx, deps)
	if err != nil {
		return err
	}

	// Enrolled (v2) vaults re-wrap the passphrase factor in place (O(1), no
	// seed rotation); the legacy v1 flow below is byte-for-byte unchanged.
	if keyslots.Exists(filepath.Dir(vaultPath)) {
		return runVaultRekeyEnveloped(ctx, stdout, stderr, in, deps, filepath.Dir(vaultPath))
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
			auditVaultRekey(ctx, deps.logger, slog.LevelWarn, "passphrase_failed", false, false, "")
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

	snapshotPath, err := snapshotVaultFile(deps, vaultPath)
	if err != nil {
		return err
	}

	freshSalt, err := mintFreshVaultSalt(deps)
	if err != nil {
		return err
	}

	newKey, err := deriveVaultRekeyKey(ctx, deps, newPass, freshSalt)
	if err != nil {
		return err
	}
	defer func() { _ = newKey.Destroy() }()

	if err := deps.saveVault(ctx, vaultPath, newKey, freshSalt, secrets); err != nil {
		return err
	}

	return finishVaultRekey(ctx, stdout, stderr, deps, vaultPath, snapshotPath, newPass)
}

// finishVaultRekey runs the post-rewrite steps: probe the PID file
// (read-only — only kill(pid, 0) is allowed), maybe update the
// Keychain, print the operator-facing success or partial-failure copy,
// and emit the terminal `vault_rekeyed` audit event with
// restart_required, keychain_updated, snapshot_path. Splits the
// post-write half out of runVaultRekey so the long sequential flow
// reads top-to-bottom.
func finishVaultRekey(ctx context.Context, stdout, stderr *Stream, deps *vaultDeps, vaultPath, snapshotPath string, newPass *securebytes.SecureBytes) error {
	restartRequired := probeAndReportPID(deps, stderr, vaultPath)
	keychainUpdated, kcErr := maybeUpdateKeychainPassphrase(ctx, deps, stderr, newPass)
	if kcErr != nil {
		_ = stderr.WriteText(vaultMsgPartialFailFmt, kcErr)
		auditVaultRekey(ctx, deps.logger, slog.LevelWarn, "success_partial", restartRequired, keychainUpdated, snapshotPath)
		return errVaultRekeyPartial
	}
	_ = stdout.WriteText(vaultMsgRekeyedFmt, snapshotPath)
	auditVaultRekey(ctx, deps.logger, slog.LevelInfo, "success", restartRequired, keychainUpdated, snapshotPath)
	return nil
}

// runVaultRekeyEnveloped re-wraps the passphrase factor of an enrolled (v2)
// vault in place. The data key — and therefore the master seed, the vault
// encryption key, and secrets.vault itself — are UNCHANGED, so a running server
// keeps working (restart_required=false). A yubikey-only vault has no passphrase
// to rekey and is refused without mutating anything. Two touches are required
// for password-and-yubikey (unlock, then re-enroll).
//
//nolint:gocognit,gocyclo // sequential enveloped-rekey flow: policy gate → prompts → snapshot → rewrap
func runVaultRekeyEnveloped(ctx context.Context, stdout, stderr *Stream, in *os.File, deps *vaultDeps, stateDir string) error {
	envelope, _, err := keyslots.Load(stateDir)
	if err != nil {
		return err
	}

	// Authoritative policy check (never trust the sidecar's advisory hint):
	// only a password-and-yubikey envelope has a passphrase factor to rekey.
	env, err := tumbler.ParseEnvelope(envelope)
	if err != nil {
		return err
	}
	if env.EffectivePolicy() != tumbler.PolicyPasswordAndYubiKey {
		_ = stderr.WriteText(vaultMsgRekeyNoPassphraseFactor)
		auditVaultRekeyEnveloped(ctx, deps.logger, slog.LevelWarn, "no_passphrase_factor", false, "")
		return nil
	}

	currentPass, newPass, err := promptRekeyPassphrases(ctx, deps, stderr, in)
	if err != nil {
		return err
	}
	defer func() { _ = currentPass.Destroy() }()
	defer func() { _ = newPass.Destroy() }()

	// Snapshot the sidecar BEFORE any mutation — the rollback artifact.
	snapshotPath, err := snapshotKeyslotsFile(deps, stateDir)
	if err != nil {
		return err
	}

	rewrapper, err := deps.newRewrapper(touchAnnouncer(stderr))
	if err != nil {
		return fmt.Errorf("this vault requires a YubiKey (is ykman installed?): %w", err)
	}

	_ = stderr.WriteText(vaultMsgRekeyTouchTwice)
	newEnvelope, err := rewrapEnvelope(ctx, rewrapper, envelope, currentPass, newPass)
	if err != nil {
		if errors.Is(err, tumbler.ErrAuthFailed) {
			auditVaultRekeyEnveloped(ctx, deps.logger, slog.LevelWarn, "passphrase_failed", false, snapshotPath)
		}
		return err
	}

	newEnv, err := tumbler.ParseEnvelope(newEnvelope)
	if err != nil {
		return err
	}
	if saveErr := keyslots.Save(stateDir, newEnvelope, newEnv.EffectivePolicy().String()); saveErr != nil {
		return saveErr // snapshot remains as the rollback artifact
	}

	return finishVaultRekeyEnveloped(ctx, stdout, stderr, deps, snapshotPath, newPass)
}

// promptRekeyPassphrases prompts current → new → confirm and runs the shared
// new-passphrase validators (length, confirmation, changed). It does NOT touch
// the vault body; the current passphrase is authenticated later by the envelope
// unlock inside RewrapPassphrase.
func promptRekeyPassphrases(ctx context.Context, deps *vaultDeps, stderr *Stream, in *os.File) (current, next *securebytes.SecureBytes, err error) {
	current, err = deps.promptPassphrase(in, stderr.w, promptVaultCurrentPassphrase)
	if err != nil {
		return nil, nil, err
	}
	next, err = deps.promptPassphrase(in, stderr.w, promptVaultNewPassphrase)
	if err != nil {
		_ = current.Destroy()
		return nil, nil, err
	}
	confirm, err := deps.promptPassphrase(in, stderr.w, promptVaultConfirmNew)
	if err != nil {
		_ = current.Destroy()
		_ = next.Destroy()
		return nil, nil, err
	}
	defer func() { _ = confirm.Destroy() }()

	if vErr := validateRekeyPassphrases(ctx, deps, stderr, current, next, confirm); vErr != nil {
		_ = current.Destroy()
		_ = next.Destroy()
		return nil, nil, vErr
	}
	return current, next, nil
}

// validateRekeyPassphrases runs the length, confirmation, and changed checks
// shared with the v1 flow.
func validateRekeyPassphrases(ctx context.Context, deps *vaultDeps, stderr *Stream, current, next, confirm *securebytes.SecureBytes) error {
	if err := enforceNewPassphraseLen(ctx, deps, stderr, next); err != nil {
		return err
	}
	if err := enforceNewPassphraseConfirmation(ctx, deps, stderr, next, confirm); err != nil {
		return err
	}
	return enforceNewPassphraseChanged(ctx, deps, stderr, current, next)
}

// rewrapEnvelope re-wraps envelope under newPass, keeping both passphrases
// mlocked through the operation. The old passphrase is cloned per call by the
// rewrapper's unlock; the new passphrase is borrowed inside its Use callback.
func rewrapEnvelope(ctx context.Context, rewrapper passphraseRewrapper, envelope []byte, currentPass, newPass *securebytes.SecureBytes) ([]byte, error) {
	oldFn := func() ([]byte, error) {
		var out []byte
		if useErr := currentPass.Use(func(b []byte) { out = append([]byte(nil), b...) }); useErr != nil {
			return nil, useErr
		}
		return out, nil
	}
	var (
		newEnvelope []byte
		rewrapErr   error
	)
	if useErr := newPass.Use(func(nw []byte) {
		newEnvelope, rewrapErr = rewrapper.RewrapPassphrase(ctx, envelope, oldFn, nw)
	}); useErr != nil {
		return nil, useErr
	}
	return newEnvelope, rewrapErr
}

// finishVaultRekeyEnveloped runs the post-rewrap steps for an enrolled vault:
// the optional Keychain update (so the next `serve` uses the new passphrase),
// the success/partial copy, and the terminal audit event. It does NOT probe the
// PID or print a restart line — an enveloped rekey leaves the seed/DEK/vault key
// unchanged, so a running server keeps working (restart_required=false).
func finishVaultRekeyEnveloped(ctx context.Context, stdout, stderr *Stream, deps *vaultDeps, snapshotPath string, newPass *securebytes.SecureBytes) error {
	keychainUpdated, kcErr := maybeUpdateKeychainPassphrase(ctx, deps, stderr, newPass)
	if kcErr != nil {
		_ = stderr.WriteText(vaultMsgPartialFailFmt, kcErr)
		auditVaultRekeyEnveloped(ctx, deps.logger, slog.LevelWarn, "success_partial", keychainUpdated, snapshotPath)
		return errVaultRekeyPartial
	}
	_ = stdout.WriteText(vaultMsgRekeyEnvelopedFmt, snapshotPath)
	auditVaultRekeyEnveloped(ctx, deps.logger, slog.LevelInfo, "success", keychainUpdated, snapshotPath)
	return nil
}

// auditVaultRekeyEnveloped emits the `vault_rekeyed` record for the enveloped
// path. restart_required is always false (seed/DEK/vault key unchanged) and the
// record carries rekey_mode="enveloped" to distinguish it from the v1 flow.
func auditVaultRekeyEnveloped(ctx context.Context, logger *slog.Logger, level slog.Level, outcome string, keychainUpdated bool, snapshotPath string) {
	logger.Log(
		ctx, level, "vault_rekeyed",
		"verb", "rekey",
		"outcome", outcome,
		"restart_required", false,
		"keychain_updated", keychainUpdated,
		"snapshot_path", snapshotPath,
		"rekey_mode", "enveloped",
	)
}

// probeAndReportPID runs the read-only PID probe against
// <stateDir>/hush.pid and prints the per-status stderr line. Returns
// restartRequired=true ONLY for pidPresent — that is the single
// status that confirms a running server with the old in-memory key.
// The probe uses kill(pid, 0); no SIGHUP/SIGTERM/SIGKILL is ever
// dispatched from the rekey path (AC-8 contract).
func probeAndReportPID(deps *vaultDeps, stderr *Stream, vaultPath string) bool {
	pidPath := filepath.Join(filepath.Dir(vaultPath), pidFilename)
	status, pid := probePIDFile(deps.readPIDFile, deps.kill, pidPath)
	switch status {
	case pidPresent:
		_ = stderr.WriteText(vaultMsgPidPresentFmt, pid)
		return true
	case pidAbsent:
		_ = stderr.WriteText(vaultMsgPidAbsent)
	case pidStale:
		_ = stderr.WriteText(vaultMsgPidStale)
	case pidNotOurUser:
		_ = stderr.WriteText(vaultMsgPidNotOurUser)
	case pidUnreadable:
		_ = stderr.WriteText(vaultMsgPidUnreadable)
	}
	return false
}

// maybeUpdateKeychainPassphrase runs the opt-in Keychain
// Retrieve→Delete→Store sequence under (hush-vault-passphrase,
// hush-server) with the running binary as the per-binary ACL. Default
// (deps.updateKeychain=false) returns (false, nil) without touching
// the Keychain at all. Unsupported platform or a missing existing
// item is a warning/no-op (returns false, nil). Any failure after the
// vault rewrite — including a Retrieve error other than
// ErrKeychainItemNotFound — propagates as the partial-failure error,
// which the caller maps to outcome=success_partial / ExitErr while
// leaving the new vault in place.
//
//nolint:gocognit,gocyclo // sequential opt-in Keychain update flow: ACL/support gate → Retrieve → Delete → Store
func maybeUpdateKeychainPassphrase(ctx context.Context, deps *vaultDeps, stderr *Stream, newPass *securebytes.SecureBytes) (bool, error) {
	if !deps.updateKeychain {
		return false, nil
	}
	if deps.platformACL == nil || !deps.platformACL() {
		_ = stderr.WriteText(vaultMsgKcUnsupported)
		return false, nil
	}
	if deps.keychain == nil {
		_ = stderr.WriteText(vaultMsgKcUnsupported)
		return false, nil
	}
	existing, err := deps.keychain.Retrieve(ctx, kcServiceVaultPassphrase, kcAccountServer)
	if err != nil {
		if errors.Is(err, keychain.ErrKeychainItemNotFound) {
			_ = stderr.WriteText(vaultMsgKcItemMissing)
			return false, nil
		}
		return false, fmt.Errorf("hush: vault: keychain retrieve: %w", err)
	}
	_ = existing.Destroy()

	binPath := ""
	if deps.binaryPath != nil {
		bp, bpErr := deps.binaryPath()
		if bpErr != nil {
			return false, fmt.Errorf("hush: vault: resolve binary path: %w", bpErr)
		}
		binPath = bp
	}

	if delErr := deps.keychain.Delete(ctx, kcServiceVaultPassphrase, kcAccountServer); delErr != nil {
		return false, fmt.Errorf("hush: vault: keychain delete: %w", delErr)
	}
	if storeErr := deps.keychain.Store(ctx, kcServiceVaultPassphrase, kcAccountServer, newPass, binPath); storeErr != nil {
		return false, fmt.Errorf("hush: vault: keychain store: %w", storeErr)
	}
	_ = stderr.WriteText(vaultMsgKcUpdated)
	return true, nil
}

// snapshotVaultFile copies the current encrypted vault to a sibling
// `secrets.vault.bak-<RFC3339>` file with 0600 perms before the new
// vault is written. Returns the absolute snapshot path. The snapshot
// is the operator's manual rollback artifact (it remains decryptable
// under the OLD passphrase) and must be created BEFORE any rewrite so
// AC-6's atomicity guarantee holds — if snapshotting fails the rekey
// aborts with the vault file untouched.
func snapshotVaultFile(deps *vaultDeps, vaultPath string) (string, error) {
	return snapshotFile(deps, vaultPath)
}

// snapshotKeyslotsFile copies the keyslots sidecar to a sibling
// `keyslots.json.bak-<RFC3339>` before an enveloped rekey rewrites it. This is
// the rollback artifact for the enveloped path (the vault body is untouched).
func snapshotKeyslotsFile(deps *vaultDeps, stateDir string) (string, error) {
	return snapshotFile(deps, keyslots.Path(stateDir))
}

// snapshotFile copies srcPath to a sibling `<name>.bak-<RFC3339>` with 0600
// perms via O_EXCL (no silent overwrite), returning the absolute snapshot path.
// It is created BEFORE any rewrite so the operator always has a rollback
// artifact; if snapshotting fails the caller aborts with the source untouched.
func snapshotFile(deps *vaultDeps, srcPath string) (string, error) {
	body, err := os.ReadFile(srcPath) //nolint:gosec // srcPath is resolved through deps.stateDirRoot or loaded server config
	if err != nil {
		return "", fmt.Errorf("hush: vault: read for snapshot: %w", err)
	}
	timestamp := deps.nowFn().UTC().Format(time.RFC3339)
	snapPath := srcPath + ".bak-" + timestamp
	// O_EXCL guards against the (theoretical) RFC3339-second-collision
	// case where two rekeys land in the same wall-clock second; the
	// caller surfaces the error rather than silently overwriting a
	// prior snapshot.
	f, err := os.OpenFile(snapPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // snapshot sibling of vaultPath; 0600 enforced via explicit chmod below
	if err != nil {
		return "", fmt.Errorf("hush: vault: create snapshot: %w", err)
	}
	if _, writeErr := f.Write(body); writeErr != nil {
		_ = f.Close()
		_ = os.Remove(snapPath)
		return "", fmt.Errorf("hush: vault: write snapshot: %w", writeErr)
	}
	if syncErr := f.Sync(); syncErr != nil {
		_ = f.Close()
		_ = os.Remove(snapPath)
		return "", fmt.Errorf("hush: vault: sync snapshot: %w", syncErr)
	}
	if closeErr := f.Close(); closeErr != nil {
		_ = os.Remove(snapPath)
		return "", fmt.Errorf("hush: vault: close snapshot: %w", closeErr)
	}
	// Belt-and-braces: neutralize any umask effect on the new file
	// (mirrors vault.SaveWithSalt's post-rename chmod).
	if err := os.Chmod(snapPath, 0o600); err != nil {
		return "", fmt.Errorf("hush: vault: chmod snapshot: %w", err)
	}
	return snapPath, nil
}

// mintFreshVaultSalt reads exactly vaultSaltLen bytes from the deps
// random source. io.ReadFull turns a short read into a fatal error so
// the salt is never partial.
func mintFreshVaultSalt(deps *vaultDeps) ([]byte, error) {
	salt := make([]byte, vaultSaltLen)
	if _, err := io.ReadFull(deps.randReader, salt); err != nil {
		return nil, fmt.Errorf("hush: vault: mint salt: %w", err)
	}
	return salt, nil
}

// enforceRekeyTTY refuses if stdin or stdout is not an interactive
// terminal. Emits the locked stderr line and a `vault_rekeyed`
// audit record so an operator monitoring stderr sees the refusal AND
// the audit log captures the attempt.
func enforceRekeyTTY(ctx context.Context, in, stdoutFile *os.File, deps *vaultDeps, stderr *Stream) error {
	if deps.isStdinTTY(in) && deps.isStdoutTTY(stdoutFile) {
		return nil
	}
	_ = stderr.WriteText(vaultMsgNoTTY)
	auditVaultRekey(ctx, deps.logger, slog.LevelWarn, "tty_refused", false, false, "")
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
	auditVaultRekey(ctx, deps.logger, slog.LevelWarn, "passphrase_too_short", false, false, "")
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
	auditVaultRekey(ctx, deps.logger, slog.LevelWarn, "new_passphrase_mismatch", false, false, "")
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
	auditVaultRekey(ctx, deps.logger, slog.LevelWarn, "new_passphrase_unchanged", false, false, "")
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

// auditVaultRekey emits the canonical `vault_rekeyed` audit record.
// Every terminal path in the rekey flow funnels through this function
// so the structured event always carries the full AC-11 attribute set
// (verb, outcome, restart_required, keychain_updated, snapshot_path).
// Early-failure call sites pass zero values for the post-write fields
// (snapshot_path="", booleans false) — those records still carry the
// full shape so log consumers can rely on the schema.
func auditVaultRekey(ctx context.Context, logger *slog.Logger, level slog.Level, outcome string, restartRequired, keychainUpdated bool, snapshotPath string) {
	logger.Log(
		ctx, level, "vault_rekeyed",
		"verb", "rekey",
		"outcome", outcome,
		"restart_required", restartRequired,
		"keychain_updated", keychainUpdated,
		"snapshot_path", snapshotPath,
	)
}
