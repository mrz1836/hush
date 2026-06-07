//go:build integration

package harness

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAtomicIntStoreLoad covers the basic store/load round-trip.
func TestAtomicIntStoreLoad(t *testing.T) {
	var a atomicInt
	assert.Equal(t, 0, a.load())
	a.store(42)
	assert.Equal(t, 42, a.load())
	a.store(-1)
	assert.Equal(t, -1, a.load())
}

// TestAtomicIntConcurrent exercises the mutex under -race.
func TestAtomicIntConcurrent(t *testing.T) {
	var a atomicInt
	var wg sync.WaitGroup
	wg.Add(20)
	for i := range 20 {
		go func(v int) {
			defer wg.Done()
			a.store(v)
			_ = a.load()
		}(i)
	}
	wg.Wait()
	assert.GreaterOrEqual(t, a.load(), 0)
}

// TestSyncBufWriterWrite verifies the shared-mutex writer appends bytes
// and honors the io.Writer contract.
func TestSyncBufWriterWrite(t *testing.T) {
	var mu sync.Mutex
	buf := &bytes.Buffer{}
	w := &syncBufWriter{mu: &mu, buf: buf}

	n, err := w.Write([]byte("abc"))
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	_, _ = w.Write([]byte("def"))
	assert.Equal(t, "abcdef", buf.String())
}

// TestNewChildBuildsConfig confirms NewChild wires the scripted-child argv
// (sentinel + flags) and stdio capture without starting the process.
func TestNewChildBuildsConfig(t *testing.T) {
	lc := NewLogCapture(t)
	tc := NewChild(t, lc, ChildOpts{
		ExitCode:   3,
		Lifetime:   10 * time.Millisecond,
		EmitStdout: "out-pat",
		EmitStderr: "err-pat",
	})

	cfg := tc.Cmd()
	require.NotEmpty(t, cfg.Command)
	joined := strings.Join(cfg.Command, " ")
	assert.Contains(t, joined, childIntegrationSentinel)
	assert.Contains(t, joined, childExitCodeFlag+"="+strconv.Itoa(3))
	assert.Contains(t, joined, childEmitStdoutFlag+"=out-pat")
	assert.Contains(t, joined, childEmitStderrFlag+"=err-pat")
	assert.Contains(t, joined, "-test.run=^$")
	assert.NotNil(t, cfg.Stdout)
	assert.NotNil(t, cfg.Stderr)
	assert.NotNil(t, cfg.Logger)

	// Nothing has run yet, so captures and exit code are empty/zero.
	assert.Empty(t, tc.Stdout())
	assert.Empty(t, tc.Stderr())
	assert.Equal(t, 0, tc.ExitCode())
}

// TestNewChildOmitsEmptyPatterns confirms the optional emit flags are
// absent when the corresponding opts are empty.
func TestNewChildOmitsEmptyPatterns(t *testing.T) {
	lc := NewLogCapture(t)
	tc := NewChild(t, lc, ChildOpts{ExitCode: 0, Lifetime: time.Millisecond})
	joined := strings.Join(tc.Cmd().Command, " ")
	assert.NotContains(t, joined, childEmitStdoutFlag)
	assert.NotContains(t, joined, childEmitStderrFlag)
}

// TestChildRunCapturesExitAndStdio runs a scripted child end-to-end via
// the TestMain dispatcher: it must exit with the scripted code and the
// READY banner plus both emit patterns must land in the captures.
func TestChildRunCapturesExitAndStdio(t *testing.T) {
	lc := NewLogCapture(t)
	tc := NewChild(t, lc, ChildOpts{
		ExitCode:   5,
		Lifetime:   20 * time.Millisecond,
		EmitStdout: "hello-stdout",
		EmitStderr: "hello-stderr",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	code, err := tc.Run(ctx)
	require.NoError(t, err)
	assert.Equal(t, 5, code)
	assert.Equal(t, 5, tc.ExitCode())

	assert.Contains(t, string(tc.Stdout()), "READY")
	assert.Contains(t, string(tc.Stdout()), "hello-stdout")
	assert.Contains(t, string(tc.Stderr()), "hello-stderr")
}

// TestChildStdoutStderrDefensiveCopy confirms the capture getters return
// copies that cannot corrupt the underlying buffers.
func TestChildStdoutStderrDefensiveCopy(t *testing.T) {
	lc := NewLogCapture(t)
	tc := NewChild(t, lc, ChildOpts{ExitCode: 0, Lifetime: 10 * time.Millisecond, EmitStdout: "copy-me"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := tc.Run(ctx)
	require.NoError(t, err)

	snap := tc.Stdout()
	require.NotEmpty(t, snap)
	for i := range snap {
		snap[i] = 0
	}
	assert.Contains(t, string(tc.Stdout()), "copy-me")
}
