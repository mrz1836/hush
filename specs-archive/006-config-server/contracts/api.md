# Contract: `internal/config` exported API (Server)

**Feature**: 006-config-server
**Status**: Locked at SDD-06; mirrored into `docs/PACKAGE-MAP.md` once the implement commit lands.

This is the contract every downstream package (SDD-10 server, SDD-15 init, SDD-18 supervisor config) depends on. Changes after SDD-06 lands require a new SDD chunk; consumers may rely on every signature, every default value, and every sentinel identity below.

SDD-18 will add supervisor-config types (`Supervisor`, `LoadSupervisor`, ...) to the same package. SDD-18 MUST NOT alter any symbol below.

---

## Package path

```
github.com/mrz1836/hush/internal/config
```

---

## Exported types

### `type Server struct`

```go
type Server struct {
    Server   ServerSection
    Discord  DiscordSection
    Crypto   CryptoSection
    Network  NetworkSection
    Security SecuritySection
}
```

**Contract**:
- Read-only after `LoadServer` returns. Consumers MUST NOT mutate any field.
- No field holds a secret value. The single secret-adjacent field, `Discord.BotTokenKeychainItem`, holds a Keychain item NAME.
- All path-bearing fields are absolute and `~`-expanded.
- All duration-bearing fields are populated `time.Duration` values (not strings).
- All addressing fields are pre-parsed `netip.AddrPort` values.

### `type ServerSection struct`

```go
type ServerSection struct {
    ListenAddr            netip.AddrPort
    PathPrefix            string
    StateDir              string
    AuditLog              string
    DiscordOwnerID        string
    ClientRegistry        string
    DiscordAuditChannelID string
}
```

### `type DiscordSection struct`

```go
type DiscordSection struct {
    BotTokenKeychainItem string
    ApplicationID        string
}
```

### `type CryptoSection struct`

```go
type CryptoSection struct {
    ArgonTime         uint32
    ArgonMemoryMB     uint32
    ArgonThreads      uint8
    JWTDefaultTTL     time.Duration
    MaxInteractiveTTL time.Duration
    MaxSupervisorTTL  time.Duration
    DefaultMaxUses    int
    NonceTTL          time.Duration
    ClockSkew         time.Duration
}
```

### `type NetworkSection struct`

```go
type NetworkSection struct {
    RequireTailscale bool
    AllowedCIDRs     []string
    HealthBind       netip.AddrPort
}
```

### `type SecuritySection struct`

```go
type SecuritySection struct {
    RequireFileModeChecks bool
    RequireKeychainACL    bool
    RequireNTPSync        bool
    MaxClockDrift         time.Duration
}
```

---

## Exported functions

### `func LoadServer(ctx context.Context, path string) (*Server, error)`

```go
func LoadServer(ctx context.Context, path string) (*Server, error)
```

**Inputs**:
- `ctx` — checked once at entry; pre-cancellation returns `ctx.Err()` immediately. The function is short and CPU-bound; cancellation mid-decode is not supported.
- `path` — absolute or relative path to the TOML file. Relative paths are resolved against the calling process's working directory.

**Output**:
- On success: a populated `*Server` with every absent optional field defaulted from the catalogue, every duration parsed, every path canonicalised, and every documented validation rule satisfied.
- On failure: `nil, err`. Never returns a partially populated `*Server` with a non-nil error.

**Idempotence**: same input file + same `$HOME` → same output. The function never installs process-wide state, never mutates any global.

**Side effects**:
- Reads the file at `path` (one `os.Open` + decoder reads + `Close`).
- Reads `$HOME` via `os.UserHomeDir` for `~` expansion.
- Calls `os.Stat(absStateDir)` to verify state-dir existence.
- Performs no writes, no network I/O, no goroutine launches.

**Concurrency**: safe to call concurrently. The function holds no mutex; concurrent calls each open their own file handle.

### `func (s *Server) Validate() error`

```go
func (s *Server) Validate() error
```

**Inputs**: a `*Server` value. Typically called by `LoadServer` itself; consumers MAY call it independently if they construct a `*Server` programmatically (rare — the typical path is `LoadServer`).

**Output**:
- `nil` on full validation success.
- A single sentinel-bearing error on a single violation.
- An `errors.Join(...)` of multiple sentinel-bearing errors when multiple rules fail.

**Sentinel matching**: `errors.Is(err, ErrArgonMemoryTooLow)` returns `true` iff the argon-memory rule failed; same for every other sentinel listed in the [Sentinel error catalogue](#sentinel-error-catalogue) below. For wrap relationships (the listen-address family), `errors.Is(err, ErrTailscaleBindRequired)` matches whenever any of `ErrListenLoopback`, `ErrListenUnspecified`, `ErrListenPublic` is wrapped.

**Determinism**: the rule order is documented and stable; tests rely on it. The order is:
1. Decode-phase errors (already produced by `LoadServer`; `Validate` does not re-decode).
2. `ErrTailscaleRequired` (network gate).
3. `ErrArgonMemoryTooLow`, `ErrArgonTimeTooLow`, `ErrArgonThreadsTooLow` (crypto floors).
4. `ErrListenMalformed` / `ErrListenLoopback` / `ErrListenUnspecified` / `ErrListenPublic` (listen-addr; the order distinguishes which check fired).
5. Same family for `health_bind` if explicitly set.
6. `ErrPathPrefixInvalid`.
7. `ErrAuditLogEscape`.
8. `ErrSupervisorTTLOutOfRange`.

---

## Default constants

All defaults are exported `var` declarations. Each maps to one row in `docs/CONFIG-SCHEMA.md`. Drift between this catalogue and that document is a bug.

| Constant                          | Type             | Value                           | CONFIG-SCHEMA row                       |
|-----------------------------------|------------------|---------------------------------|------------------------------------------|
| `DefaultArgonTime`                | `uint32`         | `4`                             | `[crypto] argon_time = 4`                |
| `DefaultArgonMemoryMB`            | `uint32`         | `256`                           | `[crypto] argon_memory_mb = 256`         |
| `DefaultArgonThreads`             | `uint8`          | `4`                             | `[crypto] argon_threads = 4`             |
| `MinArgonTime`                    | `uint32`         | `4`                             | (Constitution III floor)                 |
| `MinArgonMemoryMB`                | `uint32`         | `256`                           | (Constitution III floor)                 |
| `MinArgonThreads`                 | `uint8`          | `4`                             | (Constitution III floor)                 |
| `DefaultJWTTTL`                   | `time.Duration`  | `8 * time.Hour`                 | `[crypto] jwt_default_ttl = "8h"`        |
| `DefaultMaxInteractiveTTL`        | `time.Duration`  | `12 * time.Hour`                | `[crypto] max_interactive_ttl = "12h"`   |
| `DefaultMaxSupervisorTTL`         | `time.Duration`  | `20 * time.Hour`                | `[crypto] max_supervisor_ttl = "20h"`    |
| `DefaultSupervisorTTLMax`         | `time.Duration`  | `24 * time.Hour`                | (v0.1.0 cap from CONFIG-SCHEMA rule)     |
| `DefaultMaxUses`                  | `int`            | `50`                            | `[crypto] default_max_uses = 50`         |
| `DefaultNonceTTL`                 | `time.Duration`  | `60 * time.Second`              | `[crypto] nonce_ttl = "60s"`             |
| `DefaultClockSkew`                | `time.Duration`  | `30 * time.Second`              | `[crypto] clock_skew = "30s"`            |
| `DefaultStateDir`                 | `string`         | `"~/.hush"`                     | `[server] state_dir = "~/.hush"`         |
| `DefaultAuditLog`                 | `string`         | `"~/.hush/audit.jsonl"`         | `[server] audit_log = "~/.hush/audit.jsonl"` |
| `DefaultClientRegistry`           | `string`         | `"~/.hush/clients.json"`        | `[server] client_registry = "~/.hush/clients.json"` |
| `DefaultListenPort`               | `int`            | `7743`                          | (port from CONFIG-SCHEMA examples)        |
| `DefaultRequireTailscale`         | `bool`           | `true`                          | `[network] require_tailscale = true`     |
| `DefaultAllowedCIDRs`             | `[]string`       | `["100.64.0.0/10"]`             | `[network] allowed_cidrs = ["100.64.0.0/10"]` |
| `DefaultRequireFileModeChecks`    | `bool`           | `true`                          | `[security] require_file_mode_checks = true` |
| `DefaultRequireKeychainACL`       | `bool`           | `true`                          | `[security] require_keychain_acl = true` |
| `DefaultRequireNTPSync`           | `bool`           | `true`                          | `[security] require_ntp_sync = true`     |
| `DefaultMaxClockDrift`            | `time.Duration`  | `60 * time.Second`              | `[security] max_clock_drift = "60s"`     |
| `MinPathPrefixLen`                | `int`            | `6`                             | `[server] path_prefix` rules              |
| `MaxPathPrefixLen`                | `int`            | `32`                            | `[server] path_prefix` rules              |
| `TailscaleCGNAT`                  | `netip.Prefix`   | `netip.MustParsePrefix("100.64.0.0/10")` | (Constitution VI)             |

**Mutability contract**: every constant above is set-once at package load. The package contains no code path that writes to any of these variables after package init. External code that mutates them is invoking undefined behaviour and exits the locked contract. The `[]string` (`DefaultAllowedCIDRs`) and `netip.Prefix` (`TailscaleCGNAT`) are `var` not `const` only because Go's type system disallows non-scalar `const`; they are immutable by convention. Inline `//nolint:gochecknoglobals` annotations colocate this contract with the declarations.

---

## Sentinel error catalogue

All sentinels are `var` declarations of types `error`. Compare via `errors.Is`. Wrap relationships per the table.

| Sentinel                       | Wraps                       | Triggered by                                                                         |
|--------------------------------|-----------------------------|--------------------------------------------------------------------------------------|
| `ErrTOMLDecode`                | go-toml/v2 inner error      | go-toml/v2 returned a non-strict-mode decode error (syntax, type mismatch)           |
| `ErrUnknownField`              | go-toml/v2 strict-mode error| `DisallowUnknownFields` rejected a key                                               |
| `ErrMissingRequiredField`      | (none)                      | A required field was absent / empty after decode                                     |
| `ErrInvalidDuration`           | (none)                      | `time.ParseDuration` failed on a duration-shaped field                               |
| `ErrTailscaleBindRequired`     | (umbrella)                  | (parent of the three listen-addr family below)                                       |
| `ErrListenLoopback`            | `ErrTailscaleBindRequired`  | `listen_addr` or `health_bind` is loopback (`127.0.0.1`, `::1`, etc.)                |
| `ErrListenUnspecified`         | `ErrTailscaleBindRequired`  | `listen_addr` or `health_bind` is unspecified (`0.0.0.0`, `[::]`)                    |
| `ErrListenPublic`              | `ErrTailscaleBindRequired`  | `listen_addr` or `health_bind` is routable / outside Tailscale CGNAT                 |
| `ErrListenMalformed`           | (none)                      | `netip.ParseAddrPort` failed on `listen_addr` or `health_bind`                       |
| `ErrTailscaleRequired`         | (none)                      | `[network] require_tailscale = false` (FR-005c)                                      |
| `ErrPathPrefixInvalid`         | (none)                      | `path_prefix` length out of `[6, 32]` OR contains a non-URL-safe character           |
| `ErrAuditLogEscape`            | (none)                      | `audit_log` (after `~`-expansion + `Abs`) does not resolve under `state_dir`         |
| `ErrStateDirNotFound`          | wraps `fs.ErrNotExist`       | `os.Stat(absStateDir)` returned `fs.ErrNotExist`                                      |
| `ErrStateDirUnsafe`            | (none)                      | `os.Stat(absStateDir)` succeeded but `Mode().IsDir() == false`                       |
| `ErrArgonMemoryTooLow`         | (none)                      | `argon_memory_mb < MinArgonMemoryMB` (256)                                           |
| `ErrArgonTimeTooLow`           | (none)                      | `argon_time < MinArgonTime` (4)                                                       |
| `ErrArgonThreadsTooLow`        | (none)                      | `argon_threads < MinArgonThreads` (4)                                                |
| `ErrSupervisorTTLOutOfRange`   | (none)                      | `max_supervisor_ttl ≤ jwt_default_ttl` OR `max_supervisor_ttl > DefaultSupervisorTTLMax (24h)` |

**Multi-violation behaviour**: when `Validate` finds multiple violations in a single config, it returns `errors.Join(err1, err2, ...)`. `errors.Is(joined, ErrXxx)` returns `true` for any sentinel that any of the joined errors wraps. Tests SHOULD assert each expected sentinel individually rather than relying on string-comparison of the joined message.

---

## Behavioural invariants (testable contract)

| Invariant | Spec ref | Test name (in tasks phase) |
|-----------|----------|----------------------------|
| Loading the documented minimal-valid config returns a populated `*Server` with all defaults applied | FR-001, FR-008 | `TestServer_FullMinimalConfig` |
| Loading the documented full-default config returns a `*Server` whose every field equals the documented default | FR-008, SC-001 | `TestServer_FullMaximalConfig` |
| An unknown TOML field returns `ErrUnknownField` | FR-002 | `TestServer_RejectsUnknownField` |
| A misspelled TOML field returns `ErrUnknownField` | FR-002 | (covered by `TestServer_RejectsUnknownField` table) |
| A type mismatch returns `ErrTOMLDecode` | FR-002 (edge) | `TestServer_RejectsWrongType` |
| `listen_addr = "127.0.0.1:7743"` returns `ErrListenLoopback` (and `errors.Is(err, ErrTailscaleBindRequired)`) | FR-003 | `TestServer_RejectsLoopback` |
| `listen_addr = "0.0.0.0:7743"` returns `ErrListenUnspecified` | FR-003 | `TestServer_RejectsUnspecified` |
| `listen_addr = "8.8.8.8:7743"` returns `ErrListenPublic` | FR-003 | `TestServer_RejectsPublic` |
| `listen_addr = "100.96.10.4:7743"` (CGNAT) loads cleanly | FR-003 | `TestServer_AcceptsTailscaleCGNAT` |
| `listen_addr = ""` (empty) returns `ErrMissingRequiredField` | edge case | `TestServer_RejectsMissingListenAddr` |
| `listen_addr = "garbage"` returns `ErrListenMalformed` | FR-003 (edge) | `TestServer_RejectsMalformedListenAddr` |
| `health_bind` outside CGNAT returns the same family of errors | FR-003a | `TestServer_HealthBindRejectsLoopback`, `_RejectsPublic`, etc. |
| `health_bind` absent inherits from `listen_addr` | FR-003a | `TestServer_HealthBindInheritsListenAddr` |
| `argon_memory_mb = 128` returns `ErrArgonMemoryTooLow` | FR-004 | `TestServer_RejectsArgonMemoryUnder256` |
| `argon_memory_mb = 256` loads cleanly | FR-004 | `TestServer_AcceptsArgonMemoryAt256` |
| `argon_time = 1` returns `ErrArgonTimeTooLow` | FR-004 (Constitution III) | `TestServer_RejectsArgonTimeUnder4` |
| `argon_threads = 1` returns `ErrArgonThreadsTooLow` | FR-004 (Constitution III) | `TestServer_RejectsArgonThreadsUnder4` |
| `audit_log = "/etc/passwd"` returns `ErrAuditLogEscape` | FR-005 | `TestServer_RejectsAuditLogOutsideStateDir` |
| `audit_log = "~/.hush/audit.jsonl"` (under state_dir) loads cleanly | FR-005 | `TestServer_AcceptsAuditLogUnderStateDir` |
| `audit_log = "~/.hush/../etc/passwd"` (parent traversal) returns `ErrAuditLogEscape` | FR-005 | `TestServer_RejectsAuditLogParentTraversal` |
| `state_dir` not present on disk returns `ErrStateDirNotFound` | FR-005a | `TestServer_RejectsMissingStateDir` |
| `state_dir` resolves to a regular file returns `ErrStateDirUnsafe` | FR-005a (edge) | `TestServer_RejectsStateDirNotADirectory` |
| `~` expansion + `Abs` happens before path-safety checks | FR-005b | `TestServer_ExpandsTildePathsCorrectly` |
| Path with `$VAR` is treated as literal (not expanded) | FR-005b | `TestServer_DoesNotExpandEnvVars` |
| `require_tailscale = false` returns `ErrTailscaleRequired` | FR-005c | `TestServer_RejectsRequireTailscaleFalse` |
| `require_tailscale = true` (or absent) loads cleanly | FR-005c | `TestServer_AcceptsRequireTailscaleTrue` |
| `path_prefix = "ab"` (too short) returns `ErrPathPrefixInvalid` | FR-005d | `TestServer_RejectsPathPrefixTooShort` |
| `path_prefix = "<33 chars>"` returns `ErrPathPrefixInvalid` | FR-005d | `TestServer_RejectsPathPrefixTooLong` |
| `path_prefix = "abc def"` (space) returns `ErrPathPrefixInvalid` | FR-005d | `TestServer_RejectsPathPrefixBadCharset` |
| `path_prefix = "valid_prefix-1"` loads cleanly | FR-005d | `TestServer_AcceptsValidPathPrefix` |
| Server struct has no field whose value is a secret | FR-006, SC-005 | `TestServer_SchemaHasNoSecretFields` |
| LoadServer does not consult env vars for any secret-bearing field | FR-007, SC-005 | `TestLoadServer_DoesNotReadSecretsFromEnv` |
| Every documented default in `docs/CONFIG-SCHEMA.md` is present and asserted | FR-008, SC-001 | `TestServer_AppliesEveryDocumentedDefault` |
| Loading the same file twice returns equivalent values | FR-011 | `TestLoadServer_Idempotent` |
| Every error returned has at least one sentinel `errors.Is` match | FR-009 | `TestLoadServer_AllErrorsAreSentinels` (over the testdata/invalid corpus) |
| Hostile byte streams do not panic | FR-010, SC-003 | `FuzzServerTOML` (60s gate) |
| `max_supervisor_ttl = jwt_default_ttl` returns `ErrSupervisorTTLOutOfRange` | TTL-out-of-range edge | `TestServer_RejectsSupervisorTTLBelowJWT` |
| `max_supervisor_ttl > 24h` returns `ErrSupervisorTTLOutOfRange` | TTL-out-of-range edge | `TestServer_RejectsSupervisorTTLAboveCap` |
| Multi-violation config returns `errors.Join` with each sentinel matchable | FR-009 (multi) | `TestValidate_MultiViolationJoinsErrors` |

---

## Non-contract (explicit non-promises)

- **No exported decoder type**. `LoadServer` is the only entry point; consumers cannot supply a custom decoder.
- **No exported `materialize` function**. The decode → defaults → validate pipeline is a single internal flow.
- **No partial-load mode**. There is no "load with warnings" variant; failures are errors.
- **No file-mode enforcement**. SDD-06 records the operator's intent; SDD-10 enforces it.
- **No NTP probe**. SDD-06 records the operator's intent; SDD-17 implements the probe.
- **No Keychain access**. SDD-06 records the Keychain item name; SDD-10 fetches the actual token.
- **No environment-variable overrides**. There is no `HUSH_LISTEN_ADDR` or similar; the file is the only source.
- **No JSON / YAML support**. TOML is the only format. A future format change requires a new SDD chunk.
- **No supervisor-config types in this contract**. SDD-18 will add `Supervisor`, `LoadSupervisor`, etc. — those are out of scope for SDD-06.
- **No watch-and-reload**. The loader is single-shot. SIGHUP-driven vault reload (SDD-10) does not call `LoadServer` — config is loaded once at startup.

---

## Deprecation policy

This contract is **frozen** at SDD-06 ship. Adding a new field to `Server` (or its sub-sections) is non-breaking iff the new field has a documented default and the absence-default behaviour is backward-compatible. Removing or renaming any exported symbol requires a new SDD chunk; SDD-10, SDD-15, and SDD-18 all depend on the surface above.
