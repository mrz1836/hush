package audit

import (
	"bufio"
	"sync/atomic"
)

// SetWriterFlushHook installs a callback invoked on the writer goroutine
// immediately before each Flush.  Used by backpressure tests to pause
// the writer so producers can pile up.
func SetWriterFlushHook(w Writer, fn func()) {
	if impl, ok := w.(*writerImpl); ok {
		impl.hookBeforeFlush = fn
	}
}

// CallPersistLine exercises the per-line persistence helper with a
// caller-supplied bufio.Writer.  Used by coverage tests to drive the
// bw.Write / bw.Flush error branches deterministically.
func CallPersistLine(bw *bufio.Writer, line []byte) error {
	w := &writerImpl{bw: bw}
	return w.persistLine(line)
}

// ProcessOneEventWithFailingWriter is intentionally not exported here —
// the persistLine-error branch inside process() is exercised indirectly
// via the writer goroutine in production paths.

// MirrorBufferSize returns the locked mirror buffer depth so tests can
// reason about overflow.
func MirrorBufferSize() int { return mirrorBufferSize }

// MirrorDroppedCount returns how many events have been dropped from the
// mirror buffer due to fullness.
func MirrorDroppedCount(m *DiscordMirror) uint64 {
	if m == nil {
		return 0
	}
	return m.dropped.Load()
}

// SetMirrorPublishHook installs a callback invoked on the mirror
// goroutine immediately before each publish attempt.  Used by tests to
// pause the goroutine so the buffer fills.
func SetMirrorPublishHook(m *DiscordMirror, fn func()) {
	if m != nil {
		m.hookBeforePublish = fn
	}
}

// SetMirrorFailureSink installs the failure callback the mirror invokes
// after a publish error.  Used by the writer to emit
// `audit_mirror_failed` chain events; tests use it to assert the loop
// fires exactly once per failure.
func SetMirrorFailureSink(m *DiscordMirror, fn func(seq uint64, action, errClass string)) {
	if m != nil {
		m.failureSink = fn
	}
}

// GenesisPrevHashForTest exposes the package-private genesis prevHash
// so tests can assert reproducibility.
func GenesisPrevHashForTest() [32]byte { return genesisPrevHash }

// SecureRandReaderForTest exposes the rand.Reader-returning helper.
// Tests use it transitively when they swap nowFn but otherwise read
// signatures from the on-disk file directly.
var _ = atomic.Uint64{}
