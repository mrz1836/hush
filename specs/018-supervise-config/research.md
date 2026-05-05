# Phase 0 Research: `internal/supervise/config`

**Feature**: 018-supervise-config — per-supervisor TOML schema, defaults, validation, validator allow-list, refresh-window parser, fuzz target
**Date**: 2026-05-05

This document resolves every technical decision the plan depends on.
Each entry follows the **Decision / Rationale / Alternatives
considered** format. There are no remaining `NEEDS CLARIFICATION`
markers in the spec; the five clarification answers from
Session 2026-05-05 (`spec.md` §Clarifications) are encoded into the
relevant decisions below.

---

## R-001 — TOML decoder choice and strict-mode wiring

**Decision**: Reuse `github.com/pelletier/go-toml/v2` (the project's
already-adopted TOML decoder, locked at SDD-06). Construct the
decoder via `toml.NewDecoder(r)`, call `.DisallowUnknownFields(true)`
before `.Decode(&decoded)`. The decoder reads the file via
`os.Open(path)` → `*os.File` → `defer f.Close()` → pass the file as
the `io.Reader`.

**Rationale**: The chunk contract (SDD-18.md) names
`pelletier/go-toml/v2` and `DisallowUnknownFields(true)` explicitly,
and the constitution's "minimal dependencies" principle (XI)
demands reuse over re-introduction. Strict-decode mode is the
load-bearing feature for spec FR-002 (typo defence) and FR-017
(typed-error coverage): an unknown TOML key returns a
`*toml.StrictMissingError` which the loader translates into the
package's `ErrUnknownField` sentinel via `errors.Is`-friendly
wrapping (`fmt.Errorf("hush/supervise/config: unknown field %s: %w",
fieldPath, ErrUnknownField)`). Type-mismatch decode errors map to a
distinct `ErrTOMLDecode` sentinel.

The reader-based API lets the loader keep a `*os.File` open only
for the decode duration, never reading the entire file into a
buffer. This caps peak memory at the decoder's internal buffer
(~64 KiB for typical configs) regardless of input size, satisfying
the fuzz-target's "no unbounded memory growth" invariant.

**Alternatives considered**:
- *`BurntSushi/toml`*: rejected. The chunk contract pins
  pelletier/v2; SDD-06 already adopted it; switching would diverge
  from the locked stack and cost a constitutional amendment.
- *Hand-rolled TOML subset parser*: rejected. Even a small TOML
  subset would be 500+ LOC of parser plus a fuzz target on the
  parser itself. Reusing the maintained, fuzzed-upstream decoder is
  strictly safer and free under Constitution XI's reuse rule.
- *Read whole file into memory then decode*: rejected. Peak memory
  under fuzz would be unbounded; the streaming approach is strictly
  safer.

---

## R-002 — Validator allow-list shape and unknown-name reporting

**Decision**: Declare a package-level set
`var validatorAllowList = map[string]struct{}{ "anthropic": {},
"anthropic-oauth": {}, "openai": {}, "google-ai": {}, "github": {}
}` and a typed `type Validator string` for the public field. During
materialisation, iterate every `[validators]` entry: for each
`(secretName, validatorName)` pair, look up `validatorName` in the
allow-list. On miss, return
`fmt.Errorf("hush/supervise/config: unknown validator %q: %w",
validatorName, ErrUnknownValidator)`. The error message includes
ONLY the offending validator name (the right-hand side of the TOML
entry, e.g., `"slack"`); the secret-name key (the left-hand side,
e.g., `"ANTHROPIC_API_KEY"`) is NOT included because spec FR-014 +
FR-020 forbid token-shaped strings — and a high-entropy LHS could
theoretically encode credential material in operator typo cases.

**Rationale**: A `map[string]struct{}` set provides O(1) membership
checks with zero allocation per check, ideal for the fuzz target's
"sustained throughput" expectation. The `type Validator string`
typedef on the public struct gives downstream consumers a narrow
type signal: a function that takes `Validator` cannot accept an
arbitrary string; the type's existence is a cheap reminder that the
value is constrained.

The "name only, not LHS" rule mirrors SDD-17's secret-name
omission pattern in error messages and aligns with Constitution X's
"errors return failure mode and identifier — never the secret value,
never a partial of it". The unknown-validator name itself (e.g.,
`"slack"`) is operator-typed plaintext that names a public,
documented category; including it in the error gives the operator
exactly the actionable information needed to fix the typo, with no
secret material.

A self-test (`TestErrUnknownValidator_DoesNotIncludeSecretMaterial`)
will assert that an `ErrUnknownValidator` error string contains the
validator-RHS but does NOT contain the high-entropy LHS the test
constructs.

**Alternatives considered**:
- *Bool-valued map (`map[string]bool`)*: rejected. `struct{}`-valued
  is the idiomatic Go set; saves 1 byte per entry (8 bytes total
  for five entries) and signals "this is a set, not a map" to
  readers.
- *Slice + linear scan*: rejected. The allow-list is fixed-size at
  five but the iteration is per-entry on every load; the map's
  O(1) lookup is strictly better and the constant-time difference
  matters under fuzz.
- *Include the LHS secret name in the error*: rejected. FR-014 +
  FR-020 + Constitution X. A typo'd LHS could theoretically be a
  paste of credential material; the error message is downstream-
  diagnostic, not a debugger.

---

## R-003 — `refresh_window` parsing: split semantics and error categorisation

**Decision**: Parse `refresh_window` with the following pipeline:
1. Find the single `-` separator via `strings.Index(s, "-")`. If
   the index is `-1`, return `ErrRefreshWindowFormat`.
2. If a SECOND `-` is present (`strings.LastIndex != strings.Index`),
   return `ErrRefreshWindowFormat` — the schema is `HH:MM-HH:MM`,
   not `HH:MM-HH:MM-anything`.
3. Split into `start, end := s[:idx], s[idx+1:]`.
4. Parse `startT, err := time.Parse("15:04", start)`; on err, return
   `ErrRefreshWindowFormat`.
5. Parse `endT, err := time.Parse("15:04", end)`; on err, return
   `ErrRefreshWindowFormat`.
6. If `!startT.Before(endT)`, return `ErrRefreshWindowOrder`. The
   condition catches both `start == end` and `start > end`
   (wrap-around / inverted) per spec FR-006 and Edge Cases.

`time.Parse("15:04", ...)` enforces the leading-zero, `HH:MM`
24-hour format documented in `docs/CONFIG-SCHEMA.md`. The format
literal `"15:04"` is the canonical Go reference time for
hour-of-day:minute parsing and rejects `"9:00"` (no leading zero)
and `"25:00"` (hour out of range) automatically.

**Rationale**: Two distinct sentinels (`ErrRefreshWindowFormat` vs
`ErrRefreshWindowOrder`) is FR-006's explicit requirement. The
operator who writes `"09:00 to 10:00"` made a different mistake
than the operator who writes `"10:00-09:00"`; the error message
they see should be different too.

`time.Parse` is the stdlib's canonical time-of-day parser. Using
its built-in HH:MM range checks (rejecting `25:00`, `99:99`, `-3:00`,
`9:7a`) means the format validator is implemented in stdlib code,
not in our regex — Constitution XI prefers stdlib-correct over
hand-rolled. The same parser is reused for the start and end side
so the two sides cannot drift.

The "no second `-`" guard catches the operator who writes
`"09:00-10:00-bad"`. Without it, the first `Index` would split
into `"09:00"` and `"10:00-bad"`; the latter would fail
`time.Parse` and produce `ErrRefreshWindowFormat`, which is the
right outcome but for the wrong reason. Belt-and-braces is cheap.

Wrap-around windows (`"22:00-02:00"` meaning "10pm to 2am next
day") are explicitly rejected as ordering errors per the spec's
Edge Cases — v0.1.0 supports only same-day windows. Operators who
need an overnight refresh window can use a daytime window and
accept that the next prompt may slip into the morning of the
following day. That is the documented behaviour; "wrap-around
support" is a v0.2 concern.

**Alternatives considered**:
- *Regex `^([0-1][0-9]|2[0-3]):([0-5][0-9])-([0-1][0-9]|2[0-3]):([0-5][0-9])$`*:
  rejected. Hand-rolled, prone to off-by-one (does `"24:00"` mean
  midnight-end-of-day?), and yields a less-helpful error message
  than `time.Parse`'s typed mismatch.
- *`net.ParseTimeRange` or similar third-party*: rejected. No such
  library is in stdlib; introducing one violates Constitution XI.
- *Allow wrap-around windows*: rejected. The spec's Edge Cases
  explicitly call out wrap-around as an order error in v0.1.0.

---

## R-004 — `[child].command` absolute-path validation

**Decision**: After decoding `[child].command` as `[]string`,
validate the slice in this order:
1. `len(cmd) == 0` → `ErrCommandEmpty` (spec FR-007 first
   category).
2. `!filepath.IsAbs(cmd[0])` → `ErrCommandPathRelative` (spec
   FR-007 second category).
3. Otherwise: accept verbatim. The loader does NOT validate that
   `cmd[0]` exists on disk (FR-007 + spec Assumptions: runtime
   reachability checks belong to downstream supervisor startup).
   Subsequent elements are passed through verbatim — no quoting,
   no splitting, no shell parsing.

`filepath.IsAbs` returns the OS-correct notion of "absolute path":
on Unix, a leading `/`; on Windows, a drive letter or UNC path.
Since hush is Unix-only (darwin + linux), `filepath.IsAbs(s) ==
strings.HasPrefix(s, "/")` in practice, but using the stdlib
function preserves portability and reads more clearly.

**Rationale**: Spec FR-007 mandates two distinct sentinels for the
two failure modes. Empty-vector and relative-first-element are
genuinely different operator mistakes:
- Empty vector: forgot to author the field at all (or copy-paste
  truncation).
- Relative first element: thought of the daemon by name (`my-daemon`)
  rather than path (`/usr/local/bin/my-daemon`); a pure habit
  carry-over from interactive shells.

`filepath.IsAbs` over `strings.HasPrefix(cmd[0], "/")` is the
Constitution-XI-correct choice (stdlib first). The semantic
difference is zero on the supported platforms but the readability
difference is real.

The "do not check existence on disk" choice is critical for the
load-vs-runtime split documented in the spec Assumptions: the
loader is read-only and idempotent. Existence is checked at
runtime by the supervisor's startup sequence (SDD-19/SDD-23) when
the binary is about to be `exec`'d. A non-existent binary at load
time is a different failure mode than one that disappears between
load and exec; the supervisor should treat both uniformly via the
runtime check.

**Alternatives considered**:
- *Validate that `cmd[0]` exists and is executable at load time*:
  rejected. Violates load/runtime separation; couples the loader
  to the filesystem state at boot, which can race with
  installation scripts. Spec Assumption: runtime reachability is
  downstream's responsibility.
- *Accept relative paths with a documented stderr WARN*: rejected.
  FR-007 is unambiguous: relative paths are a load-time error.
  Constitution V's "fail loudly and visibly" + Constitution IX's
  "no half-finished implementations" both reject the
  warning-but-still-accept shape.
- *Use `path.IsAbs` instead of `filepath.IsAbs`*: rejected.
  `path.IsAbs` is the slash-only forward-path variant; `filepath`
  is the OS-aware variant and the right call for a filesystem path.

---

## R-005 — Grace-window cap enforcement and contradiction guard

**Decision**: Two related but separate validators:

1. **Cap enforcement (FR-004)**: parse `cache_grace_ttl` via
   `time.ParseDuration`; if the parsed duration is strictly greater
   than `MaxGraceWindow = 4 * time.Hour`, return
   `ErrGraceWindowTooLong`. Exactly four hours is accepted.
   Absence applies the documented default
   `DefaultGraceWindow = 60 * time.Minute`.

2. **Contradiction guard (FR-011 + Clarification 3)**: detect the
   shape `cache_secrets_for_restart = false` (or absent, per
   `DefaultCacheSecretsForRestart = false`) AND
   `cache_grace_ttl` explicitly present in the TOML. To
   distinguish "absent" from "set to zero", the wire-shape struct
   declares `CacheGraceTTL *string` (pointer). A non-nil pointer
   means the TOML had the key. If the cache flag is false and the
   pointer is non-nil, return `ErrGraceTTLWithoutCache`.

The contradiction-guard runs BEFORE the cap-enforcement, because
"`grace_ttl` set when cache disabled" is a more useful error than
"`grace_ttl` over the cap when cache disabled".

**Rationale**: Spec FR-004 + FR-011 + Clarification 3 explicitly
require the contradiction-guard as a distinct, named sentinel; a
silently-ignored `cache_grace_ttl` would convert an obvious
operator mistake into a hidden behaviour change.

The cap (4h) is constitutional (Principle IV: TTL discipline +
Layer-6 audit boundary). Encoding it as a typed `var
MaxGraceWindow = 4 * time.Hour` rather than a literal in
`validate.go` lets downstream consumers (and tests) reference the
exact constitutional value without re-deriving it; mirrors the
same pattern SDD-06 uses for `MinArgonMemoryMB`.

The pointer-discriminator pattern (`*string` for "absent vs
present-but-zero") is the same idiom SDD-06 uses for
`require_tailscale` (`*bool`) — both packages use the pattern to
distinguish "default applies" from "explicit zero/false". Sharing
the idiom across `internal/config` and
`internal/supervise/config` reduces cognitive load for readers.

**Alternatives considered**:
- *Use `time.Duration` zero-value as the "absent" sentinel*:
  rejected. `time.Duration(0)` is a valid (if useless) explicit
  setting; pointer-vs-zero is the only unambiguous discriminator.
- *Treat the contradiction as a WARN-and-continue with the grace
  TTL ignored*: rejected. Constitution V + Constitution IX:
  silent behaviour change is worse than visible failure.
- *Combine the contradiction-guard error with `ErrInvalidDuration`*:
  rejected. FR-011's "distinct, named" requirement; combining
  would lose the operator-actionable distinction.

---

## R-006 — `requested_ttl` ceiling: load-time vs. claim-time enforcement

**Decision**: At load time, parse `requested_ttl` via
`time.ParseDuration`. If the parsed value is strictly greater than
`MaxRequestedTTL = 24 * time.Hour` (the documented v0.1.0
supervisor-TTL upper bound, taken from `docs/CONFIG-SCHEMA.md`'s
note "must not exceed 24h in v0.1.0"), return
`ErrRequestedTTLOutOfRange` (spec FR-010 + Clarification 1).
Absence applies the documented default
`DefaultRequestedTTL = 20 * time.Hour`.

The loader does NOT consult any server-side config. Specifically,
it does NOT read the trusted host's `~/.hush/config.toml` and does
NOT compare `requested_ttl` against
`config.Server.Crypto.MaxSupervisorTTL` (which may be lower than
24h). The spec clarification is explicit: the load-time check is
the absolute ceiling; the server enforces a stricter value at
claim time.

**Rationale**: Clarification 1 codifies the architectural
separation. The supervisor config file lives on the agent host or
the launchd unit's host; the server's `[crypto].max_supervisor_ttl`
lives on the trusted vault host. Reading the latter from the former
would either require a network call at load time (impossible — the
supervisor isn't yet authenticated) or hard-code a path assumption
that breaks the host separation. The 24h ceiling is the
documented absolute maximum; any operator-set value below 24h is
the server's problem to enforce when the supervisor presents its
claim.

The loader's job is "fail loudly when the operator wrote something
the v0.1.0 contract does not accept under any configuration".
24h is that line.

**Alternatives considered**:
- *Read the server config and compare*: rejected. Spec
  clarification + architectural separation. The supervisor host
  may not even have read access to the server's config file.
- *Skip load-time TTL validation, defer entirely to the server*:
  rejected. FR-010's "visible failure at load time spares the
  operator a Discord round-trip" — a 25h `requested_ttl` would
  otherwise survive load, fail at claim time, and produce a
  Discord rejection that the operator must investigate.
- *Use a constant lower than 24h (e.g. 20h to match the default)*:
  rejected. The default and the ceiling are semantically distinct.
  The default is what the loader applies when the operator omits
  the field; the ceiling is the absolute maximum the operator may
  request. Conflating them prevents operators from explicitly
  setting `requested_ttl = "23h"` for a daemon that runs nightly.

---

## R-007 — `server_url` syntactic-only validation (load time)

**Decision**: Parse `server_url` via `url.Parse(raw)`. Reject:
1. Empty string (`raw == ""`) → `ErrServerURLInvalid` (with cause
   "empty value").
2. Parse error (`err != nil`) → `ErrServerURLInvalid` wrapping the
   parser error.
3. Empty host (`u.Host == ""`) → `ErrServerURLInvalid` (with cause
   "missing host").
4. Scheme not in `{"http", "https"}` (case-insensitive comparison
   via `strings.EqualFold`) → `ErrServerURLInvalid` (with cause
   "unsupported scheme").

Deeper checks (Tailscale CIDR membership of `u.Host`, port
strictly `7743`, path matching `^/h/[A-Za-z0-9_-]{6,32}$`) are NOT
performed at load time. They are the responsibility of downstream
supervisor startup hardening (SDD-19/SDD-23) per Clarification 5.

**Rationale**: Clarification 5 codifies the syntactic-vs-semantic
split. At load time, the loader catches the operator who pasted a
plaintext IP without `http://`, the operator who wrote `https//`
(missing colon), and the operator who tried `ftp://...`. The
runtime checker catches the operator who wrote
`http://1.2.3.4:7743/h/x` (a public IP that is syntactically valid
but constitutionally forbidden).

`net/url` is the stdlib's canonical URL parser; using it satisfies
Constitution XI. The four specific failure modes are listed in the
spec Edge Cases; mapping each to the same `ErrServerURLInvalid`
sentinel (with distinct wrapped causes) gives the operator one
actionable category without proliferating sentinel surfaces beyond
what the spec calls for.

`strings.EqualFold` for scheme comparison is the Go-idiomatic
case-insensitive ASCII compare; `url.Parse` lowercases the scheme
field already in modern Go versions but the explicit fold is
belt-and-braces against future stdlib behaviour change.

**Alternatives considered**:
- *Enforce Tailscale CIDR at load time*: rejected by Clarification
  5. The supervisor host may not have read access to the
  Tailscale state at load time; runtime is the right place.
- *Accept any non-empty string and defer all validation to
  runtime*: rejected. Spec Edge Cases explicitly require the four
  syntactic categories to be distinct, named load-time errors.
- *Use a regex (`^https?://[^/\s]+(/.*)?$`)*: rejected. `net/url`
  is the stdlib-correct URL parser; a regex would either accept
  invalid URLs (`https://[`) or reject valid ones (IPv6 brackets).

---

## R-008 — Watchdog defaults: missing section vs. all-fields-absent

**Decision**: Treat a missing `[watchdog]` table as semantically
identical to an empty `[watchdog]` table with all fields absent.
Apply the documented defaults to every field individually:
`Enabled = true`, `MaxAlertsPerHour = 6`, `Patterns = []string{}`.
The decoded shape declares `Watchdog *watchdogDecoded`; if the
pointer is `nil` after decode, the materializer constructs an
empty `watchdogDecoded` and proceeds with the standard default-
application logic.

**Rationale**: Clarification 4 (Session 2026-05-05) is explicit:
"Apply documented defaults to every watchdog field; watchdog runs
by default and operators must write `enabled = false` explicitly
to disable it." The clarification rationale is constitutional
(Principle V — staleness must be visible by default); a silently-
absent watchdog is the inverse of "fail loudly".

The pointer-discriminator pattern lets the materializer treat
`nil` (section absent) and `&watchdogDecoded{}` (section present
but empty) identically, while still distinguishing both from the
case where a SUBSET of watchdog fields are explicitly set (e.g.,
`[watchdog] enabled = false` with no other fields — applies the
defaults for the unset fields).

`Patterns []string{}` is intentionally an empty slice rather than
a nil slice; consumers that range over `Patterns` and expect a
zero-iteration result can rely on either, but JSON marshallers and
logging adapters render `[]` for `[]string{}` and `null` for
`[]string(nil)`. The empty-slice form is the safer default for
observability.

**Alternatives considered**:
- *Treat missing section as `Enabled = false`*: rejected by
  Clarification 4 + Constitution V.
- *Require operators to write `[watchdog]` explicitly*: rejected.
  Adds friction; the watchdog is a pure-alert (Constitution V
  layer) feature that should run by default.
- *Surface a load-time INFO log when defaults are applied*:
  rejected. The package has no logger — that's a downstream
  concern. The defaults are documented; the operator who omitted
  the section either knew the default or didn't need to.

---

## R-009 — Required-field gate: ordering and per-field error messages

**Decision**: After decode but before any other validation, run a
required-field check over the materialized struct. The required
fields, per `docs/CONFIG-SCHEMA.md` Supervisor section "Required":

Root: `name`, `reason`, `server_url`, `client_machine_index`,
`session_type`, `requested_ttl`, `refresh_window`, `status_socket`,
`pid_file`, `scope`, `[validators]` (the section, not its
contents).

`[child]`: `command`, `working_dir`, `env_passthrough` are listed
required in the schema; the schema-doc treats `restart_on_clean_exit`
and `restart_on_exit_78` as required-with-defaults.

For each missing field, produce:
```
fmt.Errorf("hush/supervise/config: missing required field %s: %w",
    fieldPath, ErrMissingRequiredField)
```
where `fieldPath` is the dotted TOML path (e.g., `child.command`).
Multiple missing fields are reported via `errors.Join`, mirroring
SDD-06's pattern.

The required-field gate runs FIRST so a missing-field error
short-circuits before format / range / containment errors that
might otherwise fire on absent values.

**Rationale**: Spec Edge Cases explicitly require "missing
required field" as a distinct category, separate from "malformed
value". A field that is absent vs. a field that is present with an
unparseable value are different operator mistakes and deserve
different error messages.

The dotted TOML path (e.g., `child.command`) is the operator-
authored coordinate, not the Go struct field path; it matches the
text the operator can grep their own config for. Same ergonomic
choice SDD-06 made for missing-field messages.

`errors.Join` for multi-violation reports lets the operator fix
all the missing fields in one round-trip rather than one-at-a-
time. Same pattern SDD-06 uses for accumulating validation
failures.

**Alternatives considered**:
- *Halt at first missing field*: rejected. SDD-06 explicitly
  chose `errors.Join` for multi-violation; SDD-18 follows the
  precedent for operator ergonomics.
- *Use struct-tag-driven required detection (e.g., `toml:"name,required"`)*:
  rejected. `pelletier/go-toml/v2` has no such tag; introducing
  one would mean a fork of the decoder. Hand-rolled
  required-field validators in `validate.go` are clearer and
  more maintainable.
- *Use struct field zero-values as the "missing" sentinel*:
  rejected. Some required fields have zero-valued types
  (`client_machine_index uint32 == 0`) that are also valid
  configurations. Pointer-typed wire-shape fields disambiguate.

---

## R-010 — Path-safety helpers: duplication vs. shared helper

**Decision**: Implement `paths.go` in
`internal/supervise/config/paths.go` with the helpers
`expandHome(s string) (string, error)` and `absPath(s string)
(string, error)`. Both helpers duplicate the implementation in
`internal/config/paths.go` (SDD-06). No shared helper package is
created at this time.

**Rationale**: Constitution IX prefers tiny duplication over thin
abstractions when the duplicate fits in one file. The helpers are
~30 LOC total, well-tested, and unlikely to drift. A shared
helper package (`internal/pathutil`) would:
- Need its own SDD chunk to introduce.
- Conflict with SDD-06's locked surface (the helpers are
  unexported in `internal/config`; making them shared requires
  promoting them to exported, which alters SDD-06's contract).
- Add a new internal-package boundary just to save ~30 LOC.

If a third caller emerges (likely SDD-23 cobra command for
`hush supervise`), we will consolidate then. Until then, the
duplication is the constitutional-correct choice.

The helpers are scoped to the package: `~` expansion via
`os.UserHomeDir`; absolute-path resolution via `filepath.Abs`. No
symlink resolution, no file-existence checks (those are SDD-19's
job), no shell-style expansion (`$VAR`, glob, nested `~user` are
all literal).

**Alternatives considered**:
- *Shared helper package `internal/pathutil`*: rejected. Premature
  abstraction; would require its own SDD chunk and mutate
  SDD-06's locked surface.
- *Import `internal/config`'s helpers directly*: rejected.
  `internal/config`'s helpers are unexported; making them
  exported alters SDD-06's locked surface and creates a
  cross-package dependency where there shouldn't be one.
- *Inline the expansion at each call site*: rejected. The
  ~3 callers (status_socket, pid_file, working_dir, …) deserve a
  shared helper within the package even if cross-package sharing
  is deferred.

---

## R-011 — Fuzz target: input strategy and termination contract

**Decision**: Implement `FuzzSuperviseTOML` in
`config_fuzz_test.go`. The fuzz function takes a `[]byte`,
writes it to a `t.TempDir()`-scoped file, and calls
`Load(ctx, path)`. The fuzz body asserts:
1. `Load` does not panic.
2. If `err != nil`, `err` matches at least one of the named
   sentinels via `errors.Is`. The set of sentinels checked is
   the full catalogue from `errors.go`. An `err` that matches
   none of them is a fuzz failure (the fuzzer reports the input
   as a corpus row).
3. If `err == nil`, the returned `*Supervisor` is non-nil and
   passes a downstream sanity check (e.g.,
   `s.Validators` does not contain a non-allow-listed value).

Seed corpus: eight files dropped into
`testdata/fuzz/FuzzSuperviseTOML/`:
- `minimal-valid.toml` — only required fields, every default
  exercised.
- `full-default.toml` — every field at its documented default.
- `malformed-bytes.toml` — random non-UTF8 bytes.
- `empty.toml` — zero bytes.
- `partial-table.toml` — `[discord` with no closing bracket.
- `conflicting-types.toml` — `requested_ttl = 42` (int instead
  of duration string).
- `unknown-validator-name.toml` — `ANTHROPIC_API_KEY = "slack"`.
- `refresh-window-edge.toml` — `refresh_window = "23:59-24:00"`.

CI gate: `go test -fuzz=FuzzSuperviseTOML -fuzztime=60s
./internal/supervise/config/`. No new corpus rows mean no
crashes / no untyped errors discovered in 60 seconds.

**Rationale**: SDD-18 chunk contract names the fuzz target +
60-second gate. The seed corpus mirrors SDD-06's
`FuzzServerTOML` corpus for cross-chunk consistency: each seed
exercises a distinct decode + validation path, so the first
60-second run produces a meaningfully covered corpus rather
than starting from zero. The "every error is a named sentinel"
invariant is FR-018 + Constitution VIII fuzz-goal #4 ("malformed
input returns explicit errors").

The `t.TempDir()`-scoped file approach (rather than passing
bytes directly to a hypothetical `LoadReader`) keeps the fuzz
harness aligned with the production code path: in production,
`Load` opens a file and decodes. The fuzzer should exercise the
same path, not a side-channel API.

**Alternatives considered**:
- *Add a `LoadReader(ctx, io.Reader)` API for fuzz convenience*:
  rejected. The chunk contract pins `Load(ctx, path)` as the only
  entry point; introducing a sibling API for testing would
  violate the locked contract.
- *Skip the seed corpus*: rejected. An empty seed corpus means
  the first 60-second run is mostly random-byte coverage of the
  TOML decoder's reject paths, with little time spent on the
  package's own validators. Seeding accelerates coverage of the
  package's own logic.
- *Run the fuzzer for longer in CI (e.g., 5 minutes)*: rejected
  for v0.1.0. The chunk contract names 60 seconds; longer runs
  are a v0.2 hardening concern. The 60-second gate is sufficient
  to catch O(1)-reachable panics and untyped error paths.

---

## R-012 — `requested_ttl`, `refresh_nudge_before`, `boot_retry_timeout`, `cache_grace_ttl`: shared duration parsing

**Decision**: Use a shared helper
`parseDuration(raw string, def time.Duration, fieldName string)
(time.Duration, error)` that mirrors SDD-06's pattern: empty
string returns `def`; otherwise `time.ParseDuration(raw)`; on parse
error wrap with `ErrInvalidDuration`. Apply this helper to every
duration-bearing field: `requested_ttl`, `refresh_nudge_before`,
`boot_retry_timeout`, `cache_grace_ttl`.

For `cache_grace_ttl` specifically, the wire-shape uses `*string`
(pointer) so the materializer can distinguish "absent" from
"empty string" (R-005); the helper is invoked only when the
pointer is non-nil.

**Rationale**: Single helper for duration parsing keeps the
sentinel error mapping consistent across all fields. Mirrors
SDD-06's `parseDuration` (in `server.go`) — the same idiom across
both config packages reduces cognitive load. `time.ParseDuration`
is the stdlib-canonical duration parser; covers `"60s"`, `"10m"`,
`"4h"`, `"1h30m"` without invention.

**Alternatives considered**:
- *Inline `time.ParseDuration` at each call site*: rejected.
  Repeats the empty-string-default check four times; helper is
  cleaner.
- *Use a third-party duration parser (e.g.,
  `github.com/prometheus/common/model`)*: rejected. Stdlib
  suffices; Constitution XI.

---

## R-013 — `client_machine_index` typing and bounds

**Decision**: Decode `client_machine_index` as `uint32` (matching
SDD-01's `keys.DeriveClientKey(seed []byte, machineIndex uint32)`
signature). The TOML decoder rejects negative values (the field
type is unsigned) and values > `math.MaxUint32` (out of int range
in TOML). No explicit bound check is added at the loader level.

**Rationale**: BIP32 child-key indices are 32-bit unsigned (per
the BIP32 specification); SDD-01 already uses `uint32` for the
machine-index parameter. Aligning the config field type with the
downstream consumer's signature avoids any conversion drift. TOML
itself does not have an unsigned-integer type but pelletier/v2
will reject overflow / negative values when decoding into a
`uint32` Go field.

A tighter bound (e.g., `< 1024`) would be premature: the spec
Edge Cases do not call out a load-time bound on this field, and
the BIP32 derivation handles the full uint32 range correctly.

**Alternatives considered**:
- *Decode as `int` and bound-check*: rejected. Type alignment
  with `keys.DeriveClientKey` is cleaner; let the TOML decoder
  enforce the unsigned bound for free.
- *Use `int32` to allow `-1`-as-sentinel*: rejected. No sentinel
  shape is needed; the spec uses absence (the field is required)
  to mean "operator must explicitly choose".

---

## R-014 — Concurrency model for the loader

**Decision**: `Load(ctx context.Context, path string) (*Supervisor,
error)` is single-shot, synchronous, and safe for concurrent calls
from multiple goroutines. No package-level mutable state is
touched. `ctx.Err()` is inspected once at function entry; if
non-nil, the function returns immediately. No goroutine is spawned
at any point. The decoder's I/O is fully synchronous (single
`os.Open` + single `Decoder.Decode`); cancellation mid-decode is
not supported (consistent with SDD-06's pattern).

**Rationale**: A supervisor process loads its config once at
startup; concurrent loads are not part of the v0.1.0 design.
The lack of mid-decode cancellation is acceptable because the
configs are tiny (<2 KiB typical) and the decode completes in
~1 ms. Goroutine-spawning would violate Constitution IX's
"every goroutine has a clear owner" without a clear benefit.

**Alternatives considered**:
- *Run the decode in a goroutine and select on `ctx.Done()`*:
  rejected. Adds complexity for no benefit; configs are tiny.
- *Cache the loaded `*Supervisor` at the package level*:
  rejected. Constitution IX forbids mutable globals; SDD-19
  (state machine) is the right home for any caching.

---

## R-015 — Test-data fixtures and helpers

**Decision**: Tests use two helper styles:

1. **TOML literal in `t.TempDir()`-scoped file**: a helper
   `writeConfig(t, body string) (path string)` that writes the
   given body to `<TempDir>/supervise.toml` and returns the path.
   Used for unit tests where the TOML body matters and is small.

2. **Golden valid TOML fixture in `testdata/`**: a fixture file
   `testdata/valid_minimal.toml` and `testdata/valid_maximal.toml`
   used as the baseline for "modify one thing, expect rejection"
   tests. Each rejection-category test starts from one of these
   fixtures and mutates a single field.

No `internal/testutil` helpers are added in this chunk — SDD-04's
`testutil` package is for cross-package fixtures (vault, keys);
config-loading is package-local enough that local helpers are
the right fit.

**Rationale**: The two-style approach mirrors SDD-06's testing
pattern. Inline literal TOML keeps a single test self-contained
and readable; golden fixtures enable "spot the difference" tests
that exercise the validator in isolation. Both styles live within
the `_test.go` files, so they are not part of the production
binary and have no Constitution-IX implications.

**Alternatives considered**:
- *Generate TOML fixtures programmatically from a struct
  literal*: rejected. The whole point of the fuzzer + unit tests
  is to verify the loader's behaviour against operator-typed
  TOML; round-tripping through a Go-side encoder skips the bytes
  the operator would actually write.
- *Promote a fixture builder to `internal/testutil`*: rejected.
  No cross-package consumer; premature abstraction.
