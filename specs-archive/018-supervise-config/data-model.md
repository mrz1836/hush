# Phase 1 Data Model: `internal/supervise/config`

**Feature**: 018-supervise-config
**Date**: 2026-05-05

This package owns one main public type (`Supervisor`) plus its
sub-section structs (`Child`, `DiscordRouting`, `Watchdog`), a
`Validator` typedef, a parallel "decoded" wire-shape used internally
by the loader, the typed-default catalogue, and the sentinel error
catalogue. There is no persistence and no on-the-wire schema beyond
the TOML file the operator authors. The "data model" below is the
in-process representation.

---

## Public types

### `Supervisor` (top-level config)

```go
type Supervisor struct {
    Name                   string
    Reason                 string
    ServerURL              string
    ClientMachineIndex     uint32
    SessionType            string                // always "supervisor" after Validate
    RequestedTTL           time.Duration
    RefreshWindow          string                // canonical "HH:MM-HH:MM"
    RefreshNudgeBefore     time.Duration
    BootRetryTimeout       time.Duration
    CacheSecretsForRestart bool
    CacheGraceTTL          time.Duration         // 0 when CacheSecretsForRestart is false
    StatusSocket           string                // absolute, ~-expanded
    PIDFile                string                // absolute, ~-expanded
    LogLevel               string                // one of {debug, info, warn, error}
    Scope                  []string              // non-empty after Validate

    Child      Child
    Discord    DiscordRouting
    Validators map[string]Validator
    Watchdog   Watchdog
}
```

`Supervisor` is plain data: pointers and reference types are NOT
held; every field is a value or an explicitly-owned slice/map.
Concurrency-safe by virtue of being read-only after `Load` returns.
Consumers MAY pass `*Supervisor` by pointer for efficiency but MUST
NOT mutate it.

| Field                  | Required | Default                                  | Validation rule                                           |
|------------------------|----------|------------------------------------------|-----------------------------------------------------------|
| `Name`                 | yes      | (no default)                             | non-empty after trim                                       |
| `Reason`               | yes      | (no default)                             | non-empty                                                 |
| `ServerURL`            | yes      | (no default)                             | parses as URL with non-empty Host + scheme `http`/`https`  |
| `ClientMachineIndex`   | yes      | (no default)                             | uint32 (decoder enforces; no narrower bound at v0.1.0)    |
| `SessionType`          | yes      | (no default)                             | exact string `"supervisor"`                               |
| `RequestedTTL`         | yes      | `DefaultRequestedTTL = 20 * time.Hour`   | parses as duration; ≤ `MaxRequestedTTL = 24 * time.Hour`   |
| `RefreshWindow`        | yes      | `DefaultRefreshWindow = "09:00-10:00"`   | matches `HH:MM-HH:MM` with start < end                     |
| `RefreshNudgeBefore`   | no       | `DefaultRefreshNudgeBefore = 30 * time.Minute` | parses as duration                                  |
| `BootRetryTimeout`     | no       | `DefaultBootRetryTimeout = 10 * time.Minute`   | parses as duration                                  |
| `CacheSecretsForRestart` | no     | `DefaultCacheSecretsForRestart = false` | bool                                                       |
| `CacheGraceTTL`        | no       | `DefaultGraceWindow = 60 * time.Minute` (when cache enabled) | ≤ `MaxGraceWindow = 4 * time.Hour`; absent-but-set → `ErrGraceTTLWithoutCache` when cache is false |
| `StatusSocket`         | yes      | (no default)                             | non-empty; `~`-expanded; absolute after expansion         |
| `PIDFile`              | yes      | (no default)                             | non-empty; `~`-expanded; absolute after expansion         |
| `LogLevel`             | no       | `DefaultLogLevel = "info"`               | one of `{debug, info, warn, error}`                        |
| `Scope`                | yes      | (no default)                             | non-empty; absence and emptiness are equivalent           |

---

### `Child` (`[child]` section)

```go
type Child struct {
    Command            []string  // first element is filepath.IsAbs
    WorkingDir         string    // ~-expanded, may be empty
    EnvPassthrough     []string  // exact env-var names to inherit
    RestartOnCleanExit bool
    RestartOnExit78    bool
}
```

| Field                | Required | Default                                  | Validation rule                                           |
|----------------------|----------|------------------------------------------|-----------------------------------------------------------|
| `Command`            | yes      | (no default)                             | non-empty array; `Command[0]` passes `filepath.IsAbs`     |
| `WorkingDir`         | yes (per CONFIG-SCHEMA) | (no default)              | non-empty; `~`-expanded after Load                         |
| `EnvPassthrough`     | yes (per CONFIG-SCHEMA) | (no default)              | string slice (may be empty)                                |
| `RestartOnCleanExit` | no       | `DefaultRestartOnCleanExit = true`       | bool                                                       |
| `RestartOnExit78`    | no       | `DefaultRestartOnExit78 = false`         | bool                                                       |

**Constitutional note**: `Command`'s first element is the absolute
path to the daemon binary. The loader does NOT verify the file
exists or is executable — that is SDD-19's runtime responsibility
per spec Assumptions. `EnvPassthrough` carries env-var NAMES only;
the loader never reads the values. `WorkingDir` is `~`-expanded
but not existence-checked at load time.

---

### `DiscordRouting` (`[discord]` section)

```go
type DiscordRouting struct {
    DaemonLabel    string  // optional; nicer label in DMs and alerts
    AlertChannelID string  // optional; non-secret Discord snowflake
}
```

| Field            | Required | Default | Validation                                        |
|------------------|----------|---------|---------------------------------------------------|
| `DaemonLabel`    | no       | `""`    | none                                              |
| `AlertChannelID` | no       | `""`    | none (downstream verifies snowflake shape)        |

**Constitutional note**: `AlertChannelID` is a non-secret Discord
snowflake (a numeric ID published in Discord's UI). Carrying it in
the loaded config does not violate Principle X. There is NO
`bot_token_keychain_item` field on the supervisor config — the
supervisor's Discord access goes through the same keychain item
the server (SDD-06) configures, not a per-supervisor token.

---

### `Validator` (typedef)

```go
type Validator string
```

`Validator` is a string typedef whose values are constrained by
the package-level `validatorAllowList`. The set is exactly:
- `"anthropic"`
- `"anthropic-oauth"`
- `"openai"`
- `"google-ai"`
- `"github"`

A `Validator` value held by a successfully loaded `*Supervisor` is
guaranteed to be in this set (SC-005, asserted by
`TestSuperviseConfig_LoadedConfigContainsOnlyAllowListedValidators`).
The typedef carries no methods in v0.1.0 — its existence is the
type-narrow signal that the value is constrained.

---

### `Watchdog` (`[watchdog]` section)

```go
type Watchdog struct {
    Enabled          bool
    Patterns         []string
    MaxAlertsPerHour int
}
```

| Field              | Required | Default                                              | Validation rule           |
|--------------------|----------|------------------------------------------------------|---------------------------|
| `Enabled`          | no       | `DefaultWatchdogEnabled = true`                      | bool                      |
| `Patterns`         | no       | `DefaultWatchdogPatterns = []string{}`               | string slice              |
| `MaxAlertsPerHour` | no       | `DefaultWatchdogMaxAlertsPerHour = 6`                | int > 0                   |

**Constitutional note**: A missing `[watchdog]` section is
semantically equivalent to a present-but-empty section
(Clarification 4): every field gets its documented default.
Operators disable the watchdog by writing `[watchdog] enabled =
false` explicitly. `Patterns` is initialised to a non-nil empty
slice so JSON / log marshallers render `[]` rather than `null`.

---

## Internal wire-shape (decoded)

The decoded shape mirrors the public shape but uses pointer /
empty-string sentinels to distinguish "absent in TOML" from "set
to zero":

```go
type supervisorDecoded struct {
    Name                   string             `toml:"name"`
    Reason                 string             `toml:"reason"`
    ServerURL              string             `toml:"server_url"`
    ClientMachineIndex     *uint32            `toml:"client_machine_index"`
    SessionType            string             `toml:"session_type"`
    RequestedTTL           string             `toml:"requested_ttl"`
    RefreshWindow          string             `toml:"refresh_window"`
    RefreshNudgeBefore     string             `toml:"refresh_nudge_before"`
    BootRetryTimeout       string             `toml:"boot_retry_timeout"`
    CacheSecretsForRestart *bool              `toml:"cache_secrets_for_restart"`
    CacheGraceTTL          *string            `toml:"cache_grace_ttl"`
    StatusSocket           string             `toml:"status_socket"`
    PIDFile                string             `toml:"pid_file"`
    LogLevel               string             `toml:"log_level"`
    Scope                  []string           `toml:"scope"`

    Child      childDecoded       `toml:"child"`
    Discord    discordDecoded     `toml:"discord"`
    Validators map[string]string  `toml:"validators"`
    Watchdog   *watchdogDecoded   `toml:"watchdog"`
}

type childDecoded struct {
    Command            []string `toml:"command"`
    WorkingDir         string   `toml:"working_dir"`
    EnvPassthrough     []string `toml:"env_passthrough"`
    RestartOnCleanExit *bool    `toml:"restart_on_clean_exit"`
    RestartOnExit78    *bool    `toml:"restart_on_exit_78"`
}

type discordDecoded struct {
    DaemonLabel    string `toml:"daemon_label"`
    AlertChannelID string `toml:"alert_channel_id"`
}

type watchdogDecoded struct {
    Enabled          *bool    `toml:"enabled"`
    Patterns         []string `toml:"patterns"`
    MaxAlertsPerHour *int     `toml:"max_alerts_per_hour"`
}
```

Pointer-discriminator rules:
- `*uint32 ClientMachineIndex`: nil means "operator did not set
  this required field" → `ErrMissingRequiredField`.
- `*bool CacheSecretsForRestart`: nil means "default applies"
  (false). Non-nil + `*ptr == false` AND `CacheGraceTTL != nil` →
  `ErrGraceTTLWithoutCache` (R-005).
- `*string CacheGraceTTL`: nil means "default applies" (60m if
  cache enabled, 0 if cache disabled). Non-nil triggers the
  contradiction guard.
- `*bool RestartOnCleanExit` / `*bool RestartOnExit78`: nil means
  "default applies" (true / false respectively).
- `*watchdogDecoded Watchdog`: nil means "section absent"; the
  materializer constructs an empty `watchdogDecoded` and proceeds
  with default-application as if every watchdog field were absent.
- `*bool Watchdog.Enabled`: nil means "default applies" (true).
- `*int Watchdog.MaxAlertsPerHour`: nil means "default applies" (6).

---

## Defaults catalogue

The defaults catalogue lives in `defaults.go` as set-once
package-level `var`s. Every value below MUST exactly equal the
corresponding documented default in `docs/CONFIG-SCHEMA.md`
Supervisor section; each is asserted by a unit test (FR-016 +
SC-001).

```go
var (
    DefaultRequestedTTL              = 20 * time.Hour
    DefaultRefreshWindow             = "09:00-10:00"
    DefaultRefreshNudgeBefore        = 30 * time.Minute
    DefaultBootRetryTimeout          = 10 * time.Minute
    DefaultCacheSecretsForRestart    = false
    DefaultGraceWindow               = 60 * time.Minute
    DefaultLogLevel                  = "info"
    DefaultRestartOnCleanExit        = true
    DefaultRestartOnExit78           = false
    DefaultWatchdogEnabled           = true
    DefaultWatchdogMaxAlertsPerHour  = 6
    DefaultWatchdogPatterns          = []string{}
    DefaultDMRateLimit               = 5 * time.Minute  // forwarded to discord.BotConfig

    MaxGraceWindow    = 4 * time.Hour    // constitutional cap (Principle IV)
    MaxRequestedTTL   = 24 * time.Hour   // documented v0.1.0 ceiling
)

// validatorAllowList is the fixed set of validator type names
// accepted in the [validators] map values (Constitution V).
var validatorAllowList = map[string]struct{}{
    "anthropic":       {},
    "anthropic-oauth": {},
    "openai":          {},
    "google-ai":       {},
    "github":          {},
}
```

`DefaultDMRateLimit` is included as a forwarded default that
downstream `internal/discord` (SDD-11) consumes via
`BotConfig.DMRateLimit`. It is NOT a TOML field on the supervisor
config in v0.1.0 — included here so any future schema extension
that exposes the value has a single source of truth.

---

## Sentinel error catalogue

The sentinel error catalogue lives in `errors.go`. Every
documented rejection category from the spec maps to exactly one
sentinel; `errors.Is` is the only matching primitive.

```go
// Decode-phase errors.
var (
    ErrTOMLDecode           = errors.New("hush/supervise/config: TOML decode failed")
    ErrUnknownField         = errors.New("hush/supervise/config: unknown field")
    ErrMissingRequiredField = errors.New("hush/supervise/config: missing required field")
    ErrInvalidDuration      = errors.New("hush/supervise/config: invalid duration")
)

// Validator allow-list.
var ErrUnknownValidator = errors.New("hush/supervise/config: unknown validator")

// Grace-cache errors.
var (
    ErrGraceWindowTooLong   = errors.New("hush/supervise/config: grace window exceeds 4h cap")
    ErrGraceTTLWithoutCache = errors.New("hush/supervise/config: cache_grace_ttl set but cache_secrets_for_restart is false")
)

// Refresh-window errors.
var (
    ErrRefreshWindowFormat = errors.New("hush/supervise/config: refresh_window must be HH:MM-HH:MM")
    ErrRefreshWindowOrder  = errors.New("hush/supervise/config: refresh_window start must be earlier than end")
)

// Child-command errors.
var (
    ErrCommandEmpty        = errors.New("hush/supervise/config: child.command must be a non-empty array")
    ErrCommandPathRelative = errors.New("hush/supervise/config: child.command first element must be an absolute path")
)

// Misc value-range errors.
var (
    ErrScopeEmpty             = errors.New("hush/supervise/config: scope must be a non-empty array")
    ErrSessionTypeInvalid     = errors.New("hush/supervise/config: session_type must be \"supervisor\"")
    ErrRequestedTTLOutOfRange = errors.New("hush/supervise/config: requested_ttl exceeds 24h ceiling")
    ErrServerURLInvalid       = errors.New("hush/supervise/config: server_url must parse with http/https scheme and non-empty host")
    ErrLogLevelInvalid        = errors.New("hush/supervise/config: log_level must be one of debug, info, warn, error")
    ErrWatchdogRateInvalid    = errors.New("hush/supervise/config: watchdog.max_alerts_per_hour must be > 0")
)
```

Every sentinel error message is a static category string. No
sentinel error message includes any byte read from the TOML file
beyond the field NAME (e.g., `child.command`) or the validator
TYPE NAME (e.g., `"slack"`); the LHS secret name in `[validators]`
is NEVER reproduced in error output (FR-014 + FR-020).

---

## Validation rule index

The full validation pipeline runs in this order:

1. **Decode** — `pelletier/go-toml/v2` with `DisallowUnknownFields(true)`.
   Any unknown key → `ErrUnknownField`. Any type mismatch →
   `ErrTOMLDecode`.
2. **Required-field gate** — every field marked "Required" in
   `docs/CONFIG-SCHEMA.md` Supervisor section that is absent from
   the decoded shape → `ErrMissingRequiredField`. Multiple missing
   fields aggregated via `errors.Join`.
3. **Per-field syntactic validation**:
   - `session_type == "supervisor"` → else `ErrSessionTypeInvalid`.
   - `server_url` parses, has non-empty host, scheme http/https →
     else `ErrServerURLInvalid`.
   - `requested_ttl` parses → else `ErrInvalidDuration`. Parsed
     value ≤ 24h → else `ErrRequestedTTLOutOfRange`.
   - `refresh_window` matches `HH:MM-HH:MM` shape → else
     `ErrRefreshWindowFormat`. Start < end → else
     `ErrRefreshWindowOrder`.
   - `refresh_nudge_before`, `boot_retry_timeout`,
     `cache_grace_ttl` parse as durations → else
     `ErrInvalidDuration`.
   - `log_level` ∈ {debug, info, warn, error} → else
     `ErrLogLevelInvalid`.
   - `scope` non-empty → else `ErrScopeEmpty`.
   - `child.command` non-empty AND `filepath.IsAbs(child.command[0])`
     → else `ErrCommandEmpty` / `ErrCommandPathRelative`.
   - `[validators]` values all in `validatorAllowList` → else
     `ErrUnknownValidator`.
   - `[watchdog].max_alerts_per_hour > 0` → else
     `ErrWatchdogRateInvalid`.
4. **Cross-field validation**:
   - `cache_secrets_for_restart == false` AND `cache_grace_ttl`
     explicitly set → `ErrGraceTTLWithoutCache`.
   - `cache_secrets_for_restart == true` AND `cache_grace_ttl >
     MaxGraceWindow (4h)` → `ErrGraceWindowTooLong`.
5. **Defaults application** — for every absent optional field,
   write the documented default into the public `Supervisor`.
   Materialise scoped types (`Validators` map values cast to
   `Validator`).

A successful return is a `*Supervisor` value with every field
populated to its documented type.

---

## Construction discipline

`Load` opens the file, decodes into the wire-shape, runs the
required-field gate, runs per-field validation, runs cross-field
validation, applies defaults, materialises the public shape, and
returns. Any failure short-circuits with `(nil, err)` — partial
configurations are NEVER returned.

`Supervisor.Validate()` is a separate method that re-runs the
full validation pipeline against an in-memory `Supervisor`. This
exists for testing convenience and for downstream callers who
might construct a `*Supervisor` programmatically (e.g., in tests).
The two entry points share the same rule engine.

The package has no `init()` and no goroutines. `Load` is the only
function that touches the filesystem.
