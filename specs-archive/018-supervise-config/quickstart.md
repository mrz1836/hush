# Quickstart: `internal/supervise/config`

**Audience**: SDD-19 (state machine), SDD-20 (child runner), SDD-21
(refill/refresh), SDD-22 (status socket), SDD-23 (`hush supervise`
CLI), SDD-26 (validators), SDD-27 (watchdog), SDD-28 (alerts), and
any future agent reading a `~/.hush/supervisors/<name>.toml`.
**Last updated**: 2026-05-05 (Phase 1 of SDD-18)

This is the operational cheat-sheet for loading a per-supervisor
config. The contract and rationale live in
[contracts/api.md](./contracts/api.md), [data-model.md](./data-model.md),
and [research.md](./research.md); this file shows how to wire the
loader into a startup path and how to react to each documented
failure.

---

## 1. Load the config

```go
import (
    "context"
    "fmt"

    superviseconfig "github.com/mrz1836/hush/internal/supervise/config"
)

func loadSupervisor(ctx context.Context, path string) (*superviseconfig.Supervisor, error) {
    s, err := superviseconfig.Load(ctx, path)
    if err != nil {
        return nil, fmt.Errorf("load supervisor config %q: %w", path, err)
    }
    return s, nil
}
```

Every absent optional field is populated from the
[defaults catalogue](./contracts/api.md#default-constants); every
duration is parsed; every path-bearing field is `~`-expanded and
absolute. The returned `*Supervisor` is read-only — pass it by
pointer; do not mutate it.

The `path` argument is typically
`~/.hush/supervisors/<name>.toml`, but the loader does not care
where the file lives. SDD-23 (`hush supervise --config <path>`)
resolves the path from the operator's flag and forwards it
verbatim.

---

## 2. React to each documented failure

Every error from `Load` is matchable via `errors.Is`. The
recommended startup path checks for the operator-actionable
categories first (so the operator gets the most useful message)
and falls back to a generic `unknown error` print.

```go
s, err := superviseconfig.Load(ctx, path)
if err == nil {
    return s, nil
}

switch {
case errors.Is(err, superviseconfig.ErrUnknownField):
    fmt.Fprintln(os.Stderr, "supervisor config has an unknown field — did you misspell something?")
case errors.Is(err, superviseconfig.ErrMissingRequiredField):
    fmt.Fprintln(os.Stderr, "supervisor config is missing a required field")
case errors.Is(err, superviseconfig.ErrUnknownValidator):
    fmt.Fprintln(os.Stderr, "[validators] contains an unsupported validator type — see docs/CONFIG-SCHEMA.md for the allow-list")
case errors.Is(err, superviseconfig.ErrGraceWindowTooLong):
    fmt.Fprintln(os.Stderr, "cache_grace_ttl exceeds the 4h cap")
case errors.Is(err, superviseconfig.ErrGraceTTLWithoutCache):
    fmt.Fprintln(os.Stderr, "cache_grace_ttl set but cache_secrets_for_restart is false; remove one or the other")
case errors.Is(err, superviseconfig.ErrRefreshWindowFormat):
    fmt.Fprintln(os.Stderr, "refresh_window must be HH:MM-HH:MM (e.g., 09:00-10:00)")
case errors.Is(err, superviseconfig.ErrRefreshWindowOrder):
    fmt.Fprintln(os.Stderr, "refresh_window start must be earlier than end (no wrap-around in v0.1.0)")
case errors.Is(err, superviseconfig.ErrCommandEmpty):
    fmt.Fprintln(os.Stderr, "[child].command must be a non-empty array")
case errors.Is(err, superviseconfig.ErrCommandPathRelative):
    fmt.Fprintln(os.Stderr, "[child].command first element must be an absolute path (no PATH lookup)")
case errors.Is(err, superviseconfig.ErrScopeEmpty):
    fmt.Fprintln(os.Stderr, "scope must be a non-empty array")
case errors.Is(err, superviseconfig.ErrSessionTypeInvalid):
    fmt.Fprintln(os.Stderr, "session_type must be exactly \"supervisor\"")
case errors.Is(err, superviseconfig.ErrRequestedTTLOutOfRange):
    fmt.Fprintln(os.Stderr, "requested_ttl exceeds the v0.1.0 24h ceiling")
case errors.Is(err, superviseconfig.ErrServerURLInvalid):
    fmt.Fprintln(os.Stderr, "server_url must be http://… or https://… with a non-empty host")
case errors.Is(err, superviseconfig.ErrLogLevelInvalid):
    fmt.Fprintln(os.Stderr, "log_level must be one of debug, info, warn, error")
case errors.Is(err, superviseconfig.ErrWatchdogRateInvalid):
    fmt.Fprintln(os.Stderr, "[watchdog].max_alerts_per_hour must be > 0")
case errors.Is(err, superviseconfig.ErrInvalidDuration):
    fmt.Fprintln(os.Stderr, "a duration field could not be parsed")
case errors.Is(err, superviseconfig.ErrTOMLDecode):
    fmt.Fprintln(os.Stderr, "supervisor config is not valid TOML")
default:
    fmt.Fprintf(os.Stderr, "supervisor config load failed: %v\n", err)
}
return nil, err
```

The full sentinel catalogue is in
[contracts/api.md](./contracts/api.md#sentinel-error-catalogue).
Multi-violation reports are `errors.Join`-style: every sentinel
matchable individually.

---

## 3. Reach into the loaded config

The struct is plain data. Typical SDD-19 (state machine) wiring:

```go
// Resolve the daemon binary.
binary := s.Child.Command[0]                // already absolute (Validate guarantees it)
args := s.Child.Command[1:]
workingDir := s.Child.WorkingDir
envPassthrough := s.Child.EnvPassthrough    // env-var NAMES only

// Resolve the session contract.
sessionTTL := s.RequestedTTL                 // ≤ MaxRequestedTTL (24h)
graceWindow := s.CacheGraceTTL               // 0 if cache disabled; ≤ MaxGraceWindow (4h) otherwise
cacheEnabled := s.CacheSecretsForRestart

// Resolve the refresh-window scheduler.
window := s.RefreshWindow                    // canonical "HH:MM-HH:MM"
nudgeBefore := s.RefreshNudgeBefore

// Resolve validators.
for secretName, validatorType := range s.Validators {
    // validatorType is one of the allow-listed strings; switch by value.
    switch validatorType {
    case "anthropic":
        // …
    case "anthropic-oauth":
        // …
    case "openai":
        // …
    case "google-ai":
        // …
    case "github":
        // …
    }
    _ = secretName
}

// Resolve the local status socket / pid file.
sockPath := s.StatusSocket                   // absolute, ~-expanded
pidPath  := s.PIDFile                        // absolute, ~-expanded

// Resolve the watchdog.
if s.Watchdog.Enabled {
    rate := s.Watchdog.MaxAlertsPerHour      // > 0
    patterns := s.Watchdog.Patterns          // []string{} when none configured
    _ = rate
    _ = patterns
}
```

---

## 4. The `Validator` typedef

`s.Validators` has type `map[string]Validator`. The map's keys are
scoped secret NAMES (e.g., `"ANTHROPIC_API_KEY"`); the values are
typed `Validator` strings constrained to the allow-list. A
downstream consumer that wants to pattern-match on the value must
compare against one of the five literal strings, since `Validator`
is a string typedef:

```go
const validatorAnthropic   superviseconfig.Validator = "anthropic"
const validatorAnthropicO  superviseconfig.Validator = "anthropic-oauth"
const validatorOpenAI      superviseconfig.Validator = "openai"
const validatorGoogleAI    superviseconfig.Validator = "google-ai"
const validatorGitHub      superviseconfig.Validator = "github"

if v, ok := s.Validators[name]; ok {
    switch v {
    case validatorAnthropic:
        // run the anthropic validator
    case validatorOpenAI:
        // run the openai validator
    // …
    }
}
```

The package does NOT export these literal constants in v0.1.0 — the
allow-list is private; downstream code can declare its own typed
constants as shown. SDD-26 (validators chunk) will likely export
its own `ValidatorType` constants when the validators are
implemented.

---

## 5. Re-validating a programmatic `Supervisor`

Tests and downstream chunks may construct a `*Supervisor` value
in-memory (e.g., to inject a fixture into the state machine). To
verify the constructed value would pass the loader's gate, call
`Validate()`:

```go
s := &superviseconfig.Supervisor{
    Name:           "example",
    Reason:         "test fixture",
    SessionType:    "supervisor",
    RequestedTTL:   2 * time.Hour,
    RefreshWindow:  "09:00-10:00",
    // …
}
if err := s.Validate(); err != nil {
    t.Fatalf("fixture invalid: %v", err)
}
```

`Validate` is pure: no I/O, no goroutines, no state. Idempotent
across calls.

---

## 6. Error-message hygiene (Constitution X)

The loader's sentinel error messages are static category strings.
The two sentinels that include operator-typed material —
`ErrUnknownField` and `ErrUnknownValidator` — include only the
field path / validator name; never any byte from a value. When
relaying load errors to the operator, the recommended pattern is
to print the matched sentinel's category message and let
`%v`-formatted underlying detail land in slog:

```go
if err != nil {
    slog.Error("supervisor config load failed",
        "path", path,
        "err", err,
    )
    // user-facing message uses the sentinel category; never %v on err.
    fmt.Fprintln(os.Stderr, "supervisor config rejected — see logs for details")
    return err
}
```

`SecureBytes` is NOT used by this package — there is no secret
material in the supervisor config — but the redaction discipline
of "no token-shaped string in error output" still applies.

---

## 7. Where this fits in the supervisor lifecycle

```
┌─────────────────────────────────────────────────────────────────┐
│ SDD-18 (this chunk)        Load + Validate per-supervisor TOML  │
└─────────────────────┬───────────────────────────────────────────┘
                      ▼
┌─────────────────────────────────────────────────────────────────┐
│ SDD-19 supervise/state     state machine consuming *Supervisor  │
└─────────────────────┬───────────────────────────────────────────┘
                      ▼
┌─────────────────────────────────────────────────────────────────┐
│ SDD-20 supervise/child     fork/exec Command, inject scope vars │
└─────────────────────┬───────────────────────────────────────────┘
                      ▼
┌─────────────────────────────────────────────────────────────────┐
│ SDD-21 supervise/refill    silent refill + refresh-window prompt│
└─────────────────────┬───────────────────────────────────────────┘
                      ▼
┌─────────────────────────────────────────────────────────────────┐
│ SDD-22 supervise/status    Unix status socket; pid_file flock   │
└─────────────────────┬───────────────────────────────────────────┘
                      ▼
┌─────────────────────────────────────────────────────────────────┐
│ SDD-23 cli/supervise       cobra command; calls Load and runs   │
└─────────────────────────────────────────────────────────────────┘
```

SDD-18 is a leaf producer: it imports nothing from intra-repo and
is imported by every supervisor chunk listed above.

---

## 8. Common operator mistakes and what they look like

| Mistake | Sentinel | Operator-facing message |
|---------|----------|------------------------|
| Misspelled field (`refrsh_window`) | `ErrUnknownField` | "supervisor config has an unknown field — did you misspell something?" |
| Forgot `name` field | `ErrMissingRequiredField` | "supervisor config is missing a required field" |
| Used `slack` validator | `ErrUnknownValidator` | "[validators] contains an unsupported validator type" |
| Set `cache_grace_ttl = "6h"` | `ErrGraceWindowTooLong` | "cache_grace_ttl exceeds the 4h cap" |
| Set `cache_grace_ttl = "1h"` with cache disabled | `ErrGraceTTLWithoutCache` | "remove one or the other" |
| Wrote `refresh_window = "9-10"` | `ErrRefreshWindowFormat` | "must be HH:MM-HH:MM" |
| Wrote `refresh_window = "10:00-09:00"` | `ErrRefreshWindowOrder` | "start must be earlier than end" |
| Wrote `command = ["my-daemon", "start"]` | `ErrCommandPathRelative` | "must be an absolute path (no PATH lookup)" |
| Wrote `command = []` | `ErrCommandEmpty` | "must be a non-empty array" |
| Wrote `scope = []` or omitted scope | `ErrScopeEmpty` | "must be a non-empty array" |
| Wrote `session_type = "interactive"` | `ErrSessionTypeInvalid` | "must be exactly \"supervisor\"" |
| Wrote `requested_ttl = "25h"` | `ErrRequestedTTLOutOfRange` | "exceeds the v0.1.0 24h ceiling" |
| Wrote `server_url = "100.64.0.1:7743/h/x"` (missing scheme) | `ErrServerURLInvalid` | "must be http:// or https:// with a non-empty host" |
| Wrote `log_level = "trace"` | `ErrLogLevelInvalid` | "must be one of debug, info, warn, error" |
| Wrote `[watchdog] max_alerts_per_hour = 0` | `ErrWatchdogRateInvalid` | "must be > 0" |

Every entry in the table above is exercised by a corresponding
unit test in `validate_test.go`.
