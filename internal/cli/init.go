package cli

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Locked literal-text strings (contracts/cli-init.md §2.3 / §3.3).
// Tests assert byte-equal on these messages.
const (
	initMsgNoTTY                = "hush: init: stdin must be an interactive terminal"
	initMsgPassphraseTooShort   = "hush: init: passphrase must be at least 12 characters"
	initMsgPassphraseMismatch   = "hush: init: passphrase confirmation does not match"
	initMsgVaultExistsFmt       = "hush: init: vault already exists at %s"
	initMsgConfigExistsFmt      = "hush: init: config already exists at %s"
	initMsgKeychainExistsFmt    = "hush: init: keychain item already exists for service=%s account=%s"
	initMsgPlatformUnsupported  = "hush: init: platform %s has no per-binary keychain ACL; init refuses to run"
	initMsgMissingMachineIndex  = "hush: init: missing required flag: --machine-index"
	initMsgMachineIndexInvalid  = "hush: init: --machine-index must be a non-negative integer"
	initMsgFieldRequiredFmt     = "hush: init: %s is required"
	initMsgKeychainStoreFailFmt = "hush: init: keychain store failed: %v"
	initMsgKeychainSkipped      = "hush: init: explicit state dir set; skipped Keychain writes. Set HUSH_DISCORD_BOT_TOKEN when running serve."
	initMsgWriteFailFmt         = "hush: init: write %s: %v"
	initMsgServerComplete       = "hush: init: server bootstrap complete"
)

// Locked prompt labels (contracts/cli-init.md §2.2 / §3.2).
const (
	promptVaultPassphrase = "Vault passphrase: "
	promptConfirmVault    = "Confirm vault passphrase: "
	promptListenAddr      = "Listen address (e.g. 100.96.10.4:7743): "
	promptOwnerID         = "Discord owner ID (snowflake): "
	promptApplicationID   = "Discord application ID (snowflake): "
	promptBotToken        = "Discord bot token: " //nolint:gosec // prompt label, not a credential
)

// Keychain (service, account) pairs, locked at SDD-15 (data-model §1.3).
const (
	kcServiceVaultPassphrase = "hush-vault-passphrase"
	kcServiceClient          = "hush-client"
	kcAccountServer          = "hush-server"
)

const minPassphraseLen = 12

// initDeps groups the testable seams threaded into init's run paths.
// All fields have a single production binding; tests substitute
// programmable replacements.
type initDeps struct {
	keychain     keychain.Keychain
	binaryPath   func() (string, error)
	randReader   io.Reader
	stateDirRoot string
	nowFn        func() time.Time
	platformACL  func() bool

	serverNonInteractive bool
	serverPassphrase     string
	serverBotToken       string
	serverInputs         serverInputs
	clientNonInteractive bool
	clientPassphrase     string
	clientRegistry       string
	clientKeyFile        string

	// promptSecret reads a no-echo line from stdin in production;
	// tests substitute a deterministic reader.
	promptSecret func(in *os.File, prompt io.Writer, label string) (*securebytes.SecureBytes, error)
	// promptLine reads a non-secret line from stdin in production;
	// tests substitute a deterministic reader.
	promptLine func(in *os.File, prompt io.Writer, label string) (string, error)
	// isTTY reports whether stdin is an interactive terminal.
	isTTY func(*os.File) bool
	// deriveMasterSeed is the Argon2id master-seed derivation.
	// Tests substitute a fast stub to avoid 1s+ Argon2 cost on
	// every test; the stub MUST still validate the passphrase
	// length contract.
	deriveMasterSeed func(ctx context.Context, passphrase, salt []byte) ([]byte, error)
}

// productionInitDeps wires the production seams. Tests construct a
// custom initDeps directly.
func productionInitDeps() (*initDeps, error) {
	kc, err := keychain.New(slog.Default())
	if err != nil {
		return nil, err
	}
	return &initDeps{
		keychain:         kc,
		binaryPath:       os.Executable,
		randReader:       rand.Reader,
		stateDirRoot:     "",
		nowFn:            time.Now,
		platformACL:      keychain.PerBinaryACLSupported,
		promptSecret:     readPassphraseTTY,
		promptLine:       readLineFromTTY,
		isTTY:            defaultIsTTY,
		deriveMasterSeed: keys.DeriveMasterSeed,
	}, nil
}

func defaultIsTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// newInitCmd builds the `hush init` parent. It has no Run* —
// invoking `hush init` without a subcommand prints help and exits
// non-zero (default cobra behavior with SilenceUsage=false here).
func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Bootstrap the vault host or enroll an agent machine",
	}
	cmd.AddCommand(newInitServerCmd())
	cmd.AddCommand(newInitClientCmd())
	return cmd
}

type serverBootstrapInput struct {
	VaultPassphrase string `json:"vault_passphrase"`
	DiscordBotToken string `json:"discord_bot_token"`
}

type clientBootstrapInput struct {
	VaultPassphrase string `json:"vault_passphrase"`
}

func readServerBootstrapInput(path string) (serverBootstrapInput, error) {
	if strings.TrimSpace(path) == "" {
		return serverBootstrapInput{}, fmt.Errorf("%w: --input-file", errMissingFlag)
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied bootstrap file path
	if err != nil {
		return serverBootstrapInput{}, fmt.Errorf("hush/cli: init: read input file: %w", err)
	}
	var input serverBootstrapInput
	if err := json.Unmarshal(body, &input); err != nil {
		return serverBootstrapInput{}, fmt.Errorf("hush/cli: init: decode input file: %w", err)
	}
	return input, nil
}

func readClientBootstrapInput(path string) (clientBootstrapInput, error) {
	if strings.TrimSpace(path) == "" {
		return clientBootstrapInput{}, fmt.Errorf("%w: --input-file", errMissingFlag)
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied bootstrap file path
	if err != nil {
		return clientBootstrapInput{}, fmt.Errorf("hush/cli: init: read input file: %w", err)
	}
	var input clientBootstrapInput
	if err := json.Unmarshal(body, &input); err != nil {
		return clientBootstrapInput{}, fmt.Errorf("hush/cli: init: decode input file: %w", err)
	}
	return input, nil
}

// newInitServerCmd builds the `hush init server` subcommand. The cobra
// command tree is the contract; no exported symbols.
func newInitServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Bootstrap the vault host (creates vault, config, keychain entries)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := productionInitDeps()
			if err != nil {
				return err
			}
			deps.serverInputs.listenAddr, _ = cmd.Flags().GetString("listen-addr")
			deps.serverInputs.ownerID, _ = cmd.Flags().GetString("discord-owner-id")
			deps.serverInputs.applicationID, _ = cmd.Flags().GetString("discord-application-id")
			deps.serverInputs.stateDir, _ = cmd.Flags().GetString("state-dir")
			deps.serverInputs.approvalChannelID, _ = cmd.Flags().GetString("discord-approval-channel-id")
			deps.serverInputs.auditChannelID, _ = cmd.Flags().GetString("discord-audit-channel-id")
			if strings.TrimSpace(deps.serverInputs.stateDir) != "" {
				deps.stateDirRoot = deps.serverInputs.stateDir
			}
			if nonInteractive, _ := cmd.Flags().GetBool("non-interactive"); nonInteractive {
				deps.serverNonInteractive = true
				inputFile, _ := cmd.Flags().GetString("input-file")
				input, inputErr := readServerBootstrapInput(inputFile)
				if inputErr != nil {
					return inputErr
				}
				deps.serverPassphrase = input.VaultPassphrase
				deps.serverBotToken = input.DiscordBotToken
			}
			out := outputFromCmd(cmd)
			return runInitServer(cmd.Context(), out.stdout, out.stderr, os.Stdin, deps)
		},
	}
	cmd.Flags().Bool("non-interactive", false, "Read server bootstrap inputs from flags/environment instead of TTY prompts")
	cmd.Flags().String("state-dir", "", "State directory for generated vault/config (default ~/.hush)")
	cmd.Flags().String("listen-addr", "", "Server listen address (required with --non-interactive)")
	cmd.Flags().String("discord-owner-id", "", "Discord owner snowflake (required with --non-interactive)")
	cmd.Flags().String("discord-application-id", "", "Discord application snowflake (required with --non-interactive)")
	cmd.Flags().String("discord-approval-channel-id", "", "Discord approval channel snowflake (optional; empty sends approvals by DM)")
	cmd.Flags().String("discord-audit-channel-id", "", "Discord audit mirror channel snowflake (optional)")
	cmd.Flags().String("input-file", "", "0600 JSON bootstrap input for --non-interactive")
	return cmd
}

// newInitClientCmd builds the `hush init client` subcommand. The cobra
// command tree is the contract; no exported symbols.
func newInitClientCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Enroll an agent machine (derives client key, prints fingerprint)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := productionInitDeps()
			if err != nil {
				return err
			}
			if nonInteractive, _ := cmd.Flags().GetBool("non-interactive"); nonInteractive {
				deps.clientNonInteractive = true
				inputFile, _ := cmd.Flags().GetString("input-file")
				input, inputErr := readClientBootstrapInput(inputFile)
				if inputErr != nil {
					return inputErr
				}
				deps.clientPassphrase = input.VaultPassphrase
			}
			deps.clientRegistry, _ = cmd.Flags().GetString("client-registry")
			deps.clientKeyFile, _ = cmd.Flags().GetString("client-key-file")
			out := outputFromCmd(cmd)
			return runInitClient(cmd.Context(), out.stdout, out.stderr, os.Stdin, cmd, deps)
		},
	}
	// Stored as a string so we can detect "missing" vs "0" before
	// flag parsing returns. Cobra does not distinguish unset from
	// zero on numeric types.
	cmd.Flags().String("machine-index", "", "Per-machine identifier (uint32) for the client key derivation")
	cmd.Flags().Bool("non-interactive", false, "Read client bootstrap inputs from a 0600 JSON file instead of TTY prompts")
	cmd.Flags().String("input-file", "", "0600 JSON bootstrap input for --non-interactive")
	cmd.Flags().String("client-registry", "", "Optional server client registry JSON file to append/update with this client's public key")
	cmd.Flags().String("client-key-file", "", "Optional 0600 smoke-test client key file used when macOS Keychain writes are unavailable")
	return cmd
}

// runInitServer is the orchestration entry-point for `hush init server`.
// All output goes to stderr (operator messages); stdout is intentionally
// unused so machine-piped consumers see an empty data stream on success.
//
//nolint:gocognit,gocyclo,cyclop // sequential bootstrap flow; complexity is structural per data-model §1
func runInitServer(ctx context.Context, _, stderr *Stream, in *os.File, deps *initDeps) error {
	if !deps.platformACL() {
		_ = stderr.WriteText(initMsgPlatformUnsupported, runtime.GOOS)
		return fmt.Errorf("%w: %s", errPlatformACLUnsupported, runtime.GOOS)
	}
	var pass *securebytes.SecureBytes
	var botToken *securebytes.SecureBytes
	var err error
	var listenAddr, ownerID, appID string
	var approvalChannelID, auditChannelID string

	if deps.serverNonInteractive {
		pass, err = securebytes.New([]byte(deps.serverPassphrase))
		if err != nil {
			return err
		}
		botToken, err = securebytes.New([]byte(deps.serverBotToken))
		if err != nil {
			_ = pass.Destroy()
			return err
		}
		listenAddr = strings.TrimSpace(deps.serverInputs.listenAddr)
		ownerID = strings.TrimSpace(deps.serverInputs.ownerID)
		appID = strings.TrimSpace(deps.serverInputs.applicationID)
		approvalChannelID = strings.TrimSpace(deps.serverInputs.approvalChannelID)
		auditChannelID = strings.TrimSpace(deps.serverInputs.auditChannelID)
	} else {
		if !deps.isTTY(in) {
			_ = stderr.WriteText(initMsgNoTTY)
			return errNoTTY
		}

		// 1. Passphrase + confirmation.
		pass, err = deps.promptSecret(in, stderr.w, promptVaultPassphrase)
		if err != nil {
			return err
		}
		if pass.Len() < minPassphraseLen {
			_ = pass.Destroy()
			_ = stderr.WriteText(initMsgPassphraseTooShort)
			return errPassphraseTooShort
		}
		confirm, confirmErr := deps.promptSecret(in, stderr.w, promptConfirmVault)
		if confirmErr != nil {
			_ = pass.Destroy()
			return confirmErr
		}
		equal, cmpErr := secureBytesEqual(pass, confirm)
		_ = confirm.Destroy()
		if cmpErr != nil {
			_ = pass.Destroy()
			return cmpErr
		}
		if !equal {
			_ = pass.Destroy()
			_ = stderr.WriteText(initMsgPassphraseMismatch)
			return errPassphraseMismatch
		}

		// 2. Operator-supplied non-secret fields (FR-009: no defaults).
		// If flags supplied these values, honor them and only prompt for
		// the missing fields. Secrets remain TTY-only in interactive mode.
		listenAddr = strings.TrimSpace(deps.serverInputs.listenAddr)
		ownerID = strings.TrimSpace(deps.serverInputs.ownerID)
		appID = strings.TrimSpace(deps.serverInputs.applicationID)
		approvalChannelID = strings.TrimSpace(deps.serverInputs.approvalChannelID)
		auditChannelID = strings.TrimSpace(deps.serverInputs.auditChannelID)

		if listenAddr == "" {
			listenAddr, err = promptRequired(deps.promptLine, in, stderr.w, promptListenAddr, "listen_addr")
		}
		if err != nil {
			_ = pass.Destroy()
			_ = stderr.WriteText(initMsgFieldRequiredFmt, "listen_addr")
			return err
		}
		if ownerID == "" {
			ownerID, err = promptRequired(deps.promptLine, in, stderr.w, promptOwnerID, "discord_owner_id")
		}
		if err != nil {
			_ = pass.Destroy()
			_ = stderr.WriteText(initMsgFieldRequiredFmt, "discord_owner_id")
			return err
		}
		if appID == "" {
			appID, err = promptRequired(deps.promptLine, in, stderr.w, promptApplicationID, "application_id")
		}
		if err != nil {
			_ = pass.Destroy()
			_ = stderr.WriteText(initMsgFieldRequiredFmt, "application_id")
			return err
		}
		botToken, err = deps.promptSecret(in, stderr.w, promptBotToken)
		if err != nil {
			_ = pass.Destroy()
			return err
		}
	}
	defer func() { _ = pass.Destroy() }()
	defer func() { _ = botToken.Destroy() }()
	if pass.Len() < minPassphraseLen {
		_ = stderr.WriteText(initMsgPassphraseTooShort)
		return errPassphraseTooShort
	}
	if botToken.Len() == 0 {
		_ = stderr.WriteText(initMsgFieldRequiredFmt, "discord_bot_token")
		return errMissingFlag
	}
	if listenAddr == "" {
		_ = stderr.WriteText(initMsgFieldRequiredFmt, "listen_addr")
		return errMissingFlag
	}
	if ownerID == "" {
		_ = stderr.WriteText(initMsgFieldRequiredFmt, "discord_owner_id")
		return errMissingFlag
	}
	if appID == "" {
		_ = stderr.WriteText(initMsgFieldRequiredFmt, "application_id")
		return errMissingFlag
	}

	// 3. Resolve target paths.
	stateDir, err := resolveStateDir(deps.stateDirRoot)
	if err != nil {
		return err
	}
	vaultPath := filepath.Join(stateDir, "secrets.vault")
	configPath := filepath.Join(stateDir, "config.toml")
	keychainItems := defaultServerKeychainItems()

	// 4. Existence guards (vault, config, both keychain items).
	if guardErr := guardFileAbsent(vaultPath, errVaultExists, initMsgVaultExistsFmt, stderr); guardErr != nil {
		return guardErr
	}
	if guardErr := guardFileAbsent(configPath, errConfigExists, initMsgConfigExistsFmt, stderr); guardErr != nil {
		return guardErr
	}
	if guardErr := guardKeychainAbsent(ctx, deps.keychain, keychainItems.vaultPassphraseService, kcAccountServer, stderr); guardErr != nil {
		return guardErr
	}
	if guardErr := guardKeychainAbsent(ctx, deps.keychain, keychainItems.discordService, kcAccountServer, stderr); guardErr != nil {
		return guardErr
	}

	// 5. Resolve binary path for keychain ACL.
	binPath, err := deps.binaryPath()
	if err != nil {
		return fmt.Errorf("hush/cli: init: resolve binary path: %w", err)
	}

	// 6. Derive master seed and vault encryption key.
	salt := make([]byte, 16)
	if _, saltErr := io.ReadFull(deps.randReader, salt); saltErr != nil {
		return fmt.Errorf("hush/cli: init: salt: %w", saltErr)
	}
	var masterSeed []byte
	var deriveErr error
	if useErr := pass.Use(func(b []byte) {
		masterSeed, deriveErr = deps.deriveMasterSeed(ctx, b, salt)
	}); useErr != nil {
		return useErr
	}
	if deriveErr != nil {
		return deriveErr
	}
	defer zeroBytes(masterSeed)

	vaultEncRaw, err := keys.DeriveVaultEncKey(masterSeed)
	if err != nil {
		return err
	}
	vaultEncKey, err := securebytes.New(vaultEncRaw)
	if err != nil {
		return err
	}
	defer func() { _ = vaultEncKey.Destroy() }()

	// 7. Ensure the state directory exists at 0700, then write the
	// empty vault.
	if dirErr := ensureStateDir(stateDir); dirErr != nil {
		return dirErr
	}
	if saveErr := vault.SaveWithSalt(ctx, vaultPath, vaultEncKey, salt, []vault.Secret{}); saveErr != nil {
		return saveErr
	}

	// 8. Generate path_prefix and write config.toml atomically.
	pathPrefix, err := generatePathPrefix(deps.randReader)
	if err != nil {
		return err
	}
	cfgBody := buildServerDecodedFromDefaults(serverInputs{
		listenAddr:        listenAddr,
		pathPrefix:        pathPrefix,
		ownerID:           ownerID,
		applicationID:     appID,
		stateDir:          stateDir,
		approvalChannelID: approvalChannelID,
		auditChannelID:    auditChannelID,
		botTokenKeychain:  keychainItems.discordService,
	})
	if err := writeConfigTOMLAtomic(configPath, cfgBody); err != nil {
		_ = stderr.WriteText(initMsgWriteFailFmt, configPath, err)
		return err
	}

	// 9. Round-trip-validate the generated config.
	if _, err := config.LoadServer(ctx, configPath); err != nil {
		return fmt.Errorf("hush/cli: init: round-trip-validate config: %w", err)
	}

	// 10. Store the keychain items. Explicit-state bootstrap is the
	// learning/smoke path, so skip Keychain writes entirely: macOS
	// security prompts for "password data for new item" in some terminal
	// contexts, which is confusing and unnecessary because serve already
	// supports HUSH_DISCORD_BOT_TOKEN and the operator keeps the vault
	// passphrase. Default-state bootstrap remains strict.
	if strings.TrimSpace(deps.serverInputs.stateDir) != "" {
		_ = stderr.WriteText(initMsgKeychainSkipped)
		_ = stderr.WriteText(initMsgServerComplete)
		return nil
	}
	if err := deps.keychain.Store(ctx, keychainItems.vaultPassphraseService, kcAccountServer, pass, binPath); err != nil {
		_ = stderr.WriteText(initMsgKeychainStoreFailFmt, err)
		return err
	}
	if err := deps.keychain.Store(ctx, keychainItems.discordService, kcAccountServer, botToken, binPath); err != nil {
		_ = stderr.WriteText(initMsgKeychainStoreFailFmt, err)
		return err
	}

	_ = stderr.WriteText(initMsgServerComplete)
	return nil
}

// runInitClient is the orchestration entry-point for `hush init client`.
//
//nolint:gocognit,gocyclo,cyclop // sequential bootstrap flow; complexity is structural
func runInitClient(ctx context.Context, stdout, stderr *Stream, in *os.File, cmd *cobra.Command, deps *initDeps) error {
	if !deps.platformACL() {
		_ = stderr.WriteText(initMsgPlatformUnsupported, runtime.GOOS)
		return fmt.Errorf("%w: %s", errPlatformACLUnsupported, runtime.GOOS)
	}

	rawIdx, _ := cmd.Flags().GetString("machine-index")
	if strings.TrimSpace(rawIdx) == "" {
		_ = stderr.WriteText(initMsgMissingMachineIndex)
		return fmt.Errorf("%w: --machine-index", errMissingFlag)
	}
	machineIndex, err := parseMachineIndex(rawIdx)
	if err != nil {
		_ = stderr.WriteText(initMsgMachineIndexInvalid)
		return fmt.Errorf("%w: --machine-index value %q", errMissingFlag, rawIdx)
	}

	var pass *securebytes.SecureBytes
	if deps.clientNonInteractive {
		pass, err = securebytes.New([]byte(deps.clientPassphrase))
		if err != nil {
			return err
		}
	} else {
		if !deps.isTTY(in) {
			_ = stderr.WriteText(initMsgNoTTY)
			return errNoTTY
		}

		pass, err = deps.promptSecret(in, stderr.w, promptVaultPassphrase)
		if err != nil {
			return err
		}
		if pass.Len() < minPassphraseLen {
			_ = pass.Destroy()
			_ = stderr.WriteText(initMsgPassphraseTooShort)
			return errPassphraseTooShort
		}
		confirm, confirmErr := deps.promptSecret(in, stderr.w, promptConfirmVault)
		if confirmErr != nil {
			_ = pass.Destroy()
			return confirmErr
		}
		equal, cmpErr := secureBytesEqual(pass, confirm)
		_ = confirm.Destroy()
		if cmpErr != nil {
			_ = pass.Destroy()
			return cmpErr
		}
		if !equal {
			_ = pass.Destroy()
			_ = stderr.WriteText(initMsgPassphraseMismatch)
			return errPassphraseMismatch
		}
	}
	defer func() { _ = pass.Destroy() }()
	if pass.Len() < minPassphraseLen {
		_ = stderr.WriteText(initMsgPassphraseTooShort)
		return errPassphraseTooShort
	}

	account := fmt.Sprintf("machine-%d", machineIndex)
	if guardErr := guardKeychainAbsent(ctx, deps.keychain, kcServiceClient, account, stderr); guardErr != nil {
		return guardErr
	}

	binPath, err := deps.binaryPath()
	if err != nil {
		return fmt.Errorf("hush/cli: init: resolve binary path: %w", err)
	}

	// Salt for client-mode derivation: client mode does not write a
	// vault file, so the salt is sourced fresh from randReader. The
	// derivation is deterministic for a given (passphrase, salt,
	// machine-index) tuple; the operator's passphrase is the source
	// of operator-side entropy. Per data-model §3.4 the same master
	// seed produces the same per-machine key.
	salt := make([]byte, 16)
	if _, saltErr := io.ReadFull(deps.randReader, salt); saltErr != nil {
		return fmt.Errorf("hush/cli: init: salt: %w", saltErr)
	}
	var masterSeed []byte
	var deriveErr error
	if useErr := pass.Use(func(b []byte) {
		masterSeed, deriveErr = deps.deriveMasterSeed(ctx, b, salt)
	}); useErr != nil {
		return useErr
	}
	if deriveErr != nil {
		return deriveErr
	}
	defer zeroBytes(masterSeed)

	clientKey, err := keys.DeriveClientKey(masterSeed, machineIndex)
	if err != nil {
		return err
	}
	priv, err := serializeECPrivKey(clientKey)
	if err != nil {
		return err
	}
	defer func() { _ = priv.Destroy() }()

	if err := deps.keychain.Store(ctx, kcServiceClient, account, priv, binPath); err != nil {
		if strings.TrimSpace(deps.clientKeyFile) == "" {
			_ = stderr.WriteText(initMsgKeychainStoreFailFmt, err)
			return err
		}
		_ = stderr.WriteText(initMsgKeychainStoreFailFmt, err)
	}
	if strings.TrimSpace(deps.clientKeyFile) != "" {
		if err := writeClientKeyFile(deps.clientKeyFile, priv); err != nil {
			return err
		}
	}

	if strings.TrimSpace(deps.clientRegistry) != "" {
		if err := upsertClientRegistry(deps.clientRegistry, keys.PublicKeyFingerprint(&clientKey.PublicKey), &clientKey.PublicKey); err != nil {
			return err
		}
	}

	fingerprint := sshStyleFingerprint(&clientKey.PublicKey)
	if _, err := io.WriteString(stdout.w, fingerprint+"\n"); err != nil {
		return fmt.Errorf("hush/cli: init: write fingerprint: %w", err)
	}
	return nil
}

// parseMachineIndex parses a string into a uint32. Negative or
// out-of-range inputs return an error.
func parseMachineIndex(s string) (uint32, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(n), nil
}

type clientRegistryJSONEntry struct {
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
}

func upsertClientRegistry(path, fingerprint string, pub *ecdsa.PublicKey) error {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied registry path
	var entries []clientRegistryJSONEntry
	switch {
	case err == nil:
		if len(strings.TrimSpace(string(raw))) > 0 {
			if jerr := json.Unmarshal(raw, &entries); jerr != nil {
				return fmt.Errorf("hush/cli: init: parse client registry: %w", jerr)
			}
		}
	case errors.Is(err, os.ErrNotExist):
		entries = nil
	default:
		return fmt.Errorf("hush/cli: init: read client registry: %w", err)
	}

	next := clientRegistryJSONEntry{
		Fingerprint: fingerprint,
		PublicKey:   compressedPublicKeyHex(pub),
	}
	replaced := false
	for i := range entries {
		if entries[i].Fingerprint == fingerprint {
			entries[i] = next
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, next)
	}
	body, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("hush/cli: init: encode client registry: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("hush/cli: init: write client registry: %w", err)
	}
	return nil
}

func compressedPublicKeyHex(pub *ecdsa.PublicKey) string {
	compressed := make([]byte, 33)
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .X/.Y are read-only
	if pub.Y.Bit(0) == 0 {
		compressed[0] = 0x02
	} else {
		compressed[0] = 0x03
	}
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .X/.Y are read-only
	xBytes := pub.X.Bytes()
	copy(compressed[1+32-len(xBytes):], xBytes)
	return hex.EncodeToString(compressed)
}

func writeClientKeyFile(path string, priv *securebytes.SecureBytes) error {
	var encoded string
	if err := priv.Use(func(b []byte) {
		encoded = hex.EncodeToString(b)
	}); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(encoded+"\n"), 0o600); err != nil {
		return fmt.Errorf("hush/cli: init: write client key file: %w", err)
	}
	return nil
}

// promptRequired re-prompts until a non-empty line is read or three
// attempts fail.
func promptRequired(reader func(*os.File, io.Writer, string) (string, error), in *os.File, prompt io.Writer, label, fieldName string) (string, error) {
	for attempt := 0; attempt < 3; attempt++ {
		v, err := reader(in, prompt, label)
		if err != nil {
			return "", err
		}
		if v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("%w: %s", errMissingFlag, fieldName)
}

// readPassphraseTTY reads a no-echo line from in. Errors if in is
// not a terminal.
func readPassphraseTTY(in *os.File, prompt io.Writer, label string) (*securebytes.SecureBytes, error) {
	if in == nil || !term.IsTerminal(int(in.Fd())) {
		return nil, errNoTTY
	}
	if _, err := io.WriteString(prompt, label); err != nil {
		return nil, fmt.Errorf("hush/cli: init: prompt: %w", err)
	}
	raw, err := term.ReadPassword(int(in.Fd()))
	_, _ = io.WriteString(prompt, "\n")
	if err != nil {
		return nil, fmt.Errorf("hush/cli: init: read passphrase: %w", err)
	}
	return securebytes.New(raw)
}

// readLineFromTTY reads a non-secret echoed line from in.
func readLineFromTTY(in *os.File, prompt io.Writer, label string) (string, error) {
	if _, err := io.WriteString(prompt, label); err != nil {
		return "", fmt.Errorf("hush/cli: init: prompt: %w", err)
	}
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("hush/cli: init: read line: %w", err)
		}
		return "", nil
	}
	return strings.TrimSpace(scanner.Text()), nil
}

// secureBytesEqual reports whether the byte payloads of a and b are
// equal. The comparison is constant-time within each Use callback
// (subtleEqual handles unequal lengths). A SecureBytes that has
// already been destroyed (or whose lock fails) surfaces as a non-nil
// error so the caller can distinguish "values differ" from "compare
// could not run" — masking the latter as the former produces the wrong
// operator-facing error message.
func secureBytesEqual(a, b *securebytes.SecureBytes) (bool, error) {
	var (
		equal    bool
		innerErr error
	)
	outerErr := a.Use(func(ab []byte) {
		innerErr = b.Use(func(bb []byte) {
			equal = subtleEqual(ab, bb)
		})
	})
	if outerErr != nil {
		return false, fmt.Errorf("hush/cli: compare: %w", outerErr)
	}
	if innerErr != nil {
		return false, fmt.Errorf("hush/cli: compare: %w", innerErr)
	}
	return equal, nil
}

// subtleEqual is a constant-time byte comparison. Avoids importing
// crypto/subtle for one call when both lengths are pre-checked
// equal.
func subtleEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// guardFileAbsent refuses if path exists; emits the locked stderr
// message and returns the supplied sentinel.
func guardFileAbsent(path string, sentinel error, msgFmt string, stderr *Stream) error {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		_ = stderr.WriteText(msgFmt, path)
		return fmt.Errorf("%w: %s", sentinel, path)
	case errors.Is(err, os.ErrNotExist):
		return nil
	default:
		return fmt.Errorf("hush/cli: init: stat %s: %w", path, err)
	}
}

// guardKeychainAbsent refuses if a keychain item already exists for
// (service, account); emits the locked stderr message and returns
// errKeychainItemExists.
func guardKeychainAbsent(ctx context.Context, kc keychain.Keychain, service, account string, stderr *Stream) error {
	got, err := kc.Retrieve(ctx, service, account)
	if err != nil {
		if errors.Is(err, keychain.ErrKeychainItemNotFound) {
			return nil
		}
		return fmt.Errorf("hush/cli: init: keychain probe: %w", err)
	}
	_ = got.Destroy()
	_ = stderr.WriteText(initMsgKeychainExistsFmt, service, account)
	return fmt.Errorf("%w: service=%s account=%s", errKeychainItemExists, service, account)
}

// resolveStateDir picks the state directory path. When override is
// non-empty (test seam) it is used directly; otherwise the configured
// default `~/.hush` is expanded.
func resolveStateDir(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return expandTilde(config.DefaultStateDir)
}

// errStateDirNotADirectory surfaces a non-directory at the resolved
// state directory path.
var errStateDirNotADirectory = errors.New("hush/cli: init: state_dir is not a directory")

// ensureStateDir creates the state directory if absent. The
// directory mode is 0700 — vault.Save's parent-mode check enforces
// this.
func ensureStateDir(dir string) error {
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("%w: %s", errStateDirNotADirectory, dir)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hush/cli: init: stat %s: %w", dir, err)
	}
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return fmt.Errorf("hush/cli: init: mkdir %s: %w", dir, mkErr)
	}
	return nil
}

// generatePathPrefix returns a 12-character URL-safe random string.
func generatePathPrefix(r io.Reader) (string, error) {
	buf := make([]byte, 9)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", fmt.Errorf("hush/cli: init: path_prefix: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// serverInputs bundles the operator-supplied or generated values
// that vary per init run.
type serverKeychainItemNames struct {
	vaultPassphraseService string
	discordService         string
}

func defaultServerKeychainItems() serverKeychainItemNames {
	return serverKeychainItemNames{
		vaultPassphraseService: kcServiceVaultPassphrase,
		discordService:         "hush-discord",
	}
}

type serverInputs struct {
	listenAddr        string
	pathPrefix        string
	ownerID           string
	applicationID     string
	stateDir          string
	approvalChannelID string
	auditChannelID    string
	botTokenKeychain  string
}

// buildServerDecodedFromDefaults produces a fully-populated
// serverDecoded TOML body from the supplied operator inputs and the
// schema-documented defaults.
func buildServerDecodedFromDefaults(in serverInputs) tomlDocument {
	stateDir := in.stateDir
	if stateDir == "" {
		stateDir = config.DefaultStateDir
	}
	botTokenKeychain := strings.TrimSpace(in.botTokenKeychain)
	if botTokenKeychain == "" {
		botTokenKeychain = "hush-discord"
	}
	// audit_log and client_registry must resolve under state_dir
	// (config validator enforces audit_log under state_dir).
	auditLog := filepath.Join(stateDir, "audit.jsonl")
	clientRegistry := filepath.Join(stateDir, "clients.json")
	return tomlDocument{
		Server: tomlServer{
			ListenAddr:               in.listenAddr,
			PathPrefix:               in.pathPrefix,
			StateDir:                 stateDir,
			AuditLog:                 auditLog,
			DiscordOwnerID:           in.ownerID,
			ClientRegistry:           clientRegistry,
			DiscordApprovalChannelID: in.approvalChannelID,
			DiscordAuditChannelID:    in.auditChannelID,
		},
		Discord: tomlDiscord{
			BotTokenKeychainItem: botTokenKeychain,
			ApplicationID:        in.applicationID,
		},
		Crypto: tomlCrypto{
			ArgonTime:            config.DefaultArgonTime,
			ArgonMemoryMB:        config.DefaultArgonMemoryMB,
			ArgonThreads:         config.DefaultArgonThreads,
			JWTDefaultTTL:        config.DefaultJWTTTL.String(),
			MaxInteractiveTTL:    config.DefaultMaxInteractiveTTL.String(),
			MaxSupervisorTTL:     config.DefaultMaxSupervisorTTL.String(),
			DefaultMaxUses:       config.DefaultMaxUses,
			NonceTTL:             config.DefaultNonceTTL.String(),
			ClockSkew:            config.DefaultClockSkew.String(),
			ClaimApprovalTimeout: config.DefaultClaimApprovalTimeout.String(),
		},
		Network: tomlNetwork{
			RequireTailscale: config.DefaultRequireTailscale,
			AllowedCIDRs:     append([]string{}, config.DefaultAllowedCIDRs...),
			HealthBind:       in.listenAddr,
		},
		Security: tomlSecurity{
			RequireFileModeChecks: config.DefaultRequireFileModeChecks,
			RequireKeychainACL:    config.DefaultRequireKeychainACL,
			RequireNTPSync:        config.DefaultRequireNTPSync,
			MaxClockDrift:         config.DefaultMaxClockDrift.String(),
		},
	}
}

// tomlDocument mirrors the TOML wire-shape that init writes; all
// fields are emitted with their schema-locked names so config.LoadServer
// can read the file back round-trip.
type tomlDocument struct {
	Server   tomlServer   `toml:"server"`
	Discord  tomlDiscord  `toml:"discord"`
	Crypto   tomlCrypto   `toml:"crypto"`
	Network  tomlNetwork  `toml:"network"`
	Security tomlSecurity `toml:"security"`
}

type tomlServer struct {
	ListenAddr               string `toml:"listen_addr"`
	PathPrefix               string `toml:"path_prefix"`
	StateDir                 string `toml:"state_dir"`
	AuditLog                 string `toml:"audit_log"`
	DiscordOwnerID           string `toml:"discord_owner_id"`
	ClientRegistry           string `toml:"client_registry"`
	DiscordApprovalChannelID string `toml:"discord_approval_channel_id,omitempty"`
	DiscordAuditChannelID    string `toml:"discord_audit_channel_id,omitempty"`
}

type tomlDiscord struct {
	BotTokenKeychainItem string `toml:"bot_token_keychain_item"`
	ApplicationID        string `toml:"application_id"`
}

type tomlCrypto struct {
	ArgonTime            uint32 `toml:"argon_time"`
	ArgonMemoryMB        uint32 `toml:"argon_memory_mb"`
	ArgonThreads         uint8  `toml:"argon_threads"`
	JWTDefaultTTL        string `toml:"jwt_default_ttl"`
	MaxInteractiveTTL    string `toml:"max_interactive_ttl"`
	MaxSupervisorTTL     string `toml:"max_supervisor_ttl"`
	DefaultMaxUses       int    `toml:"default_max_uses"`
	NonceTTL             string `toml:"nonce_ttl"`
	ClockSkew            string `toml:"clock_skew"`
	ClaimApprovalTimeout string `toml:"claim_approval_timeout"`
}

type tomlNetwork struct {
	RequireTailscale bool     `toml:"require_tailscale"`
	AllowedCIDRs     []string `toml:"allowed_cidrs"`
	HealthBind       string   `toml:"health_bind"`
}

type tomlSecurity struct {
	RequireFileModeChecks bool   `toml:"require_file_mode_checks"`
	RequireKeychainACL    bool   `toml:"require_keychain_acl"`
	RequireNTPSync        bool   `toml:"require_ntp_sync"`
	MaxClockDrift         string `toml:"max_clock_drift"`
}

// writeConfigTOMLAtomic marshals doc into TOML, writes the result to
// path via the atomic .tmp → fsync → rename pattern at mode 0600.
func writeConfigTOMLAtomic(path string, doc tomlDocument) error {
	body, err := toml.Marshal(doc)
	if err != nil {
		return fmt.Errorf("hush/cli: init: marshal config: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // tmp = configPath + ".tmp", caller-vetted state dir
	if err != nil {
		return fmt.Errorf("hush/cli: init: create %s: %w", tmp, err)
	}
	defer func() { _ = os.Remove(tmp) }() // no-op if rename succeeded
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return fmt.Errorf("hush/cli: init: write %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("hush/cli: init: sync %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("hush/cli: init: close %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("hush/cli: init: rename %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("hush/cli: init: chmod %s: %w", path, err)
	}
	return nil
}

// serializeECPrivKey returns the 32-byte fixed-width big-endian
// scalar of priv wrapped in a *SecureBytes the caller owns. On
// SecureBytes.New failure (typically mlock under RLIMIT_MEMLOCK
// pressure) the scratch buffer is zeroed and the error is propagated;
// callers must abort init rather than enroll a degraded key.
//
// secp256k1 is not supported by crypto/ecdh (Go 1.26), so the
// stdlib's PrivateKey.Bytes / ParseRawPrivateKey path is unavailable
// and priv.D is the only way to extract the scalar; see internal/keys
// for the same pattern.
func serializeECPrivKey(priv *ecdsa.PrivateKey) (*securebytes.SecureBytes, error) {
	buf := make([]byte, 32)
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .D is read-only here
	priv.D.FillBytes(buf)
	sb, err := securebytes.New(buf)
	if err != nil {
		for i := range buf {
			buf[i] = 0
		}
		return nil, fmt.Errorf("hush/cli: init: wrap client key: %w", err)
	}
	return sb, nil
}

// sec1Compress returns the 33-byte SEC1-compressed encoding of pub
// (parity byte + 32-byte X coordinate).
func sec1Compress(pub *ecdsa.PublicKey) []byte {
	out := make([]byte, 33)
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .X/.Y are read-only here
	if pub.Y.Bit(0) == 0 {
		out[0] = 0x02
	} else {
		out[0] = 0x03
	}
	//nolint:staticcheck // see above
	xb := pub.X.Bytes()
	copy(out[1+32-len(xb):], xb)
	return out
}

// sshStyleFingerprint returns the OpenSSH-style fingerprint of pub:
// `SHA256:<43-char-base64-no-padding>`.
func sshStyleFingerprint(pub *ecdsa.PublicKey) string {
	d := sha256.Sum256(sec1Compress(pub))
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(d[:])
}
