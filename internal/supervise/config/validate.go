package config

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

// requiredFieldGate runs after decode but before any other rule. Aggregates
// every missing required field via errors.Join so the operator sees the full
// punch list in one round-trip. Each individual
// error wraps ErrMissingRequiredField so errors.Is matches.
//
// Required fields per docs/CONFIG-SCHEMA.md "Supervisor config" §Root and
// [child] / [validators] sections.
//
//nolint:cyclop // rule-engine: one branch per documented required field
func requiredFieldGate(d supervisorDecoded) error {
	var errs []error
	missing := func(path string) {
		errs = append(errs, fmt.Errorf("%w: %s", ErrMissingRequiredField, path))
	}

	// Required fields per docs/CONFIG-SCHEMA.md: those without a documented
	// default. Fields that have a documented default (requested_ttl,
	// refresh_window, log_level, etc.) are optional — absence applies the
	// default.
	if strings.TrimSpace(d.Name) == "" {
		missing("name")
	}
	if strings.TrimSpace(d.Reason) == "" {
		missing("reason")
	}
	if strings.TrimSpace(d.ServerURL) == "" {
		missing("server_url")
	}
	if d.ClientMachineIndex == nil {
		missing("client_machine_index")
	}
	if strings.TrimSpace(d.SessionType) == "" {
		missing("session_type")
	}
	if strings.TrimSpace(d.StatusSocket) == "" {
		missing("status_socket")
	}
	if strings.TrimSpace(d.PIDFile) == "" {
		missing("pid_file")
	}
	// child.command absent (nil slice) → missing required; explicit empty
	// (non-nil, len=0) → ErrCommandEmpty (caught later in materialize).
	if d.Child.Command == nil {
		missing("child.command")
	}
	return errors.Join(errs...)
}

// parseDuration converts a duration string to time.Duration, returning def
// when raw is empty, or wrapping ErrInvalidDuration when parsing fails.
func parseDuration(raw string, def time.Duration, fieldName string) (time.Duration, error) {
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%w: field %s value %q", ErrInvalidDuration, fieldName, raw)
	}
	return d, nil
}

// validateRefreshWindow parses raw as "HH:MM-HH:MM". Format violations →
// ErrRefreshWindowFormat; format-clean but start >= end (incl. wrap-around) →
// ErrRefreshWindowOrder.
func validateRefreshWindow(raw string) error {
	idx := strings.Index(raw, "-")
	if idx < 0 || strings.LastIndex(raw, "-") != idx {
		return fmt.Errorf("%w: got %q", ErrRefreshWindowFormat, raw)
	}
	start, end := raw[:idx], raw[idx+1:]
	// Require exact "HH:MM" shape (length 5, leading-zero hours). time.Parse
	// alone is lenient about single-digit hours when the reference value is
	// "15:04"; we want to reject "9:00" per docs/CONFIG-SCHEMA.md.
	if len(start) != 5 || len(end) != 5 {
		return fmt.Errorf("%w: got %q", ErrRefreshWindowFormat, raw)
	}
	startT, err := time.Parse("15:04", start)
	if err != nil {
		return fmt.Errorf("%w: got %q", ErrRefreshWindowFormat, raw)
	}
	endT, err := time.Parse("15:04", end)
	if err != nil {
		return fmt.Errorf("%w: got %q", ErrRefreshWindowFormat, raw)
	}
	if !startT.Before(endT) {
		return fmt.Errorf("%w: got %q", ErrRefreshWindowOrder, raw)
	}
	return nil
}

// parseResealHHMM parses raw as "HH:MM" with an exact leading-zero shape.
func parseResealHHMM(raw string) (hhmm, error) {
	if len(raw) != 5 {
		return hhmm{}, fmt.Errorf("%w: got %q", ErrResealTimeFormat, raw)
	}
	parsed, err := time.Parse("15:04", raw)
	if err != nil {
		return hhmm{}, fmt.Errorf("%w: got %q", ErrResealTimeFormat, raw)
	}
	return hhmm{Hour: parsed.Hour(), Minute: parsed.Minute()}, nil
}

func validateResealHHMM(value hhmm) error {
	if value.Hour < 0 || value.Hour > 23 || value.Minute < 0 || value.Minute > 59 {
		return fmt.Errorf("%w: got %02d:%02d", ErrResealTimeFormat, value.Hour, value.Minute)
	}
	return nil
}

//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var weekdayByLowercaseName = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

// validateServerURL parses raw via net/url and rejects empty / parse-error /
// empty-host / non-http(s)-scheme. Deeper checks (Tailscale CIDR, port, path)
// are deferred to runtime hardening.
func validateServerURL(raw string) error {
	if raw == "" {
		return fmt.Errorf("%w: empty value", ErrServerURLInvalid)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: parse error", ErrServerURLInvalid)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: missing host", ErrServerURLInvalid)
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("%w: unsupported scheme %q", ErrServerURLInvalid, u.Scheme)
	}
	return nil
}

// filepathIsAbs is the package-local alias for filepath.IsAbs. Wrapped to
// keep the single import-path in this file's import list and to provide a
// hook if Windows-aware behaviour is ever needed (currently the project is
// darwin/linux only).
func filepathIsAbs(p string) bool {
	return filepath.IsAbs(p)
}

// requestedTTLCeiling returns the requested_ttl ceiling that applies to a
// supervisor config. A machine-bound standing lease (standing_lease = true) may
// request past 24h, up to MaxStandingLeaseTTL; every ordinary supervisor keeps
// the 24h MaxRequestedTTL ceiling. The standing ceiling never applies to a
// session that did not opt in — the caller passes the session's standing flag.
func requestedTTLCeiling(standingLease bool) time.Duration {
	if standingLease {
		return MaxStandingLeaseTTL
	}
	return MaxRequestedTTL
}

// Validate re-runs the full validation pipeline against an in-memory
// *Supervisor. Returns nil on success or a wrapped sentinel on the first
// violation; multi-violation reports use errors.Join.
//
// This entry point exists for tests that construct a *Supervisor
// programmatically and for defensive re-validation in downstream chunks. It
// is pure: no I/O, no goroutines, no state.
//
//nolint:cyclop,gocognit,gocyclo,funlen // rule-engine: one branch per documented rule, mirrors materialize
func (s *Supervisor) Validate() error {
	if s == nil {
		return errNilSupervisor
	}
	var errs []error

	if strings.TrimSpace(s.Name) == "" {
		errs = append(errs, fmt.Errorf("%w: name", ErrMissingRequiredField))
	}
	if strings.TrimSpace(s.Reason) == "" {
		errs = append(errs, fmt.Errorf("%w: reason", ErrMissingRequiredField))
	}
	if err := validateServerURL(s.ServerURL); err != nil {
		errs = append(errs, err)
	}
	if s.SessionType != "supervisor" {
		errs = append(errs, fmt.Errorf("%w: got %q", ErrSessionTypeInvalid, s.SessionType))
	}
	// standing_lease, when opted in, must anchor to a concrete machine client
	// key (a non-zero client_machine_index) so an unattended reissue re-binds.
	if s.StandingLease && s.ClientMachineIndex == 0 {
		errs = append(errs, fmt.Errorf("%w", ErrStandingLeaseNeedsMachineIndex))
	}
	// requested_ttl ceiling. A machine-bound standing lease may exceed 24h up to
	// MaxStandingLeaseTTL; every ordinary supervisor keeps the 24h ceiling.
	if ceiling := requestedTTLCeiling(s.StandingLease); s.RequestedTTL > ceiling {
		errs = append(errs, fmt.Errorf("%w: requested_ttl=%s, ceiling=%s", ErrRequestedTTLOutOfRange, s.RequestedTTL, ceiling))
	}
	if err := validateRefreshWindow(s.RefreshWindow); err != nil {
		errs = append(errs, err)
	}
	if !s.CacheSecretsForRestart && s.CacheGraceTTL != 0 {
		errs = append(errs, fmt.Errorf("%w", ErrGraceTTLWithoutCache))
	}
	if s.CacheGraceTTL > MaxGraceWindow {
		errs = append(errs, fmt.Errorf("%w: cache_grace_ttl=%s", ErrGraceWindowTooLong, s.CacheGraceTTL))
	}
	if strings.TrimSpace(s.StatusSocket) == "" {
		errs = append(errs, fmt.Errorf("%w: status_socket", ErrMissingRequiredField))
	}
	if strings.TrimSpace(s.PIDFile) == "" {
		errs = append(errs, fmt.Errorf("%w: pid_file", ErrMissingRequiredField))
	}
	if _, ok := logLevelAllowList[s.LogLevel]; !ok {
		errs = append(errs, fmt.Errorf("%w: got %q", ErrLogLevelInvalid, s.LogLevel))
	}
	if len(s.Scope) == 0 {
		errs = append(errs, fmt.Errorf("%w", ErrScopeEmpty))
	}
	if len(s.Child.Command) == 0 {
		errs = append(errs, fmt.Errorf("%w", ErrCommandEmpty))
	} else if !filepathIsAbs(s.Child.Command[0]) {
		errs = append(errs, fmt.Errorf("%w: got %q", ErrCommandPathRelative, s.Child.Command[0]))
	}
	for _, v := range s.Validators {
		if _, ok := validatorAllowList[string(v)]; !ok {
			errs = append(errs, fmt.Errorf("hush/supervise/config: unknown validator %q: %w", string(v), ErrUnknownValidator))
		}
	}
	if s.Watchdog.MaxAlertsPerHour <= 0 {
		errs = append(errs, fmt.Errorf("%w: got %d", ErrWatchdogRateInvalid, s.Watchdog.MaxAlertsPerHour))
	}

	// [child.readiness] — when present, validate URL + positive durations.
	errs = append(errs, validateReadiness(s.Child.Readiness)...)

	// [child.shutdown] — Grace must be > 0 (defaulted by Load but
	// programmatic constructors may leave it zero).
	if s.Child.Shutdown.Grace <= 0 {
		errs = append(errs, fmt.Errorf("%w: got %s", ErrShutdownGraceInvalid, s.Child.Shutdown.Grace))
	}

	// [child.handoff] — when present, enforce reload eligibility contract:
	// mode in allow-list, listen_addr non-empty, readiness present, and
	// child references HUSH_BIND_PORT.
	errs = append(errs, validateHandoff(s.Child)...)

	// [reseal] — when present, enforce materialized timezone, daily time, and
	// weekday override validity.
	errs = append(errs, validateReseal(s.Reseal)...)

	return errors.Join(errs...)
}

// validateReadiness returns the set of validation errors for the optional
// [child.readiness] section. Returns nil when rd is nil.
func validateReadiness(rd *ChildReadiness) []error {
	if rd == nil {
		return nil
	}
	var errs []error
	httpURL := strings.TrimSpace(rd.HTTPURL)
	if httpURL == "" {
		errs = append(errs, fmt.Errorf("%w: child.readiness.http_url", ErrMissingRequiredField))
	} else if err := validateReadinessURL(httpURL); err != nil {
		errs = append(errs, err)
	}
	if rd.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("%w: child.readiness.timeout=%s", ErrReadinessDurationInvalid, rd.Timeout))
	}
	if rd.Interval <= 0 {
		errs = append(errs, fmt.Errorf("%w: child.readiness.interval=%s", ErrReadinessDurationInvalid, rd.Interval))
	}
	return errs
}

// validateHandoff returns the set of validation errors for the optional
// [child.handoff] section. Returns nil when child.Handoff is nil.
func validateHandoff(child Child) []error {
	handoff := child.Handoff
	if handoff == nil {
		return nil
	}
	var errs []error
	if _, ok := handoffModeAllowList[handoff.Mode]; !ok {
		errs = append(errs, fmt.Errorf("%w: got %q", ErrHandoffModeInvalid, handoff.Mode))
	}
	if strings.TrimSpace(handoff.ListenAddr) == "" {
		errs = append(errs, fmt.Errorf("%w: child.handoff.listen_addr", ErrMissingRequiredField))
	}
	if child.Readiness == nil {
		errs = append(errs, fmt.Errorf("%w", ErrHandoffRequiresReadiness))
	}
	if !childReferencesBindPort(child) {
		errs = append(errs, fmt.Errorf("%w", ErrHandoffRequiresBindPortRef))
	}
	return errs
}

// validateReseal returns validation errors for the optional [reseal] section.
func validateReseal(reseal *ResealSchedule) []error {
	if reseal == nil {
		return nil
	}
	var errs []error
	if reseal.Location == nil {
		errs = append(errs, fmt.Errorf("%w: reseal.timezone", ErrResealTimezoneMissing))
	}
	if err := validateResealHHMM(reseal.DailyTime); err != nil {
		errs = append(errs, err)
	}
	for weekday, override := range reseal.Overrides {
		if weekday < time.Sunday || weekday > time.Saturday {
			errs = append(errs, fmt.Errorf("%w: got %d", ErrResealWeekdayInvalid, weekday))
			continue
		}
		if err := validateResealHHMM(override); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
