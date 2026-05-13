//go:build integration

package harness

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/mrz1836/hush/internal/audit"
)

// TestServer is a placeholder for the future SDD-25 in-process
// internal/server composition. The full implementation (lazy validator
// httptest servers, RoundTripper rewrite from api.*.com → loopback,
// Reload() SIGHUP-equivalent, real TokenStore exposure) is tracked under
// task T028 and remains outstanding in this SDD-25 chunk. The shell
// below is wired only to the extent the implemented scenarios need.
type TestServer struct {
	vault      *TestVault
	httpServer *httptest.Server
}

// ServerOpts is the placeholder option bag for future scenario wiring.
type ServerOpts struct {
	Vault *TestVault
}

// NewServer constructs a minimal in-process httptest.Server scaffold.
// The current implementation only stands up an empty 404 mux — sufficient
// for scenarios that need a URL but not a real chassis. Replace with a
// real internal/server.New composition when extending coverage to
// scenarios 1, 2, 7, 12, 13.
func NewServer(t *testing.T, opts ServerOpts) *TestServer {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	registerAllowedHostExternal(srv.URL)
	t.Cleanup(srv.Close)
	return &TestServer{vault: opts.Vault, httpServer: srv}
}

// URL returns the httptest server URL.
func (s *TestServer) URL() string { return s.httpServer.URL }

// Vault returns the harness vault wired into this server.
func (s *TestServer) Vault() *TestVault { return s.vault }

// ReadAudit parses the vault's audit JSONL into a slice of events.
// Returns an empty slice if the file does not exist yet.
func (s *TestServer) ReadAudit() []audit.Event {
	if s.vault == nil {
		return nil
	}
	raw := s.RawAudit()
	if len(raw) == 0 {
		return nil
	}
	var out []audit.Event
	for _, line := range splitLines(raw) {
		if len(line) == 0 {
			continue
		}
		var ev audit.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// RawAudit returns the raw audit-log byte stream for the sentinel sweep.
func (s *TestServer) RawAudit() []byte {
	if s.vault == nil {
		return nil
	}
	f, err := os.Open(s.vault.AuditPath())
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	out, _ := io.ReadAll(f)
	return out
}

// MockValidator is a placeholder for the future per-provider httptest
// stub. The current shell records the registration but does not yet
// install a working RoundTripper rewrite.
func (s *TestServer) MockValidator(_ string, _ http.HandlerFunc) {
	// Wired by future scenario implementations (T028 in tasks.md).
}

// Stop closes the in-process server. Registered automatically via
// t.Cleanup in NewServer.
func (s *TestServer) Stop() {
	if s.httpServer != nil {
		s.httpServer.Close()
	}
}

// splitLines is a minimal JSONL splitter that tolerates a trailing
// newline. Returned slices share storage with raw.
func splitLines(raw []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range raw {
		if b == '\n' {
			out = append(out, raw[start:i])
			start = i + 1
		}
	}
	if start < len(raw) {
		out = append(out, raw[start:])
	}
	return out
}

// registerAllowedHostExternal forwards to the lifecycle_test.go suite
// allow-list. Defined as a method-pointer seam so harness code can
// register listeners without importing the test package directly. The
// actual registration is wired through a package-private hook the suite
// TestMain installs before any scenario runs.
//
//nolint:gochecknoglobals // suite seam: replaced once by RegisterAllowedHostHook at TestMain init
var registerAllowedHostExternal = func(_ string) {
	// no-op default; integration suite's TestMain installs the real one
	// when it boots.
}

// RegisterAllowedHostHook is called once at suite init by lifecycle_test.go
// to bridge the harness's listener-registration into the suite's
// process-wide RoundTripper allow-list.
func RegisterAllowedHostHook(fn func(string)) {
	registerAllowedHostExternal = fn
}
