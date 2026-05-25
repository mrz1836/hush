package supervise

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/testutil"
	"github.com/mrz1836/hush/internal/vault/securebytes"
)

// allEvents is the closed event vocabulary. Used by negative-cell
// generation and keyset assertions.
var allEvents = []Event{ //nolint:gochecknoglobals // test fixture: closed-vocabulary slice shared across table-driven tests
	EventFetchOK,
	EventFetchAuthRequired,
	EventClaimDenied,
	EventClaimUnavailable,
	EventValidatorFailed,
	EventBootRetryExhausted,
	EventChildExitClean,
	EventChildExitCrash,
	EventChildExit78Stale,
	EventRefreshRequested,
	EventGraceRestartTriggered,
	EventGraceRestartOK,
	EventGraceExpired,
	EventApprovalGranted,
	EventStopRequested,
}

// allStates is the closed state vocabulary.
var allStates = []State{ //nolint:gochecknoglobals // test fixture: closed-vocabulary slice shared across table-driven tests
	StateFetching,
	StateRunning,
	StateAwaitingApproval,
	StateGraceRestart,
	StateStopped,
}

// prefixFor returns the event sequence that drives a fresh
// StateFetching store into the given source state. Sequences
// terminate before any post-arrival event (the caller applies the
// event under test against the fresh source state).
func prefixFor(src State) []Event {
	switch src {
	case StateFetching:
		return nil
	case StateRunning:
		return []Event{EventFetchOK}
	case StateAwaitingApproval:
		return []Event{EventFetchAuthRequired}
	case StateGraceRestart:
		return []Event{EventFetchOK, EventGraceRestartTriggered}
	case StateStopped:
		return []Event{EventStopRequested}
	default:
		panic(fmt.Sprintf("prefixFor: unknown state %q", src))
	}
}

// ---------- T-02: TestNewStore_NilClockPanics ----------

func TestNewStore_NilClockPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected NewStore(_, nil) to panic; got no panic")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected panic value to be string; got %T (%v)", r, r)
		}
		if !strings.Contains(msg, "non-nil Clock") {
			t.Fatalf("panic message %q does not identify nil-clock programmer error", msg)
		}
	}()
	NewStore(context.Background(), nil)
}

// ---------- T-13: TestReasons_KeysetMatchesEventVocabulary ----------

func TestReasons_KeysetMatchesEventVocabulary(t *testing.T) {
	t.Parallel()
	if got, want := len(reasons), len(allEvents); got != want {
		t.Fatalf("reasons map size = %d, want %d", got, want)
	}
	for _, e := range allEvents {
		if _, ok := reasons[e]; !ok {
			t.Errorf("reasons map missing key %q", e)
		}
	}
	want := make(map[Event]struct{}, len(allEvents))
	for _, e := range allEvents {
		want[e] = struct{}{}
	}
	for k := range reasons {
		if _, ok := want[k]; !ok {
			t.Errorf("reasons map has extra key %q outside closed vocabulary", k)
		}
	}
}

// ---------- T-14: TestTransitions_KeysetMatchesStateVocabulary ----------

func TestTransitions_KeysetMatchesStateVocabulary(t *testing.T) { //nolint:gocognit // structural complexity: nested keyset checks across the 5×N transition table
	t.Parallel()
	if got, want := len(transitions), len(allStates); got != want {
		t.Fatalf("transitions outer keyset size = %d, want %d", got, want)
	}
	stateSet := make(map[State]struct{}, len(allStates))
	for _, s := range allStates {
		stateSet[s] = struct{}{}
	}
	for src, inner := range transitions {
		if _, ok := stateSet[src]; !ok {
			t.Errorf("transitions outer key %q outside closed State vocabulary", src)
		}
		for ev, dst := range inner {
			if _, ok := stateSet[dst]; !ok {
				t.Errorf("transitions[%q][%q] = %q outside closed State vocabulary", src, ev, dst)
			}
		}
	}
	for _, s := range allStates {
		if _, ok := transitions[s]; !ok {
			t.Errorf("transitions missing outer key %q", s)
		}
	}
}

// ---------- T-01: TestNewStore_InitialState ----------

func TestNewStore_InitialState(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	clk := testutil.NewFakeClock(t0)
	s := NewStore(context.Background(), clk)
	snap := s.Snapshot()
	if snap.State != StateFetching {
		t.Errorf("initial State = %q, want %q", snap.State, StateFetching)
	}
	if snap.ChildPID != 0 {
		t.Errorf("initial ChildPID = %d, want 0", snap.ChildPID)
	}
	if snap.Token != nil {
		t.Errorf("initial Token = %v, want nil", snap.Token)
	}
	if snap.Reason != "store constructed" {
		t.Errorf("initial Reason = %q, want %q", snap.Reason, "store constructed")
	}
	if !snap.LastTransitionAt.Equal(t0) {
		t.Errorf("initial LastTransitionAt = %v, want %v", snap.LastTransitionAt, t0)
	}
}

// legalCells lists every (source, event, destination) edge from
// contracts/state-table.md (19 cells). The state-table tests are
// table-driven from this slice.
type legalCell struct {
	src  State
	ev   Event
	dst  State
	name string
}

var legalCells = []legalCell{ //nolint:gochecknoglobals // test fixture: 19 legal-cell slice transcribed from contracts/state-table.md
	{StateFetching, EventFetchOK, StateRunning, "fetching+fetch-ok"},
	{StateFetching, EventFetchAuthRequired, StateAwaitingApproval, "fetching+fetch-auth-required"},
	{StateFetching, EventClaimDenied, StateAwaitingApproval, "fetching+claim-denied"},
	{StateFetching, EventClaimUnavailable, StateAwaitingApproval, "fetching+claim-unavailable"},
	{StateFetching, EventValidatorFailed, StateAwaitingApproval, "fetching+validator-failed"},
	{StateFetching, EventBootRetryExhausted, StateStopped, "fetching+boot-retry-exhausted"},
	{StateFetching, EventStopRequested, StateStopped, "fetching+stop-requested"},
	{StateRunning, EventChildExitClean, StateFetching, "running+child-exit-clean"},
	{StateRunning, EventChildExitCrash, StateFetching, "running+child-exit-crash"},
	{StateRunning, EventChildExit78Stale, StateAwaitingApproval, "running+child-exit-78-stale"},
	{StateRunning, EventRefreshRequested, StateFetching, "running+refresh-requested"},
	{StateRunning, EventGraceRestartTriggered, StateGraceRestart, "running+grace-restart-triggered"},
	{StateRunning, EventStopRequested, StateStopped, "running+stop-requested"},
	{StateAwaitingApproval, EventApprovalGranted, StateFetching, "awaiting-approval+approval-granted"},
	{StateAwaitingApproval, EventStopRequested, StateStopped, "awaiting-approval+stop-requested"},
	{StateGraceRestart, EventGraceRestartOK, StateRunning, "grace-restart+grace-restart-ok"},
	{StateGraceRestart, EventGraceExpired, StateAwaitingApproval, "grace-restart+grace-expired"},
	{StateGraceRestart, EventStopRequested, StateStopped, "grace-restart+stop-requested"},
	{StateStopped, EventStopRequested, StateStopped, "stopped+stop-requested"},
}

// ---------- T-03: TestStore_LegalTransitions ----------

func TestStore_LegalTransitions(t *testing.T) { //nolint:gocognit // structural complexity: table-driven over 19 legal cells with prefix-replay + post-state assertions
	t.Parallel()
	if got, want := len(legalCells), 19; got != want {
		t.Fatalf("legalCells size = %d, want 19 (matrix is locked)", got)
	}
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range legalCells {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clk := testutil.NewFakeClock(t0)
			s := NewStore(context.Background(), clk)
			for _, p := range prefixFor(tc.src) {
				if err := s.Transition(context.Background(), p); err != nil {
					t.Fatalf("prefix %q: unexpected error: %v", p, err)
				}
			}
			if got := s.Snapshot().State; got != tc.src {
				t.Fatalf("after prefix, state = %q, want source %q", got, tc.src)
			}
			clk.Advance(time.Second)
			newNow := clk.Now()
			if err := s.Transition(context.Background(), tc.ev); err != nil {
				t.Fatalf("Transition(%q) returned unexpected error: %v", tc.ev, err)
			}
			snap := s.Snapshot()
			if snap.State != tc.dst {
				t.Errorf("post-state = %q, want %q", snap.State, tc.dst)
			}
			if !snap.LastTransitionAt.Equal(newNow) {
				t.Errorf("LastTransitionAt = %v, want %v", snap.LastTransitionAt, newNow)
			}
			if want := reasons[tc.ev]; snap.Reason != want {
				t.Errorf("Reason = %q, want %q", snap.Reason, want)
			}
		})
	}
}

// ---------- T-04: TestStore_IllegalTransitionErr ----------

func TestStore_IllegalTransitionErr(t *testing.T) { //nolint:gocognit,gocyclo // structural complexity: table-driven over 56 illegal cells with pre/post snapshot equality
	t.Parallel()
	legal := make(map[State]map[Event]struct{}, len(allStates))
	for _, s := range allStates {
		legal[s] = map[Event]struct{}{}
	}
	for _, c := range legalCells {
		legal[c.src][c.ev] = struct{}{}
	}

	type illegalCell struct {
		src State
		ev  Event
	}
	var illegal []illegalCell
	for _, src := range allStates {
		for _, ev := range allEvents {
			if _, ok := legal[src][ev]; ok {
				continue
			}
			illegal = append(illegal, illegalCell{src, ev})
		}
	}
	if got, want := len(illegal), 56; got != want {
		t.Fatalf("illegal-cell count = %d, want 56", got)
	}

	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	for _, ic := range illegal {
		name := fmt.Sprintf("%s+%s", ic.src, ic.ev)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			clk := testutil.NewFakeClock(t0)
			s := NewStore(context.Background(), clk)
			for _, p := range prefixFor(ic.src) {
				if err := s.Transition(context.Background(), p); err != nil {
					t.Fatalf("prefix %q: unexpected error: %v", p, err)
				}
			}
			pre := s.Snapshot()
			if pre.State != ic.src {
				t.Fatalf("after prefix, state = %q, want source %q", pre.State, ic.src)
			}
			clk.Advance(time.Second)
			err := s.Transition(context.Background(), ic.ev)
			if err == nil {
				t.Fatalf("Transition(%q) returned nil; want ErrInvalidTransition", ic.ev)
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("err = %v; want errors.Is(_, ErrInvalidTransition)", err)
			}
			msg := err.Error()
			if !strings.Contains(msg, string(ic.src)) {
				t.Errorf("err %q does not contain source state %q", msg, ic.src)
			}
			if !strings.Contains(msg, string(ic.ev)) {
				t.Errorf("err %q does not contain event %q", msg, ic.ev)
			}
			post := s.Snapshot()
			if post.State != pre.State {
				t.Errorf("State changed after rejected transition: pre=%q post=%q", pre.State, post.State)
			}
			if post.ChildPID != pre.ChildPID {
				t.Errorf("ChildPID changed after rejected transition: pre=%d post=%d", pre.ChildPID, post.ChildPID)
			}
			if !post.LastTransitionAt.Equal(pre.LastTransitionAt) {
				t.Errorf("LastTransitionAt changed after rejected transition: pre=%v post=%v", pre.LastTransitionAt, post.LastTransitionAt)
			}
			if post.Token != pre.Token {
				t.Errorf("Token changed after rejected transition")
			}
			if post.Reason != pre.Reason {
				t.Errorf("Reason changed after rejected transition: pre=%q post=%q", pre.Reason, post.Reason)
			}
		})
	}
}

// ---------- T-05: TestStore_StopIsIdempotent ----------

func TestStore_StopIsIdempotent(t *testing.T) { //nolint:gocognit,gocyclo // structural complexity: idempotency test plus rejection sweep across 14 non-stop events
	t.Parallel()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	clk := testutil.NewFakeClock(t0)
	s := NewStore(context.Background(), clk)
	if err := s.Transition(context.Background(), EventStopRequested); err != nil {
		t.Fatalf("first stop: unexpected error: %v", err)
	}
	if got := s.Snapshot().State; got != StateStopped {
		t.Fatalf("after first stop, state = %q, want %q", got, StateStopped)
	}
	clk.Advance(time.Second)
	newNow := clk.Now()
	if err := s.Transition(context.Background(), EventStopRequested); err != nil {
		t.Fatalf("idempotent stop: unexpected error: %v", err)
	}
	snap := s.Snapshot()
	if snap.State != StateStopped {
		t.Errorf("after idempotent stop, state = %q, want %q", snap.State, StateStopped)
	}
	if !snap.LastTransitionAt.Equal(newNow) {
		t.Errorf("LastTransitionAt = %v, want %v (advanced on idempotent stop)", snap.LastTransitionAt, newNow)
	}
	if snap.Reason != "stop requested" {
		t.Errorf("Reason = %q, want %q", snap.Reason, "stop requested")
	}
	pre := s.Snapshot()
	for _, ev := range allEvents {
		if ev == EventStopRequested {
			continue
		}
		err := s.Transition(context.Background(), ev)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("Transition(%q) from stopped: err=%v; want ErrInvalidTransition", ev, err)
		}
		post := s.Snapshot()
		if post != pre {
			t.Errorf("snapshot mutated by rejected event %q: pre=%+v post=%+v", ev, pre, post)
		}
	}
}

// ---------- T-06: TestStore_GraceRestartReentry ----------

func TestStore_GraceRestartReentry(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	clk := testutil.NewFakeClock(t0)
	s := NewStore(context.Background(), clk)
	mustTransition(t, s, EventFetchOK, StateRunning)
	mustTransition(t, s, EventGraceRestartTriggered, StateGraceRestart)
	mustTransition(t, s, EventGraceRestartOK, StateRunning)
	mustTransition(t, s, EventGraceRestartTriggered, StateGraceRestart)
	mustTransition(t, s, EventGraceRestartOK, StateRunning)
	mustTransition(t, s, EventGraceRestartTriggered, StateGraceRestart)
	mustTransition(t, s, EventGraceExpired, StateAwaitingApproval)
}

func mustTransition(t *testing.T, s *Store, ev Event, want State) {
	t.Helper()
	if err := s.Transition(context.Background(), ev); err != nil {
		t.Fatalf("Transition(%q): unexpected error: %v", ev, err)
	}
	if got := s.Snapshot().State; got != want {
		t.Fatalf("after Transition(%q), state = %q, want %q", ev, got, want)
	}
}

// ---------- T-07: TestStore_TwoStepRecoveryFromAwaitingApproval ----------

func TestStore_TwoStepRecoveryFromAwaitingApproval(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	clk := testutil.NewFakeClock(t0)
	s := NewStore(context.Background(), clk)
	mustTransition(t, s, EventFetchAuthRequired, StateAwaitingApproval)
	mustTransition(t, s, EventApprovalGranted, StateFetching)
	mustTransition(t, s, EventFetchOK, StateRunning)

	// Re-prove there is no composite shortcut: from a fresh awaiting-approval,
	// EventApprovalGranted lands on fetching (not running), and a subsequent
	// EventValidatorFailed re-enters awaiting-approval.
	clk2 := testutil.NewFakeClock(t0)
	s2 := NewStore(context.Background(), clk2)
	mustTransition(t, s2, EventFetchAuthRequired, StateAwaitingApproval)
	mustTransition(t, s2, EventApprovalGranted, StateFetching)
	mustTransition(t, s2, EventValidatorFailed, StateAwaitingApproval)
}

// ---------- T-08: TestStore_SnapshotIsDefensiveCopy ----------

func TestStore_SnapshotIsDefensiveCopy(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	clk := testutil.NewFakeClock(t0)
	s := NewStore(context.Background(), clk)
	clk.Advance(time.Second)
	if err := s.Transition(context.Background(), EventFetchOK); err != nil {
		t.Fatalf("EventFetchOK: %v", err)
	}
	tok, err := securebytes.New([]byte("snapshot-token"))
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	t.Cleanup(func() { _ = tok.Destroy() })
	s.setToken(tok)

	snap1 := s.Snapshot()
	original := snap1

	// Mutate every public field of the local snapshot copy.
	snap1.State = StateStopped
	snap1.ChildPID = 99
	snap1.LastTransitionAt = snap1.LastTransitionAt.Add(time.Hour)
	snap1.Reason = "tampered"
	snap1.Token = nil

	snap2 := s.Snapshot()
	if snap2.State != original.State {
		t.Errorf("store State leaked through: snap2=%q want %q", snap2.State, original.State)
	}
	if snap2.ChildPID != original.ChildPID {
		t.Errorf("store ChildPID leaked through: snap2=%d want %d", snap2.ChildPID, original.ChildPID)
	}
	if !snap2.LastTransitionAt.Equal(original.LastTransitionAt) {
		t.Errorf("store LastTransitionAt leaked through: snap2=%v want %v", snap2.LastTransitionAt, original.LastTransitionAt)
	}
	if snap2.Reason != original.Reason {
		t.Errorf("store Reason leaked through: snap2=%q want %q", snap2.Reason, original.Reason)
	}
	if snap2.Token != original.Token {
		t.Errorf("store Token pointer changed: snap2=%v want %v", snap2.Token, original.Token)
	}
}

// ---------- T-09: TestStore_TokenLogValueRedacts ----------

func TestStore_TokenLogValueRedacts(t *testing.T) {
	t.Parallel()
	plaintext := []byte("sensitive-jwt-bytes-abc")
	plainCopy := append([]byte(nil), plaintext...)
	tok, err := securebytes.New(plaintext)
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	t.Cleanup(func() { _ = tok.Destroy() })

	clk := testutil.NewFakeClock(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC))
	s := NewStore(context.Background(), clk)
	s.setToken(tok)
	snap := s.Snapshot()

	// Log the whole snapshot.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.LogAttrs(context.Background(), slog.LevelInfo, "supervise.snapshot", slog.Any("snap", snap))
	output := buf.String()
	if !strings.Contains(output, "[redacted]") {
		t.Errorf("snapshot log output missing [redacted]: %s", output)
	}
	assertNoPlaintextLeak(t, output, plainCopy)

	// Log the bare Token field.
	buf.Reset()
	logger.LogAttrs(context.Background(), slog.LevelInfo, "token", slog.Any("token", snap.Token))
	output = buf.String()
	if !strings.Contains(output, "[redacted]") {
		t.Errorf("token-only log output missing [redacted]: %s", output)
	}
	assertNoPlaintextLeak(t, output, plainCopy)
}

func assertNoPlaintextLeak(t *testing.T, output string, plaintext []byte) {
	t.Helper()
	if strings.Contains(output, string(plaintext)) {
		t.Errorf("log output leaked full plaintext: %s", output)
	}
	const window = 6
	for i := 0; i+window <= len(plaintext); i++ {
		sub := string(plaintext[i : i+window])
		if strings.Contains(output, sub) {
			t.Errorf("log output leaked %d-byte plaintext window %q: %s", window, sub, output)
		}
	}
}

// ---------- T-10: TestStore_TokenZeroOnRelease ----------

func TestStore_TokenZeroOnRelease(t *testing.T) {
	t.Parallel()
	tok, err := securebytes.New([]byte("plaintext-to-zero"))
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	clk := testutil.NewFakeClock(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC))
	s := NewStore(context.Background(), clk)
	s.setToken(tok)

	var seen []byte
	if useErr := tok.Use(func(b []byte) {
		seen = append(seen, b...)
	}); useErr != nil {
		t.Fatalf("pre-destroy Use: %v", useErr)
	}
	if string(seen) != "plaintext-to-zero" {
		t.Fatalf("pre-destroy Use payload = %q, want %q", seen, "plaintext-to-zero")
	}

	if destroyErr := tok.Destroy(); destroyErr != nil {
		t.Fatalf("Destroy: %v", destroyErr)
	}
	err = tok.Use(func(b []byte) { _ = b })
	if !errors.Is(err, securebytes.ErrDestroyed) {
		t.Errorf("post-destroy Use err = %v; want errors.Is(_, securebytes.ErrDestroyed)", err)
	}
}

// ---------- T-11: TestStore_ConcurrentTransitionAndSnapshot ----------

func TestStore_ConcurrentTransitionAndSnapshot(t *testing.T) { //nolint:gocognit // structural complexity: goroutine fan-out + race-clean assertions per Constitution VIII
	t.Parallel()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	clk := testutil.NewFakeClock(t0)
	s := NewStore(context.Background(), clk)

	const transitionGoroutines = 8
	const readerGoroutines = 8
	const iterations = 100

	var transitionWG sync.WaitGroup
	transitionWG.Add(transitionGoroutines)
	for range transitionGoroutines {
		go func() {
			defer transitionWG.Done()
			ctx := context.Background()
			for range iterations {
				clk.Advance(time.Microsecond)
				_ = s.Transition(ctx, EventFetchOK)
				clk.Advance(time.Microsecond)
				_ = s.Transition(ctx, EventChildExitClean)
			}
		}()
	}

	stop := make(chan struct{})
	var readerWG sync.WaitGroup
	readerWG.Add(readerGoroutines)
	for range readerGoroutines {
		go func() {
			defer readerWG.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				snap := s.Snapshot()
				switch snap.State {
				case StateFetching, StateRunning, StateAwaitingApproval, StateGraceRestart, StateStopped:
					// observed value is in the closed vocabulary
				default:
					t.Errorf("snapshot State outside vocabulary: %q", snap.State)
					return
				}
				if snap.LastTransitionAt.Before(t0) {
					t.Errorf("snapshot LastTransitionAt %v predates t0 %v", snap.LastTransitionAt, t0)
					return
				}
			}
		}()
	}

	transitionWG.Wait()
	close(stop)
	readerWG.Wait()
}

// ---------- T-12: TestStore_NoSideEffects ----------

func TestStore_NoSideEffects(t *testing.T) {
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	clk := testutil.NewFakeClock(t0)
	before := runtime.NumGoroutine()
	s := NewStore(context.Background(), clk)

	// Drive a sequence covering every legal edge of the matrix.
	seq := []Event{
		// fetching -> running (Scenario 2/3 etc.)
		EventFetchOK,
		// running -> fetching via clean exit
		EventChildExitClean,
		EventFetchOK,
		// running -> fetching via crash
		EventChildExitCrash,
		EventFetchOK,
		// running -> fetching via refresh
		EventRefreshRequested,
		EventFetchOK,
		// running -> awaiting-approval via 78
		EventChildExit78Stale,
		EventApprovalGranted,
		EventFetchOK,
		// running -> grace-restart -> running (Scenario 9)
		EventGraceRestartTriggered,
		EventGraceRestartOK,
		// running -> grace-restart -> awaiting-approval
		EventGraceRestartTriggered,
		EventGraceExpired,
		EventApprovalGranted,
		// fetching variants of awaiting-approval entry
		EventFetchAuthRequired,
		EventApprovalGranted,
		EventClaimDenied,
		EventApprovalGranted,
		EventClaimUnavailable,
		EventApprovalGranted,
		EventValidatorFailed,
		EventApprovalGranted,
		// fetching -> stopped via boot retry
		EventBootRetryExhausted,
		// stopped is terminal; idempotent stop
		EventStopRequested,
	}
	for _, ev := range seq {
		_ = s.Transition(context.Background(), ev)
		_ = s.Snapshot()
		clk.Advance(time.Microsecond)
	}
	// NewStore + Transition + Snapshot must spawn no goroutines of their own.
	// runtime.NumGoroutine is process-global, so unrelated runtime/sibling
	// goroutines can drift the count up or down transiently; poll until the
	// delta settles to non-positive — a genuine Store leak would persist.
	runtime.GC()
	deadline := time.Now().Add(500 * time.Millisecond)
	delta := runtime.NumGoroutine() - before
	for time.Now().Before(deadline) && delta > 0 {
		time.Sleep(10 * time.Millisecond)
		delta = runtime.NumGoroutine() - before
	}
	if delta > 0 {
		t.Errorf("goroutine leak: delta = %d (before=%d, after=%d)", delta, before, runtime.NumGoroutine())
	}
}

// ---------- T-15: TestStore_TransitionUnknownEvent ----------

func TestStore_TransitionUnknownEvent(t *testing.T) {
	t.Parallel()
	clk := testutil.NewFakeClock(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC))
	s := NewStore(context.Background(), clk)
	bogus := Event("garbage-not-in-set")
	err := s.Transition(context.Background(), bogus)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("err = %v; want errors.Is(_, ErrInvalidTransition)", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "fetching") {
		t.Errorf("err %q does not contain source state %q", msg, "fetching")
	}
	if !strings.Contains(msg, "garbage-not-in-set") {
		t.Errorf("err %q does not contain event %q", msg, "garbage-not-in-set")
	}
}

// TestStore_SetTokenDestroysPrior verifies that replacing the stored
// JWT explicitly zeroes the prior *SecureBytes (Principle VI / Layer 5
// — explicit zeroing on lifecycle transitions, not waiting for the GC
// finalizer). Without this discipline, rotating JWTs across refresh
// cycles accumulates plaintext bearer-token bytes in mlocked memory
// that remain reachable via same-user /proc/$$/mem until GC sweeps.
func TestStore_SetTokenDestroysPrior(t *testing.T) {
	t.Parallel()
	clk := testutil.NewFakeClock(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC))
	s := NewStore(context.Background(), clk)

	first, err := securebytes.New([]byte("jwt-generation-1"))
	if err != nil {
		t.Fatalf("first securebytes.New: %v", err)
	}
	second, err := securebytes.New([]byte("jwt-generation-2"))
	if err != nil {
		t.Fatalf("second securebytes.New: %v", err)
	}
	t.Cleanup(func() { _ = second.Destroy() })

	s.setToken(first)
	if useErr := first.Use(func(_ []byte) {}); useErr != nil {
		t.Fatalf("first.Use after setToken: %v want nil", useErr)
	}

	s.setToken(second)
	if useErr := first.Use(func(_ []byte) {}); !errors.Is(useErr, securebytes.ErrDestroyed) {
		t.Errorf("first.Use after replacement: %v want ErrDestroyed", useErr)
	}
	if useErr := second.Use(func(_ []byte) {}); useErr != nil {
		t.Errorf("second.Use after setToken: %v want nil", useErr)
	}
	if snap := s.Snapshot(); snap.Token != second {
		t.Errorf("Snapshot.Token after replacement is not the new pointer")
	}
}

// TestStore_SetTokenIdempotentOnSamePointer verifies that passing the
// SAME pointer twice does NOT destroy it. Defensive guard against a
// caller that re-injects the live token (e.g. a no-op refresh path or
// a test fixture).
func TestStore_SetTokenIdempotentOnSamePointer(t *testing.T) {
	t.Parallel()
	clk := testutil.NewFakeClock(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC))
	s := NewStore(context.Background(), clk)

	tok, err := securebytes.New([]byte("idempotent-jwt"))
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	t.Cleanup(func() { _ = tok.Destroy() })

	s.setToken(tok)
	s.setToken(tok)
	if useErr := tok.Use(func(_ []byte) {}); useErr != nil {
		t.Errorf("tok.Use after self-replacement: %v want nil (same-pointer setToken must be a no-op)", useErr)
	}
}

// TestStore_SetTokenNilClearsAndDestroys verifies that passing nil
// clears the slot AND destroys the prior token — the orchestrator can
// use this idiom (or destroyToken) to explicitly retire a session.
func TestStore_SetTokenNilClearsAndDestroys(t *testing.T) {
	t.Parallel()
	clk := testutil.NewFakeClock(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC))
	s := NewStore(context.Background(), clk)

	tok, err := securebytes.New([]byte("about-to-be-cleared"))
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	s.setToken(tok)
	s.setToken(nil)

	if useErr := tok.Use(func(_ []byte) {}); !errors.Is(useErr, securebytes.ErrDestroyed) {
		t.Errorf("tok.Use after setToken(nil): %v want ErrDestroyed", useErr)
	}
	if snap := s.Snapshot(); snap.Token != nil {
		t.Errorf("Snapshot.Token after setToken(nil) = %v want nil", snap.Token)
	}
}

// TestStore_DestroyTokenZeroesAndClears verifies that destroyToken
// explicitly zeroes the current JWT plaintext and nils the slot. Used
// by Lifecycle.runShutdown so the supervisor's SIGTERM path retires
// the bearer token rather than leaving it to the (process-exit-skipped)
// runtime finalizer.
func TestStore_DestroyTokenZeroesAndClears(t *testing.T) {
	t.Parallel()
	clk := testutil.NewFakeClock(time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC))
	s := NewStore(context.Background(), clk)

	tok, err := securebytes.New([]byte("shutdown-token"))
	if err != nil {
		t.Fatalf("securebytes.New: %v", err)
	}
	s.setToken(tok)

	s.destroyToken()

	if useErr := tok.Use(func(_ []byte) {}); !errors.Is(useErr, securebytes.ErrDestroyed) {
		t.Errorf("tok.Use after destroyToken: %v want ErrDestroyed", useErr)
	}
	if snap := s.Snapshot(); snap.Token != nil {
		t.Errorf("Snapshot.Token after destroyToken = %v want nil", snap.Token)
	}

	// Idempotent — second call is a silent no-op.
	s.destroyToken()
	if snap := s.Snapshot(); snap.Token != nil {
		t.Errorf("Snapshot.Token after second destroyToken = %v want nil", snap.Token)
	}
}
