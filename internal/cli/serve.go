package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/mrz1836/hush/internal/audit"
	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/discord"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/logging"
	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/token"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// passphraseSource is the testable seam for vault-passphrase
// resolution. The default implementation is resolvePassphrase; tests
// inject substitutes that return programmed bytes without touching
// real terminals.
type passphraseSource func(ctx context.Context, in *os.File, prompt io.Writer) (*securebytes.SecureBytes, error)

// approverFactory constructs the chassis's server.Approver and the
// connection-state probe surfaced via /hz. The production factory
// reads the bot token from the OS keychain, constructs a
// *discord.BotApprover, and wraps it in a translation adapter that
// matches the chassis's Approver interface. The integration test
// substitutes a stub.
type approverFactory func(ctx context.Context, cfg *config.Server, logger *slog.Logger) (server.Approver, func() bool, error)

// discordApproverAdapter bridges discord.Approver (the production
// implementation) to server.Approver (the chassis-side interface).
// Translates field names and decision/error sentinels.
type discordApproverAdapter struct {
	inner discord.Approver
}

func (a discordApproverAdapter) RequestApproval(ctx context.Context, req server.ApprovalRequest) (server.Decision, error) {
	dec, err := a.inner.RequestApproval(ctx, discord.ApprovalRequest{
		MachineName:  req.MachineName,
		ClientIP:     req.ClientIP.String(),
		Reason:       req.Reason,
		Scope:        req.Scope,
		RequestedTTL: req.RequestedTTL,
		SessionType:  mapSessionType(req.SessionType),
	})
	if err != nil {
		switch {
		case errors.Is(err, discord.ErrApprovalDenied):
			return server.Decision{}, server.ErrApproverDenied
		case errors.Is(err, discord.ErrApprovalTimeout):
			return server.Decision{}, server.ErrApproverTimeout
		case errors.Is(err, discord.ErrDiscordUnavailable):
			return server.Decision{}, server.ErrApproverUnavailable
		case errors.Is(err, discord.ErrRateLimited):
			return server.Decision{}, server.ErrApproverRateLimited
		default:
			return server.Decision{}, err
		}
	}
	if !dec.Approved {
		return server.Decision{}, server.ErrApproverDenied
	}
	return server.Decision{
		Approved:   true,
		ApprovedAt: time.Now(),
		GrantedTTL: dec.ApprovedTTL,
		ApproverID: "discord",
		Reason:     dec.Reason,
	}, nil
}

func mapSessionType(t server.SessionType) token.SessionType {
	switch t {
	case server.SessionInteractive:
		return token.SessionInteractive
	case server.SessionSupervisor:
		return token.SessionSupervisor
	default:
		return token.SessionInteractive
	}
}

// serveDeps groups the testable seams threaded into runServe.
type serveDeps struct {
	configPath       string
	verbose          bool
	passphraseSource passphraseSource
	approverFactory  approverFactory
	listener         net.Listener

	// Chassis-internal test seams. Production paths leave these
	// nil; the integration test overrides each to bypass the
	// platform-specific startup probes.
	clockSyncProbe  func(ctx context.Context) (bool, time.Duration, error)
	interfaceLister func() ([]net.Addr, error)
}

func newServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the hush vault server",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			flags := readGlobalFlags(cmd)
			deps := serveDeps{
				configPath:       flags.configPath,
				verbose:          flags.verbose,
				passphraseSource: resolvePassphrase,
				approverFactory:  newProductionBotApprover,
			}
			return runServe(cmd.Context(), out.stdout, out.stderr, deps)
		},
	}
}

// runServe is the chassis-composition path. Each step's failure is
// wrapped with %w so mapErr can match against the locked sentinels.
//
//nolint:gocognit,gocyclo,cyclop,contextcheck // sequential 11-step composition; complexity is structural per research.md §10. The audit goroutine intentionally runs on a parallel ctx so it survives parent-cancel for shutdown drain.
func runServe(ctx context.Context, stdout, stderr *Stream, deps serveDeps) error {
	_ = stdout
	verbose := func(format string, args ...any) {
		if deps.verbose {
			_ = stderr.WriteText(format, args...)
		}
	}

	// 1. Resolve config path (~ expansion).
	configPath, err := expandTilde(deps.configPath)
	if err != nil {
		return fmt.Errorf("%w: %w", errConfigUnreadable, err)
	}
	verbose("config: loading %s", configPath)

	cfg, err := config.LoadServer(ctx, configPath)
	if err != nil {
		return err
	}
	verbose("config: loaded")

	// 2. Resolve passphrase. Production: stdin-pipe → TTY-prompt → fail.
	passphrase, err := deps.passphraseSource(ctx, os.Stdin, stderr.w)
	if err != nil {
		if errors.Is(err, errNoPassphraseSource) {
			_ = stderr.WriteText("no passphrase source: stdin is not a pipe and is not a terminal")
		}
		return err
	}
	defer func() { _ = passphrase.Destroy() }()
	verbose("passphrase: resolved")

	// 3. Read salt from the on-disk vault file header (4-byte magic +
	// 1-byte version + 16-byte salt + 12-byte nonce = 33 bytes).
	salt, err := readVaultSalt(filepath.Join(cfg.Server.StateDir, "secrets.vault"))
	if err != nil {
		return err
	}

	// 4. Derive master seed.
	var masterSeed []byte
	if useErr := passphrase.Use(func(b []byte) {
		masterSeed, err = keys.DeriveMasterSeed(ctx, b, salt)
	}); useErr != nil {
		return useErr
	}
	if err != nil {
		return err
	}
	defer zeroBytes(masterSeed)
	verbose("keys: master seed derived")

	// 5. Derive subkeys.
	jwtKey, err := keys.DeriveJWTSigningKey(masterSeed)
	if err != nil {
		return err
	}
	auditKey, err := keys.DeriveAuditSigningKey(masterSeed)
	if err != nil {
		return err
	}
	vaultEncRaw, err := keys.DeriveVaultEncKey(masterSeed)
	if err != nil {
		return err
	}
	vaultEncKey, err := securebytes.New(vaultEncRaw)
	if err != nil {
		return err
	}
	verbose("keys: subkeys derived")

	// 6. Load the vault.
	vaultPath := filepath.Join(cfg.Server.StateDir, "secrets.vault")
	store, err := vault.Load(ctx, vaultPath, vaultEncKey)
	if err != nil {
		return err
	}
	var vptr atomic.Pointer[vault.Store]
	vptr.Store(&store)
	verbose("vault: loaded with %d secret(s)", len(store.Names()))

	// 7. Construct the audit writer.
	logger := logging.New(logging.Options{
		Level:  slog.LevelInfo,
		Format: logging.FormatAuto,
		Out:    stderr.w,
	})
	auditWriter, err := audit.NewWriter(ctx, cfg.Server.AuditLog, auditKey, nil, logger)
	if err != nil {
		return err
	}
	auditCtx, auditCancel := context.WithCancel(context.Background())
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		_ = auditWriter.Run(auditCtx)
	}()
	// Defers run LIFO, so register the wait first and the cancel last
	// — that way on any early return the cancel fires before we block
	// waiting for the audit goroutine to drain.
	defer func() { <-auditDone }()
	defer auditCancel()
	verbose("audit: writer started at %s", cfg.Server.AuditLog)

	// 8. Construct the Discord approver.
	approver, discordHealthFn, err := deps.approverFactory(ctx, cfg, logger)
	if err != nil {
		_ = stderr.WriteText("discord: approver init failed: %v", err)
		return err
	}
	verbose("discord: approver constructed")

	// 9. Construct the chassis.
	srvDeps := server.Deps{
		Cfg:        cfg,
		VaultPtr:   &vptr,
		TokenStore: token.NewStore(),
		TokenIssuer: func(ctx context.Context, params token.IssueParams) (*token.Token, error) {
			return token.Issue(ctx, jwtKey, params)
		},
		Approver:        approver,
		Logger:          logger,
		AuditWriter:     server.NewChassisAuditAdapter(auditWriter),
		JWTVerifyKey:    &jwtKey.PublicKey,
		DiscordHealth:   discordHealthFn,
		Listener:        deps.listener,
		VaultKey:        vaultEncKey,
		ClockSyncProbe:  deps.clockSyncProbe,
		InterfaceLister: deps.interfaceLister,
	}
	srv, err := server.New(srvDeps)
	if err != nil {
		return err
	}
	if err := srv.RegisterHandlers(); err != nil {
		return err
	}

	// 10. Bind context to OS signals; SIGINT/SIGTERM cancel the
	// chassis's Run loop which performs graceful shutdown.
	signalCtx, signalStop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer signalStop()
	verbose("server: ready")

	// 11. Run the chassis. Returns nil on clean shutdown.
	runErr := srv.Run(signalCtx)
	auditCancel()
	return runErr
}

// resolvePassphrase implements the FR-008 priority order: stdin pipe
// → TTY prompt → ExitInputErr. Never reads any environment variable.
//
// Pipe path: io.ReadAll(in) followed by stripPOSIXLineEnd —
// preserves all bytes other than exactly one trailing \n or \r\n.
//
// TTY path: term.ReadPassword(int(in.Fd())) — no echo, ever.
//
//nolint:gocognit,nestif // sequential pipe→tty→fail dispatch; branching is inherent to FR-008
func resolvePassphrase(_ context.Context, in *os.File, prompt io.Writer) (*securebytes.SecureBytes, error) {
	isTerminal := in != nil && term.IsTerminal(int(in.Fd()))

	if !isTerminal {
		stat, err := in.Stat()
		if err != nil {
			return nil, errNoPassphraseSource
		}
		// stat.Mode()&os.ModeCharDevice == 0 covers both pipes and
		// regular files; ModeCharDevice would indicate a terminal we
		// already filtered above.
		if stat.Mode()&os.ModeCharDevice != 0 {
			return nil, errNoPassphraseSource
		}
		raw, err := io.ReadAll(in)
		if err != nil {
			return nil, fmt.Errorf("hush/cli: serve: read stdin: %w", err)
		}
		clean := stripPOSIXLineEnd(raw)
		zeroBytes(raw[len(clean):])
		if len(clean) == 0 {
			return nil, errNoPassphraseSource
		}
		return securebytes.New(clean)
	}

	if _, err := io.WriteString(prompt, "Vault passphrase: "); err != nil {
		return nil, fmt.Errorf("hush/cli: serve: prompt: %w", err)
	}
	raw, err := term.ReadPassword(int(in.Fd()))
	// Newline after the no-echo read so subsequent stderr lines start
	// on a fresh row.
	_, _ = io.WriteString(prompt, "\n")
	if err != nil {
		return nil, fmt.Errorf("hush/cli: serve: read password: %w", err)
	}
	if len(raw) == 0 {
		return nil, errNoPassphraseSource
	}
	return securebytes.New(raw)
}

// stripPOSIXLineEnd removes exactly one trailing \r\n if present, or
// exactly one trailing \n if present, otherwise returns b unchanged.
// All other bytes — including additional trailing newlines, leading
// whitespace, internal whitespace — are preserved verbatim
// (FR-008a, research.md §7).
func stripPOSIXLineEnd(b []byte) []byte {
	n := len(b)
	if n >= 2 && b[n-2] == '\r' && b[n-1] == '\n' {
		return b[:n-2]
	}
	if n >= 1 && b[n-1] == '\n' {
		return b[:n-1]
	}
	return b
}

// readVaultSalt reads exactly the 16-byte salt from the on-disk
// vault file header and returns it. The header layout is
// 4-byte magic + 1-byte version + 16-byte salt + 12-byte nonce.
func readVaultSalt(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	header := make([]byte, 33)
	if _, err := io.ReadFull(f, header); err != nil {
		return nil, fmt.Errorf("hush/cli: serve: read vault header: %w", err)
	}
	salt := make([]byte, 16)
	copy(salt, header[5:21])
	return salt, nil
}

// zeroBytes overwrites b with zeros. Helper used to clear the
// master-seed slice and the post-strip suffix of the stdin read
// buffer.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// expandTilde performs leading-~ expansion against $HOME. Mirrors
// internal/config.expandHome (which is unexported); duplicated here
// because the CLI must validate the path before calling LoadServer.
func expandTilde(p string) (string, error) {
	if p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if len(p) >= 2 && p[0] == '~' && p[1] == '/' {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}

// errBotTokenMissing is returned by loadBotToken when the keychain
// helper reports no item with the configured name.
var errBotTokenMissing = errors.New("bot token unavailable from OS keychain; approve Keychain access or set HUSH_DISCORD_BOT_TOKEN")

// errBotTokenSubprocess is returned by loadBotToken on any other
// helper failure (helper not installed, helper errored, etc.).
var errBotTokenSubprocess = errors.New("bot token keychain subprocess failed")

// botTokenItemRe is the locked validation regex for keychain item
// names. Restricts the operator-supplied config field to a strict
// shell-safe alphabet so subprocess invocation cannot smuggle
// arguments via the item name. exec.CommandContext with a fixed
// argv vector is the primary defense; this regex is defense in
// depth.
var botTokenItemRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,128}$`)

// loadBotToken reads the Discord bot token from the OS keychain. On
// Darwin: `security find-generic-password -s <item> -w`. On Linux:
// `secret-tool lookup service hush attribute <item>`. Returns the
// token wrapped in *securebytes.SecureBytes.
func loadBotToken(ctx context.Context, item string) (*securebytes.SecureBytes, error) {
	// Smoke bootstrap fallback for non-interactive hosts where macOS
	// Keychain writes require a SecurityAgent prompt. Production
	// deploys should keep using the configured Keychain item below.
	if envToken, ok := os.LookupEnv("HUSH_DISCORD_BOT_TOKEN"); ok && envToken != "" {
		return securebytes.New([]byte(envToken))
	}
	if !botTokenItemRe.MatchString(item) {
		return nil, fmt.Errorf("%w: invalid item name", errBotTokenMissing)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "security", "find-generic-password", "-s", item, "-w") //nolint:gosec // fixed argv; item validated by regex
	case "linux":
		cmd = exec.CommandContext(ctx, "secret-tool", "lookup", "service", "hush", "attribute", item) //nolint:gosec // fixed argv; item validated by regex
	default:
		return nil, fmt.Errorf("%w: unsupported platform %s", errBotTokenSubprocess, runtime.GOOS)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return nil, fmt.Errorf("%w: %s", errBotTokenMissing, ee.Error())
		}
		return nil, fmt.Errorf("%w: %w", errBotTokenSubprocess, err)
	}
	if len(out) > 1024 {
		out = out[:1024]
	}
	out = stripPOSIXLineEnd(out)
	if len(out) == 0 {
		return nil, errBotTokenMissing
	}
	return securebytes.New(out)
}

// newProductionBotApprover is the production approverFactory: reads
// the bot token from the OS keychain, constructs a
// *discord.BotApprover, wraps it in the translation adapter, and
// returns the chassis-facing Approver + the Connected() probe used
// by /hz.
func newProductionBotApprover(ctx context.Context, cfg *config.Server, logger *slog.Logger) (server.Approver, func() bool, error) {
	tokenSB, err := loadBotToken(ctx, cfg.Discord.BotTokenKeychainItem)
	if err != nil {
		return nil, nil, err
	}
	approver, err := discord.NewBotApprover(ctx, discord.BotConfig{
		Token:             tokenSB,
		OwnerID:           cfg.Server.DiscordOwnerID,
		AppID:             cfg.Discord.ApplicationID,
		ApprovalChannelID: cfg.Server.DiscordApprovalChannelID,
		AuditChannelID:    cfg.Server.DiscordAuditChannelID,
		DMRateLimit:       5 * time.Minute,
	}, logger)
	if err != nil {
		return nil, nil, err
	}
	_ = tokenSB.Destroy()
	return discordApproverAdapter{inner: approver}, approver.Connected, nil
}
