package config

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// FuzzSuperviseTOML feeds random byte streams to Load. Contract:
//   - no panic under any input
//   - every non-nil error returned is one of the package's named sentinels
//     (or wraps one), or is an fs-level error from the file open path
//
// Seed corpus lives in testdata/seeds/FuzzSuperviseTOML/. Constitution VIII
// fuzz target #5 (TOML parse — distinct from SDD-06's FuzzServerTOML).
func FuzzSuperviseTOML(f *testing.F) { //nolint:gocognit,gocyclo // fuzz target: seed loading + assertions are inherently complex
	entries, err := os.ReadDir("testdata/seeds/FuzzSuperviseTOML")
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join("testdata/seeds/FuzzSuperviseTOML", entry.Name()))
			if readErr == nil {
				f.Add(data)
			}
		}
	}

	allSentinels := []error{
		ErrTOMLDecode, ErrUnknownField, ErrMissingRequiredField, ErrInvalidDuration,
		ErrUnknownValidator,
		ErrGraceWindowTooLong, ErrGraceTTLWithoutCache,
		ErrRefreshWindowFormat, ErrRefreshWindowOrder,
		ErrCommandEmpty, ErrCommandPathRelative,
		ErrScopeEmpty, ErrSessionTypeInvalid,
		ErrRequestedTTLOutOfRange, ErrServerURLInvalid,
		ErrLogLevelInvalid, ErrWatchdogRateInvalid,
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
		return errors.Is(err, fs.ErrNotExist) ||
			errors.Is(err, fs.ErrPermission) ||
			errors.Is(err, context.Canceled)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "fuzz.toml")
		if writeErr := os.WriteFile(cfg, data, 0o600); writeErr != nil {
			return
		}

		s, err := Load(context.Background(), cfg)
		if err != nil {
			if !isKnownError(err) {
				t.Errorf("FuzzSuperviseTOML: unknown error type: %v", err)
			}
			if s != nil {
				t.Error("FuzzSuperviseTOML: non-nil *Supervisor returned alongside non-nil error")
			}
			return
		}
		// On success, every validator must be in the allow-list.
		for _, v := range s.Validators {
			if _, ok := validatorAllowList[string(v)]; !ok {
				t.Errorf("FuzzSuperviseTOML: loaded config contains non-allow-listed validator %q", v)
			}
		}
	})
}
