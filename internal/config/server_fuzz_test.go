package config

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// FuzzServerTOML feeds random byte streams to LoadServer. Contract:
//   - no panic under any input
//   - every non-nil error returned is one of the package's named sentinels
//     (or wraps one), or is an fs-level error for file operations
//
// Seed corpus lives in testdata/fuzz/FuzzServerTOML/.
func FuzzServerTOML(f *testing.F) { //nolint:gocognit,gocyclo // fuzz target: seed loading + corpus replacement + assertions are inherently complex
	// Seed from the corpus directory.
	entries, err := os.ReadDir("testdata/fuzz/FuzzServerTOML")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join("testdata/fuzz/FuzzServerTOML", entry.Name()))
			if readErr == nil {
				f.Add(data)
			}
		}
	}

	allSentinels := []error{
		ErrTOMLDecode, ErrUnknownField, ErrMissingRequiredField, ErrInvalidDuration,
		ErrTailscaleBindRequired, ErrListenLoopback, ErrListenUnspecified,
		ErrListenPublic, ErrListenMalformed, ErrTailscaleRequired,
		ErrPathPrefixInvalid, ErrAuditLogEscape,
		ErrStateDirNotFound, ErrStateDirUnsafe,
		ErrArgonMemoryTooLow, ErrArgonTimeTooLow, ErrArgonThreadsTooLow,
		ErrSupervisorTTLOutOfRange,
	}

	isKnownError := func(err error) bool {
		if err == nil {
			return true
		}
		for _, sentinel := range allSentinels {
			if errors.Is(err, sentinel) {
				return true
			}
		}
		// fs-level errors (file not found, open failure) are acceptable
		return errors.Is(err, fs.ErrNotExist) ||
			errors.Is(err, fs.ErrPermission) ||
			errors.Is(err, context.Canceled)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		// Write fuzz input to a real file alongside a real state_dir.
		stateDir := t.TempDir()
		cfg := filepath.Join(stateDir, "fuzz.toml")
		if writeErr := os.WriteFile(cfg, data, 0o600); writeErr != nil {
			return // I/O setup failure; not a bug
		}

		// Replace any __STATE_DIR__ placeholder in the fuzz corpus so that
		// hand-crafted seeds with valid state_dir references can run cleanly.
		//
		// We do a best-effort replace: if the file after replacement is
		// identical to the original, no allocation was wasted.
		replaced := make([]byte, 0, len(data))
		needle := []byte("__STATE_DIR__")
		sd := []byte(stateDir)
		buf := data
		for {
			idx := indexBytes(buf, needle)
			if idx < 0 {
				replaced = append(replaced, buf...)
				break
			}
			replaced = append(replaced, buf[:idx]...)
			replaced = append(replaced, sd...)
			buf = buf[idx+len(needle):]
		}
		if len(replaced) != len(data) {
			if writeErr := os.WriteFile(cfg, replaced, 0o600); writeErr != nil {
				return
			}
		}

		s, err := LoadServer(context.Background(), cfg)
		if err != nil {
			if !isKnownError(err) {
				t.Errorf("FuzzServerTOML: unknown error type: %v", err)
			}
			if s != nil {
				t.Error("FuzzServerTOML: non-nil *Server returned alongside non-nil error")
			}
		}
	})
}

// indexBytes returns the index of needle in haystack, or -1 if not found.
func indexBytes(haystack, needle []byte) int {
	n := len(needle)
	if n == 0 {
		return 0
	}
	for i := 0; i <= len(haystack)-n; i++ {
		match := true
		for j := 0; j < n; j++ {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
