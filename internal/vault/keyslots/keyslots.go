// Package keyslots persists a go-tumbler envelope as a sidecar next to the
// vault file, rather than inside it. The vault codec (internal/vault) is a
// rigid, size-bounded, TOCTOU-checked, mmap-watched format; a sidecar keeps
// the YubiKey keyslots entirely out of that path and its reload machinery.
//
// The sidecar is <stateDir>/keyslots.json, written 0600 via a temp-file +
// rename, with the same parent-directory permission discipline as the vault.
package keyslots

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// fileName is the sidecar file name within the state directory.
	fileName = "keyslots.json"
	// filePerm is the sidecar file mode (owner read/write only).
	filePerm = 0o600
	// dirPerm is the required parent-directory mode.
	dirPerm = 0o700
	// maxFileLen bounds the sidecar read (envelopes are tiny).
	maxFileLen = 1 << 20 // 1 MiB
	// formatVersion is the sidecar container version (independent of the
	// tumbler envelope's own format version).
	formatVersion = 1
)

// ErrNotFound indicates no keyslots sidecar exists for the state directory.
var ErrNotFound = errors.New("hush/keyslots: no keyslots file")

// ErrFilePermsLoose indicates the sidecar has looser-than-0600 permissions.
var ErrFilePermsLoose = errors.New("hush/keyslots: file permissions loose")

// ErrTooLarge indicates the sidecar exceeds the size bound.
var ErrTooLarge = errors.New("hush/keyslots: file too large")

// container is the on-disk JSON shape. Envelope is base64-encoded by
// encoding/json automatically.
type container struct {
	Version  int    `json:"version"`
	Policy   string `json:"policy"`
	Envelope []byte `json:"envelope"`
}

// Path returns the sidecar path for a state directory.
func Path(stateDir string) string {
	return filepath.Join(stateDir, fileName)
}

// Exists reports whether a keyslots sidecar is present.
func Exists(stateDir string) bool {
	info, err := os.Stat(Path(stateDir))
	return err == nil && !info.IsDir()
}

// Load reads and validates the sidecar, returning the marshaled tumbler
// envelope bytes and the advisory policy hint. Returns ErrNotFound when
// absent.
func Load(stateDir string) (envelope []byte, policy string, err error) {
	path := Path(stateDir)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", ErrNotFound
		}
		return nil, "", fmt.Errorf("hush/keyslots: stat: %w", err)
	}
	if got := info.Mode().Perm(); got != filePerm {
		return nil, "", fmt.Errorf("hush/keyslots: mode %#o != %#o: %w", got, filePerm, ErrFilePermsLoose)
	}
	if info.Size() > maxFileLen {
		return nil, "", fmt.Errorf("hush/keyslots: size %d: %w", info.Size(), ErrTooLarge)
	}
	//nolint:gosec // G304: path is stateDir + fixed constant file name.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("hush/keyslots: read: %w", err)
	}
	var c container
	if err = json.Unmarshal(data, &c); err != nil {
		return nil, "", fmt.Errorf("hush/keyslots: parse: %w", err)
	}
	if len(c.Envelope) == 0 {
		return nil, "", fmt.Errorf("hush/keyslots: %w", ErrNotFound)
	}
	return c.Envelope, c.Policy, nil
}

// Save writes the sidecar atomically (temp file + rename) with 0600
// permissions, after checking the parent directory mode.
func Save(stateDir string, envelope []byte, policy string) error {
	if len(envelope) == 0 {
		return errors.New("hush/keyslots: empty envelope")
	}
	if err := checkDirMode(stateDir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(container{
		Version:  formatVersion,
		Policy:   policy,
		Envelope: envelope,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("hush/keyslots: marshal: %w", err)
	}

	path := Path(stateDir)
	tmp := path + ".tmp"
	if writeErr := os.WriteFile(tmp, data, filePerm); writeErr != nil {
		return fmt.Errorf("hush/keyslots: write tmp: %w", writeErr)
	}
	// Neutralize any umask effect before the rename.
	if chmodErr := os.Chmod(tmp, filePerm); chmodErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("hush/keyslots: chmod tmp: %w", chmodErr)
	}
	if renameErr := os.Rename(tmp, path); renameErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("hush/keyslots: rename: %w", renameErr)
	}
	return nil
}

// Remove deletes the sidecar. It is a no-op if the file is absent.
func Remove(stateDir string) error {
	if err := os.Remove(Path(stateDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("hush/keyslots: remove: %w", err)
	}
	return nil
}

// checkDirMode verifies the parent directory exists and is 0700.
func checkDirMode(stateDir string) error {
	info, err := os.Stat(stateDir)
	if err != nil {
		return fmt.Errorf("hush/keyslots: stat dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("hush/keyslots: %q is not a directory", stateDir)
	}
	if got := info.Mode().Perm(); got != dirPerm {
		return fmt.Errorf("hush/keyslots: dir mode %#o != %#o: %w", got, dirPerm, ErrFilePermsLoose)
	}
	return nil
}
