package vault

// coverage_test.go covers OS-failure and error-path branches that cannot be
// reached through normal integration flows. All tests that inject global OS
// hooks intentionally omit t.Parallel() to avoid interfering with each other
// or with parallel tests in other files.

import (
	"bytes"
	"context"
	"crypto/cipher"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// Static test sentinels — satisfy err113 (no dynamic errors in function bodies).
var (
	errInjectedOpen      = errors.New("injected open error")
	errInjectedRand      = errors.New("injected rand error")
	errInjectedNonceRand = errors.New("injected nonce rand error")
	errInjectedOpenfile  = errors.New("injected openfile error from Save path")
	errInjectedWriteHdr  = errors.New("injected write header error")
	errInjectedWriteCt   = errors.New("injected ciphertext write error")
	errInjectedSync      = errors.New("injected sync error")
	errInjectedClose     = errors.New("injected close error")
	errInjectedChmodTmp  = errors.New("injected chmod tmp error")
	errInjectedRename    = errors.New("injected rename error")
	errInjectedChmodFin  = errors.New("injected final chmod error")
	errInjectedDestroy   = errors.New("injected destroy error")
	errInjectedAEAD      = errors.New("injected AEAD error")
	errInjectedAEADOpen  = errors.New("injected AEAD open error")
	errInjectedSBNew     = errors.New("injected sbNew error")
	errInjectedRead      = errors.New("injected read error")
	errInjectedRenameRm  = errors.New("injected rename for remove-fail test")
	errInjectedRemove    = errors.New("injected remove error")
	errInjectedWriteRm   = errors.New("injected write for remove-fail test")
	errInjectedRemoveWr  = errors.New("injected remove error (write path)")
	errInjectedCloseRm   = errors.New("injected close for remove-fail test")
	errInjectedRemoveCl  = errors.New("injected remove error (close path)")
	errInjectedSBNewGet  = errors.New("injected sbNew error in Get")
)

// ---------------------------------------------------------------------------
// codec.go: MarshalJSON / UnmarshalJSON error paths
// ---------------------------------------------------------------------------

func TestWireValue_MarshalJSON_DestroyedSB(t *testing.T) {
	t.Parallel()
	sb, err := securebytes.New([]byte("secret"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = sb.Destroy()

	wv := wireValue{sb: sb}
	_, err = wv.MarshalJSON()
	if err == nil {
		t.Fatal("expected error marshaling destroyed SecureBytes")
	}
}

func TestWireValue_UnmarshalJSON_NotAString(t *testing.T) {
	t.Parallel()
	var wv wireValue
	if err := wv.UnmarshalJSON([]byte("null")); err == nil {
		t.Fatal("expected error for non-string JSON token")
	}
}

func TestWireValue_UnmarshalJSON_InvalidBase64(t *testing.T) {
	t.Parallel()
	var wv wireValue
	// "!not!" contains characters outside the standard base64 alphabet.
	if err := wv.UnmarshalJSON([]byte(`"!not!"`)); err == nil {
		t.Fatal("expected error for invalid base64")
	}
}

// ---------------------------------------------------------------------------
// codec.go: aeadSeal / aeadOpen error paths
// ---------------------------------------------------------------------------

func TestAeadSeal_WrongKeySizeCiphertextNil(t *testing.T) {
	t.Parallel()
	// 31-byte key is invalid for AES; aes.NewCipher returns error inside the
	// Use callback, the closure returns early, ciphertext stays nil → "seal failed".
	badKey := make([]byte, 31)
	vk, err := securebytes.New(badKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = vk.Destroy() }()

	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	_, err = aeadSeal(vk, salt, nonce, []byte("plaintext"))
	if err == nil {
		t.Fatal("expected error from aeadSeal with wrong key size")
	}
}

func TestAeadSeal_DestroyedKey(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	vk, err := securebytes.New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = vk.Destroy()

	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	_, err = aeadSeal(vk, salt, nonce, []byte("plaintext"))
	if err == nil {
		t.Fatal("expected error from aeadSeal with destroyed key")
	}
}

func TestAeadOpen_WrongKeySize(t *testing.T) {
	t.Parallel()
	badKey := make([]byte, 31)
	vk, err := securebytes.New(badKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = vk.Destroy() }()

	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	_, err = aeadOpen(vk, salt, nonce, make([]byte, 16))
	if err == nil {
		t.Fatal("expected error from aeadOpen with wrong key size")
	}
}

func TestAeadOpen_DestroyedKey(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	vk, err := securebytes.New(key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = vk.Destroy()

	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	_, err = aeadOpen(vk, salt, nonce, make([]byte, 16))
	if err == nil {
		t.Fatal("expected error from aeadOpen with destroyed key")
	}
}

// ---------------------------------------------------------------------------
// file.go: Load ctx / stat / open / parseAndDecrypt / Save ctx / marshal / rand / seal
// ---------------------------------------------------------------------------

func TestLoad_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Load(ctx, "/dev/null", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestLoad_StatError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, err := Load(ctx, "/nonexistent/path/to/vault.hush", nil)
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
	if errors.Is(err, ErrFilePermsLoose) || errors.Is(err, ErrBadMagic) {
		t.Fatalf("unexpected sentinel: %v", err)
	}
}

func TestLoad_OpenFileError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	data := make([]byte, headerLen+16)
	copy(data, magic)
	data[4] = version
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	restore := SetOSOpenFile(func(_ string, _ int, _ os.FileMode) (*os.File, error) {
		return nil, errInjectedOpen
	})
	defer restore()

	ctx := context.Background()
	_, err := Load(ctx, path, makeVaultKey(t, 0xF1))
	if !errors.Is(err, errInjectedOpen) {
		t.Fatalf("expected injected open error, got %v", err)
	}
}

func TestLoad_EmptyFileShortHeader(t *testing.T) {
	// Covers the io.ReadAll→parseAndDecrypt path with a zero-byte file.
	t.Parallel()
	dir := makeTestDir(t)
	path := dir + "/empty.hush"
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ctx := context.Background()
	key := makeVaultKey(t, 0xF2)
	_, err := Load(ctx, path, key)
	if !errors.Is(err, ErrShortHeader) {
		t.Fatalf("want ErrShortHeader for empty file, got %v", err)
	}
}

func TestParseAndDecrypt_CorruptJSON(t *testing.T) {
	t.Parallel()
	key := makeVaultKey(t, 0xF3)
	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	ct, err := aeadSeal(key, salt, nonce, []byte("not valid json!!"))
	if err != nil {
		t.Fatalf("aeadSeal: %v", err)
	}
	data := make([]byte, 0, len(magic)+1+len(salt)+len(nonce)+len(ct))
	data = append(data, magic...)
	data = append(data, version)
	data = append(data, salt...)
	data = append(data, nonce...)
	data = append(data, ct...)

	_, err = ParseAndDecrypt(data, key)
	if !errors.Is(err, ErrAuthFailed) {
		t.Fatalf("corrupt JSON payload: want ErrAuthFailed, got %v", err)
	}
}

func TestSave_CancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Save(ctx, "/tmp/unused.hush", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestSave_MarshalError_DestroyedValue(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	key := makeVaultKey(t, 0xF4)
	ctx := context.Background()

	sb, err := securebytes.New([]byte("secret"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = sb.Destroy()
	s := Secret{Name: "KEY", Description: "desc", Value: sb}

	saveErr := Save(ctx, path, key, []Secret{s})
	if saveErr == nil {
		t.Fatal("expected marshal error from destroyed Value")
	}
}

func TestSave_RandReadError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	key := makeVaultKey(t, 0xF5)
	ctx := context.Background()

	restore := SetRandRead(func(b []byte) (int, error) { return 0, errInjectedRand })
	defer restore()

	s := makeSecret(t, "KEY", "desc", []byte("value"))
	err := Save(ctx, path, key, []Secret{s})
	if !errors.Is(err, errInjectedRand) {
		t.Fatalf("expected injected rand error, got %v", err)
	}
}

func TestSave_RandReadError_Nonce(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	// Cover the second randRead call (nonce). First call succeeds.
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	key := makeVaultKey(t, 0xF5)
	ctx := context.Background()

	callCount := 0
	restore := SetRandRead(func(b []byte) (int, error) {
		callCount++
		if callCount == 1 {
			return len(b), nil // salt succeeds
		}
		return 0, errInjectedNonceRand // nonce fails
	})
	defer restore()

	s := makeSecret(t, "KEY", "desc", []byte("value"))
	err := Save(ctx, path, key, []Secret{s})
	if !errors.Is(err, errInjectedNonceRand) {
		t.Fatalf("expected injected nonce rand error, got %v", err)
	}
}

func TestSave_SealError(t *testing.T) {
	t.Parallel()
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	badKey := make([]byte, 31)
	vk, err := securebytes.New(badKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = vk.Destroy() }()
	ctx := context.Background()

	s := makeSecret(t, "KEY", "desc", []byte("value"))
	saveErr := Save(ctx, path, vk, []Secret{s})
	if saveErr == nil {
		t.Fatal("expected seal error from bad key")
	}
}

// ---------------------------------------------------------------------------
// file.go: Save → writeTmp error path (return err after writeTmp)
// ---------------------------------------------------------------------------

func TestSave_WriteTmpError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	// Inject osOpenFile failure so writeTmp returns an error from within Save.
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	key := makeVaultKey(t, 0xE1)
	ctx := context.Background()

	restore := SetOSOpenFile(func(_ string, _ int, _ os.FileMode) (*os.File, error) {
		return nil, errInjectedOpenfile
	})
	defer restore()

	s := makeSecret(t, "KEY", "desc", []byte("value"))
	err := Save(ctx, path, key, []Secret{s})
	if !errors.Is(err, errInjectedOpenfile) {
		t.Fatalf("expected injected writeTmp error through Save, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// file.go: writeTmp error paths (direct)
// ---------------------------------------------------------------------------

func TestWriteTmp_CreateError(t *testing.T) {
	t.Parallel()
	salt := make([]byte, saltLen)
	nonce := make([]byte, nonceLen)
	err := WriteTmp("/nonexistent/dir/vault.tmp", salt, nonce, make([]byte, 16))
	if err == nil {
		t.Fatal("expected create error for non-existent parent")
	}
}

func TestWriteTmp_WriteHeaderError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	tmpPath := dir + "/vault.tmp"

	callCount := 0
	restore := SetFileWrite(func(f *os.File, b []byte) (int, error) {
		callCount++
		if callCount == 1 {
			return 0, errInjectedWriteHdr
		}
		return f.Write(b)
	})
	defer restore()

	err := WriteTmp(tmpPath, make([]byte, saltLen), make([]byte, nonceLen), make([]byte, 16))
	if !errors.Is(err, errInjectedWriteHdr) {
		t.Fatalf("expected injected write header error, got %v", err)
	}
	if _, statErr := os.Stat(tmpPath); !os.IsNotExist(statErr) {
		t.Fatal("tmp file should be removed after write header error")
	}
}

func TestWriteTmp_WriteCiphertextError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	tmpPath := dir + "/vault.tmp"

	callCount := 0
	restore := SetFileWrite(func(f *os.File, b []byte) (int, error) {
		callCount++
		if callCount == 2 {
			return 0, errInjectedWriteCt
		}
		return f.Write(b)
	})
	defer restore()

	err := WriteTmp(tmpPath, make([]byte, saltLen), make([]byte, nonceLen), make([]byte, 16))
	if !errors.Is(err, errInjectedWriteCt) {
		t.Fatalf("expected injected ciphertext write error, got %v", err)
	}
	if _, statErr := os.Stat(tmpPath); !os.IsNotExist(statErr) {
		t.Fatal("tmp file should be removed after write ciphertext error")
	}
}

func TestWriteTmp_SyncError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	tmpPath := dir + "/vault.tmp"

	restore := SetFileSync(func(f *os.File) error { return errInjectedSync })
	defer restore()

	err := WriteTmp(tmpPath, make([]byte, saltLen), make([]byte, nonceLen), make([]byte, 16))
	if !errors.Is(err, errInjectedSync) {
		t.Fatalf("expected injected sync error, got %v", err)
	}
	if _, statErr := os.Stat(tmpPath); !os.IsNotExist(statErr) {
		t.Fatal("tmp file should be removed after sync error")
	}
}

func TestWriteTmp_CloseError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	tmpPath := dir + "/vault.tmp"

	callCount := 0
	restore := SetFileClose(func(f *os.File) error {
		callCount++
		if callCount == 1 {
			_ = f.Close() // actually close to avoid fd leak
			return errInjectedClose
		}
		return f.Close()
	})
	defer restore()

	err := WriteTmp(tmpPath, make([]byte, saltLen), make([]byte, nonceLen), make([]byte, 16))
	if !errors.Is(err, errInjectedClose) {
		t.Fatalf("expected injected close error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// file.go: Save cleanup paths (chmod tmp / rename / chmod final)
// ---------------------------------------------------------------------------

func TestSave_CleanupAfterChmodTmpError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	key := makeVaultKey(t, 0xF6)
	ctx := context.Background()

	// Save V0 so path exists.
	s0 := makeSecret(t, "V0", "desc", []byte("v0"))
	if err := Save(ctx, path, key, []Secret{s0}); err != nil {
		t.Fatalf("Save V0: %v", err)
	}
	v0Data, _ := os.ReadFile(path) //nolint:gosec // test-controlled path

	// Inject failure at os.Chmod(tmpPath, 0600): first chmod call fails.
	chmodCount := 0
	restore := SetOSChmod(func(p string, m os.FileMode) error {
		chmodCount++
		if chmodCount == 1 {
			return errInjectedChmodTmp
		}
		return os.Chmod(p, m)
	})
	defer restore()

	s1 := makeSecret(t, "V1", "desc", []byte("v1"))
	saveErr := Save(ctx, path, key, []Secret{s1})
	if !errors.Is(saveErr, errInjectedChmodTmp) {
		t.Fatalf("expected injected chmod error, got %v", saveErr)
	}
	// Original file unchanged.
	v0After, _ := os.ReadFile(path) //nolint:gosec // test-controlled path
	if !bytes.Equal(v0Data, v0After) {
		t.Fatal("original vault modified")
	}
	// No tmp file remains.
	if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatal("tmp file should be removed after chmod error")
	}
}

func TestSave_CleanupAfterRenameError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	key := makeVaultKey(t, 0xF7)
	ctx := context.Background()

	s0 := makeSecret(t, "V0", "desc", []byte("v0"))
	if err := Save(ctx, path, key, []Secret{s0}); err != nil {
		t.Fatalf("Save V0: %v", err)
	}
	v0Data, _ := os.ReadFile(path) //nolint:gosec // test-controlled path

	restore := SetOSRename(func(_, _ string) error { return errInjectedRename })
	defer restore()

	s1 := makeSecret(t, "V1", "desc", []byte("v1"))
	saveErr := Save(ctx, path, key, []Secret{s1})
	if !errors.Is(saveErr, errInjectedRename) {
		t.Fatalf("expected injected rename error, got %v", saveErr)
	}
	v0After, _ := os.ReadFile(path) //nolint:gosec // test-controlled path
	if !bytes.Equal(v0Data, v0After) {
		t.Fatal("original vault modified")
	}
	if _, statErr := os.Stat(path + ".tmp"); !os.IsNotExist(statErr) {
		t.Fatal("tmp file should be removed after rename error")
	}
}

func TestSave_ChmodFinalError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	key := makeVaultKey(t, 0xF8)
	ctx := context.Background()

	chmodCount := 0
	restore := SetOSChmod(func(p string, m os.FileMode) error {
		chmodCount++
		if chmodCount == 2 { // second chmod = final post-rename chmod
			return errInjectedChmodFin
		}
		return os.Chmod(p, m)
	})
	defer restore()

	s := makeSecret(t, "KEY", "desc", []byte("value"))
	saveErr := Save(ctx, path, key, []Secret{s})
	if !errors.Is(saveErr, errInjectedChmodFin) {
		t.Fatalf("expected injected final chmod error, got %v", saveErr)
	}
}

// ---------------------------------------------------------------------------
// permissions.go: stat error paths
// ---------------------------------------------------------------------------

func TestCheckFileMode_StatError(t *testing.T) {
	t.Parallel()
	err := CheckFileMode("/nonexistent/vault.hush", 0o600)
	if err == nil {
		t.Fatal("expected stat error")
	}
	if errors.Is(err, ErrFilePermsLoose) {
		t.Fatal("must not be ErrFilePermsLoose for a stat error")
	}
}

func TestCheckParentMode_StatError(t *testing.T) {
	t.Parallel()
	err := CheckParentMode("/nonexistent/dir/vault.hush", 0o700)
	if err == nil {
		t.Fatal("expected stat error for non-existent parent")
	}
	if errors.Is(err, ErrFilePermsLoose) {
		t.Fatal("must not be ErrFilePermsLoose for a stat error")
	}
}

// ---------------------------------------------------------------------------
// store.go: Destroy error aggregation
// ---------------------------------------------------------------------------

func TestStore_Destroy_ErrorAggregation(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	key := makeVaultKey(t, 0x19)
	s := makeSecret(t, "KEY", "desc", []byte("value"))
	store := loadTestStore(t, []Secret{s}, key)

	restore := SetSBDestroyFn(func(sb *securebytes.SecureBytes) error {
		_ = sb.Destroy() // avoid leak
		return errInjectedDestroy
	})
	defer restore()

	err := store.Destroy()
	if err == nil {
		t.Fatal("expected aggregated destroy error")
	}
	if !strings.Contains(err.Error(), "injected destroy error") {
		t.Fatalf("error does not contain injected message: %v", err)
	}
}

// ---------------------------------------------------------------------------
// codec.go: newAEAD / sbNewFromUnmarshal error paths
// ---------------------------------------------------------------------------

func TestAeadSeal_NewAEADError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	key := makeVaultKey(t, 0xA1)
	defer func() { _ = key.Destroy() }()

	restore := SetNewAEAD(func(_ []byte) (cipher.AEAD, error) {
		return nil, errInjectedAEAD
	})
	defer restore()

	_, err := aeadSeal(key, make([]byte, saltLen), make([]byte, nonceLen), []byte("payload"))
	if !errors.Is(err, errInjectedAEAD) {
		t.Fatalf("expected injected AEAD error, got %v", err)
	}
}

func TestAeadOpen_NewAEADError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	key := makeVaultKey(t, 0xA2)
	defer func() { _ = key.Destroy() }()

	restore := SetNewAEAD(func(_ []byte) (cipher.AEAD, error) {
		return nil, errInjectedAEADOpen
	})
	defer restore()

	_, err := aeadOpen(key, make([]byte, saltLen), make([]byte, nonceLen), make([]byte, 16))
	if !errors.Is(err, errInjectedAEADOpen) {
		t.Fatalf("expected injected AEAD open error, got %v", err)
	}
}

func TestWireValue_UnmarshalJSON_SecureBytesNewError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	restore := SetSBNewFromUnmarshalFn(func(_ []byte) (*securebytes.SecureBytes, error) {
		return nil, errInjectedSBNew
	})
	defer restore()

	encoded := `"aGVsbG8="` // valid base64 for "hello"
	var wv wireValue
	err := wv.UnmarshalJSON([]byte(encoded))
	if !errors.Is(err, errInjectedSBNew) {
		t.Fatalf("expected injected sbNew error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// file.go: Load io.ReadAll error
// ---------------------------------------------------------------------------

func TestLoad_ReadAllError(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	data := make([]byte, headerLen+16)
	copy(data, magic)
	data[4] = version
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	restore := SetIOReadAllFn(func(_ io.Reader) ([]byte, error) {
		return nil, errInjectedRead
	})
	defer restore()

	ctx := context.Background()
	_, err := Load(ctx, path, makeVaultKey(t, 0xB1))
	if !errors.Is(err, errInjectedRead) {
		t.Fatalf("expected injected read error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// file.go: Save cleanup slog.Debug paths (remove fails with non-ENOENT)
// ---------------------------------------------------------------------------

func TestSave_CleanupRemoveFails_DebugLog(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	// Cover the slog.Debug("vault: remove tmp failed") branch in Save's cleanup().
	// Trigger: writeTmp succeeds, rename fails (injected), and then
	// osRemoveFn returns a non-ENOENT error.
	dir := makeTestDir(t)
	path := dir + "/vault.hush"
	key := makeVaultKey(t, 0xC1)
	ctx := context.Background()

	restoreRename := SetOSRename(func(_, _ string) error { return errInjectedRenameRm })
	defer restoreRename()
	restoreRemove := SetOSRemoveFn(func(_ string) error { return errInjectedRemove })
	defer restoreRemove()

	s := makeSecret(t, "KEY", "desc", []byte("v"))
	saveErr := Save(ctx, path, key, []Secret{s})
	if !errors.Is(saveErr, errInjectedRenameRm) {
		t.Fatalf("expected rename error, got %v", saveErr)
	}
}

func TestWriteTmp_WriteError_RemoveFails_DebugLog(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	// Cover the slog.Debug("vault: remove tmp on write error") branch in writeTmp's cleanup.
	dir := makeTestDir(t)
	tmpPath := dir + "/vault.tmp"

	restoreWrite := SetFileWrite(func(f *os.File, b []byte) (int, error) {
		return 0, errInjectedWriteRm
	})
	defer restoreWrite()
	restoreRemove := SetOSRemoveFn(func(_ string) error { return errInjectedRemoveWr })
	defer restoreRemove()

	err := WriteTmp(tmpPath, make([]byte, saltLen), make([]byte, nonceLen), make([]byte, 16))
	if !errors.Is(err, errInjectedWriteRm) {
		t.Fatalf("expected write error, got %v", err)
	}
}

func TestWriteTmp_CloseError_RemoveFails_DebugLog(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	// Cover the slog.Debug("vault: remove tmp on close error") branch in writeTmp.
	dir := makeTestDir(t)
	tmpPath := dir + "/vault.tmp"

	closeCount := 0

	restoreClose := SetFileClose(func(f *os.File) error {
		closeCount++
		if closeCount == 1 {
			_ = f.Close() // prevent fd leak
			return errInjectedCloseRm
		}
		return f.Close()
	})
	defer restoreClose()
	restoreRemove := SetOSRemoveFn(func(_ string) error { return errInjectedRemoveCl })
	defer restoreRemove()

	err := WriteTmp(tmpPath, make([]byte, saltLen), make([]byte, nonceLen), make([]byte, 16))
	if !errors.Is(err, errInjectedCloseRm) {
		t.Fatalf("expected close error, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// store.go: Get inner ErrDestroyed / securebytes.New error
// ---------------------------------------------------------------------------

func TestStore_Get_InnerUseReturnsErrDestroyed(t *testing.T) {
	// Uses ForceDestroyInternalContainer — do NOT call t.Parallel() since
	// it mutates internal store state.
	key := makeVaultKey(t, 0xD1)
	s := makeSecret(t, "KEY", "desc", []byte("value"))
	store := loadTestStore(t, []Secret{s}, key)
	// Don't defer Destroy — we're poking internals.

	// Destroy the inner container directly to simulate the race scenario.
	ForceDestroyInternalContainer(store, "KEY")

	// Now Get should see inner.Use() return ErrDestroyed and map it to ErrStoreDestroyed.
	_, err := store.Get("KEY")
	_ = store.Destroy()
	if !errors.Is(err, ErrStoreDestroyed) {
		t.Fatalf("want ErrStoreDestroyed from inner-destroyed path, got %v", err)
	}
}

func TestStore_Get_SecureBytesNewError_Injected(t *testing.T) {
	// Uses global hook — do NOT call t.Parallel().
	key := makeVaultKey(t, 0xD2)
	s := makeSecret(t, "KEY", "desc", []byte("value"))
	store := loadTestStore(t, []Secret{s}, key)
	defer func() { _ = store.Destroy() }()

	restore := SetSBNewFn(func(_ []byte) (*securebytes.SecureBytes, error) {
		return nil, errInjectedSBNewGet
	})
	defer restore()

	_, err := store.Get("KEY")
	if !errors.Is(err, errInjectedSBNewGet) {
		t.Fatalf("expected injected sbNew error, got %v", err)
	}
}

// TestStore_Get_BorrowError covers the "vault: borrow: %w" dead-code path via the
// forward-compatibility comment. Since Use() only returns ErrDestroyed or nil with
// the current securebytes contract, this path requires the "borrow: %w" branch to
// be reachable. We document that it is NOT reachable without a future Use() change.
// The path is retained as a forward-compatibility guard and is excluded from the
// coverage target by design (it is the ONLY such exclusion in this package).
// If a future Use() adds a new error type, this test should be updated.
func TestStore_Get_BorrowError_ForwardCompatibility(t *testing.T) {
	t.Parallel()
	// Document the unreachable path explicitly so a reader knows it is intentional.
	// No test body needed — the production comment explains the intent.
}
