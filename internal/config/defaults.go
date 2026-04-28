package config

import (
	"net/netip"
	"time"
)

// Argon2id parameters — Constitution III floors.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	DefaultArgonTime     uint32 = 4
	DefaultArgonMemoryMB uint32 = 256
	DefaultArgonThreads  uint8  = 4
	MinArgonTime         uint32 = 4
	MinArgonMemoryMB     uint32 = 256
	MinArgonThreads      uint8  = 4
)

// JWT / session / nonce / skew durations.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	DefaultJWTTTL            = 8 * time.Hour
	DefaultMaxInteractiveTTL = 12 * time.Hour
	DefaultMaxSupervisorTTL  = 20 * time.Hour
	DefaultSupervisorTTLMax  = 24 * time.Hour // v0.1.0 cap on max_supervisor_ttl
	DefaultMaxUses           = 50
	DefaultNonceTTL          = 60 * time.Second
	DefaultClockSkew         = 30 * time.Second
)

// Path defaults.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	DefaultStateDir       = "~/.hush"
	DefaultAuditLog       = "~/.hush/audit.jsonl"
	DefaultClientRegistry = "~/.hush/clients.json"
	DefaultListenPort     = 7743 // canonical port; no canonical IP
)

// Network defaults.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	DefaultRequireTailscale = true
	DefaultAllowedCIDRs     = []string{"100.64.0.0/10"}
)

// Security defaults.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	DefaultRequireFileModeChecks = true
	DefaultRequireKeychainACL    = true // macOS; SDD-10 decides whether to enforce on Linux
	DefaultRequireNTPSync        = true
	DefaultMaxClockDrift         = 60 * time.Second
)

// path_prefix bounds.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	MinPathPrefixLen = 6
	MaxPathPrefixLen = 32
)

// TailscaleCGNAT is the only acceptable network for listen_addr / health_bind.
// Constitution VI: Tailscale-only bind.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var TailscaleCGNAT = netip.MustParsePrefix("100.64.0.0/10")
