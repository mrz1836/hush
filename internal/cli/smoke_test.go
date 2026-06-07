package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/config"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/keys"
	"github.com/mrz1836/hush/internal/vault"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

func TestSmokeCommand_RegisteredOnRoot(t *testing.T) {
	t.Parallel()
	root := newRootCmd(&outputContext{stdout: newStream(io.Discard, false, true), stderr: newStream(io.Discard, false, true)})
	for _, cmd := range root.Commands() {
		if cmd.Name() == "smoke" {
			return
		}
	}
	t.Fatal("smoke command not registered")
}

func TestRunSmoke_OrchestratesFakeSecretPath(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "hush-smoke")
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/h/testprefix/hz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(health.Close)

	serveStarted := make(chan struct{}, 1)
	requestCalled := false
	initPreflightCalled := false
	deps := smokeTestDeps(t, stateDir, kc)
	baseInitDepsFactory := deps.initDepsFactory
	deps.initDepsFactory = func() (*initDeps, error) {
		id, err := baseInitDepsFactory()
		if err != nil {
			return nil, err
		}
		id.runPreflight = func(context.Context) setupReport {
			initPreflightCalled = true
			require.False(t, id.serverAllowClockSkew, "smoke init must not use blanket clock-skew override")
			require.True(t, id.serverProbeFailureWarn, "smoke init should warn only for probe-unavailable clock failures")
			return setupReport{}
		}
		return id, nil
	}
	deps.serveRunner = func(ctx context.Context, _, _ *Stream, sd serveDeps) error {
		require.False(t, sd.allowClockSkew, "smoke must not use blanket clock-skew override")
		require.True(t, sd.allowClockProbeUnavailable, "smoke should downgrade only probe-unavailable clock failures")
		serveStarted <- struct{}{}
		<-ctx.Done()
		return nil
	}
	deps.configLoader = func(ctx context.Context, path string) (*config.Server, error) {
		cfg, err := config.LoadServer(ctx, path)
		if err != nil {
			return nil, err
		}
		cfg.Server.ListenAddr = mustAddrPortFromURL(t, health.URL)
		cfg.Server.PathPrefix = "testprefix"
		return cfg, nil
	}
	deps.requestRunner = func(_ context.Context, stdout, _ *Stream, _ requestDeps, flags requestFlags) error {
		requestCalled = true
		require.Equal(t, []string{smokeSecretName}, flags.scope)
		require.Equal(t, formatModeEval, flags.formatMode)
		require.True(t, strings.HasSuffix(flags.clientKeyFile, "client-machine-1.key"))
		return stdout.WriteText("export %s='%s'", smokeSecretName, smokeSecretValue)
	}

	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	err := runSmoke(t.Context(), newStream(stdout, false, true), newStream(stderr, false, true), dummyTTY(t), deps, smokeOptions{
		stateDir:          stateDir,
		listenAddr:        testListenAddrInput,
		ownerID:           testOwnerIDInput,
		applicationID:     testApplicationIDIn,
		approvalChannelID: "1505706794406772897",
		machineIndex:      1,
		reset:             false,
	})
	require.NoError(t, err)
	require.True(t, initPreflightCalled)
	require.True(t, requestCalled)
	require.Contains(t, stdout.String(), smokeMsgSuccess[:20])
	require.FileExists(t, filepath.Join(stateDir, "config.toml"))
	require.FileExists(t, filepath.Join(stateDir, "secrets.vault"))
	require.FileExists(t, filepath.Join(stateDir, "client-machine-1.key"))
	select {
	case <-serveStarted:
	default:
		t.Fatal("serve runner was not started")
	}

	secretDeps := productionSecretDeps()
	secretDeps.configPath = filepath.Join(stateDir, "config.toml")
	secretDeps.deriveMasterSeed = fastDeriveMasterSeed
	salt, err := readVaultSalt(filepath.Join(stateDir, "secrets.vault"))
	require.NoError(t, err)
	master, err := fastDeriveMasterSeed(t.Context(), []byte(testGoodPassphrase), salt)
	require.NoError(t, err)
	defer zeroBytes(master)
	vaultKeyRaw, err := keys.DeriveVaultEncKey(master)
	require.NoError(t, err)
	vaultKey, err := securebytes.New(vaultKeyRaw)
	require.NoError(t, err)
	defer vaultKey.Destroy()
	secrets, err := vault.LoadSecrets(t.Context(), filepath.Join(stateDir, "secrets.vault"), vaultKey)
	require.NoError(t, err)
	require.Len(t, secrets, 1)
	require.Equal(t, smokeSecretName, secrets[0].Name)
}

func TestSmokeInitServer_StrictClockDisablesProbeFailureWarn(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "hush-smoke")
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	deps := smokeTestDeps(t, stateDir, kc)
	baseInitDepsFactory := deps.initDepsFactory
	preflightCalled := false
	deps.initDepsFactory = func() (*initDeps, error) {
		id, err := baseInitDepsFactory()
		if err != nil {
			return nil, err
		}
		id.runPreflight = func(context.Context) setupReport {
			preflightCalled = true
			require.False(t, id.serverAllowClockSkew, "strict smoke init must not use blanket clock-skew override")
			require.False(t, id.serverProbeFailureWarn, "--strict-clock should disable the smoke timeout downgrade")
			return setupReport{}
		}
		return id, nil
	}
	passphrase, err := securebytes.New([]byte(testGoodPassphrase))
	require.NoError(t, err)
	t.Cleanup(func() { _ = passphrase.Destroy() })

	err = smokeInitServer(t.Context(), newStream(io.Discard, false, true), dummyTTY(t), deps, smokeOptions{
		stateDir:          stateDir,
		strictClock:       true,
		listenAddr:        testListenAddrInput,
		ownerID:           testOwnerIDInput,
		applicationID:     testApplicationIDIn,
		approvalChannelID: "1505706794406772897",
	}, stateDir, passphrase)
	require.NoError(t, err)
	require.True(t, preflightCalled)
}

func TestArchiveSmokeStateDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.Mkdir(dir, 0o700))
	archived, err := archiveSmokeStateDir(dir, time.Date(2026, 5, 18, 13, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, dir+".bak-20260518-130000", archived)
	require.NoDirExists(t, dir)
	require.DirExists(t, archived)
}

func smokeTestDeps(t *testing.T, stateDir string, kc keychain.Keychain) smokeDeps {
	t.Helper()
	deps := productionSmokeDeps()
	deps.initDepsFactory = func() (*initDeps, error) {
		return &initDeps{
			keychain:         kc,
			binaryPath:       func() (string, error) { return testInitBinaryPath, nil },
			randReader:       newDeterministicReader(),
			stateDirRoot:     stateDir,
			nowFn:            time.Now,
			platformACL:      func() bool { return true },
			isTTY:            func(*os.File) bool { return true },
			deriveMasterSeed: fastDeriveMasterSeed,
			runPreflight:     func(context.Context) setupReport { return setupReport{} },
		}, nil
	}
	deps.secretDepsFactory = func() *secretDeps {
		sd := productionSecretDeps()
		sd.deriveMasterSeed = fastDeriveMasterSeed
		sd.stateDirRoot = stateDir
		return sd
	}
	deps.keychainFactory = func() (keychain.Keychain, error) { return kc, nil }
	deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase, testBotTokenInput})
	deps.promptLine = scriptedLineReader(t, []string{testListenAddrInput, testOwnerIDInput, testApplicationIDIn, "1505706794406772897"})
	deps.isTTY = func(*os.File) bool { return true }
	deps.nowFn = time.Now
	return deps
}

func mustAddrPortFromURL(t *testing.T, raw string) netip.AddrPort {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	ap, err := netip.ParseAddrPort(u.Host)
	require.NoError(t, err)
	return ap
}

func TestSmokeClean_ArchivesDefaultSmokeDirOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.Mkdir(filepath.Join(home, ".hush-smoke"), 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(home, ".hush-release-validation"), 0o700))
	deps := productionSmokeDeps()
	deps.nowFn = func() time.Time { return time.Date(2026, 5, 18, 14, 0, 0, 0, time.UTC) }
	stderr := &strings.Builder{}
	err := runSmokeClean(newStream(io.Discard, false, true), newStream(stderr, false, true), deps, smokeCleanOptions{})
	require.NoError(t, err)
	require.NoDirExists(t, filepath.Join(home, ".hush-smoke"))
	require.DirExists(t, filepath.Join(home, ".hush-smoke.bak-20260518-140000"))
	require.DirExists(t, filepath.Join(home, ".hush-release-validation"))
	require.Contains(t, stderr.String(), "archived")
}

func TestSmokeClean_ArchivesExplicitGenericTestDir(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), ".hush-release-validation")
	require.NoError(t, os.Mkdir(dir, 0o700))
	deps := productionSmokeDeps()
	deps.nowFn = func() time.Time { return time.Date(2026, 5, 18, 14, 0, 0, 0, time.UTC) }
	err := runSmokeClean(newStream(io.Discard, false, true), newStream(io.Discard, false, true), deps, smokeCleanOptions{
		stateDirs: []string{dir},
	})
	require.NoError(t, err)
	require.NoDirExists(t, dir)
	require.DirExists(t, dir+".bak-20260518-140000")
}

func TestSmokeClean_DestroyRequiresConfirmation(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), ".hush-smoke")
	require.NoError(t, os.Mkdir(dir, 0o700))
	deps := productionSmokeDeps()
	err := runSmokeClean(newStream(io.Discard, false, true), newStream(io.Discard, false, true), deps, smokeCleanOptions{
		stateDirs: []string{dir},
		destroy:   true,
	})
	require.Error(t, err)
	require.DirExists(t, dir)

	err = runSmokeClean(newStream(io.Discard, false, true), newStream(io.Discard, false, true), deps, smokeCleanOptions{
		stateDirs: []string{dir},
		destroy:   true,
		confirm:   "destroy smoke",
	})
	require.NoError(t, err)
	require.NoDirExists(t, dir)
}

func TestSmokeClean_RefusesRealHushState(t *testing.T) {
	t.Parallel()
	err := runSmokeClean(newStream(io.Discard, false, true), newStream(io.Discard, false, true), productionSmokeDeps(), smokeCleanOptions{
		stateDirs: []string{"~/.hush"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "refuses non-smoke")
}
