# Feature Specification: Project-Wide Structured Logger with Redaction Enforcement

**Feature Branch**: `005-logging`
**Created**: 2026-04-27
**Status**: Draft
**Input**: User description: "internal/logging: project-wide stdlib slog logger; auto-detect TTY (text vs JSON); enforce type-driven redaction via LogValuer plus a regex backstop for known credential patterns; configurable level; never mutate slog.Default"

## Overview

The `internal/logging` package is the project-wide structured logger.
Every other package inside `internal/` obtains its `*slog.Logger` from
this package and never constructs its own. The logger has two
non-negotiable jobs: produce operationally useful structured records
(human-readable on a terminal, machine-parseable when piped or
redirected), and refuse — by construction — to render any secret value
in plaintext.

Redaction is enforced on two independent rails. The first rail is
type-driven: any value whose type implements the standard library's
`slog.LogValuer` interface MUST have `LogValue()` invoked before its
contents are rendered. This rail is what makes the Layer 5 secure
container (the `SecureBytes` primitive established in SDD-02) render
as the literal string `[redacted]` automatically, everywhere it
appears, without any caller cooperation. The second rail is a
pattern-based backstop: every plain string value emitted through the
logger is scanned for known credential patterns enumerated in
`docs/SECURITY.md`'s threat-model row, and any match is replaced with
the literal `[redacted]`. The backstop catches the case where a
caller builds a free-form string that happens to embed an upstream
provider's token — a class of mistake the type-driven rail cannot
defend against on its own.

The package's acceptance criterion is **indirect**: it underpins
**Constitution Principle X (Observability & Redaction)** and is the
load-bearing dependency every subsequent chunk relies on for
structured logging. Its release-gate contribution is the sentinel
invariant — a logger configured by this package, given a secret value
or a credential-pattern string, MUST emit zero occurrences of the
secret bytes anywhere in the captured output.

## Clarifications

### Session 2026-04-27

- Q: Canonical credential pattern set the package must ship — defer to `docs/SECURITY.md` §1.1 verbatim, follow the SDD-05 chunk doc's Google AI + JWT additions, or take some union? → A: Option A — ship the 4 patterns named in `docs/SECURITY.md` §1.1 verbatim (Anthropic `sk-ant-`, OpenAI project key `sk-proj-`, GitHub PAT `ghp_`, AWS access key `AKIA[0-9A-Z]{16}`). `docs/SECURITY.md` is the security source of truth; any drift (e.g. Google AI key, generic JWT) must land in `docs/SECURITY.md` first and only then expand the package's pattern set.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Internal callers get a configured logger that never leaks a wrapped secret value (Priority: P1)

Every internal package (the vault, the keys derivation, the request
handler, the supervisor state machine, the audit emitter, the CLI
itself) needs to log structured records: a request arriving, a vault
reload completing, a validator failing. Many of these records carry
attributes that name a secret — sometimes the secret material itself
is briefly held in scope as a `SecureBytes`. The logger MUST refuse,
by construction, to render that secret material as plaintext: the
caller MUST be free to attach the `SecureBytes` directly as an
attribute and trust the logger to substitute the standard
`[redacted]` placeholder.

**Why this priority**: Constitution Principle X names "secrets MUST
NEVER reach logs" as the project's most load-bearing operational
guarantee. A secrets broker that logs the secrets it brokers is
worse than no logging at all. The type-driven rail is the only way
to make that guarantee a property of the type system rather than a
property of every caller's discipline. Without it, the project
cannot ship.

**Independent Test**: A test constructs the package's logger pointed
at an in-memory writer, builds a `SecureBytes` wrapping a unique
sentinel byte sequence (`SECRET_SHOULD_NEVER_APPEAR_5`), emits a log
record at INFO level whose attributes include that secure container
under several attribute keys, and asserts the captured bytes contain
zero occurrences of the sentinel and at least one occurrence of the
literal `[redacted]`.

**Acceptance Scenarios**:

1. **Given** a logger constructed by this package and a `SecureBytes`
   wrapping a unique sentinel byte sequence,
   **When** the caller logs an INFO record with that container
   attached as an attribute value,
   **Then** the captured output contains zero bytes of the sentinel
   and renders the attribute value as the literal `[redacted]`.
2. **Given** a logger constructed by this package and a value of any
   type whose `LogValue()` method returns `slog.StringValue("[redacted]")`,
   **When** the caller logs at any level with that value as an attribute,
   **Then** the captured output renders the attribute value as the
   literal `[redacted]` regardless of the value's underlying byte
   content.
3. **Given** a structured record whose attributes are a nested
   `slog.Group` containing one or more secure-container values,
   **When** the record is emitted,
   **Then** every secure-container value inside the group renders as
   the literal `[redacted]` and no underlying byte content appears
   in the captured output.

---

### User Story 2 — Plain strings carrying credential patterns are caught by a regex backstop, even without a wrapper (Priority: P1)

A caller, in error or under maintenance pressure, builds a free-form
log message that contains the raw text of an upstream provider's API
key — for example, an Anthropic key beginning `sk-ant-`, a GitHub
personal access token beginning `ghp_`, or an AWS access key
beginning `AKIA`. The type-driven rail does not catch this case —
the offending value is a Go `string`, not a `SecureBytes`. The
package MUST nevertheless redact the credential before the bytes
leave the logger, by scanning every emitted string value (message
text and string attribute values) for the credential patterns
enumerated in `docs/SECURITY.md`'s threat-model row and replacing
every match with the literal `[redacted]`.

**Why this priority**: The threat hush exists to eliminate is
commodity malware grepping for known credential patterns. A log
file containing those exact patterns reintroduces the very surface
the project was built to remove. The type-driven rail defends the
golden path; the regex backstop defends the mistake path. Both are
required — neither alone is sufficient for Principle X.

**Independent Test**: A test constructs the package's logger pointed
at an in-memory writer, emits one record per credential pattern
enumerated in `docs/SECURITY.md`'s threat-model row (each record
carrying a representative sample credential as either the message
or a string attribute value), and asserts that for every record the
captured bytes contain zero occurrences of the sample credential
and at least one occurrence of the literal `[redacted]`.

**Acceptance Scenarios**:

1. **Given** a logger constructed by this package and a string
   containing a representative Anthropic API key (e.g.
   `sk-ant-...`),
   **When** the caller logs the string as the message or as a
   string attribute value,
   **Then** the captured output replaces the credential substring
   with the literal `[redacted]` and no byte of the original
   credential appears in the captured output.
2. **Given** the same logger and a representative OpenAI project key
   (e.g. `sk-proj-...`),
   **When** the caller logs the string,
   **Then** the captured output replaces the credential substring
   with the literal `[redacted]`.
3. **Given** the same logger and a representative GitHub personal
   access token (e.g. `ghp_...`),
   **When** the caller logs the string,
   **Then** the captured output replaces the credential substring
   with the literal `[redacted]`.
4. **Given** the same logger and a representative AWS access key
   (e.g. `AKIA...`),
   **When** the caller logs the string,
   **Then** the captured output replaces the credential substring
   with the literal `[redacted]`.
5. **Given** a string that contains no known credential pattern,
   **When** the caller logs the string,
   **Then** the captured output preserves the string unchanged.
6. **Given** a string that contains a credential pattern embedded in
   surrounding text (preceded or followed by other characters),
   **When** the caller logs the string,
   **Then** the captured output preserves the surrounding text and
   replaces only the matched credential substring with the literal
   `[redacted]`.

---

### User Story 3 — Output format auto-detects TTY versus pipe and shapes per destination (Priority: P1)

The same binary is invoked in two very different operational
contexts. An operator running `hush serve` from a terminal wants to
see compact, color-friendly, human-readable lines (timestamp, level,
message, attributes — easy to scan in a window). The same binary
running under launchd, systemd, or a redirected stdout in a
deployment script needs JSON-encoded structured records that a log
processor can parse. The package MUST detect the destination at
construction time and pick the right format automatically; the
caller MUST also be able to override the choice explicitly.

**Why this priority**: A logger that emits JSON to a terminal trains
the operator to ignore the output (it is unreadable). A logger that
emits human text to a pipe corrupts every downstream parser. The
auto-detection makes the right thing happen with no caller knowledge
of the destination, which is the only ergonomic that survives the
project's mix of CLI, daemon, and CI invocations.

**Independent Test**: A test invokes the package's constructor twice
— once with a destination that reports as a terminal under the
project's TTY-detection helper, once with a destination that does
not — and asserts that the first logger's output is the standard
slog text format and the second logger's output is the standard slog
JSON format. A third invocation passes an explicit text override
against a non-terminal destination and asserts text format is
produced; a fourth passes an explicit JSON override against a
terminal destination and asserts JSON is produced.

**Acceptance Scenarios**:

1. **Given** a logger constructed with the auto-detect format option
   and a destination that the project's TTY-detection helper reports
   as a terminal,
   **When** the caller emits a record,
   **Then** the captured output is the standard slog text format
   (key=value attributes, single-line per record).
2. **Given** a logger constructed with the auto-detect format option
   and a destination that the helper reports as not a terminal
   (a pipe, a file, an in-memory buffer),
   **When** the caller emits a record,
   **Then** the captured output is JSON-encoded structured records
   (one JSON object per record).
3. **Given** a logger constructed with an explicit text-format
   override against any destination,
   **When** the caller emits a record,
   **Then** the captured output is text format regardless of whether
   the destination is a terminal.
4. **Given** a logger constructed with an explicit JSON-format
   override against any destination,
   **When** the caller emits a record,
   **Then** the captured output is JSON format regardless of whether
   the destination is a terminal.

---

### User Story 4 — Source location appears for ERROR records in JSON output and never in text output (Priority: P2)

When the package emits JSON (the deployed-binary case), an ERROR-level
record needs to carry the file and line of the call site so the
operator can find the offending statement when triaging an incident.
When the package emits text (the interactive operator case), the same
source location is noise: it lengthens every line, distracts the
reader, and offers no additional value because the operator can
already find the call site by `grep`-ing the message text. The
package MUST therefore include source location for ERROR records in
the JSON format and MUST omit source location entirely from the text
format, regardless of level.

**Why this priority**: Tier separation between operational logs
intended for humans and operational logs intended for machines is
called out in `docs/OPERATIONS.md` and Constitution Principle X. The
specific source-location rule encodes that separation in the most
common operational scenario — the only level where finding the call
site matters, and only in the format where finding the call site
needs help.

**Independent Test**: A test constructs the package's logger in JSON
format and emits one record at each level (DEBUG, INFO, WARN, ERROR);
the assertion is that exactly the ERROR record contains a
source-location field and the others do not. A second test constructs
the logger in text format and emits the same set of records; the
assertion is that no record contains any source-location field.

**Acceptance Scenarios**:

1. **Given** a logger constructed in JSON format,
   **When** the caller emits an ERROR-level record,
   **Then** the captured JSON object contains a source-location
   field naming the file and line of the call site.
2. **Given** a logger constructed in JSON format,
   **When** the caller emits a DEBUG, INFO, or WARN record,
   **Then** the captured JSON object does not contain a
   source-location field.
3. **Given** a logger constructed in text format,
   **When** the caller emits a record at any level (including ERROR),
   **Then** the captured text line does not contain any
   source-location field.

---

### User Story 5 — The default level is INFO and the operator can configure it (Priority: P2)

The default level — what the binary emits when the operator passes
no flags — MUST be INFO: enough to follow the system's heartbeat in
production, quiet enough to avoid swamping the terminal during
normal interactive use. The operator MUST be able to raise the level
to DEBUG (verbose, for triage) or lower it to WARN or ERROR (quiet,
for noise-sensitive environments) by passing an explicit option to
the constructor, without recompiling the binary and without touching
any global state.

**Why this priority**: A default that is too verbose trains the
operator to ignore output (the staleness lesson from Constitution
Principle V); a default that is too quiet hides the system's
heartbeat. INFO is the equilibrium. Configurability is the operator's
escape hatch when triage demands it.

**Independent Test**: A test constructs the package's logger with no
explicit level option, emits one record at each level, and asserts
that DEBUG records are dropped while INFO, WARN, and ERROR records
appear. A second test constructs the logger with an explicit DEBUG
level and asserts that records at every level appear. A third test
constructs with an explicit ERROR level and asserts that only ERROR
records appear.

**Acceptance Scenarios**:

1. **Given** a logger constructed with no explicit level option,
   **When** the caller emits records at DEBUG, INFO, WARN, and ERROR,
   **Then** the captured output contains the INFO, WARN, and ERROR
   records and does not contain the DEBUG record.
2. **Given** a logger constructed with an explicit DEBUG level,
   **When** the caller emits records at every level,
   **Then** the captured output contains every record.
3. **Given** a logger constructed with an explicit ERROR level,
   **When** the caller emits records at every level,
   **Then** the captured output contains only the ERROR record.

---

### User Story 6 — The package returns an explicit logger handle and never mutates the standard library's default logger (Priority: P2)

A caller obtains a `*slog.Logger` from this package's constructor and
threads it explicitly through their call graph (as a parameter, as a
struct field on the consumer, as a context value at a boundary they
own). The package MUST NOT install its handler as the standard
library's default logger; it MUST NOT call `slog.SetDefault`; it MUST
NOT mutate any package-level slog state. Two consequences follow:
tests in any package can construct loggers with conflicting options
(text vs JSON, INFO vs DEBUG, different writers) and run in parallel
without global-state coupling; and code paths that intentionally use
`slog.Default()` (third-party libraries, the standard library itself)
are unaffected by the project's redaction policy.

**Why this priority**: Mutation of `slog.Default` is an action at a
distance — a constructor's side-effect that survives the
constructor's return. Constitution Principle IX bans this class of
side-effect (no globals, no `init()`, no mutable package-level state).
The rule is small and the enforcement is precise: the package's
public API MUST be a constructor that returns a configured logger,
nothing more.

**Independent Test**: A test captures the result of `slog.Default()`
before constructing any of this package's loggers, constructs several
loggers with different options, and asserts that `slog.Default()`
returns the same handler after every construction. The same test
asserts that the package contains no `init()` function and exposes no
package-level mutable state to its callers.

**Acceptance Scenarios**:

1. **Given** the standard library's default logger before any of
   this package's constructors have run,
   **When** the caller constructs one or more loggers with various
   option combinations,
   **Then** `slog.Default()` continues to return the same handler it
   returned before any constructor was called.
2. **Given** two loggers constructed by this package with conflicting
   options (different formats, different levels, different writers),
   **When** they are used concurrently from many goroutines,
   **Then** each logger's output is shaped only by its own options
   and the other logger's output is unaffected.
3. **Given** the package as imported by any caller,
   **When** the package is loaded,
   **Then** no `init()` function runs and no package-level handler is
   installed anywhere outside the values returned by the constructor.

---

### Edge Cases

- **Empty message and zero attributes**: A record emitted with an
  empty message and no attributes still renders correctly under
  both formats (a one-line text record with timestamp+level, or a
  JSON object with the standard fields). No panic occurs.
- **Nested groups containing secure containers**: Type-driven
  redaction MUST recurse into `slog.Group` attributes and their
  sub-attributes. A secure-container value at any depth still
  renders as `[redacted]`.
- **String containing several credential matches**: A single string
  value containing more than one occurrence of one or more credential
  patterns MUST have every match replaced with `[redacted]`. The
  surrounding non-credential text MUST be preserved.
- **Adjacent credentials**: Two credential patterns appearing
  immediately adjacent in the same string MUST each be replaced; no
  bytes of either credential survive.
- **Credential pattern in the message versus the attributes**: The
  redaction rail applies uniformly to the message string and to
  every emitted string attribute value. A caller cannot bypass
  redaction by moving a credential from an attribute to the
  message.
- **Non-string attribute values**: Numeric, boolean, time, and
  duration attribute values are not subject to the regex backstop
  (the patterns are textual). Type-driven redaction still applies:
  a `LogValue()` method on a custom numeric or duration wrapper is
  honoured.
- **Auto-detect when the destination is `os.Stderr` versus
  `os.Stdout`**: The TTY check applies to whichever destination the
  constructor was given. The default destination is `os.Stderr`;
  the auto-detect honours the actual file descriptor of whichever
  writer is in play.
- **Auto-detect when the writer is not a `*os.File`**: An
  in-memory or wrapped writer cannot be a terminal. The auto-detect
  MUST treat any non-file writer as not a terminal and pick JSON.
- **A `LogValue()` method that itself returns a string containing
  a credential pattern**: Both rails apply. The type-driven rail
  invokes `LogValue()` first; whatever it returns is then subject
  to the regex backstop if the resulting value renders as a string.
  A custom `LogValue()` returning `slog.StringValue("[redacted]")`
  passes the backstop unchanged.
- **A `LogValue()` method that panics**: Out of scope for this
  package. A misbehaving `LogValuer` is a defect in the wrapper
  type, not the logger; it is the wrapping package's responsibility
  to test that its `LogValue()` is total. The standard library's
  slog handler observes the panic; this package adds no recovery.
- **Concurrent emission from many goroutines**: The handler chain
  produced by the constructor MUST be safe for concurrent use, in
  the same sense as the standard library's slog handlers. Multiple
  goroutines calling Info/Warn/Error on the same logger handle
  concurrently MUST not produce interleaved or partially-redacted
  output.

## Requirements *(mandatory)*

### Functional Requirements

**Constructor and explicit handle**

- **FR-001**: The package MUST expose a constructor that accepts a
  set of options (level, format, output destination) and returns a
  configured `*slog.Logger`. The returned logger MUST be safe for
  concurrent use.
- **FR-002**: The constructor MUST NOT call `slog.SetDefault` and MUST
  NOT install its handler as the standard library's default logger
  anywhere in the process. Two distinct constructions of this
  package's logger MUST be independently configurable and MUST NOT
  share mutable state.
- **FR-003**: The package MUST NOT contain an `init()` function. All
  configuration MUST flow through the constructor's options.

**Output destination and format**

- **FR-004**: The constructor MUST accept an output destination
  option. When the option is unset, the destination MUST default to
  the process's standard error stream.
- **FR-005**: The constructor MUST accept a format option with at
  least the values "auto" (the default), "text", and "JSON". The
  "auto" value MUST cause the package to inspect the output
  destination at construction time and pick a format per FR-006.
  The "text" and "JSON" values MUST force the corresponding format
  regardless of the destination.
- **FR-006**: With the "auto" format option, the package MUST emit
  the standard slog text format if and only if the output
  destination is detected as a terminal; otherwise it MUST emit the
  standard slog JSON format. A non-file writer MUST be treated as
  not a terminal.

**Level and source location**

- **FR-007**: The constructor MUST accept a minimum-level option.
  When the option is unset, the minimum level MUST be INFO. When
  the option is explicitly set, the resulting logger MUST emit
  records at the specified level and above and MUST drop records
  below it.
- **FR-008**: When the format is JSON and the record's level is
  ERROR (or higher), the emitted record MUST contain a
  source-location field naming the file and line of the call site.
- **FR-009**: When the format is JSON and the record's level is
  below ERROR, the emitted record MUST NOT contain any
  source-location field.
- **FR-010**: When the format is text, the emitted record MUST NOT
  contain any source-location field, regardless of level.

**Type-driven redaction (rail one)**

- **FR-011**: For every attribute value in every emitted record,
  the package MUST invoke `LogValue()` on the value before any
  rendering takes place, when the value's type implements the
  standard library's `slog.LogValuer` interface. The bytes that
  reach the formatter MUST be those produced by `LogValue()`, not
  those of the original underlying value.
- **FR-012**: The type-driven invocation MUST recurse into grouped
  attributes (`slog.Group` and equivalent structures): if a group's
  member is a `slog.LogValuer`, the package MUST invoke `LogValue()`
  on that member before its bytes are rendered. The recursion MUST
  apply at every depth.
- **FR-013**: The Layer 5 secure-container primitive (`SecureBytes`,
  established by SDD-02) implements `slog.LogValuer` and renders as
  the literal string `[redacted]`. This package MUST honour that
  contract by virtue of FR-011 and FR-012; no caller cooperation MUST
  be required for secure-container values to render as `[redacted]`.

**Pattern-based redaction (rail two — backstop)**

- **FR-014**: For every emitted string value (the record's message
  and every string-typed attribute value, including the string
  produced by a `LogValuer` invocation under FR-011), the package
  MUST scan the string against a set of compiled patterns covering
  every credential class enumerated in `docs/SECURITY.md` §1.1
  (commodity-malware threat-model row).
- **FR-015**: Every substring matched by any pattern under FR-014
  MUST be replaced with the literal string `[redacted]` before the
  bytes reach the output destination. Multiple matches in the same
  string MUST each be replaced. Adjacent matches MUST each be
  replaced. Surrounding non-matching text MUST be preserved.
- **FR-016**: A string containing no match against any pattern MUST
  pass through the backstop unchanged.
- **FR-017**: The pattern set MUST cover, at minimum, every
  credential class named in `docs/SECURITY.md` §1.1: Anthropic API
  keys, OpenAI project keys, GitHub personal access tokens, and AWS
  access keys. The set is extensible: if `docs/SECURITY.md` adds a
  credential class, this package's pattern set MUST grow to cover
  it.

**No-leak invariants**

- **FR-018**: A logger constructed by this package, given a
  secure-container value wrapping a unique sentinel byte sequence,
  MUST emit zero occurrences of the sentinel anywhere in the
  captured output across any combination of level, format, and
  output destination supported by the constructor.
- **FR-019**: A logger constructed by this package, given a string
  value matching any pattern enumerated in `docs/SECURITY.md` §1.1,
  MUST emit zero occurrences of the matched substring anywhere in
  the captured output.
- **FR-020**: Neither rail MUST produce a panic on any reachable
  input. A pathological string (very long, containing many
  matches, containing UTF-8 sequences crossing match boundaries) is
  acceptable input to FR-015 and MUST be processed without panic.

### Key Entities

- **Logger options** — A small bundle of configuration values
  passed by the caller to the constructor. Fields: minimum level
  (defaults to INFO when unset), format (auto / text / JSON;
  defaults to auto), output destination (defaults to standard error
  when unset). The bundle is plain data; the constructor reads it
  once and does not retain a mutable reference.
- **Configured logger** — The handle returned by the constructor.
  It is the standard library's `*slog.Logger` with the package's
  redaction handler chain installed. It is safe for concurrent use,
  carries no implicit dependency on package-level state, and is
  the only object the caller threads through the rest of their
  call graph.
- **Redaction rails** — The two independent enforcement paths the
  handler chain implements: (1) type-driven, which invokes
  `LogValue()` on every `LogValuer`-implementing value at every
  depth before rendering; (2) pattern-based, which replaces every
  match of a known credential pattern in every emitted string with
  the literal `[redacted]`. The rails are composed: rail (1) runs
  first, then rail (2) runs over whatever string content rail (1)
  produced.
- **Credential pattern set** — The collection of compiled patterns
  used by rail (2). Each pattern corresponds to a credential class
  enumerated in `docs/SECURITY.md` §1.1. The set is built once per
  process and is read-only for the lifetime of the process.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001 (TTY auto-detect — text)**: When the constructor is
  called with the auto-detect format option and an output
  destination the project's TTY-detection helper reports as a
  terminal, every emitted record renders in the standard slog text
  format.
- **SC-002 (TTY auto-detect — JSON)**: When the constructor is
  called with the auto-detect format option and an output
  destination that is not a terminal (a pipe, a file, an in-memory
  buffer), every emitted record renders as a JSON-encoded
  structured record.
- **SC-003 (Format override — text)**: When the constructor is
  called with the explicit text format option, every emitted
  record renders in the standard slog text format regardless of
  the destination.
- **SC-004 (Format override — JSON)**: When the constructor is
  called with the explicit JSON format option, every emitted
  record renders as a JSON-encoded structured record regardless
  of the destination.
- **SC-005 (Default level)**: A logger constructed without an
  explicit level option drops DEBUG records and emits INFO, WARN,
  and ERROR records.
- **SC-006 (Configurable level)**: A logger constructed with an
  explicit DEBUG level emits records at every level. A logger
  constructed with an explicit ERROR level emits only ERROR
  records and drops DEBUG, INFO, and WARN.
- **SC-007 (Source on ERROR-JSON)**: A JSON-format logger emitting
  an ERROR record produces output containing a source-location
  field naming the file and line of the call site.
- **SC-008 (No source on non-ERROR JSON)**: A JSON-format logger
  emitting a DEBUG, INFO, or WARN record produces output containing
  no source-location field.
- **SC-009 (No source on text)**: A text-format logger emitting a
  record at any level produces output containing no
  source-location field.
- **SC-010 (Secure-container sentinel never leaks)**: A logger
  given a secure-container value wrapping a unique sentinel byte
  sequence emits captured output containing zero occurrences of
  the sentinel and at least one occurrence of the literal
  `[redacted]`, across every combination of level and format
  supported by the constructor.
- **SC-011 (Anthropic key sentinel never leaks)**: A logger given
  a string carrying a representative Anthropic API key sample
  emits captured output containing zero bytes of the sample and
  at least one occurrence of the literal `[redacted]`.
- **SC-012 (OpenAI project key sentinel never leaks)**: A logger
  given a string carrying a representative OpenAI project key
  sample (`sk-proj-...`) emits captured output containing zero
  bytes of the sample and at least one occurrence of the literal
  `[redacted]`.
- **SC-013 (GitHub PAT sentinel never leaks)**: A logger given a
  string carrying a representative GitHub personal access token
  sample emits captured output containing zero bytes of the
  sample and at least one occurrence of the literal `[redacted]`.
- **SC-014 (AWS access key sentinel never leaks)**: A logger
  given a string carrying a representative AWS access key sample
  emits captured output containing zero bytes of the sample and
  at least one occurrence of the literal `[redacted]`.
- **SC-015 (Pattern coverage)**: For every credential class
  enumerated in `docs/SECURITY.md` §1.1, at least one passing test
  demonstrates that a representative sample of that class is
  redacted in captured output.
- **SC-016 (slog.Default unchanged)**: The handler returned by
  `slog.Default()` after any number of calls to this package's
  constructor is observably the same handler that
  `slog.Default()` returned before the package was first imported
  by the test binary.
- **SC-017 (No init)**: The package source contains no `init()`
  function. Importing the package produces no observable
  side-effect on any package-level mutable state.
- **SC-018 (Concurrent emission is race-clean)**: Many goroutines
  calling INFO, WARN, or ERROR on a single configured logger
  concurrently complete under race-detector instrumentation with
  zero reported data races.
- **SC-019 (Backstop is total)**: A fuzz-style harness driving
  pathological strings (very long, many matches, adjacent
  matches, UTF-8 boundary cases) through the pattern-based
  redaction rail completes without panic; every output either
  preserves the input unchanged (no match) or contains zero bytes
  of any matched credential substring.

## Assumptions

- **Layer 5 contract is in place**: The Layer 5 secure-container
  primitive (`SecureBytes`, SDD-02) implements `slog.LogValuer`
  and returns `slog.StringValue("[redacted]")`. This package
  relies on that contract; it does not reproduce it.
- **TTY detection is a stable platform primitive**: The project's
  supported platforms (macOS, Linux) provide a reliable way for a
  caller to ask of an `*os.File` "are you a terminal?" without
  side-effects. The detection is invoked once at construction
  time on the destination's underlying file descriptor.
- **Standard error is the default destination**: When the caller
  passes no output destination, the logger emits to the process's
  standard error. Standard error is the conventional channel for
  operational logs (it is unbuffered, not interleaved with
  command output, and inherited by launchd/systemd unit log
  collection).
- **Source location is sourced from runtime call-site
  inspection**: When the format-and-level rule (FR-008) requires
  a source-location field, the call site is the program counter
  of the caller of the logger's level method. The standard
  library's slog handlers already capture this; this package
  configures them to include or exclude it per FR-008/FR-009/FR-010.
- **Pattern set lives with the package source**: The set of
  compiled credential patterns is defined inside this package, in
  a single file dedicated to that list. Adding a new pattern is a
  source change; runtime configuration of the pattern list is out
  of scope.
- **Single-process, single-binary scope**: This package serves the
  one `hush` binary on whichever host runs it. There is no log
  shipping, no remote collector handshake, no cross-process log
  multiplexing.
- **Internal-only consumption**: The package is consumed only by
  other packages inside the project's `internal/` tree. It is not
  part of any external API contract; the constructor signature is
  governed by the chunk's API contract and `docs/PACKAGE-MAP.md`.
- **Output is bounded by slog's own writer guarantees**: The
  package does not add buffering, batching, or flushing on top of
  the standard library's slog handlers. Whatever durability and
  atomicity the underlying writer provides (line-atomic for a
  POSIX file open in append mode, for example) is the durability
  the logger inherits.

## Out of Scope

- **Audit log emission**: The hash-chained, ECDSA-signed audit
  chain (Layer 6 of the project's security stack, Constitution
  Principle III) is a distinct package with distinct durability
  and integrity requirements. This package is for operational
  logs only; it MUST NOT duplicate audit entries, and audit
  entries MUST NOT flow through this logger.
- **Discord alert tier dispatch**: The mapping from a log level to
  a Discord channel (Critical → page, Warning → channel, Info →
  audit-only) defined in Constitution Principle X and
  `docs/OPERATIONS.md` is owned by the alerting layer, not this
  package. This package's responsibility ends at producing the
  structured record; routing to Discord is upstream.
- **Log shipping, syslog, remote sinks, file rotation**: All
  out of scope for v0.1.0. Operational logs go to the configured
  writer; rotation and shipping are an operator concern at the
  process-supervisor level (launchd, systemd, journald).
- **Metrics and counters**: Constitution Principle X bans a
  Prometheus endpoint and remote metrics for v0.1.0. The local
  Unix status socket exposes counters in a separate package.
- **Color rendering in the text format**: The global CLI flag
  `--no-color` (Constitution Principle VII) lives at the cobra
  layer. This package does not add or strip ANSI colour codes.
- **Sampling, rate limiting, deduplication**: Out of scope. Every
  call site decides whether to log; the logger emits every record
  it accepts.
- **A logger that panics on a misbehaving `LogValuer`**: The
  package adds no `recover()` around `LogValue()` invocations. A
  misbehaving `LogValuer` is a defect in the wrapping type and
  must be tested at that type's boundary.
- **A pluggable pattern set at runtime**: The credential pattern
  set is a compile-time list; runtime mutation, hot reload, or
  per-logger override of the pattern list is out of scope.
- **Cross-process or cross-host log correlation IDs**: Out of
  scope for v0.1.0. The request_id claim on the JWT (Layer 2) is
  the project's correlation primitive; this package emits it as
  an ordinary attribute when callers pass it.
- **Windows support**: Out of scope for v0.1.0 (project-wide).
  TTY detection on Windows is not configured.

## Dependencies

- **SDD-02 (`internal/vault/securebytes`)** — The Layer 5
  secure-container primitive whose `slog.LogValuer` implementation
  is the type-driven rail's golden case. This chunk is blocked on
  SDD-02 being complete (it is — see `docs/sdd/SDD-02.md`).
- **`docs/SECURITY.md` §1.1 (commodity-malware threat-model row)**
  — The authoritative list of credential classes the
  pattern-based backstop MUST cover. Any addition to that section
  becomes an addition to this package's pattern set.
- **`docs/OPERATIONS.md` (logging tier definitions and format
  expectations)** — The operator-facing contract for what TTY vs
  pipe means and what shape the logs take in each context.
- **Constitution Principle IX (Idiomatic Go Discipline)** — The
  no-`init()`, no-globals, accept-interfaces-return-concrete-types,
  panic-policy rules this package's surface inherits.
- **Constitution Principle X (Observability & Redaction)** — The
  load-bearing principle this entire chunk exists to enforce. The
  acceptance criterion this chunk contributes to is indirect: every
  subsequent chunk depends on the redaction guarantees defined here.
- **Constitution Principle XI (Native-First, Minimal Dependencies,
  Ephemeral Vault)** — Bans the introduction of any third-party
  logging dependency; mandates that this package use the standard
  library's `log/slog` and a single allowed helper for TTY
  detection.
- **Downstream packages blocked on this one**: every chunk after
  SDD-05 obtains its `*slog.Logger` from this package. The full
  fan-out is documented in `docs/sdd/SDD-PLAYBOOK.md` (every chunk
  numbered 06 and later names this package as a precondition).
