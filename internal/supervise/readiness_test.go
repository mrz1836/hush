package supervise

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise/config"
)

// readinessServer wraps an httptest.Server with a switchable handler
// so a single test can flip from 5xx to 2xx mid-probe without rebuilding
// the listener.
type readinessServer struct {
	srv       *httptest.Server
	status    atomic.Int32
	requests  atomic.Int32
	bodyMaker func() string
}

func newReadinessServer(t *testing.T) *readinessServer {
	t.Helper()
	rs := &readinessServer{}
	rs.status.Store(http.StatusServiceUnavailable)
	rs.bodyMaker = func() string { return "" }
	rs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rs.requests.Add(1)
		w.WriteHeader(int(rs.status.Load()))
		if body := rs.bodyMaker(); body != "" {
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(rs.srv.Close)
	return rs
}

func (rs *readinessServer) URL() string { return rs.srv.URL + "/health" }

func (rs *readinessServer) client() *http.Client { return rs.srv.Client() }

func (rs *readinessServer) setStatus(code int) { rs.status.Store(int32(code)) }

func readinessCfg(url string, timeout, interval time.Duration) config.ChildReadiness {
	return config.ChildReadiness{HTTPURL: url, Timeout: timeout, Interval: interval}
}

// TestProbeHTTPReady_Success: a server that already returns 200 yields
// a successful probe on the first attempt with a non-negative elapsed
// duration well under the configured Timeout.
func TestProbeHTTPReady_Success(t *testing.T) {
	t.Parallel()
	rs := newReadinessServer(t)
	rs.setStatus(http.StatusOK)

	cfg := readinessCfg(rs.URL(), 2*time.Second, 20*time.Millisecond)
	elapsed, err := ProbeHTTPReady(context.Background(), rs.client(), cfg)
	if err != nil {
		t.Fatalf("ProbeHTTPReady: unexpected error: %v", err)
	}
	if elapsed < 0 || elapsed >= cfg.Timeout {
		t.Fatalf("elapsed=%s out of (0, %s)", elapsed, cfg.Timeout)
	}
	if got := rs.requests.Load(); got < 1 {
		t.Fatalf("requests=%d, want >= 1", got)
	}
}

// TestProbeHTTPReady_AcceptsAll2xx: 201 and 204 are also success
// according to the 2xx contract.
func TestProbeHTTPReady_AcceptsAll2xx(t *testing.T) {
	t.Parallel()
	for _, code := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent, 299} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			t.Parallel()
			rs := newReadinessServer(t)
			rs.setStatus(code)

			cfg := readinessCfg(rs.URL(), time.Second, 20*time.Millisecond)
			if _, err := ProbeHTTPReady(context.Background(), rs.client(), cfg); err != nil {
				t.Fatalf("status %d: unexpected error: %v", code, err)
			}
		})
	}
}

// TestProbeHTTPReady_EventuallyReady: the server returns 503 for a
// few attempts, then 200. The probe should keep polling and succeed
// without consuming the full Timeout budget.
func TestProbeHTTPReady_EventuallyReady(t *testing.T) {
	t.Parallel()
	rs := newReadinessServer(t)
	rs.setStatus(http.StatusServiceUnavailable)

	go func() {
		for {
			if rs.requests.Load() >= 3 {
				rs.setStatus(http.StatusOK)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	cfg := readinessCfg(rs.URL(), 2*time.Second, 20*time.Millisecond)
	elapsed, err := ProbeHTTPReady(context.Background(), rs.client(), cfg)
	if err != nil {
		t.Fatalf("ProbeHTTPReady: unexpected error: %v", err)
	}
	if elapsed >= cfg.Timeout {
		t.Fatalf("elapsed=%s exceeded budget %s", elapsed, cfg.Timeout)
	}
	if got := rs.requests.Load(); got < 3 {
		t.Fatalf("requests=%d, want >= 3 (server didn't flip until 3rd attempt)", got)
	}
}

// TestProbeHTTPReady_TimeoutExhausted: a server that never returns
// 2xx within Timeout produces an error that wraps ErrReadinessTimeout,
// preserves the latest observed status, and respects the budget within
// a single Interval of slack.
func TestProbeHTTPReady_TimeoutExhausted(t *testing.T) {
	t.Parallel()
	rs := newReadinessServer(t)
	rs.setStatus(http.StatusServiceUnavailable)

	cfg := readinessCfg(rs.URL(), 150*time.Millisecond, 30*time.Millisecond)
	start := time.Now()
	_, err := ProbeHTTPReady(context.Background(), rs.client(), cfg)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("ProbeHTTPReady: expected timeout error, got nil")
	}
	if !errors.Is(err, ErrReadinessTimeout) {
		t.Fatalf("err = %v; want errors.Is(_, ErrReadinessTimeout)", err)
	}
	if elapsed < cfg.Timeout {
		t.Fatalf("elapsed=%s shorter than budget %s; should have used full timeout", elapsed, cfg.Timeout)
	}
	if elapsed > cfg.Timeout+cfg.Interval+200*time.Millisecond {
		t.Fatalf("elapsed=%s overshot budget %s by more than one Interval", elapsed, cfg.Timeout)
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("error %q does not mention last observed status 503", err.Error())
	}
}

// TestProbeHTTPReady_ContextCanceled: canceling the caller context
// while the probe is mid-poll returns wrapped ctx.Err(), and the
// error chain does NOT pivot on ErrReadinessTimeout.
func TestProbeHTTPReady_ContextCanceled(t *testing.T) {
	t.Parallel()
	rs := newReadinessServer(t)
	rs.setStatus(http.StatusServiceUnavailable)

	ctx, cancel := context.WithCancel(context.Background())
	cfg := readinessCfg(rs.URL(), 5*time.Second, 30*time.Millisecond)

	done := make(chan error, 1)
	go func() {
		_, err := ProbeHTTPReady(ctx, rs.client(), cfg)
		done <- err
	}()

	time.Sleep(80 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("ProbeHTTPReady: expected cancellation error, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v; want errors.Is(_, context.Canceled)", err)
		}
		if errors.Is(err, ErrReadinessTimeout) {
			t.Fatalf("err = %v; must NOT wrap ErrReadinessTimeout on caller cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ProbeHTTPReady did not return within 2s of cancel")
	}
}

// TestProbeHTTPReady_NetworkErrorThenReady: pointing at a closed
// listener (immediate connection refused), then unblocking by
// switching the probe URL is awkward. Instead we point at a known-
// closed address for the budget. The probe must surface
// ErrReadinessTimeout with a transport-layer last-error in the
// wrapped chain, not a status code.
func TestProbeHTTPReady_NetworkErrorTimesOut(t *testing.T) {
	t.Parallel()
	// Bind and immediately close a listener to grab a definitely-closed
	// port. Using 127.0.0.1 with port 1 is unreliable across CI; the
	// closed-port pattern guarantees ECONNREFUSED locally.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL + "/health"
	srv.Close()

	cfg := readinessCfg(url, 120*time.Millisecond, 30*time.Millisecond)
	_, err := ProbeHTTPReady(context.Background(), http.DefaultClient, cfg)
	if err == nil {
		t.Fatalf("ProbeHTTPReady: expected timeout error, got nil")
	}
	if !errors.Is(err, ErrReadinessTimeout) {
		t.Fatalf("err = %v; want errors.Is(_, ErrReadinessTimeout)", err)
	}
	if strings.Contains(err.Error(), "last status") {
		t.Fatalf("error %q should not mention last status when only transport errors observed", err.Error())
	}
}

// TestProbeHTTPReady_PollsAtInterval: with Timeout >> Interval and a
// permanently-5xx server, we expect roughly (Timeout/Interval)
// attempts, +/- generous slack for scheduler jitter. This guards
// against a regression where the loop spins (no sleep) or never
// loops at all.
//
// Not parallel: real-time cadence tests get starved by other parallel
// workers in CI (notably the fuzz job), which previously caused
// requests=2 vs the expected ~15.
func TestProbeHTTPReady_PollsAtInterval(t *testing.T) {
	rs := newReadinessServer(t)
	rs.setStatus(http.StatusServiceUnavailable)

	cfg := readinessCfg(rs.URL(), 600*time.Millisecond, 40*time.Millisecond)
	_, err := ProbeHTTPReady(context.Background(), rs.client(), cfg)
	if !errors.Is(err, ErrReadinessTimeout) {
		t.Fatalf("err = %v; want errors.Is(_, ErrReadinessTimeout)", err)
	}
	got := rs.requests.Load()
	// Expected ~15 attempts (600ms / 40ms). Floor of 4 still catches a
	// "never loops" regression while leaving headroom for slow CI
	// workers; ceiling of 25 still catches a "no sleep" spin (which
	// would observe hundreds of attempts in 600ms).
	if got < 4 || got > 25 {
		t.Fatalf("requests=%d outside expected range [4, 25]; loop cadence is off", got)
	}
}

// TestProbeHTTPReady_ErrorsAreLogSafe: ensure the error message
// contains only the URL, status, and elapsed time — no response body,
// no request headers, no child env values. The server returns a body
// that includes a fake secret marker; the marker MUST NOT appear in
// the error chain.
func TestProbeHTTPReady_ErrorsAreLogSafe(t *testing.T) {
	t.Parallel()
	const secretMarker = "HUSH-MARKER-READINESS-SECRET-42"
	rs := newReadinessServer(t)
	rs.setStatus(http.StatusServiceUnavailable)
	rs.bodyMaker = func() string { return "boot diag: " + secretMarker + " trailing" }

	cfg := readinessCfg(rs.URL(), 100*time.Millisecond, 30*time.Millisecond)
	_, err := ProbeHTTPReady(context.Background(), rs.client(), cfg)
	if err == nil {
		t.Fatalf("ProbeHTTPReady: expected timeout error, got nil")
	}
	if strings.Contains(err.Error(), secretMarker) {
		t.Fatalf("error leaked response body marker: %q", err.Error())
	}
	// URL is operator-supplied and non-secret; it SHOULD appear so
	// operators can identify which probe failed.
	if !strings.Contains(err.Error(), rs.URL()) {
		t.Fatalf("error %q missing probe URL", err.Error())
	}
}

// TestProbeHTTPReady_RejectsConfigErrors: nil client and non-positive
// Timeout/Interval are programmer-error class and return an error
// without performing any HTTP I/O.
func TestProbeHTTPReady_RejectsConfigErrors(t *testing.T) {
	t.Parallel()
	rs := newReadinessServer(t)

	t.Run("nil client", func(t *testing.T) {
		t.Parallel()
		cfg := readinessCfg(rs.URL(), time.Second, 100*time.Millisecond)
		if _, err := ProbeHTTPReady(context.Background(), nil, cfg); err == nil {
			t.Fatalf("expected error for nil client, got nil")
		}
	})
	t.Run("zero timeout", func(t *testing.T) {
		t.Parallel()
		cfg := readinessCfg(rs.URL(), 0, 100*time.Millisecond)
		if _, err := ProbeHTTPReady(context.Background(), rs.client(), cfg); err == nil {
			t.Fatalf("expected error for zero Timeout, got nil")
		}
	})
	t.Run("zero interval", func(t *testing.T) {
		t.Parallel()
		cfg := readinessCfg(rs.URL(), time.Second, 0)
		if _, err := ProbeHTTPReady(context.Background(), rs.client(), cfg); err == nil {
			t.Fatalf("expected error for zero Interval, got nil")
		}
	})
	t.Run("negative timeout", func(t *testing.T) {
		t.Parallel()
		cfg := readinessCfg(rs.URL(), -time.Second, 100*time.Millisecond)
		if _, err := ProbeHTTPReady(context.Background(), rs.client(), cfg); err == nil {
			t.Fatalf("expected error for negative Timeout, got nil")
		}
	})
}

// TestProbeHTTPReady_PreCancelledContext: a context that is already
// cancelled when ProbeHTTPReady is called returns ctx.Err() wrapped,
// not ErrReadinessTimeout, even when the configured Timeout is
// otherwise generous.
func TestProbeHTTPReady_PreCancelledContext(t *testing.T) {
	t.Parallel()
	rs := newReadinessServer(t)
	rs.setStatus(http.StatusOK)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := readinessCfg(rs.URL(), 5*time.Second, 30*time.Millisecond)
	_, err := ProbeHTTPReady(ctx, rs.client(), cfg)
	if err == nil {
		t.Fatalf("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want errors.Is(_, context.Canceled)", err)
	}
}
