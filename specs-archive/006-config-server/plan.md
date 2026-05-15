# Implementation Plan: `internal/config` — Server TOML Schema + Validation (SDD-06)

**Branch**: `006-config-server` | **Date**: 2026-04-28 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/006-config-server/spec.md`
**Chunk contract**: [docs/sdd/SDD-06.md](../../docs/sdd/SDD-06.md)

## Summary

`internal/config` owns the server-side TOML configuration: schema, defaults,
validation, and path-safety. The package is loaded once at `hush serve`
startup (SDD-10) and once at `hush init` (SDD-15). A clean load is the
contract every later startup path depends on — a loadable-but-unsafe config
is the threat shape this package exists to make impossible.

Approach (locked by SDD-06 + Constitution III/VI/VIII/IX/X/XI; not subject
to research alternatives):

- **TOML decode** via `github.com/pelletier/go-toml/v2` with
  `Decoder.DisallowUnknownFields(true)`. Unknown / misspelled fields
  produce `ErrUnknownField` before any other validation runs.
- **Two-struct decode pipeline** (`serverDecoded` → `Server`): the wire-
  shape struct uses pointer / sentinel types where "absent vs zero"
  matters (`*bool` for `require_tailscale`, `*string` for paths,
  pointer / empty-string discrimination for durations); the public
  `Server` struct uses concrete types (`bool`, `string`, `time.Duration`,
  `netip.AddrPort`). The materializer applies defaults from
  `defaults.go` to any field absent in the decoded form.
- **`listen_addr` and (when set) `health_bind`** are validated via
  `net/netip`. Reject `IsLoopback`, `IsUnspecified`, and any address
  outside the Tailscale CGNAT prefix `100.64.0.0/10`. Each rejection
  produces a distinct sentinel that wraps `ErrTailscaleBindRequired`
  (so `errors.Is(err, ErrTailscaleBindRequired)` matches all of them).
- **Argon2id minimums** are enforced before any other crypto wiring is
  touched: `argon_memory_mb < 256` → `ErrArgonMemoryTooLow`;
  `argon_time < 4` → `ErrArgonTimeTooLow`; `argon_threads < 4` →
  `ErrArgonThreadsTooLow`. All three are constitutional minimums per
  Principle III + Security Requirements table.
- **Path expansion**: a leading `~` in any path field is expanded via
  `os.UserHomeDir`; the result is canonicalised via `filepath.Abs`.
  No other shell expansion is performed (`$VAR`, glob, nested `~user`
  are all literal). After canonicalisation, `audit_log` MUST resolve
  under `state_dir` (verified via `filepath.Rel` — see R-003); any
  out-of-tree path produces `ErrAuditLogEscape`.
- **`state_dir`** MUST exist on disk and be a directory; the loader
  NEVER creates, modifies, or chmods it (that is `hush init`'s job per
  FR-005a). Missing → `ErrStateDirNotFound`; not-a-directory →
  `ErrStateDirUnsafe`.
- **`require_tailscale = false`** is a load-time error
  (`ErrTailscaleRequired`). Absent defaults to `true`. There is no
  configuration knob that disables Tailscale-only enforcement in v0.1.0.
- **`path_prefix`** is enforced to length 6–32 and the URL-safe
  character set `[A-Za-z0-9_-]`; otherwise `ErrPathPrefixInvalid`.
- **TTL bounds**: `max_supervisor_ttl > jwt_default_ttl` AND
  `max_supervisor_ttl ≤ DefaultSupervisorTTLMax (24h)`; either
  violation produces `ErrSupervisorTTLOutOfRange`.
- **No environment-variable reads** for any secret-bearing field
  (Constitution X). `os.UserHomeDir` is used solely for `~` path
  expansion, which is non-secret.
- **No `init()`** function exists in the package (Constitution IX).
  The pelletier decoder, the CGNAT prefix, and the path-prefix regex
  are constructed lazily inside their consumers (or as exported
  read-only sentinel-class `var`s where the locked API names them).
- **Fuzz target `FuzzServerTOML`** feeds random byte streams to
  `LoadServer`. The fuzz contract: no panic, no unbounded allocation,
  every error returned is one of the named sentinels (or wraps one).

Exported API (locked at SDD-06; mirrored into `docs/PACKAGE-MAP.md` once
the implement commit lands):

```go
type Server struct { /* fields per docs/CONFIG-SCHEMA.md — see contracts/api.md */ }

func LoadServer(ctx context.Context, path string) (*Server, error)
func (s *Server) Validate() error

// Default constants (typed) — every documented default in
// docs/CONFIG-SCHEMA.md has one entry. Full list: contracts/api.md.
var DefaultArgonTime, DefaultArgonMemoryMB, DefaultArgonThreads,
    DefaultJWTTTL, DefaultMaxInteractiveTTL, DefaultMaxSupervisorTTL,
    DefaultMaxUses, DefaultNonceTTL, DefaultClockSkew,
    DefaultStateDir, DefaultAuditLog, DefaultClientRegistry,
    DefaultRequireTailscale, DefaultAllowedCIDRs,
    DefaultRequireFileModeChecks, DefaultRequireKeychainACL,
    DefaultRequireNTPSync, DefaultMaxClockDrift,
    DefaultSupervisorTTLMax,
    MinArgonTime, MinArgonMemoryMB, MinArgonThreads,
    MinPathPrefixLen, MaxPathPrefixLen

// Sentinel errors — full list and wrap-relationships in contracts/api.md.
var ErrUnknownField, ErrMissingRequiredField,
    ErrTailscaleBindRequired, ErrListenLoopback, ErrListenUnspecified,
    ErrListenPublic, ErrListenMalformed,
    ErrTailscaleRequired,
    ErrPathPrefixInvalid, ErrAuditLogEscape,
    ErrStateDirNotFound, ErrStateDirUnsafe,
    ErrArgonMemoryTooLow, ErrArgonTimeTooLow, ErrArgonThreadsTooLow,
    ErrSupervisorTTLOutOfRange,
    ErrInvalidDuration, ErrTOMLDecode
```

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`); `CGO_ENABLED=0`
(Constitution IX).

**Primary Dependencies**:
- Go stdlib: `context`, `errors`, `fmt`, `io`, `net/netip`, `os`,
  `path/filepath`, `regexp`, `strings`, `time`.
- New direct dep: `github.com/pelletier/go-toml/v2`. Justification in
  research R-013 + Complexity Tracking row 1 (see below). Single
  TOML decoder for the project; the SDD-06 chunk-contract names it
  explicitly; no transitive dependencies beyond its own subpackages.
- Intra-repo (locked upstream): NONE at load-time. The package is a
  leaf consumer — it produces a `*Server` value that downstream
  packages (SDD-10 server, SDD-15 init) consume; it imports nothing
  from `internal/keys`, `internal/vault`, `internal/logging`, or any
  other intra-repo package.

**Storage**: read-only — opens the supplied file path, decodes, closes.
No writes, no temp files, no caches. Idempotent across calls (same
input → same output regardless of the calling process's environment,
modulo `$HOME` for `~` expansion which is non-secret per FR-007).

**Testing**: `go test ./internal/config/...` (table-driven unit tests
per `.github/tech-conventions/testing-standards.md`); `magex test:race`
race-clean; `go test -fuzz=FuzzServerTOML -fuzztime=60s
./internal/config/` with no panics and no new corpus rows representing
crashes. Coverage measured via `go test -cover ./internal/config/`;
target ≥95% per Constitution VIII (High-priority band: server-config
parsing).

**Target Platform**: macOS (darwin amd64/arm64) and Linux server
(amd64/arm64) per `.goreleaser.yml`. Windows is out of scope
(project-wide). One platform-conditional default exists:
`DefaultRequireKeychainACL` is documented as `true` on macOS only;
the constant ships as `true` and the runtime hardening enforcer
(SDD-10) decides whether to skip the check on non-macOS hosts. SDD-06
does not import `runtime` or `os/user` for platform conditionals — the
decision is downstream.

**Project Type**: Single Go module (`github.com/mrz1836/hush`) with a
flat `internal/<domain>` layout per `docs/PACKAGE-MAP.md`.
`internal/config` is the existing placeholder (`.gitkeep`-only)
sibling under `internal/`; SDD-06 is its first content drop. SDD-18
(supervisor config) will land under the same directory in a later
chunk.

**Performance Goals**:
- `LoadServer` total wall time: ≤5 ms for a typical config (<4 KiB)
  on a modern macOS / Linux host. The package does not call Argon2id,
  AES-GCM, or any other expensive crypto — its work is pure I/O +
  decode + validation.
- `Validate` is O(fields) — a single pass over the decoded struct,
  ~30 fields total. Sub-millisecond.
- Fuzz target: ≥1k iter/s on a 2026-class CI runner; the 60s gate
  exercises ≥60k randomly generated byte streams.

**Constraints**:
- ≥95% test coverage on `internal/config/` (Constitution VIII High
  band: "Server handlers, supervisor state machine, validators").
- Fuzz `FuzzServerTOML` runs ≥60 s clean (no panic, no unbounded
  memory growth, every error a typed sentinel) per Constitution VIII
  Fuzz target #5.
- Zero panics on hostile input. Every code path that can fail returns
  a typed sentinel error.
- No `init()` function, no mutable package-level globals beyond the
  read-only sentinel-class exported `var`s the locked API names.
  (Constitution IX — all `Default*` and `Min*` exported `var`s are
  set-once at package load, never mutated; same constitutional class
  as `var Err... = errors.New(...)` declarations.)
- The `Server` struct has no field that holds a secret value. Discord
  bot token, vault passphrase, etc. are NOT representable. The struct
  carries only Keychain item names and other non-secret pointers.
  (Constitution X.)
- No environment-variable reads for any secret-bearing field. Reading
  `$HOME` via `os.UserHomeDir` for `~` path expansion is permitted
  (non-secret, ubiquitous Unix convention).
- New direct dependency `github.com/pelletier/go-toml/v2` must be
  justified per Constitution XI — see Complexity Tracking row 1.
- No CGO, no `vendor/`, no `init()`, no goroutines.

**Scale/Scope**:
- Six source files: `server.go` (Server struct + accessors),
  `defaults.go` (default + minimum constants), `validate.go` (rule
  engine producing typed errors), `paths.go` (filesystem path-safety:
  `~` expansion, `filepath.Abs`, `filepath.Rel`-based containment),
  `errors.go` (sentinel error declarations) — split out for locality
  with the other small declaration files; permitted by the chunk
  contract's "Files: ..." list under the inclusive reading that the
  contract names the minimum set, not the maximum), `doc.go` (package
  doc + Constitution citations).
- Three test files: `server_test.go` (LoadServer happy-path + decode
  errors), `validate_test.go` (rule-engine per-field positive +
  negative), `server_fuzz_test.go` (FuzzServerTOML + corpus seed
  files).
- Estimated ~600 LOC of production Go (struct + constants +
  validators) and ~1000 LOC of tests.
- Exported surface: 1 type (`Server`), 2 functions (`LoadServer`,
  `Server.Validate`), ~22 `Default*`/`Min*`/`Max*` constants, ~16
  sentinel errors. Total exported identifiers: ~40 — large because
  every documented default and every documented rejection category
  is named.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

### Principles in scope (per SDD-06)

| Principle | Constraint | Plan compliance |
|-----------|------------|-----------------|
| **III. Defense in Depth — Argon2id minimums** | Argon2id parameters MUST be `time≥4, memory≥256 MiB, threads≥4` (Security Requirements table). Validation MUST refuse weaker values. | `validate.go` enforces all three: `argon_memory_mb < MinArgonMemoryMB (256)` → `ErrArgonMemoryTooLow`; `argon_time < MinArgonTime (4)` → `ErrArgonTimeTooLow`; `argon_threads < MinArgonThreads (4)` → `ErrArgonThreadsTooLow`. The constitutional floor is encoded as exported typed `Min*` constants so downstream consumers (and tests) can verify the gate without re-deriving the value. ✅ |
| **VI. Tailscale-only, never public** | `listen_addr` MUST resolve to a Tailscale interface; loopback / unspecified / public IPs MUST be refused. ACLs and TLS-within-Tailscale are out of scope for this package. | `validate.go` parses `listen_addr` with `net/netip.ParseAddrPort`. The address is checked against `IsLoopback`, `IsUnspecified`, and `TailscaleCGNAT.Contains`. Each failure mode emits a distinct sentinel that wraps `ErrTailscaleBindRequired`. `health_bind` (when explicitly set per FR-003a) is validated identically. `require_tailscale = false` is rejected at load time per FR-005c. ✅ |
| **VIII. Testing Discipline — TDD + fuzz target #5** | Test-first; ≥95% coverage; fuzz target #5 (TOML config parsing) ≥60 s clean in CI. Every documented rejection category exercised by a unit test. | The /speckit-tasks-phase prompt enforces test-first ordering: every behaviour-contract task has a paired test-writing task scheduled BEFORE it. Coverage gate is `go test -cover ./internal/config/` ≥95% in the implement-phase release-step list. Fuzz target `FuzzServerTOML` is mandated as the #5 fuzz target by the constitution; the chunk-contract names the 60 s gate. The named tests (`TestServer_RejectsLoopback`, `TestServer_RejectsPublic`, `TestServer_AcceptsTailscaleCGNAT`, `TestServer_RejectsArgonMemoryUnder256`, `TestServer_RejectsAuditLogOutsideStateDir`, `TestServer_RejectsUnknownField`, `TestServer_FullMinimalConfig`, `TestServer_FullMaximalConfig`) are the floor; `tasks.md` will expand to one test per documented default + one test per sentinel error. ✅ |
| **IX. Idiomatic Go Discipline — no init, no globals, errors wrapped** | No `init()`. No mutable package-level globals (sentinel-class `var Err...` and read-only `Default*` constants are permitted). Errors wrapped with `%w`. Compare with `errors.Is`/`errors.As`. `context.Context` accepted as first parameter for I/O. No goroutines. | No `init()` exists. The package's only package-level `var`s are: (a) the sentinel error declarations (`ErrUnknownField`, `ErrTailscaleBindRequired`, ...), set-once at package load, never mutated — the same constitutional class as the `errors.New` declarations in `internal/keys` (SDD-01) and `internal/vault` (SDD-03); (b) the typed default / minimum constants (`DefaultArgonMemoryMB`, `DefaultJWTTTL`, ...), all `var` because Go's `time.Duration`-and-`uint32`-mixed groups can't be `const` together but are immutable by convention; (c) `TailscaleCGNAT netip.Prefix` and `pathPrefixRegex *regexp.Regexp`, both populated by lazy `sync.Once` initialisers (the regex) or set-once at package load (the prefix from `netip.MustParsePrefix("100.64.0.0/10")`). All `var` declarations have an inline `//nolint:gochecknoglobals` annotation citing the sentinel-class precedent. `LoadServer` accepts `ctx context.Context` as first parameter (currently inspected at entry only — no goroutines spawned, no I/O cancellation possible mid-decode because the file is read fully into memory in one syscall). All errors wrap underlying causes via `%w`; no string compares; tests use `errors.Is`. No goroutines. ✅ |
| **X. Observability & Redaction — no secrets in config** | The `Server` struct MUST NOT carry any secret value. Discord bot token, vault passphrase, etc. are fetched from Keychain at runtime by callers. The loader MUST NOT consult environment variables for any secret-bearing field. | The `Server` struct's only secret-adjacent field is `Discord.BotTokenKeychainItem` — a string holding the Keychain item NAME (e.g., `"hush-discord"`), not the token itself. SDD-10 fetches the actual token from the Keychain at startup. The struct contains no `[]byte` field, no `*securebytes.SecureBytes` field, and no string field documented as holding a secret. The loader has zero `os.Getenv` calls; the only env-reading code path is `os.UserHomeDir` (which reads `$HOME`) and is invoked only for `~` path expansion — `$HOME` is non-secret. A self-test (`TestLoadServer_DoesNotReadSecretsFromEnv`) sets several plausible-secret env vars (`HUSH_DISCORD_TOKEN`, `HUSH_VAULT_PASSPHRASE`) and asserts they are absent from the loaded `Server`. ✅ |
| **XI. Native-First, Minimal Dependencies** | Stdlib first. New direct dep requires written justification per the trusted-sources hierarchy. `github.com/pelletier/go-toml/v2` is the project's TOML decoder; SDD-06 introduces it. | The decoder choice is locked by the SDD-06 chunk contract: `github.com/pelletier/go-toml/v2` with `DisallowUnknownFields(true)`. Justification: the Go standard library does NOT include a TOML decoder; the `encoding/toml` proposal has not landed. The trusted-sources hierarchy tier 4 (the wider ecosystem) applies. `pelletier/go-toml/v2` is the canonical Go TOML decoder — actively maintained, widely used (HashiCorp, GitHub Actions runners, etc.), zero CGO, single-module, no transitive deps beyond its own internal subpackages. The PR description for the implement commit will repeat the justification per Constitution XI's "every NEW direct dependency requires a written justification" clause. See research R-013 + Complexity Tracking row 1. ✅ |

### Other principles (not in scope but checked for non-violation)

- **I (Zero Files at Rest on Agent Machines):** out of scope — the
  vault server config lives only on the trusted host; this package
  does not write to disk. Agent machines never touch this package's
  output. ✅
- **II (Approval is Human):** out of scope — no approval surface in
  scope. ✅
- **IV (Supervisor for Daemons):** out of scope — supervisor config
  is SDD-18, not SDD-06. ✅
- **V (Staleness is Visible):** out of scope. ✅
- **VII (CLI Design Standards):** out of scope — this package is
  consumed by `internal/cli/serve.go` (SDD-14) and
  `internal/cli/init.go` (SDD-15) but defines no CLI surface. ✅

### Gate result

**PASS** — every principle in scope is satisfied. **One Complexity
Tracking entry** (the new direct dependency
`github.com/pelletier/go-toml/v2`) is justified inline below per
Constitution XI; it is not a deviation from the principle, but a
ratification under the principle's "stdlib-first / written-
justification" gate. The Constitution Check is re-evaluated post-
design (after Phase 1) below.

## Project Structure

### Documentation (this feature)

```text
specs/006-config-server/
├── plan.md                  # This file (/speckit-plan command output)
├── research.md              # Phase 0 output — locked HOW decisions
├── data-model.md            # Phase 1 output — Server struct shape, decoded shape, defaults catalogue
├── quickstart.md            # Phase 1 output — consumer integration recipe (SDD-10 + SDD-15)
├── contracts/
│   └── api.md               # Phase 1 output — exported API contract (locks PACKAGE-MAP §internal/config)
├── checklists/              # Pre-existing (untouched by /speckit-plan)
├── spec.md                  # WHAT contract (already written by /speckit-specify + /speckit-clarify)
└── tasks.md                 # Phase 2 output (/speckit-tasks command — NOT created by /speckit-plan)
```

### Source Code (repository root)

```text
internal/config/
├── doc.go                   # Package doc: Constitution III/VI/VIII/IX/X/XI citations + roster
├── server.go                # Server struct, sub-section structs, accessor methods, materializer
├── defaults.go              # DefaultArgonTime, DefaultArgonMemoryMB, ..., MinArgonMemoryMB, MinPathPrefixLen, etc.
├── errors.go                # ErrUnknownField, ErrTailscaleBindRequired, ErrListenLoopback, ..., wrap relationships
├── paths.go                 # expandHome, absPath, isUnderStateDir helpers
├── validate.go              # Rule engine: Server.Validate; one validator per documented rule
├── server_test.go           # LoadServer happy-path, defaults application, decode errors, idempotency
├── validate_test.go         # Rule-engine per-field positive + negative; one test per sentinel
└── server_fuzz_test.go      # FuzzServerTOML + seed corpus

go.mod                       # adds github.com/pelletier/go-toml/v2 direct dep
go.sum                       # checksum rows added
```

**Structure Decision**: hush is a single Go module
(`github.com/mrz1836/hush`) with a flat `internal/<domain>` layout
defined in `docs/PACKAGE-MAP.md`. SDD-06 fills the existing
`internal/config/` directory (currently `.gitkeep`-only). The package
ships six production source files; the chunk contract's "Files:" list
named four (`server.go`, `defaults.go`, `validate.go`, `paths.go`).
The plan adds two: `errors.go` (sentinel error declarations) and
`doc.go` (package-level doc comment with Constitution citations).
Both additions are locality refinements — Go convention is to colocate
sentinel errors in a single `errors.go` for grep-ability, and a `doc.go`
keeps the package comment isolated from any one type's declaration.
The chunk-contract's file list is read as the **minimum** set: every
file the contract names is present, and the package may add purely
declarative files where idiomatic Go discipline calls for them. No
production logic is added beyond what the chunk contract describes.

The package import path is `github.com/mrz1836/hush/internal/config`.
Per `docs/PACKAGE-MAP.md` the allowed dependency direction is `cmd/hush
→ internal/cli → internal/config`; this chunk does not import any
intra-repo package — it is a leaf producer. SDD-18 (supervisor config)
will land in the same directory and may share the `paths.go`
helpers, but that is a future-chunk concern.

## Post-Design Constitution Re-check

Re-evaluated after Phase 1 design artifacts (`research.md`,
`data-model.md`, `contracts/api.md`, `quickstart.md`) were drafted:

| Principle | Phase 1 introduced | Re-check |
|-----------|--------------------|----------|
| **III** | Defaults catalogue exposes `MinArgonTime = uint32(4)`, `MinArgonMemoryMB = uint32(256)`, `MinArgonThreads = uint8(4)`. Each is referenced by a corresponding sentinel error and a corresponding asserting test. | PASS — the constitutional floor is encoded in three places (constant, validator, test) with no drift. |
| **VI** | The contract pins `TailscaleCGNAT = netip.MustParsePrefix("100.64.0.0/10")` as the only acceptable network. `health_bind` shares the validator path with `listen_addr` (FR-003a) — no second code path exists. The `require_tailscale = false` rejection (FR-005c) is encoded as an explicit validator rule, not as a default-derivation side-effect. | PASS — the Tailscale-only boundary is enforced by a single named function (`validateTailscaleAddrPort`) shared by the two address fields; "require_tailscale always true" is a separate validator that runs unconditionally. |
| **VIII** | The contract enumerates 38 named tests across the three test files, including the named eight from the chunk contract plus one test per documented default (~22) and one test per sentinel error (~16; many shared with the named tests). The fuzz target `FuzzServerTOML` is documented with a seed corpus (eight files: minimal-valid, full-default, malformed-bytes, empty, partial-table, conflicting-types, very-long-string, unicode-edge). | PASS — every spec FR + every spec SC has at least one named test; the fuzz target ships with a deterministic seed corpus so CI's first run is meaningful. |
| **IX** | Phase 1 confirmed: zero `init()`, zero mutable globals beyond the documented sentinel-class `var`s, all errors wrapped with `%w`, all comparisons via `errors.Is`. The `paths.go` helpers do read-only filesystem inspection (`os.Stat`, `filepath.Abs`, `filepath.Rel`, `os.UserHomeDir`); no writes, no goroutines. | PASS — no new violations introduced. |
| **X** | The `Server` struct is finalised. No field holds a secret value. The single secret-adjacent field (`Discord.BotTokenKeychainItem`) holds the Keychain item *name*. The `Discord.ApplicationID`, `Server.DiscordOwnerID`, `Server.DiscordAuditChannelID` are all non-secret Discord snowflakes (public IDs). | PASS — final shape verified against `docs/CONFIG-SCHEMA.md`. |
| **XI** | One new direct dep (`github.com/pelletier/go-toml/v2`) — justification documented in research R-013. Phase 1 introduced no additional dependency. The trusted-sources hierarchy is honoured: the package is in tier 4 (wider ecosystem); the justification covers maintainer activity, supply-chain provenance (the module is on `proxy.golang.org` with a transparency-log entry), transitive footprint (zero — the module is fully self-contained), and the stdlib-gap rationale. | PASS — the dependency is the minimum surface that satisfies the chunk contract. |

**Final result**: PASS. Complexity Tracking entry (one) remains as
documented; no new violations introduced by the design phase.

## Complexity Tracking

> Fill ONLY if Constitution Check has violations that must be justified.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| New direct dependency on `github.com/pelletier/go-toml/v2` (the project's first TOML decoder; introduces a single tier-4 ecosystem dep into `go.mod`). Formally a "new direct dependency" gate under Constitution XI. | The Go standard library does NOT include a TOML decoder. The CONFIG-SCHEMA is locked at TOML by `docs/CONFIG-SCHEMA.md` and the Phase-0 specification — operators author `~/.hush/config.toml` and `hush init` writes TOML; switching the file format to JSON or stdlib `flag.Var`-style key-value would require a Phase-0 amendment and break operator-facing documentation. The chunk contract names `pelletier/go-toml/v2` with `DisallowUnknownFields(true)` as the locked HOW; switching to a different TOML library would diverge from the contract. `pelletier/go-toml/v2` is the de-facto canonical Go TOML decoder: maintained by Thomas Pelletier (active, weekly commits, 4k+ GitHub stars), zero CGO, zero non-stdlib transitive deps within its own module graph, distributed via `proxy.golang.org` with a public Sigstore transparency-log entry, and used in production by HashiCorp tooling, GitHub Actions runners, and many other Go projects. The strict-decode mode (`DisallowUnknownFields`) is the load-bearing feature for FR-002 (typo defence) — alternatives like `BurntSushi/toml` accept unknown fields by default and require a non-default code path to refuse them; pelletier's API exposes the strict mode as a single decoder option. | *Implement a TOML subset by hand*: rejected. Even a small TOML subset would be 500+ LOC of parser + a fuzz target on the parser itself; introducing parser bugs into a security-critical config loader is a far worse trade than adopting a maintained, fuzzed-upstream decoder. *Switch the config format to JSON*: rejected. The Phase-0 documentation and the operator workflow (`hush init` writes a sample TOML file with comments) depend on TOML's `# comment` support. JSON has no comment syntax; YAML would replace one ecosystem dep with a less-maintained one (`gopkg.in/yaml.v3` is also tier-4 ecosystem). *Use `BurntSushi/toml`*: rejected. The chunk contract pins `pelletier/go-toml/v2`; switching would require a constitutional amendment and re-issuing the chunk. Behaviourally, both libraries support strict-decode but pelletier's surface is closer to stdlib `encoding/json`'s `Decoder` shape, which the project's other JSON-handling packages already use. |
