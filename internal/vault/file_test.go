package vault

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

var errInjectedRenameFileTest = errors.New("injected rename failure")

// makeTestDir creates a temp dir with mode 0700 and returns its path.
func makeTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // 0700 is correct for directories
		t.Fatalf("chmod dir: %v", err)
	}
	return dir
}

// makeVaultKey creates a 32-byte AES key wrapped in SecureBytes.
func makeVaultKey(t *testing.T, seed byte) *securebytes.SecureBytes {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	vk, err := securebytes.New(key)
	if err != nil {
		t.Fatalf("securebytes.New key: %v", err)
	}
	t.Cleanup(func() { _ = vk.Destroy() })
	return vk
}

// makeSecret creates a Secret with the given name and value bytes.
func makeSecret(t *testing.T, name, description string, value []byte) Secret {
	t.Helper()
	sb, err := securebytes.New(value)
	if err != nil {
		t.Fatalf("securebytes.New value: %v", err)
	}
	t.Cleanup(func() { _ = sb.Destroy() })
	return Secret{Name: name, Description: description, Value: sb}
}

func TestVault_RoundTrip_0Secrets(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0x01)
	ctx := context.Background()

	if err := Save(ctx, path, key, []Secret{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	store, err := Load(ctx, path, key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = store.Destroy() }()

	if names := store.Names(); len(names) != 0 {
		t.Fatalf("want 0 names, got %v", names)
	}

	// Verify file length: headerLen + AES-GCM tag-only ciphertext over "[]" (2 bytes) = 33 + 2 + 16 = 51
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// headerLen(33) + gcm.Overhead(16) + len("[]")(2) = 51
	if info.Size() != int64(headerLen+16+2) {
		t.Fatalf("file size: want %d got %d", headerLen+16+2, info.Size())
	}
}

func TestVault_RoundTrip_1Secret(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0x02)
	ctx := context.Background()

	const payloadStr = "super-secret-value-1"
	s := makeSecret(t, "MY_SECRET", "my description", []byte(payloadStr))

	if err := Save(ctx, path, key, []Secret{s}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	store, err := Load(ctx, path, key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = store.Destroy() }()

	names := store.Names()
	if len(names) != 1 || names[0] != "MY_SECRET" {
		t.Fatalf("names: want [MY_SECRET] got %v", names)
	}

	got, err := store.Get("MY_SECRET")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = got.Destroy() }()

	var gotBytes []byte
	if err = got.Use(func(b []byte) {
		gotBytes = make([]byte, len(b))
		copy(gotBytes, b)
	}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if !bytes.Equal(gotBytes, []byte(payloadStr)) {
		t.Fatalf("value mismatch: want %q got %q", payloadStr, gotBytes)
	}
}

//nolint:gocognit // multi-secret round-trip test; complexity is structural
func TestVault_RoundTrip_5Secrets(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0x03)
	ctx := context.Background()

	names := []string{"ALPHA", "BRAVO", "CHARLIE", "DELTA", "ECHO"}
	secrets := make([]Secret, len(names))
	values := make([][]byte, len(names))
	for i, n := range names {
		v := []byte(fmt.Sprintf("value-for-%s", n))
		values[i] = append([]byte(nil), v...) // save copy before makeSecret zeroes v
		secrets[i] = makeSecret(t, n, "desc-"+n, v)
	}

	if err := Save(ctx, path, key, secrets); err != nil {
		t.Fatalf("Save: %v", err)
	}
	store, err := Load(ctx, path, key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = store.Destroy() }()

	gotNames := store.Names()
	if len(gotNames) != 5 {
		t.Fatalf("want 5 names got %d", len(gotNames))
	}
	for i, n := range names {
		if gotNames[i] != n {
			t.Fatalf("name[%d]: want %q got %q", i, n, gotNames[i])
		}
		sb, err := store.Get(n)
		if err != nil {
			t.Fatalf("Get %q: %v", n, err)
		}
		var got []byte
		if err = sb.Use(func(b []byte) { got = append([]byte(nil), b...) }); err != nil {
			t.Fatalf("Use %q: %v", n, err)
		}
		_ = sb.Destroy()
		if !bytes.Equal(got, values[i]) {
			t.Fatalf("value[%d] mismatch", i)
		}
	}
}

//nolint:gocognit,gocyclo // large round-trip test; complexity is structural
func TestVault_RoundTrip_500Secrets(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0x04)
	ctx := context.Background()

	rng := rand.New(rand.NewSource(42)) //nolint:gosec // test-only deterministic seed; not used for crypto
	secrets := make([]Secret, 500)
	values := make([][]byte, 500)
	for i := range secrets {
		name := fmt.Sprintf("SECRET_%04d", i)
		var val []byte
		switch i % 3 {
		case 0:
			val = []byte{byte(i)}
		case 1:
			val = make([]byte, 8*1024)
			_, _ = rng.Read(val)
		case 2:
			val = make([]byte, 64*1024)
			_, _ = rng.Read(val)
		}
		values[i] = append([]byte(nil), val...) // save copy before makeSecret zeroes val
		secrets[i] = makeSecret(t, name, "desc", val)
	}

	if err := Save(ctx, path, key, secrets); err != nil {
		t.Fatalf("Save: %v", err)
	}
	store, err := Load(ctx, path, key)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer func() { _ = store.Destroy() }()

	gotNames := store.Names()
	if len(gotNames) != 500 {
		t.Fatalf("want 500 names got %d", len(gotNames))
	}
	for i, n := range gotNames {
		expected := fmt.Sprintf("SECRET_%04d", i)
		if n != expected {
			t.Fatalf("name[%d]: want %q got %q", i, expected, n)
		}
		sb, err := store.Get(n)
		if err != nil {
			t.Fatalf("Get %q: %v", n, err)
		}
		var got []byte
		if err = sb.Use(func(b []byte) { got = append([]byte(nil), b...) }); err != nil {
			t.Fatalf("Use %q: %v", n, err)
		}
		_ = sb.Destroy()
		if !bytes.Equal(got, values[i]) {
			t.Fatalf("value[%d] mismatch", i)
		}
	}
}

func TestVault_LoadWrongPass_ReturnsAuthFailed(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	keyA := makeVaultKey(t, 0x10)
	keyB := makeVaultKey(t, 0x20)
	ctx := context.Background()

	s := makeSecret(t, "KEY", "desc", []byte("value"))
	if err := Save(ctx, path, keyA, []Secret{s}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	store, err := Load(ctx, path, keyB)
	if store != nil {
		_ = store.Destroy()
		t.Fatal("expected nil store on wrong key")
	}
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
}

func TestVault_NoLeakInError(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	keyA := makeVaultKey(t, 0x30)
	keyB := makeVaultKey(t, 0x40)
	ctx := context.Background()

	sentinel := []byte("SECRET_SHOULD_NEVER_APPEAR_3")
	s := makeSecret(t, "SENTINEL", "desc", sentinel)
	if err := Save(ctx, path, keyA, []Secret{s}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Capture slog output.
	var logBuf bytes.Buffer
	oldLogger := slog.Default()
	handler := slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldLogger)

	store, err := Load(ctx, path, keyB)
	if store != nil {
		_ = store.Destroy()
	}

	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("want ErrAuthFailed, got %v", err)
	}
	if bytes.Contains([]byte(err.Error()), sentinel) {
		t.Fatalf("sentinel leaked into err.Error()")
	}
	if bytes.Contains(logBuf.Bytes(), sentinel) {
		t.Fatalf("sentinel leaked into log output")
	}
}

func TestVault_Save_DuplicateName_NoFilesystemTouch(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0x50)
	ctx := context.Background()

	s1 := makeSecret(t, "DUP", "a", []byte("v1"))
	s2 := makeSecret(t, "DUP", "b", []byte("v2"))

	// Snapshot: neither path nor path.tmp should exist.
	err := Save(ctx, path, key, []Secret{s1, s2})
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("want ErrDuplicateName, got %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("vault file should not exist after duplicate-name error")
	}
	if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatal("tmp file should not exist after duplicate-name error")
	}
}

//nolint:gocognit // table-driven invalid-name test; complexity is structural
func TestVault_Save_InvalidName_NoFilesystemTouch(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	key := makeVaultKey(t, 0x60)
	ctx := context.Background()

	longName := make([]byte, 257)
	for i := range longName {
		longName[i] = 'A'
	}
	longDesc := make([]byte, 4097)
	for i := range longDesc {
		longDesc[i] = 'a'
	}

	table := []struct {
		name string
		s    Secret
	}{
		{"empty name", Secret{Name: "", Description: "ok", Value: makeSecret(t, "X", "ok", []byte("v")).Value}},
		{"name too long", Secret{Name: string(longName), Description: "ok", Value: makeSecret(t, "X", "ok", []byte("v")).Value}},
		{"name NUL", Secret{Name: "A\x00B", Description: "ok", Value: makeSecret(t, "X", "ok", []byte("v")).Value}},
		{"name control 0x1F", Secret{Name: "A\x1FB", Description: "ok", Value: makeSecret(t, "X", "ok", []byte("v")).Value}},
		{"name DEL 0x7F", Secret{Name: "A\x7FB", Description: "ok", Value: makeSecret(t, "X", "ok", []byte("v")).Value}},
		{"name non-ASCII", Secret{Name: "A\x80B", Description: "ok", Value: makeSecret(t, "X", "ok", []byte("v")).Value}},
		{"desc NUL", Secret{Name: "OK", Description: "a\x00b", Value: makeSecret(t, "X", "ok", []byte("v")).Value}},
		{"desc control 0x1F", Secret{Name: "OK", Description: "a\x1Fb", Value: makeSecret(t, "X", "ok", []byte("v")).Value}},
		{"desc DEL 0x7F", Secret{Name: "OK", Description: "a\x7Fb", Value: makeSecret(t, "X", "ok", []byte("v")).Value}},
		{"desc too long", Secret{Name: "OK", Description: string(longDesc), Value: makeSecret(t, "X", "ok", []byte("v")).Value}},
	}

	for _, tc := range table {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, "vault_invalid_"+tc.name+".hush")
			err := Save(ctx, path, key, []Secret{tc.s})
			if !errors.Is(err, ErrInvalidName) {
				t.Fatalf("want ErrInvalidName, got %v", err)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatal("vault file should not exist")
			}
			if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
				t.Fatal("tmp file should not exist")
			}
		})
	}
}

//nolint:gocognit,gocyclo // atomic-write test with goroutine observer; complexity is structural
func TestVault_SaveAtomic_NoIntermediate(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0x70)
	ctx := context.Background()

	// Save V1.
	s1 := makeSecret(t, "V1_SECRET", "first vault", []byte("v1-payload"))
	if err := Save(ctx, path, key, []Secret{s1}); err != nil {
		t.Fatalf("Save V1: %v", err)
	}
	infoV1, _ := os.Stat(path)
	sizeV1 := infoV1.Size()
	mtimeV1 := infoV1.ModTime()

	// Build V2 with a different payload to ensure different size.
	bigPayload := make([]byte, 512)
	for i := range bigPayload {
		bigPayload[i] = byte(i)
	}
	s2 := makeSecret(t, "V2_SECRET", "second vault with larger payload to guarantee size differs", bigPayload)

	var (
		mu          sync.Mutex
		badSnapshot bool
	)

	// Goroutine: repeatedly stat path while Save(V2) runs.
	stopCh := make(chan struct{})
	go func() {
		for {
			select {
			case <-stopCh:
				return
			default:
			}
			info, err := os.Stat(path)
			if err != nil {
				continue
			}
			sz := info.Size()
			mt := info.ModTime()
			isV1 := sz == sizeV1 && mt.Equal(mtimeV1)
			isV2 := sz != sizeV1 || !mt.Equal(mtimeV1)
			if !isV1 && !isV2 {
				mu.Lock()
				badSnapshot = true
				mu.Unlock()
			}
			time.Sleep(time.Microsecond)
		}
	}()

	if err := Save(ctx, path, key, []Secret{s2}); err != nil {
		close(stopCh)
		t.Fatalf("Save V2: %v", err)
	}
	close(stopCh)

	mu.Lock()
	bad := badSnapshot
	mu.Unlock()
	if bad {
		t.Fatal("observed an intermediate file state during atomic Save")
	}

	// No .tmp file should remain.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp file should not remain after successful Save")
	}

	// Verify V2 loads correctly.
	store, err := Load(ctx, path, key)
	if err != nil {
		t.Fatalf("Load V2: %v", err)
	}
	defer func() { _ = store.Destroy() }()
	names := store.Names()
	if len(names) != 1 || names[0] != "V2_SECRET" {
		t.Fatalf("expected V2 store, got names %v", names)
	}
}

func TestVault_SaveSetsMode0600(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0x80)
	ctx := context.Background()

	// Set umask to 0022 for the duration.
	old := syscall.Umask(0o022)
	t.Cleanup(func() { syscall.Umask(old) })

	s := makeSecret(t, "KEY", "desc", []byte("value"))
	if err := Save(ctx, path, key, []Secret{s}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("want mode 0600 got %#o", perm)
	}
}

func TestVault_Save_MidFlightFailure_RemovesTmp(t *testing.T) {
	// Uses global hook (SetOSRename) — do NOT call t.Parallel().
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0x90)
	ctx := context.Background()

	// Save a baseline V0.
	s0 := makeSecret(t, "BASELINE", "desc", []byte("baseline-payload"))
	if err := Save(ctx, path, key, []Secret{s0}); err != nil {
		t.Fatalf("Save V0: %v", err)
	}
	v0Data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("ReadFile V0: %v", err)
	}

	// Inject a rename failure so writeTmp succeeds but the atomic swap fails.
	restore := SetOSRename(func(_, _ string) error { return errInjectedRenameFileTest })
	defer restore()

	s1 := makeSecret(t, "NEW", "desc", []byte("new-payload"))
	saveErr := Save(ctx, path, key, []Secret{s1})
	restore() // restore immediately so later checks use real os.Rename

	if saveErr == nil {
		t.Fatal("expected an error from Save with injected rename failure")
	}

	// Original file must be unchanged.
	v0After, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("ReadFile after failed Save: %v", err)
	}
	if !bytes.Equal(v0Data, v0After) {
		t.Fatal("original vault was modified by a failed Save")
	}

	// No .tmp should remain.
	if _, err = os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp file should not remain after failed Save")
	}
}

func TestVault_LoadLooseFileMode_PermsLoose(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0xa0)
	ctx := context.Background()

	s := makeSecret(t, "KEY", "desc", []byte("value"))
	if err := Save(ctx, path, key, []Secret{s}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // intentionally loose for test
		t.Fatalf("chmod: %v", err)
	}
	store, err := Load(ctx, path, key)
	if store != nil {
		_ = store.Destroy()
	}
	if !errors.Is(err, ErrFilePermsLoose) {
		t.Fatalf("want ErrFilePermsLoose, got %v", err)
	}
}

func TestVault_LoadLooseParentMode_PermsLoose(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0xb0)
	ctx := context.Background()

	s := makeSecret(t, "KEY", "desc", []byte("value"))
	if err := Save(ctx, path, key, []Secret{s}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // intentionally loose for test
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // 0700 is correct for directories

	store, err := Load(ctx, path, key)
	if store != nil {
		_ = store.Destroy()
		t.Fatal("expected nil store on loose parent mode")
	}
	if !errors.Is(err, ErrFilePermsLoose) {
		t.Fatalf("want ErrFilePermsLoose, got %v", err)
	}
}

func TestVault_Save_LooseParentMode_PermsLoose(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "vault.hush")
	key := makeVaultKey(t, 0xc0)
	ctx := context.Background()

	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // intentionally loose for test
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // 0700 is correct for directories

	s := makeSecret(t, "KEY", "desc", []byte("value"))
	err := Save(ctx, path, key, []Secret{s})
	if !errors.Is(err, ErrFilePermsLoose) {
		t.Fatalf("want ErrFilePermsLoose, got %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatal("vault file must not exist after loose-parent Save")
	}
	if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatal("tmp file must not exist after loose-parent Save")
	}
}

func TestVault_Load_OversizedFile_ReturnsErrFileTooLarge(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "big.hush")
	key := makeVaultKey(t, 0xd0)
	ctx := context.Background()

	// Use os.Truncate to create a sparse file of 64 MiB + 1 bytes.
	f, err := os.Create(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err = f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err = os.Truncate(path, maxFileLen+1); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err = os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod file: %v", err)
	}

	store, err := Load(ctx, path, key)
	if store != nil {
		_ = store.Destroy()
	}
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("want ErrFileTooLarge, got %v", err)
	}

	// Exactly 64 MiB must pass the size check (though it will fail AEAD — different error).
	if err = os.Truncate(path, maxFileLen); err != nil {
		t.Fatalf("truncate exact: %v", err)
	}
	_, err2 := Load(ctx, path, key)
	if errors.Is(err2, ErrFileTooLarge) {
		t.Fatal("64 MiB exactly should not return ErrFileTooLarge")
	}
}

func TestVault_LoadBadMagic_ReturnsErrBadMagic(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "bad.hush")
	key := makeVaultKey(t, 0xe0)
	ctx := context.Background()

	// 49 bytes starting with "WRONG" instead of "HUSH".
	data := make([]byte, headerLen+16)
	copy(data, "WRONG")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Load(ctx, path, key)
	if !errors.Is(err, ErrBadMagic) {
		t.Fatalf("want ErrBadMagic, got %v", err)
	}
}

func TestVault_LoadBadVersion_ReturnsErrBadVersion(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := filepath.Join(dir, "badver.hush")
	key := makeVaultKey(t, 0xe1)
	ctx := context.Background()

	data := make([]byte, headerLen+16)
	copy(data, magic)
	data[4] = 0x02 // wrong version
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Load(ctx, path, key)
	if !errors.Is(err, ErrBadVersion) {
		t.Fatalf("want ErrBadVersion, got %v", err)
	}
}

func TestVault_LoadTruncatedAtMagic_ShortHeader(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	key := makeVaultKey(t, 0xe2)
	ctx := context.Background()

	for length := 0; length <= 3; length++ {
		t.Run(fmt.Sprintf("len%d", length), func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("trunc_magic_%d.hush", length))
			data := make([]byte, length)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := Load(ctx, path, key)
			if !errors.Is(err, ErrShortHeader) {
				t.Fatalf("want ErrShortHeader, got %v", err)
			}
		})
	}
}

func TestVault_LoadTruncatedAtSalt_ShortHeader(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	key := makeVaultKey(t, 0xe3)
	ctx := context.Background()

	// lengths 5..20: above magic+version, below end of salt
	for length := 5; length <= 20; length++ {
		t.Run(fmt.Sprintf("len%d", length), func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("trunc_salt_%d.hush", length))
			data := make([]byte, length)
			copy(data, magic)
			data[4] = version
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := Load(ctx, path, key)
			if !errors.Is(err, ErrShortHeader) {
				t.Fatalf("len=%d: want ErrShortHeader, got %v", length, err)
			}
		})
	}
}

func TestVault_LoadTruncatedAtNonce_ShortHeader(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	key := makeVaultKey(t, 0xe4)
	ctx := context.Background()

	// lengths 21..32: above salt, below end of nonce
	// Also headerLen=33 itself (no ciphertext, below minimum)
	lengths := make([]int, 0, 14)
	for l := 21; l <= 33; l++ {
		lengths = append(lengths, l)
	}
	for _, length := range lengths {
		t.Run(fmt.Sprintf("len%d", length), func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("trunc_nonce_%d.hush", length))
			data := make([]byte, length)
			copy(data, magic)
			data[4] = version
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := Load(ctx, path, key)
			if !errors.Is(err, ErrShortHeader) {
				t.Fatalf("len=%d: want ErrShortHeader, got %v", length, err)
			}
		})
	}
}

func TestVault_LoadTruncatedCiphertext_AuthFailed(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	key := makeVaultKey(t, 0xe5)
	ctx := context.Background()

	// Save a real vault first to get a properly formed file.
	vaultPath := filepath.Join(dir, "real.hush")
	s := makeSecret(t, "KEY", "desc", []byte("value123456789"))
	if err := Save(ctx, vaultPath, key, []Secret{s}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	realData, err := os.ReadFile(vaultPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Truncate by 1 byte (still >= headerLen + cipher.Overhead = 49).
	if len(realData) <= headerLen+16 {
		t.Skip("real vault too small to truncate safely")
	}
	truncData := realData[:len(realData)-1]
	path := filepath.Join(dir, "trunc_ct.hush")
	if err = os.WriteFile(path, truncData, 0o600); err != nil { //nolint:gosec // test-controlled path
		t.Fatalf("write: %v", err)
	}
	_, err = Load(ctx, path, key)
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("truncated ciphertext: want ErrAuthFailed, got %v", err)
	}

	// Exactly headerLen + cipher.Overhead() = 49 bytes (tag-only minimum).
	minData := make([]byte, headerLen+16)
	copy(minData, magic)
	minData[4] = version
	path2 := filepath.Join(dir, "min_ct.hush")
	if err = os.WriteFile(path2, minData, 0o600); err != nil {
		t.Fatalf("write min: %v", err)
	}
	_, err = Load(ctx, path2, key)
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("tag-minimum ciphertext: want ErrAuthFailed, got %v", err)
	}
}
