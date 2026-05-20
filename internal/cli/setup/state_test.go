package setup_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/cli/setup"
	"github.com/mrz1836/hush/internal/keychain"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// writeFile writes content with the given mode and returns the
// absolute path. Test helper for the classifier fixtures.
func writeFile(t *testing.T, dir, name string, content []byte, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, content, mode))
	// os.WriteFile honors umask on darwin; force the mode explicitly.
	require.NoError(t, os.Chmod(p, mode))
	return p
}

// makeFakeKeychainWithItem returns a populated FakeKeychain with
// one item under (service, account) plus a paired test cleanup.
func makeFakeKeychainWithItem(t *testing.T, service, account, value string) *keychain.FakeKeychain {
	t.Helper()
	kc := keychain.NewFake()
	t.Cleanup(kc.Destroy)
	sb, err := securebytes.New([]byte(value))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sb.Destroy() })
	require.NoError(t, kc.Store(context.Background(), service, account, sb, "/abs/hush"))
	return kc
}

// pinnedTime is the fixed timestamp the archive tests use so the
// produced .bak-<RFC3339> suffix is stable across machines.
func pinnedTime() time.Time {
	return time.Date(2026, time.May, 18, 12, 30, 45, 0, time.UTC)
}

func TestClassification_StringLockedTokens(t *testing.T) {
	t.Parallel()

	require.Equal(t, "absent", setup.ClassificationAbsent.String())
	require.Equal(t, "safe-to-reuse", setup.ClassificationSafeToReuse.String())
	require.Equal(t, "repairable", setup.ClassificationRepairable.String())
	require.Equal(t, "collision", setup.ClassificationCollision.String())
	require.Equal(t, "unknown", setup.ClassificationUnknown.String())
}

// TestClassifier_SafeToReuse covers the happy path: config + vault
// + state dir all present with strict modes; keychain item
// readable.
func TestClassifier_SafeToReuse(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := writeFile(t, dir, "config.toml", []byte("[server]\n"), 0o600)
	vault := writeFile(t, dir, "vault.bin", []byte("MAGIC..."), 0o600)
	stateDir := filepath.Join(dir, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	require.NoError(t, os.Chmod(stateDir, 0o700))

	kc := makeFakeKeychainWithItem(t, "hush-discord", "hush", "token-bytes")
	cls := &setup.Classifier{Keychain: kc}

	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		ConfigPath:   cfg,
		VaultPath:    vault,
		StateDir:     stateDir,
		KeychainItem: setup.KeychainTarget{Service: "hush-discord", Account: "hush"},
	})
	require.Len(t, rep.Artifacts, 4)
	for _, a := range rep.Artifacts {
		require.Equal(t, setup.ClassificationSafeToReuse, a.Class,
			"artifact %s should be safe-to-reuse: %s", a.Kind, a.Detail)
	}
}

// TestClassifier_AbsentArtifacts covers the fresh-host case:
// every input is empty or points at a non-existent path.
func TestClassifier_AbsentArtifacts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cls := &setup.Classifier{}

	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		ConfigPath: filepath.Join(dir, "missing-cfg.toml"),
		VaultPath:  filepath.Join(dir, "missing-vault.bin"),
		StateDir:   filepath.Join(dir, "missing-state"),
		// keychain target intentionally empty — should be skipped.
	})
	require.Len(t, rep.Artifacts, 3)
	for _, a := range rep.Artifacts {
		require.Equal(t, setup.ClassificationAbsent, a.Class,
			"artifact %s should be absent", a.Kind)
	}
	require.True(t, rep.AllAbsent())
}

// TestClassifier_RepairableLooseMode covers the "loose mode"
// repairable verdict: a 0644 config file is fixable via chmod.
func TestClassifier_RepairableLooseMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := writeFile(t, dir, "config.toml", []byte("[server]\n"), 0o644)
	vault := writeFile(t, dir, "vault.bin", []byte("MAGIC.."), 0o600)
	cls := &setup.Classifier{}

	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		ConfigPath: cfg,
		VaultPath:  vault,
	})
	require.Len(t, rep.Artifacts, 2)
	require.Equal(t, setup.ArtifactConfig, rep.Artifacts[0].Kind)
	require.Equal(t, setup.ClassificationRepairable, rep.Artifacts[0].Class)
	require.ErrorIs(t, rep.Artifacts[0].Err, setup.ErrStateStale)
	require.Contains(t, rep.Artifacts[0].Detail, "0644")

	require.Equal(t, setup.ClassificationSafeToReuse, rep.Artifacts[1].Class)
}

// TestClassifier_RepairablePartialState covers the cross-artifact
// rule: config present but vault missing (or vice versa) flags
// the present one as repairable with [setup.ErrStateStale]. This
// is the stale partial state case.
func TestClassifier_RepairablePartialState(t *testing.T) {
	t.Parallel()

	t.Run("config-without-vault", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cfg := writeFile(t, dir, "config.toml", []byte("[server]\n"), 0o600)
		cls := &setup.Classifier{}

		rep := cls.ClassifyState(context.Background(), setup.StateInputs{
			ConfigPath: cfg,
			VaultPath:  filepath.Join(dir, "missing-vault.bin"),
		})
		require.Len(t, rep.Artifacts, 2)
		require.Equal(t, setup.ClassificationRepairable, rep.Artifacts[0].Class)
		require.ErrorIs(t, rep.Artifacts[0].Err, setup.ErrStateStale)
		require.Contains(t, rep.Artifacts[0].Detail, "partial state")
		require.Equal(t, setup.ClassificationAbsent, rep.Artifacts[1].Class)
	})

	t.Run("vault-without-config", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		vault := writeFile(t, dir, "vault.bin", []byte("MAGIC..."), 0o600)
		cls := &setup.Classifier{}

		rep := cls.ClassifyState(context.Background(), setup.StateInputs{
			ConfigPath: filepath.Join(dir, "missing-cfg.toml"),
			VaultPath:  vault,
		})
		require.Len(t, rep.Artifacts, 2)
		require.Equal(t, setup.ClassificationAbsent, rep.Artifacts[0].Class)
		require.Equal(t, setup.ClassificationRepairable, rep.Artifacts[1].Class)
		require.ErrorIs(t, rep.Artifacts[1].Err, setup.ErrStateStale)
		require.Contains(t, rep.Artifacts[1].Detail, "partial state")
	})
}

// TestClassifier_CollisionWrongKind covers the explicit-collision
// path: caller expected a regular file but found a directory at
// the path (e.g. user passed --state-dir over an existing vault
// file). The result must surface [setup.ErrArtifactCollision].
func TestClassifier_CollisionWrongKind(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Treat a directory as the "config path" — wrong kind.
	cls := &setup.Classifier{}
	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		ConfigPath: dir,
	})
	require.Len(t, rep.Artifacts, 1)
	require.Equal(t, setup.ClassificationCollision, rep.Artifacts[0].Class)
	require.ErrorIs(t, rep.Artifacts[0].Err, setup.ErrArtifactCollision)
	require.Contains(t, rep.Artifacts[0].Detail, "directory")
}

// TestClassifier_StateDirLooseMode covers the loose-mode state
// directory case.
func TestClassifier_StateDirLooseMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o755))
	require.NoError(t, os.Chmod(stateDir, 0o755))

	cls := &setup.Classifier{}
	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		StateDir: stateDir,
	})
	require.Len(t, rep.Artifacts, 1)
	require.Equal(t, setup.ClassificationRepairable, rep.Artifacts[0].Class)
	require.ErrorIs(t, rep.Artifacts[0].Err, setup.ErrStateStale)
}

// TestClassifier_StateDirNotADir covers the kind-mismatch case
// for the state directory slot.
func TestClassifier_StateDirNotADir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	notADir := writeFile(t, dir, "state-but-a-file", []byte("oops"), 0o600)

	cls := &setup.Classifier{}
	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		StateDir: notADir,
	})
	require.Len(t, rep.Artifacts, 1)
	require.Equal(t, setup.ClassificationCollision, rep.Artifacts[0].Class)
	require.ErrorIs(t, rep.Artifacts[0].Err, setup.ErrArtifactCollision)
}

// TestClassifier_KeychainDenied covers the Keychain ACL-denied
// path. The fake keychain cannot synthesize the denied error
// directly, so we use a stub Keychain that returns
// ErrKeychainPermissionDenied.
func TestClassifier_KeychainDenied(t *testing.T) {
	t.Parallel()

	cls := &setup.Classifier{Keychain: stubKeychain{err: keychain.ErrKeychainPermissionDenied}}
	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		KeychainItem: setup.KeychainTarget{Service: "hush-discord", Account: "hush"},
	})
	require.Len(t, rep.Artifacts, 1)
	require.Equal(t, setup.ClassificationRepairable, rep.Artifacts[0].Class)
	require.ErrorIs(t, rep.Artifacts[0].Err, setup.ErrTokenDenied)
	require.Contains(t, rep.Artifacts[0].Detail, "denied")
}

// TestClassifier_KeychainAbsent covers the not-found path.
func TestClassifier_KeychainAbsent(t *testing.T) {
	t.Parallel()

	cls := &setup.Classifier{Keychain: stubKeychain{err: keychain.ErrKeychainItemNotFound}}
	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		KeychainItem: setup.KeychainTarget{Service: "hush-discord", Account: "hush"},
	})
	require.Len(t, rep.Artifacts, 1)
	require.Equal(t, setup.ClassificationAbsent, rep.Artifacts[0].Class)
}

// TestClassifier_KeychainNilProbe covers the "no Keychain
// configured" guard. An IsZero target is skipped entirely; a
// non-zero target with a nil Keychain produces Absent.
func TestClassifier_KeychainNilProbe(t *testing.T) {
	t.Parallel()

	cls := &setup.Classifier{}
	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		KeychainItem: setup.KeychainTarget{Service: "hush-discord", Account: "hush"},
	})
	require.Len(t, rep.Artifacts, 1)
	require.Equal(t, setup.ClassificationAbsent, rep.Artifacts[0].Class)
}

// TestClassifier_KeychainProbeUnknownErr covers the catch-all
// branch: a Keychain Retrieve error that is neither
// item-not-found nor permission-denied is classified as a
// collision so the guided flow halts.
func TestClassifier_KeychainProbeUnknownErr(t *testing.T) {
	t.Parallel()

	cls := &setup.Classifier{Keychain: stubKeychain{err: errors.New("unexpected runtime failure")}}
	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		KeychainItem: setup.KeychainTarget{Service: "hush-discord", Account: "hush"},
	})
	require.Len(t, rep.Artifacts, 1)
	require.Equal(t, setup.ClassificationCollision, rep.Artifacts[0].Class)
	require.ErrorIs(t, rep.Artifacts[0].Err, setup.ErrArtifactCollision)
}

// TestClassifier_KeychainReadable runs against the real
// FakeKeychain to ensure the production code path that destroys
// the retrieved secret is exercised.
func TestClassifier_KeychainReadable(t *testing.T) {
	t.Parallel()

	kc := makeFakeKeychainWithItem(t, "hush-discord", "hush", "token-bytes")
	cls := &setup.Classifier{Keychain: kc}
	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		KeychainItem: setup.KeychainTarget{Service: "hush-discord", Account: "hush"},
	})
	require.Len(t, rep.Artifacts, 1)
	require.Equal(t, setup.ClassificationSafeToReuse, rep.Artifacts[0].Class)
	require.Contains(t, rep.Artifacts[0].Detail, "readable")
}

// TestClassifier_StatPermissionDenied uses the StatFn seam to
// simulate a stat call that returns fs.ErrPermission.
func TestClassifier_StatPermissionDenied(t *testing.T) {
	t.Parallel()

	cls := &setup.Classifier{
		StatFn: func(_ string) (fs.FileInfo, error) {
			return nil, &os.PathError{Op: "stat", Path: "/whatever", Err: fs.ErrPermission}
		},
	}
	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		ConfigPath: "/path/that/doesnt/matter",
	})
	require.Len(t, rep.Artifacts, 1)
	require.Equal(t, setup.ClassificationRepairable, rep.Artifacts[0].Class)
	require.True(t, errors.Is(rep.Artifacts[0].Err, fs.ErrPermission))
}

// TestStateReport_Helpers covers FirstCollision, Repairable, and
// AllAbsent.
func TestStateReport_Helpers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Config: repairable (loose mode).
	cfg := writeFile(t, dir, "config.toml", []byte("[server]\n"), 0o644)
	// Vault: safe-to-reuse.
	vault := writeFile(t, dir, "vault.bin", []byte("MAGIC.."), 0o600)
	// State dir: collision (file instead of directory).
	stateDir := writeFile(t, dir, "state-but-a-file", []byte("oops"), 0o600)

	cls := &setup.Classifier{}
	rep := cls.ClassifyState(context.Background(), setup.StateInputs{
		ConfigPath: cfg,
		VaultPath:  vault,
		StateDir:   stateDir,
	})
	require.Len(t, rep.Artifacts, 3)
	require.False(t, rep.AllAbsent())
	require.Len(t, rep.Repairable(), 1)
	collision := rep.FirstCollision()
	require.NotNil(t, collision)
	require.Equal(t, setup.ArtifactStateDir, collision.Kind)
}

// TestArchive_RFC3339Suffix asserts the helper renames the source
// to <path>.bak-<RFC3339> using a UTC timestamp.
func TestArchive_RFC3339Suffix(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := writeFile(t, dir, "config.toml", []byte("[server]\n"), 0o600)

	dst, err := setup.Archive(src, pinnedTime())
	require.NoError(t, err)
	require.Equal(t, src+".bak-2026-05-18T12:30:45Z", dst)
	// Source no longer exists.
	_, err = os.Stat(src)
	require.True(t, errors.Is(err, fs.ErrNotExist))
	// Destination exists and matches the original content.
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "[server]\n", string(got))
}

// TestArchive_DirectoryRoundTrip confirms Archive works on a
// directory and not just regular files (the state dir is a
// directory).
func TestArchive_DirectoryRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "marker"), []byte("x"), 0o600))

	dst, err := setup.Archive(stateDir, pinnedTime())
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(dst, ".bak-2026-05-18T12:30:45Z"))

	// Renamed directory still contains the marker.
	got, err := os.ReadFile(filepath.Join(dst, "marker"))
	require.NoError(t, err)
	require.Equal(t, "x", string(got))
}

// TestArchive_RejectsEmptyPath covers the defensive guard.
func TestArchive_RejectsEmptyPath(t *testing.T) {
	t.Parallel()

	_, err := setup.Archive("", pinnedTime())
	require.ErrorIs(t, err, setup.ErrArtifactCollision)
}

// TestArchive_MissingSource confirms a missing source bubbles up
// the wrapped fs.ErrNotExist without a panic and without leaving
// stray .bak files.
func TestArchive_MissingSource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "nope")
	_, err := setup.Archive(target, pinnedTime())
	require.Error(t, err)
	require.True(t, errors.Is(err, fs.ErrNotExist))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "Archive must not create a sibling on stat failure")
}

// TestArchive_TimestampIsUTC asserts the stamp is rendered in UTC
// regardless of the input time's zone.
func TestArchive_TimestampIsUTC(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	src := writeFile(t, dir, "f", []byte("x"), 0o600)

	tz := time.FixedZone("PST", -8*60*60)
	when := time.Date(2026, time.May, 18, 4, 30, 45, 0, tz) // == 12:30:45 UTC
	dst, err := setup.Archive(src, when)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(dst, ".bak-2026-05-18T12:30:45Z"))
}

// stubKeychain is a single-error keychain used by classifier
// tests. Production code never sees this — keychain.NewFake
// already covers the happy path; this stub is the only way to
// surface ErrKeychainPermissionDenied without relying on macOS.
type stubKeychain struct {
	err error
}

func (s stubKeychain) Store(_ context.Context, _, _ string, _ *securebytes.SecureBytes, _ string) error {
	return s.err
}

func (s stubKeychain) Retrieve(_ context.Context, _, _ string) (*securebytes.SecureBytes, error) {
	return nil, s.err
}

func (s stubKeychain) Delete(_ context.Context, _, _ string) error { return s.err }
