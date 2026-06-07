//go:build integration

package harness

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/internal/testutil"
)

// TestLogCaptureLoggerCapturesWrites verifies the slog.Logger handed out
// by NewLogCapture writes through to the buffer that Bytes() reads.
func TestLogCaptureLoggerCapturesWrites(t *testing.T) {
	lc := NewLogCapture(t)
	require.NotNil(t, lc.Logger())

	lc.Logger().Info("hello", "key", "value")

	got := string(lc.Bytes())
	assert.Contains(t, got, "hello")
	assert.Contains(t, got, "key=value")
}

// TestLogCaptureBytesIsDefensiveCopy confirms mutating the returned slice
// does not corrupt the underlying buffer.
func TestLogCaptureBytesIsDefensiveCopy(t *testing.T) {
	lc := NewLogCapture(t)
	lc.Logger().Info("first")

	snap := lc.Bytes()
	require.NotEmpty(t, snap)
	for i := range snap {
		snap[i] = 'X'
	}

	// A fresh read must be unaffected by the caller's mutation.
	assert.Contains(t, string(lc.Bytes()), "first")
}

// TestLogCaptureEmptyBeforeWrites confirms a brand-new capture is empty.
func TestLogCaptureEmptyBeforeWrites(t *testing.T) {
	lc := NewLogCapture(t)
	assert.Empty(t, lc.Bytes())
}

// TestLogCaptureConcurrentWrites drives the syncWriter mutex under -race
// to prove concurrent logging interleaves safely.
func TestLogCaptureConcurrentWrites(t *testing.T) {
	lc := NewLogCapture(t)
	const writers = 8
	const perWriter = 50

	var wg sync.WaitGroup
	wg.Add(writers)
	for w := range writers {
		go func(id int) {
			defer wg.Done()
			for range perWriter {
				lc.Logger().Info("concurrent", "writer", id)
			}
		}(w)
	}
	wg.Wait()

	lines := strings.Count(string(lc.Bytes()), "concurrent")
	assert.Equal(t, writers*perWriter, lines)
}

// TestSyncWriterWriteReturnsCount verifies the io.Writer contract: the
// returned count equals len(p) and the bytes land in the buffer.
func TestSyncWriterWriteReturnsCount(t *testing.T) {
	lc := NewLogCapture(t)
	w := syncWriter{lc: lc}

	payload := []byte("raw-bytes\n")
	n, err := w.Write(payload)
	require.NoError(t, err)
	assert.Equal(t, len(payload), n)
	assert.Contains(t, string(lc.Bytes()), "raw-bytes")
}

// TestAssertSentinelAbsentPassesWhenClean confirms the multi-stream sweep
// is a no-op when no stream contains the sentinel (nil streams skipped).
func TestAssertSentinelAbsentPassesWhenClean(t *testing.T) {
	sentinel := testutil.SentinelSecret(0)
	AssertSentinelAbsent(
		t,
		sentinel,
		[]byte("clean operational log"),
		nil, // tolerated: a stream with no source this scenario
		[]byte("audit line without secrets"),
	)
}

// TestAssertSentinelAbsentFailsOnLeak drives the leak-detection branch:
// when a stream carries the sentinel the helper must fail the test.
func TestAssertSentinelAbsentFailsOnLeak(t *testing.T) {
	sentinel := testutil.SentinelSecret(7)
	expectFatal(t, "leaked-sentinel", func(t *testing.T) {
		AssertSentinelAbsent(
			t,
			sentinel,
			[]byte("prefix "+sentinel+" suffix"),
		)
	})
}

// TestCollectErrorsConcatenatesNonNil verifies error strings are joined
// newline-delimited and nil errors are dropped.
func TestCollectErrorsConcatenatesNonNil(t *testing.T) {
	out := string(CollectErrors(
		errors.New("first failure"),
		nil,
		errors.New("second failure"),
		nil,
	))
	assert.Equal(t, "first failure\nsecond failure\n", out)
}

// TestCollectErrorsAllNil returns an empty stream when every error is nil.
func TestCollectErrorsAllNil(t *testing.T) {
	assert.Empty(t, CollectErrors(nil, nil))
	assert.Empty(t, CollectErrors())
}
