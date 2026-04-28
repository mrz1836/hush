package vault

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/mrz1836/hush/internal/vault/securebytes"
)

//nolint:gochecknoglobals // file-format constant; immutable after init
var magic = []byte{0x48, 0x55, 0x53, 0x48}

const (
	version    byte = 0x01
	saltLen         = 16
	nonceLen        = 12
	headerLen       = 4 + 1 + saltLen + nonceLen // = 33
	maxFileLen      = 64 * 1024 * 1024           // 64 MiB
)

// OS operation bridges; replaceable in tests to cover OS-failure paths
// (same pattern as securebytes.mlockFn/munlockFn).
//
//nolint:gochecknoglobals // OS bridges; test-hookable for error-path coverage
var (
	randRead    = rand.Read
	osOpenFile  = os.OpenFile
	osRename    = os.Rename
	osChmod     = os.Chmod
	osRemoveFn  = os.Remove
	ioReadAllFn = io.ReadAll
	fileWrite   = (*os.File).Write
	fileSync    = (*os.File).Sync
	fileClose   = (*os.File).Close
)

// Sentinel errors. Compare with errors.Is.
var (
	ErrBadMagic       = errors.New("hush/vault: bad magic")
	ErrBadVersion     = errors.New("hush/vault: bad version")
	ErrShortHeader    = errors.New("hush/vault: short header")
	ErrAuthFailed     = errors.New("hush/vault: authentication failed")
	ErrFilePermsLoose = errors.New("hush/vault: file permissions loose")
	ErrSecretNotFound = errors.New("hush/vault: secret not found")
	ErrStoreDestroyed = errors.New("hush/vault: store destroyed")
	ErrDuplicateName  = errors.New("hush/vault: duplicate secret name")
	ErrFileTooLarge   = errors.New("hush/vault: file too large")
	ErrInvalidName    = errors.New("hush/vault: invalid secret name or description")
)

// Secret is one named, described, value-bearing entry in the vault.
type Secret struct {
	Name        string
	Description string
	Value       *securebytes.SecureBytes
}

// Store is the in-memory view of a loaded vault.
type Store interface {
	Get(name string) (*securebytes.SecureBytes, error)
	Names() []string
	Destroy() error
}

// Load reads, validates, and decrypts the vault file at path using vaultKey.
func Load(ctx context.Context, path string, vaultKey *securebytes.SecureBytes) (Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Single stat: mode + size together (avoids TOCTOU between two stats).
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("vault: stat %q: %w", path, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		return nil, fmt.Errorf("vault: file %q mode %#o != %#o: %w", path, got, 0o600, ErrFilePermsLoose)
	}
	if info.Size() > maxFileLen {
		return nil, fmt.Errorf("vault: file %q size %d exceeds %d: %w", path, info.Size(), maxFileLen, ErrFileTooLarge)
	}

	// Parent directory mode check.
	if err = checkParentMode(path, 0o700); err != nil {
		return nil, err
	}

	f, err := osOpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("vault: open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	data, err := ioReadAllFn(f)
	if err != nil {
		return nil, fmt.Errorf("vault: read %q: %w", path, err)
	}

	return parseAndDecrypt(data, vaultKey)
}

// parseAndDecrypt validates the HUSH envelope and decrypts the payload.
//
//nolint:gocyclo // sequential header-parse state machine; complexity is structural
func parseAndDecrypt(data []byte, vaultKey *securebytes.SecureBytes) (Store, error) {
	// Length-class invariants per data-model.md §1.
	if len(data) < 4 {
		return nil, fmt.Errorf("vault: %w", ErrShortHeader)
	}
	if data[0] != magic[0] || data[1] != magic[1] || data[2] != magic[2] || data[3] != magic[3] {
		return nil, fmt.Errorf("vault: %w", ErrBadMagic)
	}
	if len(data) < 5 {
		return nil, fmt.Errorf("vault: %w", ErrShortHeader)
	}
	if data[4] != version {
		return nil, fmt.Errorf("vault: %w", ErrBadVersion)
	}
	// Minimum: headerLen(33) + cipher.Overhead(16) = 49
	if len(data) < headerLen+16 {
		return nil, fmt.Errorf("vault: %w", ErrShortHeader)
	}

	salt := data[5 : 5+saltLen]
	nonce := data[5+saltLen : headerLen]
	ciphertext := data[headerLen:]

	plaintext, err := aeadOpen(vaultKey, salt, nonce, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", ErrAuthFailed)
	}
	_ = salt // salt is carried verbatim; consumed by KDF at higher layer

	var wires []wireSecret
	if err = json.Unmarshal(plaintext, &wires); err != nil {
		return nil, fmt.Errorf("vault: decode payload: %w", ErrAuthFailed)
	}

	return newMemStore(wires), nil
}

// Save encrypts secrets to the vault file at path using vaultKey.
//
//nolint:gocognit,gocyclo // multi-step atomic-write flow; complexity is structural
func Save(ctx context.Context, path string, vaultKey *securebytes.SecureBytes, secrets []Secret) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Pre-pass: validate names/descriptions before any filesystem touch.
	if err := validateSecrets(secrets); err != nil {
		return err
	}

	// Parent directory permission check before any filesystem write.
	if err := checkParentMode(path, 0o700); err != nil {
		return err
	}

	// Marshal the wire-shape JSON.
	wires := make([]wireSecret, len(secrets))
	for i, s := range secrets {
		wires[i] = wireSecret{
			Name:        s.Name,
			Description: s.Description,
			Value:       wireValue{sb: s.Value},
		}
	}
	plaintext, err := json.Marshal(wires)
	if err != nil {
		return fmt.Errorf("vault: marshal: %w", err)
	}

	// Generate fresh salt and nonce.
	salt := make([]byte, saltLen)
	if _, err = randRead(salt); err != nil {
		return fmt.Errorf("vault: rand salt: %w", err)
	}
	nonce := make([]byte, nonceLen)
	if _, err = randRead(nonce); err != nil {
		return fmt.Errorf("vault: rand nonce: %w", err)
	}

	ciphertext, err := aeadSeal(vaultKey, salt, nonce, plaintext)
	if err != nil {
		return fmt.Errorf("vault: seal: %w", err)
	}

	// Atomic write: temp file → fsync → rename.
	tmpPath := path + ".tmp"
	if err = writeTmp(tmpPath, salt, nonce, ciphertext); err != nil {
		return err
	}
	// Best-effort cleanup of the tmp file on any subsequent error.
	cleanup := func(origErr error) error {
		if rmErr := osRemoveFn(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.Default().Debug("vault: remove tmp failed", "path", tmpPath, "err", rmErr)
		}
		return origErr
	}

	if err = osChmod(tmpPath, 0o600); err != nil {
		return cleanup(fmt.Errorf("vault: chmod tmp: %w", err))
	}
	if err = osRename(tmpPath, path); err != nil {
		return cleanup(fmt.Errorf("vault: rename: %w", err))
	}
	// Belt-and-braces: neutralize any umask effect on the renamed file.
	if err = osChmod(path, 0o600); err != nil {
		return fmt.Errorf("vault: chmod: %w", err)
	}
	return nil
}

// writeTmp writes the HUSH envelope to tmpPath, fsyncs, and closes.
// Uses the os bridge functions (osOpenFile, fileWrite, fileSync, fileClose)
// so tests can inject OS-level failures.
//
//nolint:gocognit // cleanup-path branching; complexity is structural
func writeTmp(tmpPath string, salt, nonce, ciphertext []byte) error {
	f, err := osOpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("vault: create tmp: %w", err)
	}

	cleanup := func(origErr error) error {
		_ = fileClose(f)
		if rmErr := osRemoveFn(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.Default().Debug("vault: remove tmp on write error", "path", tmpPath, "err", rmErr)
		}
		return origErr
	}

	header := make([]byte, headerLen)
	copy(header[0:4], magic)
	header[4] = version
	copy(header[5:5+saltLen], salt)
	copy(header[5+saltLen:headerLen], nonce)

	if _, err = fileWrite(f, header); err != nil {
		return cleanup(fmt.Errorf("vault: write header: %w", err))
	}
	if _, err = fileWrite(f, ciphertext); err != nil {
		return cleanup(fmt.Errorf("vault: write ciphertext: %w", err))
	}
	// fsync before rename: durability guarantee.
	if err = fileSync(f); err != nil {
		return cleanup(fmt.Errorf("vault: fsync: %w", err))
	}
	if err = fileClose(f); err != nil {
		if rmErr := osRemoveFn(tmpPath); rmErr != nil && !os.IsNotExist(rmErr) {
			slog.Default().Debug("vault: remove tmp on close error", "path", tmpPath, "err", rmErr)
		}
		return fmt.Errorf("vault: close tmp: %w", err)
	}
	return nil
}

// validateSecrets checks for duplicate names and FR-008 violations.
func validateSecrets(secrets []Secret) error {
	seen := make(map[string]struct{}, len(secrets))
	for _, s := range secrets {
		if _, dup := seen[s.Name]; dup {
			return fmt.Errorf("vault: duplicate name %q: %w", s.Name, ErrDuplicateName)
		}
		seen[s.Name] = struct{}{}

		if err := validateName(s.Name); err != nil {
			return err
		}
		if err := validateDescription(s.Description); err != nil {
			return err
		}
	}
	return nil
}

// validateName enforces FR-008: non-empty, ≤256 bytes, printable ASCII 0x20–0x7E.
func validateName(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("vault: empty name: %w", ErrInvalidName)
	}
	if len(name) > 256 {
		return fmt.Errorf("vault: name too long: %w", ErrInvalidName)
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if b < 0x20 || b > 0x7E {
			return fmt.Errorf("vault: name contains invalid byte 0x%02x: %w", b, ErrInvalidName)
		}
	}
	return nil
}

// validateDescription enforces FR-008: ≤4096 bytes, no 0x00–0x1F, no 0x7F.
func validateDescription(desc string) error {
	if len(desc) > 4096 {
		return fmt.Errorf("vault: description too long: %w", ErrInvalidName)
	}
	for i := 0; i < len(desc); i++ {
		b := desc[i]
		if b <= 0x1F || b == 0x7F {
			return fmt.Errorf("vault: description contains invalid byte 0x%02x: %w", b, ErrInvalidName)
		}
	}
	return nil
}

// parentDir is a filepath.Dir wrapper used internally and in permissions.go.
func parentDir(path string) string {
	return filepath.Dir(path)
}
