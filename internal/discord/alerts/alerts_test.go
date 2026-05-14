package alerts

import (
	"context"
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- test helpers -----------------------------------------------------

// recordedCall captures one observation of a Sender method invocation
// for assertions in tests that need to inspect call ordering and
// arguments.
type recordedCall struct {
	method    string
	channelID string
	message   string
}

// recordingSender records every Sender call and returns nil. Safe for
// concurrent use.
type recordingSender struct {
	mu    sync.Mutex
	calls []recordedCall
}

func (s *recordingSender) SendOwnerDM(_ context.Context, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedCall{method: "SendOwnerDM", message: message})
	return nil
}

func (s *recordingSender) PostChannel(_ context.Context, channelID, message string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedCall{method: "PostChannel", channelID: channelID, message: message})
	return nil
}

func (s *recordingSender) snapshot() []recordedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedCall, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *recordingSender) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

// failingSender returns a fixed injected error from every method.
type failingSender struct {
	err error
}

func (f *failingSender) SendOwnerDM(_ context.Context, _ string) error { return f.err }
func (f *failingSender) PostChannel(_ context.Context, _, _ string) error {
	return f.err
}

// failOnInvokeSender calls t.Fatal from any method, used to prove
// Info-tier dispatch makes zero Sender calls.
type failOnInvokeSender struct {
	t *testing.T
}

func (f *failOnInvokeSender) SendOwnerDM(_ context.Context, _ string) error {
	f.t.Helper()
	f.t.Fatal("Sender.SendOwnerDM invoked for Info-tier alert")
	return nil
}

func (f *failOnInvokeSender) PostChannel(_ context.Context, _, _ string) error {
	f.t.Helper()
	f.t.Fatal("Sender.PostChannel invoked for Info-tier alert")
	return nil
}

// recordingHandler is a slog.Handler that records every emitted
// record's level, message, and attribute map. Safe for concurrent use.
type recordingHandler struct {
	mu      sync.Mutex
	records []capturedRecord
}

type capturedRecord struct {
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	rec := capturedRecord{Level: r.Level, Message: r.Message, Attrs: make(map[string]any)}
	r.Attrs(func(a slog.Attr) bool {
		rec.Attrs[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
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

// fakeClock is an atomically-advanceable monotonic clock.
type fakeClock struct {
	v atomic.Int64
}

func newFakeClock(start time.Time) *fakeClock {
	c := &fakeClock{}
	c.v.Store(start.UnixNano())
	return c
}

func (c *fakeClock) now() time.Time {
	return time.Unix(0, c.v.Load())
}

func (c *fakeClock) advance(d time.Duration) {
	c.v.Add(int64(d))
}

func newTestRouter(t *testing.T, sender Sender) *Router {
	t.Helper()
	r := NewRouter(sender, "audit-ch-id", 1*time.Second, 1*time.Second, slog.New(&recordingHandler{}))
	c := newFakeClock(time.Unix(1_700_000_000, 0))
	r.setClock(c.now)
	return r
}

// --- B-A-1 ------------------------------------------------------------

func TestAlertClass_ExportedSet(t *testing.T) {
	t.Parallel()
	want := map[AlertClass]string{
		AlertClassApprovalRequest:               "approval-request",
		AlertClassDaemonRefreshRequest:          "daemon-refresh-request",
		AlertClassValidatorStaleFailure:         "validator-stale-failure",
		AlertClassChildExit78StaleFailure:       "child-exit-78-stale-failure",
		AlertClassLogPatternStaleWarning:        "log-pattern-stale-warning",
		AlertClassDiscordDisconnected:           "discord-disconnected",
		AlertClassDiscordReconnected:            "discord-reconnected",
		AlertClassVaultUnreachableAtBootTimeout: "vault-unreachable-at-boot-timeout",
	}
	if len(want) != 8 {
		t.Fatalf("expected 8 alert classes, got %d", len(want))
	}
	for got, str := range want {
		if string(got) != str {
			t.Errorf("AlertClass value mismatch: got %q want %q", got, str)
		}
	}
}

// --- B-A-2 ------------------------------------------------------------

func TestTier_ExportedSet(t *testing.T) {
	t.Parallel()
	if int(TierCritical) != 0 {
		t.Errorf("TierCritical: want 0, got %d", TierCritical)
	}
	if int(TierWarning) != 1 {
		t.Errorf("TierWarning: want 1, got %d", TierWarning)
	}
	if int(TierInfo) != 2 {
		t.Errorf("TierInfo: want 2, got %d", TierInfo)
	}
}

// --- B-A-25 -----------------------------------------------------------

func TestNewRouter_NilGuards(t *testing.T) {
	t.Parallel()

	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("%s: expected panic, got none", name)
			}
		}()
		fn()
	}

	mustPanic("nil sender", func() {
		_ = NewRouter(nil, "ch", time.Second, time.Second, slog.Default())
	})
	mustPanic("nil logger", func() {
		_ = NewRouter(&recordingSender{}, "ch", time.Second, time.Second, nil)
	})

	// Zero/negative duration fallback to DefaultBucketWindow.
	rZero := NewRouter(&recordingSender{}, "ch", 0, -1*time.Second, slog.Default())
	if rZero.supBucketWindow() != DefaultBucketWindow {
		t.Errorf("zero perSupervisorBucket: want %v, got %v", DefaultBucketWindow, rZero.supBucketWindow())
	}
	if rZero.patBucketWindow() != DefaultBucketWindow {
		t.Errorf("negative perPatternBucket: want %v, got %v", DefaultBucketWindow, rZero.patBucketWindow())
	}
}

// --- B-A-3: 8 tier-binding tests --------------------------------------

func TestAlert_ApprovalRequest_TierBinding(t *testing.T) {
	t.Parallel()
	if got, want := classToTier[AlertClassApprovalRequest], TierCritical; got != want {
		t.Errorf("ApprovalRequest: got %v, want %v", got, want)
	}
}

func TestAlert_DaemonRefreshRequest_TierBinding(t *testing.T) {
	t.Parallel()
	if got, want := classToTier[AlertClassDaemonRefreshRequest], TierCritical; got != want {
		t.Errorf("DaemonRefreshRequest: got %v, want %v", got, want)
	}
}

func TestAlert_ValidatorStaleFailure_TierBinding(t *testing.T) {
	t.Parallel()
	if got, want := classToTier[AlertClassValidatorStaleFailure], TierWarning; got != want {
		t.Errorf("ValidatorStaleFailure: got %v, want %v", got, want)
	}
}

func TestAlert_ChildExit78StaleFailure_TierBinding(t *testing.T) {
	t.Parallel()
	if got, want := classToTier[AlertClassChildExit78StaleFailure], TierCritical; got != want {
		t.Errorf("ChildExit78StaleFailure: got %v, want %v", got, want)
	}
}

func TestAlert_LogPatternStaleWarning_TierBinding(t *testing.T) {
	t.Parallel()
	if got, want := classToTier[AlertClassLogPatternStaleWarning], TierWarning; got != want {
		t.Errorf("LogPatternStaleWarning: got %v, want %v", got, want)
	}
}

func TestAlert_DiscordDisconnected_TierBinding(t *testing.T) {
	t.Parallel()
	if got, want := classToTier[AlertClassDiscordDisconnected], TierWarning; got != want {
		t.Errorf("DiscordDisconnected: got %v, want %v", got, want)
	}
}

func TestAlert_DiscordReconnected_TierBinding(t *testing.T) {
	t.Parallel()
	if got, want := classToTier[AlertClassDiscordReconnected], TierInfo; got != want {
		t.Errorf("DiscordReconnected: got %v, want %v", got, want)
	}
}

func TestAlert_VaultUnreachableAtBootTimeout_TierBinding(t *testing.T) {
	t.Parallel()
	if got, want := classToTier[AlertClassVaultUnreachableAtBootTimeout], TierCritical; got != want {
		t.Errorf("VaultUnreachableAtBootTimeout: got %v, want %v", got, want)
	}
}

// --- B-A-4 ------------------------------------------------------------

func TestRoute_CriticalSendsDM(t *testing.T) {
	t.Parallel()
	sender := &recordingSender{}
	r := newTestRouter(t, sender)

	alert := Alert{
		Class:          AlertClassApprovalRequest,
		SupervisorName: "sup-a",
		MachineName:    "host-1",
		Detail:         "scope=ANTHROPIC_API_KEY",
	}
	if err := r.Route(context.Background(), alert); err != nil {
		t.Fatalf("Route: unexpected err %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d (%+v)", len(calls), calls)
	}
	if calls[0].method != "SendOwnerDM" {
		t.Errorf("expected SendOwnerDM, got %s", calls[0].method)
	}
	if !strings.HasPrefix(calls[0].message, "[CRITICAL][approval-request]") {
		t.Errorf("rendered missing prefix: %q", calls[0].message)
	}

	// Second Critical class (ChildExit78StaleFailure) — same routing branch.
	sender.reset()
	r2 := newTestRouter(t, sender)
	alert2 := Alert{
		Class:          AlertClassChildExit78StaleFailure,
		SupervisorName: "sup-b",
		Detail:         "stale-credential",
	}
	if err := r2.Route(context.Background(), alert2); err != nil {
		t.Fatalf("Route ChildExit78: %v", err)
	}
	calls = sender.snapshot()
	if len(calls) != 1 || calls[0].method != "SendOwnerDM" {
		t.Errorf("ChildExit78: expected single SendOwnerDM, got %+v", calls)
	}
}

// --- B-A-13 -----------------------------------------------------------

func TestRoute_CriticalTransportFailureRefundsBuckets(t *testing.T) {
	t.Parallel()

	injected := errors.New("discord 503")
	fail := &failingSender{err: injected}
	rh := &recordingHandler{}
	r := NewRouter(fail, "audit-ch-id", 1*time.Second, 1*time.Second, slog.New(rh))
	c := newFakeClock(time.Unix(1_700_000_000, 0))
	r.setClock(c.now)

	alert := Alert{Class: AlertClassApprovalRequest, SupervisorName: "sup-a", Pattern: "p1"}
	err := r.Route(context.Background(), alert)
	if !errors.Is(err, ErrAlertTransport) {
		t.Fatalf("want ErrAlertTransport, got %v", err)
	}
	if !errors.Is(err, injected) {
		t.Errorf("want underlying err in chain, got %v", err)
	}

	// Swap to a working sender; same supervisor/pattern, no clock advance.
	// Success proves both buckets were refunded.
	r.sender = &recordingSender{}
	if err := r.Route(context.Background(), alert); err != nil {
		t.Fatalf("post-refund Route: %v", err)
	}
}

// --- B-A-5 ------------------------------------------------------------

func TestRoute_WarningPostsToAuditChannel(t *testing.T) {
	t.Parallel()
	sender := &recordingSender{}
	r := newTestRouter(t, sender)

	alert := Alert{
		Class:          AlertClassValidatorStaleFailure,
		SupervisorName: "sup-b",
		Detail:         "scope=OPENAI_API_KEY",
	}
	if err := r.Route(context.Background(), alert); err != nil {
		t.Fatalf("Route: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 || calls[0].method != "PostChannel" {
		t.Fatalf("expected single PostChannel, got %+v", calls)
	}
	if calls[0].channelID != "audit-ch-id" {
		t.Errorf("channelID: want audit-ch-id, got %q", calls[0].channelID)
	}
	if !strings.HasPrefix(calls[0].message, "[WARNING][validator-stale]") {
		t.Errorf("rendered missing prefix: %q", calls[0].message)
	}

	for _, cls := range []AlertClass{AlertClassLogPatternStaleWarning, AlertClassDiscordDisconnected} {
		sender.reset()
		r2 := newTestRouter(t, sender)
		if err := r2.Route(context.Background(), Alert{Class: cls, SupervisorName: "sup-x", Pattern: "p"}); err != nil {
			t.Fatalf("Route %s: %v", cls, err)
		}
		calls = sender.snapshot()
		if len(calls) != 1 || calls[0].method != "PostChannel" {
			t.Errorf("class %s: want PostChannel, got %+v", cls, calls)
		}
	}
}

// --- B-A-14 -----------------------------------------------------------

func TestRoute_WarningTransportFailureRefundsBuckets(t *testing.T) {
	t.Parallel()

	injected := errors.New("discord 500")
	fail := &failingSender{err: injected}
	r := NewRouter(fail, "audit-ch-id", 1*time.Second, 1*time.Second, slog.New(&recordingHandler{}))
	c := newFakeClock(time.Unix(1_700_000_000, 0))
	r.setClock(c.now)

	alert := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "sup-w", Pattern: "p1"}
	err := r.Route(context.Background(), alert)
	if !errors.Is(err, ErrAlertTransport) {
		t.Fatalf("want ErrAlertTransport, got %v", err)
	}

	r.sender = &recordingSender{}
	if err := r.Route(context.Background(), alert); err != nil {
		t.Fatalf("post-refund Route: %v", err)
	}
}

// --- B-A-6 ------------------------------------------------------------

func TestRoute_InfoLogsOnly_NoDiscordCall(t *testing.T) {
	t.Parallel()
	fail := &failOnInvokeSender{t: t}
	rh := &recordingHandler{}
	r := NewRouter(fail, "audit-ch-id", 1*time.Second, 1*time.Second, slog.New(rh))
	c := newFakeClock(time.Unix(1_700_000_000, 0))
	r.setClock(c.now)

	alert := Alert{Class: AlertClassDiscordReconnected, SupervisorName: "sup-c"}
	if err := r.Route(context.Background(), alert); err != nil {
		t.Fatalf("Route: %v", err)
	}
	records := rh.snapshot()
	if len(records) != 1 {
		t.Fatalf("expected 1 slog record, got %d", len(records))
	}
	if records[0].Level != slog.LevelInfo {
		t.Errorf("expected INFO level, got %v", records[0].Level)
	}
	if records[0].Message != msgRouted {
		t.Errorf("expected msg %q, got %q", msgRouted, records[0].Message)
	}
}

// --- B-A-15 -----------------------------------------------------------

func TestRoute_UnknownClass_TypedError(t *testing.T) {
	t.Parallel()
	sender := &recordingSender{}
	rh := &recordingHandler{}
	r := NewRouter(sender, "audit-ch-id", 1*time.Second, 1*time.Second, slog.New(rh))

	bogus := Alert{Class: AlertClass("not-a-real-class"), SupervisorName: "sup-z"}
	err := r.Route(context.Background(), bogus)
	if !errors.Is(err, ErrUnknownAlertClass) {
		t.Fatalf("want ErrUnknownAlertClass, got %v", err)
	}
	if calls := sender.snapshot(); len(calls) != 0 {
		t.Errorf("expected 0 sender calls, got %+v", calls)
	}

	records := rh.snapshot()
	if len(records) != 1 {
		t.Fatalf("expected 1 WARN slog record, got %d", len(records))
	}
	if records[0].Level != slog.LevelWarn {
		t.Errorf("expected WARN, got %v", records[0].Level)
	}
	if records[0].Attrs[attrOutcome] != outcomeUnknownClass {
		t.Errorf("expected outcome=unknown_class, got %v", records[0].Attrs[attrOutcome])
	}

	// Bucket untouched — a subsequent valid route for the same supervisor succeeds.
	ok := Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "sup-z", Pattern: "p"}
	if err := r.Route(context.Background(), ok); err != nil {
		t.Errorf("buckets should be untouched; got err %v", err)
	}
}

// --- B-A-7 ------------------------------------------------------------

func TestRoute_CallerSuppliedTierIgnored(t *testing.T) {
	t.Parallel()
	sender := &recordingSender{}
	r := newTestRouter(t, sender)

	// Class is Warning-bound (ValidatorStaleFailure); caller bug
	// supplies TierCritical. Router must route by class → PostChannel,
	// NOT SendOwnerDM.
	alert := Alert{
		Class:          AlertClassValidatorStaleFailure,
		Tier:           TierCritical,
		SupervisorName: "sup-a",
		Pattern:        "p",
	}
	if err := r.Route(context.Background(), alert); err != nil {
		t.Fatalf("Route: %v", err)
	}
	calls := sender.snapshot()
	if len(calls) != 1 || calls[0].method != "PostChannel" {
		t.Errorf("expected PostChannel (class-derived Warning), got %+v", calls)
	}
}

// --- B-A-20 -----------------------------------------------------------

func TestRoute_SlogLevelMatrix(t *testing.T) {
	t.Parallel()

	type row struct {
		name     string
		alert    Alert
		setup    func(r *Router)
		wantLvl  slog.Level
		wantRecs int
	}

	rows := []row{
		{
			name:     "critical-success-debug",
			alert:    Alert{Class: AlertClassApprovalRequest, SupervisorName: "s1"},
			wantLvl:  slog.LevelDebug,
			wantRecs: 1,
		},
		{
			name:     "warning-success-debug",
			alert:    Alert{Class: AlertClassValidatorStaleFailure, SupervisorName: "s2"},
			wantLvl:  slog.LevelDebug,
			wantRecs: 1,
		},
		{
			name:     "info-success-info",
			alert:    Alert{Class: AlertClassDiscordReconnected, SupervisorName: "s3"},
			wantLvl:  slog.LevelInfo,
			wantRecs: 1,
		},
		{
			name:  "transport-failure-warn",
			alert: Alert{Class: AlertClassApprovalRequest, SupervisorName: "s4"},
			setup: func(r *Router) {
				r.sender = &failingSender{err: errors.New("boom")}
			},
			wantLvl:  slog.LevelWarn,
			wantRecs: 1,
		},
		{
			name:     "unknown-class-warn",
			alert:    Alert{Class: AlertClass("???"), SupervisorName: "s5"},
			wantLvl:  slog.LevelWarn,
			wantRecs: 1,
		},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			sender := &recordingSender{}
			rh := &recordingHandler{}
			rt := NewRouter(sender, "audit-ch-id", 1*time.Second, 1*time.Second, slog.New(rh))
			if r.setup != nil {
				r.setup(rt)
			}
			_ = rt.Route(context.Background(), r.alert)
			recs := rh.snapshot()
			if len(recs) != r.wantRecs {
				t.Fatalf("%s: want %d record(s), got %d", r.name, r.wantRecs, len(recs))
			}
			if recs[0].Level != r.wantLvl {
				t.Errorf("%s: want level %v, got %v", r.name, r.wantLvl, recs[0].Level)
			}
		})
	}

	// Rate-limited path emits NO slog record.
	t.Run("rate-limited-no-record", func(t *testing.T) {
		t.Parallel()
		sender := &recordingSender{}
		rh := &recordingHandler{}
		rt := NewRouter(sender, "audit-ch-id", 1*time.Hour, 1*time.Hour, slog.New(rh))
		alert := Alert{Class: AlertClassApprovalRequest, SupervisorName: "rls"}
		if err := rt.Route(context.Background(), alert); err != nil {
			t.Fatalf("first Route: %v", err)
		}
		before := len(rh.snapshot())
		err := rt.Route(context.Background(), alert)
		if !errors.Is(err, ErrAlertRateLimited) {
			t.Fatalf("want ErrAlertRateLimited, got %v", err)
		}
		after := len(rh.snapshot())
		if after != before {
			t.Errorf("rate-limited path emitted %d new record(s); want 0", after-before)
		}
	})
}

// --- B-A-21 -----------------------------------------------------------

func TestRoute_SlogAttributeAllowList(t *testing.T) {
	t.Parallel()
	allow := map[string]struct{}{
		attrClass:      {},
		attrTier:       {},
		attrSupervisor: {},
		attrMachine:    {},
		attrPattern:    {},
		attrOutcome:    {},
	}

	sender := &recordingSender{}
	rh := &recordingHandler{}
	r := NewRouter(sender, "audit-ch-id", 1*time.Second, 1*time.Second, slog.New(rh))

	// Drive every code path.
	_ = r.Route(context.Background(), Alert{Class: AlertClassApprovalRequest, SupervisorName: "sa", MachineName: "m", Pattern: "p", Detail: "d"})
	r.sender = &failingSender{err: errors.New("x")}
	_ = r.Route(context.Background(), Alert{Class: AlertClassDaemonRefreshRequest, SupervisorName: "sb"})
	_ = r.Route(context.Background(), Alert{Class: AlertClass("nope"), SupervisorName: "sc"})

	for _, rec := range rh.snapshot() {
		for k := range rec.Attrs {
			if _, ok := allow[k]; !ok {
				t.Errorf("disallowed slog attr key: %q (record: %+v)", k, rec)
			}
		}
	}
}

// --- B-A-24 -----------------------------------------------------------

func TestRoute_SentinelDisjointness(t *testing.T) {
	t.Parallel()
	pairs := []struct {
		a, b error
	}{
		{ErrAlertRateLimited, ErrAlertTransport},
		{ErrAlertRateLimited, ErrUnknownAlertClass},
		{ErrAlertTransport, ErrUnknownAlertClass},
	}
	for _, p := range pairs {
		if errors.Is(p.a, p.b) {
			t.Errorf("sentinel collision: errors.Is(%v, %v) is true", p.a, p.b)
		}
		if errors.Is(p.b, p.a) {
			t.Errorf("sentinel collision: errors.Is(%v, %v) is true", p.b, p.a)
		}
	}

	// Drive each error path and assert disjoint matching.
	sender := &recordingSender{}
	r := NewRouter(sender, "ch", 1*time.Hour, 1*time.Hour, slog.New(&recordingHandler{}))
	a := Alert{Class: AlertClassApprovalRequest, SupervisorName: "x"}
	_ = r.Route(context.Background(), a)
	errRL := r.Route(context.Background(), a)
	if !errors.Is(errRL, ErrAlertRateLimited) || errors.Is(errRL, ErrAlertTransport) || errors.Is(errRL, ErrUnknownAlertClass) {
		t.Errorf("rate-limited err matches wrong sentinel: %v", errRL)
	}

	r2 := NewRouter(&failingSender{err: errors.New("boom")}, "ch", 1*time.Second, 1*time.Second, slog.New(&recordingHandler{}))
	errT := r2.Route(context.Background(), Alert{Class: AlertClassApprovalRequest, SupervisorName: "y"})
	if !errors.Is(errT, ErrAlertTransport) || errors.Is(errT, ErrAlertRateLimited) || errors.Is(errT, ErrUnknownAlertClass) {
		t.Errorf("transport err matches wrong sentinel: %v", errT)
	}

	r3 := NewRouter(&recordingSender{}, "ch", 1*time.Second, 1*time.Second, slog.New(&recordingHandler{}))
	errU := r3.Route(context.Background(), Alert{Class: AlertClass("???"), SupervisorName: "z"})
	if !errors.Is(errU, ErrUnknownAlertClass) || errors.Is(errU, ErrAlertRateLimited) || errors.Is(errU, ErrAlertTransport) {
		t.Errorf("unknown-class err matches wrong sentinel: %v", errU)
	}
}

// --- B-A-22 -----------------------------------------------------------

func TestRoute_ConcurrentSafety(t *testing.T) {
	t.Parallel()
	sender := &recordingSender{}
	r := NewRouter(sender, "audit-ch-id", 1*time.Microsecond, 1*time.Microsecond, slog.New(&recordingHandler{}))

	classes := []AlertClass{
		AlertClassApprovalRequest,
		AlertClassDaemonRefreshRequest,
		AlertClassValidatorStaleFailure,
		AlertClassChildExit78StaleFailure,
		AlertClassLogPatternStaleWarning,
		AlertClassDiscordDisconnected,
		AlertClassDiscordReconnected,
		AlertClassVaultUnreachableAtBootTimeout,
	}

	const G, N = 8, 100
	var wg sync.WaitGroup
	wg.Add(G)
	for g := 0; g < G; g++ {
		go func() {
			defer wg.Done()
			for i := 0; i < N; i++ {
				alert := Alert{
					Class:          classes[(g+i)%len(classes)],
					SupervisorName: "sup-" + string(rune('0'+(g%4))),
					Pattern:        "pat-" + string(rune('a'+(i%5))),
				}
				_ = r.Route(context.Background(), alert)
			}
		}()
	}
	wg.Wait()
}

// --- B-A-19 -----------------------------------------------------------

func TestRouter_ZeroNewDependencies(t *testing.T) {
	t.Parallel()
	allowed := map[string]struct{}{
		"context":  {},
		"errors":   {},
		"fmt":      {},
		"log/slog": {},
		"strings":  {},
		"sync":     {},
		"time":     {},
	}
	forbidden := []string{
		"github.com/bwmarrin/discordgo",
		"github.com/mrz1836/hush/internal/discord",
		"github.com/mrz1836/hush/internal/vault/securebytes",
	}

	fset := token.NewFileSet()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	saw := map[string]struct{}{}
	err = filepath.WalkDir(cwd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Skip test files for the dependency audit; tests bring in
		// extra stdlib packages (testing, sync/atomic, go/parser, etc.).
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			val := strings.Trim(imp.Path.Value, `"`)
			saw[val] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for imp := range saw {
		if _, ok := allowed[imp]; !ok {
			t.Errorf("unexpected import in production files: %q", imp)
		}
	}
	for _, bad := range forbidden {
		if _, ok := saw[bad]; ok {
			t.Errorf("forbidden import present: %q", bad)
		}
	}
}

// --- TestAlert_ZeroAlertTime_NotInRecord ------------------------------
// (B-A-18 secret-byte test lives in templates_test.go; this assertion
// ensures Alert.Time is not rendered into the slog record either.)

func TestRoute_AlertTime_NotInSlogRecord(t *testing.T) {
	t.Parallel()
	sender := &recordingSender{}
	rh := &recordingHandler{}
	r := NewRouter(sender, "ch", 1*time.Second, 1*time.Second, slog.New(rh))

	alert := Alert{
		Class:          AlertClassDiscordReconnected,
		SupervisorName: "sup",
		Time:           time.Unix(1234567890, 0),
	}
	_ = r.Route(context.Background(), alert)
	for _, rec := range rh.snapshot() {
		for k, v := range rec.Attrs {
			if k == "time" {
				t.Errorf("slog record has 'time' attr: %v", v)
			}
		}
	}
}
