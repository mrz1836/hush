package testutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode"
)

func TestSentinelSecret_FormatStability(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "SECRET_SHOULD_NEVER_APPEAR_0"},
		{7, "SECRET_SHOULD_NEVER_APPEAR_7"},
		{123, "SECRET_SHOULD_NEVER_APPEAR_123"},
	}
	for _, tc := range cases {
		got := SentinelSecret(tc.n)
		if got != tc.want {
			t.Errorf("SentinelSecret(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestSentinelSecret_Uniqueness(t *testing.T) {
	seen := map[string]struct{}{}
	for i := range 100 {
		s := SentinelSecret(i)
		if _, dup := seen[s]; dup {
			t.Errorf("SentinelSecret(%d) produced duplicate: %q", i, s)
		}
		seen[s] = struct{}{}
	}
}

func TestSentinelSecret_NoWhitespacePunct(t *testing.T) {
	s := SentinelSecret(42)
	for _, r := range s {
		if unicode.IsSpace(r) {
			t.Errorf("SentinelSecret(42) contains whitespace rune %q in %q", r, s)
		}
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			t.Errorf("SentinelSecret(42) contains disallowed rune %q in %q", r, s)
		}
	}
}

func TestSentinelSecret_EdgeIndices(t *testing.T) {
	if got := SentinelSecret(0); got != "SECRET_SHOULD_NEVER_APPEAR_0" {
		t.Errorf("SentinelSecret(0) = %q, want SECRET_SHOULD_NEVER_APPEAR_0", got)
	}
	if got := SentinelSecret(-1); got != "SECRET_SHOULD_NEVER_APPEAR_-1" {
		t.Errorf("SentinelSecret(-1) = %q, want SECRET_SHOULD_NEVER_APPEAR_-1", got)
	}
}

func TestAssertSentinelAbsent_Absent(t *testing.T) {
	sentinel := SentinelSecret(99)
	t.Run("subtest", func(t *testing.T) {
		AssertSentinelAbsent(t, sentinel, "no leak here")
		if t.Failed() {
			t.Errorf("AssertSentinelAbsent should not have failed when sentinel is absent")
		}
	})
}

// TestAssertSentinelAbsent_Present uses subprocess testing because calling
// AssertSentinelAbsent with a present sentinel marks the test handle as failed,
// and Go propagates subtest failures to the parent — the only safe way to
// observe this behavior without corrupting the outer suite is a subprocess.
func TestAssertSentinelAbsent_Present(t *testing.T) {
	const envKey = "HUSH_TESTUTIL_SENTINEL_INNER"
	if os.Getenv(envKey) == "1" {
		sentinel := SentinelSecret(42)
		AssertSentinelAbsent(t, sentinel, "oops "+sentinel+" leaked")
		return
	}

	//nolint:gosec // subprocess test pattern: os.Args[0] is the compiled test binary, not external user input
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAssertSentinelAbsent_Present$", "-test.v")
	cmd.Env = append(os.Environ(), envKey+"=1")
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err == nil {
		t.Fatalf("expected subprocess to fail; got output: %q", output)
	}
	sentinel := SentinelSecret(42)
	if !strings.Contains(output, sentinel) {
		t.Errorf("failure message missing sentinel %q; output: %q", sentinel, output)
	}
	if !strings.Contains(output, "offset") {
		t.Errorf("failure message missing 'offset'; output: %q", output)
	}
}

func TestSentinelContextWindow(t *testing.T) {
	cases := []struct {
		i, sentinelLen, n, wantStart, wantEnd int
	}{
		{i: 50, sentinelLen: 10, n: 200, wantStart: 18, wantEnd: 92},    // normal case — no clamping
		{i: 5, sentinelLen: 10, n: 200, wantStart: 0, wantEnd: 47},      // left edge clamped
		{i: 190, sentinelLen: 10, n: 200, wantStart: 158, wantEnd: 200}, // right edge clamped
		{i: 0, sentinelLen: 5, n: 10, wantStart: 0, wantEnd: 10},        // both edges clamped
	}
	for _, tc := range cases {
		gotStart, gotEnd := sentinelContextWindow(tc.i, tc.sentinelLen, tc.n)
		if gotStart != tc.wantStart || gotEnd != tc.wantEnd {
			t.Errorf("sentinelContextWindow(%d,%d,%d) = [%d,%d), want [%d,%d)",
				tc.i, tc.sentinelLen, tc.n, gotStart, gotEnd, tc.wantStart, tc.wantEnd)
		}
	}
}

func TestAssertSentinelAbsent_EmptyHaystack(t *testing.T) {
	sentinel := SentinelSecret(1)
	t.Run("subtest", func(t *testing.T) {
		AssertSentinelAbsent(t, sentinel, "")
		if t.Failed() {
			t.Error("AssertSentinelAbsent should not fail on empty haystack")
		}
	})
}
