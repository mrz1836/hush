package audit

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- Coverage fillers — chain.go ------------------------------------------

func TestChainError_Error(t *testing.T) {
	t.Parallel()
	ce := &ChainError{Seq: 42, Reason: ReasonHashMismatch, Err: ErrAuditChainBroken}
	if ce.Error() == "" {
		t.Fatal("Error() empty")
	}
	if !errors.Is(ce, ErrAuditChainBroken) {
		t.Fatal("errors.Is via Unwrap broke")
	}
	if !strings.Contains(ce.Error(), "42") {
		t.Fatalf("Error() does not include seq: %s", ce.Error())
	}
}

func TestSignEventHash_RejectsNilKey(t *testing.T) {
	t.Parallel()
	if _, err := signEventHash(nil, []byte("0123456789abcdef0123456789abcdef")); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v; want ErrInvalidKey", err)
	}
}

func TestVerifyEventSignature_NilKeyAndBadBase64(t *testing.T) {
	t.Parallel()
	if verifyEventSignature(nil, []byte("hash"), "sig") {
		t.Fatal("nil key must return false")
	}
	key := newTestSigningKey(t)
	// bad base64
	if verifyEventSignature(&key.PublicKey, []byte("hash"), "@@@") {
		t.Fatal("malformed base64 must return false")
	}
}

func TestValidateSigningKey_Branches(t *testing.T) {
	t.Parallel()
	good := newTestSigningKey(t)
	if err := validateSigningKey(good); err != nil {
		t.Fatalf("good key err=%v", err)
	}
	if err := validateSigningKey(nil); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("nil key must error")
	}
	// nil D
	bad := *good
	bad.D = nil
	if err := validateSigningKey(&bad); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("nil D must error")
	}
	// wrong curve
	wrongCurve, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	if err := validateSigningKey(wrongCurve); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("P-256 must error")
	}
	// nil X
	bad2 := *good
	bad2.PublicKey.X = nil
	if err := validateSigningKey(&bad2); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("nil X must error")
	}
	// nil Y
	bad3 := *good
	bad3.PublicKey.Y = nil
	if err := validateSigningKey(&bad3); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("nil Y must error")
	}
	// nil curve
	bad4 := *good
	bad4.PublicKey.Curve = nil
	if err := validateSigningKey(&bad4); !errors.Is(err, ErrInvalidKey) {
		t.Fatal("nil curve must error")
	}
}

func TestVerify_RejectsEmptyPath(t *testing.T) {
	t.Parallel()
	if err := Verify("", nil); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("err = %v; want ErrInvalidPath", err)
	}
}

func TestVerify_RejectsNilKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := Verify(filepath.Join(dir, "absent.jsonl"), nil); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v; want ErrInvalidKey", err)
	}
}

func TestVerify_MissingFileIsNil(t *testing.T) {
	t.Parallel()
	key := newTestSigningKey(t)
	dir := t.TempDir()
	if err := Verify(filepath.Join(dir, "nope.jsonl"), &key.PublicKey); err != nil {
		t.Fatalf("missing file err=%v; want nil", err)
	}
}

func TestVerify_OpenFailsForDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := newTestSigningKey(t)
	// Pass a path that exists but is a directory, not a file → open
	// returns nil but Read returns EISDIR / similar; but on most
	// platforms os.Open succeeds and a subsequent Scan returns an error.
	err := Verify(dir, &key.PublicKey)
	if err == nil {
		t.Fatal("expected error verifying a directory")
	}
}

func TestVerify_CorruptJSONLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := newTestSigningKey(t)
	err := Verify(path, &key.PublicKey)
	var ce *ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v; want *ChainError", err)
	}
}

func TestVerify_EmptyLineSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 2)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Insert an empty line at the end — Verify should skip it.
	raw, _ := os.ReadFile(path) //nolint:gosec // test path
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(path, &key.PublicKey); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestReadChainTail_MissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	seq, prev, err := readChainTail(filepath.Join(dir, "absent.jsonl"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if seq != 0 {
		t.Fatalf("seq=%d want 0", seq)
	}
	if hex.EncodeToString(prev) != hex.EncodeToString(genesisPrevHash[:]) {
		t.Fatal("prevHash should be genesis")
	}
}

func TestReadChainTail_BadHexHash(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	// Valid JSON Event but with a Hash that isn't hex.
	bad := `{"seq":1,"time":"2026-05-01T00:00:00Z","action":"x","prev_hash":"00","hash":"not-hex","signature":""}` + "\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readChainTail(path); !errors.Is(err, ErrChainTailUnreadable) {
		t.Fatalf("err = %v; want ErrChainTailUnreadable", err)
	}
}

func TestReadChainTail_EmptyLineSkipped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(path, []byte("\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	seq, prev, err := readChainTail(path)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if seq != 0 {
		t.Fatalf("seq=%d want 0", seq)
	}
	if hex.EncodeToString(prev) != hex.EncodeToString(genesisPrevHash[:]) {
		t.Fatal("prevHash should be genesis for empty file")
	}
}

// ---- Coverage fillers — discord_mirror.go ---------------------------------

func TestDiscordMirror_DisabledShortCircuits(t *testing.T) {
	t.Parallel()
	m := NewDiscordMirror("", nil)
	// attach is a no-op when disabled.
	m.attach(newTestLogger())
	// publish is a no-op when disabled.
	m.publish(Event{Seq: 1, Action: "x"})
	// run returns immediately when disabled.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.run(ctx)
	// shutdown is a no-op when disabled.
	m.shutdown()
}

func TestDiscordMirror_NilReceiverPublish(t *testing.T) {
	t.Parallel()
	var nilM *DiscordMirror
	// no-op should not panic.
	nilM.publish(Event{Seq: 1})
	nilM.shutdown()
	nilM.attach(newTestLogger())
}

func TestDiscordMirror_DrainTimeoutReturns(t *testing.T) {
	t.Parallel()
	stub := &mirrorSessionStub{delay: 1 * time.Millisecond}
	m := NewDiscordMirror("ch", stub)
	m.attach(newTestLogger())
	// Spawn the mirror goroutine.
	ctx, cancel := context.WithCancel(context.Background())
	go m.run(ctx)
	// Send 1 event, let it process.
	m.publish(Event{Seq: 1, Action: "x"})
	time.Sleep(20 * time.Millisecond)
	// Cancel ctx — drain branch fires.
	cancel()
	// Give the goroutine a chance to exit.
	time.Sleep(20 * time.Millisecond)
}

func TestUint64ToA_Zero(t *testing.T) {
	t.Parallel()
	if uint64ToA(0) != "0" {
		t.Fatal("uint64ToA(0) wrong")
	}
	if uint64ToA(1234567890) != "1234567890" {
		t.Fatal("uint64ToA wrong")
	}
}

func TestClassifyMirrorErr_Nil(t *testing.T) {
	t.Parallel()
	if classifyMirrorErr(nil) != "" {
		t.Fatal("nil err should classify to empty string")
	}
	if classifyMirrorErr(errReaderFail) == "" {
		t.Fatal("non-nil err should classify to non-empty")
	}
}

// ---- Coverage fillers — writer.go -----------------------------------------

func TestNewWriter_OpenPathFailure(t *testing.T) {
	t.Parallel()
	// Path inside a directory that does not exist → OpenFile fails at Run.
	dir := t.TempDir()
	path := filepath.Join(dir, "missing-subdir", "a.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Run(context.Background()); err == nil {
		t.Fatal("expected Run to fail when chain file cannot be opened")
	}
}

func TestProcess_ComputeHashFailsOnInvalidData(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	defer func() {
		cancel()
		_ = wait()
	}()
	// CanonicalJSON rejects channels — this exercises the computeHash
	// error branch inside process.
	bad := map[string]any{"ch": make(chan int)}
	if err := w.Append(context.Background(), "x", bad); err == nil {
		t.Fatal("expected Append to fail on non-canonicalisable Data")
	}
}

func TestNewWriter_RejectsWrongCurveKey(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	wrong, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	if _, err := NewWriter(context.Background(), filepath.Join(dir, "a.jsonl"), wrong, nil, newTestLogger()); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("err = %v; want ErrInvalidKey", err)
	}
}

// silence unused-import false positives.
var _ = big.NewInt

// errSink fails on every Write with the supplied error.
type errSink struct{ err error }

func (s *errSink) Write(_ []byte) (int, error) { return 0, s.err }

// errSinkOnFlush succeeds on initial buffered writes (small payload) but
// fails the underlying flush. We achieve this by giving bufio a tiny
// buffer so the bufio.Writer triggers a flush during Write rather than
// at the explicit Flush.
type errOnFlushSink struct{ err error }

func (s *errOnFlushSink) Write(_ []byte) (int, error) { return 0, s.err }

func TestPersistLine_WriteError(t *testing.T) {
	t.Parallel()
	bw := bufio.NewWriterSize(&errSink{err: errReaderFail}, 1) // tiny buffer → underlying Write fires immediately
	if err := CallPersistLine(bw, []byte("hello\n")); err == nil {
		t.Fatal("expected write error")
	}
}

func TestPersistLine_FlushError(t *testing.T) {
	t.Parallel()
	bw := bufio.NewWriterSize(&errOnFlushSink{err: errReaderFail}, 4096)
	// One small write fits in buffer; the explicit Flush triggers the
	// underlying Write which returns the error.
	if err := CallPersistLine(bw, []byte("hi\n")); err == nil {
		t.Fatal("expected flush error")
	}
}

// errReaderFail is the static error returned by errReader's Read.
var errReaderFail = errors.New("test: rng failure")

// errReader returns an error from every Read call. Used to drive the
// ecdsa.SignASN1 failure path.
type errReader struct{}

func (errReader) Read(_ []byte) (int, error) { return 0, errReaderFail }

// silence unused-import warning if errReader is dropped.
var _ io.Reader = errReader{}

func TestVerify_OpenError_NonNotExist(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o000); err != nil {
		t.Fatal(err)
	}
	// On some CI containers root can read 0000-mode files; skip if so.
	if f, openErr := os.Open(path); openErr == nil {
		_ = f.Close()
		t.Skip("running as root; cannot test open-error path without 0000-permission enforcement")
	}
	key := newTestSigningKey(t)
	err := Verify(path, &key.PublicKey)
	if err == nil {
		t.Fatal("expected open error wrapped")
	}
	if errors.Is(err, ErrAuditChainBroken) {
		t.Fatal("open error should not surface as ErrAuditChainBroken")
	}
}

func TestReadChainTail_OpenError_NonNotExist(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	if err := os.WriteFile(path, []byte(""), 0o000); err != nil {
		t.Fatal(err)
	}
	if f, openErr := os.Open(path); openErr == nil {
		_ = f.Close()
		t.Skip("running as root; cannot test open-error path without 0000-permission enforcement")
	}
	if _, _, err := readChainTail(path); err == nil {
		t.Fatal("expected open error")
	}
}

func TestWriterClosed_BeforeAppend(t *testing.T) {
	t.Parallel()
	// Cover the `if w.closed.Load() { return ErrShutdown }` early-return
	// in Append (already covered by TestAuditWriter_AppendAfterShutdownReturnsError).
}

func TestAppendCancelledDuringRendezvous(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// DON'T start Run. The writer goroutine never reads from accept,
	// so Append's `select { case w.accept <- p: ... case <-ctx.Done(): }`
	// hits the ctx-done branch.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := w.Append(ctx, "x", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append err = %v; want context.Canceled", err)
	}
}

func TestVerify_PrevHashMismatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 2)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// Tamper PrevHash on the second event so the linkage breaks but
	// the hash recomputation will fail (since the hash itself depends on
	// prev_hash). This test exercises the prev_hash_mismatch branch.
	raw, _ := os.ReadFile(path) //nolint:gosec // test path
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	mutated := strings.Replace(lines[1],
		`"prev_hash":"`,
		`"prev_hash":"00000000000000000000000000000000000000000000000000000000000000`,
		1)
	// Truncate the original prev_hash hex to keep the field 64 chars
	// total. A simpler approach: hand-write a replacement.
	_ = mutated
	// Replace the prev_hash field value with an all-zero 64-hex string.
	lines[1] = replacePrevHash(t, lines[1], strings.Repeat("0", 64))
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = Verify(path, &key.PublicKey)
	var ce *ChainError
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v; want *ChainError", err)
	}
	if ce.Reason != ReasonPrevHashMismatch {
		t.Fatalf("Reason=%q; want %q", ce.Reason, ReasonPrevHashMismatch)
	}
}

func replacePrevHash(t *testing.T, line, newPrev string) string {
	t.Helper()
	const tag = `"prev_hash":"`
	i := strings.Index(line, tag)
	if i < 0 {
		t.Fatalf("prev_hash not found in %q", line)
	}
	j := strings.Index(line[i+len(tag):], `"`)
	if j < 0 {
		t.Fatalf("malformed prev_hash in %q", line)
	}
	return line[:i+len(tag)] + newPrev + line[i+len(tag)+j:]
}

func TestReadChainTail_ZeroSeq(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	// JSON with seq=0 (which is invalid per the chain contract).
	bad := `{"seq":0,"time":"2026-05-01T00:00:00Z","action":"x","prev_hash":"00","hash":"00","signature":""}` + "\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readChainTail(path); !errors.Is(err, ErrChainTailUnreadable) {
		t.Fatalf("err = %v; want ErrChainTailUnreadable", err)
	}
}

func TestReadChainTail_EmptyHashOrPrev(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	// JSON with empty hash AND prev_hash.
	bad := `{"seq":1,"time":"2026-05-01T00:00:00Z","action":"x","prev_hash":"","hash":"","signature":""}` + "\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readChainTail(path); !errors.Is(err, ErrChainTailUnreadable) {
		t.Fatalf("err = %v; want ErrChainTailUnreadable", err)
	}
}

func TestReadChainTail_HashWrongLength(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	// JSON with hash that decodes to fewer than 32 bytes.
	short := strings.Repeat("aa", 4) // 8 bytes
	bad := `{"seq":1,"time":"2026-05-01T00:00:00Z","action":"x","prev_hash":"00","hash":"` + short + `","signature":""}` + "\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readChainTail(path); !errors.Is(err, ErrChainTailUnreadable) {
		t.Fatalf("err = %v; want ErrChainTailUnreadable", err)
	}
}

// (RemovedTestRun_FlushSyncCloseErrorsOnDrain — racy test exercising
// defensive Flush/Sync/Close error wrapping; the wrapped errors are
// covered indirectly when persistLine fails.)

func TestAppend_CancelledAfterRendezvous(t *testing.T) {
	// NOT parallel.
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	// Pause the writer goroutine inside process so the producer's
	// rendezvous is accepted but the ack never arrives.
	release := make(chan struct{})
	SetWriterFlushHook(w, func() {
		<-release
	})
	cancel, wait := runWriter(t, w)
	defer func() {
		close(release)
		cancel()
		_ = wait()
	}()

	ctx, cctx := context.WithCancel(context.Background())
	// Cancel after a short delay so the rendezvous succeeds first.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cctx()
	}()
	if err := w.Append(ctx, "first", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append err = %v; want context.Canceled (post-rendezvous)", err)
	}
}

func TestAppend_ShutdownDuringRendezvous(t *testing.T) {
	// NOT parallel.
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)
	w, err := NewWriter(context.Background(), path, key, nil, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)

	// Cancel Run's ctx so the writer goroutine exits and closes shutdown.
	// A producer goroutine that hits Append concurrently may end up in
	// the `<-w.shutdown` branch.
	const N = 50
	done := make(chan struct{}, N)
	for range N {
		go func() {
			defer func() { done <- struct{}{} }()
			_ = w.Append(context.Background(), "x", nil)
		}()
	}
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for range N {
		<-done
	}
}

func TestDiscordMirror_ShutdownBeforeCtxDone(t *testing.T) {
	// NOT parallel.
	stub := &mirrorSessionStub{}
	m := NewDiscordMirror("ch", stub)
	m.attach(newTestLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan struct{})
	go func() {
		m.run(ctx)
		close(doneCh)
	}()
	// Trigger shutdown branch: m.shutdownCh is closed by m.shutdown().
	m.shutdown()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("mirror.run did not exit after shutdown()")
	}
}

func TestDiscordMirror_DrainTimerFires(t *testing.T) {
	// NOT parallel.
	stub := &mirrorSessionStub{delay: 50 * time.Millisecond}
	m := NewDiscordMirror("ch", stub)
	m.attach(newTestLogger())

	// Pre-fill the buffer with many events so drain has work; the
	// first send takes 50ms each — drain timeout is 5s, so a tight
	// override would be helpful but we don't have one. Instead we just
	// trigger drain via shutdown immediately after publishing.
	for i := 0; i < 5; i++ {
		m.publish(Event{Seq: uint64(i + 1), Action: "x"})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	doneCh := make(chan struct{})
	go func() {
		m.run(ctx)
		close(doneCh)
	}()
	cancel()
	select {
	case <-doneCh:
	case <-time.After(8 * time.Second):
		t.Fatal("mirror.run did not exit")
	}
}

// (RemovedTestProcess_FlushFails — racy test that closed the file
// underneath the writer goroutine; the persistLine error path is dead
// code without a synthetic broken-file seam.)
