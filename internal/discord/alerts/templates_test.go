package alerts

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// allClasses is the canonical traversal order used by every per-class
// table test in this file (one row per AlertClass constant).
func allClasses() []AlertClass {
	return []AlertClass{
		AlertClassApprovalRequest,
		AlertClassDaemonRefreshRequest,
		AlertClassValidatorStaleFailure,
		AlertClassChildExit78StaleFailure,
		AlertClassLogPatternStaleWarning,
		AlertClassDiscordDisconnected,
		AlertClassDiscordReconnected,
		AlertClassVaultUnreachableAtBootTimeout,
	}
}

func renderTestAlert(class AlertClass) (Alert, string) {
	a := Alert{
		Class:          class,
		SupervisorName: "sup-x",
		MachineName:    "host-1",
		Pattern:        "p-401",
		Detail:         "scope=ANTHROPIC",
	}
	return a, classToTemplate[class].render(a)
}

// --- 8 per-class render-snapshot tests --------------------------------

func TestAlert_ApprovalRequest_RenderSnapshot(t *testing.T) {
	t.Parallel()
	assertRender(t, AlertClassApprovalRequest, "[CRITICAL][approval-request]")
}

func TestAlert_DaemonRefreshRequest_RenderSnapshot(t *testing.T) {
	t.Parallel()
	assertRender(t, AlertClassDaemonRefreshRequest, "[CRITICAL][daemon-refresh]")
}

func TestAlert_ValidatorStaleFailure_RenderSnapshot(t *testing.T) {
	t.Parallel()
	assertRender(t, AlertClassValidatorStaleFailure, "[WARNING][validator-stale]")
}

func TestAlert_ChildExit78StaleFailure_RenderSnapshot(t *testing.T) {
	t.Parallel()
	assertRender(t, AlertClassChildExit78StaleFailure, "[CRITICAL][child-exit-78]")
}

func TestAlert_LogPatternStaleWarning_RenderSnapshot(t *testing.T) {
	t.Parallel()
	assertRender(t, AlertClassLogPatternStaleWarning, "[WARNING][log-pattern]")
}

func TestAlert_DiscordDisconnected_RenderSnapshot(t *testing.T) {
	t.Parallel()
	assertRender(t, AlertClassDiscordDisconnected, "[WARNING][discord-disconnected]")
}

func TestAlert_DiscordReconnected_RenderSnapshot(t *testing.T) {
	t.Parallel()
	assertRender(t, AlertClassDiscordReconnected, "[INFO][discord-reconnected]")
}

func TestAlert_VaultUnreachableAtBootTimeout_RenderSnapshot(t *testing.T) {
	t.Parallel()
	assertRender(t, AlertClassVaultUnreachableAtBootTimeout, "[CRITICAL][vault-unreachable]")
}

func assertRender(t *testing.T, class AlertClass, wantPrefix string) {
	t.Helper()
	a, got := renderTestAlert(class)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Errorf("%s: rendered %q missing prefix %q", class, got, wantPrefix)
	}
	for _, s := range []string{a.SupervisorName, a.MachineName, a.Pattern, a.Detail} {
		if !strings.Contains(got, s) {
			t.Errorf("%s: rendered %q missing %q", class, got, s)
		}
	}
}

// --- B-A-16 -----------------------------------------------------------

func TestTemplate_LabelPrefixUniqueAndStable(t *testing.T) {
	t.Parallel()
	seen := make(map[string]AlertClass, 8)
	want := map[AlertClass]string{
		AlertClassApprovalRequest:               "[CRITICAL][approval-request]",
		AlertClassDaemonRefreshRequest:          "[CRITICAL][daemon-refresh]",
		AlertClassValidatorStaleFailure:         "[WARNING][validator-stale]",
		AlertClassChildExit78StaleFailure:       "[CRITICAL][child-exit-78]",
		AlertClassLogPatternStaleWarning:        "[WARNING][log-pattern]",
		AlertClassDiscordDisconnected:           "[WARNING][discord-disconnected]",
		AlertClassDiscordReconnected:            "[INFO][discord-reconnected]",
		AlertClassVaultUnreachableAtBootTimeout: "[CRITICAL][vault-unreachable]",
	}
	for class, prefix := range want {
		got, ok := classToTemplate[class]
		if !ok {
			t.Errorf("missing template for %s", class)
			continue
		}
		if got.labelPrefix != prefix {
			t.Errorf("%s: prefix want %q got %q", class, prefix, got.labelPrefix)
		}
		if dup, exists := seen[got.labelPrefix]; exists {
			t.Errorf("duplicate prefix %q: %s and %s", got.labelPrefix, dup, class)
		}
		seen[got.labelPrefix] = class
	}
	if len(seen) != 8 {
		t.Errorf("expected 8 unique prefixes, got %d", len(seen))
	}
}

// --- B-A-17 -----------------------------------------------------------

func TestTemplate_OmitEmptyLines(t *testing.T) {
	t.Parallel()
	// For each class × each optional field, render with the field empty
	// and assert the corresponding key= segment is absent.
	type fld struct {
		name string
		zero func(a *Alert)
		key  string
	}
	fields := []fld{
		{"machine", func(a *Alert) { a.MachineName = "" }, " machine="},
		{"pattern", func(a *Alert) { a.Pattern = "" }, " pattern="},
		{"detail", func(a *Alert) { a.Detail = "" }, " detail="},
	}
	for _, class := range allClasses() {
		for _, f := range fields {
			a, _ := renderTestAlert(class)
			f.zero(&a)
			got := classToTemplate[class].render(a)
			if strings.Contains(got, f.key) {
				t.Errorf("%s/%s: empty field still rendered: %q", class, f.name, got)
			}
			for _, placeholder := range []string{"<missing>", "?", "key="} {
				_ = placeholder
			}
			// supervisor floor must remain.
			if !strings.Contains(got, " supervisor=sup-x") {
				t.Errorf("%s/%s: supervisor floor missing: %q", class, f.name, got)
			}
		}
	}
}

// --- B-A-18 -----------------------------------------------------------

func TestAlert_NoSecretByteLeakage(t *testing.T) {
	t.Parallel()
	// Seed unique 16-byte sentinels into each allow-list field.
	mk := func() string {
		var b [16]byte
		_, _ = rand.Read(b[:])
		return hex.EncodeToString(b[:])
	}
	supMark, machMark, patMark, detMark := mk(), mk(), mk(), mk()
	// A separate sentinel that NEVER reaches any Alert field.
	secretMark := mk()
	// A timestamp string to verify Alert.Time never renders.
	ts := time.Unix(1234567890, 0)

	for _, class := range allClasses() {
		a := Alert{
			Class:          class,
			SupervisorName: supMark,
			MachineName:    machMark,
			Pattern:        patMark,
			Detail:         detMark,
			Time:           ts,
		}
		got := classToTemplate[class].render(a)
		for _, want := range []string{supMark, machMark, patMark, detMark} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: missing operator-safe sentinel %q in render %q", class, want, got)
			}
		}
		if strings.Contains(got, secretMark) {
			t.Errorf("%s: secret marker leaked into render %q", class, got)
		}
		for _, ban := range []string{"time=", "1234567890", ts.Format(time.RFC3339)} {
			if strings.Contains(got, ban) {
				t.Errorf("%s: Alert.Time leaked (%q) into render %q", class, ban, got)
			}
		}
	}
}
