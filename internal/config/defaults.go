package config

import (
	"net/netip"
	"time"
)

// Argon2id parameters — Constitution III floors and v0.1.0 ceilings.
//
// Floors enforce the security minimum (256 MiB / time=4 / threads=4 — the
// 2026 commodity-malware-resistant baseline). Ceilings prevent
// mis-configuration from accidentally OOM-ing or wedging the boot
// sequence: a `argon_memory_mb = 1000000` would request 1 TB of RAM at
// first key derivation. Operators with extreme paranoia profiles can
// raise the ceilings here in a forked build; the public TOML schema is
// validated against both bounds.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	DefaultArgonTime     uint32 = 4
	DefaultArgonMemoryMB uint32 = 256
	DefaultArgonThreads  uint8  = 4
	MinArgonTime         uint32 = 4
	MinArgonMemoryMB     uint32 = 256
	MinArgonThreads      uint8  = 4
	MaxArgonTime         uint32 = 16   // 16 iterations: ≥30s on 2026 hardware, hard upper bound
	MaxArgonMemoryMB     uint32 = 4096 // 4 GiB: well above any realistic single-user baseline
	MaxArgonThreads      uint8  = 128  // 128 lanes: > any consumer CPU as of 2026
)

// JWT / session / nonce / skew durations.
//
//nolint:gochecknoglobals // sentinel-class: set-once at package load, never mutated
var (
	DefaultJWTTTL               = 8 * time.Hour
	DefaultMaxInteractiveTTL    = 12 * time.Hour
	DefaultMaxSupervisorTTL     = 20 * time.Hour
	DefaultSupervisorTTLMax     = 24 * time.Hour // v0.1.0 cap on max_supervisor_ttl
	DefaultMaxUses              = 50
	DefaultNonceTTL             = 60 * time.Second
	DefaultClockSkew            = 30 * time.Second
	DefaultClaimApprovalTimeout = 60 * time.Second
	MinClaimApprovalTimeout     = 1 * time.Second
	MaxClaimApprovalTimeout     = 10 * time.Minute
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
