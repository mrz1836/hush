# Implementation Plan: `internal/supervise/config` — Per-Supervisor TOML Schema + Validation (SDD-18)

**Branch**: `018-supervise-config` | **Date**: 2026-05-05 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/018-supervise-config/spec.md`
**Chunk contract**: [docs/sdd/SDD-18.md](../../docs/sdd/SDD-18.md)

## Summary

`internal/supervise/config` owns the per-supervisor TOML configuration:
the daemon's identity, child command vector, `[validators]` allow-list,
optional grace cache window, refresh-window scheduler, watchdog
patterns, and Discord routing. The package is loaded once per
supervisor process at startup; every downstream supervisor chunk
(SDD-19 state machine, SDD-20 child runner, SDD-21 refill/refresh,
SDD-22 status socket, SDD-23 CLI orchestrator, SDD-26 validators,
SDD-27 watchdog, SDD-28 alerts) consumes the loaded `*Supervisor`.

A clean load is the contract every later supervisor path depends on
— a loadable-but-unsafe supervisor config is the threat shape this
package exists to make impossible. Specifically: a 6-hour grace
cache turns one Discord approval into a multi-shift access window
(Constitution IV); an unrecognised validator name silently disables
staleness detection on the scoped secret (Constitution V, the
canonical 2026-04-04 Mini-Zai failure mode); a relative
`[child].command` first element re-introduces the `PATH`-hijack
attack surface that absolute paths exist to remove.

Approach (locked by SDD-18 + Constitution IV/V/VIII/IX/X/XI; not
subject to research alternatives):

- **TOML decode** via `github.com/pelletier/go-toml/v2` (the
  decoder already locked by SDD-06; no new direct dep) with
  `Decoder.DisallowUnknownFields(true)`. Unknown / misspelled keys
  in any of the documented sections (root, `[child]`, `[discord]`,
  `[validators]`, `[watchdog]`) produce `ErrUnknownField` before any
  other validation runs (FR-002).
- **Two-struct decode pipeline** (`supervisorDecoded` →
  `Supervisor`): the wire-shape struct uses pointer / sentinel types
  where "absent vs zero" matters (`*bool` for booleans, `*int` for
  counters, `string` for durations / refresh-window with empty-
  string sentinels), so the materializer can apply the exact
  documented default for every absent optional field per FR-016. The
  public `Supervisor` struct uses concrete types (`bool`,
  `time.Duration`, `[]string`, `map[string]Validator`).
- **Validator allow-list** is a package-level
  `map[string]struct{}` populated from a single
  `validatorAllowList` set: `{anthropic, anthropic-oauth, openai,
  google-ai, github}` (FR-003). Materialisation iterates each
  declared `[validators]` entry and rejects unknown values with
  `ErrUnknownValidator` carrying the offending validator name only
  (NOT the value/secret name on the left-hand side, since FR-014 +
  FR-020 forbid any token-shaped string in error messages).
- **Grace-window cap** is `4 * time.Hour`, exposed as the
  `MaxGraceWindow` constant. `cache_grace_ttl` parses via
  `time.ParseDuration`; `> MaxGraceWindow` produces
  `ErrGraceWindowTooLong` (FR-004). Absent applies
  `DefaultGraceWindow = 60 * time.Minute` (per
  `docs/CONFIG-SCHEMA.md`).
- **Grace-cache contradiction guard**: if
  `cache_secrets_for_restart = false` (or absent) but
  `cache_grace_ttl` is explicitly set in the TOML, the loader returns
  `ErrGraceTTLWithoutCache` per FR-011 + Clarification 3 (Session
  2026-05-05). The decoded shape distinguishes "absent" from "set to
  zero" via a `*string` pointer for the duration field.
- **Refresh-window parser**: `refresh_window` is split on the single
  `-` separator. Each side parses via `time.Parse("15:04", side)`.
  Format violations (missing dash, missing colon, non-numeric, hour
  / minute out of range) produce `ErrRefreshWindowFormat` (FR-005).
  Format-clean but `start >= end` (or wrap-around) produces a
  distinct `ErrRefreshWindowOrder` (FR-006). Both errors are
  separately matchable per the spec's "two distinct rejection
  categories" rule.
- **Child command shape**: `[child].command` MUST be a non-empty
  string array. Empty array → `ErrCommandEmpty`. First element
  failing `filepath.IsAbs` → `ErrCommandPathRelative`. Both are
  distinct sentinels per FR-007. Subsequent elements are passed
  through verbatim — the loader never quotes, splits, or interprets
  them.
- **Scope guard**: top-level `scope` MUST be a non-empty `[]string`.
  Absence and emptiness are equivalent (FR-008) and produce
  `ErrScopeEmpty`.
- **Session-type guard**: `session_type` MUST be the literal string
  `"supervisor"` (FR-009); any other value → `ErrSessionTypeInvalid`.
- **TTL ceiling**: `requested_ttl` parses via `time.ParseDuration`;
  any value > `24*time.Hour` (the documented v0.1.0 supervisor TTL
  ceiling per `docs/CONFIG-SCHEMA.md` `max_supervisor_ttl` upper
  bound, codified as `MaxRequestedTTL`) produces
  `ErrRequestedTTLOutOfRange` (FR-010 + Clarification 1). The
  loader does NOT consult any server-side config; the stricter
  server-side ceiling is enforced at claim time per the spec
  clarification.
- **`server_url` syntax**: parses via `url.Parse`; reject empty,
  parse-error, empty `Host`, or any scheme other than `http` /
  `https` with `ErrServerURLInvalid` (FR-013a + Clarification 5).
  Tailscale CIDR / port / path-prefix membership is deferred to
  downstream supervisor startup hardening per the clarification.
- **`log_level` allow-list**: `{debug, info, warn, error}`; any
  other value → `ErrLogLevelInvalid` (FR-013); absent → `"info"`
  (`DefaultLogLevel`).
- **Watchdog defaults**: a missing `[watchdog]` section is
  equivalent to all watchdog fields absent (FR-016 +
  Clarification 4): `enabled = true`, `max_alerts_per_hour = 6`,
  `patterns = []`. `max_alerts_per_hour <= 0` → `ErrWatchdogRateInvalid`
  (FR-012).
- **Required-field gate**: every documented required field
  (`name`, `reason`, `server_url`, `client_machine_index`,
  `session_type`, `requested_ttl`, `refresh_window`,
  `status_socket`, `pid_file`, `[child].command`, `scope`,
  `[validators]`) absent → `ErrMissingRequiredField` carrying the
  field path. The check runs after decode but before any other
  rule, so a missing-field error short-circuits before mistypes.
- **Defaults applied AFTER decode** for every absent optional
  field per FR-016. The applied default exactly equals the value
  documented in `docs/CONFIG-SCHEMA.md` Supervisor section; every
  default is asserted by a corresponding test.
- **No environment-variable reads** for any supervisor field
  (FR-015). The same TOML file produces equivalent loaded
  configurations regardless of the calling process's environment.
  `os.UserHomeDir` is the only env-touching call (used solely for
  `~` path expansion of `status_socket` / `pid_file`); `$HOME` is
  non-secret per Constitution X.
- **No `init()`** function exists in the package (Constitution IX).
  `validatorAllowList`, `MaxGraceWindow`, `MaxRequestedTTL`, and
  every `Default*` are exported package-level set-once `var`s in
  the same constitutional class as SDD-06's defaults.
- **Fuzz target `FuzzSuperviseTOML`** feeds random byte streams to
  `Load`. Contract: no panic, no unbounded allocation, every
  returned error is one of the named sentinels (or wraps one).

Exported API (locked at SDD-18; mirrored into `docs/PACKAGE-MAP.md`
once the implement commit lands; SDD-18's symbols are additive — no
SDD-06 symbol is altered):

```go
type Supervisor struct { /* fields per docs/CONFIG-SCHEMA.md Supervisor section */ }
type Child struct      { /* [child] section */ }
type DiscordRouting struct { /* [discord] section */ }
type Watchdog struct   { /* [watchdog] section */ }
type Validator string  // values constrained by validatorAllowList

func Load(ctx context.Context, path string) (*Supervisor, error)
func (s *Supervisor) Validate() error

// Default constants — every documented default has one entry.
var DefaultGraceWindow, DefaultRefreshWindow,
    DefaultBootRetryTimeout, DefaultDMRateLimit,
    DefaultRefreshNudgeBefore, DefaultRequestedTTL,
    DefaultCacheSecretsForRestart, DefaultRestartOnCleanExit,
    DefaultRestartOnExit78, DefaultLogLevel,
    DefaultWatchdogEnabled, DefaultWatchdogMaxAlertsPerHour,
    DefaultWatchdogPatterns,
    MaxGraceWindow, MaxRequestedTTL

// Sentinel errors — full list in contracts/api.md.
var ErrTOMLDecode, ErrUnknownField, ErrMissingRequiredField,
    ErrInvalidDuration, ErrUnknownValidator,
    ErrGraceWindowTooLong, ErrGraceTTLWithoutCache,
    ErrRefreshWindowFormat, ErrRefreshWindowOrder,
    ErrCommandEmpty, ErrCommandPathRelative,
    ErrScopeEmpty, ErrSessionTypeInvalid,
    ErrRequestedTTLOutOfRange, ErrServerURLInvalid,
    ErrLogLevelInvalid, ErrWatchdogRateInvalid
```

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`); `CGO_ENABLED=0`
(Constitution IX).

**Primary Dependencies**:
- Go stdlib: `context`, `errors`, `fmt`, `net/url`, `os`,
  `path/filepath`, `sort`, `strings`, `time`.
- Existing direct dep (locked by SDD-06; no new dep introduced):
  `github.com/pelletier/go-toml/v2 v2.3.1`. SDD-18 reuses the
  decoder choice, the `DisallowUnknownFields(true)` configuration,
  and the strict-decode error mapping pattern. No additional TOML
  library is added; no other third-party package is added.
- Intra-repo: NONE at load-time. The package is a leaf consumer.
  It produces a `*Supervisor` value that downstream supervisor
  chunks (SDD-19..23, 26..28) consume; it imports nothing from
  `internal/keys`, `internal/vault`, `internal/logging`,
  `internal/config`, or any other intra-repo package.

**Storage**: read-only — opens the supplied file path, decodes,
closes. No writes, no temp files, no caches. Idempotent across
calls (FR-019): same input → same output regardless of the calling
process's environment, modulo `$HOME` for `~` expansion of path
fields (non-secret, FR-015 spirit preserved).

**Testing**: `go test ./internal/supervise/config/...` (table-driven
unit tests per `.github/tech-conventions/testing-standards.md`);
`magex test:race` race-clean; `go test -fuzz=FuzzSuperviseTOML
-fuzztime=60s ./internal/supervise/config/` with no panics and no
new corpus rows representing crashes. Coverage measured via
`go test -cover ./internal/supervise/config/`; target ≥95% per
Constitution VIII (High-priority band: supervisor state machine +
validators; the supervisor-config gateway sits in this band).

**Target Platform**: macOS (darwin amd64/arm64) and Linux server
(amd64/arm64) per `.goreleaser.yml`. The package is platform-neutral
by design — `filepath.IsAbs` already encodes the OS-correct notion
of absolute path; no platform-conditional code paths exist within
SDD-18.

**Project Type**: Single Go module (`github.com/mrz1836/hush`)
with a flat `internal/<domain>` layout per
`docs/PACKAGE-MAP.md`. SDD-18 creates the `internal/supervise/`
parent directory and lands its first content (`config/`) under it
— see PACKAGE-MAP.md `internal/supervise/` section, currently a
placeholder ("Filled by SDD-18..SDD-23..."). SDD-19 onwards will
add sibling sub-packages or files at `internal/supervise/`.

**Performance Goals**:
- `Load` total wall time: ≤5 ms for a typical supervisor config
  (<2 KiB) on a modern macOS / Linux host. The package does not
  call Argon2id, AES-GCM, or any other expensive crypto — its
  work is pure I/O + decode + validation.
- `Validate` is O(fields) — a single pass over the decoded struct,
  ~25 root + section fields total. Sub-millisecond.
- Fuzz target: ≥1k iter/s on a 2026-class CI runner; the 60s gate
  exercises ≥60k randomly generated byte streams.

**Constraints**:
- ≥95% test coverage on `internal/supervise/config/` (Constitution
  VIII High band: "supervisor state machine, validators").
- Fuzz `FuzzSuperviseTOML` runs ≥60 s clean (no panic, no
  unbounded memory growth, every error a typed sentinel) per
  Constitution VIII Fuzz target #5 (per SDD-18 chunk contract:
  "fuzz target #5 — TOML parse, distinct from SDD-06's
  server-config target"). The two TOML fuzzers share the
  constitutional fuzz-target line item (#5) but exercise distinct
  schemas: SDD-06 fuzzes the server-config decoder via
  `FuzzServerTOML`, SDD-18 fuzzes the supervisor-config decoder
  via `FuzzSuperviseTOML`. Both run for ≥60 s in CI.
- Zero panics on hostile input. Every code path that can fail
  returns a typed sentinel error (FR-018).
- No `init()` function, no mutable package-level globals beyond
  the read-only sentinel-class exported `var`s the locked API
  names. (Constitution IX — all `Default*` and `Max*` exported
  `var`s are set-once at package load, never mutated; same
  constitutional class as `var Err... = errors.New(...)`
  declarations.)
- The `Supervisor` struct has no field that holds a secret value
  (FR-014). Discord bot tokens, API tokens, OAuth credentials are
  fetched from Keychain / vault server at runtime by other
  components. The struct carries only non-secret pointers
  (Discord channel IDs, keychain item names, scoped secret
  *names*, validator type names, file paths). (Constitution X.)
- No environment-variable reads for any secret-bearing field
  (FR-015 + Constitution X). Reading `$HOME` via `os.UserHomeDir`
  for `~` path expansion of `status_socket` / `pid_file` is
  permitted (non-secret, ubiquitous Unix convention; same
  precedent as SDD-06).
- No new direct dependencies (Constitution XI). SDD-18 reuses
  `pelletier/go-toml/v2` introduced by SDD-06; the `go.mod` /
  `go.sum` files do not change.
- No CGO, no `vendor/`, no `init()`, no goroutines.

**Scale/Scope**:
- Six source files: `config.go` (`Supervisor` + sub-structs +
  `Validator` + decoded shape + materializer), `defaults.go`
  (`Default*` and `Max*` constants + `validatorAllowList`),
  `validate.go` (rule engine producing typed errors), `paths.go`
  (filesystem path-safety helpers: `~` expansion + `filepath.Abs`,
  reused pattern from SDD-06 — duplicated rather than shared
  because `internal/config/paths.go` is a sibling-package not a
  shared helper, and Constitution IX prefers tiny duplication
  over thin abstractions), `errors.go` (sentinel error
  declarations), `doc.go` (package doc + Constitution citations).
- Three test files: `config_test.go` (Load happy-path + decode
  errors + defaults application + idempotency), `validate_test.go`
  (rule-engine per-field positive + negative; one test per
  sentinel), `config_fuzz_test.go` (FuzzSuperviseTOML + seed
  corpus).
- The chunk contract names "config.go, defaults.go, validate.go,
  *_test.go, config_fuzz_test.go" — the plan adds two purely
  declarative files (`errors.go`, `paths.go`) and one
  documentation file (`doc.go`) in line with the SDD-06
  precedent. No production logic is added beyond the chunk
  contract; the file split is locality-only.
- Estimated ~700 LOC of production Go (struct + constants + rule
  engine) and ~1200 LOC of tests.
- Exported surface: 5 types (`Supervisor`, `Child`,
  `DiscordRouting`, `Watchdog`, `Validator`), 2 functions (`Load`,
  `Supervisor.Validate`), ~14 `Default*`/`Max*` constants, ~17
  sentinel errors. Total exported identifiers: ~38.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principles in scope (per SDD-18 chunk contract)

| Principle | Constraint | Plan compliance |
|-----------|------------|-----------------|
| **IV. Supervisor for Daemons — TTL discipline + grace-window cap** | Supervisor TTL is bounded; the optional grace-cache window MUST NOT extend a single Discord approval beyond the 4-hour ceiling. The constitution names the cap explicitly because a longer grace cache redefines the threat model (one approval covering a multi-shift access window). | `validate.go` enforces `cache_grace_ttl > MaxGraceWindow (4h) → ErrGraceWindowTooLong` (FR-004). The cap is encoded as a typed `var MaxGraceWindow = 4 * time.Hour` so downstream consumers (and tests) read the constitutional bound rather than re-deriving it. The contradiction-guard `cache_secrets_for_restart=false` + explicit `cache_grace_ttl → ErrGraceTTLWithoutCache` (FR-011 + Clarification 3) prevents a silently-ignored mistake from re-introducing the longer-than-cap shape via a misconfigured cache. The TTL ceiling on `requested_ttl` (`MaxRequestedTTL = 24 * time.Hour`) is a sibling guard preventing the operator from over-requesting at the supervisor itself, even though the server is the canonical enforcer at claim time. ✅ |
| **V. Staleness is Visible — operator visibility, validator allow-list explicit** | Validators are the line of defence against a stale credential reaching the child process. The constitution requires explicit, named validators for `anthropic`, `anthropic-oauth`, `openai`, `google-ai`, `github`. Unknown validator names in config MUST be a startup error, never silently dropped — silent drop converts a staleness-detection feature into the canonical 2026-04-04 Mini-Zai failure mode (114 MB of logs from an undetected stale token). | `defaults.go` declares `validatorAllowList map[string]struct{}` with exactly the five constitutional names. The materialisation step iterates each `[validators]` entry; any value outside the allow-list returns `ErrUnknownValidator` carrying the offending validator NAME (not the value/secret-name on the LHS, since FR-014 + FR-020 forbid token-shaped strings in error messages). The `Validator` typedef on the struct's `Validators map[string]Validator` field is type-narrow: a downstream consumer that holds a successfully loaded `*Supervisor` cannot encounter an out-of-allow-list validator name (SC-005, asserted by `TestSuperviseConfig_LoadedConfigContainsOnlyAllowListedValidators`). ✅ |
| **VIII. Testing Discipline — TDD + 95% coverage + fuzz target #5** | Test-first; ≥95% coverage; fuzz target #5 (TOML config parsing) ≥60 s clean in CI. Every documented rejection category exercised by a unit test. Every documented default exercised by a unit test. | The /speckit-tasks-phase prompt (Prompt 4) enforces test-first ordering: every behaviour-contract task has a paired test-writing task scheduled BEFORE it. Coverage gate is `go test -cover ./internal/supervise/config/` ≥95% in the implement-phase release-step list. Fuzz target `FuzzSuperviseTOML` is mandated as the SDD-18 fuzz contribution under Constitution VIII fuzz-target #5; the chunk-contract names the 60 s gate. The named tests from the chunk contract (`TestSuperviseConfig_FullMinimal`, `TestSuperviseConfig_FullMaximal`, `TestSuperviseConfig_RejectsUnknownField`, `TestSuperviseConfig_RejectsUnknownValidator`, `TestSuperviseConfig_GraceWindowOver4h_Rejected`, `TestSuperviseConfig_RefreshWindowFormat`, `TestSuperviseConfig_RefreshWindowStartGEEnd_Rejected`, `TestSuperviseConfig_CommandFirstElementMustBeAbsolute`, `TestSuperviseConfig_CommandEmpty_Rejected`) are the floor; `tasks.md` will expand to one test per documented default + one test per sentinel error (per Constitution VIII "Every AC → required test types" mapping). ✅ |
| **IX. Idiomatic Go Discipline — no init, no globals, errors wrapped** | No `init()`. No mutable package-level globals (sentinel-class `var Err...` and read-only `Default*` / `Max*` constants are permitted). Errors wrapped with `%w`. Compare with `errors.Is` / `errors.As`. `context.Context` accepted as first parameter for I/O. No goroutines. Modules-only, CGO-disabled, no `vendor/`. | No `init()` exists. The package's only package-level `var`s are: (a) sentinel error declarations (`ErrUnknownField`, `ErrUnknownValidator`, …), set-once at package load, never mutated — the same constitutional class as the `errors.New` declarations in SDD-06 (`internal/config`); (b) typed default / maximum constants (`DefaultGraceWindow`, `MaxGraceWindow`, …), all `var` because Go's `time.Duration`-and-`bool`-and-`[]string`-mixed groups can't be `const` together but are immutable by convention; (c) `validatorAllowList map[string]struct{}`, populated as a literal at package load. All `var` declarations carry an inline `//nolint:gochecknoglobals` annotation citing the sentinel-class precedent in SDD-06. `Load` accepts `ctx context.Context` as first parameter (inspected at entry only — no goroutines spawned, no I/O cancellation possible mid-decode because the file is read fully into memory in one syscall). All errors wrap underlying causes via `%w`; no string compares; tests use `errors.Is`. No goroutines. No CGO. No new direct deps. ✅ |
| **X. Observability & Redaction — no secrets in config + redacted errors** | The `Supervisor` struct MUST NOT carry any secret value. Discord bot token, API tokens, OAuth credentials, vault passphrases are fetched from Keychain or vault at runtime by other components. The loader MUST NOT consult environment variables for any secret-bearing field. Error messages MUST NOT include any secret value, scope value used as credential material, or other token-shaped string read from any source. | The `Supervisor` struct's only secret-adjacent fields are `Discord.AlertChannelID` (non-secret Discord snowflake), the `Validators` map keys (scoped secret *names* — non-secret label), and the `Scope []string` (scoped secret names). No `[]byte` field, no `*securebytes.SecureBytes` field, no string field documented as holding a secret. The loader has zero `os.Getenv` calls; the only env-reading code path is `os.UserHomeDir` (which reads `$HOME`) invoked solely for `~` path expansion of `status_socket` / `pid_file` — `$HOME` is non-secret. A self-test (`TestLoad_DoesNotReadSecretsFromEnv`) sets several plausible-secret env vars (`HUSH_DISCORD_TOKEN`, `HUSH_VAULT_PASSPHRASE`, `HUSH_REASON`, `HUSH_REFRESH_WINDOW`) and asserts the loaded config equals the file's literal contents. Sentinel error messages are static category strings; `ErrUnknownValidator`'s wrapping message includes only the offending validator NAME (e.g., `"slack"`), never the LHS secret name and never any byte beyond the validator-type literal (FR-020 + Constitution X). A self-test (`TestErrUnknownValidator_DoesNotIncludeSecretMaterial`) constructs a TOML file mapping a high-entropy LHS to an unknown validator, parses it, and asserts the error string contains only the validator name on the RHS. ✅ |
| **XI. Native-First, Minimal Dependencies — reuse, don't add** | Stdlib first. Every NEW direct dep requires a written justification. SDD-06 already added `github.com/pelletier/go-toml/v2`; SDD-18 reuses it. The crypto stack is OUT OF SCOPE for this package (no crypto). | No new direct dep is introduced. `pelletier/go-toml/v2` was justified at SDD-06 (research R-001 + plan Complexity Tracking row 1) and ships at v2.3.1 in the current `go.sum`. SDD-18's only third-party import is the same module, used identically (`toml.NewDecoder` + `DisallowUnknownFields(true)`). No additional TOML library, no validation library, no URL-parsing library — `net/url` from stdlib handles `server_url` syntactic checks. `go.mod` and `go.sum` are unchanged. ✅ |

### Other principles (not in scope but checked for non-violation)

- **I (Zero Files at Rest on Agent Machines):** out of scope —
  the supervisor config lives on the trusted host or on the same
  machine as the supervised daemon (which runs locally with the
  operator's launchd/systemd unit), at
  `~/.hush/supervisors/<name>.toml`. The file holds no secret
  material per FR-014. Agent machines don't author this file. ✅
- **II (Approval is Human):** out of scope — this package
  describes WHEN a supervisor will request approval (refresh
  window) and HOW it will route alerts (Discord channel ID), but
  performs no approval logic itself. ✅
- **III (Defense in Depth Through Crypto Layering):** out of
  scope — no crypto primitives are touched. The configured
  `bot_token_keychain_item` field on the server-side config
  (SDD-06) is the keychain pointer; this supervisor config has
  no equivalent token surface. ✅
- **VI (Tailscale-Only):** the loader does NOT enforce
  Tailscale-CIDR membership of `server_url` (Clarification 5
  + FR-013a). Syntactic-only `http`/`https` + non-empty `Host`
  is the load-time gate; the downstream supervisor startup
  hardening (SDD-19/SDD-23) enforces the network constraint
  against the host's Tailscale interface at runtime. The
  separation matches the spec clarification's explicit decision
  and the project-wide pattern of "load syntactic, harden
  semantic". ✅
- **VII (CLI Design Standards):** out of scope — this package
  defines no CLI surface. SDD-23 (`hush supervise` command) will
  consume the loaded `*Supervisor` and surface the cobra subcommand. ✅

### Gate result

**PASS** — every principle in scope is satisfied. **Zero
Complexity Tracking entries**: SDD-18 introduces no new direct
dependency, no `init()`, no mutable globals beyond sentinel-class
`var`s, no CGO, no `vendor/`, no goroutines, and no secret-bearing
field on the public struct. The Constitution Check is re-evaluated
post-design (after Phase 1) below.

## Project Structure

### Documentation (this feature)

```text
specs/018-supervise-config/
├── plan.md                  # This file (/speckit-plan command output)
├── research.md              # Phase 0 output — locked HOW decisions
├── data-model.md            # Phase 1 output — Supervisor struct shape, decoded shape, defaults catalogue
├── quickstart.md            # Phase 1 output — consumer integration recipe (SDD-19..23)
├── contracts/
│   └── api.md               # Phase 1 output — exported API contract (locks PACKAGE-MAP §internal/supervise)
├── spec.md                  # WHAT contract (already written by /speckit-specify + /speckit-clarify)
└── tasks.md                 # Phase 2 output (/speckit-tasks command — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/supervise/
└── config/
    ├── doc.go                   # Package doc: Constitution IV/V/VIII/IX/X/XI citations + roster
    ├── config.go                # Supervisor + Child + DiscordRouting + Watchdog + Validator + decoded shape + materializer
    ├── defaults.go              # DefaultGraceWindow, MaxGraceWindow, validatorAllowList, …
    ├── errors.go                # ErrUnknownField, ErrUnknownValidator, ErrGraceWindowTooLong, …
    ├── paths.go                 # expandHome, absPath helpers (mirrors internal/config/paths.go)
    ├── validate.go              # Rule engine: Supervisor.Validate; one validator per documented rule
    ├── config_test.go           # Load happy-path, defaults application, decode errors, idempotency
    ├── validate_test.go         # Rule-engine per-field positive + negative; one test per sentinel
    └── config_fuzz_test.go      # FuzzSuperviseTOML + seed corpus

go.mod                       # UNCHANGED (no new dep — pelletier/go-toml/v2 already present)
go.sum                       # UNCHANGED
```

**Structure Decision**: hush is a single Go module
(`github.com/mrz1836/hush`) with a flat `internal/<domain>` layout
defined in `docs/PACKAGE-MAP.md`. SDD-18 creates the
`internal/supervise/` parent directory (currently absent — this is
the first chunk to land under it) and immediately drops a `config/`
sub-package. The `internal/supervise/` parent is left without
production source until SDD-19 (state machine), per the chunk-by-
chunk discipline of the SDD playbook.

The package import path is
`github.com/mrz1836/hush/internal/supervise/config`. Per
`docs/PACKAGE-MAP.md` the allowed dependency direction is
`cmd/hush → internal/cli → internal/supervise/{state machine,
runner, config, …}`; this chunk does not import any intra-repo
package — it is a leaf producer.

The chunk contract's "Files: `config.go`, `defaults.go`,
`validate.go`, `*_test.go`, `config_fuzz_test.go`" enumerates the
**minimum** set; the plan adds two purely declarative files
(`errors.go`, `paths.go`) and one documentation file (`doc.go`)
in line with the SDD-06 precedent (where the same minimum-vs-
maximum reading was adopted). No production logic is added beyond
what the chunk contract describes.

A note on `paths.go` duplication: `internal/config/paths.go` (SDD-06)
already implements `~` expansion + `filepath.Abs` for the server
config. SDD-18's `paths.go` will duplicate the same ~30 LOC rather
than introduce a `internal/pathutil` shared helper, because (a)
Constitution IX prefers tiny duplication over thin abstractions
when the duplicate fits in one file; (b) cross-package helper
extraction is its own SDD chunk (would conflict with SDD-06's
locked surface); (c) the function is small, well-tested, and
unlikely to drift. If a third caller emerges (SDD-23 cobra command)
we will consolidate then.

## Post-Design Constitution Re-check

Re-evaluated after Phase 1 design artifacts (`research.md`,
`data-model.md`, `contracts/api.md`, `quickstart.md`) were drafted:

| Principle | Phase 1 introduced | Re-check |
|-----------|--------------------|----------|
| **IV** | The defaults catalogue exposes `MaxGraceWindow = 4 * time.Hour` and `MaxRequestedTTL = 24 * time.Hour`. Each is referenced by a corresponding sentinel error and a corresponding asserting test. The grace-cache contradiction guard (`cache_secrets_for_restart=false` + explicit `cache_grace_ttl`) is a separate validator path producing `ErrGraceTTLWithoutCache`. | PASS — the constitutional grace-window ceiling is encoded in three places (constant, validator, test) with no drift. The contradiction guard prevents the silently-ignored mistake re-vector. |
| **V** | The `validatorAllowList` is the single source of truth for valid validator names. The materializer iterates `[validators]` entries one at a time and rejects unknown names with `ErrUnknownValidator` carrying the validator NAME only. SC-005 asserts that no successfully loaded config holds a non-allow-listed validator. | PASS — staleness-detection coverage is enforced by construction: a downstream consumer cannot observe an unrecognised validator name on a `*Supervisor`. |
| **VIII** | The contract enumerates ~32 named tests across the three test files, including the chunk-contract's nine plus one test per documented default (~13) and one test per sentinel error (~17; many shared with the named tests). The fuzz target `FuzzSuperviseTOML` is documented with a seed corpus (eight files: minimal-valid, full-default, malformed-bytes, empty, partial-table, conflicting-types, unknown-validator-name, refresh-window-edge). | PASS — every spec FR + every spec SC has at least one named test; the fuzz target ships with a deterministic seed corpus so CI's first run is meaningful. |
| **IX** | Phase 1 confirmed: zero `init()`, zero mutable globals beyond the documented sentinel-class `var`s, all errors wrapped with `%w`, all comparisons via `errors.Is`. The `paths.go` helpers do read-only filesystem inspection (`os.Stat`, `filepath.Abs`, `os.UserHomeDir`); no writes, no goroutines. | PASS — no new violations introduced. |
| **X** | The `Supervisor` struct is finalised. No field holds a secret value. The single secret-adjacent field, the `Validators` map, has type `map[string]Validator` where both key (scoped secret name) and value (allow-listed validator name) are non-secret labels. Error messages are static category strings; `ErrUnknownValidator` wrapping includes only the validator type name on the RHS, never any byte from the LHS. | PASS — final shape verified against `docs/CONFIG-SCHEMA.md` Supervisor section. |
| **XI** | Zero new direct deps. Phase 1 introduced no additional dependency. `go.mod` / `go.sum` unchanged. | PASS — the dependency surface is unchanged; SDD-18 honours Constitution XI by reuse, not addition. |

**Final result**: PASS. Zero Complexity Tracking entries; no new
violations introduced by the design phase.

## Complexity Tracking

> Fill ONLY if Constitution Check has violations that must be justified.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| _none_ | _N/A_ | _N/A_ |

SDD-18 introduces no new direct dependency, no `init()`, no mutable
globals beyond sentinel-class `var`s, no CGO, no `vendor/`, no
goroutines, and no secret-bearing field on the public struct.
Every gate clears.
