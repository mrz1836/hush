package watchdog

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/mrz1836/hush/internal/supervise"
)

// logPatternErrorClass is the closed-set error-class label carried on every
// log-pattern alert. The watchdog is alert-only — a match is advisory, never
// authoritative — so the class is always "transient".
const logPatternErrorClass = "transient"

// logPatternReason is the operator-visible phrase for a log-pattern alert.
// It mirrors the supervise package's closed reason map.
const logPatternReason = "log pattern matched"

// BuildPatterns compiles a slice of operator-configured regex strings into
// watchdog Patterns. Every pattern shares the rate limit derived from
// maxAlertsPerHour (clamped to at least one alert/hour). Patterns are named
// by their 1-based index so duplicate-name validation never trips on
// identical regex text. An invalid regex returns a wrapped error.
func BuildPatterns(patterns []string, maxAlertsPerHour int) ([]Pattern, error) {
	if maxAlertsPerHour < 1 {
		maxAlertsPerHour = 1
	}
	rate := time.Hour / time.Duration(maxAlertsPerHour)
	out := make([]Pattern, 0, len(patterns))
	for i, expr := range patterns {
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("watchdog: pattern[%d] %q: %w", i, expr, err)
		}
		out = append(out, Pattern{
			Name:      fmt.Sprintf("pattern-%d", i+1),
			Regex:     re,
			RateLimit: rate,
		})
	}
	return out, nil
}

// DrainToAlerts consumes watchdog match Events and forwards each as an
// AlertClassLogPatternMatch alert on the supplied supervise.Alerts sink.
// It blocks until ctx is cancelled or events is closed; run it in its own
// goroutine alongside Watchdog.Run.
//
// This is the bridge between the watchdog's typed Event channel and the
// orchestrator's operator-visible alert sink. The watchdog has zero
// authority over the state machine, so DrainToAlerts only ever calls
// Alerts.Emit — it never triggers a transition.
func DrainToAlerts(ctx context.Context, events <-chan Event, alerts supervise.Alerts) {
	if alerts == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			alerts.Emit(ctx, supervise.AlertClassLogPatternMatch, supervise.AlertPayload{
				ErrorClass: logPatternErrorClass,
				Reason:     logPatternReason,
			})
		}
	}
}
