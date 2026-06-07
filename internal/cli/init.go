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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mrz1836/hush/internal/cli/setup"
	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// onExisting* enumerate the legal values of the `--on-existing` flag
// and the per-artifact recovery decisions surfaced by the guided
// flow. The flag-facing modes are prompt / reuse / repair / archive /
// fail; the additional modes (recreate, env-token) are emitted only
// by the ACL-aware Keychain recovery branch.
//
// "" (unset) defers to per-mode defaults: prompt in interactive,
// fail in non-interactive.
const (
	onExistingPrompt   = "prompt"
	onExistingReuse    = "reuse"
	onExistingRepair   = "repair"
	onExistingArchive  = "archive"
	onExistingFail     = "fail"
	onExistingRecreate = "recreate"  // ACL-denied bot token: delete + restore
	onExistingEnvToken = "env-token" // ACL-denied bot token: skip Keychain, use HUSH_DISCORD_BOT_TOKEN
)

// keychainStoreRecovery* are the single-character return values for
// the bot-token write-recovery prompt shown when a Keychain Store
// fails with a recoverable error. The prompt is distinct from the
// existing-state recovery panel because the token is still in memory
// and can be retried without re-prompting the operator.
const (
	keychainStoreRecoveryRetry     = 'r'
	keychainStoreRecoveryDedicated = 'h'
	keychainStoreRecoveryEnvToken  = 'e'
	keychainStoreRecoveryQuit      = 'q'
)

// onExistingChoiceR/P/A/Q are the single-character return values
// from a promptRecovery seam. Locked so tests can pin them.
const (
	recoveryChoiceReuse   = 'r'
	recoveryChoiceRepair  = 'p'
	recoveryChoiceArchive = 'a'
	recoveryChoiceQuit    = 'q'
)

// keychainACLChoice* are the single-character return values for the
// ACL-aware Keychain recovery panel.
// The panel renders when the existing `hush-discord` Keychain item
// reads back as denied (see [setup.ErrTokenDenied]); each choice
// drives a distinct recovery branch.
const (
	keychainACLChoiceRepair   = '1' // ACL repair: print `security` commands + re-check
	keychainACLChoiceRecreate = '2' // delete + recreate (requires typing "delete")
	keychainACLChoiceEnvToken = '3' // skip Keychain, use HUSH_DISCORD_BOT_TOKEN
	keychainACLChoiceQuit     = 'q'
)

// keychainDeleteConfirmation is the literal string the operator must
// type to confirm the destructive delete-and-recreate branch.
// `y` is intentionally insufficient: tests
// assert this gate refuses anything that is not exact-byte equal.
const keychainDeleteConfirmation = "delete"

func dedicatedHushKeychainPath(stateDir string) string {
	return filepath.Join(stateDir, "hush.keychain-db")
}

// Locked literal-text strings (contracts/cli-init.md §2.3 / §3.3).
// Tests assert byte-equal on these messages.
const (
	initMsgNoTTY                    = "hush: init: stdin must be an interactive terminal"
	initMsgPassphraseTooShort       = "hush: init: passphrase must be at least 12 characters"
	initMsgPassphraseMismatch       = "hush: init: passphrase confirmation does not match"
	initMsgVaultExistsFmt           = "hush: init: vault already exists at %s"
	initMsgConfigExistsFmt          = "hush: init: config already exists at %s"
	initMsgKeychainExistsFmt        = "hush: init: keychain item already exists for service=%s account=%s"
	initMsgPlatformUnsupported      = "hush: init: platform %s has no per-binary keychain ACL; init refuses to run"
	initMsgMissingMachineIndex      = "hush: init: missing required flag: --machine-index"
	initMsgMachineIndexInvalid      = "hush: init: --machine-index must be a non-negative integer"
	initMsgFieldRequiredFmt         = "hush: init: %s is required"
	initMsgKeychainStoreFailFmt     = "hush: init: keychain store failed: %v"
	initMsgClientKeyFileFallbackFmt = "hush: init: macOS Keychain was unavailable for the client key, so hush used the requested key-file fallback.\n" +
		"  detail: %v\n" +
		"  ok:     client enrolled successfully; use --client-key-file with hush request."
	initMsgClientKeyFileSelected = "hush: init: --client-key-file set; storing the client key in that file and skipping macOS Keychain.\n" +
		"  ok: use --client-key-file with hush request."
	initMsgKeychainLockedStoreFmt = "hush: init: macOS login Keychain appears locked or refused the write: %v\n  next: hush will ask macOS to unlock the login Keychain, then retry while the token is still in memory."
	initMsgKeychainStoreLockedFmt = "hush: init: macOS Keychain is locked while storing the Discord bot token (%v).\n" +
		"  item: service=%s account=%s\n" +
		"  note: the token is still only in memory right now; nothing was written.\n" +
		"Choose how to proceed:\n" +
		"  [r] Retry — hush will ask macOS to unlock the login Keychain, then retry the write.\n" +
		"  [h] Create/use a dedicated hush Keychain without touching login.keychain-db.\n" +
		"  [e] Use explicit env-token fallback for this session.\n" +
		"  [q] Quit without changes."
	initMsgKeychainStoreDeniedFmt = "hush: init: macOS Keychain refused the Discord bot-token write (%v).\n" +
		"  item: service=%s account=%s\n" +
		"  note: the token is still only in memory right now; nothing was written.\n" +
		"Choose how to proceed:\n" +
		"  [r] Retry — after you unlock or repair Keychain access, try the write again.\n" +
		"  [h] Create/use a dedicated hush Keychain without touching login.keychain-db.\n" +
		"  [e] Use explicit env-token fallback for this session.\n" +
		"  [q] Quit without changes."
	initMsgKeychainStoreRecoveryPrompt = "Choose [r/h/e/q]: "
	initMsgKeychainStoreRetryOK        = "hush: init: Keychain write succeeded; the Discord bot token is now stored in macOS Keychain."
	initMsgKeychainUnlockFailedFmt     = "hush: init: login Keychain unlock failed: %v\n" +
		"  note: the Discord bot token is still only in memory; nothing was written.\n" +
		"  why:  this prompt is for the macOS login Keychain, which can drift out of sync with your current Mac login password after a password change or migration.\n" +
		"  options: retry with the correct/older login Keychain password, choose [h] to create/use a dedicated hush Keychain, repair the login Keychain in Keychain Access or System Settings, or choose env-token fallback for this session.\n" +
		"  caution: if the login Keychain password is unknown, resetting the login Keychain is an OS-level destructive repair outside hush."
	initMsgKeychainStoreNonInteractiveFmt = "hush: init: macOS Keychain refused the Discord bot-token write (%v); re-run interactively to retry/approve, or set HUSH_DISCORD_BOT_TOKEN for this session."
	initMsgBotTokenEnvAutoFallback        = "hush: init: env-token fallback selected; skipping Keychain write for the bot token.\n" +
		"  next: export HUSH_DISCORD_BOT_TOKEN='<your-…ken>' before running 'hush serve'.\n" +
		"  note: Keychain remains the preferred long-term storage when available.\n" +
		"  note: hush keychain doctor will say missing because no token was stored; rerun `hush init server` to store it later."
	initMsgExplicitStateKeychain     = "hush: init: --state-dir set; storing Discord bot token in macOS Keychain for serve. Vault passphrase is not stored for this learning/smoke path."
	initMsgDedicatedKeychainSelected = "hush: init: dedicated hush Keychain selected at %s.\n" +
		"  note: this does not reset or alter login.keychain-db.\n" +
		"  next: hush will store the Discord bot token in that file and load it from there on future runs."
	initMsgWriteFailFmt          = "hush: init: write %s: %v"
	initMsgServerComplete        = "hush: init: server bootstrap complete"
	initMsgServerNextCommandsFmt = "hush: init: next commands\n  1. Start the vault server:\n     %s\n  2. Enroll this machine as a client:\n     %s\n  3. Add a secret:\n     %s\n  4. Request the secret for a command:\n     %s"

	// initMsgKeychainPreExplainFmt is the hush-authored explanation
	// printed before every Keychain write call. The placeholders are
	// (purpose, service, account). Tests assert the literal text via
	// transcript scan: no raw Apple `security`
	// prompt may fire without this preamble.
	initMsgKeychainPreExplainFmt = "hush: init: about to store the %s in your macOS Keychain.\n" +
		"  item:    service=%s, account=%s\n" +
		"  why:     hush serve reads this on every start; storing it here means you grant access once now and never type it again.\n" +
		"  prompt:  macOS will ask 'do you want to allow access' — click 'Always Allow' so future serve restarts stay non-interactive."

	// initMsgPreflightFailFmt renders a preflight failure: name, status,
	// detail, remedy. Tests assert the remedy line is non-empty.
	initMsgPreflightFailFmt = "hush: init: preflight %s failed: %s\n  remedy: %s"

	// initMsgPreflightWarnFmt renders a preflight warning. The guided
	// flow asks the operator to confirm before continuing.
	initMsgPreflightWarnFmt = "hush: init: preflight %s warning: %s"

	// initMsgRecoveryPromptFmt renders the per-artifact recovery prompt.
	// Placeholders: kind, classification, current path (or "—" when
	// none). Tests pin the literal options string.
	initMsgRecoveryPromptFmt = "hush: init: existing %s (%s) at %s — choose [r]euse / [p]repair / [a]rchive (renames to .bak-<RFC3339>) / [q]uit: "

	// initMsgRecoveryArchivedFmt records a successful archive action.
	// %s = old path, %s = new path.
	initMsgRecoveryArchivedFmt = "hush: init: archived %s to %s"

	// initMsgRecoveryRepairFmt records a `[p]repair` choice. Phase 2
	// treats repair as silent reuse for files (Phase 3 wires Keychain
	// ACL repair); the message clarifies the limitation.
	initMsgRecoveryRepairFmt = "hush: init: repair selected for %s — Phase 2 treats this as silent reuse; full repair flows arrive in later phases."

	// initMsgRecoveryUserAborted is the locked stderr message emitted
	// when the operator picks `[q]uit` from the recovery prompt.
	initMsgRecoveryUserAborted = "hush: init: aborted by operator (user-aborted)"

	// initMsgOnExistingInvalidFmt fires when --on-existing carries a
	// value outside the allowed enum.
	initMsgOnExistingInvalidFmt = "hush: init: --on-existing must be one of prompt/reuse/repair/archive/fail; got %q"

	// initMsgClockSkewOverrideFmt fires when --allow-clock-skew was
	// supplied AND the clock-sync preflight returned warn/fail. Phase 2
	// just records the override; Phase 4 wires the audit pipeline.
	initMsgClockSkewOverrideFmt = "hush: init: --allow-clock-skew override active; clock-sync check downgraded for %s"

	// initMsgKeychainACLPanelFmt renders the ACL-denial panel.
	// Placeholders are
	// (service, account, keychainPath). The panel intentionally
	// embeds shell snippets that are zsh-safe (no `read -p` / `read -s`).
	initMsgKeychainACLPanelFmt = `hush: init: macOS Keychain is denying hush access to the existing '%s' item.
  why:  the OS refused the read (errSecAuthFailed / errSecInteractionNotAllowed / errSecUserCanceled).
  item: service=%s account=%s keychain=%s

Choose how to proceed:
  [1] ACL repair — run hush keychain repair in another terminal, then re-check.
      That command wraps:
        security set-generic-password-partition-list \
          -S apple-tool:,apple: \
          -s %s -a %s \
          %s
      Then return here and pick [1] again to re-check the Keychain.
  [2] Delete + recreate — destructive: removes the existing item and stores a new one.
      Requires typing '%s' to confirm.
  [3] Use HUSH_DISCORD_BOT_TOKEN env-var instead (recommended only if Keychain is unavailable).
      Hush will skip the Keychain write for the bot token; you supply the token at serve time via:
        export HUSH_DISCORD_BOT_TOKEN=<your-discord-bot-token>
      Keep using Keychain when possible — env-token mode loses the per-binary ACL.
  [q] Quit without changes.`

	// initMsgKeychainACLChoicePrompt is the trailing choice label after
	// the panel renders. Tests assert exact text.
	initMsgKeychainACLChoicePrompt = "Choose [1/2/3/q]: "

	// initMsgKeychainACLRecheckFmt prints the result of a re-check
	// attempt after the operator claims the ACL was repaired.
	initMsgKeychainACLRecheckOK     = "hush: init: Keychain re-check succeeded; reusing the existing '%s' item."
	initMsgKeychainACLRecheckFailed = "hush: init: Keychain re-check still denied; re-displaying the recovery panel."

	// initMsgKeychainDeleteConfirmPrompt is the literal confirmation
	// prompt for the destructive delete-and-recreate branch. The
	// operator must type the locked confirmation string (see
	// [keychainDeleteConfirmation]) to proceed.
	initMsgKeychainDeleteConfirmPrompt = "Type 'delete' to confirm destructive removal of the existing Keychain item, or anything else to cancel: "

	// initMsgKeychainDeleteCancelled fires when the operator does not
	// type the locked confirmation string at the delete prompt.
	initMsgKeychainDeleteCancelled = "hush: init: delete-and-recreate cancelled (confirmation string not matched)."

	// initMsgKeychainDeletedFmt records a successful destructive
	// removal of an existing Keychain item. Audit-grade: the
	// service+account pair is captured so the operator can correlate
	// against external Keychain Access UI activity.
	initMsgKeychainDeletedFmt = "hush: init: deleted existing Keychain item service=%s account=%s (recreating next)."

	// initMsgKeychainEnvTokenFallbackFmt explains the env-token
	// fallback decision.
	initMsgKeychainEnvTokenFallbackFmt = "hush: init: env-token fallback selected; skipping Keychain write for the bot token.\n" +
		"  next: export HUSH_DISCORD_BOT_TOKEN='<your-discord-bot-token>' before running 'hush serve'.\n" +
		"  note: env-token mode is supported but loses the per-binary ACL — use Keychain when possible."

	// initMsgKeychainACLInvalidChoiceFmt fires when the operator's
	// choice rune is not one of [1/2/3/q] on the ACL panel.
	initMsgKeychainACLInvalidChoiceFmt = "hush: init: invalid choice %q; pick one of [1/2/3/q]."
)

// Locked prompt labels (contracts/cli-init.md §2.2 / §3.2).
const (
	promptVaultPassphrase = "Vault passphrase: "
	promptConfirmVault    = "Confirm vault passphrase: "
	promptListenAddr      = "Listen address (e.g. 100.96.10.4:7743): "
	promptOwnerID         = "Discord owner ID (snowflake): "
	promptApplicationID   = "Discord application ID (snowflake): "
	promptApprovalChannel = "Discord approval channel ID (optional; empty sends DMs): "
	promptAuditChannel    = "Discord audit channel ID (optional; empty disables mirror): "
	promptBotToken        = "Discord bot token: " //nolint:gosec // prompt label, not a credential
)

// Keychain (service, account) pairs.
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
	// serverPassphrase is the operator-supplied vault passphrase used when
	// serverNonInteractive=true. Owned by the caller that populates initDeps
	// (see the JSON --input-file boundary in newInitServerCmd); runInitServer
	// borrows the bytes inside a single Use callback to materialize a fresh
	// mlocked working copy and never converts to a Go string.
	serverPassphrase *securebytes.SecureBytes
	// serverBotToken is the operator-supplied Discord bot token used when
	// serverNonInteractive=true (and when token persistence is required).
	// Same ownership/borrow contract as serverPassphrase. nil means "no
	// token supplied" (legitimate when the caller sets an explicit state
	// dir and the existing keychain item is reused).
	serverBotToken         *securebytes.SecureBytes
	serverInputs           serverInputs
	serverAllowClockSkew   bool
	serverProbeFailureWarn bool
	serverOnExisting       string
	clientNonInteractive   bool
	// clientPassphrase is the operator-supplied vault passphrase used when
	// clientNonInteractive=true. Same ownership/borrow contract as
	// serverPassphrase.
	clientPassphrase *securebytes.SecureBytes
	clientRegistry   string
	clientKeyFile    string

	// promptSecret reads a no-echo line from stdin in production;
	// tests substitute a deterministic reader.
	promptSecret func(in *os.File, prompt io.Writer, label string) (*securebytes.SecureBytes, error)
	// promptLine reads a non-secret line from stdin in production;
	// tests substitute a deterministic reader.
	promptLine func(in *os.File, prompt io.Writer, label string) (string, error)
	// keychainFactory constructs either the default login-keychain
	// implementation or a path-scoped dedicated keychain when the
	// user chooses the hush-keychain escape hatch.
	keychainFactory func(string) (keychain.Keychain, error)
	// promptOptionalLine reads an optional non-secret line in production.
	// Tests leave it nil unless they specifically exercise optional prompts.
	promptOptionalLine func(in *os.File, prompt io.Writer, label string) (string, error)
	// promptRecovery reads a single-character recovery choice
	// (r/p/a/q) for per-artifact existing-state recovery. Tests
	// substitute a scripted reader; the production binding reads
	// a single line via promptLine and returns its first rune.
	promptRecovery func(in *os.File, prompt io.Writer, label string) (rune, error)
	// unlockLoginKeychain asks macOS to unlock the login Keychain before a
	// retry. It never receives the Discord token; macOS/security owns any
	// password prompt.
	unlockLoginKeychain func(context.Context) error
	// isTTY reports whether stdin is an interactive terminal.
	isTTY func(*os.File) bool
	// deriveMasterSeed is the Argon2id master-seed derivation.
	// Tests substitute a fast stub to avoid 1s+ Argon2 cost on
	// every test; the stub MUST still validate the passphrase
	// length contract.
	deriveMasterSeed func(ctx context.Context, passphrase, salt []byte) ([]byte, error)
	// runPreflight runs the diagnostic-first preflight pipeline
	// before any TTY prompt. Production returns an
	// empty Report until Phase 4 wires real checks. Tests inject a
	// synthetic Report to drive the warn / fail branches.
	runPreflight func(ctx context.Context) setup.Report
}

// productionInitDeps wires the production seams. Tests construct a
// custom initDeps directly.
func productionInitDeps() (*initDeps, error) {
	kc, err := keychain.New(slog.Default())
	if err != nil {
		return nil, err
	}
	deps := &initDeps{
		keychain:            kc,
		binaryPath:          os.Executable,
		randReader:          rand.Reader,
		stateDirRoot:        "",
		nowFn:               time.Now,
		platformACL:         keychain.PerBinaryACLSupported,
		promptSecret:        readPassphraseTTY,
		promptLine:          readLineFromTTY,
		promptOptionalLine:  readLineFromTTY,
		promptRecovery:      defaultPromptRecovery,
		unlockLoginKeychain: defaultUnlockLoginKeychain,
		keychainFactory:     func(path string) (keychain.Keychain, error) { return keychain.NewAtPath(slog.Default(), path) },
		isTTY:               defaultIsTTY,
		deriveMasterSeed:    keys.DeriveMasterSeed,
	}
	deps.runPreflight = defaultRunPreflightFor(deps)
	return deps, nil
}

// defaultPromptRecovery is the production binding for the recovery
// (r/p/a/q) prompt. Reads a single line via the same TTY helper used
// by the other line prompts; returns its first non-whitespace rune
// lowercased. An empty line errors so the caller can re-prompt.
func defaultPromptRecovery(in *os.File, prompt io.Writer, label string) (rune, error) {
	line, err := readLineFromTTY(in, prompt, label)
	if err != nil {
		return 0, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, errEmptyRecoveryChoice
	}
	r := []rune(line)[0]
	if r >= 'A' && r <= 'Z' {
		r = r + ('a' - 'A')
	}
	return r, nil
}

func defaultUnlockLoginKeychain(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "~"
	}
	keychainPath := filepath.Join(home, "Library", "Keychains", "login.keychain-db")
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "unlock-keychain", keychainPath) //nolint:gosec // fixed binary and argv; macOS owns password prompt
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("security unlock-keychain: %w", err)
	}
	return nil
}

// defaultRunPreflightFor returns the production preflight closure
// bound to deps. The closure builds a fresh [setup.Registry] on every
// invocation so a re-run after recovery picks up the operator's
// latest flag state (notably [initDeps.serverAllowClockSkew], which
// is mutable up to the moment the preflight runs).
//
// Phase 4 wires the clock-sync slot; later phases register the rest
// of [setup.CheckOrder] in this same factory.
func defaultRunPreflightFor(deps *initDeps) func(ctx context.Context) setup.Report {
	return func(ctx context.Context) setup.Report {
		stateDir, _ := resolveStateDir(deps.stateDirRoot)
		reg := setup.NewRegistry()
		reg.MustRegister(setup.NewClockSyncCheck(setup.ClockSyncCheckConfig{
			Required:         config.DefaultRequireNTPSync,
			MaxDrift:         config.DefaultMaxClockDrift,
			Timeout:          server.DefaultClockSyncTimeout,
			AllowSkew:        deps.serverAllowClockSkew,
			ProbeFailureWarn: deps.serverProbeFailureWarn,
			StateDir:         stateDir,
		}))
		return reg.Run(ctx)
	}
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

// readServerBootstrapSecrets reads the --input-file JSON, converts the two
// secret fields into fresh mlocked *SecureBytes, zeroes the file body, and
// returns ownership of both SecureBytes pointers to the caller.
//
// botTok is nil iff the JSON's discord_bot_token field was absent or empty
// (the caller treats nil as "no token supplied").
//
// Residual exposure: json.Unmarshal allocates Go strings for the two fields
// during parse. Those strings cannot be zeroed (Go strings are immutable);
// they become unreachable as soon as this function returns and survive in
// heap until GC. The file body []byte IS zeroed before return, so the only
// non-erasable copies are the short-lived JSON struct strings — a strict
// improvement over the previous design that threaded those strings through
// initDeps for the whole runInitServer call.
func readServerBootstrapSecrets(path string) (pass, botTok *securebytes.SecureBytes, err error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("%w: --input-file", errMissingFlag)
	}
	body, readErr := os.ReadFile(path) //nolint:gosec // operator-supplied bootstrap file path
	if readErr != nil {
		return nil, nil, fmt.Errorf("hush/cli: init: read input file: %w", readErr)
	}
	defer zeroByteSlice(body)
	var input serverBootstrapInput
	if jerr := json.Unmarshal(body, &input); jerr != nil {
		return nil, nil, fmt.Errorf("hush/cli: init: decode input file: %w", jerr)
	}
	pass, err = securebytes.New([]byte(input.VaultPassphrase))
	if err != nil {
		return nil, nil, fmt.Errorf("hush/cli: init: wrap vault passphrase: %w", err)
	}
	if strings.TrimSpace(input.DiscordBotToken) != "" {
		botTok, err = securebytes.New([]byte(input.DiscordBotToken))
		if err != nil {
			_ = pass.Destroy()
			return nil, nil, fmt.Errorf("hush/cli: init: wrap bot token: %w", err)
		}
	}
	return pass, botTok, nil
}

// readClientBootstrapSecret mirrors readServerBootstrapSecrets for the client
// bootstrap JSON (passphrase only).
func readClientBootstrapSecret(path string) (*securebytes.SecureBytes, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: --input-file", errMissingFlag)
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied bootstrap file path
	if err != nil {
		return nil, fmt.Errorf("hush/cli: init: read input file: %w", err)
	}
	defer zeroByteSlice(body)
	var input clientBootstrapInput
	if jerr := json.Unmarshal(body, &input); jerr != nil {
		return nil, fmt.Errorf("hush/cli: init: decode input file: %w", jerr)
	}
	pass, err := securebytes.New([]byte(input.VaultPassphrase))
	if err != nil {
		return nil, fmt.Errorf("hush/cli: init: wrap vault passphrase: %w", err)
	}
	return pass, nil
}

// zeroByteSlice overwrites every byte of b with 0. Used to scrub the
// JSON input file's raw bytes before this function returns — the JSON
// struct's string fields are the irreducible residual (see
// readServerBootstrapSecrets).
func zeroByteSlice(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// cloneSecureBytes returns an independently-owned *SecureBytes carrying
// a copy of src's payload. Both src and the returned copy are valid
// after the call; the caller owns Destroy on the returned pointer.
//
// Used at API boundaries where a function that defers Destroy is
// handed a SecureBytes owned by an outer scope (typical case: cobra
// RunE owns deps.serverPassphrase, runInitServer wants its own copy
// to manage lifetime locally).
func cloneSecureBytes(src *securebytes.SecureBytes) (*securebytes.SecureBytes, error) {
	var (
		out    *securebytes.SecureBytes
		newErr error
	)
	if useErr := src.Use(func(b []byte) {
		buf := make([]byte, len(b))
		copy(buf, b)
		out, newErr = securebytes.New(buf)
	}); useErr != nil {
		return nil, useErr
	}
	return out, newErr
}

// newInitServerCmd builds the `hush init server` subcommand. The cobra
// command tree is the contract; no exported symbols.
//
// The default mode is **guided/interactive**: hush runs a
// diagnostic-first preflight, then prompts the operator for every
// required input. `--non-interactive` is the explicit opt-out for
// scripted/test callers.
//
//nolint:gocognit // cobra RunE wires flag parsing + JSON-input boundary + Destroy defers
func newInitServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Bootstrap the vault host (default: guided/interactive; --non-interactive for scripted callers)",
		Long: strings.TrimSpace(`
Bootstrap the vault host.

Default: guided/interactive. hush runs a diagnostic-first preflight
(binary, config target, state dir, file modes, Keychain readability,
Tailscale bind, listen port, clock sync, existing-artifact collision)
and then prompts you for vault passphrase + Discord bot token. No
prompt fires until the preflight pipeline has succeeded.

Existing config / vault / state-dir / Keychain artifacts are classified
per-artifact and you are offered [r]euse / [p]repair / [a]rchive /
[q]uit — never silently overwritten.

Flags:
  --non-interactive        Opt out of the guided flow; read bootstrap
                           inputs from --input-file and the listed
                           flags. Use this in scripts / tests / CI.
  --allow-clock-skew       Downgrade a failing clock-sync preflight to
                           a warning. hush will never auto-sudo on
                           your behalf; rely on this only if you have
                           knowingly accepted the skew.
  --on-existing=<mode>     prompt | reuse | repair | archive | fail
                           Default: prompt (interactive) / fail
                           (non-interactive). 'archive' renames the
                           colliding artifact to
                           '<path>.bak-<RFC3339>' before continuing.
  --state-dir <path>       Override the default ~/.hush state dir
                           (smoke/learning path; Keychain writes are
                           skipped).
`),
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
			deps.serverAllowClockSkew, _ = cmd.Flags().GetBool("allow-clock-skew")
			deps.serverOnExisting, _ = cmd.Flags().GetString("on-existing")
			if strings.TrimSpace(deps.serverInputs.stateDir) != "" {
				deps.stateDirRoot = deps.serverInputs.stateDir
			}
			if nonInteractive, _ := cmd.Flags().GetBool("non-interactive"); nonInteractive {
				deps.serverNonInteractive = true
				inputFile, _ := cmd.Flags().GetString("input-file")
				pass, botTok, inputErr := readServerBootstrapSecrets(inputFile)
				if inputErr != nil {
					return inputErr
				}
				deps.serverPassphrase = pass
				deps.serverBotToken = botTok
				defer func() {
					if deps.serverPassphrase != nil {
						_ = deps.serverPassphrase.Destroy()
					}
					if deps.serverBotToken != nil {
						_ = deps.serverBotToken.Destroy()
					}
				}()
			}
			out := outputFromCmd(cmd)
			return runInitServer(cmd.Context(), out.stdout, out.stderr, os.Stdin, deps)
		},
	}
	cmd.Flags().Bool("non-interactive", false, "Opt out of the guided flow; read bootstrap inputs from --input-file and flags")
	cmd.Flags().String("state-dir", "", "State directory for generated vault/config (default ~/.hush)")
	cmd.Flags().String("listen-addr", "", "Server listen address (required with --non-interactive)")
	cmd.Flags().String("discord-owner-id", "", "Discord owner snowflake (required with --non-interactive)")
	cmd.Flags().String("discord-application-id", "", "Discord application snowflake (required with --non-interactive)")
	cmd.Flags().String("discord-approval-channel-id", "", "Discord approval channel snowflake (optional; empty sends approvals by DM)")
	cmd.Flags().String("discord-audit-channel-id", "", "Discord audit mirror channel snowflake (optional)")
	cmd.Flags().String("input-file", "", "0600 JSON bootstrap input for --non-interactive")
	cmd.Flags().Bool("allow-clock-skew", false, "Downgrade a failing clock-sync preflight to a warning (no auto-sudo)")
	cmd.Flags().String("on-existing", "", "Recovery mode for pre-existing artifacts: prompt|reuse|repair|archive|fail")
	return cmd
}

// newInitClientCmd builds the `hush init client` subcommand. The cobra
// command tree is the contract; no exported symbols.
//
//nolint:gocognit // cobra RunE wires flag parsing + JSON-input boundary + Destroy defer
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
				pass, inputErr := readClientBootstrapSecret(inputFile)
				if inputErr != nil {
					return inputErr
				}
				deps.clientPassphrase = pass
				defer func() {
					if deps.clientPassphrase != nil {
						_ = deps.clientPassphrase.Destroy()
					}
				}()
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

// initServerSetup carries the operator-supplied passphrase, bot token, and
// non-secret string fields resolved by gatherInitServerInputs. Caller owns
// both SecureBytes and must Destroy() them.
type initServerSetup struct {
	pass              *securebytes.SecureBytes
	botToken          *securebytes.SecureBytes // may be nil in explicit-state-dir non-interactive flows
	listenAddr        string
	ownerID           string
	appID             string
	approvalChannelID string
	auditChannelID    string
}

// destroy releases both SecureBytes. Safe to call multiple times — nil
// fields are ignored.
func (s *initServerSetup) destroy() {
	if s == nil {
		return
	}
	if s.pass != nil {
		_ = s.pass.Destroy()
	}
	if s.botToken != nil {
		_ = s.botToken.Destroy()
	}
}

// promptVaultPassphraseWithConfirm prompts the operator for the vault
// passphrase, enforces the minimum length, prompts again to confirm, and
// returns the validated passphrase. Caller owns the returned SecureBytes.
func promptVaultPassphraseWithConfirm(deps *initDeps, in *os.File, stderr *Stream) (*securebytes.SecureBytes, error) {
	pass, err := deps.promptSecret(in, stderr.w, promptVaultPassphrase)
	if err != nil {
		return nil, err
	}
	if pass.Len() < minPassphraseLen {
		_ = pass.Destroy()
		_ = stderr.WriteText(initMsgPassphraseTooShort)
		return nil, errPassphraseTooShort
	}
	confirm, confirmErr := deps.promptSecret(in, stderr.w, promptConfirmVault)
	if confirmErr != nil {
		_ = pass.Destroy()
		return nil, confirmErr
	}
	equal, cmpErr := secureBytesEqual(pass, confirm)
	_ = confirm.Destroy()
	if cmpErr != nil {
		_ = pass.Destroy()
		return nil, cmpErr
	}
	if !equal {
		_ = pass.Destroy()
		_ = stderr.WriteText(initMsgPassphraseMismatch)
		return nil, errPassphraseMismatch
	}
	return pass, nil
}

// gatherInitServerInputsNonInteractive resolves the inputs for
// `hush init server --non-interactive`. Clones deps.serverPassphrase and
// deps.serverBotToken (token may be skipped under --state-dir).
func gatherInitServerInputsNonInteractive(deps *initDeps, explicitStateDir bool) (*initServerSetup, error) {
	if deps.serverPassphrase == nil {
		return nil, fmt.Errorf("%w: serverPassphrase", errMissingFlag)
	}
	pass, err := cloneSecureBytes(deps.serverPassphrase)
	if err != nil {
		return nil, err
	}
	botTokenPresent := deps.serverBotToken != nil && deps.serverBotToken.Len() > 0
	var botToken *securebytes.SecureBytes
	if !explicitStateDir || botTokenPresent {
		if !botTokenPresent {
			_ = pass.Destroy()
			return nil, fmt.Errorf("%w: serverBotToken", errMissingFlag)
		}
		botToken, err = cloneSecureBytes(deps.serverBotToken)
		if err != nil {
			_ = pass.Destroy()
			return nil, err
		}
	}
	return &initServerSetup{
		pass:              pass,
		botToken:          botToken,
		listenAddr:        strings.TrimSpace(deps.serverInputs.listenAddr),
		ownerID:           strings.TrimSpace(deps.serverInputs.ownerID),
		appID:             strings.TrimSpace(deps.serverInputs.applicationID),
		approvalChannelID: strings.TrimSpace(deps.serverInputs.approvalChannelID),
		auditChannelID:    strings.TrimSpace(deps.serverInputs.auditChannelID),
	}, nil
}

// seedInitServerStringFields seeds the five non-secret string fields from
// deps.serverInputs, overlaying values from an existing config when the
// operator-supplied input is empty.
func seedInitServerStringFields(deps *initDeps, existingCfg *config.Server) (listen, owner, app, approval, audit string) {
	listen = strings.TrimSpace(deps.serverInputs.listenAddr)
	owner = strings.TrimSpace(deps.serverInputs.ownerID)
	app = strings.TrimSpace(deps.serverInputs.applicationID)
	approval = strings.TrimSpace(deps.serverInputs.approvalChannelID)
	audit = strings.TrimSpace(deps.serverInputs.auditChannelID)
	if existingCfg != nil {
		listen = firstNonEmpty(listen, existingCfg.Server.ListenAddr.String())
		owner = firstNonEmpty(owner, existingCfg.Server.DiscordOwnerID)
		app = firstNonEmpty(app, existingCfg.Discord.ApplicationID)
		approval = firstNonEmpty(approval, existingCfg.Server.DiscordApprovalChannelID)
		audit = firstNonEmpty(audit, existingCfg.Server.DiscordAuditChannelID)
	}
	return listen, owner, app, approval, audit
}

// promptInitServerRequiredFields prompts for any of the three required
// fields (listen addr, owner ID, app ID) that arrived empty. On prompt
// error it emits the field-required stderr message before returning.
func promptInitServerRequiredFields(deps *initDeps, in *os.File, stderr *Stream, listen, owner, app string) (string, string, string, error) {
	var err error
	if listen == "" {
		listen, err = promptRequired(deps.promptLine, in, stderr.w, promptListenAddr, "listen_addr")
		if err != nil {
			_ = stderr.WriteText(initMsgFieldRequiredFmt, "listen_addr")
			return "", "", "", err
		}
	}
	if owner == "" {
		owner, err = promptRequired(deps.promptLine, in, stderr.w, promptOwnerID, "discord_owner_id")
		if err != nil {
			_ = stderr.WriteText(initMsgFieldRequiredFmt, "discord_owner_id")
			return "", "", "", err
		}
	}
	if app == "" {
		app, err = promptRequired(deps.promptLine, in, stderr.w, promptApplicationID, "application_id")
		if err != nil {
			_ = stderr.WriteText(initMsgFieldRequiredFmt, "application_id")
			return "", "", "", err
		}
	}
	return listen, owner, app, nil
}

// promptInitServerOptionalChannels prompts for any of the two optional
// Discord channel IDs that arrived empty. Guarded on promptOptionalLine
// availability — older test deps that don't wire it skip the prompts.
func promptInitServerOptionalChannels(deps *initDeps, in *os.File, stderr *Stream, approval, audit string) (string, string, error) {
	if deps.promptOptionalLine == nil {
		return approval, audit, nil
	}
	var err error
	if approval == "" {
		approval, err = promptOptional(deps.promptOptionalLine, in, stderr.w, promptApprovalChannel)
		if err != nil {
			return "", "", err
		}
	}
	if audit == "" {
		audit, err = promptOptional(deps.promptOptionalLine, in, stderr.w, promptAuditChannel)
		if err != nil {
			return "", "", err
		}
	}
	return approval, audit, nil
}

// promptInitServerStringFields composes seed + required + optional steps.
func promptInitServerStringFields(deps *initDeps, in *os.File, stderr *Stream, existingCfg *config.Server) (string, string, string, string, string, error) {
	listen, owner, app, approval, audit := seedInitServerStringFields(deps, existingCfg)
	listen, owner, app, err := promptInitServerRequiredFields(deps, in, stderr, listen, owner, app)
	if err != nil {
		return "", "", "", "", "", err
	}
	approval, audit, err = promptInitServerOptionalChannels(deps, in, stderr, approval, audit)
	if err != nil {
		return "", "", "", "", "", err
	}
	return listen, owner, app, approval, audit, nil
}

// gatherInitServerInputsInteractive prompts the operator on a TTY for the
// passphrase + confirm, the non-secret fields, and the Discord bot token.
func gatherInitServerInputsInteractive(deps *initDeps, in *os.File, stderr *Stream, existingCfg *config.Server) (*initServerSetup, error) {
	if !deps.isTTY(in) {
		_ = stderr.WriteText(initMsgNoTTY)
		return nil, errNoTTY
	}
	pass, err := promptVaultPassphraseWithConfirm(deps, in, stderr)
	if err != nil {
		return nil, err
	}
	listenAddr, ownerID, appID, approvalChannelID, auditChannelID, err := promptInitServerStringFields(deps, in, stderr, existingCfg)
	if err != nil {
		_ = pass.Destroy()
		return nil, err
	}
	botToken, err := deps.promptSecret(in, stderr.w, promptBotToken)
	if err != nil {
		_ = pass.Destroy()
		return nil, err
	}
	return &initServerSetup{
		pass:              pass,
		botToken:          botToken,
		listenAddr:        listenAddr,
		ownerID:           ownerID,
		appID:             appID,
		approvalChannelID: approvalChannelID,
		auditChannelID:    auditChannelID,
	}, nil
}

// gatherInitServerInputs branches between the non-interactive (deps-driven)
// and interactive (TTY-driven) input sources.
func gatherInitServerInputs(deps *initDeps, in *os.File, stderr *Stream, existingCfg *config.Server, explicitStateDir bool) (*initServerSetup, error) {
	if deps.serverNonInteractive {
		return gatherInitServerInputsNonInteractive(deps, explicitStateDir)
	}
	return gatherInitServerInputsInteractive(deps, in, stderr, existingCfg)
}

// validateInitServerSetup re-checks the gathered inputs against the
// required-field contract. Mirrors the post-gathering sanity check the
// monolithic flow performed, so any divergence between the non-interactive
// and interactive paths is caught uniformly.
func validateInitServerSetup(s *initServerSetup, explicitStateDir, nonInteractive bool, stderr *Stream) error {
	if s.pass.Len() < minPassphraseLen {
		_ = stderr.WriteText(initMsgPassphraseTooShort)
		return errPassphraseTooShort
	}
	if (s.botToken == nil || s.botToken.Len() == 0) && (!explicitStateDir || !nonInteractive) {
		_ = stderr.WriteText(initMsgFieldRequiredFmt, "discord_bot_token")
		return errMissingFlag
	}
	if s.listenAddr == "" {
		_ = stderr.WriteText(initMsgFieldRequiredFmt, "listen_addr")
		return errMissingFlag
	}
	if s.ownerID == "" {
		_ = stderr.WriteText(initMsgFieldRequiredFmt, "discord_owner_id")
		return errMissingFlag
	}
	if s.appID == "" {
		_ = stderr.WriteText(initMsgFieldRequiredFmt, "application_id")
		return errMissingFlag
	}
	return nil
}

// applyExistingArtifactGuards runs the classifier, then enforces the
// per-artifact `fail` modes for vault + config and the legacy keychain
// guard. Returns the per-artifact decisions on success.
func applyExistingArtifactGuards(ctx context.Context, in *os.File, stderr *Stream, deps *initDeps, vaultPath, configPath, stateDir string, keychainItems serverKeychainItemNames, explicitStateDir bool) (recoveryDecisions, error) {
	decisions, recoveryErr := recoverExistingArtifacts(ctx, in, stderr, deps, vaultPath, configPath, stateDir, keychainItems)
	if recoveryErr != nil {
		return recoveryDecisions{}, recoveryErr
	}
	if decisions.modeFor(setup.ArtifactVault) == onExistingFail {
		if guardErr := guardFileAbsent(vaultPath, errVaultExists, initMsgVaultExistsFmt, stderr); guardErr != nil {
			return recoveryDecisions{}, guardErr
		}
	}
	if decisions.modeFor(setup.ArtifactConfig) == onExistingFail {
		if guardErr := guardFileAbsent(configPath, errConfigExists, initMsgConfigExistsFmt, stderr); guardErr != nil {
			return recoveryDecisions{}, guardErr
		}
	}
	if guardErr := applyLegacyKeychainGuards(ctx, deps, stderr, decisions, keychainItems, explicitStateDir); guardErr != nil {
		return recoveryDecisions{}, guardErr
	}
	return decisions, nil
}

// deriveInitServerVaultKey produces a fresh 16-byte salt and derives the
// vault encryption key (BIP32 path m/44'/7743'/1') from the operator's
// passphrase. Caller must Destroy() the returned SecureBytes; the
// intermediate master seed is wiped before this returns.
func deriveInitServerVaultKey(ctx context.Context, deps *initDeps, pass *securebytes.SecureBytes) (*securebytes.SecureBytes, []byte, error) {
	salt := make([]byte, 16)
	if _, saltErr := io.ReadFull(deps.randReader, salt); saltErr != nil {
		return nil, nil, fmt.Errorf("hush/cli: init: salt: %w", saltErr)
	}
	var masterSeed []byte
	var deriveErr error
	if useErr := pass.Use(func(b []byte) {
		masterSeed, deriveErr = deps.deriveMasterSeed(ctx, b, salt)
	}); useErr != nil {
		return nil, nil, useErr
	}
	if deriveErr != nil {
		return nil, nil, deriveErr
	}
	defer zeroBytes(masterSeed)

	vaultEncRaw, err := keys.DeriveVaultEncKey(masterSeed)
	if err != nil {
		return nil, nil, err
	}
	vaultEncKey, err := securebytes.New(vaultEncRaw)
	if err != nil {
		return nil, nil, err
	}
	return vaultEncKey, salt, nil
}

// writeInitialServerArtifacts ensures the state dir exists, writes the
// empty vault when the operator didn't choose reuse, generates a fresh
// path_prefix and writes config.toml when the operator didn't choose
// reuse, then round-trip-validates the on-disk config.
func writeInitialServerArtifacts(ctx context.Context, deps *initDeps, stderr *Stream, vaultPath, configPath, stateDir string, vaultEncKey *securebytes.SecureBytes, salt []byte, cfgInputs *serverInputs, decisions recoveryDecisions) (*config.Server, error) {
	if dirErr := ensureStateDir(stateDir); dirErr != nil {
		return nil, dirErr
	}
	if !reuseArtifact(decisions, setup.ArtifactVault) {
		if saveErr := vault.SaveWithSalt(ctx, vaultPath, vaultEncKey, salt, []vault.Secret{}); saveErr != nil {
			return nil, saveErr
		}
	}
	if !reuseArtifact(decisions, setup.ArtifactConfig) {
		pathPrefix, ppErr := generatePathPrefix(deps.randReader)
		if ppErr != nil {
			return nil, ppErr
		}
		cfgInputs.pathPrefix = pathPrefix
		cfgBody := buildServerDecodedFromDefaults(*cfgInputs)
		if wErr := writeConfigTOMLAtomic(configPath, cfgBody); wErr != nil {
			_ = stderr.WriteText(initMsgWriteFailFmt, configPath, wErr)
			return nil, wErr
		}
	}
	loadedCfg, err := config.LoadServer(ctx, configPath)
	if err != nil {
		return nil, fmt.Errorf("hush/cli: init: round-trip-validate config: %w", err)
	}
	return loadedCfg, nil
}

// rewriteServerConfigWithBotPath rewrites config.toml with the supplied
// dedicated Discord-keychain path baked into Discord.BotKeychainPath and
// round-trip-validates. Returns the freshly-loaded config.
func rewriteServerConfigWithBotPath(ctx context.Context, configPath string, cfgInputs *serverInputs, loadedCfg *config.Server, dedicatedKeychainPath string, stderr *Stream) (*config.Server, error) {
	cfgInputs.pathPrefix = loadedCfg.Server.PathPrefix
	cfgBody := buildServerDecodedFromDefaults(*cfgInputs)
	cfgBody.Discord.BotKeychainPath = dedicatedKeychainPath
	if wErr := writeConfigTOMLAtomic(configPath, cfgBody); wErr != nil {
		_ = stderr.WriteText(initMsgWriteFailFmt, configPath, wErr)
		return nil, wErr
	}
	reloaded, err := config.LoadServer(ctx, configPath)
	if err != nil {
		return nil, fmt.Errorf("hush/cli: init: round-trip-validate dedicated keychain config: %w", err)
	}
	return reloaded, nil
}

// storeExplicitStateBotToken tries the configured-keychain-path route first;
// if that route doesn't handle the token, falls back to the decision-driven
// store. Returns (autoEnvTokenMode, dedicatedKeychainPath, error).
func storeExplicitStateBotToken(ctx context.Context, deps *initDeps, stderr *Stream, in *os.File, loadedCfg *config.Server, keychainItems serverKeychainItemNames, botToken *securebytes.SecureBytes, stateDir, binPath string, decisions recoveryDecisions) (bool, string, error) {
	handled, configuredPath, storeErr := storeBotTokenUsingConfiguredKeychainPath(ctx, deps, stderr, botToken, loadedCfg, keychainItems.discordService, binPath)
	if storeErr != nil {
		return false, "", storeErr
	}
	if handled {
		return false, configuredPath, nil
	}
	return storeBotTokenForDecision(ctx, deps, stderr, in, keychainItems, botToken, stateDir, binPath, decisions)
}

// finalizeInitServerExplicitState handles the keychain + completion flow
// when the operator passed --state-dir. The vault passphrase is NOT
// stored in Keychain on this path; the bot token is stored when present.
func finalizeInitServerExplicitState(ctx context.Context, deps *initDeps, stderr *Stream, in *os.File, configPath string, loadedCfg *config.Server, cfgInputs *serverInputs, keychainItems serverKeychainItemNames, botToken *securebytes.SecureBytes, stateDir, binPath string, decisions recoveryDecisions) error {
	_ = stderr.WriteText(initMsgExplicitStateKeychain)
	autoEnvTokenMode := false
	var dedicatedKeychainPath string
	var err error
	if botToken != nil {
		autoEnvTokenMode, dedicatedKeychainPath, err = storeExplicitStateBotToken(ctx, deps, stderr, in, loadedCfg, keychainItems, botToken, stateDir, binPath, decisions)
		if err != nil {
			return err
		}
	}
	if dedicatedKeychainPath != "" {
		reloaded, rewriteErr := rewriteServerConfigWithBotPath(ctx, configPath, cfgInputs, loadedCfg, dedicatedKeychainPath, stderr)
		if rewriteErr != nil {
			return rewriteErr
		}
		loadedCfg = reloaded
	}
	emitServerNextCommands(stderr, configPath, loadedCfg, autoEnvTokenMode || decisions.modeFor(setup.ArtifactKeychainToken) == onExistingEnvToken || botToken == nil)
	_ = stderr.WriteText(initMsgServerComplete)
	return nil
}

// finalizeInitServerStandard handles the standard keychain + completion
// flow. Vault passphrase + bot token are both written to Keychain.
func finalizeInitServerStandard(ctx context.Context, deps *initDeps, stderr *Stream, in *os.File, configPath string, loadedCfg *config.Server, cfgInputs *serverInputs, keychainItems serverKeychainItemNames, pass, botToken *securebytes.SecureBytes, stateDir, binPath string, decisions recoveryDecisions) error {
	// Hush-authored pre-explanations ensure no raw Apple `security`
	// prompt fires without a hush-authored preamble of what / why /
	// what-to-click.
	emitKeychainPreExplain(stderr, "vault passphrase", keychainItems.vaultPassphraseService, kcAccountServer)
	if storeErr := deps.keychain.Store(ctx, keychainItems.vaultPassphraseService, kcAccountServer, pass, binPath); storeErr != nil {
		_ = stderr.WriteText(initMsgKeychainStoreFailFmt, storeErr)
		return storeErr
	}
	autoEnvTokenMode, dedicatedKeychainPath, err := storeBotTokenForDecision(ctx, deps, stderr, in, keychainItems, botToken, stateDir, binPath, decisions)
	if err != nil {
		return err
	}
	if dedicatedKeychainPath != "" {
		reloaded, rewriteErr := rewriteServerConfigWithBotPath(ctx, configPath, cfgInputs, loadedCfg, dedicatedKeychainPath, stderr)
		if rewriteErr != nil {
			return rewriteErr
		}
		loadedCfg = reloaded
	}
	emitServerNextCommands(stderr, configPath, loadedCfg, autoEnvTokenMode || decisions.modeFor(setup.ArtifactKeychainToken) == onExistingEnvToken)
	_ = stderr.WriteText(initMsgServerComplete)
	return nil
}

// runInitServerPreflight runs the platform-ACL gate, the --on-existing
// flag validation, and the diagnostic preflight pipeline before any
// prompt fires. All output goes to stderr.
func runInitServerPreflight(ctx context.Context, deps *initDeps, stderr *Stream) error {
	if !deps.platformACL() {
		_ = stderr.WriteText(initMsgPlatformUnsupported, runtime.GOOS)
		return fmt.Errorf("%w: %s", errPlatformACLUnsupported, runtime.GOOS)
	}
	if err := validateOnExisting(deps.serverOnExisting); err != nil {
		_ = stderr.WriteText(initMsgOnExistingInvalidFmt, deps.serverOnExisting)
		return err
	}
	if deps.runPreflight != nil {
		report := deps.runPreflight(ctx)
		if pfErr := handlePreflightReport(report, deps, stderr); pfErr != nil {
			return pfErr
		}
	}
	return nil
}

// runInitServer is the orchestration entry-point for `hush init server`.
// All output goes to stderr (operator messages); stdout is intentionally
// unused so machine-piped consumers see an empty data stream on success.
func runInitServer(ctx context.Context, _, stderr *Stream, in *os.File, deps *initDeps) error {
	if err := runInitServerPreflight(ctx, deps, stderr); err != nil {
		return err
	}

	explicitStateDir := strings.TrimSpace(deps.serverInputs.stateDir) != ""

	// Resolve target paths before prompting so an existing reusable config can
	// supply its own non-secret fields. This keeps reruns from asking for
	// listen/application/channel IDs that are already present on disk.
	stateDir, err := resolveStateDir(deps.stateDirRoot)
	if err != nil {
		return err
	}
	vaultPath := filepath.Join(stateDir, "secrets.vault")
	configPath := filepath.Join(stateDir, "config.toml")
	keychainItems := defaultServerKeychainItems()
	existingCfg, _ := config.LoadServer(ctx, configPath)

	gathered, err := gatherInitServerInputs(deps, in, stderr, existingCfg, explicitStateDir)
	if err != nil {
		return err
	}
	defer gathered.destroy()
	if vErr := validateInitServerSetup(gathered, explicitStateDir, deps.serverNonInteractive, stderr); vErr != nil {
		return vErr
	}
	pass := gathered.pass
	botToken := gathered.botToken
	listenAddr := gathered.listenAddr
	ownerID := gathered.ownerID
	appID := gathered.appID
	approvalChannelID := gathered.approvalChannelID
	auditChannelID := gathered.auditChannelID

	// 4. Existence handling — classifier-first. Each non-absent artifact
	//    triggers a per-artifact prompt (interactive) or consumes
	//    --on-existing (non-interactive). The legacy keychain guard is
	//    preserved so the explicit error contract holds on `fail` mode.
	decisions, err := applyExistingArtifactGuards(ctx, in, stderr, deps, vaultPath, configPath, stateDir, keychainItems, explicitStateDir)
	if err != nil {
		return err
	}

	// 5. Resolve binary path for keychain ACL.
	binPath, err := deps.binaryPath()
	if err != nil {
		return fmt.Errorf("hush/cli: init: resolve binary path: %w", err)
	}

	// 6. Derive vault encryption key from the passphrase.
	vaultEncKey, salt, err := deriveInitServerVaultKey(ctx, deps, pass)
	if err != nil {
		return err
	}
	defer func() { _ = vaultEncKey.Destroy() }()

	// 7. State dir + empty vault + initial config + round-trip validate.
	cfgInputs := serverInputs{
		listenAddr:        listenAddr,
		ownerID:           ownerID,
		applicationID:     appID,
		stateDir:          stateDir,
		approvalChannelID: approvalChannelID,
		auditChannelID:    auditChannelID,
		botTokenKeychain:  keychainItems.discordService,
	}
	loadedCfg, err := writeInitialServerArtifacts(ctx, deps, stderr, vaultPath, configPath, stateDir, vaultEncKey, salt, &cfgInputs, decisions)
	if err != nil {
		return err
	}

	// 8. Final keychain writes + completion message — branches on whether
	//    the operator chose --state-dir.
	if explicitStateDir {
		return finalizeInitServerExplicitState(ctx, deps, stderr, in, configPath, loadedCfg, &cfgInputs, keychainItems, botToken, stateDir, binPath, decisions)
	}
	return finalizeInitServerStandard(ctx, deps, stderr, in, configPath, loadedCfg, &cfgInputs, keychainItems, pass, botToken, stateDir, binPath, decisions)
}

func emitServerNextCommands(stderr *Stream, configPath string, cfg *config.Server, envTokenMode bool) {
	if stderr == nil || cfg == nil {
		return
	}
	configArg := shellQuote(configPath)
	stateDir := cfg.Server.StateDir
	clientRegistry := cfg.Server.ClientRegistry
	if strings.TrimSpace(clientRegistry) == "" {
		clientRegistry = filepath.Join(stateDir, "clients.json")
	}
	clientKeyFile := filepath.Join(stateDir, "client-machine-1.key")
	serverURL := "http://" + cfg.Server.ListenAddr.String() + "/h/" + cfg.Server.PathPrefix
	serveCmd := "hush --config " + configArg + " serve --reload-on-vault-change"
	if envTokenMode {
		serveCmd = "HUSH_DISCORD_BOT_TOKEN=<your-bot-token> " + serveCmd
	}
	initClientCmd := "hush --config " + configArg + " init client --machine-index 1 --client-registry " + shellQuote(clientRegistry) + " --client-key-file " + shellQuote(clientKeyFile)
	secretAddCmd := "hush --config " + configArg + " secret add YOUR_SECRET"
	requestCmd := "hush --config " + configArg + " request --machine-index 1 --client-key-file " + shellQuote(clientKeyFile) + " --server " + shellQuote(serverURL) + " --scope YOUR_SECRET --ttl 5m --max-uses 1 --reason " + shellQuote("smoke test") + " --exec printenv -- YOUR_SECRET"
	_ = stderr.WriteText(initMsgServerNextCommandsFmt, serveCmd, initClientCmd, secretAddCmd, requestCmd)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func storeBotTokenUsingConfiguredKeychainPath(
	ctx context.Context,
	deps *initDeps,
	stderr *Stream,
	botToken *securebytes.SecureBytes,
	cfg *config.Server,
	service, binPath string,
) (bool, string, error) {
	if cfg == nil || strings.TrimSpace(cfg.Discord.BotKeychainPath) == "" {
		return false, "", nil
	}
	path := strings.TrimSpace(cfg.Discord.BotKeychainPath)
	if err := storeBotTokenInDedicatedKeychain(ctx, deps, stderr, botToken, path, service, binPath); err != nil {
		if errors.Is(err, keychain.ErrKeychainItemExists) {
			return true, path, nil
		}
		return true, path, err
	}
	return true, path, nil
}

// storeBotTokenForDecision honors the per-artifact recovery decision
// for the Discord bot-token Keychain slot. The decision modes drive
// distinct branches:
//
//   - [onExistingReuse] / [onExistingRepair] — leave the existing
//     Keychain item untouched; the operator either had a working item
//     already (reuse) or repaired the ACL (repair).
//   - [onExistingEnvToken] — env-token fallback: skip the Keychain
//     write entirely and let `hush serve` read HUSH_DISCORD_BOT_TOKEN.
//   - default (including [onExistingRecreate] after an inline delete)
//     — emit the hush-authored pre-explanation and Store the supplied
//     token under the configured ACL.
func storeBotTokenForDecision(
	ctx context.Context,
	deps *initDeps,
	stderr *Stream,
	in *os.File,
	keychainItems serverKeychainItemNames,
	botToken *securebytes.SecureBytes,
	stateDir string,
	binPath string,
	decisions recoveryDecisions,
) (bool, string, error) {
	switch decisions.modeFor(setup.ArtifactKeychainToken) {
	case onExistingReuse, onExistingRepair:
		return false, "", nil
	case onExistingEnvToken:
		return true, "", nil
	default:
		emitKeychainPreExplain(stderr, "Discord bot token", keychainItems.discordService, kcAccountServer)
		if err := deps.keychain.Store(ctx, keychainItems.discordService, kcAccountServer, botToken, binPath); err != nil {
			if !isRecoverableBotTokenStoreError(err) {
				_ = stderr.WriteText(initMsgKeychainStoreFailFmt, err)
				return false, "", err
			}
			if deps.serverNonInteractive {
				_ = stderr.WriteText(initMsgKeychainStoreNonInteractiveFmt, err)
				return false, "", fmt.Errorf("%w: %w", errKeychainStoreNonInteractive, err)
			}
			return promptKeychainStoreRecovery(ctx, deps, stderr, in, keychainItems, botToken, stateDir, binPath, err)
		}
		return false, "", nil
	}
}

var (
	errKeychainStoreNonInteractive         = errors.New("hush/cli: init: bot-token Keychain write failed in non-interactive mode")
	errKeychainStoreRecoveryExhausted      = errors.New("hush/cli: init: bot-token Keychain recovery loop exhausted")
	errDedicatedKeychainFactoryUnavailable = errors.New("hush/cli: init: dedicated keychain factory unavailable")
)

// keychainStoreRecoveryOutcome is the per-attempt result of a single
// pass through the bot-token store recovery loop. `done` signals a
// terminal outcome (success, user abort, or non-recoverable failure);
// when false the loop refreshes `cause` and continues.
type keychainStoreRecoveryOutcome struct {
	done             bool
	autoEnvTokenMode bool
	dedicatedPath    string
	err              error
	nextCause        error
}

func promptKeychainStoreRecovery(
	ctx context.Context,
	deps *initDeps,
	stderr *Stream,
	in *os.File,
	keychainItems serverKeychainItemNames,
	botToken *securebytes.SecureBytes,
	stateDir string,
	binPath string,
	cause error,
) (bool, string, error) {
	for attempt := 0; attempt < 5; attempt++ {
		emitKeychainStoreCauseMessage(stderr, keychainItems.discordService, cause)
		if deps.promptRecovery == nil {
			return false, "", errNoRecoverySeam
		}
		ch, err := deps.promptRecovery(in, stderr.w, initMsgKeychainStoreRecoveryPrompt)
		if err != nil {
			return false, "", err
		}
		outcome := handleKeychainStoreRecoveryChoice(ctx, deps, stderr, ch, keychainItems, botToken, stateDir, binPath, cause)
		if outcome.done {
			return outcome.autoEnvTokenMode, outcome.dedicatedPath, outcome.err
		}
		if outcome.nextCause != nil {
			cause = outcome.nextCause
		}
	}
	return false, "", errKeychainStoreRecoveryExhausted
}

func emitKeychainStoreCauseMessage(stderr *Stream, service string, cause error) {
	if errors.Is(cause, keychain.ErrKeychainLocked) {
		_ = stderr.WriteText(initMsgKeychainStoreLockedFmt, cause, service, kcAccountServer)
		return
	}
	_ = stderr.WriteText(initMsgKeychainStoreDeniedFmt, cause, service, kcAccountServer)
}

func handleKeychainStoreRecoveryChoice(
	ctx context.Context,
	deps *initDeps,
	stderr *Stream,
	ch rune,
	keychainItems serverKeychainItemNames,
	botToken *securebytes.SecureBytes,
	stateDir string,
	binPath string,
	cause error,
) keychainStoreRecoveryOutcome {
	switch ch {
	case keychainStoreRecoveryRetry:
		return retryKeychainStore(ctx, deps, stderr, keychainItems, botToken, binPath, cause)
	case keychainStoreRecoveryDedicated:
		return useDedicatedKeychain(ctx, deps, stderr, keychainItems, botToken, stateDir, binPath)
	case keychainStoreRecoveryEnvToken:
		_ = stderr.WriteText(initMsgBotTokenEnvAutoFallback)
		return keychainStoreRecoveryOutcome{done: true, autoEnvTokenMode: true}
	case keychainStoreRecoveryQuit:
		_ = stderr.WriteText(initMsgRecoveryUserAborted)
		return keychainStoreRecoveryOutcome{done: true, err: errUserAborted}
	default:
		_ = stderr.WriteText("hush: init: invalid choice %q; pick one of [r/h/e/q].", string(ch))
		return keychainStoreRecoveryOutcome{}
	}
}

func retryKeychainStore(
	ctx context.Context,
	deps *initDeps,
	stderr *Stream,
	keychainItems serverKeychainItemNames,
	botToken *securebytes.SecureBytes,
	binPath string,
	cause error,
) keychainStoreRecoveryOutcome {
	if errors.Is(cause, keychain.ErrKeychainLocked) && deps.unlockLoginKeychain != nil {
		if unlockErr := deps.unlockLoginKeychain(ctx); unlockErr != nil {
			_ = stderr.WriteText(initMsgKeychainUnlockFailedFmt, unlockErr)
			return keychainStoreRecoveryOutcome{}
		}
	}
	if err := deps.keychain.Store(ctx, keychainItems.discordService, kcAccountServer, botToken, binPath); err != nil {
		if !isRecoverableBotTokenStoreError(err) {
			_ = stderr.WriteText(initMsgKeychainStoreFailFmt, err)
			return keychainStoreRecoveryOutcome{done: true, err: err}
		}
		return keychainStoreRecoveryOutcome{nextCause: err}
	}
	_ = stderr.WriteText(initMsgKeychainStoreRetryOK)
	return keychainStoreRecoveryOutcome{done: true}
}

func useDedicatedKeychain(
	ctx context.Context,
	deps *initDeps,
	stderr *Stream,
	keychainItems serverKeychainItemNames,
	botToken *securebytes.SecureBytes,
	stateDir string,
	binPath string,
) keychainStoreRecoveryOutcome {
	path := dedicatedHushKeychainPath(stateDir)
	if err := storeBotTokenInDedicatedKeychain(ctx, deps, stderr, botToken, path, keychainItems.discordService, binPath); err != nil {
		return keychainStoreRecoveryOutcome{done: true, err: err}
	}
	_ = stderr.WriteText(initMsgDedicatedKeychainSelected, path)
	return keychainStoreRecoveryOutcome{done: true, dedicatedPath: path}
}

func storeBotTokenInDedicatedKeychain(
	ctx context.Context,
	deps *initDeps,
	stderr *Stream,
	botToken *securebytes.SecureBytes,
	keychainPath, service, binPath string,
) error {
	if deps.keychainFactory == nil {
		return errDedicatedKeychainFactoryUnavailable
	}
	kc, err := deps.keychainFactory(keychainPath)
	if err != nil {
		return fmt.Errorf("hush/cli: init: create dedicated keychain %s: %w", keychainPath, err)
	}
	if dedicated, ok := kc.(keychain.DedicatedKeychainManager); ok {
		if err := dedicated.EnsureDedicatedKeychain(ctx); err != nil {
			return err
		}
	}
	emitKeychainPreExplain(stderr, "Discord bot token", service, kcAccountServer)
	if err := kc.Store(ctx, service, kcAccountServer, botToken, binPath); err != nil {
		return err
	}
	return nil
}

func isRecoverableBotTokenStoreError(err error) bool {
	return errors.Is(err, keychain.ErrKeychainLocked) || errors.Is(err, keychain.ErrKeychainPermissionDenied)
}

// emitKeychainPreExplain writes the hush-authored multi-line
// explanation that must precede every macOS Keychain write call.
// Tests scan the transcript for this string;
// see init_test.go TestInitServer_PreExplainsKeychainWrites.
func emitKeychainPreExplain(stderr *Stream, purpose, service, account string) {
	_ = stderr.WriteText(initMsgKeychainPreExplainFmt, purpose, service, account)
}

// runInitClient is the orchestration entry-point for `hush init client`.
//
//nolint:gocognit,gocyclo,cyclop,nestif // sequential bootstrap flow; complexity is structural
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
		if deps.clientPassphrase == nil {
			return fmt.Errorf("%w: clientPassphrase", errMissingFlag)
		}
		pass, err = cloneSecureBytes(deps.clientPassphrase)
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
	useClientKeyFile := strings.TrimSpace(deps.clientKeyFile) != ""
	if !useClientKeyFile {
		if guardErr := guardKeychainAbsent(ctx, deps.keychain, kcServiceClient, account, stderr); guardErr != nil {
			return guardErr
		}
	}

	binPath, err := deps.binaryPath()
	if err != nil {
		return fmt.Errorf("hush/cli: init: resolve binary path: %w", err)
	}

	// Salt for client-mode derivation: client mode does not write a
	// vault file, so the salt is sourced fresh from randReader. The
	// derivation is deterministic for a given (passphrase, salt,
	// machine-index) tuple; the operator's passphrase is the source
	// of operator-side entropy. The same master
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

	if useClientKeyFile {
		_ = stderr.WriteText(initMsgClientKeyFileSelected)
		if err := writeClientKeyFile(deps.clientKeyFile, priv); err != nil {
			return err
		}
	} else if err := deps.keychain.Store(ctx, kcServiceClient, account, priv, binPath); err != nil {
		_ = stderr.WriteText(initMsgKeychainStoreFailFmt, err)
		return err
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

//nolint:gocognit // read-modify-write over heterogeneous registry states
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
func promptOptional(reader func(*os.File, io.Writer, string) (string, error), in *os.File, prompt io.Writer, label string) (string, error) {
	v, err := reader(in, prompt, label)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(v), nil
}

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
	listenAddr           string
	pathPrefix           string
	ownerID              string
	applicationID        string
	stateDir             string
	approvalChannelID    string
	auditChannelID       string
	botTokenKeychain     string
	botTokenKeychainPath string
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
			BotKeychainPath:      strings.TrimSpace(in.botTokenKeychainPath),
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
	BotKeychainPath      string `toml:"bot_keychain_path,omitempty"`
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
