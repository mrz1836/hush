package config

import (
	"context"
	"errors"
	"fmt"
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
// Seed corpus lives in testdata/seeds/FuzzSuperviseTOML/. TOML-parse fuzz
// target — distinct from the server-config FuzzServerTOML.
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
		ErrResealTimezoneInvalid, ErrResealTimezoneMissing,
		ErrResealTimeFormat, ErrResealTimeMissing, ErrResealWeekdayInvalid,
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

func FuzzResealScheduleStrings(f *testing.F) { //nolint:gocognit // fuzz target: seed setup + sentinel assertions are intentionally in one harness
	f.Add("America/New_York", "03:00", "04:00")
	f.Add("", "03:00", "04:00")
	f.Add("Not/AZone", "03:00", "04:00")
	f.Add("America/New_York", "3:00", "04:00")
	f.Add("America/New_York", "03:00", "24:00")

	base, err := os.ReadFile("testdata/valid_minimal.toml")
	if err != nil {
		f.Fatalf("read seed config: %v", err)
	}

	newSentinels := []error{
		ErrResealTimezoneInvalid, ErrResealTimezoneMissing,
		ErrResealTimeFormat, ErrResealTimeMissing, ErrResealWeekdayInvalid,
	}
	isKnownResealError := func(err error) bool {
		for _, sentinel := range newSentinels {
			if errors.Is(err, sentinel) {
				return true
			}
		}
		return false
	}

	f.Fuzz(func(t *testing.T, timezone, dailyTime, mondayOverride string) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "reseal.toml")
		body := string(base) + fmt.Sprintf(`
[reseal]
timezone = %q
daily_time = %q

[reseal.overrides]
monday = %q
`, timezone, dailyTime, mondayOverride)
		if writeErr := os.WriteFile(cfg, []byte(body), 0o600); writeErr != nil {
			return
		}

		s, err := Load(context.Background(), cfg)
		if err != nil {
			if !isKnownResealError(err) {
				t.Errorf("FuzzResealScheduleStrings: unknown error type: %v", err)
			}
			return
		}
		if s.Reseal == nil {
			t.Error("FuzzResealScheduleStrings: loaded config has nil reseal schedule")
			return
		}
		if validateErr := s.Validate(); validateErr != nil {
			t.Errorf("FuzzResealScheduleStrings: loaded invalid schedule: %v", validateErr)
		}
	})
}
