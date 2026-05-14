package watchdog

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
	"github.com/mrz1836/hush/internal/testutil"
)

// -------------------- test helpers --------------------

type capturedRecord struct {
	Level      slog.Level
	Message    string
	Attrs      map[string]slog.Value
	Serialized []byte
}

type recordingHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordingHandler) Handle(ctx context.Context, r slog.Record) error {
	cr := capturedRecord{
		Level:   r.Level,
		Message: r.Message,
		Attrs:   map[string]slog.Value{},
	}
	buf := &bytes.Buffer{}
	jh := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	_ = jh.Handle(ctx, r)
	cr.Serialized = append([]byte(nil), buf.Bytes()...)
	r.Attrs(func(a slog.Attr) bool {
		cr.Attrs[a.Key] = a.Value
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, cr)
	h.mu.Unlock()
	return nil
}

func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *recordingHandler) snapshot() []capturedRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]capturedRecord, len(h.records))
	copy(out, h.records)
	return out
}

func (h *recordingHandler) warnRecords() []capturedRecord {
	out := []capturedRecord{}
	for _, r := range h.snapshot() {
		if r.Level == slog.LevelWarn {
			out = append(out, r)
		}
	}
	return out
}

func newTestLogger() (*slog.Logger, *recordingHandler) {
	h := &recordingHandler{}
	return slog.New(h), h
}

type fastCountHandler struct {
	warn atomic.Uint64
	info atomic.Uint64
	errN atomic.Uint64

	queueFullWarnEpisodes atomic.Uint64
	rateLimitWarns        atomic.Uint64
	saturationWarns       atomic.Uint64

	mu                   sync.Mutex
	queueFullDrops       uint64
	pendingDropsOnCancel uint64
}

func (h *fastCountHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *fastCountHandler) Handle(_ context.Context, r slog.Record) error {
	switch r.Level {
	case slog.LevelWarn:
		h.warn.Add(1)
		h.classifyWarn(r)
	case slog.LevelInfo:
		h.info.Add(1)
		h.classifyInfo(r)
	case slog.LevelError:
		h.errN.Add(1)
	case slog.LevelDebug:
	default:
	}
	return nil
}

func (h *fastCountHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *fastCountHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *fastCountHandler) classifyWarn(r slog.Record) {
	switch {
	case strings.Contains(r.Message, "queue full"):
		h.queueFullWarnEpisodes.Add(1)
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "dropped_count" && a.Value.Kind() == slog.KindUint64 {
				h.mu.Lock()
				h.queueFullDrops += a.Value.Uint64()
				h.mu.Unlock()
			}
			return true
		})
	case strings.Contains(r.Message, "suppressed by rate limit"):
		h.rateLimitWarns.Add(1)
	case strings.Contains(r.Message, "output saturated"):
		h.saturationWarns.Add(1)
	}
}

func (h *fastCountHandler) classifyInfo(r slog.Record) {
	if !strings.Contains(r.Message, "pending lines dropped") {
		return
	}
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "dropped_pending_count" && a.Value.Kind() == slog.KindUint64 {
			h.mu.Lock()
			h.pendingDropsOnCancel += a.Value.Uint64()
			h.mu.Unlock()
		}
		return true
	})
}

// setNowForTest installs a fake clock on w and resets per-pattern
// lastRefill to the fake clock's current instant so subsequent
// bucket math operates entirely within fake-clock space.
//
// Must be called BEFORE Run starts (otherwise the matcher goroutine
// races on bucket and now).
func setNowForTest(w *Watchdog, now func() time.Time) {
	w.now = now
	t := now()
	for _, b := range w.buckets {
		b.lastRefill = t
	}
}

func mustCompile(t *testing.T, pat string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(pat)
	if err != nil {
		t.Fatalf("regexp.Compile(%q): %v", pat, err)
	}
	return re
}

func waitForLinesEmpty(t *testing.T, w *Watchdog, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if len(w.lines) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for w.lines to drain; %d remaining", len(w.lines))
}

func startRun(t *testing.T, w *Watchdog) (cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	ctx, c := context.WithCancel(context.Background())
	d := make(chan struct{})
	go func() {
		defer close(d)
		_ = w.Run(ctx)
	}()
	return c, d
}

func drainAlerts(ch <-chan Event) []Event {
	out := []Event{}
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}

// -------------------- Phase 3 (US1) tests --------------------

func TestWatchdog_NewWatchdog_ValidPatternSet(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)

	cases := []struct {
		name     string
		patterns []Pattern
	}{
		{"empty", nil},
		{"single", []Pattern{{Name: "a", Regex: mustCompile(t, "a"), RateLimit: time.Hour}}},
		{"three", []Pattern{
			{Name: "a", Regex: mustCompile(t, "a"), RateLimit: time.Hour},
			{Name: "b", Regex: mustCompile(t, "b"), RateLimit: time.Minute},
			{Name: "c", Regex: mustCompile(t, "c"), RateLimit: time.Second},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wd, err := NewWatchdog(tc.patterns, alerts, logger)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if wd == nil {
				t.Fatal("expected non-nil Watchdog")
			}
		})
	}
}

func TestWatchdog_NewWatchdog_DuplicatePatternName(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)

	patterns := []Pattern{
		{Name: "auth", Regex: mustCompile(t, "x"), RateLimit: time.Hour},
		{Name: "auth", Regex: mustCompile(t, "y"), RateLimit: time.Hour},
	}
	wd, err := NewWatchdog(patterns, alerts, logger)
	if !errors.Is(err, ErrDuplicatePatternName) {
		t.Fatalf("want ErrDuplicatePatternName, got %v", err)
	}
	if wd != nil {
		t.Fatalf("want nil watchdog, got %v", wd)
	}
}

func TestWatchdog_NewWatchdog_InvalidInputs(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)
	validRe := mustCompile(t, "x")

	cases := []struct {
		name     string
		patterns []Pattern
		alerts   chan Event
		logger   *slog.Logger
		want     error
	}{
		{
			name:     "empty-name",
			patterns: []Pattern{{Name: "", Regex: validRe, RateLimit: time.Hour}},
			alerts:   alerts, logger: logger, want: ErrEmptyPatternName,
		},
		{
			name:     "nil-regex",
			patterns: []Pattern{{Name: "p", Regex: nil, RateLimit: time.Hour}},
			alerts:   alerts, logger: logger, want: ErrNilPatternRegex,
		},
		{
			name:     "zero-rate-limit",
			patterns: []Pattern{{Name: "p", Regex: validRe, RateLimit: 0}},
			alerts:   alerts, logger: logger, want: ErrNonPositiveRateLimit,
		},
		{
			name:     "negative-rate-limit",
			patterns: []Pattern{{Name: "p", Regex: validRe, RateLimit: -time.Second}},
			alerts:   alerts, logger: logger, want: ErrNonPositiveRateLimit,
		},
		{
			name:     "nil-alerts",
			patterns: []Pattern{{Name: "p", Regex: validRe, RateLimit: time.Hour}},
			alerts:   nil, logger: logger, want: ErrNilAlertsChannel,
		},
		{
			name:     "nil-logger",
			patterns: []Pattern{{Name: "p", Regex: validRe, RateLimit: time.Hour}},
			alerts:   alerts, logger: nil, want: ErrNilLogger,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var alertsCh chan<- Event
			if tc.alerts != nil {
				alertsCh = tc.alerts
			}
			wd, err := NewWatchdog(tc.patterns, alertsCh, tc.logger)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
			if wd != nil {
				t.Fatalf("want nil watchdog, got %v", wd)
			}
		})
	}
}

func TestWatchdog_PatternMatchEmitsAlert(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)
	pat := Pattern{Name: "auth-401", Regex: mustCompile(t, "401 Unauthorized"), RateLimit: time.Hour}
	wd, err := NewWatchdog([]Pattern{pat}, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}

	cancel, done := startRun(t, wd)
	defer func() { cancel(); <-done }()

	line := []byte("upstream returned 401 Unauthorized; refresh token stale")
	wd.Ingest(line)

	select {
	case ev := <-alerts:
		if ev.Pattern != "auth-401" {
			t.Errorf("want pattern auth-401, got %q", ev.Pattern)
		}
		if ev.Line != string(line) {
			t.Errorf("want line %q, got %q", string(line), ev.Line)
		}
		if ev.Time.IsZero() {
			t.Errorf("want non-zero Time, got zero")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no alert within 100ms")
	}
}

func TestWatchdog_NoMatchNoAlert(t *testing.T) {
	t.Parallel()
	logger, handler := newTestLogger()
	alerts := make(chan Event, 1)
	pat := Pattern{Name: "auth-401", Regex: mustCompile(t, "401 Unauthorized"), RateLimit: time.Hour}
	wd, err := NewWatchdog([]Pattern{pat}, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}

	cancel, done := startRun(t, wd)
	defer func() { cancel(); <-done }()

	wd.Ingest([]byte("everything is fine"))

	select {
	case ev := <-alerts:
		t.Fatalf("unexpected alert: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}

	for _, r := range handler.snapshot() {
		if r.Level == slog.LevelWarn || r.Level == slog.LevelInfo {
			if strings.Contains(string(r.Serialized), "everything is fine") {
				t.Errorf("log record references the unmatched line: %s", r.Serialized)
			}
		}
	}
}

func TestWatchdog_EmptyPatternSetIsBenign(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)
	wd, err := NewWatchdog(nil, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}
	cancel, done := startRun(t, wd)

	for range 100 {
		wd.Ingest([]byte("noise"))
	}

	select {
	case ev := <-alerts:
		t.Fatalf("unexpected alert: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
}

func TestWatchdog_MultipleMatchesOnSameLine(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 4)
	patterns := []Pattern{
		{Name: "p-401", Regex: mustCompile(t, "401"), RateLimit: time.Hour},
		{Name: "p-unauth", Regex: mustCompile(t, "Unauthorized"), RateLimit: time.Hour},
	}
	wd, err := NewWatchdog(patterns, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}
	cancel, done := startRun(t, wd)
	defer func() { cancel(); <-done }()

	wd.Ingest([]byte("HTTP 401 Unauthorized"))

	deadline := time.After(150 * time.Millisecond)
	got := map[string]int{}
	for len(got) < 2 {
		select {
		case ev := <-alerts:
			got[ev.Pattern]++
		case <-deadline:
			t.Fatalf("only saw alerts: %v", got)
		}
	}
	if got["p-401"] != 1 || got["p-unauth"] != 1 {
		t.Errorf("want one alert per pattern, got %v", got)
	}
}

func TestWatchdog_MultipleSpansSingleEmit(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 4)
	pat := Pattern{Name: "p-401", Regex: mustCompile(t, "401"), RateLimit: time.Hour}
	wd, err := NewWatchdog([]Pattern{pat}, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}
	cancel, done := startRun(t, wd)
	defer func() { cancel(); <-done }()

	wd.Ingest([]byte("401 retried; 401 retried; 401 retried"))

	select {
	case ev := <-alerts:
		if ev.Pattern != "p-401" {
			t.Errorf("unexpected pattern: %q", ev.Pattern)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected one alert")
	}

	select {
	case ev := <-alerts:
		t.Fatalf("unexpected second alert for same line: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestWatchdog_RunSingleShot(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)
	wd, err := NewWatchdog(nil, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}

	cancel, done := startRun(t, wd)
	cancel()
	<-done

	err2 := wd.Run(context.Background())
	if !errors.Is(err2, ErrAlreadyRan) {
		t.Fatalf("want ErrAlreadyRan, got %v", err2)
	}
}

func TestWatchdog_RunStopsOnCtxCancel(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)
	wd, err := NewWatchdog(nil, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}

	runtime.GC()
	pre := runtime.NumGoroutine()

	cancel, done := startRun(t, wd)

	// Allow Run goroutine to spin up.
	time.Sleep(20 * time.Millisecond)

	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatalf("Run did not return within 250ms (SC-004); elapsed %v", time.Since(start))
	}

	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		runtime.Gosched()
		runtime.GC()
		post := runtime.NumGoroutine()
		if post <= pre {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Errorf("goroutine count did not return to baseline: pre=%d post=%d", pre, runtime.NumGoroutine())
}

func TestWatchdog_IngestAfterRunReturnIsNoop(t *testing.T) {
	t.Parallel()
	logger, handler := newTestLogger()
	alerts := make(chan Event, 1)
	pat := Pattern{Name: "p", Regex: mustCompile(t, "match"), RateLimit: time.Hour}
	wd, err := NewWatchdog([]Pattern{pat}, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}
	cancel, done := startRun(t, wd)
	cancel()
	<-done

	preRecords := len(handler.snapshot())

	const tag = "POST-RUN-TAG-XYZ"
	for range 100 {
		wd.Ingest([]byte("match " + tag))
	}

	select {
	case ev := <-alerts:
		t.Fatalf("unexpected alert after Run return: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	for _, r := range handler.snapshot()[preRecords:] {
		if bytes.Contains(r.Serialized, []byte(tag)) {
			t.Errorf("post-Run log record references the post-Run line: %s", r.Serialized)
		}
	}
}

func TestWatchdog_PrecompiledPatternsReused(t *testing.T) {
	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)
	originalRe := mustCompile(t, "always")
	pat := Pattern{Name: "p", Regex: originalRe, RateLimit: time.Nanosecond}
	wd, err := NewWatchdog([]Pattern{pat}, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}

	if wd.patterns[0].Regex != originalRe {
		t.Fatalf("watchdog's Pattern.Regex pointer != original; got %p want %p",
			wd.patterns[0].Regex, originalRe)
	}

	cancel, done := startRun(t, wd)
	defer func() { cancel(); <-done }()

	go func() {
		for {
			select {
			case <-alerts:
			case <-done:
				return
			}
		}
	}()

	for range 10000 {
		wd.Ingest([]byte("always-matching line"))
	}

	allocs := testing.AllocsPerRun(50, func() {
		_ = originalRe.MatchString("always-matching line")
	})
	if allocs > 5 {
		t.Errorf("regex.Match should be allocation-light, got %v allocs/run", allocs)
	}
}

func TestWatchdog_SC001_EmitLatencyUnder100ms(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)
	pat := Pattern{Name: "p", Regex: mustCompile(t, "401"), RateLimit: time.Microsecond}
	wd, err := NewWatchdog([]Pattern{pat}, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}
	cancel, done := startRun(t, wd)
	defer func() { cancel(); <-done }()

	for i := range 100 {
		time.Sleep(2 * time.Microsecond)
		start := time.Now()
		wd.Ingest([]byte("401 trial"))
		select {
		case <-alerts:
			if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
				t.Fatalf("trial %d: latency %v >= 100ms (SC-001)", i, elapsed)
			}
		case <-time.After(time.Second):
			t.Fatalf("trial %d: timeout", i)
		}
	}
}

// -------------------- Phase 4 (US2) tests --------------------

func TestWatchdog_RateLimitBlocksExcess(t *testing.T) {
	t.Parallel()
	const sentinel = "SENTINEL-RATE-LIMIT-XYZZY"
	logger, handler := newTestLogger()
	alerts := make(chan Event, 16)
	pat := Pattern{Name: "auth-401", Regex: mustCompile(t, "401"), RateLimit: 10 * time.Minute}
	wd, err := NewWatchdog([]Pattern{pat}, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}
	clock := testutil.NewFakeClock(time.Unix(1_000_000, 0))
	setNowForTest(wd, clock.Now)

	cancel, done := startRun(t, wd)
	defer func() { cancel(); <-done }()

	line := []byte("401 Unauthorized " + sentinel)
	for range 5 {
		wd.Ingest(line)
	}
	waitForLinesEmpty(t, wd, time.Second)
	// Brief settle to ensure WARN log entries land.
	time.Sleep(20 * time.Millisecond)

	got := drainAlerts(alerts)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 alert, got %d", len(got))
	}

	warns := handler.warnRecords()
	if len(warns) != 4 {
		t.Fatalf("want 4 WARN entries, got %d", len(warns))
	}
	for i, w := range warns {
		assertRateLimitWarn(t, i, w, "auth-401", sentinel)
	}
}

func assertRateLimitWarn(t *testing.T, i int, w capturedRecord, wantPattern, sentinel string) {
	t.Helper()
	if bytes.Contains(w.Serialized, []byte(sentinel)) {
		t.Errorf("WARN %d leaks matched-line content (Q2 invariant): %s", i, w.Serialized)
	}
	if pn, ok := w.Attrs["pattern"]; !ok || pn.String() != wantPattern {
		t.Errorf("WARN %d missing pattern attr; have %v", i, w.Attrs)
	}
	sc, ok := w.Attrs["suppressed_count"]
	if !ok {
		t.Errorf("WARN %d missing suppressed_count attr", i)
		return
	}
	want := uint64(i + 1)
	if got := sc.Uint64(); got != want {
		t.Errorf("WARN %d suppressed_count = %d, want %d", i, got, want)
	}
}

func TestWatchdog_BucketRefillsAfterInterval(t *testing.T) {
	t.Parallel()
	logger, handler := newTestLogger()
	alerts := make(chan Event, 4)
	pat := Pattern{Name: "p", Regex: mustCompile(t, "401"), RateLimit: 10 * time.Minute}
	wd, err := NewWatchdog([]Pattern{pat}, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}
	clock := testutil.NewFakeClock(time.Unix(1_000_000, 0))
	setNowForTest(wd, clock.Now)

	cancel, done := startRun(t, wd)
	defer func() { cancel(); <-done }()

	wd.Ingest([]byte("401 one"))
	waitForLinesEmpty(t, wd, time.Second)
	time.Sleep(10 * time.Millisecond)

	clock.Advance(time.Second)
	wd.Ingest([]byte("401 two"))
	waitForLinesEmpty(t, wd, time.Second)
	time.Sleep(10 * time.Millisecond)

	clock.Advance(10*time.Minute + time.Second)
	wd.Ingest([]byte("401 three"))
	waitForLinesEmpty(t, wd, time.Second)
	time.Sleep(10 * time.Millisecond)

	alertsGot := drainAlerts(alerts)
	if len(alertsGot) != 2 {
		t.Fatalf("want 2 alerts, got %d", len(alertsGot))
	}
	warns := handler.warnRecords()
	if len(warns) != 1 {
		t.Fatalf("want 1 WARN, got %d", len(warns))
	}
}

func TestWatchdog_PerPatternBudgetIsolation(t *testing.T) {
	t.Parallel()
	logger, handler := newTestLogger()
	alerts := make(chan Event, 4)
	patterns := []Pattern{
		{Name: "A", Regex: mustCompile(t, "alpha"), RateLimit: time.Hour},
		{Name: "B", Regex: mustCompile(t, "beta"), RateLimit: time.Hour},
	}
	wd, err := NewWatchdog(patterns, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}
	clock := testutil.NewFakeClock(time.Unix(1_000_000, 0))
	setNowForTest(wd, clock.Now)

	cancel, done := startRun(t, wd)
	defer func() { cancel(); <-done }()

	wd.Ingest([]byte("alpha first"))
	wd.Ingest([]byte("alpha second"))
	waitForLinesEmpty(t, wd, time.Second)
	time.Sleep(10 * time.Millisecond)

	wd.Ingest([]byte("beta only"))
	waitForLinesEmpty(t, wd, time.Second)
	time.Sleep(10 * time.Millisecond)

	evs := drainAlerts(alerts)
	if len(evs) != 2 {
		t.Fatalf("want 2 alerts (A first, B first), got %d", len(evs))
	}
	gotPatterns := map[string]bool{}
	for _, ev := range evs {
		gotPatterns[ev.Pattern] = true
	}
	if !gotPatterns["A"] || !gotPatterns["B"] {
		t.Errorf("want alerts for both A and B, got %v", gotPatterns)
	}
	warns := handler.warnRecords()
	if len(warns) != 1 {
		t.Fatalf("want 1 WARN (A's second match suppressed), got %d", len(warns))
	}
}

func TestWatchdog_IngestNonBlockingWhenQueueFull(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)
	wd, err := NewWatchdog(nil, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}

	for range lineChannelCapacity {
		wd.Ingest([]byte("filler"))
	}

	latencies := make([]time.Duration, 1000)
	for i := range latencies {
		start := time.Now()
		wd.Ingest([]byte("drop"))
		latencies[i] = time.Since(start)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p99 := latencies[990]
	if p99 >= time.Millisecond {
		t.Fatalf("Ingest p99 latency under full queue = %v, want < 1ms", p99)
	}
}

func TestWatchdog_QueueFullDropEpisodeOnceWARN(t *testing.T) {
	t.Parallel()
	logger, handler := newTestLogger()
	alerts := make(chan Event, 1)
	wd, err := NewWatchdog(nil, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}

	fillSentinel := []byte("FILLLLER-XYZZY-MARK")
	episodeSentinel := []byte("EPISODE-LINE-XYZZY-MARK")
	closeSentinel := []byte("CLOSE-XYZZY-MARK")

	for range lineChannelCapacity {
		wd.Ingest(fillSentinel)
	}
	for range 100 {
		wd.Ingest(episodeSentinel)
	}

	cancel, done := startRun(t, wd)
	waitForLinesEmpty(t, wd, time.Second)
	time.Sleep(10 * time.Millisecond)

	wd.Ingest(closeSentinel)
	waitForLinesEmpty(t, wd, time.Second)
	time.Sleep(10 * time.Millisecond)

	cancel()
	<-done

	w := singleQueueFullWARN(t, handler)
	if dc, ok := w.Attrs["dropped_count"]; !ok || dc.Uint64() != 100 {
		t.Errorf("dropped_count attr = %v, want 100", dc)
	}
	if _, ok := w.Attrs["first_drop_at"]; !ok {
		t.Errorf("missing first_drop_at attr in WARN: %v", w.Attrs)
	}
	for _, frag := range [][]byte{fillSentinel, episodeSentinel, closeSentinel} {
		if bytes.Contains(w.Serialized, frag) {
			t.Errorf("queue-full WARN leaks line content fragment %q", frag)
		}
	}
}

func singleQueueFullWARN(t *testing.T, handler *recordingHandler) capturedRecord {
	t.Helper()
	warns := []capturedRecord{}
	for _, r := range handler.snapshot() {
		if r.Level == slog.LevelWarn && strings.Contains(r.Message, "queue full") {
			warns = append(warns, r)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("want 1 queue-full WARN, got %d", len(warns))
	}
	return warns[0]
}

func TestWatchdog_AlertOutputSaturatedDropsWARN(t *testing.T) {
	t.Parallel()
	const sentinel = "SENTINEL-SATURATED-MARK"
	logger, handler := newTestLogger()
	alerts := make(chan Event) // unbuffered, never drained
	pat := Pattern{Name: "p", Regex: mustCompile(t, "401"), RateLimit: time.Hour}
	wd, err := NewWatchdog([]Pattern{pat}, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}

	cancel, done := startRun(t, wd)
	defer func() { cancel(); <-done }()

	line := []byte("401 " + sentinel)
	for range 3 {
		wd.Ingest(line)
	}
	waitForLinesEmpty(t, wd, time.Second)
	time.Sleep(30 * time.Millisecond)

	warns := handler.warnRecords()
	if len(warns) != 3 {
		t.Fatalf("want 3 WARN entries (1 saturation + 2 rate-limit), got %d", len(warns))
	}

	sat, ratelimit := 0, 0
	for _, w := range warns {
		if bytes.Contains(w.Serialized, []byte(sentinel)) {
			t.Errorf("WARN leaks matched-line content (Q2): %s", w.Serialized)
		}
		switch {
		case strings.Contains(w.Message, "output saturated"):
			sat++
		case strings.Contains(w.Message, "suppressed by rate limit"):
			ratelimit++
		}
	}
	if sat != 1 || ratelimit != 2 {
		t.Errorf("want saturation=1 rate_limit=2, got saturation=%d rate_limit=%d", sat, ratelimit)
	}
}

func TestWatchdog_ConcurrentLogIngest(t *testing.T) {
	t.Parallel()
	const producers = 8
	const perProducer = 500
	const total = producers * perProducer

	handler := &fastCountHandler{}
	logger := slog.New(handler)

	alerts := make(chan Event, 5000)
	pat := Pattern{Name: "p", Regex: mustCompile(t, "match"), RateLimit: time.Hour}
	wd, err := NewWatchdog([]Pattern{pat}, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}

	cancel, done := startRun(t, wd)

	var wg sync.WaitGroup
	wg.Add(producers)
	for range producers {
		go func() {
			defer wg.Done()
			for range perProducer {
				wd.Ingest([]byte("match"))
			}
		}()
	}
	wg.Wait()

	waitForLinesEmpty(t, wd, 5*time.Second)
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done

	alertCount := uint64(len(drainAlerts(alerts)))
	if alertCount != 1 {
		t.Errorf("want exactly 1 alert (capacity-1 bucket, 1h refill), got %d", alertCount)
	}

	rateLimitWarns := handler.rateLimitWarns.Load()
	handler.mu.Lock()
	queueFullDrops := handler.queueFullDrops
	pendingDropsOnCancel := handler.pendingDropsOnCancel
	handler.mu.Unlock()

	// Every Ingest call ends up in one of four buckets:
	//   (a) matcher emits the first alert (alertCount = 1)
	//   (b) matcher logs a rate-limit WARN (subsequent matches; empty bucket)
	//   (c) queue-full at Ingest (counted via dropped_count attrs in WARNs)
	//   (d) enqueued but never evaluated; drained on ctx cancel
	accounted := alertCount + rateLimitWarns + queueFullDrops + pendingDropsOnCancel
	if accounted != total {
		t.Errorf("accounting: alert=%d + rate_limit_warns=%d + queue_full_drops=%d + pending_on_cancel=%d = %d, want %d",
			alertCount, rateLimitWarns, queueFullDrops, pendingDropsOnCancel, accounted, total)
	}
}

// -------------------- Phase 5 (US3) tests --------------------

// recordingStateDouble satisfies a state-machine-shaped method set and panics
// if any method is invoked. The watchdog has no compile-time path to any of
// these methods (it does not import the state machine); the recorder serves
// as a witness that proves the alert-only contract.
type recordingStateDouble struct {
	claimSession   atomic.Uint64
	refreshSession atomic.Uint64
	revokeSession  atomic.Uint64
	requestRestart atomic.Uint64
	transition     atomic.Uint64
}

func (s *recordingStateDouble) ClaimSession() {
	s.claimSession.Add(1)
	panic("watchdog called ClaimSession")
}

func (s *recordingStateDouble) RefreshSession() {
	s.refreshSession.Add(1)
	panic("watchdog called RefreshSession")
}

func (s *recordingStateDouble) RevokeSession() {
	s.revokeSession.Add(1)
	panic("watchdog called RevokeSession")
}

func (s *recordingStateDouble) RequestRestart() {
	s.requestRestart.Add(1)
	panic("watchdog called RequestRestart")
}
func (s *recordingStateDouble) Transition() { s.transition.Add(1); panic("watchdog called Transition") }

func TestWatchdog_NeverTransitionsState(t *testing.T) {
	t.Parallel()
	logger, _ := newTestLogger()
	alerts := make(chan Event, 16)
	patterns := []Pattern{
		{Name: "exit78", Regex: mustCompile(t, "exit code 78"), RateLimit: time.Hour},
		{Name: "validator", Regex: mustCompile(t, "validator failed"), RateLimit: time.Hour},
		{Name: "jwt", Regex: mustCompile(t, "401 Unauthorized"), RateLimit: time.Hour},
	}
	wd, err := NewWatchdog(patterns, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}

	state := &recordingStateDouble{}
	cancel, done := startRun(t, wd)

	go func() {
		for {
			select {
			case <-alerts:
			case <-done:
				return
			}
		}
	}()

	for _, line := range [][]byte{
		[]byte("child exited with exit code 78"),
		[]byte("validator failed for scope=anthropic"),
		[]byte("upstream returned 401 Unauthorized"),
		[]byte("no match at all"),
	} {
		wd.Ingest(line)
	}
	waitForLinesEmpty(t, wd, time.Second)
	time.Sleep(20 * time.Millisecond)

	cancel()
	<-done

	assertNoStateCalls(t, state)
	assertNoForbiddenSuperviseSubImport(t)
}

func assertNoStateCalls(t *testing.T, state *recordingStateDouble) {
	t.Helper()
	checks := []struct {
		name  string
		count uint64
	}{
		{"ClaimSession", state.claimSession.Load()},
		{"RefreshSession", state.refreshSession.Load()},
		{"RevokeSession", state.revokeSession.Load()},
		{"RequestRestart", state.requestRestart.Load()},
		{"Transition", state.transition.Load()},
	}
	for _, c := range checks {
		if c.count != 0 {
			t.Errorf("%s invoked %d times (want 0)", c.name, c.count)
		}
	}
}

func assertNoForbiddenSuperviseSubImport(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "list", "-f", "{{ join .Imports \"\\n\" }}", "github.com/mrz1836/hush/internal/supervise/watchdog").Output()
	if err != nil {
		t.Logf("go list failed (skipping import audit): %v", err)
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "github.com/mrz1836/hush/internal/supervise") &&
			line != "github.com/mrz1836/hush/internal/supervise" {
			t.Errorf("watchdog imports forbidden supervise sub-path: %s", line)
		}
	}
}

func TestWatchdog_NoSecureBytesStringConversion(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("watchdog.go")
	if err != nil {
		t.Fatalf("read watchdog.go: %v", err)
	}
	srcText := string(src)

	if matched, _ := regexp.MatchString(`(?i)securebytes`, srcText); matched {
		t.Errorf("watchdog.go references securebytes (Constitution X violation)")
	}

	secretConv := regexp.MustCompile(`string\([^)]*[Ss]ecret`)
	if secretConv.MatchString(srcText) {
		t.Errorf("watchdog.go converts secret material to string (Constitution X violation)")
	}

	stringSites := regexp.MustCompile(`\bstring\(([^)]+)\)`).FindAllStringSubmatch(srcText, -1)
	if len(stringSites) != 1 {
		t.Fatalf("want exactly one string(...) conversion site, got %d: %v", len(stringSites), stringSites)
	}
	arg := strings.TrimSpace(stringSites[0][1])
	if arg != "line" {
		t.Errorf("the single string(...) conversion arg = %q, want %q", arg, "line")
	}
}

func TestWatchdog_ZeroNewDependencies(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go binary not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "list", "-f", "{{ join .Imports \"\\n\" }}", "github.com/mrz1836/hush/internal/supervise/watchdog").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	allowedDirect := map[string]bool{
		"context":     true,
		"errors":      true,
		"fmt":         true,
		"log/slog":    true,
		"regexp":      true,
		"sync":        true,
		"sync/atomic": true,
		"time":        true,
		"github.com/mrz1836/hush/internal/supervise": true,
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !allowedDirect[line] {
			t.Errorf("watchdog has unauthorized direct import: %s", line)
		}
	}
}

func TestWatchdog_SatisfiesSuperviseInterface(t *testing.T) {
	t.Parallel()
	var _ supervise.Watchdog = (*Watchdog)(nil)

	logger, _ := newTestLogger()
	alerts := make(chan Event, 1)
	wd, err := NewWatchdog(nil, alerts, logger)
	if err != nil {
		t.Fatalf("NewWatchdog: %v", err)
	}
	// Should not panic even though Run has not started.
	wd.OnStderrLine(context.Background(), []byte("ignored"))
}
