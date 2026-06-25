package cli

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
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
	// Not parallel: runSmoke reads and writes the process-global
	// HUSH_DISCORD_BOT_TOKEN env var (smoke.go), so concurrent runSmoke
	// tests race on it. Running in the sequential phase keeps them isolated.
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
	smokeToken, err := kc.Retrieve(context.Background(), smokeBotTokenKeychainItem, smokeBotTokenKeychainAccount)
	require.NoError(t, err)
	defer smokeToken.Destroy()
	require.NoError(t, smokeToken.Use(func(b []byte) {
		require.Equal(t, testBotTokenInput, string(b))
	}))
	require.Equal(t, testInitBinaryPath, kc.RecordedACL(smokeBotTokenKeychainItem, smokeBotTokenKeychainAccount))
	_, err = kc.Retrieve(context.Background(), config.DefaultBotTokenKeychainItem, kcAccountServer)
	require.True(t, errors.Is(err, keychain.ErrKeychainItemNotFound), "smoke must not touch production bot-token item")
	cfgLoaded, err := config.LoadServer(context.Background(), filepath.Join(stateDir, "config.toml"))
	require.NoError(t, err)
	require.Equal(t, smokeBotTokenKeychainItem, cfgLoaded.Discord.BotTokenKeychainItem)
	require.Equal(t, smokeBotTokenKeychainAccount, cfgLoaded.Discord.BotKeychainAccount)
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

func TestRunSmoke_ResetDeletesSmokeKeychainAcrossConsecutiveRuns(t *testing.T) {
	// Not parallel: runSmoke reads HUSH_DISCORD_BOT_TOKEN to decide whether
	// to prompt for the bot token and also writes it (smoke.go). Under
	// t.Parallel() a concurrent runSmoke test could leave the env var set,
	// causing this test to skip its bot-token prompt and desync the scripted
	// reader into a spurious passphrase mismatch.
	stateDir := filepath.Join(t.TempDir(), "hush-smoke")
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	mustStoreKeychainValue(t, kc, smokeBotTokenKeychainItem, smokeBotTokenKeychainAccount, "stale-smoke-token")
	mustStoreKeychainValue(t, kc, config.DefaultBotTokenKeychainItem, kcAccountServer, "production-token")

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/h/testprefix/hz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(health.Close)

	deps := smokeTestDeps(t, stateDir, kc)
	deps.promptSecret = scriptedSecretReader(t, []string{
		testGoodPassphrase, testGoodPassphrase, testBotTokenInput,
		testGoodPassphrase, testGoodPassphrase, testBotTokenInput,
	})
	deps.configLoader = func(ctx context.Context, path string) (*config.Server, error) {
		cfg, err := config.LoadServer(ctx, path)
		if err != nil {
			return nil, err
		}
		cfg.Server.ListenAddr = mustAddrPortFromURL(t, health.URL)
		cfg.Server.PathPrefix = "testprefix"
		return cfg, nil
	}
	deps.serveRunner = func(ctx context.Context, _, _ *Stream, _ serveDeps) error {
		<-ctx.Done()
		return nil
	}
	requestCalls := 0
	deps.requestRunner = func(_ context.Context, stdout, _ *Stream, _ requestDeps, _ requestFlags) error {
		requestCalls++
		return stdout.WriteText("export %s='%s'", smokeSecretName, smokeSecretValue)
	}

	opts := smokeOptions{
		stateDir:          stateDir,
		listenAddr:        testListenAddrInput,
		ownerID:           testOwnerIDInput,
		applicationID:     testApplicationIDIn,
		approvalChannelID: "1505706794406772897",
		machineIndex:      1,
		reset:             true,
	}
	err := runSmoke(t.Context(), newStream(io.Discard, false, true), newStream(io.Discard, false, true), dummyTTY(t), deps, opts)
	require.NoError(t, err)
	err = runSmoke(t.Context(), newStream(io.Discard, false, true), newStream(io.Discard, false, true), dummyTTY(t), deps, opts)
	require.NoError(t, err)
	require.Equal(t, 2, requestCalls)

	smokeToken, err := kc.Retrieve(context.Background(), smokeBotTokenKeychainItem, smokeBotTokenKeychainAccount)
	require.NoError(t, err)
	defer smokeToken.Destroy()
	require.NoError(t, smokeToken.Use(func(b []byte) {
		require.Equal(t, testBotTokenInput, string(b))
	}))
	prodToken, err := kc.Retrieve(context.Background(), config.DefaultBotTokenKeychainItem, kcAccountServer)
	require.NoError(t, err)
	defer prodToken.Destroy()
	require.NoError(t, prodToken.Use(func(b []byte) {
		require.Equal(t, "production-token", string(b))
	}))
}

func TestRunSmoke_RefusesProductionListenAddr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prodDir := filepath.Join(home, ".hush")
	prodAddr := "100.96.10.4:47744"
	require.NoError(t, os.MkdirAll(prodDir, 0o700))
	writeSmokeProofConfig(t, prodDir, prodAddr, "prodxx")

	stateDir := filepath.Join(t.TempDir(), "hush-smoke")
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	deps := smokeTestDeps(t, stateDir, kc)
	deps.initDepsFactory = func() (*initDeps, error) {
		return nil, errors.New("init must not run")
	}

	err := runSmoke(t.Context(), newStream(io.Discard, false, true), newStream(io.Discard, false, true), dummyTTY(t), deps, smokeOptions{
		stateDir:          stateDir,
		listenAddr:        ":47744",
		ownerID:           testOwnerIDInput,
		applicationID:     testApplicationIDIn,
		approvalChannelID: "1505706794406772897",
		machineIndex:      1,
	})
	require.ErrorIs(t, err, errSmokeProductionAddr)
	require.Contains(t, err.Error(), "omit --listen-addr to auto-pick")
}

func TestRefuseProductionSmokeAddr_AllowsAbsentProductionConfig(t *testing.T) {
	t.Parallel()
	deps := productionSmokeDeps()
	err := refuseProductionSmokeAddr(t.Context(), deps, smokeOptions{
		listenAddr: ":47744",
		configPath: filepath.Join(t.TempDir(), "missing-config.toml"),
	})
	require.NoError(t, err)
}

func TestChooseSmokeListenAddr_AutoPicksFreePortWhenRequestedAddrBusy(t *testing.T) {
	t.Parallel()
	var lc net.ListenConfig
	busy, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = busy.Close() })
	requested := busy.Addr().String()

	chosen, autoPicked, err := chooseSmokeListenAddr(t.Context(), requested)
	require.NoError(t, err)
	require.True(t, autoPicked)
	require.NotEqual(t, requested, chosen)
	ap, err := netip.ParseAddrPort(chosen)
	require.NoError(t, err)
	require.Equal(t, netip.MustParseAddr("127.0.0.1"), ap.Addr())
	require.NotZero(t, ap.Port())
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
	botToken, err := securebytes.New([]byte(testBotTokenInput))
	require.NoError(t, err)
	t.Cleanup(func() { _ = botToken.Destroy() })

	err = smokeInitServer(t.Context(), newStream(io.Discard, false, true), dummyTTY(t), deps, smokeOptions{
		stateDir:          stateDir,
		strictClock:       true,
		listenAddr:        testListenAddrInput,
		ownerID:           testOwnerIDInput,
		applicationID:     testApplicationIDIn,
		approvalChannelID: "1505706794406772897",
	}, stateDir, passphrase, botToken)
	require.NoError(t, err)
	require.True(t, preflightCalled)
}

func TestRunSmokeAgainstRunning_PostsFakeClaimOnly(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	configPath := writeSmokeProofConfig(t, stateDir, testListenAddrInput, "proofx")
	clientKeyPath := filepath.Join(stateDir, "client-machine-1.key")
	writeSmokeClientKeyFile(t, clientKeyPath, makeClientKey(t))

	var claimReq claimWireRequest
	var secretFetches int
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/h/proofx/claim":
			require.NoError(t, json.NewDecoder(r.Body).Decode(&claimReq))
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"denied","request_id":"proof-request"}`))
		case strings.HasPrefix(r.URL.Path, "/h/proofx/s/"):
			secretFetches++
			http.Error(w, "unexpected secret fetch", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.Close)

	deps := smokeTestDeps(t, stateDir, keychain.NewFake())
	deps.keychainFactory = func() (keychain.Keychain, error) { return failOnUseKeychain{}, nil }
	initCalled := false
	serveCalled := false
	requestCalled := false
	deps.initDepsFactory = func() (*initDeps, error) {
		initCalled = true
		return nil, errors.New("init must not run")
	}
	deps.serveRunner = func(context.Context, *Stream, *Stream, serveDeps) error {
		serveCalled = true
		return errors.New("serve must not run")
	}
	deps.requestRunner = func(context.Context, *Stream, *Stream, requestDeps, requestFlags) error {
		requestCalled = true
		return errors.New("request runner must not run")
	}

	stdout, stderr := &strings.Builder{}, &strings.Builder{}
	err := runSmoke(t.Context(), newStream(stdout, false, true), newStream(stderr, false, true), nil, deps, smokeOptions{
		againstRunning: true,
		configPath:     configPath,
		listenAddr:     mustAddrPortFromURL(t, stub.URL).String(),
		clientKeyFile:  clientKeyPath,
		machineIndex:   1,
	})
	require.NoError(t, err)
	require.False(t, initCalled)
	require.False(t, serveCalled)
	require.False(t, requestCalled)
	require.Zero(t, secretFetches)
	require.Regexp(t, `^HUSH_SMOKE_PROOF_[0-9A-F]{16}$`, claimReq.Scope[0])
	require.Equal(t, []string{claimReq.Scope[0]}, claimReq.Scope)
	require.Equal(t, smokeAgainstRunningReason, claimReq.Reason)
	require.Equal(t, "30s", claimReq.TTL)
	require.Contains(t, stdout.String(), "against-running proof passed")
	require.Contains(t, stdout.String(), "denied")
}

func TestRunSmokeAgainstRunning_RequiresClientKeyFile(t *testing.T) {
	t.Parallel()
	deps := productionSmokeDeps()
	err := runSmoke(t.Context(), newStream(io.Discard, false, true), newStream(io.Discard, false, true), nil, deps, smokeOptions{
		againstRunning: true,
	})
	require.ErrorIs(t, err, errMissingFlag)
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
	deps.keychainFactory = func() (keychain.Keychain, error) { return nonDestroyingKeychain{Keychain: kc}, nil }
	deps.promptSecret = scriptedSecretReader(t, []string{testGoodPassphrase, testGoodPassphrase, testBotTokenInput})
	deps.promptLine = scriptedLineReader(t, []string{testListenAddrInput, testOwnerIDInput, testApplicationIDIn, "1505706794406772897"})
	deps.isTTY = func(*os.File) bool { return true }
	deps.nowFn = time.Now
	return deps
}

type nonDestroyingKeychain struct {
	keychain.Keychain
}

type failOnUseKeychain struct{}

func (failOnUseKeychain) Store(context.Context, string, string, *securebytes.SecureBytes, string) error {
	return errors.New("keychain store must not run")
}

func (failOnUseKeychain) Retrieve(context.Context, string, string) (*securebytes.SecureBytes, error) {
	return nil, errors.New("keychain retrieve must not run")
}

func (failOnUseKeychain) Delete(context.Context, string, string) error {
	return errors.New("keychain delete must not run")
}

func mustStoreKeychainValue(t *testing.T, kc *keychain.FakeKeychain, service, account, value string) {
	t.Helper()
	sb, err := securebytes.New([]byte(value))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Destroy() })
	require.NoError(t, kc.Store(context.Background(), service, account, sb, "/test/acl"))
}

func smokeCleanTestDeps(kc keychain.Keychain) smokeDeps {
	deps := productionSmokeDeps()
	deps.keychainFactory = func() (keychain.Keychain, error) {
		return nonDestroyingKeychain{Keychain: kc}, nil
	}
	return deps
}

func writeSmokeClientKeyFile(t *testing.T, path string, priv *ecdsa.PrivateKey) {
	t.Helper()
	scalar := make([]byte, 32)
	//nolint:staticcheck // secp256k1 unsupported by crypto/ecdh; .D access intentional for key-file fixture
	priv.D.FillBytes(scalar)
	t.Cleanup(func() {
		for i := range scalar {
			scalar[i] = 0
		}
	})
	require.NoError(t, os.WriteFile(path, []byte(hex.EncodeToString(scalar)), 0o600))
}

func writeSmokeProofConfig(t *testing.T, dir, listenAddr, prefix string) string {
	t.Helper()
	require.NoError(t, os.Chmod(dir, 0o700))
	configPath := filepath.Join(dir, "config.toml")
	clientReg := filepath.Join(dir, "clients.json")
	require.NoError(t, os.WriteFile(clientReg, []byte("[]"), 0o600))
	body := "" +
		"[server]\n" +
		"listen_addr = \"" + listenAddr + "\"\n" +
		"path_prefix = \"" + prefix + "\"\n" +
		"state_dir = \"" + dir + "\"\n" +
		"audit_log = \"" + filepath.Join(dir, "audit.jsonl") + "\"\n" +
		"discord_owner_id = \"100000000000000000\"\n" +
		"client_registry = \"" + clientReg + "\"\n" +
		"\n[discord]\n" +
		"bot_token_keychain_item = \"hush-discord\"\n" +
		"application_id = \"100000000000000000\"\n" +
		"\n[crypto]\n" +
		"argon_time = 4\n" +
		"argon_memory_mb = 256\n" +
		"argon_threads = 4\n" +
		"jwt_default_ttl = \"15m\"\n" +
		"max_interactive_ttl = \"30m\"\n" +
		"max_supervisor_ttl = \"6h\"\n" +
		"default_max_uses = 5\n" +
		"nonce_ttl = \"5m\"\n" +
		"clock_skew = \"1m\"\n" +
		"claim_approval_timeout = \"30s\"\n" +
		"\n[network]\n" +
		"require_tailscale = true\n" +
		"allowed_cidrs = [\"100.64.0.0/10\", \"127.0.0.0/8\"]\n" +
		"\n[security]\n" +
		"require_file_mode_checks = true\n" +
		"require_keychain_acl = false\n" +
		"require_ntp_sync = true\n" +
		"max_clock_drift = \"1m\"\n"
	require.NoError(t, os.WriteFile(configPath, []byte(body), 0o600))
	return configPath
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
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	deps := smokeCleanTestDeps(kc)
	deps.nowFn = func() time.Time { return time.Date(2026, 5, 18, 14, 0, 0, 0, time.UTC) }
	stderr := &strings.Builder{}
	err := runSmokeClean(t.Context(), newStream(io.Discard, false, true), newStream(stderr, false, true), deps, smokeCleanOptions{})
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
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	deps := smokeCleanTestDeps(kc)
	deps.nowFn = func() time.Time { return time.Date(2026, 5, 18, 14, 0, 0, 0, time.UTC) }
	err := runSmokeClean(t.Context(), newStream(io.Discard, false, true), newStream(io.Discard, false, true), deps, smokeCleanOptions{
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
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	deps := smokeCleanTestDeps(kc)
	err := runSmokeClean(t.Context(), newStream(io.Discard, false, true), newStream(io.Discard, false, true), deps, smokeCleanOptions{
		stateDirs: []string{dir},
		destroy:   true,
	})
	require.Error(t, err)
	require.DirExists(t, dir)

	err = runSmokeClean(t.Context(), newStream(io.Discard, false, true), newStream(io.Discard, false, true), deps, smokeCleanOptions{
		stateDirs: []string{dir},
		destroy:   true,
		confirm:   "destroy smoke",
	})
	require.NoError(t, err)
	require.NoDirExists(t, dir)
}

func TestSmokeClean_RefusesRealHushState(t *testing.T) {
	t.Parallel()
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	err := runSmokeClean(t.Context(), newStream(io.Discard, false, true), newStream(io.Discard, false, true), smokeCleanTestDeps(kc), smokeCleanOptions{
		stateDirs: []string{"~/.hush"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "refuses non-smoke")
}

func TestSmokeClean_DeletesSmokeKeychainAndLeavesProductionItem(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), ".hush-smoke")
	require.NoError(t, os.Mkdir(dir, 0o700))
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	mustStoreKeychainValue(t, kc, smokeBotTokenKeychainItem, smokeBotTokenKeychainAccount, "smoke-token")
	mustStoreKeychainValue(t, kc, config.DefaultBotTokenKeychainItem, kcAccountServer, "production-token")
	deps := smokeCleanTestDeps(kc)

	err := runSmokeClean(t.Context(), newStream(io.Discard, false, true), newStream(io.Discard, false, true), deps, smokeCleanOptions{
		stateDirs: []string{dir},
	})
	require.NoError(t, err)
	_, err = kc.Retrieve(context.Background(), smokeBotTokenKeychainItem, smokeBotTokenKeychainAccount)
	require.ErrorIs(t, err, keychain.ErrKeychainItemNotFound)
	prodToken, err := kc.Retrieve(context.Background(), config.DefaultBotTokenKeychainItem, kcAccountServer)
	require.NoError(t, err)
	defer prodToken.Destroy()
	require.NoError(t, prodToken.Use(func(b []byte) {
		require.Equal(t, "production-token", string(b))
	}))
}

func TestSmokeClean_KeychainAbsentIsNoop(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), ".hush-smoke")
	require.NoError(t, os.Mkdir(dir, 0o700))
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)

	err := runSmokeClean(t.Context(), newStream(io.Discard, false, true), newStream(io.Discard, false, true), smokeCleanTestDeps(kc), smokeCleanOptions{
		stateDirs: []string{dir},
		destroy:   true,
		confirm:   "destroy smoke",
	})
	require.NoError(t, err)
	require.NoDirExists(t, dir)
}
