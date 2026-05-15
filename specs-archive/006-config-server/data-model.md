# Phase 1 Data Model: `internal/config` (Server)

**Feature**: 006-config-server
**Date**: 2026-04-28

This package owns one main public type (`Server`) plus its sub-section structs, a parallel "decoded" wire-shape used internally by the loader, the typed-default catalogue, and the sentinel error catalogue. There is no persistence, no on-the-wire schema beyond the TOML file the operator authors. The "data model" below is the in-process representation.

---

## Public types

### `Server` (top-level config)

```go
type Server struct {
    Server   ServerSection
    Discord  DiscordSection
    Crypto   CryptoSection
    Network  NetworkSection
    Security SecuritySection
}
```

`Server` is plain data: pointers and reference types are NOT held; every field is a value. Concurrency-safe by virtue of being read-only after `LoadServer` returns. Consumers MAY pass `*Server` by pointer for efficiency but MUST NOT mutate it.

---

### `ServerSection`

```go
type ServerSection struct {
    ListenAddr             netip.AddrPort // pre-parsed from string; canonicalised
    PathPrefix             string         // [A-Za-z0-9_-]{6,32}
    StateDir               string         // absolute, ~-expanded, exists, is-a-directory
    AuditLog               string         // absolute, ~-expanded, under StateDir
    DiscordOwnerID         string         // Discord snowflake (non-secret)
    ClientRegistry         string         // absolute, ~-expanded
    DiscordAuditChannelID  string         // optional; empty == not configured
}
```

| Field                   | Required | Default                     | Validation rule                                      |
|-------------------------|----------|-----------------------------|------------------------------------------------------|
| `ListenAddr`            | yes      | (no default)                | Tailscale CGNAT membership; reject loopback/unspec/public/malformed |
| `PathPrefix`            | yes      | (no default)                | length 6-32; charset `[A-Za-z0-9_-]`                |
| `StateDir`              | yes      | `DefaultStateDir = "~/.hush"` (the `~` is documented; runtime expands) | exists; is-a-directory; never created by loader |
| `AuditLog`              | yes      | `DefaultAuditLog = "~/.hush/audit.jsonl"` | resolves under `StateDir`              |
| `DiscordOwnerID`        | yes      | (no default)                | non-empty (further format check is downstream)      |
| `ClientRegistry`        | yes      | `DefaultClientRegistry = "~/.hush/clients.json"` | none beyond `~`-expansion + Abs        |
| `DiscordAuditChannelID` | no       | `""` (omitted = unset)      | none                                                 |

Note: the chunk contract lists `DefaultListenAddr` as a constant. The implementation exposes this as `DefaultListenPort = 7743` (the documented port from CONFIG-SCHEMA examples). There is no canonical default IP — every operator's Tailscale IP is host-specific. The `DefaultListenPort` constant is what `hush init` writes into a sample config alongside the operator's prompted IP.

---

### `DiscordSection`

```go
type DiscordSection struct {
    BotTokenKeychainItem string // e.g., "hush-discord" — the Keychain item NAME, not the token
    ApplicationID        string // Discord app/bot ID — non-secret snowflake
}
```

| Field                  | Required | Default | Validation                                          |
|------------------------|----------|---------|-----------------------------------------------------|
| `BotTokenKeychainItem` | yes      | (none)  | non-empty string                                    |
| `ApplicationID`        | yes      | (none)  | non-empty string                                    |

**Constitutional note**: `BotTokenKeychainItem` holds an item name like `"hush-discord"`. SDD-10's startup wiring calls into the macOS Keychain (or a Linux equivalent) to fetch the actual token using this name. SDD-06 NEVER touches the token — the field is a non-secret pointer, satisfying Principle X.

---

### `CryptoSection`

```go
type CryptoSection struct {
    ArgonTime         uint32        // ≥ MinArgonTime (4)
    ArgonMemoryMB     uint32        // ≥ MinArgonMemoryMB (256)
    ArgonThreads      uint8         // ≥ MinArgonThreads (4)
    JWTDefaultTTL     time.Duration // default 8h
    MaxInteractiveTTL time.Duration // default 12h
    MaxSupervisorTTL  time.Duration // default 20h; > JWTDefaultTTL; ≤ DefaultSupervisorTTLMax (24h)
    DefaultMaxUses    int           // default 50
    NonceTTL          time.Duration // default 60s
    ClockSkew         time.Duration // default 30s
}
```

| Field               | Required | Default                            | Validation                                  |
|---------------------|----------|------------------------------------|---------------------------------------------|
| `ArgonTime`         | no       | `DefaultArgonTime = 4`             | ≥ `MinArgonTime`                            |
| `ArgonMemoryMB`     | no       | `DefaultArgonMemoryMB = 256`       | ≥ `MinArgonMemoryMB` (constitutional floor) |
| `ArgonThreads`      | no       | `DefaultArgonThreads = 4`          | ≥ `MinArgonThreads`                         |
| `JWTDefaultTTL`     | no       | `DefaultJWTTTL = 8h`               | parse must succeed                          |
| `MaxInteractiveTTL` | no       | `DefaultMaxInteractiveTTL = 12h`   | parse must succeed                          |
| `MaxSupervisorTTL`  | no       | `DefaultMaxSupervisorTTL = 20h`    | `> JWTDefaultTTL` AND `≤ DefaultSupervisorTTLMax` |
| `DefaultMaxUses`    | no       | `DefaultMaxUses = 50`              | (defensive) `> 0`                           |
| `NonceTTL`          | no       | `DefaultNonceTTL = 60s`            | parse must succeed                          |
| `ClockSkew`         | no       | `DefaultClockSkew = 30s`           | parse must succeed                          |

---

### `NetworkSection`

```go
type NetworkSection struct {
    RequireTailscale bool           // MUST be true; default true; false → ErrTailscaleRequired
    AllowedCIDRs     []string       // default ["100.64.0.0/10"]
    HealthBind       netip.AddrPort // default == ListenAddr; if explicit, validated identically
}
```

| Field              | Required | Default                                | Validation                                    |
|--------------------|----------|----------------------------------------|-----------------------------------------------|
| `RequireTailscale` | no       | `DefaultRequireTailscale = true`       | MUST be `true`                                |
| `AllowedCIDRs`     | no       | `DefaultAllowedCIDRs = ["100.64.0.0/10"]` | (defensive) at least one Tailscale CIDR present when `RequireTailscale == true` |
| `HealthBind`       | no       | inherits from `ListenAddr`             | when explicit: same rules as `ListenAddr`     |

---

### `SecuritySection`

```go
type SecuritySection struct {
    RequireFileModeChecks bool          // default true
    RequireKeychainACL    bool          // default true (macOS); SDD-10 may skip on non-darwin
    RequireNTPSync        bool          // default true
    MaxClockDrift         time.Duration // default 60s
}
```

| Field                   | Required | Default                                  | Validation         |
|-------------------------|----------|------------------------------------------|--------------------|
| `RequireFileModeChecks` | no       | `DefaultRequireFileModeChecks = true`    | (none — bool data) |
| `RequireKeychainACL`    | no       | `DefaultRequireKeychainACL = true`       | (none — bool data) |
| `RequireNTPSync`        | no       | `DefaultRequireNTPSync = true`           | (none — bool data) |
| `MaxClockDrift`         | no       | `DefaultMaxClockDrift = 60s`             | parse must succeed |

These flags have no validate-step rules in SDD-06 — they are consumed at runtime by SDD-10 (startup hardening) and SDD-17 (NTP-sync helper). SDD-06's job is to record the operator's intent; SDD-10 acts on it.

---

## Wire-shape (decoded) types — INTERNAL

These mirror the public types one-for-one but use pointer / sentinel forms where "absent vs zero" matters. The `materialize(serverDecoded) (*Server, error)` function in `server.go` reads decoded values and writes the public `Server`, applying defaults as listed above.

```go
type serverDecoded struct {
    Server   serverSectionDecoded   `toml:"server"`
    Discord  discordSectionDecoded  `toml:"discord"`
    Crypto   cryptoSectionDecoded   `toml:"crypto"`
    Network  networkSectionDecoded  `toml:"network"`
    Security securitySectionDecoded `toml:"security"`
}

type serverSectionDecoded struct {
    ListenAddr            string `toml:"listen_addr"`             // empty == absent
    PathPrefix            string `toml:"path_prefix"`             // empty == absent
    StateDir              string `toml:"state_dir"`               // empty == use default
    AuditLog              string `toml:"audit_log"`               // empty == use default
    DiscordOwnerID        string `toml:"discord_owner_id"`        // empty == absent
    ClientRegistry        string `toml:"client_registry"`         // empty == use default
    DiscordAuditChannelID string `toml:"discord_audit_channel_id"`// empty == omitted
}

type discordSectionDecoded struct {
    BotTokenKeychainItem string `toml:"bot_token_keychain_item"`
    ApplicationID        string `toml:"application_id"`
}

type cryptoSectionDecoded struct {
    ArgonTime         *uint32 `toml:"argon_time"`
    ArgonMemoryMB     *uint32 `toml:"argon_memory_mb"`
    ArgonThreads      *uint8  `toml:"argon_threads"`
    JWTDefaultTTL     string  `toml:"jwt_default_ttl"`     // duration string; "" == default
    MaxInteractiveTTL string  `toml:"max_interactive_ttl"`
    MaxSupervisorTTL  string  `toml:"max_supervisor_ttl"`
    DefaultMaxUses    *int    `toml:"default_max_uses"`
    NonceTTL          string  `toml:"nonce_ttl"`
    ClockSkew         string  `toml:"clock_skew"`
}

type networkSectionDecoded struct {
    RequireTailscale *bool    `toml:"require_tailscale"`
    AllowedCIDRs     []string `toml:"allowed_cidrs"`     // nil == default; non-nil incl empty == as-supplied
    HealthBind       string   `toml:"health_bind"`       // empty == inherit from ListenAddr
}

type securitySectionDecoded struct {
    RequireFileModeChecks *bool  `toml:"require_file_mode_checks"`
    RequireKeychainACL    *bool  `toml:"require_keychain_acl"`
    RequireNTPSync        *bool  `toml:"require_ntp_sync"`
    MaxClockDrift         string `toml:"max_clock_drift"`
}
```

The `*Decoded` types are unexported (lowercase). They never appear in the public API. The materializer is the only consumer.

---

## Defaults catalogue

All defaults are exported `var` declarations in `defaults.go`. The catalogue is the contract every `hush init` template and every test asserts against. Drift between this catalogue and `docs/CONFIG-SCHEMA.md` is a bug.

```go
// Argon2id parameters (Constitution III floors).
var (
    DefaultArgonTime     uint32 = 4
    DefaultArgonMemoryMB uint32 = 256
    DefaultArgonThreads  uint8  = 4
    MinArgonTime         uint32 = 4
    MinArgonMemoryMB     uint32 = 256
    MinArgonThreads      uint8  = 4
)

// JWT / session / nonce / skew durations.
var (
    DefaultJWTTTL             = 8 * time.Hour
    DefaultMaxInteractiveTTL  = 12 * time.Hour
    DefaultMaxSupervisorTTL   = 20 * time.Hour
    DefaultSupervisorTTLMax   = 24 * time.Hour // v0.1.0 cap on max_supervisor_ttl
    DefaultMaxUses            = 50
    DefaultNonceTTL           = 60 * time.Second
    DefaultClockSkew          = 30 * time.Second
)

// Path defaults.
var (
    DefaultStateDir       = "~/.hush"
    DefaultAuditLog       = "~/.hush/audit.jsonl"
    DefaultClientRegistry = "~/.hush/clients.json"
    DefaultListenPort     = 7743 // canonical port; no canonical IP
)

// Network defaults.
var (
    DefaultRequireTailscale = true
    DefaultAllowedCIDRs     = []string{"100.64.0.0/10"}
)

// Security defaults.
var (
    DefaultRequireFileModeChecks = true
    DefaultRequireKeychainACL    = true // macOS; SDD-10 decides whether to enforce on Linux
    DefaultRequireNTPSync        = true
    DefaultMaxClockDrift         = 60 * time.Second
)

// path_prefix bounds.
var (
    MinPathPrefixLen = 6
    MaxPathPrefixLen = 32
)

// Tailscale CGNAT prefix — the only acceptable network for listen_addr / health_bind.
var TailscaleCGNAT = netip.MustParsePrefix("100.64.0.0/10")
```

All variables are read-only after package load. Inline `//nolint:gochecknoglobals` comments cite the sentinel-class precedent.

---

## Sentinel error catalogue

All sentinels live in `errors.go`. Each is `var ErrXxx = errors.New("hush/config: <message>")`. Wrap relationships are documented in the godoc.

```go
// Decode-phase errors.
var (
    ErrTOMLDecode           = errors.New("hush/config: TOML decode failed")
    ErrUnknownField         = errors.New("hush/config: unknown field")
    ErrMissingRequiredField = errors.New("hush/config: missing required field")
    ErrInvalidDuration      = errors.New("hush/config: invalid duration")
)

// Network/listen-addr errors.
var (
    ErrTailscaleBindRequired = errors.New("hush/config: Tailscale bind required (100.64.0.0/10)")
    ErrListenLoopback        = fmt.Errorf("hush/config: listen address is loopback: %w", ErrTailscaleBindRequired)
    ErrListenUnspecified     = fmt.Errorf("hush/config: listen address is unspecified: %w", ErrTailscaleBindRequired)
    ErrListenPublic          = fmt.Errorf("hush/config: listen address is not in Tailscale CGNAT: %w", ErrTailscaleBindRequired)
    ErrListenMalformed       = errors.New("hush/config: listen address is malformed")
    ErrTailscaleRequired     = errors.New("hush/config: require_tailscale must be true (v0.1.0)")
)

// Path-safety errors.
var (
    ErrPathPrefixInvalid = errors.New("hush/config: path_prefix invalid (must be 6-32 chars, [A-Za-z0-9_-])")
    ErrAuditLogEscape    = errors.New("hush/config: audit_log resolves outside state_dir")
    ErrStateDirNotFound  = errors.New("hush/config: state_dir does not exist")
    ErrStateDirUnsafe    = errors.New("hush/config: state_dir is not a directory")
)

// Crypto-floor errors.
var (
    ErrArgonMemoryTooLow  = errors.New("hush/config: argon_memory_mb below floor (256 MiB)")
    ErrArgonTimeTooLow    = errors.New("hush/config: argon_time below floor (4)")
    ErrArgonThreadsTooLow = errors.New("hush/config: argon_threads below floor (4)")
)

// TTL-bound error.
var ErrSupervisorTTLOutOfRange = errors.New("hush/config: max_supervisor_ttl out of range (must be > jwt_default_ttl and ≤ 24h)")
```

| Sentinel                       | Wraps                       | Field(s) it can apply to                |
|--------------------------------|-----------------------------|------------------------------------------|
| `ErrTOMLDecode`                | go-toml/v2 inner error      | (top-level decode)                       |
| `ErrUnknownField`              | go-toml/v2 strict-mode error| any                                      |
| `ErrMissingRequiredField`      | (none)                      | listen_addr, path_prefix, discord_owner_id, bot_token_keychain_item, application_id |
| `ErrInvalidDuration`           | (none — context in message) | jwt_default_ttl, max_interactive_ttl, max_supervisor_ttl, nonce_ttl, clock_skew, max_clock_drift |
| `ErrTailscaleBindRequired`     | (umbrella)                  | listen_addr, health_bind                 |
| `ErrListenLoopback`            | `ErrTailscaleBindRequired`  | listen_addr, health_bind                 |
| `ErrListenUnspecified`         | `ErrTailscaleBindRequired`  | listen_addr, health_bind                 |
| `ErrListenPublic`              | `ErrTailscaleBindRequired`  | listen_addr, health_bind                 |
| `ErrListenMalformed`           | (none)                      | listen_addr, health_bind                 |
| `ErrTailscaleRequired`         | (none)                      | require_tailscale                        |
| `ErrPathPrefixInvalid`         | (none)                      | path_prefix                              |
| `ErrAuditLogEscape`            | (none)                      | audit_log                                |
| `ErrStateDirNotFound`          | wraps `fs.ErrNotExist`       | state_dir                                |
| `ErrStateDirUnsafe`            | (none)                      | state_dir                                |
| `ErrArgonMemoryTooLow`         | (none)                      | argon_memory_mb                          |
| `ErrArgonTimeTooLow`           | (none)                      | argon_time                               |
| `ErrArgonThreadsTooLow`        | (none)                      | argon_threads                            |
| `ErrSupervisorTTLOutOfRange`   | (none)                      | max_supervisor_ttl (relative to jwt_default_ttl + 24h cap) |

---

## Lifecycle / state transitions

The package has no long-lived state. The `LoadServer` lifecycle is:

```
caller calls LoadServer(ctx, path)
   └── (1) os.Open(path) — error → wrap as fs error, return
   └── (2) toml.NewDecoder(f).DisallowUnknownFields(true).Decode(&serverDecoded)
   │        ├── go-toml/v2 strict-mode error → wrap as ErrUnknownField, return
   │        └── any other decode error → wrap as ErrTOMLDecode, return
   └── (3) materialize(serverDecoded) → (*Server, error)
   │        ├── apply defaults to absent fields
   │        ├── parse duration strings → ErrInvalidDuration on parse fail
   │        ├── ~-expand + Abs every path field
   │        ├── stat state_dir → ErrStateDirNotFound | ErrStateDirUnsafe
   │        └── accumulate ErrMissingRequiredField for empty required fields
   └── (4) (*Server).Validate()
   │        ├── argon floors → ErrArgonMemoryTooLow | ErrArgonTimeTooLow | ErrArgonThreadsTooLow
   │        ├── require_tailscale truthiness → ErrTailscaleRequired
   │        ├── listen_addr → ErrListenMalformed | ErrListenLoopback | ErrListenUnspecified | ErrListenPublic
   │        ├── health_bind (when set) → same family
   │        ├── path_prefix → ErrPathPrefixInvalid
   │        ├── audit_log under state_dir → ErrAuditLogEscape
   │        └── max_supervisor_ttl bounds → ErrSupervisorTTLOutOfRange
   │        (multi-violation → errors.Join)
   └── return (s, nil) on success; (nil, err) on any failure (s is never partially populated when err != nil).
```

After return, the `*Server` is read-only. The package never spawns a goroutine, never opens a network socket, never writes to disk.

---

## Acceptance-criterion → entity mapping

| Spec requirement / SC | Entity / behaviour                                                                                |
|-----------------------|---------------------------------------------------------------------------------------------------|
| FR-001                | `Server` struct fields exactly mirror `docs/CONFIG-SCHEMA.md` server section                       |
| FR-002                | `toml.Decoder.DisallowUnknownFields(true)` → `ErrUnknownField` on unknown key                     |
| FR-003                | `validateTailscaleAddrPort` → `ErrListenLoopback` / `ErrListenUnspecified` / `ErrListenPublic` / `ErrListenMalformed` |
| FR-003a               | Same validator path applied to `health_bind` when explicit                                         |
| FR-004                | `Validate.argonFloors` → `ErrArgonMemoryTooLow` (and ErrArgonTimeTooLow / ErrArgonThreadsTooLow)  |
| FR-005                | `Validate.auditLogContainment` → `ErrAuditLogEscape`                                              |
| FR-005a               | `materialize.statStateDir` → `ErrStateDirNotFound`                                                |
| FR-005b               | `expandHome` + `filepath.Abs` in `paths.go`                                                       |
| FR-005c               | `Validate.requireTailscale` → `ErrTailscaleRequired`                                              |
| FR-005d               | `Validate.pathPrefix` → `ErrPathPrefixInvalid`                                                    |
| FR-006                | `Server` struct schema review + `TestServerSchema_NoSecretFields`                                 |
| FR-007                | No `os.Getenv` in package + `TestLoadServer_DoesNotReadSecretsFromEnv`                            |
| FR-008                | `defaults.go` catalogue + `TestLoadServer_AppliesEveryDocumentedDefault`                          |
| FR-009                | Sentinel-error catalogue table above                                                              |
| FR-010                | `FuzzServerTOML` (60 s gate)                                                                      |
| FR-011                | `LoadServer` is pure (no global state mutation); `TestLoadServer_Idempotent`                       |
| FR-012                | Error messages name fields; never embed file content                                              |
| SC-001                | `TestLoadServer_AppliesEveryDocumentedDefault` + 95% coverage gate                                |
| SC-002                | One named test per sentinel — see `validate_test.go` table-driven                                 |
| SC-003                | `FuzzServerTOML` 60 s gate                                                                        |
| SC-004                | Error messages are field-named; multi-violation reports use `errors.Join`                         |
| SC-005                | Schema-review + `TestLoadServer_DoesNotReadSecretsFromEnv`                                       |

---

## Anti-model (what is NOT modelled)

- **No `*os.File` retained**: the file is opened, decoded, closed in `LoadServer`. No handle escapes the function.
- **No `context.Context` storage**: `ctx` is inspected once at entry (cancellation check); no goroutines are spawned that would carry it.
- **No mutable shared state**: every default and sentinel `var` is set-once at package load; mutation by external code is undefined behaviour.
- **No environment-variable mapping**: there is no "if env var is set, override" code path. The only env-reading call is `os.UserHomeDir` for `~` expansion (non-secret).
- **No file mode enforcement**: SDD-06 records the operator's intent (`RequireFileModeChecks` flag); SDD-10 enforces it at startup.
- **No NTP probe**: SDD-06 records the operator's intent (`RequireNTPSync` flag); SDD-17 implements the probe.
- **No Keychain access**: SDD-06 records the Keychain item name; SDD-10 fetches the actual token.
- **No supervisor config**: that lives at `internal/config/supervisor.go` per SDD-18.
