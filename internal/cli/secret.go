// Package cli — `hush secret` subcommand: vault-entry management.
//
// SDD-17. Mounts on the SDD-14 cobra root via newSecretCmd() (no new
// exported package-level symbols). Four verbs:
//   - add NAME    — append a new entry (TTY-only; refuses piped stdin)
//   - remove NAME — delete a named entry (typed-name confirmation)
//   - list        — enumerate entries (text on TTY, JSON on pipe; never
//     prints values)
//   - rotate      — re-encrypt the vault and signal a running server
//     via SIGHUP (tolerates missing PID)
//
// The TTY-first refusal across every verb (including `list`) is the
// documented defence against the "rogue process runs hush secret add"
// threat row in docs/SECURITY.md (cited inline below).
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Locked stderr literals — every byte is contract-asserted by tests
// (contracts/cli-secret.md §3). gosec G101 false-positive: these are
// user-facing diagnostic messages, not credentials.
//
//nolint:gosec // user-facing diagnostic messages, not credentials
const (
	secretMsgNoTTY               = "hush: secret: this command requires an interactive TTY (rogue-process defence)"
	secretMsgInvalidName         = "hush: secret: NAME must match ^[A-Z_][A-Z0-9_]*$ (1–64 chars)"
	secretMsgValueMismatch       = "hush: secret: secret value confirmation does not match"
	secretMsgExistsFmt           = "hush: secret: entry %s already exists; use 'hush secret rotate' to replace"
	secretMsgRemoveTokenMismatch = "hush: secret: typed name does not match the entry argument"
	secretMsgEmptyVault          = "(vault is empty)"
	secretMsgPidPresentFmt       = "hush: secret: signalled running server (pid=%d)"
	secretMsgPidAbsent           = "hush: secret: no running server signalled (no PID file)"
	secretMsgPidStale            = "hush: secret: no running server signalled (stale PID file)"
	secretMsgPidNotOurUser       = "hush: secret: no running server signalled (PID owned by another user)"
	secretMsgPidUnreadable       = "hush: secret: no running server signalled (PID file unreadable)"
)

// Locked prompt labels (contracts/cli-secret.md §4).
const (
	promptSecretValue        = "Secret value: "
	promptConfirmSecretValue = "Confirm secret value: "
	promptDescription        = "Description (optional): "
	promptRemoveConfirmName  = "Type the entry name to confirm: "
)

// pidFilename is the filename component of the server PID file under
// <state_dir>/. SDD-17 §"Implementation contract" inlines the literal
// here; no other component currently writes the PID file (a future
// SDD chunk wires it into `serve`). The rogue-process threat row in
// docs/SECURITY.md is the documented defence motivating the universal
// stdin-TTY gate.
const pidFilename = "hush.pid"

// vaultFilename is the on-disk vault filename under <state_dir>/. Same
// literal as the server-side constant; duplicated here so this chunk
// does not depend on the server package.
const secretsVaultFilename = "secrets.vault"

// secretNameRE enforces the FR-017 entry-name shape. Length 1–64 is
// checked separately so the error is more specific than a regex
// failure for long names.
var secretNameRE = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

const secretNameMaxLen = 64

// Verb-internal sentinels. All four route through the existing mapErr
// classifier via errors.Is wraps — no edits to exit_codes.go.
var (
	// errInvalidSecretName surfaces a name failing the regex / length
	// check. Wraps errMissingFlag → ExitInputErr.
	errInvalidSecretName = fmt.Errorf("hush: secret: invalid entry name: %w", errMissingFlag)

	// errSecretValueMismatch surfaces an `add` confirmation prompt
	// that did not match the first prompt. Wraps errPassphraseMismatch
	// → ExitInputErr.
	errSecretValueMismatch = fmt.Errorf("hush: secret: value confirmation mismatch: %w", errPassphraseMismatch)

	// errConfirmationMismatch surfaces a `remove` typed-name
	// confirmation that did not match the NAME argument. Wraps
	// errPassphraseMismatch → ExitInputErr.
	errConfirmationMismatch = fmt.Errorf("hush: secret: typed-name confirmation mismatch: %w", errPassphraseMismatch)

	// errSecretExists surfaces an `add` for a name that already
	// exists. Catch-all classification (ExitErr) — the operator-facing
	// message is the contractual signal.
	errSecretExists = errors.New("hush: secret: entry already exists; use 'hush secret rotate' to replace")
)

// pidStatus enumerates the PID-file outcomes that drive the rotate
// stderr message and the audit-log "signalled" boolean.
type pidStatus uint8

const (
	pidPresent    pidStatus = iota // PID file exists, parses, owned by us
	pidAbsent                      // no PID file at <state_dir>/hush.pid
	pidStale                       // PID file present but no live process at that PID
	pidNotOurUser                  // PID file present, process exists, but we cannot signal it (EPERM)
	pidUnreadable                  // PID file present but unreadable / unparseable
)

// listEntry is the exact JSON wire shape emitted by `hush secret list`
// when stdout is not a TTY. Field order matters — encoding/json
// preserves struct declaration order in its output.
type listEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// secretDeps groups the testable seams threaded into every verb.
type secretDeps struct {
	loadSecrets func(ctx context.Context, path string, key *securebytes.SecureBytes) ([]vault.Secret, error)
	saveVault   func(ctx context.Context, path string, key *securebytes.SecureBytes, salt []byte, secrets []vault.Secret) error

	promptPassphrase func(in *os.File, prompt io.Writer, label string) (*securebytes.SecureBytes, error)
	promptSecret     func(in *os.File, prompt io.Writer, label string) (*securebytes.SecureBytes, error)
	promptLine       func(in *os.File, prompt io.Writer, label string) (string, error)

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

	nonInteractive bool
	passphrase     string
	secretValue    string
	description    string
}

// productionSecretDeps wires the real seams. Tests construct a custom
// secretDeps directly.
func productionSecretDeps() *secretDeps {
	return &secretDeps{
		loadSecrets:      vault.LoadSecrets,
		saveVault:        vault.SaveWithSalt,
		promptPassphrase: readPassphraseTTY,
		promptSecret:     readPassphraseTTY,
		promptLine:       readLineFromTTY,
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

// newSecretCmd builds the `hush secret` parent. No RunE — invoking
// `hush secret` without a verb prints help (default cobra behaviour).
func newSecretCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage vault entries (add, remove, list, rotate)",
	}
	cmd.AddCommand(newSecretAddCmd())
	cmd.AddCommand(newSecretRemoveCmd())
	cmd.AddCommand(newSecretListCmd())
	cmd.AddCommand(newSecretRotateCmd())
	return cmd
}

type secretAddInput struct {
	VaultPassphrase string `json:"vault_passphrase"`
	Value           string `json:"value"`
	Description     string `json:"description"`
}

func readSecretAddInput(path string) (secretAddInput, error) {
	if strings.TrimSpace(path) == "" {
		return secretAddInput{}, fmt.Errorf("%w: --input-file", errMissingFlag)
	}
	body, err := os.ReadFile(path) //nolint:gosec // operator-supplied bootstrap file path
	if err != nil {
		return secretAddInput{}, fmt.Errorf("hush: secret: read input file: %w", err)
	}
	var input secretAddInput
	if err := json.Unmarshal(body, &input); err != nil {
		return secretAddInput{}, fmt.Errorf("hush: secret: decode input file: %w", err)
	}
	return input, nil
}

func newSecretAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add NAME",
		Short: "Add a new vault entry (interactive TTY by default)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := outputFromCmd(cmd)
			deps := productionSecretDeps()
			deps.configPath = readGlobalFlags(cmd).configPath
			if nonInteractive, _ := cmd.Flags().GetBool("non-interactive"); nonInteractive {
				deps.nonInteractive = true
				inputFile, _ := cmd.Flags().GetString("input-file")
				input, inputErr := readSecretAddInput(inputFile)
				if inputErr != nil {
					return inputErr
				}
				deps.passphrase = input.VaultPassphrase
				deps.secretValue = input.Value
				deps.description = input.Description
			}
			return runSecretAdd(cmd.Context(), out.stderr, os.Stdin, deps, args)
		},
	}
	cmd.Flags().Bool("non-interactive", false, "Read add inputs from environment instead of TTY prompts")
	cmd.Flags().String("input-file", "", "0600 JSON input for --non-interactive")
	return cmd
}

func newSecretRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a vault entry (typed-name confirmation; TTY only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := outputFromCmd(cmd)
			deps := productionSecretDeps()
			deps.configPath = readGlobalFlags(cmd).configPath
			return runSecretRemove(cmd.Context(), out.stderr, os.Stdin, deps, args)
		},
	}
}

func newSecretListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List vault entries (NAME + description; values never printed)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			deps := productionSecretDeps()
			deps.configPath = readGlobalFlags(cmd).configPath
			return runSecretList(cmd.Context(), out.stdout, out.stderr, os.Stdin, os.Stdout, deps)
		},
	}
}

func newSecretRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate",
		Short: "Re-encrypt the vault and signal a running server via SIGHUP",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			deps := productionSecretDeps()
			deps.configPath = readGlobalFlags(cmd).configPath
			return runSecretRotate(cmd.Context(), out.stderr, os.Stdin, deps)
		},
	}
}

// validateSecretName runs the FR-017 regex + length check. Runs BEFORE
// any vault I/O — a malformed name returns errInvalidSecretName which
// mapErr classifies as ExitInputErr via the errMissingFlag wrap.
func validateSecretName(name string) error {
	if len(name) < 1 || len(name) > secretNameMaxLen {
		return errInvalidSecretName
	}
	if !secretNameRE.MatchString(name) {
		return errInvalidSecretName
	}
	return nil
}

// auditEvent emits a structured audit record with the canonical Phase
// 4 secret-verb schema: every record carries `verb` and `outcome`,
// optional `name` (omitted when empty), and any caller-supplied extras
// appended verbatim. Centralizing the shape prevents drift across the
// ten-plus call sites in this file when SDD-13 hardens the audit log.
func auditEvent(ctx context.Context, logger *slog.Logger, level slog.Level, event, verb, name, outcome string, extras ...any) {
	args := make([]any, 0, 6+len(extras))
	args = append(args, "verb", verb)
	if name != "" {
		args = append(args, "name", name)
	}
	args = append(args, "outcome", outcome)
	args = append(args, extras...)
	logger.Log(ctx, level, event, args...)
}

// enforceStdinTTY is the universal first-line defence across every
// verb. Returns errNoTTY (mapped to ExitInputErr) on a piped stdin
// AND emits the contract-locked stderr message and a security-relevant
// slog WARN record so an operator monitoring stderr sees the refusal
// AND the audit log captures the attempt.
func enforceStdinTTY(ctx context.Context, in *os.File, deps *secretDeps, stderr *Stream, verb string) error {
	if deps.isStdinTTY(in) {
		return nil
	}
	_ = stderr.WriteText(secretMsgNoTTY)
	auditEvent(ctx, deps.logger, slog.LevelWarn, "secret_tty_refused", verb, "", "tty_refused")
	return errNoTTY
}

// resolveVaultPath returns the absolute path to the on-disk vault file
// based on deps.stateDirRoot (test override) or the loaded server
// config (production).
func resolveVaultPath(ctx context.Context, deps *secretDeps) (string, error) {
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

// resolveStateDirPath returns the absolute state directory path used
// to look up the PID file. Mirrors resolveVaultPath but stops at the
// directory.
func resolveStateDirPath(ctx context.Context, deps *secretDeps) (string, error) {
	if deps.stateDirRoot != "" {
		return deps.stateDirRoot, nil
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
	return cfg.Server.StateDir, nil
}

// deriveVaultKey runs the passphrase → master seed → vault encryption
// key derivation. Returns the AES-GCM key wrapped in *SecureBytes; the
// caller owns it and MUST Destroy.
func deriveVaultKey(ctx context.Context, deps *secretDeps, passphrase *securebytes.SecureBytes, salt []byte) (*securebytes.SecureBytes, error) {
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

// destroySecrets calls Destroy on every value SecureBytes inside the
// supplied slice. LIFO — last allocated first destroyed — preserves
// the invariant that the slice's first element is the oldest handle.
func destroySecrets(secrets []vault.Secret) {
	for i := len(secrets) - 1; i >= 0; i-- {
		if secrets[i].Value != nil {
			_ = secrets[i].Value.Destroy()
		}
	}
}

// runSecretAdd implements the `add` flow. Order:
// stdin-TTY gate → name validation → passphrase prompt → derive vault
// key → load vault → secret value prompt → confirm-value prompt →
// description prompt → exists check → append → save → audit → ExitOK.
//
//nolint:gocognit,gocyclo,cyclop // sequential add flow; complexity is structural per data-model §3.1
func runSecretAdd(ctx context.Context, stderr *Stream, in *os.File, deps *secretDeps, args []string) error {
	if !deps.nonInteractive {
		if err := enforceStdinTTY(ctx, in, deps, stderr, "add"); err != nil {
			return err
		}
	}
	name := args[0]
	if err := validateSecretName(name); err != nil {
		_ = stderr.WriteText(secretMsgInvalidName)
		return err
	}

	vaultPath, err := resolveVaultPath(ctx, deps)
	if err != nil {
		return err
	}

	var passphrase *securebytes.SecureBytes
	if deps.nonInteractive {
		passphrase, err = securebytes.New([]byte(deps.passphrase))
	} else {
		passphrase, err = deps.promptPassphrase(in, stderr.w, promptVaultPassphrase)
	}
	if err != nil {
		return err
	}
	defer func() { _ = passphrase.Destroy() }()

	salt, err := deps.readVaultSalt(vaultPath)
	if err != nil {
		return err
	}

	vaultKey, err := deriveVaultKey(ctx, deps, passphrase, salt)
	if err != nil {
		return err
	}
	defer func() { _ = vaultKey.Destroy() }()

	secrets, err := deps.loadSecrets(ctx, vaultPath, vaultKey)
	if err != nil {
		if errors.Is(err, vault.ErrAuthFailed) {
			auditEvent(ctx, deps.logger, slog.LevelWarn, "secret_passphrase_failed", "add", name, "passphrase_failed")
		}
		return err
	}
	defer destroySecrets(secrets)

	var value *securebytes.SecureBytes
	var description string
	if deps.nonInteractive {
		value, err = securebytes.New([]byte(deps.secretValue))
		description = deps.description
	} else {
		value, err = deps.promptSecret(in, stderr.w, promptSecretValue)
	}
	if err != nil {
		return err
	}
	defer func() { _ = value.Destroy() }()

	if !deps.nonInteractive {
		confirm, confirmErr := deps.promptSecret(in, stderr.w, promptConfirmSecretValue)
		if confirmErr != nil {
			return confirmErr
		}
		defer func() { _ = confirm.Destroy() }()

		equal, cmpErr := secureBytesEqual(value, confirm)
		if cmpErr != nil {
			return cmpErr
		}
		if !equal {
			_ = stderr.WriteText(secretMsgValueMismatch)
			auditEvent(ctx, deps.logger, slog.LevelWarn, "secret_confirmation_mismatch", "add", name, "value_mismatch")
			return errSecretValueMismatch
		}

		description, err = deps.promptLine(in, stderr.w, promptDescription)
		if err != nil {
			return err
		}
	}

	for _, s := range secrets {
		if s.Name == name {
			_ = stderr.WriteText(secretMsgExistsFmt, name)
			return errSecretExists
		}
	}

	// Append a fresh Secret carrying our typed value SecureBytes. The
	// destroySecrets defer above only iterates the original pre-load
	// slice; the typed value SecureBytes is destroyed by the `value`
	// defer above. vault.Save does not retain the reference.
	combined := make([]vault.Secret, 0, len(secrets)+1)
	combined = append(combined, secrets...)
	combined = append(combined, vault.Secret{Name: name, Description: description, Value: value})

	if err := deps.saveVault(ctx, vaultPath, vaultKey, salt, combined); err != nil {
		return err
	}

	auditEvent(ctx, deps.logger, slog.LevelInfo, "secret_added", "add", name, "success")
	return nil
}

// runSecretRemove implements the `remove` flow. Order:
// stdin-TTY gate → name validation → passphrase → load → not-found
// check → confirmation prompt → typed-name compare → filter → save →
// audit → ExitOK.
//
//nolint:gocognit,gocyclo,cyclop // sequential remove flow; complexity is structural per data-model §3.2
func runSecretRemove(ctx context.Context, stderr *Stream, in *os.File, deps *secretDeps, args []string) error {
	if err := enforceStdinTTY(ctx, in, deps, stderr, "remove"); err != nil {
		return err
	}
	name := args[0]
	if err := validateSecretName(name); err != nil {
		_ = stderr.WriteText(secretMsgInvalidName)
		return err
	}

	vaultPath, err := resolveVaultPath(ctx, deps)
	if err != nil {
		return err
	}

	var passphrase *securebytes.SecureBytes
	if deps.nonInteractive {
		passphrase, err = securebytes.New([]byte(deps.passphrase))
	} else {
		passphrase, err = deps.promptPassphrase(in, stderr.w, promptVaultPassphrase)
	}
	if err != nil {
		return err
	}
	defer func() { _ = passphrase.Destroy() }()

	salt, err := deps.readVaultSalt(vaultPath)
	if err != nil {
		return err
	}

	vaultKey, err := deriveVaultKey(ctx, deps, passphrase, salt)
	if err != nil {
		return err
	}
	defer func() { _ = vaultKey.Destroy() }()

	secrets, err := deps.loadSecrets(ctx, vaultPath, vaultKey)
	if err != nil {
		if errors.Is(err, vault.ErrAuthFailed) {
			auditEvent(ctx, deps.logger, slog.LevelWarn, "secret_passphrase_failed", "remove", name, "passphrase_failed")
		}
		return err
	}
	defer destroySecrets(secrets)

	idx := -1
	for i, s := range secrets {
		if s.Name == name {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("hush: secret: %w", vault.ErrSecretNotFound)
	}

	typed, err := deps.promptLine(in, stderr.w, promptRemoveConfirmName)
	if err != nil {
		return err
	}
	if typed != name {
		_ = stderr.WriteText(secretMsgRemoveTokenMismatch)
		auditEvent(ctx, deps.logger, slog.LevelWarn, "secret_confirmation_mismatch", "remove", name, "confirmation_mismatch")
		return errConfirmationMismatch
	}

	filtered := make([]vault.Secret, 0, len(secrets)-1)
	filtered = append(filtered, secrets[:idx]...)
	filtered = append(filtered, secrets[idx+1:]...)

	if err := deps.saveVault(ctx, vaultPath, vaultKey, salt, filtered); err != nil {
		return err
	}

	auditEvent(ctx, deps.logger, slog.LevelInfo, "secret_removed", "remove", name, "success")
	return nil
}

// runSecretList implements the `list` flow. Order:
// stdin-TTY gate → passphrase → load → enumerate → destroy values →
// sort → render (TTY-aware) → ExitOK. NO audit on success.
//
//nolint:gocognit,gocyclo,cyclop // sequential list flow with TTY/pipe split
func runSecretList(ctx context.Context, stdout, stderr *Stream, in, stdoutFile *os.File, deps *secretDeps) error {
	if err := enforceStdinTTY(ctx, in, deps, stderr, "list"); err != nil {
		return err
	}

	vaultPath, err := resolveVaultPath(ctx, deps)
	if err != nil {
		return err
	}

	var passphrase *securebytes.SecureBytes
	if deps.nonInteractive {
		passphrase, err = securebytes.New([]byte(deps.passphrase))
	} else {
		passphrase, err = deps.promptPassphrase(in, stderr.w, promptVaultPassphrase)
	}
	if err != nil {
		return err
	}
	defer func() { _ = passphrase.Destroy() }()

	salt, err := deps.readVaultSalt(vaultPath)
	if err != nil {
		return err
	}

	vaultKey, err := deriveVaultKey(ctx, deps, passphrase, salt)
	if err != nil {
		return err
	}
	defer func() { _ = vaultKey.Destroy() }()

	secrets, err := deps.loadSecrets(ctx, vaultPath, vaultKey)
	if err != nil {
		if errors.Is(err, vault.ErrAuthFailed) {
			auditEvent(ctx, deps.logger, slog.LevelWarn, "secret_passphrase_failed", "list", "", "passphrase_failed")
		}
		return err
	}

	// Build entries — IMMEDIATELY destroy the value SecureBytes so
	// the renderer cannot leak them. Description is plaintext metadata
	// already in memory; we only carry that forward.
	entries := make([]listEntry, 0, len(secrets))
	for _, s := range secrets {
		entries = append(entries, listEntry{Name: s.Name, Description: s.Description})
		if s.Value != nil {
			_ = s.Value.Destroy()
		}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	if deps.isStdoutTTY(stdoutFile) {
		return renderListTTY(stdout, stderr, entries)
	}

	enc := json.NewEncoder(stdout.w)
	if err := enc.Encode(entries); err != nil {
		return fmt.Errorf("hush: secret: encode list: %w", err)
	}
	return nil
}

// renderListTTY emits the human-readable text format for `list` on
// stdout (`NAME — description\n` per entry; `NAME\n` when description
// is empty). An empty vault writes the `(vault is empty)` hint to
// stderr.
func renderListTTY(stdout, stderr *Stream, entries []listEntry) error {
	if len(entries) == 0 {
		_ = stderr.WriteText(secretMsgEmptyVault)
		return nil
	}
	for _, e := range entries {
		if e.Description == "" {
			if _, err := io.WriteString(stdout.w, e.Name+"\n"); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(stdout.w, "%s — %s\n", e.Name, e.Description); err != nil {
			return err
		}
	}
	return nil
}

// runSecretRotate implements the `rotate` flow. Order:
// stdin-TTY gate → passphrase → load → re-save (fresh nonce + salt)
// → probe PID file → SIGHUP-or-warn → audit → ExitOK.
//
//nolint:gocognit,gocyclo,cyclop // sequential rotate flow with PID-status dispatch
func runSecretRotate(ctx context.Context, stderr *Stream, in *os.File, deps *secretDeps) error {
	if err := enforceStdinTTY(ctx, in, deps, stderr, "rotate"); err != nil {
		return err
	}

	vaultPath, err := resolveVaultPath(ctx, deps)
	if err != nil {
		return err
	}
	stateDir, err := resolveStateDirPath(ctx, deps)
	if err != nil {
		return err
	}

	var passphrase *securebytes.SecureBytes
	if deps.nonInteractive {
		passphrase, err = securebytes.New([]byte(deps.passphrase))
	} else {
		passphrase, err = deps.promptPassphrase(in, stderr.w, promptVaultPassphrase)
	}
	if err != nil {
		return err
	}
	defer func() { _ = passphrase.Destroy() }()

	salt, err := deps.readVaultSalt(vaultPath)
	if err != nil {
		return err
	}

	vaultKey, err := deriveVaultKey(ctx, deps, passphrase, salt)
	if err != nil {
		return err
	}
	defer func() { _ = vaultKey.Destroy() }()

	secrets, err := deps.loadSecrets(ctx, vaultPath, vaultKey)
	if err != nil {
		if errors.Is(err, vault.ErrAuthFailed) {
			auditEvent(ctx, deps.logger, slog.LevelWarn, "secret_passphrase_failed", "rotate", "", "passphrase_failed")
		}
		return err
	}
	defer destroySecrets(secrets)

	// Re-save with the file's existing salt so the salt → KDF → vaultKey
	// chain stays coherent across rotate. The nonce is freshly minted
	// per call by SaveWithSalt (FR-009, SC-003); ciphertext bytes still
	// change while the plaintext set is preserved.
	if err := deps.saveVault(ctx, vaultPath, vaultKey, salt, secrets); err != nil {
		return err
	}

	pidPath := filepath.Join(stateDir, pidFilename)
	status, pid := probePIDFile(deps, pidPath)
	signalled := false
	switch status {
	case pidPresent:
		if killErr := deps.kill(pid, syscall.SIGHUP); killErr == nil {
			_ = stderr.WriteText(secretMsgPidPresentFmt, pid)
			signalled = true
		} else {
			_ = stderr.WriteText(secretMsgPidStale)
		}
	case pidAbsent:
		_ = stderr.WriteText(secretMsgPidAbsent)
	case pidStale:
		_ = stderr.WriteText(secretMsgPidStale)
	case pidNotOurUser:
		_ = stderr.WriteText(secretMsgPidNotOurUser)
	case pidUnreadable:
		_ = stderr.WriteText(secretMsgPidUnreadable)
	}

	auditEvent(ctx, deps.logger, slog.LevelInfo, "vault_rotated", "rotate", "", "success", "signalled", signalled)
	return nil
}

// probePIDFile reads, parses, and probes the PID file. Returns the
// pidStatus that drives the rotate stderr message and the (best-effort)
// integer PID on success.
func probePIDFile(deps *secretDeps, path string) (pidStatus, int) {
	body, err := deps.readPIDFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return pidAbsent, 0
		}
		return pidUnreadable, 0
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(body)))
	if parseErr != nil || pid <= 0 {
		return pidUnreadable, 0
	}
	if err := deps.kill(pid, 0); err != nil {
		switch {
		case errors.Is(err, syscall.ESRCH):
			return pidStale, pid
		case errors.Is(err, syscall.EPERM):
			return pidNotOurUser, pid
		default:
			return pidStale, pid
		}
	}
	return pidPresent, pid
}
