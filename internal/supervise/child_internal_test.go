package supervise

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Internal tests cover ringBuffer paths and Forward coalesce
// edges that are awkward to drive cleanly through the cross-
// process integration suite.

func TestRingBuffer_WriteUnderCap(t *testing.T) {
	t.Parallel()
	r := newRingBuffer("stdout", slog.New(slog.NewTextHandler(io.Discard, nil)))
	n, err := r.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write: got (%d, %v)", n, err)
	}
	if r.atCapacity {
		t.Fatalf("atCapacity should remain false under cap")
	}
}

func TestRingBuffer_WriteSinglePayloadLargerThanCap(t *testing.T) {
	t.Parallel()
	r := newRingBuffer("stdout", slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.cap = 16
	r.buf = make([]byte, 0, 16)
	payload := bytes.Repeat([]byte("X"), 32)
	n, err := r.Write(payload)
	if err != nil || n != 32 {
		t.Fatalf("Write: got (%d, %v)", n, err)
	}
	if !r.atCapacity {
		t.Fatalf("atCapacity should be true after overflow")
	}
	if len(r.buf) != 16 {
		t.Fatalf("buf length should be cap=%d, got %d", r.cap, len(r.buf))
	}
}

func TestRingBuffer_WritePartialEvictionFromBuf(t *testing.T) {
	t.Parallel()
	r := newRingBuffer("stdout", slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.cap = 16
	r.buf = bytes.Repeat([]byte("A"), 10)
	// 10 bytes already in buf; write 10 more → overflow by 4.
	// Should drop 4 oldest A bytes from buf, then append 10 new.
	n, err := r.Write(bytes.Repeat([]byte("B"), 10))
	if err != nil || n != 10 {
		t.Fatalf("Write: got (%d, %v)", n, err)
	}
	if len(r.buf) != 16 {
		t.Fatalf("buf length: want 16, got %d", len(r.buf))
	}
	if !bytes.Equal(r.buf, []byte("AAAAAABBBBBBBBBB")) {
		t.Fatalf("FIFO eviction wrong: got %q", r.buf)
	}
}

func TestRingBuffer_WriteEvictsEntireBufAndPartialPayload(t *testing.T) {
	t.Parallel()
	r := newRingBuffer("stdout", slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.cap = 8
	r.buf = bytes.Repeat([]byte("A"), 8)
	// Buf is full; write 12 bytes → overflow by 12.
	// Should drop all 8 buf bytes and 4 prefix bytes of payload.
	n, err := r.Write(bytes.Repeat([]byte("B"), 12))
	if err != nil || n != 12 {
		t.Fatalf("Write: got (%d, %v)", n, err)
	}
	if len(r.buf) != 8 {
		t.Fatalf("buf length: want 8, got %d", len(r.buf))
	}
	if !bytes.Equal(r.buf, []byte("BBBBBBBB")) {
		t.Fatalf("payload-prefix eviction wrong: got %q", r.buf)
	}
}

func TestRingBuffer_WriteToClosedReturnsLenP(t *testing.T) {
	t.Parallel()
	r := newRingBuffer("stdout", slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = r.Close()
	n, err := r.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Write to closed: got (%d, %v); want (5, nil)", n, err)
	}
}

func TestRingBuffer_WriteCountsOverflowEpisodes(t *testing.T) {
	t.Parallel()
	var count int32
	h := &countHandler{counter: &count}
	r := newRingBuffer("stdout", slog.New(h))
	r.cap = 8
	r.buf = make([]byte, 0, 8)
	// First overflow → +1 warning.
	_, _ = r.Write(bytes.Repeat([]byte("A"), 16))
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("first overflow: want 1 warning, got %d", got)
	}
	// Second overflow without drain → no new warning (still
	// in the same episode).
	_, _ = r.Write(bytes.Repeat([]byte("B"), 16))
	if got := atomic.LoadInt32(&count); got != 1 {
		t.Fatalf("second overflow same episode: want 1 warning, got %d", got)
	}
	// Drain to reset atCapacity.
	var buf bytes.Buffer
	if _, err := r.drain(&buf); err != nil {
		t.Fatalf("drain: %v", err)
	}
	// New overflow → +1 warning.
	_, _ = r.Write(bytes.Repeat([]byte("C"), 16))
	if got := atomic.LoadInt32(&count); got != 2 {
		t.Fatalf("new episode: want 2 warnings, got %d", got)
	}
}

func TestRingBuffer_DrainEmptyOpenReturnsNoError(t *testing.T) {
	t.Parallel()
	r := newRingBuffer("stdout", slog.New(slog.NewTextHandler(io.Discard, nil)))
	n, err := r.drain(io.Discard)
	if err != nil || n != 0 {
		t.Fatalf("drain empty: got (%d, %v)", n, err)
	}
}

func TestRingBuffer_DrainEmptyClosedReturnsEOF(t *testing.T) {
	t.Parallel()
	r := newRingBuffer("stdout", slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = r.Close()
	n, err := r.drain(io.Discard)
	if err == nil || !errors.Is(err, io.EOF) {
		t.Fatalf("drain empty closed: got (%d, %v); want (0, io.EOF)", n, err)
	}
}

func TestRingBuffer_DrainNilDstUsesDiscard(t *testing.T) {
	t.Parallel()
	r := newRingBuffer("stdout", slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, _ = r.Write([]byte("hello"))
	n, err := r.drain(nil)
	if err != nil || n != 5 {
		t.Fatalf("drain nil dst: got (%d, %v)", n, err)
	}
}

func TestRingBuffer_CloseIdempotent(t *testing.T) {
	t.Parallel()
	r := newRingBuffer("stdout", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := r.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// Forward coalesce path: send one signal that fills the buffer,
// then send another while the goroutine has not drained — the
// second should overwrite (coalesce) without blocking.
func TestForward_CoalescesOnFullBuffer(t *testing.T) {
	t.Parallel()
	c := &Child{
		cfg:       ChildConfig{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))},
		forwardCh: make(chan os.Signal, 1),
	}
	// Pretend a child is live.
	c.cmd = &exec.Cmd{}
	c.pid = 12345
	// Fill the buffer.
	c.forwardCh <- syscall.SIGTERM
	// Second Forward must not block.
	done := make(chan struct{})
	go func() {
		_ = c.Forward(syscall.SIGINT)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("Forward blocked when buffer full — coalesce path broken")
	}
	// Channel should now hold one signal (the most recent).
	if len(c.forwardCh) != 1 {
		t.Fatalf("forwardCh len: want 1 after coalesce, got %d", len(c.forwardCh))
	}
}

// countHandler counts Warn-level records.
type countHandler struct {
	counter *int32
}

func (h *countHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *countHandler) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		atomic.AddInt32(h.counter, 1)
	}
	return nil
}
func (h *countHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *countHandler) WithGroup(_ string) slog.Handler      { return h }
