package supervise

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mrz1836/hush/internal/supervise/config"
)

// errors and lint-suppressions for the parser are declared near the
// parser implementation below to keep them adjacent.

// errRefresherAlreadyRan is returned by Run when called a second time
// on the same *Refresher (RF-7).
var errRefresherAlreadyRan = errors.New("supervise: Refresher.Run already invoked")

// refresherTickInterval is the wall-clock granularity at which the
// Refresher re-evaluates its window predicates. The contract is "at
// most one fire per (window, calendar-day) pair" + "at most one
// pre-deadline fallback per session" — minute-level granularity is well below any
// operator-perceptible delay and avoids busy-spin.
const refresherTickInterval = time.Minute

// Refresher schedules at most one refill callback fire per configured
// local-time window per calendar day, plus at most one pre-deadline fallback
// fire per session. Run blocks until ctx is cancelled. Single-shot —
// Run returns a sentinel error on second call.
type Refresher struct {
	window string
	ttl    time.Duration
	refill func(ctx context.Context) error
	logger *slog.Logger

	startHour, startMin int
	endHour, endMin     int

	now          func() time.Time
	bornAt       time.Time
	lastFiredDay time.Time
	t30Fired     bool

	nudge      time.Duration
	deadlineFn func() time.Time
	publish    func(time.Time)

	// testTickC, when non-nil, replaces the internal time.Timer
	// driving Run's tick loop. Set by package-private tests via
	// setTickerForTest. Production code leaves this nil and Run
	// arms a real time.Timer at refresherTickInterval cadence.
	testTickC <-chan time.Time

	runOnce sync.Once
	ran     bool
}

// NewRefresher constructs a Refresher bound to the supplied window
// string, session TTL, fire callback, and logger. Panics if window
// fails to parse, if refill is nil, or if logger is nil (Constitution
// IX startup-wiring exemption).
//
// The window string MUST be canonical "HH:MM-HH:MM" (validated at
// config-load time); this constructor parses it eagerly into four
// ints and panics on parse failure (programmer error — orchestrator
// pre-validates).
func NewRefresher(window string, ttl time.Duration, refill func(ctx context.Context) error, logger *slog.Logger) *Refresher {
	if refill == nil {
		panic("supervise: NewRefresher requires a non-nil refill callback")
	}
	if logger == nil {
		panic("supervise: NewRefresher requires a non-nil *slog.Logger")
	}
	sh, sm, eh, em, err := parseRefreshWindow(window)
	if err != nil {
		panic("supervise: NewRefresher window parse: " + err.Error())
	}
	return &Refresher{
		window:    window,
		ttl:       ttl,
		refill:    refill,
		logger:    logger,
		startHour: sh,
		startMin:  sm,
		endHour:   eh,
		endMin:    em,
		now:       time.Now,
	}
}

// Run drives the scheduler tick loop. Returns ctx.Err() on
// cancellation; never any other error from a normal run. A second
// call to Run on the same *Refresher returns a sentinel error
// immediately (sync.Once-guarded). Spawns NO goroutines beyond its own
// tick loop body.
//
// On entry, if the wall clock is already inside the configured window
// AND lastFiredDay != today, Run fires once on init.
//
// On a non-nil error from refill, Run logs WARN naming the error
// category and advances lastFiredDay anyway — the fire counts as
// "issued" (rate-limited refresh fires never retry inside the same
// window).
func (r *Refresher) Run(ctx context.Context) error {
	first := false
	r.runOnce.Do(func() {
		r.ran = true
		first = true
	})
	if !first {
		return errRefresherAlreadyRan
	}
	defer func() { _ = recover() }()

	r.bornAt = r.now()

	r.tick(ctx)

	if r.testTickC != nil {
		for {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-r.testTickC:
				r.tick(ctx)
			}
		}
	}

	timer := time.NewTimer(refresherTickInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			r.tick(ctx)
			timer.Reset(refresherTickInterval)
		}
	}
}

// setClockForTest replaces the wall-clock source used by Run. The
// seam is unexported and only available to package-internal tests.
func (r *Refresher) setClockForTest(now func() time.Time) {
	r.now = now
}

// setTickerForTest replaces the internal tick-source channel. The
// seam is unexported; tests use it to drive Run deterministically
// without real wall-clock elapsing.
func (r *Refresher) setTickerForTest(c <-chan time.Time) {
	r.testTickC = c
}

// primeForTest sets internal flags directly (lastFiredDay, t30Fired)
// so tests can simulate "we already fired at 09:30 today" before Run.
//
//nolint:unparam // t30Fired=true caller lives in export_for_integration.go (//go:build integration), invisible to the default lint build
func (r *Refresher) primeForTest(lastFiredDay time.Time, t30Fired bool) {
	r.lastFiredDay = lastFiredDay
	r.t30Fired = t30Fired
}

// errAlreadyRan exports the single-shot sentinel for test
// assertions. It is package-private and not part of the locked API.
func errAlreadyRan() error { return errRefresherAlreadyRan }

// tick runs one wall-clock evaluation step.
func (r *Refresher) tick(ctx context.Context) {
	// The refresh window is operator-configured local-time;
	// time.Local is the anchor.
	now := r.now().In(time.Local) //nolint:gosmopolitan // window is operator-configured local-time
	defer func() {
		if r.publish != nil {
			r.publish(r.nextFire(now))
		}
	}()
	today := dateOnly(now)
	deadline := r.effectiveDeadline(now)
	nudge := r.effectiveNudge()

	if r.t30Fired && deadline.Sub(now) >= nudge {
		r.t30Fired = false
	}

	if r.windowContains(now) {
		if !sameDay(today, r.lastFiredDay) {
			r.fire(ctx)
			r.lastFiredDay = today
		}
		return
	}

	// Consider the fallback before the session deadline.
	if r.t30Fired {
		return
	}
	if deadline.Sub(now) <= nudge && !sameDay(today, r.lastFiredDay) {
		r.fire(ctx)
		r.lastFiredDay = today
		r.t30Fired = true
	}
}

func (r *Refresher) effectiveNudge() time.Duration {
	if r.nudge != 0 {
		return r.nudge
	}
	return config.DefaultRefreshNudgeBefore
}

func (r *Refresher) effectiveDeadline(now time.Time) time.Time {
	if r.deadlineFn != nil {
		if deadline := r.deadlineFn(); !deadline.IsZero() {
			return deadline.In(now.Location())
		}
	}
	return r.bornAt.Add(r.ttl).In(now.Location())
}

func (r *Refresher) nextFire(now time.Time) time.Time {
	now = now.In(time.Local) //nolint:gosmopolitan // window is operator-configured local-time
	windowStart := r.nextWindowStart(now)
	deadlineCandidate := r.effectiveDeadline(now).Add(-r.effectiveNudge())
	candidateDay := dateOnly(deadlineCandidate)

	if !deadlineCandidate.Before(now) && deadlineCandidate.Before(windowStart) && !sameDay(candidateDay, r.lastFiredDay) {
		return deadlineCandidate
	}
	return windowStart
}

func (r *Refresher) nextWindowStart(now time.Time) time.Time {
	today := dateOnly(now)
	windowStart := time.Date(
		now.Year(),
		now.Month(),
		now.Day(),
		r.startHour,
		r.startMin,
		0,
		0,
		now.Location(),
	)
	if windowStart.Before(now) || sameDay(today, r.lastFiredDay) {
		windowStart = windowStart.AddDate(0, 0, 1)
	}
	return windowStart
}

// fire invokes the operator-supplied refill callback. A non-nil
// return value is logged at WARN and treated as "issued" — the caller
// of Run NEVER receives a fire-failure error.
func (r *Refresher) fire(ctx context.Context) {
	err := r.refill(ctx)
	if err != nil {
		r.logger.Warn(
			"refresh: fire failed; counted as issued",
			slog.String("class", classifyOutcome(err)),
			slog.Any("err", err),
		)
	}
}

// windowContains reports whether t (in local time) falls inside the
// configured [start, end) interval. Honors the midnight-crossing
// case ("23:00-01:00"): when end < start the interval is treated as
// the contiguous span across midnight.
func (r *Refresher) windowContains(t time.Time) bool {
	mins := t.Hour()*60 + t.Minute()
	startMins := r.startHour*60 + r.startMin
	endMins := r.endHour*60 + r.endMin
	if startMins == endMins {
		return false
	}
	if startMins < endMins {
		return mins >= startMins && mins < endMins
	}
	// midnight-crossing: [start..24:00) U [00:00..end)
	return mins >= startMins || mins < endMins
}

// windowEndedBefore reports whether the window's end-instant has
// already elapsed *today* in local time. Used to gate the pre-deadline
// fallback: only fires when the window has already passed.
func (r *Refresher) windowEndedBefore(t time.Time) bool {
	mins := t.Hour()*60 + t.Minute()
	startMins := r.startHour*60 + r.startMin
	endMins := r.endHour*60 + r.endMin
	if startMins < endMins {
		return mins >= endMins
	}
	// midnight-crossing window: defines the window as crossing into
	// the next day; "today's window ended" only after the start hour
	// has passed AND we are past end (ambiguous in real life — we
	// treat it as ended only when we have passed the same-day end).
	return mins >= endMins && mins < startMins
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func sameDay(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	ya, ma, da := a.Date()
	yb, mb, db := b.Date()
	return ya == yb && ma == mb && da == db
}

// errRefreshWindowParse, errRefreshWindowHHMM, errRefreshWindowHour,
// errRefreshWindowMinute back the err113-compliant parser error
// shapes used by parseRefreshWindow / parseHHMM.
var (
	errRefreshWindowParse  = errors.New("supervise: invalid refresh window")
	errRefreshWindowHHMM   = errors.New("supervise: refresh window expected HH:MM")
	errRefreshWindowHour   = errors.New("supervise: refresh window invalid hour")
	errRefreshWindowMinute = errors.New("supervise: refresh window invalid minute")
)

// parseRefreshWindow parses a canonical "HH:MM-HH:MM" string.
func parseRefreshWindow(s string) (sh, sm, eh, em int, err error) {
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return 0, 0, 0, 0, fmt.Errorf("%w: %q", errRefreshWindowParse, s)
	}
	sh, sm, err = parseHHMM(parts[0])
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("%w: %q: start: %w", errRefreshWindowParse, s, err)
	}
	eh, em, err = parseHHMM(parts[1])
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("%w: %q: end: %w", errRefreshWindowParse, s, err)
	}
	return sh, sm, eh, em, nil
}

func parseHHMM(s string) (int, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("%w: got %q", errRefreshWindowHHMM, s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("%w: %q", errRefreshWindowHour, s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("%w: %q", errRefreshWindowMinute, s)
	}
	return h, m, nil
}
