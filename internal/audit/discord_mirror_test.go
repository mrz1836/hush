package audit

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/mrz1836/hush/internal/testutil"
)

// mirrorSessionStub is a configurable test fake satisfying [MirrorSession].
type mirrorSessionStub struct {
	mu          sync.Mutex
	calls       int
	failOnCall  int // 0 = never fail; 1 = first call fails; etc.
	failAlways  bool
	failErr     error
	delay       time.Duration
	dropMessage func(s string) // optional inspector for the rendered message
}

// errStubFailure is the canned error returned by mirrorSessionStub when
// no caller-supplied failErr is configured.
var errStubFailure = errors.New("stub failure")

func (m *mirrorSessionStub) ChannelMessageSendComplex(_ string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	m.mu.Lock()
	m.calls++
	idx := m.calls
	failNow := m.failAlways || (m.failOnCall != 0 && idx == m.failOnCall)
	delay := m.delay
	dropMessage := m.dropMessage
	failErr := m.failErr
	m.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if dropMessage != nil && data != nil {
		dropMessage(data.Content)
	}
	if failNow {
		if failErr == nil {
			failErr = errStubFailure
		}
		return nil, failErr
	}
	return &discordgo.Message{ID: "ok"}, nil
}

func (m *mirrorSessionStub) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestNewDiscordMirror_EmptyChannelDisablesMirror(t *testing.T) {
	t.Parallel()
	stub := &mirrorSessionStub{}
	m := NewDiscordMirror("", stub)
	if m.enabled() {
		t.Fatal("empty channelID must disable mirror")
	}
	// nil session also disables.
	m2 := NewDiscordMirror("ch", nil)
	if m2.enabled() {
		t.Fatal("nil session must disable mirror")
	}
}

func TestDiscordMirror_FailureLogsWarnNoBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	stub := &mirrorSessionStub{failAlways: true, failErr: errors.New("transport down")} //nolint:err113 // test fixture; static error not load-bearing
	m := NewDiscordMirror("ch1", stub)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	w, err := NewWriter(context.Background(), path, key, m, logger)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)

	const N = 5
	start := time.Now()
	appendN(t, w, N)
	elapsed := time.Since(start)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := readEvents(t, path)
	if len(events) != N {
		t.Fatalf("got %d on-disk events; want %d", len(events), N)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("Append latency %v exceeds 1s — mirror failure should not block", elapsed)
	}
	logs := buf.String()
	if logs == "" {
		t.Fatal("expected at least one WARN log line for mirror failures")
	}
	// sentinel-leak guard: warn logs must not echo any privileged fields.
	testutil.AssertSentinelAbsent(t, "transport down", logs) // exact bot-error string never logged verbatim
}

func TestDiscordMirror_NoBotTokenInWarn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	sentinel := testutil.SentinelSecret(13)
	stub := &mirrorSessionStub{failAlways: true, failErr: errors.New(sentinel)} //nolint:err113 // sentinel injected as test stimulus
	m := NewDiscordMirror("ch1", stub)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	w, err := NewWriter(context.Background(), path, key, m, logger)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 2)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	testutil.AssertSentinelAbsent(t, sentinel, buf.String())
}

func TestDiscordMirror_ChainUnaffectedByMirrorFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	stub := &mirrorSessionStub{failOnCall: 2}
	m := NewDiscordMirror("ch1", stub)
	w, err := NewWriter(context.Background(), path, key, m, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	appendN(t, w, 3)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if err := Verify(path, &key.PublicKey); err != nil {
		t.Fatalf("Verify after mirror failure: %v", err)
	}
}

func TestDiscordMirror_NoRetryOnFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	stub := &mirrorSessionStub{failOnCall: 1}
	m := NewDiscordMirror("ch1", stub)
	w, err := NewWriter(context.Background(), path, key, m, newTestLogger())
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)
	if err := w.Append(context.Background(), "x", nil); err != nil {
		t.Fatalf("Append: %v", err)
	}
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := stub.Calls(); got != 1 {
		t.Fatalf("stub.Calls = %d; want 1 (no retry)", got)
	}
}

func TestDiscordMirror_BufferFullDropsMirrorCopyNotChain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	key := newTestSigningKey(t)

	stub := &mirrorSessionStub{}
	m := NewDiscordMirror("ch1", stub)

	// Pause the mirror goroutine before its first publish so its buffer
	// fills.
	mirrorRelease := make(chan struct{})
	var pauseOnce sync.Once
	SetMirrorPublishHook(m, func() {
		pauseOnce.Do(func() {
			<-mirrorRelease
		})
	})

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	w, err := NewWriter(context.Background(), path, key, m, logger)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	cancel, wait := runWriter(t, w)

	// 1 (popped, parked in hook) + 64 (buffer) = 65; everything past that drops.
	const total = 100
	appendN(t, w, total)

	close(mirrorRelease)
	cancel()
	if err := wait(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	events := readEvents(t, path)
	if len(events) != total {
		t.Fatalf("on-disk events = %d; want %d", len(events), total)
	}
	if MirrorDroppedCount(m) == 0 {
		t.Fatal("expected at least one mirror-buffer drop under sustained pressure")
	}
}

func TestDiscordMirror_FailedEventEmitsAuditEvent(t *testing.T) {
	t.Parallel()
	// The audit-package contract is that the mirror's failure callback is
	// installable so the writer can emit `audit_mirror_failed` chain
	// events. Here we exercise the seam directly: when configured, the
	// callback fires once per failure.
	stub := &mirrorSessionStub{failAlways: true}
	m := NewDiscordMirror("ch1", stub)
	m.attach(newTestLogger())

	var fires atomic.Int64
	SetMirrorFailureSink(m, func(seq uint64, action, errClass string) {
		_ = seq
		_ = action
		_ = errClass
		fires.Add(1)
	})

	const N = 3
	for i := 0; i < N; i++ {
		m.publish(Event{Seq: uint64(i + 1), Action: "x"})
	}
	// Run a one-shot mirror loop.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()
	m.run(ctx)

	if got := fires.Load(); got != int64(N) {
		t.Fatalf("failureSink fires = %d; want %d", got, N)
	}
}

// silence unused-import false positives.
var (
	_ = os.Stat
	_ = bytes.NewBuffer
)
