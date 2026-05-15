//go:build never_built

// Package contracts captures the locked Go signatures that SDD-27
// commits internal/supervise/watchdog to. This file is for review
// only — the canonical implementation lives at
// internal/supervise/watchdog/watchdog.go after Phase 5
// (/speckit-implement). Reviewers should diff this against the
// implemented surface and flag any drift.
//
// Build-tagged with `never_built` so lint/build skip it; this
// directory has no go.mod and is excluded from the repo's Go module.
// The file exists solely as a typed mirror of the API contract.
package contracts

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

// ----- Sentinel errors (locked, exported) -----

// ErrAlreadyRan is returned by (*Watchdog).Run when invoked a second
// time on the same instance. Single-shot lifecycle (research.md R-012).
var ErrAlreadyRan = errors.New("watchdog: Run already invoked")

// ErrEmptyPatternName is returned by NewWatchdog when any supplied
// Pattern.Name is the empty string (P-1).
var ErrEmptyPatternName = errors.New("watchdog: pattern name is empty")

// ErrDuplicatePatternName is returned by NewWatchdog when the supplied
// pattern slice contains non-pairwise-distinct Pattern.Name values
// (spec FR-007a, Clarification Q5).
var ErrDuplicatePatternName = errors.New("watchdog: duplicate pattern name")

// ErrNilPatternRegex is returned by NewWatchdog when any supplied
// Pattern.Regex is nil (P-3).
var ErrNilPatternRegex = errors.New("watchdog: pattern Regex is nil")

// ErrNonPositiveRateLimit is returned by NewWatchdog when any supplied
// Pattern.RateLimit is <= 0 (P-4).
var ErrNonPositiveRateLimit = errors.New("watchdog: pattern RateLimit must be positive")

// ErrNilAlertsChannel is returned by NewWatchdog when the alerts
// channel argument is nil.
var ErrNilAlertsChannel = errors.New("watchdog: alerts channel is nil")

// ErrNilLogger is returned by NewWatchdog when the logger argument is nil.
var ErrNilLogger = errors.New("watchdog: logger is nil")

// ----- Pattern -----

// Pattern is an operator-named regex predicate paired with a per-pattern
// alert refill interval. Constructed by the SDD-23 CLI wiring from
// config.Supervisor.Watchdog.Patterns + .MaxAlertsPerHour; passed
// verbatim to NewWatchdog and never mutated thereafter (spec FR-007).
//
// Name MUST be non-empty and pairwise distinct within a single
// watchdog instance (spec FR-007a). Regex MUST be pre-compiled
// (spec FR-008, research.md R-015). RateLimit MUST be positive and
// equals one token-bucket refill interval (capacity 1) per spec
// Clarification Q3.
type Pattern struct {
	Name      string
	Regex     *regexp.Regexp
	RateLimit time.Duration
}

// ----- Event -----

// Event is the typed alert emitted on every non-suppressed pattern
// match (spec FR-002, FR-013). Consumed by the downstream alert
// router (SDD-28); represented as a value type.
//
// Pattern identifies the matched Pattern.Name verbatim. Line is a
// defensive copy of the matched line content (string-owned by Event).
// Time is the wall-clock of the match per the watchdog's clock seam.
type Event struct {
	Pattern string
	Line    string
	Time    time.Time
}

// ----- Watchdog -----

// Watchdog is the single-instance, single-run pattern engine
// (data-model.md §3). Lifecycle:
//
//	wd, err := NewWatchdog(patterns, alertsCh, logger)
//	if err != nil { /* fail boot */ }
//	go wd.Run(ctx)
//	// ... producers call wd.Ingest(line) ...
//	<-ctx.Done()
//	// Run returns wrapped ctx.Err(); wd is post-Run.
//
// After Run returns: Ingest is a no-op (cancelled atomic short-
// circuit); no goroutines remain alive that were owned by wd
// (spec FR-009); the alerts channel passed at construction is
// left open (research.md R-011); second Run returns ErrAlreadyRan
// (research.md R-012).
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

// bucketState — internal per-pattern token-bucket state. Capacity 1;
// mutated only inside the matcher goroutine (research.md R-006).
type bucketState struct {
	tokens          int
	lastRefill      time.Time
	suppressedCount uint64
}

// dropEpisode — internal queue-full episode bookkeeping. Mutated
// under enqueueMu (research.md R-008).
type dropEpisode struct {
	count       uint64
	firstDropAt time.Time
}

// NewWatchdog constructs a Watchdog. Returns nil + a wrapped sentinel
// on any validation failure (see Err* documented above). Empty
// patterns slice is permitted and produces a benign-no-op watchdog
// (spec FR-014).
//
// Locked signature EXTENDED from the chunk doc to return an error
// per spec FR-007a (Clarification Q5). Recorded in plan.md
// Complexity Tracking entry #2.
func NewWatchdog(patterns []Pattern, alerts chan<- Event, logger *slog.Logger) (*Watchdog, error) {
	_ = patterns
	_ = alerts
	_ = logger
	return nil, nil
}

// Ingest is non-blocking. The supplied line is defensively copied
// and enqueued on the internal channel. If the channel is full, the
// line is dropped and the drop is bookkept in the current drop
// episode (research.md R-008). If Run has already returned, Ingest
// is a silent no-op (research.md R-009).
//
// Ingest is safe for concurrent invocation from multiple producers
// (spec FR-010).
func (w *Watchdog) Ingest(line []byte) {
	_ = line
}

// Run drives the matcher loop. Single-shot: returns ErrAlreadyRan on
// the second invocation (research.md R-012). On <-ctx.Done(), any
// lines still buffered in the internal channel are DROPPED (not
// evaluated), one INFO-level structured log entry is emitted naming
// the watchdog and the drop count, the cancelled atomic is set, and
// Run returns the wrapped ctx.Err() (research.md R-007).
//
// Run never panics on normal-path errors. The matcher loop never
// blocks on the alerts channel — saturated sends drop with a WARN
// (research.md R-010).
//
// Spawns no goroutines beyond the matcher loop itself. After Run
// returns, the goroutine count returns to the pre-Run baseline
// within 250 ms (spec SC-004).
func (w *Watchdog) Run(ctx context.Context) error {
	_ = ctx
	return nil
}

// OnStderrLine satisfies the supervise.Watchdog interface declared
// at internal/supervise/lifecycle_interfaces.go:51 (locked at SDD-24).
// Discards ctx and delegates to (*Watchdog).Ingest(line). Allows
// callers (the SDD-23 CLI wiring) to pass *Watchdog directly into
// Deps.Watchdog without an inline adapter (research.md R-003).
//
// ADDITIVE BEYOND the chunk-doc API. Recorded in plan.md Complexity
// Tracking entry #3.
func (w *Watchdog) OnStderrLine(ctx context.Context, line []byte) {
	_ = ctx
	_ = line
}
