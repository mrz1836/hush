package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/mrz1836/hush/internal/cli/setup"
	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
)

type keychainCmdDeps struct {
	keychain        keychain.Keychain
	keychainFactory func(string) (keychain.Keychain, error)
	binaryPath      func() (string, error)
	repairACL       func(context.Context, keychain.Keychain, string, string) error
	platformACL     func() bool
}

type keychainACLRepairer interface {
	RepairACL(ctx context.Context, service, account string) error
}

var (
	errKeychainDoctorMissing = errors.New("hush: keychain doctor: bot-token item missing")
	errKeychainDoctorDenied  = errors.New("hush: keychain doctor: bot-token item denied")
	errKeychainRepairFailed  = errors.New("hush: keychain repair: bot-token ACL repair failed")
	errKeychainDoctorEmpty   = errors.New("hush/cli: keychain doctor: empty report")
	errKeychainRepairEmpty   = errors.New("hush/cli: keychain repair: empty report")
	errKeychainRecheckEmpty  = errors.New("hush/cli: keychain repair: empty re-check")
)

func productionKeychainCmdDeps() (*keychainCmdDeps, error) {
	kc, err := keychain.New(slog.Default())
	if err != nil {
		return nil, err
	}
	return &keychainCmdDeps{
		keychain: kc,
		keychainFactory: func(path string) (keychain.Keychain, error) {
			return keychain.NewAtPath(slog.Default(), path)
		},
		binaryPath:  os.Executable,
		repairACL:   defaultKeychainACLRepair,
		platformACL: keychain.PerBinaryACLSupported,
	}, nil
}

func defaultKeychainACLRepair(ctx context.Context, kc keychain.Keychain, service, account string) error {
	repairer, ok := kc.(keychainACLRepairer)
	if !ok {
		return fmt.Errorf("%w: platform keychain does not support ACL repair", errPlatformACLUnsupported)
	}
	return repairer.RepairACL(ctx, service, account)
}

// loadKeychainConfigPath returns the server config when one is reachable via
// the --config flag, or a nil config (no error) when the flag is absent or
// the file cannot be loaded — callers fall back to the default keychain in
// those cases.
func loadKeychainConfigPath(ctx context.Context, cmd *cobra.Command) (*config.Server, error) {
	if cmd == nil {
		return nil, nil //nolint:nilnil // absent config is the expected "use default keychain" signal
	}
	cfgPath := readGlobalFlags(cmd).configPath
	if cfgPath == "" {
		return nil, nil //nolint:nilnil // absent config is the expected "use default keychain" signal
	}
	expanded, err := expandTilde(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("hush/cli: keychain: expand config path: %w", err)
	}
	cfg, err := config.LoadServer(ctx, expanded)
	if err != nil {
		return nil, nil //nolint:nilnil,nilerr // config-load failure means "fall back to default keychain"
	}
	return cfg, nil
}

func newKeychainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keychain",
		Short: "Inspect or repair hush's bot-token Keychain item",
	}
	cmd.AddCommand(newKeychainDoctorCmd())
	cmd.AddCommand(newKeychainRepairCmd())
	return cmd
}

func newKeychainDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Report whether the Discord bot-token Keychain item is readable",
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := productionKeychainCmdDeps()
			if err != nil {
				return err
			}
			out := outputFromCmd(cmd)
			return runKeychainDoctor(cmd.Context(), cmd, out.stderr, deps)
		},
	}
	return cmd
}

func newKeychainRepairCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repair",
		Short: "Repair the Discord bot-token Keychain ACL for the current hush binary",
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps, err := productionKeychainCmdDeps()
			if err != nil {
				return err
			}
			out := outputFromCmd(cmd)
			return runKeychainRepair(cmd.Context(), cmd, out.stderr, deps)
		},
	}
	return cmd
}

// keychainCmdContext bundles the resolved per-invocation state shared
// by `keychain doctor` and `keychain repair`: the binary path used for
// ACL identity, the keychain handle (possibly a dedicated keychain
// loaded from config), and the service/account names to probe.
type keychainCmdContext struct {
	binaryPath string
	keychain   keychain.Keychain
	service    string
	account    string
}

func prepareKeychainCmd(ctx context.Context, cmd *cobra.Command, stderr *Stream, deps *keychainCmdDeps, op string) (*keychainCmdContext, error) {
	if !deps.platformACL() {
		_ = stderr.WriteText(initMsgPlatformUnsupported, runtime.GOOS)
		return nil, fmt.Errorf("%w: %s", errPlatformACLUnsupported, runtime.GOOS)
	}
	binaryPath, err := deps.binaryPath()
	if err != nil {
		return nil, fmt.Errorf("hush/cli: keychain %s: resolve binary path: %w", op, err)
	}
	items := defaultServerKeychainItems()
	kc := deps.keychain
	cfg, pathErr := loadKeychainConfigPath(ctx, cmd)
	if pathErr != nil {
		return nil, pathErr
	}
	if cfg != nil && cfg.Discord.BotKeychainPath != "" && deps.keychainFactory != nil {
		if pathKC, factoryErr := deps.keychainFactory(cfg.Discord.BotKeychainPath); factoryErr == nil {
			kc = pathKC
		}
	}
	return &keychainCmdContext{
		binaryPath: binaryPath,
		keychain:   kc,
		service:    items.discordService,
		account:    kcAccountServer,
	}, nil
}

func classifyKeychain(ctx context.Context, kc keychain.Keychain, service, account string) (setup.Artifact, bool) {
	cls := setup.Classifier{Keychain: kc}
	report := cls.ClassifyState(ctx, setup.StateInputs{KeychainItem: setup.KeychainTarget{Service: service, Account: account}})
	if len(report.Artifacts) == 0 {
		return setup.Artifact{}, false
	}
	return report.Artifacts[0], true
}

func runKeychainDoctor(ctx context.Context, cmd *cobra.Command, stderr *Stream, deps *keychainCmdDeps) error {
	kcCtx, err := prepareKeychainCmd(ctx, cmd, stderr, deps, "doctor")
	if err != nil {
		return err
	}
	art, ok := classifyKeychain(ctx, kcCtx.keychain, kcCtx.service, kcCtx.account)
	if !ok {
		return errKeychainDoctorEmpty
	}
	return reportDoctorClassification(stderr, kcCtx, art)
}

func reportDoctorClassification(stderr *Stream, kcCtx *keychainCmdContext, art setup.Artifact) error {
	switch {
	case art.Class == setup.ClassificationSafeToReuse:
		_ = stderr.WriteText("hush: keychain doctor: OK — service=%s account=%s readable by %s", kcCtx.service, kcCtx.account, kcCtx.binaryPath)
		return nil
	case art.Class == setup.ClassificationAbsent:
		_ = stderr.WriteText("hush: keychain doctor: missing — service=%s account=%s is absent; run `hush init server` to store the bot token", kcCtx.service, kcCtx.account)
		return fmt.Errorf("%w: service=%s account=%s", errKeychainDoctorMissing, kcCtx.service, kcCtx.account)
	case art.Class == setup.ClassificationRepairable && errors.Is(art.Err, setup.ErrTokenDenied):
		_ = stderr.WriteText("hush: keychain doctor: denied — service=%s account=%s is present, but %s cannot read it; run `hush keychain repair`", kcCtx.service, kcCtx.account, kcCtx.binaryPath)
		return fmt.Errorf("%w: service=%s account=%s", errKeychainDoctorDenied, kcCtx.service, kcCtx.account)
	default:
		_ = stderr.WriteText("hush: keychain doctor: unexpected state for service=%s account=%s (%s)", kcCtx.service, kcCtx.account, art.Detail)
		return fmt.Errorf("hush/cli: keychain doctor: %w", art.Err)
	}
}

// repairDecision captures the outcome of the pre-repair classification.
type repairDecision int

const (
	repairDecisionProceed repairDecision = iota
	repairDecisionAlreadyOK
	repairDecisionMissing
	repairDecisionUnexpected
)

func decideRepair(art setup.Artifact) repairDecision {
	switch {
	case art.Class == setup.ClassificationAbsent:
		return repairDecisionMissing
	case art.Class == setup.ClassificationSafeToReuse:
		return repairDecisionAlreadyOK
	case art.Class == setup.ClassificationRepairable && errors.Is(art.Err, setup.ErrTokenDenied):
		return repairDecisionProceed
	default:
		return repairDecisionUnexpected
	}
}

func runKeychainRepair(ctx context.Context, cmd *cobra.Command, stderr *Stream, deps *keychainCmdDeps) error {
	kcCtx, err := prepareKeychainCmd(ctx, cmd, stderr, deps, "repair")
	if err != nil {
		return err
	}
	art, ok := classifyKeychain(ctx, kcCtx.keychain, kcCtx.service, kcCtx.account)
	if !ok {
		return errKeychainRepairEmpty
	}
	switch decideRepair(art) {
	case repairDecisionMissing:
		_ = stderr.WriteText("hush: keychain repair: missing — service=%s account=%s is absent; run `hush init server` first", kcCtx.service, kcCtx.account)
		return fmt.Errorf("%w: service=%s account=%s", errKeychainDoctorMissing, kcCtx.service, kcCtx.account)
	case repairDecisionAlreadyOK:
		_ = stderr.WriteText("hush: keychain repair: already OK — service=%s account=%s readable by %s", kcCtx.service, kcCtx.account, kcCtx.binaryPath)
		return nil
	case repairDecisionUnexpected:
		_ = stderr.WriteText("hush: keychain repair: unexpected state for service=%s account=%s (%s)", kcCtx.service, kcCtx.account, art.Detail)
		return fmt.Errorf("hush/cli: keychain repair: %w", art.Err)
	case repairDecisionProceed:
		// fall through to ACL repair
	}

	if err := repairKeychainACL(ctx, stderr, deps, kcCtx); err != nil {
		return err
	}
	return verifyKeychainRepair(ctx, stderr, kcCtx)
}

func repairKeychainACL(ctx context.Context, stderr *Stream, deps *keychainCmdDeps, kcCtx *keychainCmdContext) error {
	err := deps.repairACL(ctx, deps.keychain, kcCtx.service, kcCtx.account)
	if err == nil {
		return nil
	}
	if errors.Is(err, keychain.ErrKeychainItemNotFound) {
		_ = stderr.WriteText("hush: keychain repair: missing — service=%s account=%s is absent; run `hush init server` first", kcCtx.service, kcCtx.account)
		return fmt.Errorf("%w: service=%s account=%s", errKeychainDoctorMissing, kcCtx.service, kcCtx.account)
	}
	_ = stderr.WriteText("hush: keychain repair: ACL repair failed for service=%s account=%s (%v)", kcCtx.service, kcCtx.account, err)
	return fmt.Errorf("%w: %w", errKeychainRepairFailed, err)
}

func verifyKeychainRepair(ctx context.Context, stderr *Stream, kcCtx *keychainCmdContext) error {
	art, ok := classifyKeychain(ctx, kcCtx.keychain, kcCtx.service, kcCtx.account)
	if !ok {
		return errKeychainRecheckEmpty
	}
	switch {
	case art.Class == setup.ClassificationSafeToReuse:
		_ = stderr.WriteText("hush: keychain repair: repaired — service=%s account=%s is now readable by %s", kcCtx.service, kcCtx.account, kcCtx.binaryPath)
		return nil
	case art.Class == setup.ClassificationAbsent:
		_ = stderr.WriteText("hush: keychain repair: missing after repair — service=%s account=%s", kcCtx.service, kcCtx.account)
		return fmt.Errorf("%w: service=%s account=%s", errKeychainDoctorMissing, kcCtx.service, kcCtx.account)
	case art.Class == setup.ClassificationRepairable && errors.Is(art.Err, setup.ErrTokenDenied):
		_ = stderr.WriteText("hush: keychain repair: still denied — service=%s account=%s remains unreadable by %s", kcCtx.service, kcCtx.account, kcCtx.binaryPath)
		return fmt.Errorf("%w: service=%s account=%s", errKeychainDoctorDenied, kcCtx.service, kcCtx.account)
	default:
		_ = stderr.WriteText("hush: keychain repair: unexpected re-check state for service=%s account=%s (%s)", kcCtx.service, kcCtx.account, art.Detail)
		return fmt.Errorf("hush/cli: keychain repair: %w", art.Err)
	}
}
