package cli

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	tumbler "github.com/mrz1836/go-tumbler"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/keyslots"
	"github.com/mrz1836/hush/internal/vault/securebytes"
	"github.com/mrz1836/hush/internal/yubikey"
	"github.com/spf13/cobra"
)

// parseEnrollPolicy maps the --policy flag to a tumbler policy.
func parseEnrollPolicy(s string) (tumbler.Policy, error) {
	switch s {
	case "", "password-and-yubikey":
		return tumbler.PolicyPasswordAndYubiKey, nil
	case "yubikey-only":
		return tumbler.PolicyYubiKeyOnly, nil
	default:
		return tumbler.PolicyInvalid, fmt.Errorf("unknown policy %q (use password-and-yubikey or yubikey-only)", s)
	}
}

// newVaultEnrollYubiKeyCmd builds the `hush vault enroll-yubikey` leaf.
func newVaultEnrollYubiKeyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enroll-yubikey",
		Short: "Protect the vault with a YubiKey (password+yubikey or yubikey-only)",
		Long: "Re-encrypt the vault under a new random master seed wrapped by a\n" +
			"YubiKey keyslot, so the passphrase alone can no longer open it. A\n" +
			"pre-migration snapshot is written for rollback. The server must be\n" +
			"restarted after migration to pick up the new key.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			deps := productionVaultDeps()
			deps.configPath = readGlobalFlags(cmd).configPath
			policyStr, _ := cmd.Flags().GetString("policy")
			withRecovery, _ := cmd.Flags().GetBool("recovery-code")
			return runVaultEnrollYubiKey(cmd.Context(), out.stdout, out.stderr, os.Stdin, os.Stdout, deps, policyStr, withRecovery)
		},
	}
	cmd.Flags().String("policy", "password-and-yubikey", "unlock policy: password-and-yubikey | yubikey-only")
	cmd.Flags().Bool("recovery-code", false, "also generate a one-time printed recovery code")
	return cmd
}

// runVaultEnrollYubiKey mirrors runVaultRekey's front half (TTY gate, current
// passphrase, load secrets, snapshot) then hands off to migrateVaultToYubiKey.
//
//nolint:gocognit,gocyclo // sequential migration flow with clear guardrails
func runVaultEnrollYubiKey(ctx context.Context, stdout, stderr *Stream, in, stdoutFile *os.File, deps *vaultDeps, policyStr string, withRecovery bool) error {
	policy, err := parseEnrollPolicy(policyStr)
	if err != nil {
		return err
	}
	if err = enforceRekeyTTY(ctx, in, stdoutFile, deps, stderr); err != nil {
		return err
	}
	vaultPath, err := resolveVaultRekeyPath(ctx, deps)
	if err != nil {
		return err
	}
	if keyslots.Exists(filepath.Dir(vaultPath)) {
		return fmt.Errorf("this vault already has a YubiKey envelope enrolled")
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
		return err
	}
	defer destroyVaultRekeySecrets(secrets)

	snapshotPath, err := snapshotVaultFile(deps, vaultPath)
	if err != nil {
		return err
	}

	store, err := yubikey.NewStoreFromConfig(defaultYkmanPath, defaultYkmanSlot)
	if err != nil {
		return fmt.Errorf("this migration requires a YubiKey (is ykman installed?): %w", err)
	}
	store.SetTouchPrompt(func() {
		_ = stdout.WriteText("\n👆  Touch your YubiKey now — it's blinking...\n")
	})

	opts := yubikey.EnrollOptions{WithRecovery: withRecovery}
	if policy == tumbler.PolicyPasswordAndYubiKey {
		if useErr := currentPass.Use(func(b []byte) {
			opts.Passphrase = append([]byte(nil), b...)
		}); useErr != nil {
			return useErr
		}
		defer zeroBytes(opts.Passphrase)
	}

	_ = stdout.WriteText("hush: vault: enrolling — you'll be asked to touch your key in a moment...\n")
	stateDir := filepath.Dir(vaultPath)
	recoveryCode, err := migrateVaultToYubiKey(ctx, vaultPath, stateDir, secrets, store, policy, opts)
	if err != nil {
		return fmt.Errorf("migration failed (vault snapshot at %s): %w", snapshotPath, err)
	}

	_ = stdout.WriteText("hush: vault: enrolled YubiKey (policy=%s); snapshot=%s; restart the server\n", policy.String(), snapshotPath)
	if recoveryCode != "" {
		_ = stdout.WriteText("hush: vault: RECOVERY CODE (write down now, shown once): %s\n", recoveryCode)
	}
	return nil
}

// seedEnroller is the yubikey.Store capability the migration needs; an
// interface so tests inject a FakeTransport-backed store.
type seedEnroller interface {
	Enroll(ctx context.Context, policy tumbler.Policy, opts yubikey.EnrollOptions) (*yubikey.EnrollResult, error)
}

// migrateVaultToYubiKey re-encrypts the vault at vaultPath under a NEW random
// master seed wrapped by a YubiKey keyslots envelope, stamps the vault with
// format version 0x02, and writes the keyslots sidecar under stateDir.
//
// secrets are the current vault contents (already loaded via the old
// passphrase-derived key by the caller). On success the vault can no longer be
// opened by the passphrase alone — recovery requires the envelope (YubiKey,
// plus passphrase for 2FA) or the returned recovery code.
//
// The caller is responsible for snapshotting the vault first (rollback) and
// for destroying the loaded secrets afterward.
func migrateVaultToYubiKey(
	ctx context.Context,
	vaultPath, stateDir string,
	secrets []vault.Secret,
	store seedEnroller,
	policy tumbler.Policy,
	opts yubikey.EnrollOptions,
) (recoveryCode string, err error) {
	// 1. Generate the new random master seed + envelope (this triggers touch).
	res, err := store.Enroll(ctx, policy, opts)
	if err != nil {
		return "", fmt.Errorf("enroll: %w", err)
	}
	defer zeroBytes(res.MasterSeed)

	// 2. Derive the new vault encryption key from the new master seed.
	rawKey, err := keys.DeriveVaultEncKey(res.MasterSeed)
	if err != nil {
		return "", fmt.Errorf("derive vault key: %w", err)
	}
	defer zeroBytes(rawKey)
	vaultEncKey, err := securebytes.New(rawKey)
	if err != nil {
		return "", fmt.Errorf("wrap vault key: %w", err)
	}
	defer func() { _ = vaultEncKey.Destroy() }()

	// 3. Re-encrypt the vault under the new key with a fresh salt and v2.
	salt := make([]byte, 16)
	if _, rerr := rand.Read(salt); rerr != nil {
		return "", fmt.Errorf("rand salt: %w", rerr)
	}
	if serr := vault.SaveWithSaltAndVersion(ctx, vaultPath, vaultEncKey, salt, vault.Version2, secrets); serr != nil {
		return "", fmt.Errorf("re-encrypt vault: %w", serr)
	}

	// 4. Persist the keyslots sidecar. If this fails after the vault was
	// re-encrypted, the caller must restore the snapshot.
	if kerr := keyslots.Save(stateDir, res.Envelope, policy.String()); kerr != nil {
		return "", fmt.Errorf("save keyslots: %w", kerr)
	}
	return res.RecoveryCode, nil
}
