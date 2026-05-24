package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mrz1836/hush/internal/testutil"
)

// healthFixture renders an /hz response from a healthSnapshot.
func healthFixture(snap healthSnapshot) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		body, _ := json.Marshal(snap)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}

// TestHealth_HappyPath drives a green health response through both
// TTY and non-TTY render paths.
//
//nolint:gocognit // table-driven via subtests; each branch is straightforward
func TestHealth_HappyPath(t *testing.T) {
	t.Parallel()
	snap := healthSnapshot{
		Status: "ok", Uptime: "1m0s", SecretsCount: 7, ActiveTokens: 2,
		DiscordConnected: true, ConfigValid: true, VaultLoaded: true, ClockInSync: true,
	}
	srv := httptest.NewServer(healthFixture(snap))
	t.Cleanup(srv.Close)

	t.Run("TTY", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		out := newStream(&stdout, true, false)
		errStream := newStream(&stderr, false, true)
		if err := runHealth(t.Context(), out, errStream, srv.URL); err != nil {
			t.Fatalf("runHealth: %v", err)
		}
		got := stdout.String()
		// TTY labels are the human-readable form ("discord connected" not
		// "discord_connected"); wire JSON keys are unchanged. The new
		// grouped layout also adds section headers (Server/Vault/Sessions).
		for _, want := range []string{"Server", "Vault", "Sessions", "status", "uptime", "discord connected"} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in TTY output: %q", want, got)
			}
		}
	})

	t.Run("non-TTY", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		out := newStream(&stdout, false, true)
		errStream := newStream(&stderr, false, true)
		if err := runHealth(t.Context(), out, errStream, srv.URL); err != nil {
			t.Fatalf("runHealth: %v", err)
		}
		var got healthSnapshot
		if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &got); err != nil {
			t.Fatalf("decode JSON: %v (raw=%q)", err, stdout.String())
		}
		if got != snap {
			t.Errorf("decoded snapshot: %+v, want %+v", got, snap)
		}
	})
}

// TestHealth_PartialHealth_ExitErr asserts partial-health (e.g.
// discord_connected=false) renders the full summary AND returns a
// non-nil error mapping to ExitErr.
func TestHealth_PartialHealth_ExitErr(t *testing.T) {
	t.Parallel()
	snap := healthSnapshot{
		Status: "ok", Uptime: "1m0s", DiscordConnected: false, ConfigValid: true,
		VaultLoaded: true, ClockInSync: true,
	}
	srv := httptest.NewServer(healthFixture(snap))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	err := runHealth(t.Context(), out, errStream, srv.URL)
	if err == nil {
		t.Fatal("expected partial-health error, got nil")
	}
	if got := mapErr(err); got != ExitErr {
		t.Errorf("mapErr = %d, want ExitErr (1)", got)
	}
	if !strings.Contains(stdout.String(), "discord_connected") {
		t.Errorf("expected full summary even on partial-health: %q", stdout.String())
	}
}

// TestHealth_ConnectionRefusedExplicitMessage asserts the literal
// connection-refused string on stderr.
func TestHealth_ConnectionRefusedExplicitMessage(t *testing.T) {
	t.Parallel()
	closedURL := "http://127.0.0.1:1"
	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	err := runHealth(t.Context(), out, errStream, closedURL)
	if err == nil {
		t.Fatal("expected error on closed port, got nil")
	}
	want := "could not connect to hush server at " + closedURL + ": connection refused"
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want substring %q", stderr.String(), want)
	}
	if got := mapErr(err); got != ExitErr {
		t.Errorf("mapErr = %d, want ExitErr (1)", got)
	}
}

// TestHealth_5xxServerError_ExitErr asserts a 500 maps to ExitErr.
func TestHealth_5xxServerError_ExitErr(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	err := runHealth(t.Context(), out, errStream, srv.URL)
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
	if got := mapErr(err); got != ExitErr {
		t.Errorf("mapErr = %d, want ExitErr (1)", got)
	}
}

// TestHealth_NoAuthRequired asserts the outgoing request has no
// Authorization header — health is reachable on the trusted-network
// perimeter without a JWT.
func TestHealth_NoAuthRequired(t *testing.T) {
	t.Parallel()
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		body, _ := json.Marshal(healthSnapshot{Status: "ok", DiscordConnected: true, ConfigValid: true, VaultLoaded: true, ClockInSync: true})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	if err := runHealth(t.Context(), out, errStream, srv.URL); err != nil {
		t.Fatalf("runHealth: %v", err)
	}
	if seen != "" {
		t.Errorf("Authorization header set: %q", seen)
	}
}

// TestHealth_OutputNoSentinel asserts the SECRET sentinel never
// leaks through any output stream even when a server returns it.
func TestHealth_OutputNoSentinel(t *testing.T) {
	t.Parallel()
	sentinel := testutil.SentinelSecret(14)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Plant the sentinel as the uptime text — runHealth echoes
		// the JSON body verbatim on a pipe, so this test verifies
		// the response classifier paths don't shove the sentinel
		// onto stderr or into a verbose-trace line.
		body, _ := json.Marshal(healthSnapshot{
			Status: "ok", Uptime: sentinel, DiscordConnected: true, ConfigValid: true,
			VaultLoaded: true, ClockInSync: true,
		})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	_ = runHealth(t.Context(), out, errStream, srv.URL)
	// stderr must NOT contain the sentinel — only stdout (the echo
	// path) is permitted.
	testutil.AssertSentinelAbsent(t, sentinel, stderr.String())
}

// TestHealth_MissingServerFlag asserts an empty --server returns
// errMissingFlag → ExitInputErr.
func TestHealth_MissingServerFlag(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	out := newStream(&stdout, false, true)
	errStream := newStream(&stderr, false, true)
	err := runHealth(context.Background(), out, errStream, "")
	if got := mapErr(err); got != ExitInputErr {
		t.Errorf("mapErr = %d, want ExitInputErr", got)
	}
}
