package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

const (
	smokeSecretName  = "HUSH_SMOKE_TEST"
	smokeSecretValue = "hello-from-hush"
)

const (
	smokeMsgStart             = "hush: smoke: starting fake-secret smoke test"
	smokeMsgInitServer        = "hush: smoke: initialized server state at %s"
	smokeMsgSecretAdded       = "hush: smoke: added fake secret %s"
	smokeMsgClientEnrolled    = "hush: smoke: enrolled client machine-%d"
	smokeMsgServeStarting     = "hush: smoke: starting temporary server at %s"
	smokeMsgApprove           = "hush: smoke: approve the %s request in Discord now"
	smokeMsgSuccess           = "hush: smoke: success — %s=%s"
	smokeMsgArchivedStateDir  = "hush: smoke: archived existing state dir to %s"
	smokeMsgCleanArchivedFmt  = "hush: smoke clean: archived %s to %s"
	smokeMsgCleanDestroyedFmt = "hush: smoke clean: destroyed %s"
	smokeMsgCleanAbsentFmt    = "hush: smoke clean: absent %s"
)

type smokeDeps struct {
	initDepsFactory   func() (*initDeps, error)
	secretDepsFactory func() *secretDeps
	keychainFactory   func() (keychain.Keychain, error)
	serveRunner       func(ctx context.Context, stdout, stderr *Stream, deps serveDeps) error
	requestRunner     func(ctx context.Context, stdout, stderr *Stream, deps requestDeps, flags requestFlags) error
	configLoader      func(ctx context.Context, path string) (*config.Server, error)
	promptSecret      func(in *os.File, prompt io.Writer, label string) (*securebytes.SecureBytes, error)
	promptLine        func(in *os.File, prompt io.Writer, label string) (string, error)
	isTTY             func(*os.File) bool
	nowFn             func() time.Time
	httpClient        *http.Client
}

type smokeOptions struct {
	stateDir          string
	listenAddr        string
	ownerID           string
	applicationID     string
	approvalChannelID string
	auditChannelID    string
	machineIndex      uint32
	reset             bool
	strictClock       bool
}

type smokeCleanOptions struct {
	stateDirs []string
	destroy   bool
	confirm   string
}

func productionSmokeDeps() smokeDeps {
	return smokeDeps{
		initDepsFactory:   productionInitDeps,
		secretDepsFactory: productionSecretDeps,
		keychainFactory: func() (keychain.Keychain, error) {
			return keychain.New(nil)
		},
		serveRunner:   runServe,
		requestRunner: runRequest,
		configLoader:  config.LoadServer,
		promptSecret:  readPassphraseTTY,
		promptLine:    readLineFromTTY,
		isTTY:         defaultIsTTY,
		nowFn:         time.Now,
		httpClient:    &http.Client{Timeout: 2 * time.Second},
	}
}

func newSmokeCmd() *cobra.Command {
	opts := smokeOptions{stateDir: "~/.hush-smoke", machineIndex: 1}
	cmd := &cobra.Command{
		Use:   "smoke",
		Short: "Run the guided fake-secret end-to-end smoke test",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			return runSmoke(cmd.Context(), out.stdout, out.stderr, os.Stdin, productionSmokeDeps(), opts)
		},
	}
	cmd.AddCommand(newSmokeCleanCmd())
	cmd.Flags().StringVar(&opts.stateDir, "state-dir", opts.stateDir, "Isolated smoke-test state directory")
	cmd.Flags().StringVar(&opts.listenAddr, "listen-addr", "", "Vault host Tailscale listen address (ip:port); prompts when empty")
	cmd.Flags().StringVar(&opts.ownerID, "discord-owner-id", "", "Discord owner/user snowflake; prompts when empty")
	cmd.Flags().StringVar(&opts.applicationID, "discord-application-id", "", "Discord application snowflake; prompts when empty")
	cmd.Flags().StringVar(&opts.approvalChannelID, "discord-approval-channel-id", "", "Discord approval channel snowflake; prompts when empty")
	cmd.Flags().StringVar(&opts.auditChannelID, "discord-audit-channel-id", "", "Discord audit channel snowflake; defaults to approval channel when empty")
	cmd.Flags().Uint32Var(&opts.machineIndex, "machine-index", opts.machineIndex, "Smoke client machine index")
	cmd.Flags().BoolVar(&opts.reset, "reset", false, "Archive an existing smoke state dir before starting")
	cmd.Flags().BoolVar(&opts.strictClock, "strict-clock", false, "Do not apply the smoke-only clock-skew override while serving")
	return cmd
}

func runSmoke(ctx context.Context, stdout, stderr *Stream, in *os.File, deps smokeDeps, opts smokeOptions) error {
	if !deps.isTTY(in) {
		_ = stderr.WriteText(initMsgNoTTY)
		return errNoTTY
	}
	_ = stderr.WriteText(smokeMsgStart)

	stateDir, err := expandTilde(opts.stateDir)
	if err != nil {
		return err
	}
	if opts.reset {
		archived, archiveErr := archiveSmokeStateDir(stateDir, deps.nowFn())
		if archiveErr != nil {
			return archiveErr
		}
		if archived != "" {
			_ = stderr.WriteText(smokeMsgArchivedStateDir, archived)
		}
	}

	pass, err := promptAndConfirmSecret(in, stderr.w, deps.promptSecret, promptVaultPassphrase, promptConfirmVault)
	if err != nil {
		return err
	}
	defer func() { _ = pass.Destroy() }()
	passphrase, err := secureBytesString(pass)
	if err != nil {
		return err
	}
	defer zeroBytesString(&passphrase)

	botToken, hasEnvBotToken := os.LookupEnv("HUSH_DISCORD_BOT_TOKEN")
	if !hasEnvBotToken || botToken == "" {
		bot, botErr := deps.promptSecret(in, stderr.w, promptBotToken)
		if botErr != nil {
			return botErr
		}
		defer func() { _ = bot.Destroy() }()
		botToken, err = secureBytesString(bot)
		if err != nil {
			return err
		}
		defer zeroBytesString(&botToken)
	}

	if opts.listenAddr == "" {
		opts.listenAddr, err = promptRequired(deps.promptLine, in, stderr.w, promptListenAddr, "listen_addr")
		if err != nil {
			return err
		}
	}
	if opts.ownerID == "" {
		opts.ownerID, err = promptRequired(deps.promptLine, in, stderr.w, promptOwnerID, "discord_owner_id")
		if err != nil {
			return err
		}
	}
	if opts.applicationID == "" {
		opts.applicationID, err = promptRequired(deps.promptLine, in, stderr.w, promptApplicationID, "application_id")
		if err != nil {
			return err
		}
	}
	if opts.approvalChannelID == "" {
		opts.approvalChannelID, err = promptRequired(deps.promptLine, in, stderr.w, promptApprovalChannel, "discord_approval_channel_id")
		if err != nil {
			return err
		}
	}
	if opts.auditChannelID == "" {
		opts.auditChannelID = opts.approvalChannelID
	}

	cfgPath := filepath.Join(stateDir, "config.toml")
	if err := smokeInitServer(ctx, stderr, in, deps, opts, stateDir, passphrase); err != nil {
		return err
	}
	_ = stderr.WriteText(smokeMsgInitServer, stateDir)
	if err := smokeAddSecret(ctx, stderr, in, deps, cfgPath, stateDir, passphrase); err != nil {
		return err
	}
	_ = stderr.WriteText(smokeMsgSecretAdded, smokeSecretName)
	clientKeyFile, err := smokeInitClient(ctx, stderr, in, deps, cfgPath, stateDir, passphrase, opts.machineIndex)
	if err != nil {
		return err
	}
	_ = stderr.WriteText(smokeMsgClientEnrolled, opts.machineIndex)

	cfg, err := deps.configLoader(ctx, cfgPath)
	if err != nil {
		return err
	}
	serverURL := "http://" + cfg.Server.ListenAddr.String() + "/h/" + cfg.Server.PathPrefix

	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	serveErrCh := make(chan error, 1)
	oldToken, hadOldToken := os.LookupEnv("HUSH_DISCORD_BOT_TOKEN")
	_ = os.Setenv("HUSH_DISCORD_BOT_TOKEN", botToken)
	defer restoreEnv("HUSH_DISCORD_BOT_TOKEN", oldToken, hadOldToken)

	go func() {
		serveErrCh <- deps.serveRunner(serveCtx, stdout, stderr, serveDeps{
			configPath:       cfgPath,
			verbose:          true,
			allowClockSkew:   !opts.strictClock,
			passphraseSource: fixedPassphraseSource(passphrase),
			approverFactory:  newProductionBotApprover,
		})
	}()
	_ = stderr.WriteText(smokeMsgServeStarting, serverURL)
	if err := waitForSmokeServer(ctx, deps.httpClient, serverURL, serveErrCh); err != nil {
		cancelServe()
		return err
	}

	_ = stderr.WriteText(smokeMsgApprove, smokeSecretName)
	var requestOut bytes.Buffer
	requestStdout := newStream(&requestOut, false, true)
	requestStderr := stderr
	kc, err := deps.keychainFactory()
	if err != nil {
		cancelServe()
		return err
	}
	if destroyer, ok := kc.(interface{ Destroy() }); ok {
		defer destroyer.Destroy()
	}
	if err := deps.requestRunner(ctx, requestStdout, requestStderr, requestDeps{
		keychain:     kc,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		nowFn:        deps.nowFn,
		randReader:   rand.Reader,
		hostnameFn:   os.Hostname,
		ephemeralKey: generateEphemeralKey,
		looker:       exec.LookPath,
		runner:       func(cmd *exec.Cmd) error { return cmd.Run() },
		signalCtx:    signal.NotifyContext,
	}, requestFlags{
		server:        serverURL,
		scope:         []string{smokeSecretName},
		reason:        "hush smoke test",
		ttl:           5 * time.Minute,
		maxUses:       1,
		machineIndex:  opts.machineIndex,
		clientKeyFile: clientKeyFile,
		formatMode:    formatModeEval,
	}); err != nil {
		cancelServe()
		return err
	}
	if !strings.Contains(requestOut.String(), smokeSecretValue) {
		cancelServe()
		return fmt.Errorf("hush: smoke: fake secret value was not returned")
	}
	_ = stdout.WriteText(smokeMsgSuccess, smokeSecretName, smokeSecretValue)
	cancelServe()
	select {
	case err := <-serveErrCh:
		if err != nil && ctx.Err() == nil {
			return err
		}
	case <-time.After(5 * time.Second):
		return fmt.Errorf("hush: smoke: temporary server did not stop cleanly")
	}
	return nil
}

func smokeInitServer(ctx context.Context, stderr *Stream, in *os.File, deps smokeDeps, opts smokeOptions, stateDir, passphrase string) error {
	initDeps, err := deps.initDepsFactory()
	if err != nil {
		return err
	}
	initDeps.serverNonInteractive = true
	initDeps.serverPassphrase = passphrase
	initDeps.stateDirRoot = stateDir
	initDeps.serverInputs = serverInputs{
		stateDir:          stateDir,
		listenAddr:        opts.listenAddr,
		ownerID:           opts.ownerID,
		applicationID:     opts.applicationID,
		approvalChannelID: opts.approvalChannelID,
		auditChannelID:    opts.auditChannelID,
	}
	initDeps.serverAllowClockSkew = true
	return runInitServer(ctx, newStream(io.Discard, false, true), stderr, in, initDeps)
}

func smokeAddSecret(ctx context.Context, stderr *Stream, in *os.File, deps smokeDeps, cfgPath, stateDir, passphrase string) error {
	secretDeps := deps.secretDepsFactory()
	secretDeps.configPath = cfgPath
	secretDeps.stateDirRoot = stateDir
	secretDeps.nonInteractive = true
	secretDeps.passphrase = passphrase
	secretDeps.secretValue = smokeSecretValue
	secretDeps.description = "hush built-in fake smoke-test secret"
	return runSecretAdd(ctx, stderr, in, secretDeps, []string{smokeSecretName})
}

func smokeInitClient(ctx context.Context, stderr *Stream, in *os.File, deps smokeDeps, cfgPath, stateDir, passphrase string, machineIndex uint32) (string, error) {
	initDeps, err := deps.initDepsFactory()
	if err != nil {
		return "", err
	}
	keyFile := filepath.Join(stateDir, fmt.Sprintf("client-machine-%d.key", machineIndex))
	initDeps.clientNonInteractive = true
	initDeps.clientPassphrase = passphrase
	initDeps.clientRegistry = filepath.Join(stateDir, "clients.json")
	initDeps.clientKeyFile = keyFile
	cmd := newInitClientCmd()
	cmd.Flags().Set("machine-index", fmt.Sprintf("%d", machineIndex)) //nolint:errcheck // value is generated uint32
	return keyFile, runInitClient(ctx, newStream(io.Discard, false, true), stderr, in, cmd, initDeps)
}

func promptAndConfirmSecret(in *os.File, prompt io.Writer, reader func(*os.File, io.Writer, string) (*securebytes.SecureBytes, error), label, confirmLabel string) (*securebytes.SecureBytes, error) {
	first, err := reader(in, prompt, label)
	if err != nil {
		return nil, err
	}
	if first.Len() < minPassphraseLen {
		_ = first.Destroy()
		return nil, errPassphraseTooShort
	}
	second, err := reader(in, prompt, confirmLabel)
	if err != nil {
		_ = first.Destroy()
		return nil, err
	}
	equal, cmpErr := secureBytesEqual(first, second)
	_ = second.Destroy()
	if cmpErr != nil {
		_ = first.Destroy()
		return nil, cmpErr
	}
	if !equal {
		_ = first.Destroy()
		return nil, errPassphraseMismatch
	}
	return first, nil
}

func secureBytesString(sb *securebytes.SecureBytes) (string, error) {
	var out string
	if err := sb.Use(func(b []byte) { out = string(b) }); err != nil {
		return "", err
	}
	return out, nil
}

func zeroBytesString(s *string) { *s = "" }

func fixedPassphraseSource(passphrase string) passphraseSource {
	return func(context.Context, *os.File, io.Writer) (*securebytes.SecureBytes, error) {
		return securebytes.New([]byte(passphrase))
	}
}

func waitForSmokeServer(ctx context.Context, client *http.Client, serverURL string, serveErrCh <-chan error) error {
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-serveErrCh:
			if err == nil {
				return fmt.Errorf("hush: smoke: temporary server exited before health check")
			}
			return err
		case <-deadline.C:
			return fmt.Errorf("hush: smoke: timed out waiting for temporary server health")
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/hz", nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

func archiveSmokeStateDir(path string, now time.Time) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	backup := path + ".bak-" + now.UTC().Format("20060102-150405")
	if err := os.Rename(path, backup); err != nil {
		return "", err
	}
	return backup, nil
}

func restoreEnv(key, old string, hadOld bool) {
	if hadOld {
		_ = os.Setenv(key, old)
		return
	}
	_ = os.Unsetenv(key)
}

func newSmokeCleanCmd() *cobra.Command {
	opts := smokeCleanOptions{}
	cmd := &cobra.Command{
		Use:   "clean",
		Short: "Archive or destroy isolated smoke-test state",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := outputFromCmd(cmd)
			return runSmokeClean(out.stdout, out.stderr, productionSmokeDeps(), opts)
		},
	}
	cmd.Flags().StringArrayVar(&opts.stateDirs, "state-dir", nil, "Smoke/test state dir to clean; repeatable (defaults to ~/.hush-smoke and ~/.hush-t278-validation)")
	cmd.Flags().BoolVar(&opts.destroy, "destroy", false, "Permanently delete instead of archiving (requires --confirm 'destroy smoke')")
	cmd.Flags().StringVar(&opts.confirm, "confirm", "", "Required confirmation phrase for --destroy")
	return cmd
}

func runSmokeClean(_ *Stream, stderr *Stream, deps smokeDeps, opts smokeCleanOptions) error {
	targets := opts.stateDirs
	if len(targets) == 0 {
		targets = []string{"~/.hush-smoke", "~/.hush-t278-validation"}
	}
	if opts.destroy && opts.confirm != "destroy smoke" {
		return fmt.Errorf("%w: --destroy requires --confirm 'destroy smoke'", errMissingFlag)
	}
	for _, raw := range targets {
		path, err := expandTilde(raw)
		if err != nil {
			return err
		}
		if err := validateSmokeCleanTarget(path); err != nil {
			return err
		}
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				_ = stderr.WriteText(smokeMsgCleanAbsentFmt, path)
				continue
			}
			return err
		}
		if opts.destroy {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			_ = stderr.WriteText(smokeMsgCleanDestroyedFmt, path)
			continue
		}
		archived, err := archiveSmokeStateDir(path, deps.nowFn())
		if err != nil {
			return err
		}
		if archived != "" {
			_ = stderr.WriteText(smokeMsgCleanArchivedFmt, path, archived)
		}
	}
	return nil
}

func validateSmokeCleanTarget(path string) error {
	clean := filepath.Clean(path)
	base := filepath.Base(clean)
	switch {
	case base == ".hush-smoke" || strings.HasPrefix(base, ".hush-smoke-"):
		return nil
	case base == ".hush-t278-validation" || strings.HasPrefix(base, ".hush-t278-validation-"):
		return nil
	default:
		return fmt.Errorf("%w: smoke clean refuses non-smoke state dir %q", errMissingFlag, path)
	}
}
