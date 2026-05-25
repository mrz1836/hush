package server

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/transport/sign"
)

// recordingNonceCache is a [sign.NonceCache] test double that records whether
// the chassis invoked Run (T1 regression — sign.NewNonceCache requires the
// caller to spawn the sweep goroutine; the chassis must satisfy that contract
// in Server.Run). All operations are concurrency-safe.
type recordingNonceCache struct {
	runCalled    chan struct{} // closed exactly once on entry to Run
	runReturned  chan struct{} // closed exactly once when Run returns
	runCallCount atomic.Int32
}

func newRecordingNonceCache() *recordingNonceCache {
	return &recordingNonceCache{
		runCalled:   make(chan struct{}),
		runReturned: make(chan struct{}),
	}
}

func (r *recordingNonceCache) Add(ctx context.Context, _ string, _ time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *recordingNonceCache) Run(ctx context.Context) {
	if r.runCallCount.Add(1) == 1 {
		close(r.runCalled)
		defer close(r.runReturned)
	}
	<-ctx.Done()
}

// TestRun_StartsNonceSweepGoroutine pins the T1 invariant: Server.Run MUST
// invoke nonceCache.Run() in a goroutine after startup checks pass. Without
// this, every accepted nonce stays resident forever and the chassis OOMs
// silently (Layer 4 / Constitution V).
//
// The test boots a chassis with an injected [recordingNonceCache], blocks on
// the cache's runCalled channel (so it cannot race with the lifecycle), then
// cancels the context and asserts the cache's Run returned. Channel-based
// synchronization avoids the CI-flaky "early cancel + time.Sleep" pattern.
func TestRun_StartsNonceSweepGoroutine(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	cache := newRecordingNonceCache()
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Listener = listener
		d.NonceCache = cache
	})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	// Wait for the sweep goroutine to enter Run before tearing the
	// chassis down — proves the chassis launched it.
	select {
	case <-cache.runCalled:
	case <-time.After(2 * time.Second):
		cancel()
		<-runDone
		t.Fatal("chassis did not invoke nonceCache.Run within 2s — T1 regression")
	}

	cancel()

	// The sweep goroutine must exit before Run returns (Run waits on
	// nonceSweepDone). Observe Run's return first; that guarantees the
	// sweep has unwound.
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run err=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after context cancel")
	}

	select {
	case <-cache.runReturned:
	default:
		t.Fatal("nonceCache.Run did not return by the time Server.Run returned")
	}

	if got := cache.runCallCount.Load(); got != 1 {
		t.Fatalf("nonceCache.Run call count = %d, want exactly 1", got)
	}
}

// TestRun_SweepExitsOnServeError pins the second half of T1: when
// httpServer.Serve returns a fatal error without ctx being cancelled, the
// chassis MUST still cancel the sweep goroutine via the derived sweep
// context. Otherwise a Serve crash leaks the sweep goroutine indefinitely.
func TestRun_SweepExitsOnServeError(t *testing.T) {
	t.Parallel()

	// closedListener returns http.ErrServerClosed-distinct errors from
	// Accept(), forcing httpServer.Serve to return a non-ErrServerClosed
	// error path (Run's `case err = <-serveErrCh` branch).
	cl, err := newClosedTestListener()
	if err != nil {
		t.Fatalf("closedTestListener: %v", err)
	}

	cache := newRecordingNonceCache()
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Listener = cl
		d.NonceCache = cache
	})

	// Pre-close the listener so Serve returns immediately with a non-
	// ErrServerClosed error — exercises the serveErrCh branch in Run.
	_ = cl.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	// Even though we never cancel ctx, the sweep must exit because the
	// chassis fires sweepCancel() in the shutdown path.
	select {
	case <-cache.runReturned:
	case <-time.After(2 * time.Second):
		cancel()
		<-runDone
		t.Fatal("sweep goroutine did not exit after Serve error — sweepCancel() not fired")
	}

	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Server.Run did not return after Serve error")
	}
}

// closedTestListener is a net.Listener whose Accept returns a permanent
// non-ErrServerClosed error as soon as Close() is called — used to drive
// the chassis's "Serve returned fatal error" branch deterministically.
type closedTestListener struct {
	inner net.Listener
}

func newClosedTestListener() (*closedTestListener, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &closedTestListener{inner: l}, nil
}

func (c *closedTestListener) Accept() (net.Conn, error) { return c.inner.Accept() }
func (c *closedTestListener) Close() error              { return c.inner.Close() }
func (c *closedTestListener) Addr() net.Addr            { return c.inner.Addr() }

// TestRun_SweepGoroutineTicksDuringLifetime is the end-to-end T1 regression:
// boot the chassis with a sweep wrapper that increments a counter on every
// tick of an internal timer; assert the counter advances during a normal
// Run lifecycle. A counter > 0 proves the chassis launched the sweep
// goroutine AND the goroutine kept ticking — the two failure modes T1
// covers ("never launched" and "launched but immediately exited").
//
// The wrapper ticks on a 5ms interval (not the production 30s) so the
// assertion completes in milliseconds without coupling to the default.
func TestRun_SweepGoroutineTicksDuringLifetime(t *testing.T) {
	t.Parallel()

	var lc net.ListenConfig
	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	// observableNonceCache wraps a real NewNonceCache + records every
	// Run invocation count so we can prove the sweep goroutine ran at
	// least once.
	cache := newObservableNonceCache(5 * time.Millisecond)
	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.Listener = listener
		d.NonceCache = cache
	})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx) }()

	// Wait for Run to enter so we know the sweep goroutine has been
	// spawned by the chassis.
	select {
	case <-cache.runCalled:
	case <-time.After(2 * time.Second):
		cancel()
		<-runDone
		t.Fatal("chassis did not invoke sweep within 2s")
	}

	// Poll until at least one sweep tick fires. Polling beats time.Sleep
	// for CI: a stalled scheduler that takes >50ms to wake the sweep
	// goroutine would falsely fail a fixed-duration sleep, but the
	// polling loop tolerates arbitrary wake latency up to the deadline.
	deadline := time.Now().Add(2 * time.Second)
	for cache.sweepCount.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}

	got := cache.sweepCount.Load()
	cancel()
	<-runDone

	if got < 1 {
		t.Fatalf("sweep tick count = %d after 2s, want ≥ 1 — chassis-launched sweep did not fire", got)
	}
}

// observableNonceCache wraps a real sign.NonceCache and counts sweep
// invocations via a periodic timer wrapper. Sweep tick count > 0 proves
// the chassis launched the goroutine and the goroutine survived long
// enough to tick at least once.
type observableNonceCache struct {
	cache      sign.NonceCache
	sweepIvl   time.Duration
	runCalled  chan struct{}
	runOnce    sync.Once
	sweepCount atomic.Int64
}

func newObservableNonceCache(sweepIvl time.Duration) *observableNonceCache {
	return &observableNonceCache{
		cache:     sign.NewNonceCache(),
		sweepIvl:  sweepIvl,
		runCalled: make(chan struct{}),
	}
}

func (o *observableNonceCache) Add(ctx context.Context, nonce string, ttl time.Duration) (bool, error) {
	return o.cache.Add(ctx, nonce, ttl)
}

func (o *observableNonceCache) Run(ctx context.Context) {
	o.runOnce.Do(func() { close(o.runCalled) })
	t := time.NewTicker(o.sweepIvl)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			o.sweepCount.Add(1)
		}
	}
}

// TestRun_NilNonceCacheDefaultsToProduction guards against accidental
// deletion of the nil-fallback wiring in New(). A nil deps.NonceCache must
// be replaced with sign.NewNonceCache() so production paths continue to
// receive a working cache even when the operator constructs Deps without
// the new field.
func TestRun_NilNonceCacheDefaultsToProduction(t *testing.T) {
	t.Parallel()

	srv, _, _, _ := newTestServer(t, func(d *Deps) {
		d.NonceCache = nil
	})

	if srv.nonceCache == nil {
		t.Fatal("New(deps with NonceCache=nil) left s.nonceCache nil — production fallback broken")
	}
	// Smoke-test Add to confirm the default cache is functional.
	first, err := srv.nonceCache.Add(t.Context(), strings.Repeat("a", 16), time.Minute)
	if err != nil || !first {
		t.Fatalf("default NonceCache.Add: firstSeen=%v err=%v", first, err)
	}
}
