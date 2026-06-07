//go:build integration

// main_test.go is the harness package's own test entrypoint. When the
// harness is exercised by its own in-package tests (go test
// ./tests/integration/harness/), the resulting harness.test binary must
// reproduce the two roles the integration suite's TestMain provides
// (lifecycle_test.go), because in-package helper tests run under THIS
// binary, not the suite's:
//
//  1. Scripted-child dispatch. TestChild.Run re-execs os.Executable()
//     with --integration-child-mode + flags; without a dispatcher here
//     the re-exec'd harness.test would just run the test suite again.
//  2. Loopback allow-list transport. NewServer / MockValidator register
//     httptest listeners via RegisterAllowedHostHook; the matching
//     process-wide http.DefaultTransport guard lives in the suite's
//     TestMain, so we install an equivalent here.
package harness

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
)

// childIntegrationSentinel is declared in child.go; the flags below mirror
// the integration suite's scripted-child contract verbatim so a TestChild
// built against this binary behaves identically to one built against the
// suite binary.
const (
	childExitCodeFlag   = "--exit-code"
	childLifetimeFlag   = "--lifetime"
	childEmitStderrFlag = "--emit-stderr-pattern"
	childEmitStdoutFlag = "--emit-stdout-pattern"
)

// allowedHosts is the registry of loopback-bound listeners harness
// builders register during setup. The allow-list RoundTripper consults
// it on every outbound request.
//
//nolint:gochecknoglobals // package-test-wide allow-list; mirrors lifecycle_test.go
var allowedHosts struct {
	mu    sync.RWMutex
	hosts map[string]struct{}
}

// registerAllowedHost adds host (host:port) to the allow-list. Idempotent.
func registerAllowedHost(host string) {
	allowedHosts.mu.Lock()
	defer allowedHosts.mu.Unlock()
	if allowedHosts.hosts == nil {
		allowedHosts.hosts = map[string]struct{}{}
	}
	allowedHosts.hosts[host] = struct{}{}
}

// isAllowedHost reports whether host is loopback OR a registered listener.
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
	if _, ok := allowedHosts.hosts[host]; ok {
		return true
	}
	_, ok := allowedHosts.hosts[h]
	return ok
}

// allowListRoundTripper rejects any outbound request whose host is not
// loopback or a registered listener.
type allowListRoundTripper struct {
	inner http.RoundTripper
}

// RoundTrip enforces the loopback / registered-listener allow-list.
func (a *allowListRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	if !isAllowedHost(host) {
		nonLoopbackAttempts.Add(1)
		return nil, fmt.Errorf("harness: non-loopback HTTP host %q rejected by RoundTripper", host)
	}
	return a.inner.RoundTrip(req)
}

// nonLoopbackAttempts counts every rejected outbound request; surfaced
// after m.Run so a leaking helper fails the package test run.
//
//nolint:gochecknoglobals // package-test-wide observability counter
var nonLoopbackAttempts atomic.Int64

// childScript carries the four scripted-child parameters.
type childScript struct {
	exitCode  int
	lifetime  time.Duration
	stderrPat string
	stdoutPat string
}

// childModeArgs detects the sentinel and returns the framework-flag-filtered
// argv for flag parsing.
func childModeArgs() ([]string, bool) {
	if len(os.Args) < 2 {
		return nil, false
	}
	saw := false
	for _, a := range os.Args[1:] {
		if a == childIntegrationSentinel || strings.HasPrefix(a, childIntegrationSentinel+"=") {
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

// parseChildArgs binds the scripted-child flags and parses args.
func parseChildArgs(args []string) childScript {
	fs := flag.NewFlagSet("integration-child", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.Bool(strings.TrimPrefix(childIntegrationSentinel, "--"), false, "sentinel; presence triggers child mode")
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

// dispatchChildMode runs scripted-child mode if the sentinel is present.
func dispatchChildMode() bool {
	args, present := childModeArgs()
	if !present {
		return false
	}
	runChildScript(parseChildArgs(args))
	return true
}

// TestMain dispatches scripted-child mode when re-exec'd, otherwise
// installs the allow-list transport, wires the registration hook, and
// runs the package tests.
func TestMain(m *testing.M) {
	if dispatchChildMode() {
		return
	}
	prev := http.DefaultTransport
	http.DefaultTransport = &allowListRoundTripper{inner: prev}
	RegisterAllowedHostHook(registerAllowedHost)
	code := m.Run()
	if attempts := nonLoopbackAttempts.Load(); attempts > 0 {
		fmt.Fprintf(os.Stderr, "%d non-loopback HTTP attempt(s) blocked by allow-list RoundTripper\n", attempts)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// expectFatal asserts that fn fails the test it is given — the canonical
// way to cover a harness helper's t.Fatal / t.Error guard branch without
// failing the real test. A subtest cannot be used (a failed subtest also
// fails its parent), so fn runs against a throwaway *testing.T in its own
// goroutine: t.FailNow / t.Fatalf trigger runtime.Goexit which terminates
// that goroutine, and t.Errorf simply records the failure. label names the
// case for diagnostics.
func expectFatal(t *testing.T, label string, fn func(t *testing.T)) {
	t.Helper()
	if !runIsolated(fn) {
		t.Errorf("expectFatal(%s): expected the helper to fail the test, but it passed", label)
	}
}

// runIsolated runs fn with a detached *testing.T and reports whether fn
// failed it. The detached T's failure does not propagate to any real
// test. A panic inside fn is recovered and counts as a failure.
func runIsolated(fn func(t *testing.T)) bool {
	fake := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = recover() }()
		fn(fake)
	}()
	<-done
	return fake.Failed()
}
