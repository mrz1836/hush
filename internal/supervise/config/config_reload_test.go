package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reloadMinimalBody returns testdata/valid_reload_minimal.toml.
func reloadMinimalBody(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/valid_reload_minimal.toml")
	require.NoError(t, err)
	return string(b)
}

// loadReloadBody writes body to a temp file and Loads it.
func loadReloadBody(t *testing.T, body string) (*Supervisor, error) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "supervise.toml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return Load(context.Background(), p)
}

// ---- Defaults for the new sections -----------------------------------------

func TestSuperviseConfig_DefaultShutdownGrace(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 30*time.Second, DefaultShutdownGrace)
}

func TestSuperviseConfig_DefaultReadinessTimeoutAndInterval(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 30*time.Second, DefaultReadinessTimeout)
	assert.Equal(t, 200*time.Millisecond, DefaultReadinessInterval)
}

func TestSuperviseConfig_HandoffModeConstant(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "http-proxy", HandoffModeHTTPProxy)
}

func TestSuperviseConfig_EnvVarBindPortConstant(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "HUSH_BIND_PORT", EnvVarBindPort)
}

// ---- Shutdown defaulting for plain (non-reload) configs ---------------------

func TestSuperviseConfig_ShutdownAbsent_AppliesDefaultGrace(t *testing.T) {
	t.Parallel()
	// minimal config has no [child.shutdown] table — Grace should still
	// materialize to DefaultShutdownGrace so SIGTERM/SIGKILL timing is
	// well-defined for every supervisor config.
	s, err := loadBody(t, minimalBody(t))
	require.NoError(t, err)
	assert.Equal(t, DefaultShutdownGrace, s.Child.Shutdown.Grace)
}

func TestSuperviseConfig_ShutdownGracePresent_Honored(t *testing.T) {
	t.Parallel()
	body := minimalBody(t) + "\n[child.shutdown]\ngrace = \"45s\"\n"
	s, err := loadBody(t, body)
	require.NoError(t, err)
	assert.Equal(t, 45*time.Second, s.Child.Shutdown.Grace)
}

func TestSuperviseConfig_ShutdownGraceNonPositive_Rejected(t *testing.T) {
	t.Parallel()
	for _, g := range []string{"0s", "-5s"} {
		t.Run(g, func(t *testing.T) {
			t.Parallel()
			body := minimalBody(t) + "\n[child.shutdown]\ngrace = \"" + g + "\"\n"
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrShutdownGraceInvalid), "want ErrShutdownGraceInvalid, got %v", err)
		})
	}
}

func TestSuperviseConfig_ShutdownGraceInvalidDuration_Rejected(t *testing.T) {
	t.Parallel()
	body := minimalBody(t) + "\n[child.shutdown]\ngrace = \"not-a-duration\"\n"
	_, err := loadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidDuration), "want ErrInvalidDuration, got %v", err)
}

// ---- Readiness on its own (no handoff) -------------------------------------

func TestSuperviseConfig_ReadinessAbsent_LeavesNil(t *testing.T) {
	t.Parallel()
	s, err := loadBody(t, minimalBody(t))
	require.NoError(t, err)
	assert.Nil(t, s.Child.Readiness, "absent [child.readiness] should leave Readiness nil")
}

func TestSuperviseConfig_ReadinessPresentWithoutHandoff_Accepted(t *testing.T) {
	t.Parallel()
	// Readiness can be configured even without reload handoff (e.g. for
	// future health monitoring); validation must not require handoff in
	// ordinary supervisor configs.
	body := minimalBody(t) + "\n[child.readiness]\nhttp_url = \"http://127.0.0.1:9000/health\"\n"
	s, err := loadBody(t, body)
	require.NoError(t, err)
	require.NotNil(t, s.Child.Readiness)
	assert.Equal(t, "http://127.0.0.1:9000/health", s.Child.Readiness.HTTPURL)
	assert.Equal(t, DefaultReadinessTimeout, s.Child.Readiness.Timeout)
	assert.Equal(t, DefaultReadinessInterval, s.Child.Readiness.Interval)
}

func TestSuperviseConfig_ReadinessMissingURL_Rejected(t *testing.T) {
	t.Parallel()
	body := minimalBody(t) + "\n[child.readiness]\ntimeout = \"30s\"\n"
	_, err := loadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingRequiredField), "want ErrMissingRequiredField, got %v", err)
	assert.Contains(t, err.Error(), "child.readiness.http_url")
}

func TestSuperviseConfig_ReadinessURLInvalid_Rejected(t *testing.T) {
	t.Parallel()
	cases := []string{
		"ftp://1.2.3.4/health",
		"tcp://127.0.0.1:8080",
		"://no-scheme",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Parallel()
			body := minimalBody(t) + "\n[child.readiness]\nhttp_url = \"" + c + "\"\n"
			_, err := loadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrReadinessURLInvalid), "want ErrReadinessURLInvalid, got %v", err)
		})
	}
}

func TestSuperviseConfig_ReadinessDurationsNonPositive_Rejected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{"zero_timeout", "\n[child.readiness]\nhttp_url = \"http://127.0.0.1/h\"\ntimeout = \"0s\"\n"},
		{"negative_timeout", "\n[child.readiness]\nhttp_url = \"http://127.0.0.1/h\"\ntimeout = \"-1s\"\n"},
		{"zero_interval", "\n[child.readiness]\nhttp_url = \"http://127.0.0.1/h\"\ninterval = \"0s\"\n"},
		{"negative_interval", "\n[child.readiness]\nhttp_url = \"http://127.0.0.1/h\"\ninterval = \"-1ms\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadBody(t, minimalBody(t)+tc.body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrReadinessDurationInvalid), "want ErrReadinessDurationInvalid, got %v", err)
		})
	}
}

// ---- Reload-eligible (handoff) happy path ----------------------------------

func TestSuperviseConfig_ReloadMinimal_LoadsAndPopulates(t *testing.T) {
	t.Parallel()
	s, err := loadReloadBody(t, reloadMinimalBody(t))
	require.NoError(t, err)
	require.NotNil(t, s.Child.Readiness)
	require.NotNil(t, s.Child.Handoff)
	assert.Equal(t, "http://127.0.0.1:0/health", s.Child.Readiness.HTTPURL)
	assert.Equal(t, 30*time.Second, s.Child.Readiness.Timeout)
	assert.Equal(t, 200*time.Millisecond, s.Child.Readiness.Interval)
	assert.Equal(t, 30*time.Second, s.Child.Shutdown.Grace)
	assert.Equal(t, HandoffModeHTTPProxy, s.Child.Handoff.Mode)
	assert.Equal(t, "127.0.0.1:8080", s.Child.Handoff.ListenAddr)
}

func TestSuperviseConfig_ReloadMinimal_ValidateRoundTrip(t *testing.T) {
	t.Parallel()
	s, err := loadReloadBody(t, reloadMinimalBody(t))
	require.NoError(t, err)
	require.NoError(t, s.Validate())
}

// ---- Reload eligibility refusal paths (AC-8) -------------------------------

func TestSuperviseConfig_HandoffWithoutReadiness_Rejected(t *testing.T) {
	t.Parallel()
	// Strip the [child.readiness] block from the reload-minimal config.
	body := reloadMinimalBody(t)
	idx := strings.Index(body, "[child.readiness]")
	require.GreaterOrEqual(t, idx, 0)
	end := strings.Index(body[idx:], "[child.shutdown]")
	require.Greater(t, end, 0)
	body = body[:idx] + body[idx+end:]
	_, err := loadReloadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrHandoffRequiresReadiness), "want ErrHandoffRequiresReadiness, got %v", err)
}

func TestSuperviseConfig_HandoffBadMode_Rejected(t *testing.T) {
	t.Parallel()
	for _, m := range []string{"socket-activation", "sequential-rebind", "HTTP-PROXY", "tcp-proxy"} {
		t.Run(m, func(t *testing.T) {
			t.Parallel()
			body := strings.Replace(reloadMinimalBody(t),
				`mode = "http-proxy"`,
				`mode = "`+m+`"`, 1)
			_, err := loadReloadBody(t, body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrHandoffModeInvalid), "want ErrHandoffModeInvalid, got %v", err)
		})
	}
}

func TestSuperviseConfig_HandoffEmptyMode_Rejected(t *testing.T) {
	t.Parallel()
	body := strings.Replace(reloadMinimalBody(t),
		`mode = "http-proxy"`,
		`mode = ""`, 1)
	_, err := loadReloadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingRequiredField), "want ErrMissingRequiredField, got %v", err)
	assert.Contains(t, err.Error(), "child.handoff.mode")
}

func TestSuperviseConfig_HandoffEmptyListenAddr_Rejected(t *testing.T) {
	t.Parallel()
	body := strings.Replace(reloadMinimalBody(t),
		`listen_addr = "127.0.0.1:8080"`,
		`listen_addr = ""`, 1)
	_, err := loadReloadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMissingRequiredField), "want ErrMissingRequiredField, got %v", err)
	assert.Contains(t, err.Error(), "child.handoff.listen_addr")
}

func TestSuperviseConfig_HandoffWithoutBindPortReference_Rejected(t *testing.T) {
	t.Parallel()
	// Replace the command line so it no longer mentions HUSH_BIND_PORT.
	body := strings.Replace(reloadMinimalBody(t),
		`command = ["/usr/local/bin/your-daemon-binary", "start", "--port=$HUSH_BIND_PORT"]`,
		`command = ["/usr/local/bin/your-daemon-binary", "start"]`, 1)
	_, err := loadReloadBody(t, body)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrHandoffRequiresBindPortRef), "want ErrHandoffRequiresBindPortRef, got %v", err)
}

// ---- HUSH_BIND_PORT reference detection across surfaces --------------------

func TestSuperviseConfig_BindPortReferenceViaChildEnv_Accepted(t *testing.T) {
	t.Parallel()
	// Strip the command's HUSH_BIND_PORT and provide it via [child.env].
	body := strings.Replace(reloadMinimalBody(t),
		`command = ["/usr/local/bin/your-daemon-binary", "start", "--port=$HUSH_BIND_PORT"]`,
		`command = ["/usr/local/bin/your-daemon-binary", "start"]`, 1)
	body += "\n[child.env]\nPORT = \"$HUSH_BIND_PORT\"\n"
	s, err := loadReloadBody(t, body)
	require.NoError(t, err)
	require.NotNil(t, s.Child.Handoff)
}

func TestSuperviseConfig_BindPortReferenceViaEnvPassthrough_Accepted(t *testing.T) {
	t.Parallel()
	body := strings.Replace(reloadMinimalBody(t),
		`command = ["/usr/local/bin/your-daemon-binary", "start", "--port=$HUSH_BIND_PORT"]`,
		`command = ["/usr/local/bin/your-daemon-binary", "start"]`, 1)
	body = strings.Replace(body,
		`env_passthrough = ["PATH"]`,
		`env_passthrough = ["PATH", "HUSH_BIND_PORT"]`, 1)
	s, err := loadReloadBody(t, body)
	require.NoError(t, err)
	require.NotNil(t, s.Child.Handoff)
}

// ---- Validate() coverage for the new rules ---------------------------------

func TestSupervisor_Validate_ReloadEligibility(t *testing.T) {
	t.Parallel()
	base := func() *Supervisor {
		return &Supervisor{
			Name:               "x",
			Reason:             "y",
			ServerURL:          "http://100.96.10.4:7743/h/abc",
			ClientMachineIndex: 1,
			SessionType:        "supervisor",
			RequestedTTL:       20 * 60 * 60 * 1e9, // 20h
			RefreshWindow:      "09:00-10:00",
			StatusSocket:       "/tmp/x.sock",
			PIDFile:            "/tmp/x.pid",
			LogLevel:           "info",
			Scope:              []string{"X"},
			Child: Child{
				Command: []string{"/bin/x", "--port=$HUSH_BIND_PORT"},
				Readiness: &ChildReadiness{
					HTTPURL:  "http://127.0.0.1/h",
					Timeout:  30 * time.Second,
					Interval: 200 * time.Millisecond,
				},
				Shutdown: ChildShutdown{Grace: DefaultShutdownGrace},
				Handoff:  &ChildHandoff{Mode: HandoffModeHTTPProxy, ListenAddr: "127.0.0.1:8080"},
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
		{"handoff bad mode", func(s *Supervisor) { s.Child.Handoff.Mode = "sequential-rebind" }, ErrHandoffModeInvalid},
		{"handoff empty listen", func(s *Supervisor) { s.Child.Handoff.ListenAddr = "" }, ErrMissingRequiredField},
		{"handoff without readiness", func(s *Supervisor) { s.Child.Readiness = nil }, ErrHandoffRequiresReadiness},
		{"handoff without bindport", func(s *Supervisor) { s.Child.Command = []string{"/bin/x"} }, ErrHandoffRequiresBindPortRef},
		{"readiness bad url", func(s *Supervisor) { s.Child.Readiness.HTTPURL = "ftp://bad" }, ErrReadinessURLInvalid},
		{"readiness zero timeout", func(s *Supervisor) { s.Child.Readiness.Timeout = 0 }, ErrReadinessDurationInvalid},
		{"readiness zero interval", func(s *Supervisor) { s.Child.Readiness.Interval = 0 }, ErrReadinessDurationInvalid},
		{"shutdown grace zero", func(s *Supervisor) { s.Child.Shutdown.Grace = 0 }, ErrShutdownGraceInvalid},
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

// ---- Strict-decode of new subtable rejects unknown fields ------------------

func TestSuperviseConfig_RejectsUnknownFieldInReloadSubtables(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{"readiness", reloadMinimalBody(t) + "\n[child.readiness.extra]\nbogus = 1\n"},
		{"shutdown", strings.Replace(reloadMinimalBody(t), "[child.shutdown]\ngrace = \"30s\"", "[child.shutdown]\ngrace = \"30s\"\nbogus = 1", 1)},
		{"handoff", strings.Replace(reloadMinimalBody(t), `mode = "http-proxy"`, `mode = "http-proxy"`+"\nbogus = 1", 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadReloadBody(t, tc.body)
			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrUnknownField), "want ErrUnknownField, got %v", err)
		})
	}
}
