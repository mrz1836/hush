//go:build integration

package harness

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/mrz1836/hush/internal/testutil"
)

// LogCapture is a per-scenario slog sink wired into every harness piece's
// Logger. The captured bytes feed the canonical multi-stream
// AssertSentinelAbsent sweep (data-model.md §4.6, Contract D).
//
// Concurrency: Bytes() and the underlying handler synchronize on a single
// sync.Mutex so concurrent supervisor / server / refresher writes interleave
// safely. There is no package-level mutable global; one instance per
// scenario.
type LogCapture struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	logger *slog.Logger
}

// NewLogCapture constructs a per-scenario LogCapture. The returned
// *slog.Logger is the Logger every harness builder hands to the in-process
// real internal/* package. t.Cleanup releases the buffer at scenario end.
func NewLogCapture(t *testing.T) *LogCapture {
	t.Helper()
	lc := &LogCapture{}
	handler := slog.NewTextHandler(syncWriter{lc: lc}, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	lc.logger = slog.New(handler)
	t.Cleanup(func() {
		lc.mu.Lock()
		lc.buf.Reset()
		lc.mu.Unlock()
	})
	return lc
}

// Logger returns the *slog.Logger that captures into this buffer.
func (l *LogCapture) Logger() *slog.Logger {
	return l.logger
}

// Bytes returns a defensive copy of the captured byte stream.
func (l *LogCapture) Bytes() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]byte, l.buf.Len())
	copy(out, l.buf.Bytes())
	return out
}

// syncWriter is the io.Writer the slog handler writes through; serializes
// concurrent writes onto LogCapture.buf.
type syncWriter struct {
	lc *LogCapture
}

// Write implements io.Writer.
func (s syncWriter) Write(p []byte) (int, error) {
	s.lc.mu.Lock()
	defer s.lc.mu.Unlock()
	return s.lc.buf.Write(p)
}

// AssertSentinelAbsent runs testutil.AssertSentinelAbsent over every
// supplied byte stream. Each stream is labeled for diagnostics. nil
// streams are tolerated (Scenario 1 has no supervisor → nil StatusRaw()).
//
// Canonical six-stream coverage (data-model.md §4.6):
//
//  1. operational slog (LogCapture.Bytes())
//  2. audit JSONL raw bytes (TestServer.RawAudit())
//  3. status-socket raw bytes (TestSupervisor.StatusRaw())
//  4. Discord alert payloads (TestDiscord.AlertsRaw())
//  5. child stdout + stderr (TestChild.Stdout(), .Stderr())
//  6. error message strings (CollectErrors)
func AssertSentinelAbsent(t *testing.T, sentinel string, streams ...[]byte) {
	t.Helper()
	for i, s := range streams {
		if s == nil {
			continue
		}
		if idx := bytes.Index(s, []byte(sentinel)); idx >= 0 {
			t.Errorf("harness.AssertSentinelAbsent: stream[%d] leaked sentinel %q at offset %d", i, sentinel, idx)
		}
		// Forward to testutil for matching diagnostics format.
		testutil.AssertSentinelAbsent(t, sentinel, string(s))
	}
}

// CollectErrors concatenates every non-nil error.Error() string into a
// single byte stream suitable for AssertSentinelAbsent's error-stream
// slot. Nil errors are dropped.
func CollectErrors(errs ...error) []byte {
	var b strings.Builder
	for _, e := range errs {
		if e == nil {
			continue
		}
		b.WriteString(e.Error())
		b.WriteByte('\n')
	}
	return []byte(b.String())
}
