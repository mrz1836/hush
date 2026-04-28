# Feature Specification: internal/config — server TOML schema + validation

**Feature Branch**: `006-config-server`
**Created**: 2026-04-28
**Status**: Draft
**Input**: User description: "internal/config: load + validate the hush server TOML config; strict schema (unknown fields rejected); Tailscale-only bind enforcement; Argon2id minimum-memory enforcement; audit-log-inside-state-dir enforcement; never reads secrets from env or stores them in the config struct"

## Overview

The `internal/config` package owns the server-side TOML configuration file:
schema, defaults, validation, and path-safety checks. It is loaded once at
server startup and at `hush init`, and produces typed sentinel errors that
guide the operator to a working config without ever crashing on bad input.

This feature does NOT define new operator workflows — the operator continues
to author `~/.hush/config.toml` by hand or via `hush init`. What this feature
does establish is the contract every other startup path depends on: a config
that loads cleanly is a config that is safe to start the vault server with.

## Clarifications

### Session 2026-04-28

- Q: When the configured `state_dir` does not exist on disk, what should `LoadServer` do? → A: Reject with a typed error; never create. Creation is `hush init`'s job.
- Q: How should the loader resolve filesystem paths that begin with `~` (as written in the documented schema examples)? → A: Expand a leading `~` to `$HOME`, then resolve to absolute paths via `filepath.Abs` before path-safety checks. No other shell-style expansion.
- Q: Should the loader reject `[network].require_tailscale = false` at load time? → A: Yes — reject with a distinct typed error. The flag must be `true` (or absent — defaults to true) in v0.1.0.
- Q: When `[network].health_bind` is explicitly set, should it be validated against the same Tailscale CGNAT rules as `listen_addr`? → A: Yes — apply identical Tailscale CGNAT rules (reject loopback, unspecified, public, malformed) to `health_bind` when explicitly set.
- Q: Should the loader enforce the `path_prefix` format (6-32 URL-safe characters) at load time? → A: Yes — enforce length 6-32 and URL-safe charset (`[A-Za-z0-9_-]`) at load time with a typed error (`ErrPathPrefixInvalid`).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator runs the vault server with a valid config (Priority: P1)

The operator has authored a `~/.hush/config.toml` that conforms to the
documented schema. They invoke the server (or a downstream consumer such as
`hush init`) and the configuration loader returns a populated, validated
configuration object. Every absent optional field has been filled with its
documented default. No secret material is present in the returned object.

**Why this priority**: Without a working "happy path", every other startup
flow in the project is blocked. This is the smallest end-to-end slice that
proves the loader exists, the schema is complete, and defaults are applied.

**Independent Test**: Provide a minimal valid TOML file containing only the
required fields. Loading it returns a fully populated configuration whose
optional fields equal the documented defaults; loading the same file twice
returns equivalent values; the returned object contains no secret values.

**Acceptance Scenarios**:

1. **Given** a TOML file containing only the required server fields and a
   Tailscale CGNAT listen address, **When** the configuration is loaded,
   **Then** the loader returns a populated configuration with no error and
   every optional field populated from the documented default.
2. **Given** a TOML file containing every documented field at its documented
   default value, **When** the configuration is loaded, **Then** the loader
   returns successfully and the resulting object's field values match the
   documented defaults exactly.
3. **Given** any successfully loaded configuration, **When** its contents are
   inspected, **Then** no field of the returned object holds a Discord bot
   token, vault passphrase, or any other secret value.

---

### User Story 2 - Operator catches a typo before it reaches production (Priority: P2)

The operator misspells a field name (for example, `lisen_addr` instead of
`listen_addr`) or adds a field the schema does not define. The loader
refuses to silently ignore the unknown field; instead, it returns a typed
error that names the offending field, so the operator can correct the typo
without trial-and-error.

**Why this priority**: Silently dropping an unknown field is the canonical
way a strict-bind config "succeeds" while binding to the wrong address. The
strict-schema gate is the cheapest way to catch operator mistakes that
would otherwise become security incidents.

**Independent Test**: Author a TOML file that is otherwise valid but adds
an extra key (or misspells a known key). Loading the file returns a
distinct, named error identifying the unknown field. No partial
configuration object is returned to the caller.

**Acceptance Scenarios**:

1. **Given** a TOML file that includes a key not defined in the documented
   schema, **When** the configuration is loaded, **Then** the loader
   returns a distinct, named error identifying the unknown field and does
   not return a configuration object.
2. **Given** a TOML file with a misspelled known field (the misspelling is
   itself an unknown field), **When** the configuration is loaded,
   **Then** the loader returns the same unknown-field error.
3. **Given** a TOML file with a known field whose value has the wrong
   type (for example, a string where an integer is expected), **When**
   the configuration is loaded, **Then** the loader returns a distinct,
   typed decode error and does not return a configuration object.

---

### User Story 3 - Operator cannot accidentally weaken the network boundary (Priority: P1)

The operator (or a script) supplies a `listen_addr` that is loopback, the
unspecified address, or a public/routable IP. The loader refuses the
configuration with a distinct, named error per category. Only addresses
that resolve to a Tailscale CGNAT host (the documented `100.64.0.0/10`
range) are accepted. This is non-negotiable per the project's network
boundary principle.

**Why this priority**: A vault server bound outside Tailscale defeats the
entire product. This rule is enforced at the same priority as the happy
path because it is the load-bearing constraint of the threat model — a
loadable-but-unsafe config must not exist.

**Independent Test**: For each rejected address category (loopback,
unspecified, public IP), supply an otherwise valid TOML file. Loading the
file returns the corresponding distinct, named error. For an accepted
Tailscale CGNAT address, the same file loads cleanly.

**Acceptance Scenarios**:

1. **Given** a TOML file whose `listen_addr` host is loopback (for example
   `127.0.0.1` or `::1`), **When** the configuration is loaded, **Then**
   the loader returns a named "Tailscale bind required" error and does
   not return a configuration object.
2. **Given** a TOML file whose `listen_addr` host is the unspecified
   address (for example `0.0.0.0` or `[::]`), **When** the configuration
   is loaded, **Then** the loader returns the named "Tailscale bind
   required" error.
3. **Given** a TOML file whose `listen_addr` host is a public/routable IP
   (for example a real internet address), **When** the configuration is
   loaded, **Then** the loader returns the named "Tailscale bind
   required" error.
4. **Given** a TOML file whose `listen_addr` host is inside the Tailscale
   CGNAT range `100.64.0.0/10`, **When** the configuration is loaded,
   **Then** the loader accepts the address.
5. **Given** a TOML file whose `listen_addr` is syntactically malformed
   (missing port, malformed host literal), **When** the configuration is
   loaded, **Then** the loader returns a distinct, named address-syntax
   error.

---

### User Story 4 - Operator cannot weaken Argon2id below the floor (Priority: P1)

The operator supplies an `argon_memory_mb` value below the documented
minimum of 256 MiB. The loader refuses with a distinct, named error so
the operator cannot accidentally trade off encryption strength for boot
speed.

**Why this priority**: The Argon2id memory floor is a constitutional
non-negotiable. A loadable-but-weakened crypto parameter is the same
threat shape as a loadable-but-unsafe bind: visible at config time, fatal
at runtime.

**Independent Test**: Supply a TOML file whose `argon_memory_mb` is below
the documented minimum. Loading returns the named "Argon memory too low"
error. A file whose `argon_memory_mb` is at or above the minimum loads
cleanly.

**Acceptance Scenarios**:

1. **Given** a TOML file whose `argon_memory_mb` is set to a value below
   the documented minimum (256 MiB), **When** the configuration is
   loaded, **Then** the loader returns a named "Argon memory too low"
   error and does not return a configuration object.
2. **Given** a TOML file whose `argon_memory_mb` is exactly 256 MiB,
   **When** the configuration is loaded, **Then** the loader accepts the
   value.
3. **Given** a TOML file that omits `argon_memory_mb`, **When** the
   configuration is loaded, **Then** the loader applies the documented
   default of 256 MiB.

---

### User Story 5 - Operator cannot redirect the audit log out of the state directory (Priority: P1)

The audit log path supplied in the configuration must resolve to a
location underneath the state directory. Any path that escapes the
state directory (via absolute path, parent traversal, or any other
means) is rejected with a distinct, named error.

**Why this priority**: The audit log holds the only signed record of
"who fetched what, when". Allowing the operator to redirect it to an
arbitrary filesystem location turns a security-of-record file into a
path-traversal write primitive.

**Independent Test**: Supply a TOML file whose `audit_log` path is
outside the configured `state_dir`. Loading returns the named
"audit log escape" error. Supply a TOML file whose `audit_log` path
is underneath `state_dir` and loading succeeds.

**Acceptance Scenarios**:

1. **Given** a TOML file whose `audit_log` resolves to a path outside
   the configured `state_dir` (for example, an absolute path elsewhere
   on the filesystem), **When** the configuration is loaded, **Then**
   the loader returns a named "audit log escape" error and does not
   return a configuration object.
2. **Given** a TOML file whose `audit_log` uses parent-directory
   segments (`..`) that resolve to a location outside `state_dir`,
   **When** the configuration is loaded, **Then** the loader returns
   the same named error.
3. **Given** a TOML file whose `audit_log` resolves to a file directly
   underneath `state_dir`, **When** the configuration is loaded,
   **Then** the loader accepts the path.

---

### User Story 6 - Operator cannot smuggle secrets through the config (Priority: P1)

The configuration schema does not contain any field whose value is a
secret. Discord bot tokens, vault passphrases, and similar material are
fetched at runtime from the macOS Keychain via the configured keychain
item name. The loader does not consult environment variables for any
secret-bearing field, regardless of the variable's name.

**Why this priority**: Storing a secret in a plaintext config file or
reading it from the environment recreates the exact attack the product
exists to eliminate. The loader is the gate that keeps that mistake
impossible to make.

**Independent Test**: Inspect the public configuration schema and
confirm it contains no secret-typed fields — only keychain item names
and other non-secret pointers. Set environment variables that name
likely secret fields (for example, a bot token) and load a TOML file
that does not contain those values; the loaded configuration must not
pick up the environment values.

**Acceptance Scenarios**:

1. **Given** the documented configuration schema, **When** the schema is
   inspected, **Then** no field's value is a secret — fields that
   reference secret material (for example, the Discord bot token) hold
   only a keychain item name.
2. **Given** a TOML file that omits any secret-bearing field and an
   environment that defines variables likely to contain secrets,
   **When** the configuration is loaded, **Then** no field of the
   returned configuration object holds a value that originated from
   the environment.
3. **Given** any successfully loaded configuration, **When** the
   configuration is logged or otherwise serialised for diagnostics,
   **Then** no secret value can appear in the output because no secret
   value is present in the configuration object at all.

---

### Edge Cases

- A TOML file that is syntactically invalid (truncated, malformed table
  header, mismatched quoting) returns a distinct, named decode error
  and never returns a partial configuration object.
- A TOML file whose `listen_addr` is empty or missing returns a named
  "missing required field" error rather than a generic decode error.
- A TOML file whose `state_dir` does not exist on disk is rejected
  with a distinct, named error; the loader never creates the
  directory itself. Creating `state_dir` with mode `0700` is the
  responsibility of `hush init` (SDD-15), not the loader.
- A TOML file whose `audit_log` path lies outside `state_dir` after
  resolving any parent-directory segments is treated as an out-of-tree
  path and rejected.
- A TOML file with conflicting `network` constraints (for example
  `require_tailscale = true` together with an `allowed_cidrs` list that
  does not include any Tailscale range) returns a named consistency
  error.
- A TOML file whose `max_supervisor_ttl` is less than or equal to
  `jwt_default_ttl`, or whose `max_supervisor_ttl` exceeds the
  documented v0.1.0 cap of 24h, returns a named TTL-out-of-range error.
- Random or hostile byte streams supplied to the loader (fuzz inputs)
  do not panic, do not exhaust memory, and always produce a typed
  error.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST expose a single, documented schema for the
  server-side configuration whose fields exactly match the schema
  documented in the project's configuration reference. No undocumented
  field may be silently accepted.
- **FR-002**: The configuration loader MUST reject any TOML file that
  contains a field not defined in the documented schema, returning a
  distinct, named error that identifies the offending field.
- **FR-003**: The configuration loader MUST validate that the configured
  listen address resolves to a Tailscale CGNAT host (within
  `100.64.0.0/10`). Loopback, the unspecified address, and any
  public/routable IP MUST each be rejected with a distinct, named error.
- **FR-003a**: When `[network].health_bind` is explicitly supplied, the
  configuration loader MUST validate it under the same rules as
  `listen_addr` (Tailscale CGNAT membership; reject loopback,
  unspecified, public/routable, and malformed addresses with the same
  named errors). When `health_bind` is absent, it inherits the value of
  `listen_addr`, which has already passed those checks.
- **FR-004**: The configuration loader MUST reject any configured
  Argon2id memory parameter strictly below 256 MiB, returning a
  distinct, named error.
- **FR-005**: The configuration loader MUST validate that the configured
  audit log path resolves to a location underneath the configured state
  directory; any out-of-tree audit log path MUST be rejected with a
  distinct, named error.
- **FR-005a**: The configuration loader MUST reject any configuration
  whose `state_dir` does not exist on disk, returning a distinct, named
  error. The loader MUST NOT create, modify, or change the permissions
  of the state directory itself; that responsibility belongs to
  `hush init`.
- **FR-005b**: The configuration loader MUST expand a leading `~` in any
  filesystem path field to the operator's home directory and then
  resolve each path to its absolute form before applying path-safety
  checks. The loader MUST NOT perform any other shell-style expansion
  (no `$VAR`, no nested `~user`, no glob expansion).
- **FR-005c**: The configuration loader MUST reject any configuration
  whose `[network].require_tailscale` is set to `false`, returning a
  distinct, named error. An absent value MUST default to `true` per
  the configuration reference; the loader does not accept any other
  value in v0.1.0.
- **FR-005d**: The configuration loader MUST reject any `path_prefix`
  that is fewer than 6 characters, more than 32 characters, or
  contains any character outside the URL-safe set
  (`A-Z`, `a-z`, `0-9`, `_`, `-`). The rejection MUST be a distinct,
  named error.
- **FR-006**: The configuration schema MUST NOT contain any field whose
  value is a secret. Fields that reference secret material MUST hold
  only a non-secret pointer (such as a Keychain item name).
- **FR-007**: The configuration loader MUST NOT consult environment
  variables for any secret-bearing field. The same configuration file
  MUST produce the same loaded configuration regardless of the calling
  process's environment.
- **FR-008**: The configuration loader MUST apply the documented default
  for every optional field that is absent from the supplied TOML file.
  The applied default MUST exactly match the value documented in the
  configuration reference.
- **FR-009**: The configuration loader MUST return typed sentinel errors
  for every defined rejection category (unknown field, address
  rejection per category, Argon2id minimum, audit-log escape, missing
  required field, malformed value, TTL-out-of-range, consistency
  violation). Generic or untyped errors are not acceptable for any
  documented rejection.
- **FR-010**: The configuration loader MUST tolerate hostile byte
  streams without panicking or exhausting memory; every malformed
  input MUST produce a typed error.
- **FR-011**: The configuration loader MUST be safe to call once at
  process startup and once during `hush init`; the same input MUST
  produce equivalent output across calls. The loader MUST NOT install
  process-wide state.
- **FR-012**: Operator-facing error messages from the loader MUST name
  the offending field or path and MUST NOT include any secret value
  read from any source.

### Key Entities

- **Server configuration**: The complete, validated representation of
  the server-side config file. Holds non-secret pointers (such as
  Keychain item names) but no secret values. Produced by a successful
  load; absent on any rejection.
- **Defaults catalogue**: The set of typed values the loader applies
  to optional fields when the operator omits them. Sourced from the
  configuration reference; equality with that reference is part of the
  contract.
- **Sentinel error**: A named, comparable error value returned by the
  loader for each documented rejection category. Operators (and tests)
  identify rejection causes by sentinel identity, never by message
  string parsing.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every field documented in the project's configuration
  reference is represented in the loaded configuration shape, and every
  documented default is asserted by an automated test. Coverage of the
  configuration package is at least 95%.
- **SC-002**: Each defined rejection category (unknown field, loopback
  bind, unspecified bind, public-IP bind, malformed address, Argon2id
  memory below floor, audit-log escape, missing required field,
  TTL-out-of-range, network consistency violation, `state_dir` missing,
  `require_tailscale = false`, invalid `path_prefix`, and the same
  address-rejection categories applied to an explicit `health_bind`)
  is exercised by at least one passing automated test that asserts the
  corresponding sentinel error.
- **SC-003**: A 60-second random-input fuzz run against the loader
  completes without panic, without exhausting memory, and produces only
  typed errors for every malformed input.
- **SC-004**: An operator who introduces a single typo or schema
  violation in their configuration file receives a single, named error
  identifying the offending field within the same load attempt — no
  trial-and-error is required to discover what is wrong.
- **SC-005**: An external review of the loaded configuration shape
  confirms that no field of the configuration object holds a secret
  value and that the loader does not read any secret-bearing field
  from the process environment.

## Assumptions

- The configuration reference (`docs/CONFIG-SCHEMA.md` server section)
  is authoritative for the schema, defaults, and validation rules. Any
  divergence between the loader's behaviour and that document is a bug
  in the loader.
- The operator authors `~/.hush/config.toml` directly (or via
  `hush init`); there is no admin UI, remote provisioning, or
  templated overlay in v0.1.0.
- Secret material (Discord bot token, vault passphrase) is held in the
  macOS Keychain on the trusted host and fetched at runtime by callers
  outside this package; this package only carries the keychain item
  name.
- Path-safety semantics ("inside the state directory") use absolute
  path resolution as captured in the configuration reference; the
  exact resolution mechanism is plan-phase, but the user-visible
  guarantee is "no out-of-tree audit log".
- The Tailscale CGNAT range is `100.64.0.0/10` per the configuration
  reference and constitution; this range is the only acceptable
  network for the listen address in v0.1.0.
- The `internal/logging` package (SDD-05) is available for any
  diagnostic surface the loader needs; the loader does not introduce
  its own logger.
- This package is consumed by the server entry point (SDD-10) and by
  `hush init` (SDD-15); both consumers receive the loaded
  configuration object and the same set of sentinel errors.
- This specification only defines load-time behaviour. Runtime startup
  hardening (file-mode checks, NTP sync, Keychain ACL verification) is
  performed by SDD-10 against the loaded configuration; those checks
  are out of scope for this package.
