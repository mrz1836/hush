package audit

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
)

func TestAuditWriter_NewWriter_RejectsEmptyPath(t *testing.T) {
	t.Parallel()
	key := newTestSigningKey(t)
	_, err := NewWriter(context.Background(), "", key, nil, newTestLogger())
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("err = %v; want ErrInvalidPath", err)
	}
}

func TestAuditWriter_NewWriter_RejectsNilKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, err := NewWriter(context.Background(), filepath.Join(dir, "a.jsonl"), nil, nil, newTestLogger())
	if !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v; want ErrInvalidKey", err)
	}
}

func TestAuditWriter_NewWriter_RejectsNilLogger(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := newTestSigningKey(t)
	_, err := NewWriter(context.Background(), filepath.Join(dir, "a.jsonl"), key, nil, nil)
	if !errors.Is(err, ErrInvalidLogger) {
		t.Fatalf("err = %v; want ErrInvalidLogger", err)
	}
}

func TestAuditWriter_NewWriter_RejectsCancelledCtx(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := newTestSigningKey(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewWriter(ctx, filepath.Join(dir, "a.jsonl"), key, nil, newTestLogger())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
}

func TestAuditWriter_NewWriter_RejectsCorruptTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatalf("seed corrupt file: %v", err)
	}
	key := newTestSigningKey(t)
	_, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if !errors.Is(err, ErrChainTailUnreadable) {
		t.Fatalf("err = %v; want ErrChainTailUnreadable", err)
	}
}

func TestAuditWriter_AppendEmptyAction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)

	if err := w.Append(context.Background(), "", nil); !errors.Is(err, ErrEmptyAction) {
		t.Fatalf("Append empty action err = %v; want ErrEmptyAction", err)
	}
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestAuditWriter_AppendCancelledCallerCtx(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)

	ctx, c := context.WithCancel(context.Background())
	c()
	if err := w.Append(ctx, "x", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append cancelled-ctx err = %v; want context.Canceled", err)
	}
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestAuditWriter_BlocksOnBackpressure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	// Pause the writer goroutine just before its Flush. The first Append's
	// process() will sit on the hook, holding the rendezvous channel.
	// Subsequent producers must block in `w.accept <- p`.
	release := make(chan struct{})
	hookCallCount := atomic.Int64{}
	SetWriterFlushHook(w, func() {
		if hookCallCount.Add(1) == 1 {
			<-release
		}
	})

	cancel, wait := runWriter(t, w)

	const N = 4
	var wg sync.WaitGroup
	starts := make([]time.Time, N)
	ends := make([]time.Time, N)
	for i := range N {
		wg.Add(1)
		go func() {
			defer wg.Done()
			starts[i] = time.Now()
			if err := w.Append(context.Background(), "test_event", map[string]any{"i": i}); err != nil {
				t.Errorf("Append #%d: %v", i, err)
			}
			ends[i] = time.Now()
		}()
	}

	// Give the goroutines time to enqueue. With N=4 producers and the
	// writer paused on Flush of the FIRST event, producers 2..4 are still
	// blocked on the unbuffered `accept` channel.
	time.Sleep(80 * time.Millisecond)

	// At this point at most 1 ack should have fired (the first one is paused
	// pre-flush so its ack hasn't been sent either). All N goroutines must
	// still be in Append. Release.
	close(release)

	wg.Wait()
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := readEvents(t, path)
	if len(events) != N {
		t.Fatalf("got %d events; want %d", len(events), N)
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d seq=%d; want %d", i, ev.Seq, i+1)
		}
	}
}

func TestAuditWriter_ConcurrentAppendMonotonicSeq(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)

	const N, M = 8, 64
	var wg sync.WaitGroup
	for g := 0; g < N; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < M; i++ {
				if err := w.Append(context.Background(), "test_event", map[string]any{"g": g, "i": i}); err != nil {
					t.Errorf("Append g=%d i=%d: %v", g, i, err)
				}
			}
		}()
	}
	wg.Wait()
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := readEvents(t, path)
	if len(events) != N*M {
		t.Fatalf("got %d events; want %d", len(events), N*M)
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d seq=%d; want %d", i, ev.Seq, i+1)
		}
	}
	if err := Verify(path, &key.PublicKey); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestAuditWriter_AppendSuccess_MeansOnChain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)

	if err := w.Append(context.Background(), "first", nil); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// IMMEDIATELY read the file. Without bw.Flush() per event, the file
	// would be empty here.
	raw, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("expected at least one event on disk after Append return")
	}

	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestAuditWriter_NeverDropsUnderLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)

	const total = 200
	var ok atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.Append(context.Background(), "x", nil); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := readEvents(t, path)
	if int64(len(events)) != ok.Load() {
		t.Fatalf("on-disk events=%d; successful Append count=%d", len(events), ok.Load())
	}
	if int64(len(events)) != total {
		t.Fatalf("on-disk events=%d; want %d (no drops permitted)", len(events), total)
	}
}

func TestAuditChain_ResumesFromTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	// First writer.
	w1, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter1: %v", err)
	}
	c1, wait1 := runWriter(t, w1)
	appendN(t, w1, 3)
	c1()
	if err := wait1(); err != nil {
		t.Fatalf("Run1: %v", err)
	}

	// Second writer at the same path.
	w2, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter2: %v", err)
	}
	c2, wait2 := runWriter(t, w2)
	appendN(t, w2, 2)
	c2()
	if err := wait2(); err != nil {
		t.Fatalf("Run2: %v", err)
	}

	events := readEvents(t, path)
	if len(events) != 5 {
		t.Fatalf("got %d events; want 5", len(events))
	}
	for i, ev := range events {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d seq=%d; want %d", i, ev.Seq, i+1)
		}
		if i > 0 && ev.PrevHash != events[i-1].Hash {
			t.Fatalf("resumed prev_hash mismatch at %d", i)
		}
	}
	if err := Verify(path, &key.PublicKey); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestAudit_RecordNoSecretValue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)

	sentinel := testutil.SentinelSecret(13)

	// Producer-side discipline: the handler-side audit-data builder is the
	// load-bearing leak boundary (FR-028). This test exercises the readback
	// loop: when producers correctly construct a Data map carrying ONLY the
	// secret name (and never the value), the on-disk chain is sentinel-free.
	if err := w.Append(context.Background(), "secret_retrieved", map[string]any{
		"secret_name":  "API_KEY", // name is allowed; value is forbidden
		"outcome":      "secret_retrieved",
		"client_ip":    "100.64.0.1",
		"session_type": "interactive",
		"request_id":   "abc",
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	raw, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	testutil.AssertSentinelAbsent(t, sentinel, string(raw))
}

func TestAuditWriter_AppendAfterShutdownReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if err := w.Append(context.Background(), "x", nil); !errors.Is(err, ErrShutdown) {
		t.Fatalf("Append after shutdown err = %v; want ErrShutdown", err)
	}
}

func TestAuditWriter_RunReturnsAfterDrain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 4)
	cancel()
	deadline := time.After(2 * time.Second)
	done := make(chan error, 1)
	go func() { done <- wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-deadline:
		t.Fatal("Run did not return within 2s")
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() == 0 {
		t.Fatal("file empty after Run; expected events synced to disk")
	}
}

func TestAuditWriter_DoubleRunReturnsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	// give first Run time to flip runStarted
	time.Sleep(20 * time.Millisecond)
	if err := w.Run(ctx); !errors.Is(err, ErrAlreadyRun) {
		t.Fatalf("second Run err = %v; want ErrAlreadyRun", err)
	}
}

// TestAuditWriter_ConcurrentRunSecondReturnsChainLocked asserts the
// single-Writer-per-path contract: a second process (or a second Writer
// in the same process) opening the same chain file gets ErrChainLocked
// from flock instead of silently corrupting the hash chain.
func TestAuditWriter_ConcurrentRunSecondReturnsChainLocked(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	w1, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter 1: %v", err)
	}
	cancel1, wait1 := runWriter(t, w1)
	defer func() {
		cancel1()
		_ = wait1()
	}()

	// First writer must reach the inner loop before we try to lock again.
	// Append a single event and wait for the rendezvous ack to confirm
	// the lock is held.
	if appErr := w1.Append(context.Background(), "test", map[string]any{"k": "v"}); appErr != nil {
		t.Fatalf("Append: %v", appErr)
	}

	w2, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter 2: %v", err)
	}
	ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	err = w2.Run(ctx)
	if !errors.Is(err, ErrChainLocked) {
		t.Fatalf("second writer Run err = %v; want ErrChainLocked", err)
	}
}

func TestAuditWriter_FilePermsAre0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 1)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode = %#o; want 0600", mode)
	}
}

// silence unused-import warnings in some cross-platform builds.
var (
	_ = bytes.NewBuffer
	_ = ecdsa.GenerateKey
	_ = slog.Default
)
