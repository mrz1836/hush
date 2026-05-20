package config

import "time"

// Defaults catalog — every value below MUST exactly equal the corresponding
// documented default in docs/CONFIG-SCHEMA.md "Supervisor config" section.
// Each is asserted by a unit test.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	DefaultRequestedTTL             = 20 * time.Hour
	DefaultRefreshWindow            = "09:00-10:00"
	DefaultRefreshNudgeBefore       = 30 * time.Minute
	DefaultBootRetryTimeout         = 10 * time.Minute
	DefaultCacheSecretsForRestart   = false
	DefaultGraceWindow              = 60 * time.Minute
	DefaultLogLevel                 = "info"
	DefaultRestartOnCleanExit       = true
	DefaultRestartOnExit78          = false
	DefaultWatchdogEnabled          = true
	DefaultWatchdogMaxAlertsPerHour = 6
	DefaultWatchdogPatterns         = []string{}
	DefaultDMRateLimit              = 5 * time.Minute
)

// Constitutional bounds — encoded as typed vars so downstream consumers and
// tests reference the exact constitutional values.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	MaxGraceWindow        = 4 * time.Hour  // Constitution IV: TTL discipline + Layer-6 audit boundary
	MaxRequestedTTL       = 24 * time.Hour // v0.1.0 ceiling per docs/CONFIG-SCHEMA.md max_supervisor_ttl
	MaxBootRetryTimeout   = 1 * time.Hour  // operator typo guard: 100h would silently disable boot timeout
	MaxRefreshNudgeBefore = 6 * time.Hour  // bounded to a fraction of MaxRequestedTTL
)

// validatorAllowList is the fixed set of validator type names accepted in the
// [validators] map values. Constitution V — operator visibility requires the
// allow-list to be explicit and small. Any addition or removal requires a
// constitutional amendment, not a configuration change.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var validatorAllowList = map[string]struct{}{
	"anthropic":       {},
	"anthropic-oauth": {},
	"openai":          {},
	"google-ai":       {},
	"github":          {},
}

// logLevelAllowList is the fixed set of log_level values accepted at load
// time. Mirrored from docs/CONFIG-SCHEMA.md.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var logLevelAllowList = map[string]struct{}{
	"debug": {},
	"info":  {},
	"warn":  {},
	"error": {},
}
