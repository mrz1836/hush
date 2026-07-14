package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadBody is a small helper that writes body to a temp file and Loads it.
func loadBody(t *testing.T, body string) (*Supervisor, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "supervise.toml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return Load(context.Background(), p)
}

// withMinimalReplaced reads testdata/valid_minimal.toml and applies the given
// replacements. Each pair is (old, new); old must appear exactly once.
func withMinimalReplaced(t *testing.T, replacements ...string) string {
	t.Helper()
	require.Equal(t, 0, len(replacements)%2)
	b, err := os.ReadFile("testdata/valid_minimal.toml")
	require.NoError(t, err)
	body := string(b)
	for i := 0; i < len(replacements); i += 2 {
		body = strings.Replace(body, replacements[i], replacements[i+1], 1)
	}
	return body
}

// ---- US2: missing required fields ------------------------------------------

func TestSuperviseConfig_RejectsMissingRequiredField(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		prefix string
	}{
		{"name", "name ="},
		{"reason", "reason ="},
		{"server_url", "server_url ="},
		{"client_machine_index", "client_machine_index ="},
		{"session_type", "session_type ="},
		{"status_socket", "status_socket ="},
		{"pid_file", "pid_file ="},
	}
	b, err := os.ReadFile("testdata/valid_minimal.toml")
	require.NoError(t, err)
	base := string(b)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := removeLine(base, tc.prefix)
			_, loadErr := loadBody(t, body)
			require.Error(t, loadErr)
			assert.True(t, errors.Is(loadErr, ErrMissingRequiredField), "expected ErrMissingRequiredField, got %v", loadErr)
			assert.Contains(t, loadErr.Error(), tc.name, "error should name field; got %v", loadErr)
		})
	}
}

func TestSuperviseConfig_MultipleMissingFields_AllSurfaced(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/valid_minimal.toml")
	require.NoError(t, err)
	body := removeLine(string(b), "name =")
	body = removeLine(body, "reason =")
	_, loadErr := loadBody(t, body)
	require.Error(t, loadErr)
	assert.True(t, errors.Is(loadErr, ErrMissingRequiredField))
	// errors.Is on a joined error should match the wrapped sentinel.
	msg := loadErr.Error()
	assert.Contains(t, msg, "name")
	assert.Contains(t, msg, "reason")
}

// ---- US3: validator allow-list ---------------------------------------------

func TestSuperviseConfig_RejectsUnknownValidator(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		val  string
	}{
		{"slack", "slack"},
		{"typo_anthropc", "anthropc"},
		{"empty", ""},
		{"uppercase", "ANTHROPIC"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := withMinimalReplaced(t, `ANTHROPIC_API_KEY = "anthropic"`, `ANTHROPIC_API_KEY = "`+tc.val+`"`)
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrUnknownValidator), "expected ErrUnknownValidator, got %v", err)
		})
	}
}

func TestSuperviseConfig_AcceptsAllAllowListedValidators(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"anthropic", "anthropic-oauth", "openai", "google-ai", "github"} {
		t.Run(v, func(t *testing.T) {
			t.Parallel()
			body := withMinimalReplaced(t, `ANTHROPIC_API_KEY = "anthropic"`, `ANTHROPIC_API_KEY = "`+v+`"`)
			_, err := loadBody(t, body)
			require.NoError(t, err)
		})
	}
}

func TestErrUnknownValidator_DoesNotIncludeSecretMaterial(t *testing.T) {
	t.Parallel()
	const lhs = "HIGH_ENTROPY_LHS_xyz789ABC"
	body := withMinimalReplaced(
		t,
		`ANTHROPIC_API_KEY = "anthropic"`,
		lhs+` = "slack"`,
	)
	body = strings.Replace(body, `"ANTHROPIC_API_KEY",`, `"`+lhs+`",`, 1)
	_, err := loadBody(t, body)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrUnknownValidator))
	assert.Contains(t, err.Error(), `"slack"`, "error should name validator-RHS")
	assert.NotContains(t, err.Error(), lhs, "error must not include LHS secret name")
}

// ---- US4: grace window cap + contradiction guard ---------------------------

func TestSuperviseConfig_GraceWindowOver4h_Rejected(t *testing.T) {
	t.Parallel()
	cases := []string{"5h", "12h", "4h1m", "24h"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			body := "cache_secrets_for_restart = true\ncache_grace_ttl = \"" + c + "\"\n" + minimalBody(t)
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrGraceWindowTooLong), "expected ErrGraceWindowTooLong, got %v", err)
		})
	}
}

func TestSuperviseConfig_GraceWindowExactly4h_Accepted(t *testing.T) {
	t.Parallel()
	body := "cache_secrets_for_restart = true\ncache_grace_ttl = \"4h\"\n" + minimalBody(t)
	_, err := loadBody(t, body)
	require.NoError(t, err)
}

func TestSuperviseConfig_GraceTTLWithoutCache_Rejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{"flag_absent_ttl_set", "cache_grace_ttl = \"1h\"\n" + minimalBody(t)},
		{"flag_false_ttl_set", "cache_secrets_for_restart = false\ncache_grace_ttl = \"1h\"\n" + minimalBody(t)},
		{"flag_false_ttl_zero", "cache_secrets_for_restart = false\ncache_grace_ttl = \"0s\"\n" + minimalBody(t)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadBody(t, tc.body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrGraceTTLWithoutCache), "expected ErrGraceTTLWithoutCache, got %v", err)
		})
	}
}

func TestSuperviseConfig_GraceTTL_AbsentWithCacheTrue_AppliesDefault(t *testing.T) {
	t.Parallel()
	body := "cache_secrets_for_restart = true\n" + minimalBody(t)
	s, err := loadBody(t, body)
	require.NoError(t, err)
	assert.Equal(t, DefaultGraceWindow, s.CacheGraceTTL)
}

// ---- US5: refresh window ---------------------------------------------------

func TestSuperviseConfig_RefreshWindowFormat(t *testing.T) {
	t.Parallel()
	// Empty string is treated as "absent" → default applies — see
	// TestSuperviseConfig_DefaultRefreshWindow for that path. The cases
	// below are non-empty strings whose syntax fails the HH:MM-HH:MM gate.
	cases := []string{
		"9-10",
		"09:00 to 10:00",
		"09:00-10",
		"09:00-25:00",
		"99:99-99:99",
		"09:00-10:00-bad",
		"9:00-10:00",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			body := withMinimalReplaced(t, `refresh_window = "09:00-10:00"`, `refresh_window = "`+c+`"`)
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrRefreshWindowFormat), "expected ErrRefreshWindowFormat, got %v", err)
		})
	}
}

func TestSuperviseConfig_RefreshWindowStartGEEnd_Rejected(t *testing.T) {
	t.Parallel()
	cases := []string{"10:00-09:00", "09:00-09:00", "23:59-00:01"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			body := withMinimalReplaced(t, `refresh_window = "09:00-10:00"`, `refresh_window = "`+c+`"`)
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrRefreshWindowOrder), "expected ErrRefreshWindowOrder, got %v", err)
			assert.False(t, errors.Is(err, ErrRefreshWindowFormat), "must not also match ErrRefreshWindowFormat")
		})
	}
}

func TestSuperviseConfig_RefreshWindowAccepts_InOrder(t *testing.T) {
	t.Parallel()
	cases := []string{"09:00-10:00", "00:00-23:59", "08:30-08:31"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			body := withMinimalReplaced(t, `refresh_window = "09:00-10:00"`, `refresh_window = "`+c+`"`)
			_, err := loadBody(t, body)
			require.NoError(t, err)
		})
	}
}

// ---- US6: child command ----------------------------------------------------

func TestSuperviseConfig_CommandFirstElementMustBeAbsolute(t *testing.T) {
	t.Parallel()
	cases := [][]string{
		{"my-daemon"},
		{"./run.sh"},
		{"bin/daemon"},
		{"../etc/daemon"},
	}
	for _, c := range cases {
		t.Run(c[0], func(t *testing.T) {
			t.Parallel()
			body := withMinimalReplaced(
				t,
				`command = ["/usr/local/bin/your-daemon-binary", "start"]`,
				`command = ["`+c[0]+`"]`,
			)
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrCommandPathRelative), "expected ErrCommandPathRelative, got %v", err)
		})
	}
}

func TestSuperviseConfig_CommandEmpty_Rejected(t *testing.T) {
	t.Parallel()
	t.Run("explicit_empty", func(t *testing.T) {
		t.Parallel()
		body := withMinimalReplaced(
			t,
			`command = ["/usr/local/bin/your-daemon-binary", "start"]`,
			`command = []`,
		)
		_, err := loadBody(t, body)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrCommandEmpty))
	})
	t.Run("absent_command", func(t *testing.T) {
		t.Parallel()
		body := removeLine(minimalBody(t), "command =")
		_, err := loadBody(t, body)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrMissingRequiredField))
	})
}

func TestSuperviseConfig_CommandAcceptsAbsoluteWithArgs(t *testing.T) {
	t.Parallel()
	body := withMinimalReplaced(
		t,
		`command = ["/usr/local/bin/your-daemon-binary", "start"]`,
		`command = ["/usr/local/bin/your-daemon", "start", "--flag", ""]`,
	)
	s, err := loadBody(t, body)
	require.NoError(t, err)
	assert.Equal(t, []string{"/usr/local/bin/your-daemon", "start", "--flag", ""}, s.Child.Command)
}

// ---- US7: scope ------------------------------------------------------------

func TestSuperviseConfig_ScopeEmpty_Rejected(t *testing.T) {
	t.Parallel()
	body := withMinimalReplaced(
		t,
		`scope = [
  "ANTHROPIC_API_KEY",
]`,
		`scope = []`,
	)
	_, err := loadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrScopeEmpty))
}

func TestSuperviseConfig_ScopeAbsent_RejectedSameSentinel(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/valid_minimal.toml")
	require.NoError(t, err)
	body := string(b)
	// Strip the multi-line scope block.
	idx := strings.Index(body, "scope = [")
	require.GreaterOrEqual(t, idx, 0)
	end := strings.Index(body[idx:], "]")
	require.GreaterOrEqual(t, end, 0)
	body = body[:idx] + body[idx+end+1:]
	_, err = loadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrScopeEmpty))
}

func TestSuperviseConfig_ScopeAccepts_NonEmpty(t *testing.T) {
	t.Parallel()
	_, err := loadBody(t, minimalBody(t))
	require.NoError(t, err)
}

// ---- Cross-cutting validators (Phase 11) ------------------------------------

func TestSuperviseConfig_SessionTypeInvalid_Rejected(t *testing.T) {
	t.Parallel()
	cases := []string{"interactive", "daemon", "", "SUPERVISOR"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			body := withMinimalReplaced(t, `session_type = "supervisor"`, `session_type = "`+c+`"`)
			_, err := loadBody(t, body)
			require.Error(t, err)
			if c == "" {
				// Empty session_type → ErrMissingRequiredField (gate runs first).
				assert.True(t, errors.Is(err, ErrMissingRequiredField))
				return
			}
			assert.True(t, errors.Is(err, ErrSessionTypeInvalid), "expected ErrSessionTypeInvalid, got %v", err)
		})
	}
}

func TestSuperviseConfig_RequestedTTLOver24h_Rejected(t *testing.T) {
	t.Parallel()
	cases := []string{"25h", "48h", "24h1m"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			body := withMinimalReplaced(t, `requested_ttl = "20h"`, `requested_ttl = "`+c+`"`)
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrRequestedTTLOutOfRange), "expected ErrRequestedTTLOutOfRange, got %v", err)
		})
	}
	t.Run("exactly_24h_accepted", func(t *testing.T) {
		t.Parallel()
		body := withMinimalReplaced(t, `requested_ttl = "20h"`, `requested_ttl = "24h"`)
		_, err := loadBody(t, body)
		require.NoError(t, err)
	})
}

// TestRequestedTTLCeiling asserts the ceiling selector returns the ordinary 24h
// bound for a non-standing supervisor and the distinguished standing bound for a
// machine-bound standing lease.
func TestRequestedTTLCeiling(t *testing.T) {
	t.Parallel()
	assert.Equal(t, MaxRequestedTTL, requestedTTLCeiling(false), "ordinary supervisor keeps the 24h ceiling")
	assert.Equal(t, MaxStandingLeaseTTL, requestedTTLCeiling(true), "standing lease may reach the distinguished ceiling")
}

// TestRequestedTTLCeiling_Boundaries proves the ordinary path rejects >24h while
// the standing path accepts a long lease up to MaxStandingLeaseTTL and rejects
// beyond it — the "ordinary capped at 24h, standing may exceed it" invariant.
func TestRequestedTTLCeiling_Boundaries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		standing bool
		ttl      time.Duration
		overCap  bool
	}{
		{"ordinary at 24h accepted", false, 24 * time.Hour, false},
		{"ordinary just over 24h rejected", false, 24*time.Hour + time.Nanosecond, true},
		{"standing at 14d accepted", true, 14 * 24 * time.Hour, false},
		{"standing at ceiling accepted", true, MaxStandingLeaseTTL, false},
		{"standing just over ceiling rejected", true, MaxStandingLeaseTTL + time.Nanosecond, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ceiling := requestedTTLCeiling(tc.standing)
			assert.Equal(t, tc.overCap, tc.ttl > ceiling)
		})
	}
}

func TestSuperviseConfig_LogLevelInvalid_Rejected(t *testing.T) {
	t.Parallel()
	cases := []string{"trace", "verbose", "INFO"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			// Inject log_level into the root section before any [table].
			body := "log_level = \"" + c + "\"\n" + minimalBody(t)
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrLogLevelInvalid), "expected ErrLogLevelInvalid, got %v", err)
		})
	}
}

func TestSuperviseConfig_ServerURLInvalid_Rejected(t *testing.T) {
	t.Parallel()
	cases := []string{"https//bad", "http://", "ftp://1.2.3.4:7743"}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			body := withMinimalReplaced(
				t,
				`server_url = "http://100.96.10.4:7743/h/a8k2f9"`,
				`server_url = "`+c+`"`,
			)
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrServerURLInvalid), "expected ErrServerURLInvalid, got %v", err)
		})
	}
	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		body := withMinimalReplaced(
			t,
			`server_url = "http://100.96.10.4:7743/h/a8k2f9"`,
			`server_url = ""`,
		)
		_, err := loadBody(t, body)
		require.Error(t, err)
		// Empty server_url → ErrMissingRequiredField (gate first).
		assert.True(t, errors.Is(err, ErrMissingRequiredField))
	})
}

func TestSuperviseConfig_WatchdogRateInvalid_Rejected(t *testing.T) {
	t.Parallel()
	cases := []int{0, -1, -100}
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			body := minimalBody(t) + "\n[watchdog]\nmax_alerts_per_hour = " +
				strconv.Itoa(c) + "\n"
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrWatchdogRateInvalid))
		})
	}
}

// ---- Reseal schedule -------------------------------------------------------

func TestSuperviseConfig_ResealValidation(t *testing.T) {
	t.Parallel()
	validReseal := `
[reseal]
timezone = "America/New_York"
daily_time = "03:15"

[reseal.overrides]
monday = "04:30"
sunday = "00:00"
`
	cases := []struct {
		name     string
		reseal   string
		sentinel error
	}{
		{"valid", validReseal, nil},
		{"missing timezone", strings.Replace(validReseal, `timezone = "America/New_York"`+"\n", "", 1), ErrResealTimezoneMissing},
		{"bad timezone", strings.Replace(validReseal, `timezone = "America/New_York"`, `timezone = "Not/AZone"`, 1), ErrResealTimezoneInvalid},
		{"missing daily time", strings.Replace(validReseal, `daily_time = "03:15"`+"\n", "", 1), ErrResealTimeMissing},
		{"bad daily time", strings.Replace(validReseal, `daily_time = "03:15"`, `daily_time = "3:15"`, 1), ErrResealTimeFormat},
		{"bad override time", strings.Replace(validReseal, `monday = "04:30"`, `monday = "24:00"`, 1), ErrResealTimeFormat},
		{"bad weekday key", strings.Replace(validReseal, `monday = "04:30"`, `Monday = "04:30"`, 1), ErrResealWeekdayInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, err := loadBody(t, minimalBody(t)+tc.reseal)
			if tc.sentinel != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.sentinel), "expected %v, got %v", tc.sentinel, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, s.Reseal)
			assert.Equal(t, "America/New_York", s.Reseal.Location.String())
			assert.Equal(t, hhmm{Hour: 3, Minute: 15}, s.Reseal.DailyTime)
			assert.Equal(t, hhmm{Hour: 4, Minute: 30}, s.Reseal.Overrides[time.Monday])
			assert.Equal(t, hhmm{Hour: 0, Minute: 0}, s.Reseal.Overrides[time.Sunday])
		})
	}
}

func TestSuperviseConfig_RejectsInvalidDuration(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		field string
		repl  string
	}{
		{"requested_ttl", `requested_ttl = "20h"`, `requested_ttl = "not-a-duration"`},
		{"refresh_nudge_before", `refresh_window = "09:00-10:00"`, `refresh_window = "09:00-10:00"
refresh_nudge_before = "garbage"`},
		{"boot_retry_timeout", `refresh_window = "09:00-10:00"`, `refresh_window = "09:00-10:00"
boot_retry_timeout = "??"`},
		{"cache_grace_ttl", `refresh_window = "09:00-10:00"`, `refresh_window = "09:00-10:00"
cache_secrets_for_restart = true
cache_grace_ttl = "***"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := withMinimalReplaced(t, tc.field, tc.repl)
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrInvalidDuration), "expected ErrInvalidDuration, got %v", err)
		})
	}
}

// ---- Validate() -------------------------------------------------------------

func TestSupervisor_Validate_NilReceiver(t *testing.T) {
	t.Parallel()
	var s *Supervisor
	assert.Error(t, s.Validate())
}

func TestSupervisor_Validate_RejectsUnpopulated(t *testing.T) {
	t.Parallel()
	s := &Supervisor{}
	err := s.Validate()
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingRequiredField))
}

func TestSupervisor_Validate_AcceptsPopulated(t *testing.T) {
	t.Parallel()
	p := writeConfigForValidate(t)
	s, err := Load(context.Background(), p)
	require.NoError(t, err)
	require.NoError(t, s.Validate())
}

// TestSupervisor_Validate_DetectsAllRulesIndependently constructs a
// *Supervisor in code (bypassing Load) and asserts each Validate branch
// fires for the appropriate violation. This covers Validate() branches that
// Load short-circuits before they can be reached via the file path.
func TestSupervisor_Validate_DetectsAllRulesIndependently(t *testing.T) {
	t.Parallel()
	base := func() *Supervisor {
		return &Supervisor{
			Name:               "x",
			Reason:             "y",
			ServerURL:          "http://100.96.10.4:7743/h/abc",
			ClientMachineIndex: 1,
			SessionType:        "supervisor",
			RequestedTTL:       20 * 60 * 60 * 1e9, // 20h in ns
			RefreshWindow:      "09:00-10:00",
			StatusSocket:       "/tmp/x.sock",
			PIDFile:            "/tmp/x.pid",
			LogLevel:           "info",
			Scope:              []string{"X"},
			Child: Child{
				Command:  []string{"/bin/x"},
				Shutdown: ChildShutdown{Grace: DefaultShutdownGrace},
			},
			Validators: map[string]Validator{"X": "anthropic"},
			Watchdog:   Watchdog{MaxAlertsPerHour: 6},
		}
	}
	require.NoError(t, base().Validate())

	cases := []struct {
		name     string
		mutate   func(*Supervisor)
		sentinel error
	}{
		{"requested_ttl over cap", func(s *Supervisor) { s.RequestedTTL = MaxRequestedTTL + 1 }, ErrRequestedTTLOutOfRange},
		{"refresh_window bad", func(s *Supervisor) { s.RefreshWindow = "bad" }, ErrRefreshWindowFormat},
		{"grace ttl without cache", func(s *Supervisor) { s.CacheSecretsForRestart = false; s.CacheGraceTTL = 1 }, ErrGraceTTLWithoutCache},
		{"grace window too long", func(s *Supervisor) { s.CacheSecretsForRestart = true; s.CacheGraceTTL = MaxGraceWindow + 1 }, ErrGraceWindowTooLong},
		{"missing status_socket", func(s *Supervisor) { s.StatusSocket = "" }, ErrMissingRequiredField},
		{"missing pid_file", func(s *Supervisor) { s.PIDFile = "" }, ErrMissingRequiredField},
		{"log level invalid", func(s *Supervisor) { s.LogLevel = "bogus" }, ErrLogLevelInvalid},
		{"scope empty", func(s *Supervisor) { s.Scope = nil }, ErrScopeEmpty},
		{"command empty", func(s *Supervisor) { s.Child.Command = nil }, ErrCommandEmpty},
		{"command relative", func(s *Supervisor) { s.Child.Command = []string{"daemon"} }, ErrCommandPathRelative},
		{"unknown validator", func(s *Supervisor) { s.Validators = map[string]Validator{"X": "slack"} }, ErrUnknownValidator},
		{"watchdog rate <=0", func(s *Supervisor) { s.Watchdog.MaxAlertsPerHour = 0 }, ErrWatchdogRateInvalid},
		{"session type invalid", func(s *Supervisor) { s.SessionType = "interactive" }, ErrSessionTypeInvalid},
		{"server url invalid", func(s *Supervisor) { s.ServerURL = "ftp://bad" }, ErrServerURLInvalid},
		{"missing name", func(s *Supervisor) { s.Name = "" }, ErrMissingRequiredField},
		{"missing reason", func(s *Supervisor) { s.Reason = "" }, ErrMissingRequiredField},
		{"reseal timezone missing", func(s *Supervisor) { s.Reseal = &ResealSchedule{} }, ErrResealTimezoneMissing},
		{"reseal daily time invalid", func(s *Supervisor) {
			s.Reseal = &ResealSchedule{Location: time.UTC, DailyTime: hhmm{Hour: 24}}
		}, ErrResealTimeFormat},
		{"reseal weekday invalid", func(s *Supervisor) {
			s.Reseal = &ResealSchedule{Location: time.UTC, Overrides: map[time.Weekday]hhmm{time.Weekday(7): {}}}
		}, ErrResealWeekdayInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := base()
			tc.mutate(s)
			err := s.Validate()
			require.Error(t, err)
			assert.True(t, errors.Is(err, tc.sentinel), "expected %v, got %v", tc.sentinel, err)
		})
	}
}

// ---- Coverage: error paths that Load can hit only via env / fs failures ----

func TestExpandHome_TildeOnly(t *testing.T) {
	// Not Parallel: overrides package-level userHomeDir.
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	home := t.TempDir()
	userHomeDir = func() (string, error) { return home, nil }

	out, err := expandHome("~")
	require.NoError(t, err)
	assert.Equal(t, home, out)
}

func TestExpandHome_Error_BareTilde(t *testing.T) {
	// Not Parallel: overrides package-level userHomeDir.
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	userHomeDir = func() (string, error) { return "", errors.New("no home") }

	_, err := expandHome("~")
	require.Error(t, err)
}

func TestExpandHome_Error_TildeSlash(t *testing.T) {
	// Not Parallel: overrides package-level userHomeDir.
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	userHomeDir = func() (string, error) { return "", errors.New("no home") }

	_, err := expandHome("~/x")
	require.Error(t, err)
}

func TestLoad_StatusSocketExpandError(t *testing.T) {
	// Not Parallel: overrides package-level userHomeDir.
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	userHomeDir = func() (string, error) { return "", errors.New("no home") }

	body := strings.Replace(minimalBody(t),
		`status_socket = "/tmp/hush/supervise-example-daemon.sock"`,
		`status_socket = "~/x.sock"`, 1)
	_, err := loadBody(t, body)
	require.Error(t, err)
}

func TestLoad_PIDFileExpandError(t *testing.T) {
	// Not Parallel: overrides package-level userHomeDir.
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	failed := false
	userHomeDir = func() (string, error) {
		// Status socket asks first; let it succeed once, then fail for pid.
		if failed {
			return "", errors.New("no home")
		}
		failed = true
		return "/tmp", nil
	}

	body := strings.Replace(minimalBody(t),
		`status_socket = "/tmp/hush/supervise-example-daemon.sock"`,
		`status_socket = "~/x.sock"`, 1)
	body = strings.Replace(body,
		`pid_file = "/tmp/hush/supervise-example-daemon.pid"`,
		`pid_file = "~/x.pid"`, 1)
	_, err := loadBody(t, body)
	require.Error(t, err)
}

func TestLoad_ChildWorkingDirExpandError(t *testing.T) {
	// Not Parallel: overrides package-level userHomeDir.
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	calls := 0
	userHomeDir = func() (string, error) {
		calls++
		if calls >= 3 { // fail on the 3rd call (working_dir)
			return "", errors.New("no home")
		}
		return "/tmp", nil
	}

	body := strings.Replace(minimalBody(t),
		`status_socket = "/tmp/hush/supervise-example-daemon.sock"`,
		`status_socket = "~/x.sock"`, 1)
	body = strings.Replace(body,
		`pid_file = "/tmp/hush/supervise-example-daemon.pid"`,
		`pid_file = "~/x.pid"`, 1)
	body = strings.Replace(body, `working_dir = "/tmp"`, `working_dir = "~/work"`, 1)
	_, err := loadBody(t, body)
	require.Error(t, err)
}

func TestLoad_ChildEnvDecodes(t *testing.T) {
	t.Parallel()
	body := strings.Replace(minimalBody(t),
		`env_passthrough = ["PATH"]`+"\n",
		`env_passthrough = ["PATH"]`+"\n\n[child.env]\nDAEMON_LAUNCHD_LABEL = \"ai.example.daemon\"\nNODE_USE_SYSTEM_CA = \"1\"\n", 1)
	s, err := loadBody(t, body)
	require.NoError(t, err)
	require.NotNil(t, s.Child.Env)
	assert.Equal(t, "ai.example.daemon", s.Child.Env["DAEMON_LAUNCHD_LABEL"])
	assert.Equal(t, "1", s.Child.Env["NODE_USE_SYSTEM_CA"])
}

func TestLoad_ChildEnvAbsentLeavesNil(t *testing.T) {
	t.Parallel()
	s, err := loadBody(t, minimalBody(t))
	require.NoError(t, err)
	assert.Nil(t, s.Child.Env, "absent [child.env] should leave Env nil so callers can detect 'not configured'")
}

func TestLoad_ChildStdoutStderrPathDecodes(t *testing.T) {
	t.Parallel()
	body := strings.Replace(minimalBody(t),
		`env_passthrough = ["PATH"]`,
		`env_passthrough = ["PATH"]`+"\n"+`stdout_path = "/tmp/child.out.log"`+"\n"+`stderr_path = "/tmp/child.err.log"`, 1)
	s, err := loadBody(t, body)
	require.NoError(t, err)
	assert.Equal(t, "/tmp/child.out.log", s.Child.StdoutPath)
	assert.Equal(t, "/tmp/child.err.log", s.Child.StderrPath)
}

func TestLoad_ChildStdoutStderrPathDefaultsToEmpty(t *testing.T) {
	t.Parallel()
	s, err := loadBody(t, minimalBody(t))
	require.NoError(t, err)
	assert.Empty(t, s.Child.StdoutPath)
	assert.Empty(t, s.Child.StderrPath)
}

func TestLoad_ChildStdoutPathExpandsHome(t *testing.T) {
	// Not Parallel: overrides package-level userHomeDir.
	prev := userHomeDir
	t.Cleanup(func() { userHomeDir = prev })
	userHomeDir = func() (string, error) { return "/Users/example", nil }
	body := strings.Replace(minimalBody(t),
		`env_passthrough = ["PATH"]`,
		`env_passthrough = ["PATH"]`+"\n"+`stdout_path = "~/logs/child.out.log"`, 1)
	s, err := loadBody(t, body)
	require.NoError(t, err)
	assert.Equal(t, "/Users/example/logs/child.out.log", s.Child.StdoutPath)
}

func TestLoad_EnvPassthroughDefaultsToEmpty(t *testing.T) {
	t.Parallel()
	body := strings.Replace(minimalBody(t),
		`env_passthrough = ["PATH"]`+"\n", "", 1)
	s, err := loadBody(t, body)
	require.NoError(t, err)
	assert.NotNil(t, s.Child.EnvPassthrough)
	assert.Empty(t, s.Child.EnvPassthrough)
}

func TestLoad_MissingValidatorsTableDefaultsToNoop(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/valid_minimal.toml")
	require.NoError(t, err)
	body := string(b)
	idx := strings.Index(body, "[validators]")
	require.GreaterOrEqual(t, idx, 0)
	body = body[:idx]
	s, err := loadBody(t, body)
	require.NoError(t, err)
	assert.Empty(t, s.Validators)
}

func TestValidateServerURL_ParseError(t *testing.T) {
	t.Parallel()
	// A URL with an unprintable control character forces url.Parse to error.
	err := validateServerURL("http://bad host\x7f/")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrServerURLInvalid))
}

// TestLoad_StatusSocketRejectsNonCleanPath asserts that absPath refuses
// paths whose lexical form contains ".." components or other non-canonical
// elements (defense-in-depth — operator typo guard).
func TestLoad_StatusSocketRejectsNonCleanPath(t *testing.T) {
	t.Parallel()
	body := strings.Replace(minimalBody(t),
		`status_socket = "/tmp/hush/supervise-example-daemon.sock"`,
		`status_socket = "/tmp/hush/../etc/passwd"`, 1)
	_, err := loadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPathNotClean), "want ErrPathNotClean, got %v", err)
}

// TestLoad_PIDFileRejectsNonCleanPath same as above for pid_file.
func TestLoad_PIDFileRejectsNonCleanPath(t *testing.T) {
	t.Parallel()
	body := strings.Replace(minimalBody(t),
		`pid_file = "/tmp/hush/supervise-example-daemon.pid"`,
		`pid_file = "/var/run/hush/../../etc/passwd.pid"`, 1)
	_, err := loadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPathNotClean), "want ErrPathNotClean, got %v", err)
}

// TestLoad_BootRetryTimeoutCap asserts that boot_retry_timeout > 1h is
// rejected (operator typo guard: 100h would silently disable boot timeout).
func TestLoad_BootRetryTimeoutCap(t *testing.T) {
	t.Parallel()
	body := "boot_retry_timeout = \"100h\"\n" + minimalBody(t)
	_, err := loadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBootRetryTimeoutTooLong), "want ErrBootRetryTimeoutTooLong, got %v", err)
}

// TestLoad_BootRetryTimeoutAtCap asserts the boundary: exactly 1h is accepted.
func TestLoad_BootRetryTimeoutAtCap(t *testing.T) {
	t.Parallel()
	body := "boot_retry_timeout = \"1h\"\n" + minimalBody(t)
	s, err := loadBody(t, body)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, s.BootRetryTimeout)
}

// TestLoad_RefreshNudgeBeforeCap asserts that refresh_nudge_before > 6h is
// rejected.
func TestLoad_RefreshNudgeBeforeCap(t *testing.T) {
	t.Parallel()
	body := "refresh_nudge_before = \"7h\"\n" + minimalBody(t)
	_, err := loadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrRefreshNudgeBeforeTooLong), "want ErrRefreshNudgeBeforeTooLong, got %v", err)
}

// TestAbsPath_CleanPathAccepted asserts the happy path: already-clean paths
// pass through unchanged.
func TestAbsPath_CleanPathAccepted(t *testing.T) {
	t.Parallel()
	out, err := absPath("/tmp/hush/x.sock")
	require.NoError(t, err)
	assert.Equal(t, "/tmp/hush/x.sock", out)
}

// writeConfigForValidate creates a minimal valid config file for Validate
// round-trip testing.
func writeConfigForValidate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "supervise.toml")
	b, err := os.ReadFile("testdata/valid_minimal.toml")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, b, 0o600))
	return p
}
