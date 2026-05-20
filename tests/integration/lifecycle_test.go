//go:build integration

// Package integration_test is the lifecycle integration test suite.
// Build-tagged //go:build integration; default `go test ./...` compiles
// zero files in this directory.
//
// TestMain serves two roles:
//  1. When invoked by the supervisor's child fork-exec with the
//     --integration-child-mode sentinel flag, dispatch to the scripted-
//     child entrypoint, run the scripted exit-code / lifetime / stderr-
//     pattern script, and os.Exit before m.Run.
//  2. Otherwise, install a process-wide http.DefaultTransport allow-list
//     RoundTripper rejecting any host outside 127.0.0.1, ::1, or one of
//     the registered httptest listeners, then call
//     m.Run() and exit with its code.
package integration_test

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/tests/integration/harness"
)

// integrationChildModeSentinel is the argv flag the supervisor passes when
// re-invoking this test binary as a scripted child. The dispatcher
// below recognizes it before testing.M.Run is called.
const integrationChildModeSentinel = "--integration-child-mode"

// childExitCodeFlag is the script's exit code.
const childExitCodeFlag = "--exit-code"

// childLifetimeFlag is the duration the child sleeps before exiting.
const childLifetimeFlag = "--lifetime"

// childEmitStderrFlag is a pattern the child writes to stderr before
// exiting. Used by Scenario 15 (log-pattern watchdog).
const childEmitStderrFlag = "--emit-stderr-pattern"

// childEmitStdoutFlag is a pattern the child writes to stdout. Useful for
// child-stdout sentinel sweeps.
const childEmitStdoutFlag = "--emit-stdout-pattern"

// allowedHosts is the registry of loopback-bound httptest listeners every
// scenario has registered. The process-wide allow-list RoundTripper
// consults this map. Mutated only at scenario setup time before any
// concurrent HTTP traffic starts; reads happen during HTTP dispatch.
//
//nolint:gochecknoglobals // suite-wide allow-list mutated only before m.Run dispatches scenarios
var allowedHosts struct {
	mu    sync.RWMutex
	hosts map[string]struct{}
}

// nonLoopbackAttempts records every reject the allow-list RoundTripper
// produced; reported by TestMain after m.Run.
//
//nolint:gochecknoglobals // suite-wide observability counter (atomic)
var nonLoopbackAttempts atomic.Int64

// registerAllowedHost adds host (host:port) to the allow-list. Idempotent.
// Wired into the harness via harness.RegisterAllowedHostHook so harness
// builders that stand up httptest listeners can extend the allow-list
// without importing the test package directly.
func registerAllowedHost(host string) {
	allowedHosts.mu.Lock()
	defer allowedHosts.mu.Unlock()
	if allowedHosts.hosts == nil {
		allowedHosts.hosts = map[string]struct{}{}
	}
	allowedHosts.hosts[host] = struct{}{}
}

// isAllowedHost reports whether host (host:port) is loopback OR a
// registered httptest listener.
func isAllowedHost(host string) bool {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if h == "127.0.0.1" || h == "::1" || h == "localhost" {
		return true
	}
	allowedHosts.mu.RLock()
	defer allowedHosts.mu.RUnlock()
	_, ok := allowedHosts.hosts[host]
	if ok {
		return true
	}
	// Some httptest listeners register host-only (without port).
	_, ok = allowedHosts.hosts[h]
	return ok
}

// allowListRoundTripper wraps http.DefaultTransport and rejects any
// outbound connect attempt whose Host header is not in the allow-list.
type allowListRoundTripper struct {
	inner http.RoundTripper
}

// RoundTrip enforces the loopback / registered-listener allow-list.
func (a *allowListRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	if !isAllowedHost(host) {
		nonLoopbackAttempts.Add(1)
		return nil, fmt.Errorf("integration: non-loopback HTTP host %q rejected by RoundTripper", host)
	}
	return a.inner.RoundTrip(req)
}

// installAllowListTransport mutates http.DefaultTransport to be the
// allow-list wrapper. Called from TestMain BEFORE m.Run; not safe for
// concurrent install.
func installAllowListTransport() {
	prev := http.DefaultTransport
	http.DefaultTransport = &allowListRoundTripper{inner: prev}
}

// childModeArgs detects whether os.Args carries the integration-child-mode
// sentinel and returns the test-framework-flag-filtered argv for parsing.
// Returns (filtered, true) when the sentinel is present; (nil, false)
// otherwise.
func childModeArgs() ([]string, bool) {
	if len(os.Args) < 2 {
		return nil, false
	}
	saw := false
	for _, a := range os.Args[1:] {
		if a == integrationChildModeSentinel || strings.HasPrefix(a, integrationChildModeSentinel+"=") {
			saw = true
			break
		}
	}
	if !saw {
		return nil, false
	}
	filtered := make([]string, 0, len(os.Args))
	for _, a := range os.Args[1:] {
		if strings.HasPrefix(a, "-test.") || a == "--" {
			continue
		}
		filtered = append(filtered, a)
	}
	return filtered, true
}

// childScript carries the four scripted-child parameters parsed from
// the integration-child-mode argv.
type childScript struct {
	exitCode  int
	lifetime  time.Duration
	stderrPat string
	stdoutPat string
}

// parseChildArgs binds the supported flags onto a flag.FlagSet and
// parses args. Exits on parse failure.
func parseChildArgs(args []string) childScript {
	fs := flag.NewFlagSet("integration-child", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.Bool(strings.TrimPrefix(integrationChildModeSentinel, "--"), false, "sentinel; presence triggers child mode")
	exitCode := fs.Int(strings.TrimPrefix(childExitCodeFlag, "--"), 0, "exit code")
	lifetime := fs.Duration(strings.TrimPrefix(childLifetimeFlag, "--"), 50*time.Millisecond, "sleep before exit")
	stderrPat := fs.String(strings.TrimPrefix(childEmitStderrFlag, "--"), "", "pattern to write to stderr")
	stdoutPat := fs.String(strings.TrimPrefix(childEmitStdoutFlag, "--"), "", "pattern to write to stdout")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "integration-child: parse: %v\n", err)
		os.Exit(2)
	}
	return childScript{
		exitCode:  *exitCode,
		lifetime:  *lifetime,
		stderrPat: *stderrPat,
		stdoutPat: *stdoutPat,
	}
}

// runChildScript executes the parsed script: write READY, optional
// patterns, sleep, exit with the configured code.
func runChildScript(s childScript) {
	_, _ = os.Stdout.WriteString("READY\n")
	if s.stdoutPat != "" {
		_, _ = os.Stdout.WriteString(s.stdoutPat + "\n")
	}
	if s.stderrPat != "" {
		_, _ = os.Stderr.WriteString(s.stderrPat + "\n")
	}
	if s.lifetime > 0 {
		time.Sleep(s.lifetime)
	}
	os.Exit(s.exitCode)
}

// dispatchChildMode runs the scripted child mode when os.Args carries the
// integration-child-mode sentinel. Returns true if it dispatched (and
// already called os.Exit); false otherwise.
func dispatchChildMode() bool {
	args, present := childModeArgs()
	if !present {
		return false
	}
	runChildScript(parseChildArgs(args))
	return true
}

// installHarnessHooks bridges the harness's seam functions (currently
// just the allow-list listener registration) to the suite's local
// state. Called once from TestMain before m.Run.
func installHarnessHooks() {
	harness.RegisterAllowedHostHook(registerAllowedHost)
}

// TestMain is the suite entrypoint. Dispatches integration-child mode
// when invoked by the supervisor's fork-exec, otherwise installs the
// loopback allow-list RoundTripper and runs the suite.
func TestMain(m *testing.M) {
	if dispatchChildMode() {
		return
	}
	installAllowListTransport()
	installHarnessHooks()
	code := m.Run()
	if attempts := nonLoopbackAttempts.Load(); attempts > 0 {
		fmt.Fprintf(os.Stderr, "%d non-loopback HTTP attempt(s) blocked by allow-list RoundTripper\n", attempts)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
