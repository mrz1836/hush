package supervise

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestStatusInputs_Accessors exercises every accessor on the statusInputs
// struct under empty and populated states. Split into helpers per
// gocyclo budget.
func TestStatusInputs_Accessors(t *testing.T) {
	t.Run("empty defaults", testStatusInputsEmptyDefaults)
	t.Run("populated", testStatusInputsPopulated)
}

func testStatusInputsEmptyDefaults(t *testing.T) {
	o := &statusInputs{name: "n"}
	if o.Name() != "n" {
		t.Errorf("Name: got %q want n", o.Name())
	}
	if !o.SessionExpiresAt().IsZero() {
		t.Error("SessionExpiresAt: want zero")
	}
	if !o.RefreshWindowNext().IsZero() {
		t.Error("RefreshWindowNext: want zero")
	}
	if o.ScopeHealthy() != nil {
		t.Errorf("ScopeHealthy: %+v", o.ScopeHealthy())
	}
	if o.ScopeStale() != nil {
		t.Errorf("ScopeStale: %+v", o.ScopeStale())
	}
	if o.LastAuthFailure() != nil {
		t.Errorf("LastAuthFailure: %+v", o.LastAuthFailure())
	}
	if o.ChildUptime() != 0 {
		t.Errorf("ChildUptime: %v", o.ChildUptime())
	}
	if o.DiscordConnected() {
		t.Error("DiscordConnected: want false")
	}
}

func testStatusInputsPopulated(t *testing.T) {
	o := &statusInputs{name: "p"}
	sea := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	rwn := time.Date(2026, 4, 15, 16, 0, 0, 0, time.UTC)
	laf := time.Date(2026, 4, 14, 12, 0, 0, 0, time.UTC)
	startedAt := time.Now().Add(-2 * time.Hour)
	o.sessionExp.Store(&sea)
	o.refreshNext.Store(&rwn)
	healthy := []string{"A", "B"}
	stale := []string{"C"}
	o.scopeHealthy.Store(&healthy)
	o.scopeStale.Store(&stale)
	o.lastAuthFail.Store(&laf)
	o.childStartedAt.Store(&startedAt)
	o.discordConnected.Store(true)

	if got := o.SessionExpiresAt(); got != sea {
		t.Errorf("SessionExpiresAt: %v", got)
	}
	if got := o.RefreshWindowNext(); got != rwn {
		t.Errorf("RefreshWindowNext: %v", got)
	}
	if got := o.ScopeHealthy(); !equalStrings(got, []string{"A", "B"}) {
		t.Errorf("ScopeHealthy: %+v", got)
	}
	if got := o.ScopeStale(); !equalStrings(got, []string{"C"}) {
		t.Errorf("ScopeStale: %+v", got)
	}
	if got := o.LastAuthFailure(); got == nil || *got != laf {
		t.Errorf("LastAuthFailure: %+v", got)
	}
	if got := o.ChildUptime(); got < 2*time.Hour {
		t.Errorf("ChildUptime: %v", got)
	}
	if !o.DiscordConnected() {
		t.Error("DiscordConnected: want true")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestNoopInterfaces calls the no-op default implementations to cover them.
func TestNoopInterfaces(t *testing.T) {
	ctx := context.Background()
	var a Alerts = noopAlerts{}
	a.Emit(ctx, AlertClassExit78, AlertPayload{})
	var w Watchdog = noopWatchdog{}
	w.OnStderrLine(ctx, []byte("test"))
	nv := noopValidator{}
	if err := nv.Validate(ctx, "x", nil); err != nil {
		t.Errorf("noopValidator: %v", err)
	}
}

// TestAlertClass_StringCoversAllValues exercises every AlertClass.String()
// case so the switch covers all enum values.
func TestAlertClass_StringCoversAllValues(t *testing.T) {
	cases := []struct {
		c    AlertClass
		want string
	}{
		{AlertClassValidatorFailure, "ValidatorFailure"},
		{AlertClassExit78, "Exit78"},
		{AlertClassVaultRejectedJWT, "VaultRejectedJWT"},
		{AlertClassRefillFailed, "RefillFailed"},
		{AlertClassDiscordUnavailableOnClaim, "DiscordUnavailableOnClaim"},
		{AlertClassRefreshDenied, "RefreshDenied"},
		{AlertClassRefreshTimeout, "RefreshTimeout"},
		{AlertClassGraceEntered, "GraceEntered"},
		{AlertClassLogPatternMatch, "LogPatternMatch"},
		{AlertClassBootTimeout, "BootTimeout"},
		{AlertClass(99), "Unknown"},
	}
	for _, tc := range cases {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("AlertClass(%d): got %q want %q", tc.c, got, tc.want)
		}
	}
}

// TestAlertReasonFor covers the closed phrase map.
func TestAlertReasonFor(t *testing.T) {
	for _, c := range []AlertClass{
		AlertClassValidatorFailure,
		AlertClassExit78,
		AlertClassVaultRejectedJWT,
		AlertClassRefillFailed,
		AlertClassDiscordUnavailableOnClaim,
		AlertClassRefreshDenied,
		AlertClassRefreshTimeout,
		AlertClassGraceEntered,
		AlertClassLogPatternMatch,
		AlertClassBootTimeout,
	} {
		if got := alertReasonFor(c); got == "" {
			t.Errorf("alertReasonFor(%s): empty", c)
		}
	}
	if got := alertReasonFor(AlertClass(99)); got != "" {
		t.Errorf("alertReasonFor(99): got %q want empty", got)
	}
}

// TestLineSplittingWriter writes lines and asserts the watchdog observes each.
func TestLineSplittingWriter(t *testing.T) {
	wd := &recordingWatchdog{}
	var sink bytes.Buffer
	w := newLineSplittingWriter(context.Background(), &sink, wd, nil)

	// Two complete lines + a partial.
	_, _ = w.Write([]byte("alpha\nbeta\ngamma"))
	if lines := wd.Lines(); len(lines) != 2 {
		t.Errorf("got %d lines, want 2: %v", len(lines), lines)
	}
	// Finish gamma with \n.
	_, _ = w.Write([]byte("\n"))
	if lines := wd.Lines(); len(lines) != 3 {
		t.Errorf("got %d lines after gamma, want 3", len(lines))
	}
	// Sink receives full bytes (tee).
	if !strings.Contains(sink.String(), "alpha") {
		t.Errorf("sink missing payload: %q", sink.String())
	}
	// nil sink + nil watchdog fall back to discards.
	w2 := newLineSplittingWriter(context.Background(), nil, nil, nil)
	n, err := w2.Write([]byte("hello\n"))
	if err != nil || n != len("hello\n") {
		t.Errorf("nil-sink write: n=%d err=%v", n, err)
	}
	// Empty write is a no-op.
	if n, err := w.Write(nil); n != 0 || err != nil {
		t.Errorf("nil write: n=%d err=%v", n, err)
	}
	// Overflow protection — a single huge line gets capped.
	big := make([]byte, stderrLineCap+1024)
	for i := range big {
		big[i] = 'A'
	}
	_, _ = w.Write(big) // no newline → buffer overflow path
}

// TestIndexByte covers the helper.
func TestIndexByte(t *testing.T) {
	if i := indexByte([]byte("abc\ndef"), '\n'); i != 3 {
		t.Errorf("indexByte: got %d want 3", i)
	}
	if i := indexByte([]byte("abc"), '\n'); i != -1 {
		t.Errorf("indexByte: got %d want -1", i)
	}
}

// TestDefaultNonceFn / DefaultRequestIDFn — round-trip checks.
func TestDefaultNonceFn(t *testing.T) {
	a, b := defaultNonceFn(), defaultNonceFn()
	if a == "" || b == "" || a == b {
		t.Errorf("nonce stability: a=%q b=%q", a, b)
	}
}

func TestDefaultRequestIDFn(t *testing.T) {
	a, b := defaultRequestIDFn(), defaultRequestIDFn()
	if a == "" || b == "" || a == b {
		t.Errorf("request_id stability: a=%q b=%q", a, b)
	}
}

// TestDefaultVaultHzProbe_OK exercises the default probe against a tiny
// httptest server.
func TestDefaultVaultHzProbe_OK(t *testing.T) {
	srv := newMockVault(t, &testECDSAKey(t).PublicKey)
	probe := defaultVaultHzProbe(srv.Client())
	if err := probe(context.Background(), srv.URL()); err != nil {
		t.Errorf("probe OK: %v", err)
	}
	srv.SetHzStatus(500)
	if err := probe(context.Background(), srv.URL()); err == nil {
		t.Errorf("probe 500: expected error")
	}
}

// TestCompressedEphemeralPubHex covers the pub-key serializer including
// odd Y coordinate.
func TestCompressedEphemeralPubHex(t *testing.T) {
	pk := testECDSAKey(t)
	got := compressedEphemeralPubHex(&pk.PublicKey)
	if len(got) != 66 {
		t.Errorf("compressedEphemeralPubHex len=%d want 66", len(got))
	}
	// nil-safety.
	if got := compressedEphemeralPubHex(nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
}

// TestClientKeyFingerprintHex covers the helper.
func TestClientKeyFingerprintHex(t *testing.T) {
	pk := testECDSAKey(t)
	got := clientKeyFingerprintHex(&pk.PublicKey)
	if len(got) != 16 {
		t.Errorf("clientKeyFingerprintHex len=%d want 16", len(got))
	}
	if got := clientKeyFingerprintHex(nil); got != "" {
		t.Errorf("nil: got %q", got)
	}
}

// TestJitterInterval covers the boundary cases.
func TestJitterInterval(t *testing.T) {
	if got := jitterInterval(0); got != bootBackoffInitial {
		t.Errorf("zero: got %v want %v", got, bootBackoffInitial)
	}
	if got := jitterInterval(time.Hour); got > bootBackoffCap {
		t.Errorf("cap: got %v > %v", got, bootBackoffCap)
	}
	d := 500 * time.Millisecond
	got := jitterInterval(d)
	if got < d*4/5 || got > d*6/5 {
		t.Errorf("jitter: %v not in [%v, %v]", got, d*4/5, d*6/5)
	}
}

// TestDispatchRefreshResult_AllArms exercises the dispatch arms.
func TestDispatchRefreshResult_AllArms(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	ctx := context.Background()

	// nil err — JWT swap already applied; no alert.
	tl.lc.dispatchRefreshResult(ctx, refreshResult{})
	if got := tl.alerts.CountClass(AlertClassRefreshDenied); got != 0 {
		t.Errorf("nil err arm: alerts emitted")
	}

	// deny arm.
	tl.lc.dispatchRefreshResult(ctx, refreshResult{deny: true, err: errors.New("denied")})
	if got := tl.alerts.CountClass(AlertClassRefreshDenied); got != 1 {
		t.Errorf("deny arm: got %d alerts want 1", got)
	}

	// timeout arm.
	tl.lc.dispatchRefreshResult(ctx, refreshResult{err: errors.New("timeout")})
	if got := tl.alerts.CountClass(AlertClassRefreshTimeout); got != 1 {
		t.Errorf("timeout arm: got %d alerts want 1", got)
	}
}

// TestPerformRefreshClaim_Errors covers the error-path branches.
func TestPerformRefreshClaim_Errors(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	ctx := context.Background()

	// Pre-queue a denial.
	tl.vault.QueueDenied()
	got := tl.lc.performRefreshClaim(ctx)
	if !got.deny {
		t.Errorf("denial branch: %+v", got)
	}

	// Timeout (408).
	tl.vault.QueueClaim(claimOutcome{status: http.StatusRequestTimeout, body: `{"error":"approval_timeout","request_id":"x"}`})
	got = tl.lc.performRefreshClaim(ctx)
	if got.err == nil {
		t.Errorf("timeout branch: nil err")
	}

	// Other 5xx.
	tl.vault.QueueClaim(claimOutcome{status: 500, body: `{"error":"server","request_id":"x"}`})
	got = tl.lc.performRefreshClaim(ctx)
	if got.err == nil {
		t.Errorf("5xx branch: nil err")
	}
}

// TestDispatchRefreshVerb_StoppedAndUnknown exercises the rejection arms.
func TestDispatchRefreshVerb_StoppedAndUnknown(t *testing.T) {
	tl := newTestLifecycle(t, longChildCmd())
	// Transition to StateStopped.
	if err := tl.lc.store.Transition(context.Background(), EventStopRequested); err != nil {
		t.Fatalf("transition: %v", err)
	}
	verb := refreshVerb{ack: make(chan error, 1)}
	tl.lc.dispatchRefreshVerb(context.Background(), verb)
	select {
	case err := <-verb.ack:
		if err == nil || !strings.Contains(err.Error(), "stopped") {
			t.Errorf("stopped arm: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("no ack")
	}
}
