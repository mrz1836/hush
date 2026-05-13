//go:build integration

package harness

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
)

// childIntegrationSentinel is the argv flag the integration TestMain
// dispatcher inspects to enter scripted-child mode. Mirrored verbatim
// from lifecycle_test.go (research.md §6).
const childIntegrationSentinel = "--integration-child-mode"

// ChildOpts script one programmable child invocation.
type ChildOpts struct {
	// ExitCode is the code the scripted child exits with after Lifetime.
	ExitCode int
	// Lifetime is the time the scripted child sleeps before exiting.
	Lifetime time.Duration
	// EmitStderr, when non-empty, is written to stderr before sleep.
	EmitStderr string
	// EmitStdout, when non-empty, is written to stdout before sleep.
	EmitStdout string
}

// TestChild is the harness handle for a scripted-child fork-exec. It
// re-invokes the integration test binary itself with
// --integration-child-mode + flags interpreted by TestMain's dispatcher.
type TestChild struct {
	opts   ChildOpts
	cfg    supervise.ChildConfig
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	mu     sync.Mutex
	child  *supervise.Child
	exit   atomicInt
}

// atomicInt is a tiny atomic-int wrapper kept package-private to avoid
// importing sync/atomic in this file's public surface.
type atomicInt struct {
	mu sync.Mutex
	v  int
}

// store sets v.
func (a *atomicInt) store(x int) { a.mu.Lock(); a.v = x; a.mu.Unlock() }

// load returns v.
func (a *atomicInt) load() int { a.mu.Lock(); defer a.mu.Unlock(); return a.v }

// NewChild constructs the supervise.ChildConfig pointing at the
// integration test binary in scripted-child mode. The child does NOT
// start until the supervisor's Lifecycle invokes it (via Cmd()).
func NewChild(t *testing.T, lc *LogCapture, opts ChildOpts) *TestChild {
	t.Helper()
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("harness.NewChild: os.Executable: %v", err)
	}
	stdoutBuf := &bytes.Buffer{}
	stderrBuf := &bytes.Buffer{}
	args := []string{
		exePath,
		"-test.run=^$",
		childIntegrationSentinel,
		"--exit-code=" + strconv.Itoa(opts.ExitCode),
		"--lifetime=" + opts.Lifetime.String(),
	}
	if opts.EmitStderr != "" {
		args = append(args, "--emit-stderr-pattern="+opts.EmitStderr)
	}
	if opts.EmitStdout != "" {
		args = append(args, "--emit-stdout-pattern="+opts.EmitStdout)
	}
	tc := &TestChild{
		opts:   opts,
		stdout: stdoutBuf,
		stderr: stderrBuf,
	}
	tc.cfg = supervise.ChildConfig{
		Command: args,
		Env:     os.Environ(),
		Stdout:  &syncBufWriter{mu: &tc.mu, buf: stdoutBuf},
		Stderr:  &syncBufWriter{mu: &tc.mu, buf: stderrBuf},
		Logger:  lc.Logger(),
	}
	return tc
}

// Cmd returns the supervise.ChildConfig the supervisor wires into its
// lifecycle. The supervisor owns lifetime (Start / Wait / Forward); the
// harness only owns stdio capture and exit-code observation.
func (c *TestChild) Cmd() supervise.ChildConfig { return c.cfg }

// Stdout returns the bytes captured from the scripted child's stdout.
func (c *TestChild) Stdout() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, c.stdout.Len())
	copy(out, c.stdout.Bytes())
	return out
}

// Stderr returns the bytes captured from the scripted child's stderr.
func (c *TestChild) Stderr() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]byte, c.stderr.Len())
	copy(out, c.stderr.Bytes())
	return out
}

// Run starts the child directly (without a Supervisor) and blocks until
// it exits. Used by simple scenarios that don't need the full Lifecycle
// composition. Returns the observed exit code.
func (c *TestChild) Run(ctx context.Context) (int, error) {
	child := supervise.NewChild(c.cfg)
	c.mu.Lock()
	c.child = child
	c.mu.Unlock()
	if err := child.Start(ctx); err != nil {
		return 0, fmt.Errorf("harness.TestChild.Run: start: %w", err)
	}
	code, _, _ := child.Wait()
	c.exit.store(code)
	return code, nil
}

// ExitCode returns the scripted exit code (after Run completes).
func (c *TestChild) ExitCode() int { return c.exit.load() }

// syncBufWriter is a tiny io.Writer that synchronizes on a shared mutex.
type syncBufWriter struct {
	mu  *sync.Mutex
	buf *bytes.Buffer
}

// Write implements io.Writer.
func (w *syncBufWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}
