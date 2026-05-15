# Feature Specification: internal/supervise/config — per-supervisor TOML schema + validation

**Feature Branch**: `018-supervise-config`
**Created**: 2026-05-05
**Status**: Draft
**Input**: User description: "internal/supervise/config: load + validate per-supervisor TOML; strict schema with unknown-field rejection; fixed validator allow-list (anthropic, anthropic-oauth, openai, google-ai, github); grace window cap (≤4h); refresh window format and ordering enforcement; child command first element must be absolute"

## Overview

The `internal/supervise/config` package owns the per-supervisor TOML
configuration: the daemon's identity, child command, validators, grace
window, refresh window, watchdog patterns, and Discord routing. One
file describes one long-running daemon; one supervisor process loads
exactly one such file at startup.

This package is the type-and-validation gateway every downstream
supervisor component depends on (state machine, child runner,
refresh/refill loop, status socket, watchdog, alerts). A supervisor
config that loads cleanly is a config that is safe to start a
supervisor with — every constitutional non-negotiable that can be
checked at parse time is checked here, so no later code path needs
to re-prove the same property.

This feature does NOT define new operator workflows — the operator
continues to author `~/.hush/supervisors/<name>.toml` by hand. What
this feature establishes is the contract every supervisor lifecycle
chunk (state machine, child fork/exec, refresh, validators, watchdog,
alerts) consumes: a strict, defaulted, validated configuration object
that returns a typed sentinel error for every documented rejection
category.

## Clarifications

### Session 2026-05-05

- Q: Which value does the loader compare `requested_ttl` against for FR-010, given it cannot read the server's `[crypto].max_supervisor_ttl`? → A: The absolute v0.1.0 ceiling of `24h` (the documented `max_supervisor_ttl` upper bound); the server enforces stricter values at claim time.
- Q: How does the loader couple `[validators]` map keys with the `scope` list? → A: It does not. The loader validates only that `[validators]` *values* are in the fixed allow-list; coupling between validator keys and `scope` entries is the responsibility of downstream supervisor components, not this package.
- Q: When `cache_secrets_for_restart = false` but `cache_grace_ttl` is explicitly set, what does the loader do? → A: Reject with a distinct, named "grace TTL without cache" sentinel error — explicit contradictory configuration surfaces at load time rather than being silently ignored.
- Q: When the entire `[watchdog]` section is absent from the TOML, what does the loader produce? → A: Apply documented defaults to every watchdog field (`enabled = true`, `max_alerts_per_hour = 6`, `patterns = []`); watchdog runs by default and operators must write `enabled = false` explicitly to disable it.
- Q: How strict is `server_url` validation at load time? → A: Syntactic-plus-scheme — the value MUST parse as a URL with a non-empty host AND a scheme of `http` or `https`. Deeper semantic checks (Tailscale CIDR membership, port `7743`, `/h/<prefix>` path) are deferred to downstream supervisor startup hardening, consistent with the spec's existing assumption that runtime-level hardening is out of scope for this package.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator starts a supervisor with a valid config (Priority: P1)

The operator has authored `~/.hush/supervisors/<name>.toml` that
conforms to the documented supervisor schema. They invoke the
supervisor and the configuration loader returns a populated, validated
configuration object. Every absent optional field has been filled with
its documented default. No secret material is present in the returned
object.

**Why this priority**: Without a working "happy path", every other
supervisor lifecycle scenario is blocked. This is the smallest
end-to-end slice that proves the loader exists, the schema covers
every documented field, and defaults are applied.

**Independent Test**: Provide a minimal valid TOML file containing
only the required supervisor fields. Loading it returns a fully
populated configuration whose optional fields equal the documented
defaults; loading the same file twice returns equivalent values; the
returned object contains no secret values.

**Acceptance Scenarios**:

1. **Given** a TOML file containing only the required supervisor
   fields (name, reason, server URL, client machine index,
   session type, requested TTL, refresh window, status socket,
   PID file, child command, scope, validators), **When** the
   configuration is loaded, **Then** the loader returns a populated
   configuration with no error and every optional field populated
   from the documented default.
2. **Given** a TOML file containing every documented supervisor
   field at its documented default value, **When** the configuration
   is loaded, **Then** the loader returns successfully and the
   resulting object's field values match the documented defaults
   exactly.
3. **Given** any successfully loaded supervisor configuration,
   **When** its contents are inspected, **Then** no field of the
   returned object holds a Discord bot token, vault passphrase,
   API credential, or any other secret value.

---

### User Story 2 - Operator catches a typo before it reaches production (Priority: P1)

The operator misspells a field name (for example, `refrsh_window`
instead of `refresh_window`), or adds a section the schema does not
define. The loader refuses to silently ignore the unknown field;
instead, it returns a typed error that names the offending field, so
the operator can correct the typo without trial-and-error.

**Why this priority**: Silently dropping an unknown field is the
canonical way a supervisor "succeeds" while running with an unintended
configuration (wrong refresh window, missing watchdog, ignored grace
cache). The strict-schema gate is the cheapest way to catch operator
mistakes that would otherwise become Discord noise or silent staleness.

**Independent Test**: Author a TOML file that is otherwise valid but
adds an extra key (or misspells a known key). Loading the file
returns a distinct, named error identifying the unknown field. No
partial configuration object is returned to the caller.

**Acceptance Scenarios**:

1. **Given** a TOML file that includes a key not defined in the
   documented supervisor schema (root, `[child]`, `[discord]`,
   `[validators]`, or `[watchdog]`), **When** the configuration is
   loaded, **Then** the loader returns a distinct, named error
   identifying the unknown field and does not return a configuration
   object.
2. **Given** a TOML file with a misspelled known field (the
   misspelling is itself an unknown field), **When** the configuration
   is loaded, **Then** the loader returns the same unknown-field
   error.
3. **Given** a TOML file with a known field whose value has the
   wrong type (for example, a string where an integer is expected),
   **When** the configuration is loaded, **Then** the loader returns
   a distinct, typed decode error and does not return a configuration
   object.

---

### User Story 3 - Operator cannot declare an unsupported validator (Priority: P1)

The `[validators]` section maps each scoped secret name to a validator
type that runs on the supervisor host. The set of supported validator
types is fixed and documented: `anthropic`, `anthropic-oauth`,
`openai`, `google-ai`, `github`. Any other validator name MUST be
rejected at load time with a distinct, named error that identifies the
offending validator name (the name only, never any associated secret
material).

**Why this priority**: An unrecognised validator is one of two things,
both unacceptable: an operator typo (the intended validator never
runs, staleness goes silent, the operator is paged hours later) or a
bug-induced reference to a validator that does not exist (the
supervisor would crash on first refresh). Either way, the safe,
visible failure is to refuse the config.

**Independent Test**: Author a TOML file whose `[validators]` table
contains a name outside the documented allow-list (for example,
`"anthropc"` or `"slack"`). Loading the file returns a distinct,
named "unknown validator" error that includes the rejected validator
name. Loading a file whose `[validators]` uses only allow-listed
names succeeds.

**Acceptance Scenarios**:

1. **Given** a TOML file whose `[validators]` table assigns a secret
   name to a validator type outside the documented allow-list,
   **When** the configuration is loaded, **Then** the loader returns
   a distinct, named "unknown validator" error and does not return
   a configuration object.
2. **Given** a TOML file whose `[validators]` table uses each of the
   five allow-listed validator types in turn, **When** the
   configuration is loaded, **Then** the loader accepts the file in
   every case.
3. **Given** any "unknown validator" error returned by the loader,
   **When** the error is read, **Then** the error names the
   offending validator (so the operator can fix the typo) and does
   NOT include any secret value, scope value, or other credential
   material.

---

### User Story 4 - Operator cannot extend the daily grace window beyond the cap (Priority: P1)

The optional grace cache lets a supervisor briefly hold mlocked secret
material across a child restart so that a 3am crash does not require a
fresh Discord approval. The size of that window is bounded: a
configured grace window strictly greater than four hours MUST be
rejected with a distinct, named error. The cap is constitutional —
the project's TTL discipline forbids any single approval from covering
more than a day's worth of activity, and the four-hour cap is the
documented ceiling for the grace-cache subset of that day.

**Why this priority**: A loadable-but-overlong grace window is the
same threat shape as a loadable-but-unsafe network bind: visible at
config time, fatal at runtime. A 6-hour or 12-hour grace cache turns
a one-time approval into a multi-shift access window, defeats the
audit boundary, and silently shifts the project's threat model.

**Independent Test**: Author a TOML file whose grace window is set
to a value strictly greater than four hours. Loading returns a named
"grace window too long" error. A file whose grace window is at or
below four hours loads cleanly. A file that omits the grace window
applies the documented default.

**Acceptance Scenarios**:

1. **Given** a TOML file whose configured grace window is strictly
   greater than four hours (for example, `5h`, `12h`, `24h`),
   **When** the configuration is loaded, **Then** the loader returns
   a distinct, named "grace window too long" error and does not
   return a configuration object.
2. **Given** a TOML file whose configured grace window is exactly
   four hours, **When** the configuration is loaded, **Then** the
   loader accepts the value.
3. **Given** a TOML file that omits the grace window field, **When**
   the configuration is loaded, **Then** the loader applies the
   documented default and that default is at most four hours.

---

### User Story 5 - Operator cannot misformat or invert the refresh window (Priority: P1)

The daily refresh window pins the supervisor's once-a-day Discord
approval prompt to a configured local-time band (for example, the
operator's morning routine). The configured value MUST be a string of
the form `HH:MM-HH:MM`, where the start time is strictly earlier than
the end time. Two distinct rejection categories exist:

1. **Format error** — the string does not match `HH:MM-HH:MM`
   (missing dash, missing colon, non-numeric component, hour or
   minute out of range).
2. **Ordering error** — the string parses cleanly, but the start
   time is greater than or equal to the end time.

Each category MUST produce its own named, sentinel error so the
operator sees exactly which mistake was made.

**Why this priority**: A refresh window is the only knob the operator
has to anchor "when am I willing to be paged". A silent
misinterpretation (start >= end producing a zero-length or wrap-around
window) means the daily nudge never fires, the supervisor falls
through to TTL expiry, and the next prompt arrives at an unpredictable
time. Visible failure at load time is the only acceptable behaviour.

**Independent Test**: For each rejection category (format error,
ordering error), supply an otherwise valid TOML file. Loading the
file returns the category-specific named error. For an in-format,
in-order window, the same file loads cleanly.

**Acceptance Scenarios**:

1. **Given** a TOML file whose refresh window value does not match
   the `HH:MM-HH:MM` shape (for example, `"9-10"`, `"09:00 to 10:00"`,
   `"09:00-10"`, `"09:00-25:00"`, `"99:99-99:99"`), **When** the
   configuration is loaded, **Then** the loader returns a distinct,
   named "refresh window format" error and does not return a
   configuration object.
2. **Given** a TOML file whose refresh window parses cleanly but
   whose start time is greater than or equal to its end time (for
   example, `"10:00-09:00"` or `"09:00-09:00"`), **When** the
   configuration is loaded, **Then** the loader returns a distinct,
   named "refresh window order" error — separate from the format
   error — and does not return a configuration object.
3. **Given** a TOML file whose refresh window is in format and in
   order (for example, `"09:00-10:00"`), **When** the configuration
   is loaded, **Then** the loader accepts the value.

---

### User Story 6 - Operator cannot smuggle a child command through a shell (Priority: P1)

The supervisor starts a single child process and inherits its
lifecycle. The child command is supplied as an explicit argument
vector, not a shell string; the first element of that vector MUST be
an absolute path. A relative path, a bare command name, an empty
vector, or anything that would imply a shell `PATH` lookup MUST be
rejected at load time with a distinct, named error.

**Why this priority**: The supervisor runs with whatever privileges
its host process has and injects scoped secrets into the child's
environment. A relative command name resolved via `PATH` is a
classic privilege-escalation vector — anyone able to write to a
directory earlier on `PATH` can hijack the daemon. An absolute path
removes the ambiguity at parse time and removes the entire shell-
parsing surface.

**Independent Test**: Author a TOML file whose `[child].command`
first element is a relative path (for example, `"my-daemon"` or
`"./run.sh"`). Loading returns the named "command path relative"
error. A file whose first element is an absolute path loads cleanly.
An empty `command` vector returns a distinct "command empty" error.

**Acceptance Scenarios**:

1. **Given** a TOML file whose `[child].command` first element is
   a relative path (no leading `/`), **When** the configuration is
   loaded, **Then** the loader returns a distinct, named "command
   path relative" error and does not return a configuration object.
2. **Given** a TOML file whose `[child].command` is an empty array,
   **When** the configuration is loaded, **Then** the loader returns
   a distinct, named "command empty" error.
3. **Given** a TOML file whose `[child].command` first element is
   an absolute path (leading `/`), **When** the configuration is
   loaded, **Then** the loader accepts the file and the loaded
   command vector preserves every element verbatim (no shell parsing,
   no quoting changes, no element splitting).

---

### User Story 7 - Operator cannot start a supervisor with no scoped secrets (Priority: P2)

A supervisor's reason for existing is to deliver a defined set of
secrets to a child process under a single Discord approval. A
configuration with an empty `scope` list — or one that omits the
field entirely — is not a meaningful supervisor; it is a buggy
config. The loader MUST reject it with a distinct, named error.

**Why this priority**: Catching this at load time prevents a class of
silent failures in which the supervisor starts, completes a Discord
approval, and then injects nothing into the child. The child either
crashes loudly or — worse — runs with environment variables inherited
from the parent shell, exactly the dotfile-secret pattern the project
exists to eliminate.

**Independent Test**: Author a TOML file whose top-level `scope`
field is an empty array (or absent). Loading returns the named
"scope empty" error. A file with at least one scoped secret name
loads cleanly.

**Acceptance Scenarios**:

1. **Given** a TOML file whose `scope` is an empty array, **When**
   the configuration is loaded, **Then** the loader returns a
   distinct, named "scope empty" error.
2. **Given** a TOML file that omits the `scope` field entirely,
   **When** the configuration is loaded, **Then** the loader returns
   the same "scope empty" error (absence and emptiness are equivalent
   for this gate).
3. **Given** a TOML file whose `scope` lists one or more secret
   names, **When** the configuration is loaded, **Then** the loader
   accepts the file.

---

### User Story 8 - Operator cannot smuggle secrets through the supervisor config (Priority: P1)

The supervisor configuration schema does not contain any field whose
value is a secret. Discord channel IDs, Discord application IDs,
keychain item names, file paths, and validator type names are all
non-secret pointers; the actual API tokens, OAuth credentials, and
vault material are fetched at runtime from the vault server (over
Tailscale, after Discord approval) and injected into the child
process's environment. The loader does not consult environment
variables for any field, secret-bearing or otherwise — the same
configuration file MUST produce the same loaded configuration in
every environment.

**Why this priority**: The supervisor is a process that holds plaintext
secrets in memory by design; pushing any one of those secrets into
the supervisor's own configuration file recreates the exact attack
the project exists to eliminate (file-resident credentials on a
machine that runs untrusted code paths).

**Independent Test**: Inspect the public supervisor configuration
schema and confirm it contains no secret-typed fields. Set environment
variables that name plausible supervisor fields (for example,
`HUSH_REASON`, `HUSH_REFRESH_WINDOW`) and load a TOML file that
defines those values explicitly; the loaded configuration's values
MUST come from the file, not the environment.

**Acceptance Scenarios**:

1. **Given** the documented supervisor schema, **When** the schema
   is inspected, **Then** no field's value is a secret — every field
   that references credential material does so through a non-secret
   pointer (a scoped secret name, a keychain item name, a Discord
   channel ID, a validator type name).
2. **Given** any successfully loaded supervisor configuration,
   **When** the configuration is logged or otherwise serialised for
   diagnostics, **Then** no secret value can appear in the output
   because no secret value is present in the configuration object at
   all.
3. **Given** a TOML file and a process environment that defines
   variables sharing names with supervisor fields, **When** the
   configuration is loaded, **Then** the loaded values come from
   the file alone; the environment has no effect on the result.

---

### Edge Cases

- A TOML file that is syntactically invalid (truncated, malformed
  table header, mismatched quoting) returns a distinct, named decode
  error and never returns a partial configuration object.
- A TOML file whose required fields (`name`, `reason`, `server_url`,
  `client_machine_index`, `session_type`, `requested_ttl`,
  `refresh_window`, `status_socket`, `pid_file`, `[child].command`,
  `scope`, `[validators]`) are missing returns a named
  "missing required field" error per offending field rather than a
  generic decode error.
- A TOML file whose `session_type` is anything other than the fixed
  value `"supervisor"` is rejected with a distinct, named error.
  This config type exists to describe one daemon; an interactive
  session does not load this file.
- A TOML file whose `requested_ttl` exceeds the documented v0.1.0 cap
  (the server-side maximum supervisor TTL) is rejected with a
  distinct, named error, even though the server would also enforce
  the cap at claim time — visible failure at load time spares the
  operator a Discord round-trip.
- A TOML file whose grace cache is enabled (`cache_secrets_for_restart
  = true`) but whose grace window is absent, malformed, or out of
  range is rejected with the appropriate grace-window error.
- A TOML file whose grace cache is disabled (`cache_secrets_for_restart
  = false` or absent) but whose `cache_grace_ttl` is explicitly set
  is rejected with a distinct, named "grace TTL without cache" error.
  Contradictory configuration MUST NOT be silently ignored.
- A TOML file whose `refresh_window` parses cleanly but whose times
  cross midnight (start time later in the clock-day than end time)
  is rejected as an ordering error — wrap-around windows are not
  supported in v0.1.0.
- A TOML file whose `[child].command` first element is absolute but
  whose remaining elements are empty strings is accepted; element-
  level content beyond the absolute-path requirement is the child
  process's contract, not the loader's.
- A TOML file whose `pid_file` or `status_socket` path is an empty
  string is rejected with a distinct, named "missing required field"
  error.
- A TOML file whose `server_url` is empty, fails URL parsing, has an
  empty host, or has a scheme other than `http`/`https` is rejected
  with a distinct, named "server URL invalid" error. Deeper checks
  (Tailscale CIDR, port `7743`, `/h/<prefix>` path) are deferred to
  downstream supervisor startup hardening and are NOT performed by
  this loader.
- A TOML file whose `log_level` is set to a value outside the
  documented allow-list (`debug`, `info`, `warn`, `error`) is
  rejected with a distinct, named "log level invalid" error; an
  absent `log_level` applies the documented default.
- A TOML file whose `[watchdog].max_alerts_per_hour` is zero or
  negative is rejected with a distinct, named error; an absent value
  applies the documented default.
- A TOML file that omits the entire `[watchdog]` section is accepted
  and the loader applies the documented default for every watchdog
  field (`enabled = true`, `max_alerts_per_hour = 6`, `patterns = []`).
  Section absence is equivalent to all fields absent; operators
  disable the watchdog by writing `[watchdog] enabled = false`
  explicitly.
- Random or hostile byte streams supplied to the loader (fuzz inputs)
  do not panic, do not exhaust memory, and always produce a typed
  error.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST expose a single, documented schema for
  the per-supervisor configuration whose fields exactly match the
  schema documented in the project's configuration reference (root
  fields plus `[child]`, `[discord]`, `[validators]`, `[watchdog]`
  sections). No undocumented field may be silently accepted.
- **FR-002**: The configuration loader MUST reject any TOML file that
  contains a field not defined in the documented supervisor schema
  (whether at the root or inside any of the documented sections),
  returning a distinct, named error that identifies the offending
  field.
- **FR-003**: The configuration loader MUST validate that every
  validator *value* declared in `[validators]` (i.e., the validator
  type name on the right-hand side of each entry) is from the fixed
  allow-list `{anthropic, anthropic-oauth, openai, google-ai,
  github}`. Any other validator value MUST be rejected with a
  distinct, named "unknown validator" error that includes the
  rejected validator value (and no secret value). The loader does
  NOT enforce any coupling between `[validators]` map keys and the
  top-level `scope` list — that coupling is the responsibility of
  downstream supervisor components.
- **FR-004**: The configuration loader MUST reject any configured
  grace window strictly greater than four hours, returning a
  distinct, named "grace window too long" error.
- **FR-005**: The configuration loader MUST validate the
  `refresh_window` field against the format `HH:MM-HH:MM` (24-hour
  clock, leading zeros, single dash separator) and MUST reject
  format violations with a distinct, named "refresh window format"
  error.
- **FR-006**: The configuration loader MUST validate that the
  parsed `refresh_window` start time is strictly earlier than the
  parsed end time and MUST reject violations with a distinct, named
  "refresh window order" error — separate from the format error in
  FR-005.
- **FR-007**: The configuration loader MUST validate that
  `[child].command` is a non-empty array of strings whose first
  element is an absolute path. Each violation category MUST produce
  its own distinct, named error: an empty command vector returns
  "command empty"; a non-absolute first element returns "command
  path relative".
- **FR-008**: The configuration loader MUST reject any configuration
  whose top-level `scope` is missing or empty, returning a distinct,
  named "scope empty" error. Absence and emptiness are equivalent
  for this gate.
- **FR-009**: The configuration loader MUST reject any configuration
  whose `session_type` is anything other than the documented fixed
  value (`"supervisor"`), returning a distinct, named error.
- **FR-010**: The configuration loader MUST reject any configuration
  whose `requested_ttl` exceeds the documented v0.1.0 supervisor-TTL
  ceiling of `24h` (the upper bound documented for the server's
  `max_supervisor_ttl`), returning a distinct, named "TTL out of
  range" error. The loader does not read the server's actual
  `max_supervisor_ttl`; the server enforces any stricter value at
  claim time.
- **FR-011**: The configuration loader MUST reject any configuration
  whose `cache_secrets_for_restart` is enabled but whose grace
  window field is absent, malformed, or out of range, surfacing the
  underlying grace-window error category. Conversely, a configuration
  with `cache_secrets_for_restart = false` (or absent) that
  explicitly sets `cache_grace_ttl` MUST be rejected with a distinct,
  named "grace TTL without cache" error; the loader MUST NOT silently
  ignore contradictory settings.
- **FR-012**: The configuration loader MUST reject any configuration
  whose `[watchdog].max_alerts_per_hour` is zero or negative,
  returning a distinct, named error.
- **FR-013**: The configuration loader MUST reject any configuration
  whose `log_level` (if present) is not one of the documented
  allow-list values (`debug`, `info`, `warn`, `error`), returning a
  distinct, named "log level invalid" error.
- **FR-013a**: The configuration loader MUST reject any configuration
  whose `server_url` is empty, unparseable as a URL, has an empty
  host, or has a scheme other than `http` or `https`, returning a
  distinct, named "server URL invalid" error. Deeper semantic checks
  (Tailscale CIDR membership, port `7743`, `/h/<prefix>` path shape)
  are NOT performed at load time; they are the responsibility of
  downstream supervisor startup hardening.
- **FR-014**: The supervisor configuration schema MUST NOT contain
  any field whose value is a secret. Fields that reference secret
  material MUST hold only a non-secret pointer (a scoped secret
  name, a keychain item name, a Discord channel ID, or a validator
  type name).
- **FR-015**: The configuration loader MUST NOT consult environment
  variables for any supervisor field. The same configuration file
  MUST produce equivalent loaded configurations regardless of the
  calling process's environment.
- **FR-016**: The configuration loader MUST apply the documented
  default for every optional field that is absent from the supplied
  TOML file. The applied default MUST exactly match the value
  documented in the configuration reference. Every documented
  default MUST be exercised by at least one passing automated test.
  Absence of an entire optional section (for example, `[watchdog]`)
  is equivalent to absence of every field within that section: each
  documented default is applied individually.
- **FR-017**: The configuration loader MUST return typed sentinel
  errors for every defined rejection category (unknown field,
  unknown validator, grace window too long, grace TTL without cache,
  refresh window format, refresh window order, command empty,
  command path relative, scope empty, session-type invalid, TTL out
  of range, log level invalid, server URL invalid, watchdog rate
  invalid, missing required field, malformed value). Generic or
  untyped errors are not acceptable for any documented rejection.
- **FR-018**: The configuration loader MUST tolerate hostile byte
  streams without panicking or exhausting memory; every malformed
  input MUST produce a typed error.
- **FR-019**: The configuration loader MUST be safe to call once per
  supervisor process at startup; the same input MUST produce
  equivalent output across calls. The loader MUST NOT install
  process-wide state.
- **FR-020**: Operator-facing error messages from the loader MUST
  name the offending field, validator, or value category and MUST
  NOT include any secret value, scope value used as credential
  material, or other token-shaped string read from any source.

### Key Entities

- **Supervisor configuration**: The complete, validated representation
  of a single per-supervisor TOML file. Holds non-secret pointers
  (scoped secret names, keychain item names, Discord channel IDs,
  file paths, validator type names) and structural settings (refresh
  window, grace window, watchdog patterns, child command vector) but
  no secret values. Produced by a successful load; absent on any
  rejection.
- **Validator allow-list**: The fixed, documented set of validator
  type names that may appear in `[validators]`: `anthropic`,
  `anthropic-oauth`, `openai`, `google-ai`, `github`. Any other name
  is a load-time error.
- **Defaults catalogue**: The set of typed values the loader applies
  to optional supervisor fields when the operator omits them.
  Sourced from the configuration reference; equality with that
  reference is part of the contract and is enforced by tests.
- **Sentinel error**: A named, comparable error value returned by
  the loader for each documented supervisor-config rejection
  category. Operators (and downstream tests) identify rejection
  causes by sentinel identity, never by message string parsing.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every field documented in the project's configuration
  reference (supervisor section, including all documented sub-
  sections) is represented in the loaded configuration shape, and
  every documented default is asserted by an automated test.
  Coverage of the supervisor configuration package is at least 95%.
- **SC-002**: Each defined rejection category (unknown field,
  unknown validator, grace window too long, grace TTL without cache,
  refresh window format, refresh window order, command empty,
  command path relative, scope empty, session-type invalid, TTL out
  of range, log level invalid, server URL invalid, watchdog rate
  invalid, missing required field, malformed value) is exercised by
  at least one passing automated test that asserts the corresponding
  sentinel error.
- **SC-003**: A 60-second random-input fuzz run against the loader
  completes without panic, without exhausting memory, and produces
  only typed errors for every malformed input.
- **SC-004**: An operator who introduces a single typo, an unknown
  validator name, an out-of-range grace window, an inverted refresh
  window, or a relative command path in their supervisor file
  receives a single, named error identifying the offending category
  within the same load attempt — no trial-and-error is required to
  discover what is wrong.
- **SC-005**: The validator allow-list is enforced exclusively by
  the loader: a downstream component that consumes a successfully
  loaded supervisor configuration NEVER sees a validator name
  outside the allow-list. This property is asserted by a test that
  reviews the loaded configuration shape.
- **SC-006**: An external review of the loaded configuration shape
  confirms that no field of the supervisor configuration object
  holds a secret value and that the loader does not read any field
  from the process environment.

## Assumptions

- The configuration reference (`docs/CONFIG-SCHEMA.md`, supervisor
  section) is authoritative for the schema, defaults, and validation
  rules. Any divergence between the loader's behaviour and that
  document is a bug in the loader.
- The operator authors `~/.hush/supervisors/<name>.toml` directly;
  there is no admin UI, remote provisioning, or templated overlay
  in v0.1.0.
- The validator allow-list is fixed for v0.1.0 (`anthropic`,
  `anthropic-oauth`, `openai`, `google-ai`, `github`). Additions or
  removals require a constitutional amendment, not a configuration
  change.
- The four-hour grace-window cap is the documented ceiling on the
  optional grace-cache window; the supervisor's TTL discipline
  (constitution principle IV) and the audit-window boundary
  (Layer 6) both anchor on this cap.
- Path-safety semantics ("absolute path") use the operating system's
  notion of absolute path as captured in the configuration reference
  and the project's filesystem conventions. The exact resolution
  mechanism is plan-phase; the user-visible guarantee is "no shell
  parsing, no PATH lookup, no relative-path resolution".
- The status socket and PID file paths are validated for syntactic
  presence by this loader; runtime-level checks (mode `0600`,
  parent-directory `0700`, writeability) are performed by the
  supervisor process at startup, not by this package.
- Secret material (Discord bot token, API tokens, OAuth credentials,
  vault passphrase) is fetched by other components at runtime from
  the macOS Keychain (where applicable) or the vault server (over
  Tailscale, after Discord approval); this package only carries
  non-secret pointers.
- This package is consumed by every supervisor lifecycle chunk
  (state machine, child fork/exec, refresh/refill loop, status
  socket, validators, watchdog, alert classes); each consumer
  receives the loaded configuration object and the same set of
  sentinel errors.
- This specification only defines load-time behaviour. Runtime
  startup hardening (NTP sync verification, vault reachability,
  Discord availability, file-mode and ACL checks on the status
  socket and PID file) is performed by downstream supervisor
  chunks against the loaded configuration; those checks are out
  of scope for this package.
