// Package watchdog implements an alert-only log-pattern watchdog for hush
// supervisor child processes. It tails child stderr/stdout lines, evaluates
// each against operator-configured regex patterns, and emits typed alert
// Events on matches. Per-pattern token buckets (capacity 1) prevent alert
// floods; every suppression is loud-logged via WARN. The watchdog has zero
// authority over the supervisor state machine: matches NEVER trigger
// restarts, session-claims, refreshes, or transitions (spec FR-003,
// Constitution V).
//
// Lifecycle:
//
//	wd, err := watchdog.NewWatchdog(patterns, alertsCh, logger)
//	if err != nil { /* fail boot */ }
//	go wd.Run(ctx)
//	// producers call wd.Ingest(line) or wd.OnStderrLine(ctx, line)
//	<-ctx.Done() // Run drops pending lines, INFO-logs count, returns
package watchdog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
)

const lineChannelCapacity = 512

// Sentinel errors. Operator-input validation failures return a wrapped
// sentinel via fmt.Errorf("...: %w", sentinel) so callers can errors.Is
// each case.
var (
	ErrAlreadyRan           = errors.New("watchdog: Run already invoked")
	ErrEmptyPatternName     = errors.New("watchdog: pattern name is empty")
	ErrDuplicatePatternName = errors.New("watchdog: duplicate pattern name")
	ErrNilPatternRegex      = errors.New("watchdog: pattern Regex is nil")
	ErrNonPositiveRateLimit = errors.New("watchdog: pattern RateLimit must be positive")
	ErrNilAlertsChannel     = errors.New("watchdog: alerts channel is nil")
	ErrNilLogger            = errors.New("watchdog: logger is nil")
)

// Pattern is an operator-named regex predicate paired with a per-pattern
// alert refill interval. Values are immutable after construction; the
// Regex pointer is borrowed (caller pre-compiles and owns it).
type Pattern struct {
	Name      string
	Regex     *regexp.Regexp
	RateLimit time.Duration
}

// Event is the typed alert emitted on each non-suppressed pattern match.
// Consumed by the downstream alert router (SDD-28) as a value type.
type Event struct {
	Pattern string
	Line    string
	Time    time.Time
}

// bucketState holds the per-pattern token-bucket state. Capacity 1; mutated
// only inside the matcher goroutine (Run).
type bucketState struct {
	tokens          int
	lastRefill      time.Time
	suppressedCount uint64
}

// dropEpisode coalesces queue-full Ingest drops into one WARN per
// contiguous run of drops. Mutated under enqueueMu.
type dropEpisode struct {
	count       uint64
	firstDropAt time.Time
}

// Watchdog is the single-instance, single-run pattern engine.
type Watchdog struct {
	patterns []Pattern
	alerts   chan<- Event
	logger   *slog.Logger

	now func() time.Time

	lines chan []byte

	buckets map[string]*bucketState

	enqueueMu sync.Mutex
	drops     dropEpisode

	ran       atomic.Bool
	cancelled atomic.Bool

	suppressedByAlertOutput uint64
}

// Compile-time guard: *Watchdog satisfies the supervise.Watchdog interface
// declared at internal/supervise/lifecycle_interfaces.go:51.
var _ supervise.Watchdog = (*Watchdog)(nil)

// NewWatchdog constructs a Watchdog from a pre-validated pattern slice, a
// caller-owned alerts channel, and a logger. Returns a wrapped sentinel on
// any validation failure. An empty patterns slice is permitted (FR-014).
func NewWatchdog(patterns []Pattern, alerts chan<- Event, logger *slog.Logger) (*Watchdog, error) {
	if alerts == nil {
		return nil, fmt.Errorf("watchdog: %w", ErrNilAlertsChannel)
	}
	if logger == nil {
		return nil, fmt.Errorf("watchdog: %w", ErrNilLogger)
	}
	if err := validatePatterns(patterns); err != nil {
		return nil, err
	}

	patternsCopy := make([]Pattern, len(patterns))
	copy(patternsCopy, patterns)

	now := time.Now
	t0 := now()
	buckets := make(map[string]*bucketState, len(patternsCopy))
	for i := range patternsCopy {
		buckets[patternsCopy[i].Name] = &bucketState{tokens: 1, lastRefill: t0}
	}

	return &Watchdog{
		patterns: patternsCopy,
		alerts:   alerts,
		logger:   logger,
		now:      now,
		lines:    make(chan []byte, lineChannelCapacity),
		buckets:  buckets,
	}, nil
}

func validatePatterns(patterns []Pattern) error {
	seen := make(map[string]struct{}, len(patterns))
	for i := range patterns {
		p := patterns[i]
		switch {
		case p.Name == "":
			return fmt.Errorf("watchdog: pattern[%d]: %w", i, ErrEmptyPatternName)
		case p.Regex == nil:
			return fmt.Errorf("watchdog: pattern %q: %w", p.Name, ErrNilPatternRegex)
		case p.RateLimit <= 0:
			return fmt.Errorf("watchdog: pattern %q: %w", p.Name, ErrNonPositiveRateLimit)
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("watchdog: pattern %q: %w", p.Name, ErrDuplicatePatternName)
		}
		seen[p.Name] = struct{}{}
	}
	return nil
}

// Ingest defensively copies line and enqueues it for evaluation. Non-
// blocking: a full internal queue drops the line and bookkeeps it in the
// current drop episode. Post-Run-return ingests are silent no-ops.
//
// Safe for concurrent invocation from multiple producer goroutines.
func (w *Watchdog) Ingest(line []byte) {
	if w.cancelled.Load() {
		return
	}
	dup := make([]byte, len(line))
	copy(dup, line)

	w.enqueueMu.Lock()
	defer w.enqueueMu.Unlock()
	if w.cancelled.Load() {
		return
	}
	select {
	case w.lines <- dup:
		if w.drops.count > 0 {
			w.logger.LogAttrs(context.Background(), slog.LevelWarn,
				"watchdog: lines dropped (queue full)",
				slog.Uint64("dropped_count", w.drops.count),
				slog.Time("first_drop_at", w.drops.firstDropAt))
			w.drops = dropEpisode{}
		}
	default:
		if w.drops.count == 0 {
			w.drops.firstDropAt = w.now()
		}
		w.drops.count++
	}
}

// OnStderrLine satisfies the supervise.Watchdog interface by delegating to
// Ingest. The ctx is intentionally discarded; the watchdog already holds
// Run's ctx and Ingest is non-blocking.
//
//nolint:contextcheck // Ingest signature is locked by the SDD-27 chunk doc.
func (w *Watchdog) OnStderrLine(_ context.Context, line []byte) {
	w.Ingest(line)
}

// Run drives the matcher loop. Single-shot: a second invocation returns
// ErrAlreadyRan without spawning a goroutine. On <-ctx.Done(), pending
// lines are dropped (not evaluated), one INFO log records the drop count,
// the cancelled atomic is set, and Run returns the wrapped ctx.Err().
//
// Run is itself the matcher goroutine — there is no additional goroutine
// spawned. Callers invoke Run in their own goroutine (`go wd.Run(ctx)`),
// so the watchdog adds exactly one live goroutine between Run start and
// Run return (FR-009, invariant W-1).
func (w *Watchdog) Run(ctx context.Context) error {
	if !w.ran.CompareAndSwap(false, true) {
		return ErrAlreadyRan
	}
	defer func() {
		if r := recover(); r != nil {
			w.logger.LogAttrs(ctx, slog.LevelError,
				"watchdog: matcher panic recovered",
				slog.Any("panic", r))
		}
	}()

	for {
		select {
		case <-ctx.Done():
			w.shutdown(ctx)
			return fmt.Errorf("watchdog: run cancelled: %w", ctx.Err())
		case line := <-w.lines:
			w.evaluate(ctx, line)
		}
	}
}

// shutdown is invoked once Run observes ctx.Done(). It blocks Ingest via
// enqueueMu, flushes any open queue-full drop episode, drains remaining
// lines (counting drops), and emits the cancel-time INFO entry.
func (w *Watchdog) shutdown(ctx context.Context) {
	w.enqueueMu.Lock()
	w.cancelled.Store(true)
	if w.drops.count > 0 {
		w.logger.LogAttrs(ctx, slog.LevelWarn,
			"watchdog: lines dropped (queue full)",
			slog.Uint64("dropped_count", w.drops.count),
			slog.Time("first_drop_at", w.drops.firstDropAt))
		w.drops = dropEpisode{}
	}
	var dropped uint64
	for {
		select {
		case <-w.lines:
			dropped++
		default:
			w.enqueueMu.Unlock()
			w.logger.LogAttrs(ctx, slog.LevelInfo,
				"watchdog: run cancelled; pending lines dropped",
				slog.Uint64("dropped_pending_count", dropped))
			return
		}
	}
}

// evaluate runs one line through every pattern. Called only from Run's
// matcher loop, so buckets and suppressedByAlertOutput are accessed by a
// single goroutine.
func (w *Watchdog) evaluate(ctx context.Context, line []byte) {
	if len(line) == 0 {
		return
	}
	now := w.now()
	for i := range w.patterns {
		pat := &w.patterns[i]
		if !pat.Regex.Match(line) {
			continue
		}
		bucket := w.buckets[pat.Name]
		if now.Sub(bucket.lastRefill) >= pat.RateLimit {
			bucket.tokens = 1
			bucket.lastRefill = now
		}
		if bucket.tokens <= 0 {
			bucket.suppressedCount++
			w.logger.LogAttrs(ctx, slog.LevelWarn,
				"watchdog: alert suppressed by rate limit",
				slog.String("pattern", pat.Name),
				slog.Uint64("suppressed_count", bucket.suppressedCount),
				slog.Time("time", now))
			continue
		}
		ev := Event{
			Pattern: pat.Name,
			Line:    string(line),
			Time:    now,
		}
		select {
		case w.alerts <- ev:
		default:
			w.suppressedByAlertOutput++
			w.logger.LogAttrs(ctx, slog.LevelWarn,
				"watchdog: alert dropped (output saturated)",
				slog.String("pattern", pat.Name),
				slog.Uint64("alert_output_drops", w.suppressedByAlertOutput),
				slog.Time("time", now))
		}
		bucket.tokens = 0
		bucket.lastRefill = now
	}
}
