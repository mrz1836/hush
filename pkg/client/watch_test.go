package client_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mrz1836/hush/pkg/client"
)

// scriptedSocket binds a Unix listener that serves a configurable
// sequence of status documents — one per accepted connection. Lets a
// test simulate a supervisor whose state evolves over time.
type scriptedSocket struct {
	t       *testing.T
	path    string
	mu      sync.Mutex
	replies [][]byte
	idx     int
	counter int
}

func newScriptedSocket(t *testing.T) *scriptedSocket {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "h23w-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")

	s := &scriptedSocket{t: t, path: path}

	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "unix", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, aerr := listener.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 64)
				_, _ = c.Read(buf)
				s.mu.Lock()
				s.counter++
				body := s.currentReplyLocked()
				s.mu.Unlock()
				_, _ = c.Write(body)
			}(conn)
		}
	}()
	return s
}

// SetReplies replaces the scripted reply sequence.
func (s *scriptedSocket) SetReplies(replies ...[]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = replies
	s.idx = 0
}

// Calls returns the number of connections served.
func (s *scriptedSocket) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counter
}

// currentReplyLocked returns the current scripted reply, advancing
// the index until it points at the last entry (which is sticky).
func (s *scriptedSocket) currentReplyLocked() []byte {
	if len(s.replies) == 0 {
		return []byte("{}\n")
	}
	if s.idx >= len(s.replies) {
		return s.replies[len(s.replies)-1]
	}
	body := s.replies[s.idx]
	s.idx++
	return body
}

func statusBytes(t *testing.T, m map[string]any) []byte {
	t.Helper()
	defaults := map[string]any{
		"supervisor":          "ex",
		"state":               "running",
		"session_expires_at":  "0001-01-01T00:00:00Z",
		"session_jti":         "",
		"restart_count":       0,
		"refresh_window_next": "0001-01-01T00:00:00Z",
		"scope_healthy":       []string{},
		"scope_stale":         []string{},
		"last_auth_failure":   nil,
		"child_pid":           nil,
		"child_uptime":        "0s",
		"discord_connected":   true,
	}
	for k, v := range m {
		defaults[k] = v
	}
	b, err := json.Marshal(defaults)
	require.NoError(t, err)
	return append(b, '\n')
}

// drain reads up to maxEvents events with a per-event timeout.
// Returns after exhausting either the count or the deadline.
func drain(t *testing.T, ch <-chan client.Event, maxEvents int, timeout time.Duration) []client.Event {
	t.Helper()
	out := make([]client.Event, 0, maxEvents)
	for range maxEvents {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, ev)
		case <-time.After(timeout):
			return out
		}
	}
	return out
}

// =====================================================================
// Initial event
// =====================================================================

func TestWatch_EmitsInitialEvent(t *testing.T) {
	s := newScriptedSocket(t)
	s.SetReplies(statusBytes(t, map[string]any{"state": "running"}))

	sup := client.NewSupervisorStatus(s.path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := sup.Watch(ctx, client.WatchOptions{PollInterval: time.Second})
	require.NoError(t, err)

	evs := drain(t, ch, 1, 2*time.Second)
	require.Len(t, evs, 1)
	assert.Equal(t, client.EventInitial, evs[0].Type)
	require.NotNil(t, evs[0].Status)
	assert.Equal(t, client.State("running"), evs[0].Status.State)
}

func TestWatch_InitialError_EmitsErrorEventNotClose(t *testing.T) {
	// Socket doesn't exist → first Snapshot fails.
	sup := client.NewSupervisorStatus("/tmp/hush-watch-nope.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := sup.Watch(ctx, client.WatchOptions{PollInterval: time.Second})
	require.NoError(t, err)

	evs := drain(t, ch, 1, 2*time.Second)
	require.Len(t, evs, 1)
	assert.Equal(t, client.EventError, evs[0].Type)
	require.Error(t, evs[0].Err)
}

// =====================================================================
// State change
// =====================================================================

func TestWatch_StateChange(t *testing.T) {
	s := newScriptedSocket(t)
	s.SetReplies(
		statusBytes(t, map[string]any{"state": "running"}),
		statusBytes(t, map[string]any{"state": "awaiting-approval"}),
	)

	sup := client.NewSupervisorStatus(s.path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := sup.Watch(ctx, client.WatchOptions{PollInterval: 50 * time.Millisecond})
	require.NoError(t, err)

	evs := drain(t, ch, 2, 2*time.Second)
	require.Len(t, evs, 2)
	assert.Equal(t, client.EventInitial, evs[0].Type)
	assert.Equal(t, client.EventStateChange, evs[1].Type)
	assert.Equal(t, client.State("awaiting-approval"), evs[1].Status.State)
}

// =====================================================================
// Session renewed
// =====================================================================

func TestWatch_SessionRenewed_ResetsExpiryThresholds(t *testing.T) {
	s := newScriptedSocket(t)
	// First session expires in 10s; second session in 1h.
	soon := time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339)
	later := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	s.SetReplies(
		statusBytes(t, map[string]any{
			"state": "running", "session_jti": "old",
			"session_expires_at": soon,
		}),
		statusBytes(t, map[string]any{
			"state": "running", "session_jti": "new",
			"session_expires_at": later,
		}),
	)

	sup := client.NewSupervisorStatus(s.path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := sup.Watch(ctx, client.WatchOptions{
		PollInterval:     50 * time.Millisecond,
		ExpiryThresholds: []time.Duration{30 * time.Second},
	})
	require.NoError(t, err)

	evs := drain(t, ch, 4, 2*time.Second)
	// Expect: Initial, ExpiresSoon (since 10s < 30s), SessionRenewed.
	types := make([]client.EventType, 0, len(evs))
	for _, ev := range evs {
		types = append(types, ev.Type)
	}
	assert.Contains(t, types, client.EventInitial)
	assert.Contains(t, types, client.EventExpiresSoon)
	assert.Contains(t, types, client.EventSessionRenewed)
}

// =====================================================================
// ExpiresSoon — ladder of thresholds
// =====================================================================

func TestWatch_ExpiresSoon_FiresAtThresholds(t *testing.T) {
	s := newScriptedSocket(t)
	// Session expires in 80ms; thresholds at 60ms and 30ms before
	// expiry mean events should fire at ~20ms and ~50ms after start.
	expiresAt := time.Now().Add(80 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	s.SetReplies(statusBytes(t, map[string]any{
		"state": "running", "session_jti": "j1",
		"session_expires_at": expiresAt,
	}))

	sup := client.NewSupervisorStatus(s.path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := sup.Watch(ctx, client.WatchOptions{
		PollInterval:     5 * time.Second, // way longer than test runtime — events come from timers, not polls
		ExpiryThresholds: []time.Duration{60 * time.Millisecond, 30 * time.Millisecond},
	})
	require.NoError(t, err)

	evs := drain(t, ch, 3, 500*time.Millisecond)
	// Expect Initial, then two ExpiresSoon events (60ms then 30ms).
	expires := []time.Duration{}
	for _, ev := range evs {
		if ev.Type == client.EventExpiresSoon {
			expires = append(expires, ev.Threshold)
		}
	}
	require.Len(t, expires, 2, "got events: %+v", evs)
	// Largest threshold fires first (earliest warning).
	assert.Equal(t, 60*time.Millisecond, expires[0])
	assert.Equal(t, 30*time.Millisecond, expires[1])
}

func TestWatch_ExpiresSoon_DoesNotRefireSameThreshold(t *testing.T) {
	s := newScriptedSocket(t)
	expiresAt := time.Now().Add(40 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	s.SetReplies(statusBytes(t, map[string]any{
		"state": "running", "session_jti": "j1",
		"session_expires_at": expiresAt,
	}))

	sup := client.NewSupervisorStatus(s.path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := sup.Watch(ctx, client.WatchOptions{
		PollInterval:     50 * time.Millisecond,
		ExpiryThresholds: []time.Duration{30 * time.Millisecond},
	})
	require.NoError(t, err)

	// Drain for 400ms — even though the poll fires multiple times
	// past the threshold, ExpiresSoon should appear exactly once.
	evs := drain(t, ch, 20, 400*time.Millisecond)
	expiresCount := 0
	for _, ev := range evs {
		if ev.Type == client.EventExpiresSoon {
			expiresCount++
		}
	}
	assert.Equal(t, 1, expiresCount, "ExpiresSoon must not re-fire; got %+v", evs)
}

// =====================================================================
// Scope-health change
// =====================================================================

func TestWatch_ScopeHealthChange(t *testing.T) {
	s := newScriptedSocket(t)
	s.SetReplies(
		statusBytes(t, map[string]any{
			"scope_healthy": []string{"A", "B"}, "scope_stale": []string{},
		}),
		statusBytes(t, map[string]any{
			"scope_healthy": []string{"A"}, "scope_stale": []string{"B"},
		}),
	)

	sup := client.NewSupervisorStatus(s.path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := sup.Watch(ctx, client.WatchOptions{PollInterval: 50 * time.Millisecond})
	require.NoError(t, err)

	evs := drain(t, ch, 2, 2*time.Second)
	require.Len(t, evs, 2)
	assert.Equal(t, client.EventInitial, evs[0].Type)
	assert.Equal(t, client.EventScopeHealthChange, evs[1].Type)
	assert.Equal(t, []string{"B"}, evs[1].Status.ScopeStale)
}

// =====================================================================
// Context cancel closes the channel
// =====================================================================

func TestWatch_ContextCancelClosesChannel(t *testing.T) {
	s := newScriptedSocket(t)
	s.SetReplies(statusBytes(t, map[string]any{"state": "running"}))

	sup := client.NewSupervisorStatus(s.path)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := sup.Watch(ctx, client.WatchOptions{PollInterval: 50 * time.Millisecond})
	require.NoError(t, err)

	// Drain the initial event so the loop is ticking.
	evs := drain(t, ch, 1, time.Second)
	require.Len(t, evs, 1)

	cancel()
	// Channel must close within a poll interval.
	select {
	case _, open := <-ch:
		// Either an in-flight event arrived, or the channel closed.
		// Drain until close.
		for open {
			_, open = <-ch
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after ctx cancel")
	}
}

// =====================================================================
// Transient error keeps the channel alive
// =====================================================================

func TestWatch_PollErrorEmitsErrorAndContinues(t *testing.T) {
	s := newScriptedSocket(t)
	// First poll OK, second returns invalid JSON → Error event, then OK.
	s.SetReplies(
		statusBytes(t, map[string]any{"state": "running"}),
		[]byte("not json\n"),
		statusBytes(t, map[string]any{"state": "awaiting-approval"}),
	)

	sup := client.NewSupervisorStatus(s.path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := sup.Watch(ctx, client.WatchOptions{PollInterval: 50 * time.Millisecond})
	require.NoError(t, err)

	evs := drain(t, ch, 4, 2*time.Second)
	types := make([]client.EventType, 0, len(evs))
	for _, ev := range evs {
		types = append(types, ev.Type)
	}
	assert.Contains(t, types, client.EventInitial)
	assert.Contains(t, types, client.EventError)
	assert.Contains(t, types, client.EventStateChange, "Watch must continue after a transient error")
}

// =====================================================================
// Options defaults
// =====================================================================

func TestWatch_DefaultsApplied(t *testing.T) {
	s := newScriptedSocket(t)
	s.SetReplies(statusBytes(t, map[string]any{"state": "running"}))

	sup := client.NewSupervisorStatus(s.path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Zero-value options must apply defaults without panicking.
	ch, err := sup.Watch(ctx, client.WatchOptions{})
	require.NoError(t, err)

	evs := drain(t, ch, 1, 5*time.Second)
	require.Len(t, evs, 1)
	assert.Equal(t, client.EventInitial, evs[0].Type)
}

func TestWatch_NilReceiver(t *testing.T) {
	var sup *client.SupervisorStatus
	_, err := sup.Watch(context.Background(), client.WatchOptions{})
	require.Error(t, err)
}

// =====================================================================
// Panic recovery (Principle VII: every goroutine recover()s at top frame)
// =====================================================================

func TestRecoverWatchPanic_EmitsEventError(t *testing.T) {
	ch := make(chan client.Event, 1)
	func() {
		defer client.RecoverWatchPanic(context.Background(), ch)
		panic("induced for test")
	}()
	select {
	case ev := <-ch:
		assert.Equal(t, client.EventError, ev.Type)
		require.Error(t, ev.Err)
		assert.Contains(t, ev.Err.Error(), "induced for test")
		assert.Contains(t, ev.Err.Error(), "panic")
	case <-time.After(time.Second):
		t.Fatal("recoverWatchPanic did not emit EventError")
	}
}

func TestRecoverWatchPanic_NoOpWhenNoPanic(t *testing.T) {
	ch := make(chan client.Event, 1)
	func() {
		defer client.RecoverWatchPanic(context.Background(), ch)
		// no panic
	}()
	select {
	case ev := <-ch:
		t.Fatalf("recoverWatchPanic emitted an event with no panic: %+v", ev)
	case <-time.After(50 * time.Millisecond):
		// expected: nothing sent
	}
}

func TestRecoverWatchPanic_RespectsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled
	ch := make(chan client.Event)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer client.RecoverWatchPanic(ctx, ch)
		panic("induced")
	}()
	select {
	case <-done:
		// expected: handler returned via ctx.Done() race
	case <-time.After(time.Second):
		t.Fatal("recoverWatchPanic blocked despite ctx cancel")
	}
}

func TestSendBlocking_DeliversToConsumer(t *testing.T) {
	ch := make(chan client.Event, 1)
	ch <- client.Event{Type: client.EventInitial} // saturate
	delivered := make(chan struct{})
	go func() {
		client.SendBlocking(context.Background(), ch, client.Event{Type: client.EventError})
		close(delivered)
	}()
	// Drain the sentinel; sendBlocking should now unblock.
	<-ch
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("sendBlocking did not deliver after drain")
	}
	select {
	case ev := <-ch:
		assert.Equal(t, client.EventError, ev.Type)
	case <-time.After(time.Second):
		t.Fatal("EventError not on channel after sendBlocking returned")
	}
}

func TestSendBlocking_ReturnsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan client.Event) // unbuffered, no consumer
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.SendBlocking(ctx, ch, client.Event{Type: client.EventError})
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sendBlocking did not return after ctx cancel")
	}
}

// =====================================================================
// Smallest-threshold EventExpiresSoon survives buffer backpressure
// =====================================================================

func TestWatch_SmallestExpiresSoon_DeliversUnderBackpressure(t *testing.T) {
	s := newScriptedSocket(t)
	// RFC3339 has seconds precision; format `now + 30s` so the
	// parsed value lands somewhere in [now+29s, now+30s].
	expires := time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339)
	s.SetReplies(statusBytes(t, map[string]any{
		"session_expires_at": expires,
		"session_jti":        "j1",
		"state":              "running",
	}))

	sup := client.NewSupervisorStatus(s.path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Buffer=1 + single-element ExpiryThresholds → the threshold IS
	// the smallest, so fireExpiresSoon takes the sendBlocking path.
	// A 29s threshold against a ~29-30s horizon means the threshold
	// is immediately overdue when the watch loop starts; the event
	// fires in the "already crossed" branch of watchOnce, which
	// guarantees the blocking-send path executes while EventInitial
	// still occupies the buffer.
	ch, err := sup.Watch(ctx, client.WatchOptions{
		PollInterval:     200 * time.Millisecond,
		ExpiryThresholds: []time.Duration{29 * time.Second},
		Buffer:           1,
	})
	require.NoError(t, err)

	first := <-ch
	require.Equal(t, client.EventInitial, first.Type)

	select {
	case ev := <-ch:
		assert.Equal(t, client.EventExpiresSoon, ev.Type, "smallest threshold must arrive even under backpressure")
		assert.Equal(t, 29*time.Second, ev.Threshold)
	case <-time.After(2 * time.Second):
		t.Fatal("EventExpiresSoon was dropped under backpressure")
	}
}

// =====================================================================
// Integration: a panic inside the watch goroutine surfaces as EventError
// =====================================================================

// scriptedPanicSocket sends a malformed status payload that causes
// statusWire.toStatus to error — which Watch propagates as EventError
// (not a panic). To exercise the recover path end-to-end we'd need a
// real panic surface in the loop; the unit tests above cover the
// recover helper directly.

// =====================================================================
// Sanity: production constants are exported for assertion
// =====================================================================

func TestConstants_ExposeProductionValues(t *testing.T) {
	assert.Equal(t, 30*time.Second, client.MeDefaultTimeout)
	assert.Equal(t, 5*time.Second, client.SupervisorDefaultTimeout)
	assert.Equal(t, 64*1024, client.SupervisorMaxResponseBytes)
}
