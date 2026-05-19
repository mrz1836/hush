package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/cli/setup"
	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/server"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// TestT273_Fixture1_KeychainDeniedSurfacesACLPanel is SC-10 / AC-12
// case 1: when the `hush-discord` Keychain item exists but reads fail
// with permission denied (mapped from Darwin exit 51 in
// internal/keychain), the guided flow classifies the artifact as
// [setup.ClassificationRepairable] with [setup.ErrTokenDenied] and
// renders the ACL-repair panel. This test pins the cross-package wire
// from [keychain.ErrKeychainPermissionDenied] through
// [setup.TokenErrorFromKeychain] → [setup.ErrTokenDenied] → panel.
func TestT273_Fixture1_KeychainDeniedSurfacesACLPanel(t *testing.T) {
	t.Parallel()

	// 1 denied read on the bot-token slot is enough to drive the
	// classifier into ClassificationRepairable + ErrTokenDenied.
	fx, acl := newACLFixture(t, 1)
	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{keychainACLChoiceQuit})

	// The TokenErrorFromKeychain mapper is the documented bridge from
	// the low-level keychain sentinel to the setup-level token
	// sentinel. Pin both directions here so a future refactor cannot
	// silently break the panel-trigger condition.
	require.ErrorIs(t,
		setup.TokenErrorFromKeychain(keychain.ErrKeychainPermissionDenied),
		setup.ErrTokenDenied,
		"keychain permission-denied must map to setup.ErrTokenDenied",
	)

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errUserAborted),
		"quitting the panel should surface errUserAborted (got %v)", err)

	transcript := fx.stderr.String()
	require.Contains(t, transcript, "macOS Keychain is denying hush access",
		"ACL panel must render on Keychain denial")
	require.Contains(t, transcript, "set-generic-password-partition-list",
		"panel must include the ACL-repair security CLI command")
	require.Contains(t, transcript, "Use HUSH_DISCORD_BOT_TOKEN env-var instead",
		"panel must offer the env-token fallback option")

	// No destructive side effects — quit means quit.
	require.Zero(t, acl.deleteCalls, "no Delete may fire when the operator quits")
	require.Zero(t, acl.storeCalls, "no Store may fire when the operator quits")
}

// TestT273_Fixture2_ExplicitStateDirTreatsDefaultTokenAsExternal is
// SC-10 / AC-12 case 2: when an explicit `--state-dir` is set AND a
// `hush-discord` Keychain item already exists at the default location,
// the guided flow intentionally does NOT probe the Keychain (the
// explicit-state-dir flow is the learning/smoke path; Keychain writes
// are skipped). The pre-existing item must be left untouched and init
// must complete. The classifier API itself, when explicitly probed,
// correctly surfaces the existing token as safe-to-reuse — so a future
// `hush doctor` that re-uses the classifier can detect the collision
// even though init does not.
func TestT273_Fixture2_ExplicitStateDirTreatsDefaultTokenAsExternal(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	explicitDir := filepath.Join(fx.tempDir, "explicit-learning-dir")
	fx.deps.stateDirRoot = explicitDir
	fx.deps.serverInputs.stateDir = explicitDir
	fx.deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase, testBotTokenInput})

	// Pre-populate the default-location bot-token item with arbitrary
	// bytes so a Delete or Store would corrupt it. securebytes.New
	// zeroes its input on success, so keep an immutable copy of the
	// expected bytes for the post-init equality check.
	const preexisting = "PREEXISTING-DEFAULT-LOCATION-TOKEN"
	prep, err := securebytes.New([]byte(preexisting))
	require.NoError(t, err)
	require.NoError(t, fx.keychain.Store(context.Background(), "hush-discord", kcAccountServer, prep, "/abs/external"))
	require.NoError(t, prep.Destroy())

	// Existing readable token is offered as a reusable artifact; choose reuse so
	// the explicit-state-dir flow does not corrupt an existing default token.
	fx.deps.promptRecovery = func(*os.File, io.Writer, string) (rune, error) {
		return recoveryChoiceReuse, nil
	}

	require.NoError(t, runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps))

	// The pre-existing item is untouched (bytes intact, ACL intact).
	got, err := fx.keychain.Retrieve(context.Background(), "hush-discord", kcAccountServer)
	require.NoError(t, err)
	t.Cleanup(func() { _ = got.Destroy() })
	require.NoError(t, got.Use(func(b []byte) {
		require.Equal(t, preexisting, string(b), "pre-existing default-location token must not be modified")
	}))
	require.Equal(t, "/abs/external", fx.keychain.RecordedACL("hush-discord", kcAccountServer),
		"pre-existing ACL must survive explicit-state-dir init")

	// Init completes and does not require a second serve-time token paste.
	transcript := fx.stderr.String()
	require.Contains(t, transcript, initMsgExplicitStateKeychain)
	require.Contains(t, transcript, initMsgServerComplete)

	// The classifier, when explicitly invoked against the same target,
	// correctly reports the pre-existing item as safe-to-reuse. This
	// pins the API surface a future `hush doctor` would use to detect
	// the collision even though the guided init path elides the probe.
	cls := &setup.Classifier{Keychain: fx.keychain}
	report := cls.ClassifyState(context.Background(), setup.StateInputs{
		KeychainItem: setup.KeychainTarget{Service: "hush-discord", Account: kcAccountServer},
	})
	require.Len(t, report.Artifacts, 1)
	require.Equal(t, setup.ArtifactKeychainToken, report.Artifacts[0].Kind)
	require.Equal(t, setup.ClassificationSafeToReuse, report.Artifacts[0].Class)
}

// TestT273_Fixture3_PartialStateConfigWithoutVault is SC-10 / AC-12
// case 3 (config-without-vault sub-case): the cross-artifact classifier
// rule flips a `safe-to-reuse` config to `repairable` with
// [setup.ErrStateStale] when its companion vault file is missing. The
// guided flow then prompts the operator per artifact.
func TestT273_Fixture3_PartialStateConfigWithoutVault(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)

	// Pre-populate a baseline config so the classifier sees it as
	// safe-to-reuse on its own; the cross-artifact rule will demote
	// it to repairable because the companion vault file is absent.
	configPath := filepath.Join(fx.tempDir, "config.toml")
	body := buildServerDecodedFromDefaults(serverInputs{
		listenAddr:    testListenAddrInput,
		pathPrefix:    "partialcfg0",
		ownerID:       testOwnerIDInput,
		applicationID: testApplicationIDIn,
		stateDir:      fx.tempDir,
	})
	require.NoError(t, writeConfigTOMLAtomic(configPath, body))

	// Picking `a`rchive on the repairable config drives the
	// archive helper; init then proceeds to write a fresh config +
	// vault pair.
	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{'a'})

	require.NoError(t, runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps))

	transcript := fx.stderr.String()
	require.Contains(t, transcript, "archived",
		"per-artifact recovery archive branch must announce the rename")
	require.Contains(t, transcript, initMsgServerComplete)

	// Original config moved to <path>.bak-<RFC3339>.
	entries, err := os.ReadDir(fx.tempDir)
	require.NoError(t, err)
	var backups int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "config.toml.bak-") {
			backups++
		}
	}
	require.Equal(t, 1, backups, "expected exactly one config.toml.bak-* sibling")

	// Fresh config + vault pair landed in their canonical locations.
	_, err = os.Stat(configPath)
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(fx.tempDir, "secrets.vault"))
	require.NoError(t, err)
}

// TestT273_Fixture3_PartialStateVaultWithoutConfig is the symmetric
// sub-case: a vault file present without a companion config. The
// classifier flips the vault to `repairable`; the guided flow prompts
// per artifact.
func TestT273_Fixture3_PartialStateVaultWithoutConfig(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)

	// Pre-populate a vault file (any non-empty bytes — the classifier
	// only stats it).
	vaultPath := filepath.Join(fx.tempDir, "secrets.vault")
	require.NoError(t, os.WriteFile(vaultPath, []byte("preexisting-vault-bytes"), 0o600))

	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{'a'})

	require.NoError(t, runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps))

	transcript := fx.stderr.String()
	require.Contains(t, transcript, "archived")
	require.Contains(t, transcript, initMsgServerComplete)

	entries, err := os.ReadDir(fx.tempDir)
	require.NoError(t, err)
	var backups int
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "secrets.vault.bak-") {
			backups++
		}
	}
	require.Equal(t, 1, backups, "expected exactly one secrets.vault.bak-* sibling")
}

// TestT273_Fixture4_EnvTokenFallbackWhenKeychainDenied is SC-10 /
// AC-12 case 4: when the Keychain bot-token read is denied AND the
// operator picks the env-token fallback at the ACL panel, init
// completes without writing a Keychain item, the documented
// "next-step" message announces the fallback (audit-grade stderr per
// [initMsgKeychainEnvTokenFallbackFmt]), and the serve-side
// [loadBotToken] then loads the token from HUSH_DISCORD_BOT_TOKEN
// without touching Keychain.
//
// t.Setenv mutates process-global state and cannot run in parallel
// with sibling tests that read the same env var; the fixture is
// therefore intentionally serial.
func TestT273_Fixture4_EnvTokenFallbackWhenKeychainDenied(t *testing.T) {
	fx, acl := newACLFixture(t, 1)
	fx.deps.promptRecovery = scriptedRecoveryReader(t, []rune{keychainACLChoiceEnvToken})

	require.NoError(t, runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps))

	transcript := fx.stderr.String()
	// One audit-grade stderr announcement per AC-6 / Task 3.4.
	require.Equal(t, 1, strings.Count(transcript, "env-token fallback selected"),
		"exactly one env-token fallback announcement must fire")
	require.Contains(t, transcript, "export HUSH_DISCORD_BOT_TOKEN")
	require.Contains(t, transcript, "use Keychain when possible",
		"fallback message must include the 'use Keychain when possible' guidance from Q4=b")

	// Init must NOT call Store on the bot-token slot.
	require.Equal(t, 1, acl.storeCalls,
		"env-token fallback must skip the bot-token Keychain write (only vault-passphrase Store fires)")
	require.Zero(t, acl.deleteCalls)

	// Pre-existing bot-token item is intact (not deleted, not
	// overwritten by init).
	got, err := acl.inner.Retrieve(context.Background(), "hush-discord", kcAccountServer)
	require.NoError(t, err)
	t.Cleanup(func() { _ = got.Destroy() })
	require.NoError(t, got.Use(func(b []byte) {
		require.Equal(t, "preexisting-bot-token", string(b),
			"env-token fallback must not modify the existing Keychain item")
	}))

	// Serve-side: with HUSH_DISCORD_BOT_TOKEN set, loadBotToken returns
	// the env value without touching Keychain. Use a deliberately
	// non-existent keychain item name to prove the env-var path wins
	// outright (no keychain subprocess can succeed against a
	// non-existent item, and no error fires).
	const envToken = "T273-fixture4-env-token"
	t.Setenv("HUSH_DISCORD_BOT_TOKEN", envToken)
	tok, err := loadBotToken(context.Background(), "hush-nonexistent-item-T273-f4", "")
	require.NoError(t, err, "loadBotToken must succeed via env-var when Keychain is unavailable")
	t.Cleanup(func() { _ = tok.Destroy() })
	require.NoError(t, tok.Use(func(b []byte) {
		require.Equal(t, envToken, string(b))
	}))
}

// TestT273_Fixture5_ClockSyncFailEmitsExactRemediation is SC-10 /
// AC-12 case 5 (default, no override): the guided init flow surfaces
// the platform-aware exact remediation command in its preflight
// failure render and exits non-zero with [errPreflightFailed].
func TestT273_Fixture5_ClockSyncFailEmitsExactRemediation(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.runPreflight = func(ctx context.Context) setupReport {
		// Use a real ClockSyncCheck so its remedy hint is the
		// production-grade string the production preflight registers.
		check := setup.NewClockSyncCheck(setup.ClockSyncCheckConfig{
			Probe:    func(context.Context) (bool, time.Duration, error) { return false, 0, nil },
			Required: true,
			MaxDrift: config.DefaultMaxClockDrift,
			Timeout:  server.DefaultClockSyncTimeout,
		})
		return setup.Report{Results: []setup.SetupCheckResult{check.Run(ctx)}}
	}

	err := runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps)
	require.True(t, errors.Is(err, errPreflightFailed),
		"clock-sync fail must short-circuit with errPreflightFailed; got %v", err)
	require.True(t, errors.Is(err, server.ErrClockUnsynchronised),
		"wrapped error must surface ErrClockUnsynchronised for downstream branching")

	transcript := fx.stderr.String()
	require.Contains(t, transcript, "preflight clock_sync failed")
	require.Contains(t, transcript, "remedy:")
	// Platform-aware exact remediation command is pinned per GOOS.
	require.Contains(t, transcript, setup.ClockSyncRemedy(runtime.GOOS),
		"exact remediation command must appear in stderr render")
}

// TestT273_Fixture5_AllowClockSkewDowngradesFailToWarn is SC-10 /
// AC-12 case 5 (with `--allow-clock-skew`): the same probe failure
// downgrades from fail to warn, init proceeds, and the override
// announcement appears in stderr. The serve-side audit event is
// covered separately by TestStartupChecks_AllowClockSkew*.
func TestT273_Fixture5_AllowClockSkewDowngradesFailToWarn(t *testing.T) {
	t.Parallel()
	fx := newInitFixture(t)
	fx.deps.serverAllowClockSkew = true
	fx.deps.runPreflight = func(ctx context.Context) setupReport {
		check := setup.NewClockSyncCheck(setup.ClockSyncCheckConfig{
			Probe:     func(context.Context) (bool, time.Duration, error) { return false, 0, nil },
			Required:  true,
			MaxDrift:  config.DefaultMaxClockDrift,
			Timeout:   server.DefaultClockSyncTimeout,
			AllowSkew: true,
		})
		return setup.Report{Results: []setup.SetupCheckResult{check.Run(ctx)}}
	}

	require.NoError(t, runInitServer(context.Background(), fx.stdoutS, fx.stderrS, fx.stdinFile, fx.deps))

	transcript := fx.stderr.String()
	require.Contains(t, transcript, "--allow-clock-skew override active",
		"override announcement must fire when --allow-clock-skew downgrades a clock-sync warn")
	require.NotContains(t, transcript, "preflight clock_sync failed",
		"the fail render must NOT appear under --allow-clock-skew")
	require.Contains(t, transcript, initMsgServerComplete,
		"init must complete normally under --allow-clock-skew")
}
