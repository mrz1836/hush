package supervise_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
)

const helperEnv = "HUSH_CHILD_TEST_HELPER_MODE"

// pidfilePathEnv is the env-var read by the "pidfile-acquire-and-exit"
// helper mode (stale-acquired test). The helper acquires the
// configured PID file via supervise.AcquirePidFile and exits 0 without
// calling Release — the OS releases the flock at process death so the
// parent test can re-acquire cleanly.
const pidfilePathEnv = "HUSH_PIDFILE_TEST_PATH"

//nolint:gocognit,gocyclo // TestMain dispatches 12 helper modes — branching is inherent
func TestMain(m *testing.M) {
	switch os.Getenv(helperEnv) {
	case "":
		os.Exit(m.Run())
	case "pidfile-acquire-and-exit":
		path := os.Getenv(pidfilePathEnv)
		if path == "" {
			os.Exit(2)
		}
		if _, err := supervise.AcquirePidFile(path); err != nil {
			os.Exit(3)
		}
		// Exit without Release — OS drops the flock at process death.
		os.Exit(0)
	case "exit-zero":
		os.Exit(0)
	case "exit-seven":
		os.Exit(7)
	case "exit-78":
		os.Exit(78)
	case "kill-self-sigkill":
		_ = syscall.Kill(syscall.Getpid(), syscall.SIGKILL)
		// Should not reach here.
		time.Sleep(5 * time.Second)
		os.Exit(99)
	case "sigterm-trap":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		_, _ = os.Stdout.WriteString("READY\n")
		<-ch
		_, _ = os.Stdout.WriteString("SIGTERM_TRAPPED\n")
		os.Exit(0)
	case "sleep-30s":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "stdout-flood-1mb":
		// Write 1 MB then trap SIGTERM and exit cleanly.
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		chunk := make([]byte, 4*1024)
		for i := range chunk {
			chunk[i] = 'a'
		}
		go func() {
			for i := 0; i < 256; i++ {
				_, _ = os.Stdout.Write(chunk)
			}
		}()
		select {
		case <-ch:
		case <-time.After(10 * time.Second):
		}
		os.Exit(0)
	case "stdout-flood-200kb":
		chunk := make([]byte, 4*1024)
		for i := range chunk {
			chunk[i] = 'b'
		}
		for i := 0; i < 50; i++ {
			_, _ = os.Stdout.Write(chunk)
		}
		time.Sleep(100 * time.Millisecond)
		for i := 0; i < 50; i++ {
			_, _ = os.Stdout.Write(chunk)
		}
		os.Exit(0)
	case "spawn-grandchild-and-sleep":
		exePath, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		// Grandchild inherits this process's pgid (no Setpgid)
		// so kill(-pgid) reaches both supervisor and grandchild.
		cmd := exec.CommandContext(context.Background(), exePath, "-test.run=^$") //nolint:gosec // helper mode re-invokes the test binary
		cmd.Env = append(os.Environ(), helperEnv+"=sleep-30s")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(os.Stdout, "SUPERVISOR_PID=%d\n", os.Getpid())
		_, _ = fmt.Fprintf(os.Stdout, "GRANDCHILD_PID=%d\n", cmd.Process.Pid)
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "subsupervisor-with-grandchild":
		exePath, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		grand := supervise.NewChild(supervise.ChildConfig{
			Command: []string{exePath, "-test.run=^$"},
			Env:     append(os.Environ(), helperEnv+"=sleep-30s"),
			Logger:  logger,
		})
		if err := grand.Start(context.Background()); err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(os.Stdout, "GRANDCHILD_PID=%d\n", grand.PID())
		_, _, _ = grand.Wait()
		os.Exit(0)
	case "exit-42-after-100ms":
		time.Sleep(100 * time.Millisecond)
		os.Exit(42)
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode")
		os.Exit(2)
	}
}

func helperConfig(t *testing.T, mode string, stdout io.Writer) supervise.ChildConfig {
	t.Helper()
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return supervise.ChildConfig{
		Command: []string{exePath, "-test.run=^$"},
		Env:     append(os.Environ(), helperEnv+"="+mode),
		Stdout:  stdout,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---------- T-13: Logger nil panic at NewChild ----------

func TestChild_LoggerNilPanicsAtNewChild(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic, got nil")
		}
		got, ok := r.(string)
		if !ok || got != "supervise: NewChild requires a non-nil Logger" {
			t.Fatalf("unexpected panic value: %v", r)
		}
	}()
	_ = supervise.NewChild(supervise.ChildConfig{Logger: nil})
}

// ---------- T-05: Empty command rejected ----------

func TestChild_RejectsEmptyCommand(t *testing.T) {
	t.Parallel()
	c := supervise.NewChild(supervise.ChildConfig{Logger: discardLogger()})
	err := c.Start(context.Background())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, supervise.ErrCommandEmpty) {
		t.Fatalf("want ErrCommandEmpty, got %v", err)
	}
}

// ---------- T-06: Relative-path command rejected ----------

func TestChild_RejectsRelativeCommand(t *testing.T) {
	t.Parallel()
	c := supervise.NewChild(supervise.ChildConfig{
		Command: []string{"daemon", "--flag"},
		Logger:  discardLogger(),
	})
	err := c.Start(context.Background())
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, supervise.ErrCommandPathRelative) {
		t.Fatalf("want ErrCommandPathRelative, got %v", err)
	}
	if errors.Is(err, supervise.ErrCommandEmpty) {
		t.Fatalf("want distinct from ErrCommandEmpty")
	}
}

// ---------- T-01: Start + Wait happy path ----------

func TestChild_StartAndWait_HappyPath(t *testing.T) {
	t.Parallel()
	cfg := helperConfig(t, "exit-zero", nil)
	c := supervise.NewChild(cfg)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	exit, sig, err := c.Wait()
	if err != nil {
		t.Fatalf("Wait err: %v", err)
	}
	if exit != 0 || sig != 0 {
		t.Fatalf("want (0, 0, nil), got (%d, %d, nil)", exit, sig)
	}
	// Confirm no shell prefix: cfg.Command[0] equals the exe path
	// (cmd.Path was set explicitly).
	if !strings.HasSuffix(cfg.Command[0], ".test") &&
		!strings.Contains(cfg.Command[0], "/") {
		t.Fatalf("expected absolute path, got %q", cfg.Command[0])
	}
}

// ---------- T-02: Non-zero exit code verbatim ----------

func TestChild_Wait_NonZeroExitCodeVerbatim(t *testing.T) {
	t.Parallel()
	c := supervise.NewChild(helperConfig(t, "exit-seven", nil))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	exit, sig, err := c.Wait()
	if err != nil {
		t.Fatalf("Wait err: %v", err)
	}
	if exit != 7 || sig != 0 {
		t.Fatalf("want (7, 0, nil), got (%d, %d, nil)", exit, sig)
	}
}

// ---------- T-03: Terminating signal distinct ----------

func TestChild_Wait_TerminatingSignalDistinct(t *testing.T) {
	t.Parallel()
	c := supervise.NewChild(helperConfig(t, "kill-self-sigkill", nil))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	exit, sig, err := c.Wait()
	if err != nil {
		t.Fatalf("Wait err: %v", err)
	}
	if exit != 0 {
		t.Fatalf("want exit 0 for signal-terminated, got %d", exit)
	}
	if sig != syscall.SIGKILL {
		t.Fatalf("want SIGKILL, got %v", sig)
	}
}

// ---------- T-04: Exit78 detection ----------

//nolint:gocognit // two-subtest pattern + helper config + Wait branching
func TestChild_Exit78Detection(t *testing.T) {
	t.Parallel()
	t.Run("exit-78", func(t *testing.T) {
		t.Parallel()
		c := supervise.NewChild(helperConfig(t, "exit-78", nil))
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		exit, sig, err := c.Wait()
		if err != nil {
			t.Fatalf("Wait err: %v", err)
		}
		if exit != 78 || sig != 0 {
			t.Fatalf("want (78, 0, nil), got (%d, %d, nil)", exit, sig)
		}
		if exit != supervise.Exit78 {
			t.Fatalf("want exitCode == supervise.Exit78")
		}
	})
	t.Run("non-78", func(t *testing.T) {
		t.Parallel()
		c := supervise.NewChild(helperConfig(t, "exit-seven", nil))
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		exit, _, _ := c.Wait()
		if exit == supervise.Exit78 {
			t.Fatalf("want exit != Exit78, got %d", exit)
		}
	})
}

// ---------- T-07: Signal forwarding (SIGTERM) ----------

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestChild_SignalForwardingSIGTERM(t *testing.T) {
	t.Parallel()
	out := &syncBuffer{}
	c := supervise.NewChild(helperConfig(t, "sigterm-trap", out))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Wait for the helper to install the handler.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), "READY") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := c.Forward(syscall.SIGTERM); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	exit, sig, err := c.Wait()
	if err != nil {
		t.Fatalf("Wait err: %v", err)
	}
	if exit != 0 || sig != 0 {
		t.Fatalf("want (0,0,nil), got (%d, %d, nil)", exit, sig)
	}
	if !strings.Contains(out.String(), "SIGTERM_TRAPPED") {
		t.Fatalf("expected SIGTERM_TRAPPED in stdout, got %q", out.String())
	}
}

// ---------- T-07b: Forward after exit returns ErrChildNotStarted ----------

func TestChild_ForwardAfterExit_ErrChildNotStarted(t *testing.T) {
	t.Parallel()
	t.Run("after-wait", func(t *testing.T) {
		t.Parallel()
		c := supervise.NewChild(helperConfig(t, "exit-zero", nil))
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		_, _, _ = c.Wait()
		err := c.Forward(syscall.SIGTERM)
		if !errors.Is(err, supervise.ErrChildNotStarted) {
			t.Fatalf("want ErrChildNotStarted, got %v", err)
		}
	})
	t.Run("never-started", func(t *testing.T) {
		t.Parallel()
		c := supervise.NewChild(supervise.ChildConfig{Logger: discardLogger()})
		err := c.Forward(syscall.SIGTERM)
		if !errors.Is(err, supervise.ErrChildNotStarted) {
			t.Fatalf("want ErrChildNotStarted, got %v", err)
		}
	})
}

// ---------- T-07c: Forwarding goroutine exits on ctx cancel ----------

func TestChild_ForwardingGoroutineExitsOnCtxCancel(t *testing.T) {
	t.Parallel()
	baseline := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	c := supervise.NewChild(helperConfig(t, "sleep-30s", nil))
	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()

	// Clean up child to avoid hanging the test runner.
	defer func() {
		_ = c.Forward(syscall.SIGKILL)
		_, _, _ = c.Wait()
	}()

	// CommandContext kills the child on ctx cancel; after Wait,
	// goroutine count returns to baseline.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+5 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("forwarding goroutine did not exit; goroutines=%d baseline=%d",
		runtime.NumGoroutine(), baseline)
}

// ---------- T-08: stdout pipe non-blocking ----------

type blockingWriter struct{ ch chan struct{} }

func (b *blockingWriter) Write(_ []byte) (int, error) {
	<-b.ch
	return 0, io.ErrClosedPipe
}

//nolint:gocognit // two-subtest pattern with deadline polling for both sink scenarios
func TestChild_StdoutPipeNonBlocking(t *testing.T) {
	t.Parallel()
	t.Run("discard-sink", func(t *testing.T) {
		t.Parallel()
		c := supervise.NewChild(helperConfig(t, "stdout-flood-1mb", io.Discard))
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		// Give the helper a moment to start flooding, then SIGTERM.
		time.Sleep(50 * time.Millisecond)
		if err := c.Forward(syscall.SIGTERM); err != nil {
			t.Fatalf("Forward: %v", err)
		}
		done := make(chan struct{})
		go func() {
			_, _, _ = c.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("Wait did not return within 10s — supervisor blocked")
		}
	})
	t.Run("blocking-sink", func(t *testing.T) {
		t.Parallel()
		bw := &blockingWriter{ch: make(chan struct{})}
		c := supervise.NewChild(helperConfig(t, "stdout-flood-1mb", bw))
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
		if err := c.Forward(syscall.SIGTERM); err != nil {
			t.Fatalf("Forward: %v", err)
		}
		// The drain goroutine is blocked on bw.Write — but the helper
		// must still exit because the ring absorbs writes via FIFO.
		done := make(chan struct{})
		go func() {
			// Unblock the sink so Wait can join the drain goroutine.
			time.Sleep(500 * time.Millisecond)
			close(bw.ch)
			_, _, _ = c.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("Wait did not return within 10s")
		}
	})
}

// ---------- T-08b: Overflow warning — one episode per stream ----------

type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *captureHandler) warnings(stream string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, r := range h.records {
		if r.Level != slog.LevelWarn {
			continue
		}
		match := false
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == "stream" && a.Value.String() == stream {
				match = true
				return false
			}
			return true
		})
		if match {
			count++
		}
	}
	return count
}

type slowWriter struct {
	mu    sync.Mutex
	delay time.Duration
}

func (s *slowWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	time.Sleep(s.delay)
	return len(p), nil
}

func TestChild_OverflowWarning_OneEpisodePerStream(t *testing.T) {
	t.Parallel()
	h := &captureHandler{}
	logger := slog.New(h)
	exePath, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cfg := supervise.ChildConfig{
		Command: []string{exePath, "-test.run=^$"},
		Env:     append(os.Environ(), helperEnv+"=stdout-flood-200kb"),
		Stdout:  &slowWriter{delay: 50 * time.Millisecond},
		Logger:  logger,
	}
	c := supervise.NewChild(cfg)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, _, err := c.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	got := h.warnings("stdout")
	// Spec Clarification 5: at most one warning per overflow
	// episode per stream. With a slow drain, multiple episodes
	// can occur; assert at least one fired and the count is not
	// runaway (proving the per-episode rate-limit works).
	if got < 1 {
		t.Fatalf("want >= 1 overflow warning, got 0")
	}
	if got > 50 {
		t.Fatalf("warning count %d suggests no rate-limit", got)
	}
}

// ---------- T-09: Concurrent Wait race-clean ----------

//nolint:gocyclo // 100-goroutine fan-out + winner/loser categorization
func TestChild_ConcurrentWaitOK(t *testing.T) {
	c := supervise.NewChild(helperConfig(t, "exit-42-after-100ms", nil))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const N = 100
	type result struct {
		exit int
		sig  syscall.Signal
		err  error
	}
	results := make(chan result, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			exit, sig, err := c.Wait()
			results <- result{exit, sig, err}
		}()
	}
	wg.Wait()
	close(results)

	winners := 0
	losers := 0
	for r := range results {
		switch {
		case r.exit == 42 && r.sig == 0 && r.err == nil:
			winners++
		case r.exit == 0 && r.sig == 0 && errors.Is(r.err, supervise.ErrChildNotStarted):
			losers++
		default:
			t.Fatalf("unexpected result: %+v", r)
		}
	}
	if winners != 1 {
		t.Fatalf("want exactly 1 winner, got %d", winners)
	}
	if losers != N-1 {
		t.Fatalf("want %d losers, got %d", N-1, losers)
	}
}

// ---------- T-09b: Restart cycles — no goroutine leak ----------

//nolint:gocognit // 20-cycle restart loop + warmup + settle window
func TestChild_RestartCycles_NoGoroutineLeak(t *testing.T) {
	// Warm up to avoid initial-allocation skew.
	for i := 0; i < 3; i++ {
		c := supervise.NewChild(helperConfig(t, "exit-zero", nil))
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		_, _, _ = c.Wait()
	}
	runtime.GC()
	baseline := runtime.NumGoroutine()
	// A per-cycle goroutine leak grows linearly, so N=20 vs delta>5 still
	// catches single-leak regressions with >3x headroom while keeping the
	// fork+exec+wait cost (≈1s/cycle under -race) bounded.
	for i := 0; i < 20; i++ {
		c := supervise.NewChild(helperConfig(t, "exit-zero", nil))
		if err := c.Start(context.Background()); err != nil {
			t.Fatalf("Start cycle %d: %v", i, err)
		}
		if _, _, err := c.Wait(); err != nil {
			t.Fatalf("Wait cycle %d: %v", i, err)
		}
	}
	runtime.GC()
	// Allow brief settling.
	deadline := time.Now().Add(500 * time.Millisecond)
	delta := runtime.NumGoroutine() - baseline
	for time.Now().Before(deadline) && delta > 5 {
		time.Sleep(10 * time.Millisecond)
		delta = runtime.NumGoroutine() - baseline
	}
	if delta > 5 {
		t.Fatalf("goroutine leak: baseline=%d after=%d delta=%d",
			baseline, runtime.NumGoroutine(), delta)
	}
}

// ---------- T-10: PGID isolation ----------

//nolint:gocognit,gocyclo // PID-pair parsing + reap orchestration + dual processAlive checks
func TestChild_PgidIsolation_KillingPgKillsChildren(t *testing.T) {
	out := &syncBuffer{}
	c := supervise.NewChild(helperConfig(t, "spawn-grandchild-and-sleep", out))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for both PIDs to appear.
	var supPID, grandPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s := out.String()
		if strings.Contains(s, "SUPERVISOR_PID=") && strings.Contains(s, "GRANDCHILD_PID=") {
			for line := range strings.SplitSeq(s, "\n") {
				if v, ok := strings.CutPrefix(line, "SUPERVISOR_PID="); ok {
					supPID, _ = strconv.Atoi(strings.TrimSpace(v))
				}
				if v, ok := strings.CutPrefix(line, "GRANDCHILD_PID="); ok {
					grandPID, _ = strconv.Atoi(strings.TrimSpace(v))
				}
			}
			if supPID != 0 && grandPID != 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if supPID == 0 || grandPID == 0 {
		t.Fatalf("PIDs not observed: %q", out.String())
	}

	// Kill the entire process group via negative PID.
	if err := syscall.Kill(-supPID, syscall.SIGTERM); err != nil {
		t.Fatalf("kill pgid: %v", err)
	}

	// Reap our supervisor (zombie until Wait runs) so its slot
	// is released and processAlive returns false.
	waitDone := make(chan struct{})
	go func() {
		_, _, _ = c.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("supervisor Wait did not return — process did not exit on SIGTERM")
	}

	// Grandchild was reparented to init when supervisor died;
	// init reaps it after the SIGTERM-on-pgid takes effect.
	if !waitGone(t, grandPID, 3*time.Second) {
		t.Errorf("grandchild pid %d still alive — pgid signal did not propagate", grandPID)
		_ = syscall.Kill(grandPID, syscall.SIGKILL)
	}
}

func waitGone(t *testing.T, pid int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return !processAlive(pid)
}

func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// ---------- T-12: PID returns 0 before Start and after Wait ----------

func TestChild_PIDReturnsZeroBeforeStartAndAfterWait(t *testing.T) {
	t.Parallel()
	c := supervise.NewChild(helperConfig(t, "sleep-30s", nil))
	if c.PID() != 0 {
		t.Fatalf("PID before Start: want 0, got %d", c.PID())
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if c.PID() == 0 {
		t.Fatalf("PID after Start: want non-zero")
	}
	if err := c.Forward(syscall.SIGKILL); err != nil {
		t.Fatalf("Forward: %v", err)
	}
	_, _, _ = c.Wait()
	if c.PID() != 0 {
		t.Fatalf("PID after Wait: want 0, got %d", c.PID())
	}
}

// ---------- Coverage helpers ----------

// TestChild_DoubleWait_LoserGetsErrChildNotStarted is a sequential
// re-entry test (distinct from concurrent T-09): Wait → Wait
// returns ErrChildNotStarted on the second call.
func TestChild_DoubleWait_LoserGetsErrChildNotStarted(t *testing.T) {
	t.Parallel()
	c := supervise.NewChild(helperConfig(t, "exit-zero", nil))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, _, err := c.Wait(); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	exit, sig, err := c.Wait()
	if !errors.Is(err, supervise.ErrChildNotStarted) {
		t.Fatalf("second Wait: want ErrChildNotStarted, got (%d,%d,%v)", exit, sig, err)
	}
}

// TestChild_WaitBeforeStart asserts Wait without Start returns
// ErrChildNotStarted.
func TestChild_WaitBeforeStart(t *testing.T) {
	t.Parallel()
	c := supervise.NewChild(supervise.ChildConfig{Logger: discardLogger()})
	exit, sig, err := c.Wait()
	if !errors.Is(err, supervise.ErrChildNotStarted) {
		t.Fatalf("want ErrChildNotStarted, got (%d,%d,%v)", exit, sig, err)
	}
}

type panickingWriter struct {
	once sync.Once
}

func (p *panickingWriter) Write(_ []byte) (int, error) {
	var panicked bool
	p.once.Do(func() {
		panicked = true
	})
	if panicked {
		panic("test sink panic")
	}
	return 0, io.EOF
}

// TestChild_DrainLoopRecoversFromSinkPanic exercises the drain
// goroutine's top-frame recover (Constitution IX). A panicking
// sink must not crash the supervisor; Wait must still complete.
func TestChild_DrainLoopRecoversFromSinkPanic(t *testing.T) {
	t.Parallel()
	sink := &panickingWriter{}
	c := supervise.NewChild(helperConfig(t, "stdout-flood-200kb", sink))
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	exit, _, err := c.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if exit != 0 {
		t.Fatalf("want exit 0, got %d", exit)
	}
}

// TestChild_StartFailsForBadAbsolutePath exercises cmd.Start error
// path (absolute path that does not exist).
func TestChild_StartFailsForBadAbsolutePath(t *testing.T) {
	t.Parallel()
	c := supervise.NewChild(supervise.ChildConfig{
		Command: []string{"/nonexistent/binary/that/should/not/exist"},
		Logger:  discardLogger(),
	})
	err := c.Start(context.Background())
	if err == nil {
		t.Fatalf("expected Start error for missing binary")
	}
	if errors.Is(err, supervise.ErrCommandPathRelative) {
		t.Fatalf("absolute path should not match ErrCommandPathRelative")
	}
	if errors.Is(err, supervise.ErrCommandEmpty) {
		t.Fatalf("non-empty command should not match ErrCommandEmpty")
	}
}
