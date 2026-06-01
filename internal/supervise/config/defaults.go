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

	// T-306 reload-eligibility defaults. Readiness defaults are tuned for
	// HTTP /health probes on local-loopback/Tailscale: a 30s budget covers a
	// cold-starting daemon while a 200ms interval keeps swap latency low.
	// Shutdown grace is a hard answer to Q4 (configurable, default 30s).
	DefaultReadinessTimeout  = 30 * time.Second
	DefaultReadinessInterval = 200 * time.Millisecond
	DefaultShutdownGrace     = 30 * time.Second
)

// HandoffModeHTTPProxy is the only v1 reload handoff strategy. Socket
// activation is the planned generic follow-up but is intentionally not in
// the allow-list yet.
const HandoffModeHTTPProxy = "http-proxy"

// EnvVarBindPort is the env var hush sets on a reload-eligible child so the
// child binds to the hush-allocated private backend port. Validation
// requires child.command or child.env to mention this name verbatim, which
// is the operator's signal that the child knows about HUSH_BIND_PORT.
const EnvVarBindPort = "HUSH_BIND_PORT"

// handoffModeAllowList is the set of accepted child.handoff.mode values.
// Single-entry today; kept as a map so adding socket-activation later is a
// one-line change without restructuring validation.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var handoffModeAllowList = map[string]struct{}{
	HandoffModeHTTPProxy: {},
}

// Constitutional bounds — encoded as typed vars so downstream consumers and
// tests reference the exact constitutional values.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	MaxGraceWindow        = 4 * time.Hour  // Constitution IV: TTL discipline + Layer-6 audit boundary
	MaxRequestedTTL       = 24 * time.Hour // v0.1.0 ceiling per docs/CONFIG-SCHEMA.md max_supervisor_ttl
	MaxBootRetryTimeout   = 1 * time.Hour  // operator typo guard: 100h would silently disable boot timeout
	MaxRefreshNudgeBefore = 6 * time.Hour  // bounded to a fraction of MaxRequestedTTL
	ResealMinSessionFloor = 1 * time.Hour  // v1 scheduled reseal floor for avoiding too-short sessions
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
