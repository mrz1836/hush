# Contract: `internal/supervise/config` exported API

**Feature**: 018-supervise-config
**Status**: Locked at SDD-18; mirrored into `docs/PACKAGE-MAP.md`
once the implement commit lands.

This is the contract every downstream supervisor chunk (SDD-19
state machine, SDD-20 child runner, SDD-21 refill/refresh, SDD-22
status socket, SDD-23 CLI orchestrator, SDD-26 validators, SDD-27
watchdog, SDD-28 alerts) depends on. Changes after SDD-18 lands
require a new SDD chunk; consumers may rely on every signature,
every default value, and every sentinel identity below.

SDD-18's symbols are additive — no SDD-06 (`internal/config`)
symbol is altered. The two packages share a TOML decoder
(pelletier/v2 with strict mode) but live in separate import
paths and have no cross-package dependency.

---

## Package path

```
github.com/mrz1836/hush/internal/supervise/config
```

---

## Exported types

### `type Supervisor struct`

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

**Contract**:
- Read-only after `Load` returns. Consumers MUST NOT mutate any
  field, including the slice / map elements.
- No field holds a secret value. The struct's reference fields
  (`Validators` map, `Scope` slice, `Child.EnvPassthrough` slice,
  `Watchdog.Patterns` slice) hold non-secret labels only —
  scoped secret NAMES, validator TYPE NAMES, env-var NAMES,
  log-pattern strings.
- All path-bearing fields are absolute and `~`-expanded.
- All duration-bearing fields are populated `time.Duration` values
  (not strings).
- The `Validators` map's value type `Validator` is constrained:
  every value is in the package-level allow-list.

### `type Child struct`

```go
type Child struct {
    Command            []string
    WorkingDir         string
    EnvPassthrough     []string
    RestartOnCleanExit bool
    RestartOnExit78    bool
}
```

**Contract**:
- `Command` is non-empty; `Command[0]` passes `filepath.IsAbs`.
- `EnvPassthrough` carries env-var NAMES only; the loader never
  reads the values.

### `type DiscordRouting struct`

```go
type DiscordRouting struct {
    DaemonLabel    string
    AlertChannelID string
}
```

**Contract**:
- Both fields are optional; empty string means "not configured".
- `AlertChannelID` is a Discord snowflake (non-secret).

### `type Watchdog struct`

```go
type Watchdog struct {
    Enabled          bool
    Patterns         []string
    MaxAlertsPerHour int
}
```

**Contract**:
- A missing `[watchdog]` section yields `Enabled = true`,
  `Patterns = []string{}` (non-nil empty slice),
  `MaxAlertsPerHour = 6`.
- `MaxAlertsPerHour > 0` after `Validate` returns nil.

### `type Validator string`

```go
type Validator string
```

**Contract**:
- A `Validator` value held by a successfully loaded `*Supervisor`
  is one of: `"anthropic"`, `"anthropic-oauth"`, `"openai"`,
  `"google-ai"`, `"github"`. No other value is reachable through
  the public API.

---

## Exported functions

### `func Load(ctx context.Context, path string) (*Supervisor, error)`

```go
func Load(ctx context.Context, path string) (*Supervisor, error)
```

**Contract**:
- Inspects `ctx.Err()` once at function entry; pre-cancellation
  short-circuits with `ctx.Err()` returned verbatim.
- Opens the file at `path`, decodes via `pelletier/go-toml/v2`
  with `DisallowUnknownFields(true)`, runs the required-field
  gate, runs per-field validation, runs cross-field validation,
  applies defaults, materialises the public shape, and returns.
- On any failure, returns `(nil, err)` where `err` wraps one of
  the package's sentinel errors (or, for filesystem errors,
  wraps the underlying `os` / `fs` error). Partial configurations
  are NEVER returned.
- Idempotent: same input file path → equivalent `*Supervisor`
  across calls, regardless of process environment (modulo
  `$HOME` for `~` path expansion of `status_socket` / `pid_file`,
  which is non-secret per Constitution X).
- Spawns no goroutines, holds no package-level state, performs
  no writes to the filesystem.

### `func (s *Supervisor) Validate() error`

```go
func (s *Supervisor) Validate() error
```

**Contract**:
- Re-runs the full validation pipeline against an in-memory
  `*Supervisor` value. Returns `nil` on success or a wrapped
  sentinel on the first violation; multi-violation reports use
  `errors.Join`.
- Useful for tests that construct a `*Supervisor` programmatically
  and want to verify it would pass `Load`'s gate, and for
  defensive re-validation in downstream chunks.
- Pure function: no I/O, no goroutines, no state.

---

## Default constants

The defaults catalogue lives in `defaults.go`. Every value below
exactly equals the corresponding documented default in
`docs/CONFIG-SCHEMA.md` Supervisor section; each is asserted by a
unit test (FR-016 + SC-001).

```go
var (
    DefaultRequestedTTL              time.Duration  // 20 * time.Hour
    DefaultRefreshWindow             string         // "09:00-10:00"
    DefaultRefreshNudgeBefore        time.Duration  // 30 * time.Minute
    DefaultBootRetryTimeout          time.Duration  // 10 * time.Minute
    DefaultCacheSecretsForRestart    bool           // false
    DefaultGraceWindow               time.Duration  // 60 * time.Minute (cache-enabled)
    DefaultLogLevel                  string         // "info"
    DefaultRestartOnCleanExit        bool           // true
    DefaultRestartOnExit78           bool           // false
    DefaultWatchdogEnabled           bool           // true
    DefaultWatchdogMaxAlertsPerHour  int            // 6
    DefaultWatchdogPatterns          []string       // []string{} (non-nil empty)
    DefaultDMRateLimit               time.Duration  // 5 * time.Minute (forwarded to discord.BotConfig)

    MaxGraceWindow                   time.Duration  // 4 * time.Hour (Constitution IV cap)
    MaxRequestedTTL                  time.Duration  // 24 * time.Hour (v0.1.0 ceiling)
)
```

All `Default*` and `Max*` constants are typed `var`s rather than
Go `const`s because `time.Duration`, `[]string`, and `int` cannot
be grouped under a single `const`. The values are set-once at
package load and never mutated; they live in the same
constitutional class as `var Err... = errors.New(...)`
declarations (Constitution IX, sentinel-class).

The package-level `validatorAllowList map[string]struct{}` is
unexported; downstream consumers do not need to read it because
the `Validator` typedef on the public struct already guarantees
membership.

---

## Sentinel error catalogue

Every documented rejection category from the spec maps to exactly
one sentinel; `errors.Is` is the only matching primitive.
Sentinel error messages are static category strings; no message
includes any byte read from the TOML file beyond the field NAME
or the validator TYPE NAME.

```go
// Decode-phase errors.
var (
    ErrTOMLDecode           error // wraps pelletier/v2 inner error; type-mismatch / syntax
    ErrUnknownField         error // pelletier/v2 strict-mode error; unknown / misspelled key
    ErrMissingRequiredField error // absent required field after decode; carries dotted field path
    ErrInvalidDuration      error // time.ParseDuration failure on any duration field
)

// Validator allow-list.
var ErrUnknownValidator    error // [validators] value not in allow-list; carries validator name only

// Grace-cache errors.
var (
    ErrGraceWindowTooLong   error // cache_grace_ttl > MaxGraceWindow (4h)
    ErrGraceTTLWithoutCache error // cache_grace_ttl set but cache_secrets_for_restart is false
)

// Refresh-window errors.
var (
    ErrRefreshWindowFormat error // refresh_window does not match HH:MM-HH:MM
    ErrRefreshWindowOrder  error // refresh_window start >= end (incl. wrap-around)
)

// Child-command errors.
var (
    ErrCommandEmpty        error // [child].command is an empty array
    ErrCommandPathRelative error // [child].command first element is not absolute
)

// Misc value-range errors.
var (
    ErrScopeEmpty             error // top-level scope absent or empty array
    ErrSessionTypeInvalid     error // session_type ≠ "supervisor"
    ErrRequestedTTLOutOfRange error // requested_ttl > MaxRequestedTTL (24h)
    ErrServerURLInvalid       error // server_url empty / unparseable / wrong scheme / empty host
    ErrLogLevelInvalid        error // log_level not in {debug, info, warn, error}
    ErrWatchdogRateInvalid    error // watchdog.max_alerts_per_hour ≤ 0
)
```

Wrap relationships (asserted by `errors.Is` self-tests):
- `ErrUnknownField` is reachable via `errors.Is` from any decoder
  error wrapping a `*toml.StrictMissingError`.
- `ErrTOMLDecode` is reachable via `errors.Is` from any decoder
  error not matching `*toml.StrictMissingError`.
- `ErrServerURLInvalid` does NOT wrap the underlying parser
  error; the parser error message can leak operator-typed bytes,
  and the loader's diagnostic value (which of the four categories
  triggered) is conveyed by the wrapping message text alone.
- `ErrUnknownValidator` wrapping format: `fmt.Errorf("hush/supervise/config:
  unknown validator %q: %w", name, ErrUnknownValidator)`. The
  `%q` produces a Go-quoted version of the validator name only
  (e.g., `"slack"`). The LHS secret name is NEVER included.
- `ErrMissingRequiredField` wrapping format: `fmt.Errorf("hush/supervise/config:
  missing required field %s: %w", path, ErrMissingRequiredField)`,
  where `path` is the dotted TOML coordinate (e.g.,
  `child.command`).

Multi-violation reports use `errors.Join`; every constituent
error is individually matchable via `errors.Is`.

---

## Behavioural invariants (asserted by tests)

1. **No init**: the package has no `init()` function. Asserted by
   `TestPackage_NoInit` (grep-style guard).
2. **No goroutines**: `Load` and `Validate` spawn no goroutines.
   Asserted by `TestLoad_NoGoroutineLeak` (`runtime.NumGoroutine`
   diff before / after).
3. **No env reads for secret-bearing fields**: setting
   `HUSH_DISCORD_TOKEN`, `HUSH_VAULT_PASSPHRASE`, `HUSH_REASON`,
   `HUSH_REFRESH_WINDOW` does not affect the loaded config.
   Asserted by `TestLoad_DoesNotReadSecretsFromEnv`.
4. **No secret material in errors**: every sentinel error message
   is a static category string; `ErrUnknownValidator`'s wrapping
   includes only the RHS validator name, never the LHS secret
   name. Asserted by
   `TestErrUnknownValidator_DoesNotIncludeSecretMaterial`.
5. **Allow-list enforcement**: a successfully loaded config holds
   only allow-listed validator values. Asserted by
   `TestSuperviseConfig_LoadedConfigContainsOnlyAllowListedValidators`.
6. **Idempotency**: two calls to `Load` with the same path
   produce equivalent `*Supervisor` values
   (reflect.DeepEqual-true). Asserted by `TestLoad_Idempotent`.
7. **Defaults parity**: every documented default equals the
   value the loader applies for an absent field. Asserted by
   ~13 individual `TestSuperviseConfig_Default*` tests.
8. **Fuzz contract**: `FuzzSuperviseTOML` runs ≥60 s clean — no
   panic, no untyped error, no unbounded memory growth.

---

## Forward compatibility

SDD-19..23 + SDD-26..28 may add types and helpers under
`internal/supervise/` (sibling sub-packages or files). Those
chunks MUST NOT alter any symbol locked above. If a future chunk
needs to extend the `Supervisor` struct (e.g., to surface a new
optional field), the chunk MUST go through its own SDD lifecycle:
spec → clarify → plan → tasks → implement.

The package's import path is `github.com/mrz1836/hush/internal/supervise/config`;
it is INTERNAL to the hush module. No external consumer is part
of any contract.
