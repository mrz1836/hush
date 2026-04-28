# Phase 1 Data Model: `internal/logging`

**Feature**: 005-logging
**Date**: 2026-04-27

This package is purely behavioural — it has no persistent storage, no on-the-wire schema, no database. The "data model" is the small set of in-process types the package exports plus the one private wrapper handler that composes the stdlib formatting handlers with the redaction logic.

---

## Exported types

### `Format` (enum)

```go
type Format int

const (
    FormatAuto Format = iota // 0 — TTY-detect at construction
    FormatText               // 1 — force text regardless of destination
    FormatJSON               // 2 — force JSON regardless of destination
)
```

| Field    | Type | Description |
|----------|------|-------------|
| (value)  | `int` | One of `FormatAuto`, `FormatText`, `FormatJSON`. Zero value is `FormatAuto` so an unset `Options.Format` field defaults correctly. |

**Validation**: any value outside the three named constants is treated as `FormatAuto` (defensive — the caller would have had to construct it with an integer literal). This is the spec's "format option with at least the values auto / text / JSON" (FR-005).

**State transitions**: none — `Format` is plain immutable data.

---

### `Options`

```go
type Options struct {
    Level  slog.Level
    Format Format
    Out    io.Writer
}
```

| Field    | Type           | Default-when-zero                                | Source             |
|----------|----------------|--------------------------------------------------|--------------------|
| `Level`  | `slog.Level`   | `slog.LevelInfo` (the zero value of `slog.Level` is `LevelInfo` because `LevelInfo == 0`) | FR-007 |
| `Format` | `Format`       | `FormatAuto` (zero value of the enum)            | FR-005             |
| `Out`    | `io.Writer`    | `os.Stderr` when `nil`                            | FR-004, R-009      |

**Validation**: the constructor reads each field once and applies the defaults above. The `Options` value is treated as immutable input — the constructor does not retain it, mutate it, or expose it after `New` returns. Concurrency-safe by virtue of being plain data passed by value.

**Lifecycle**: caller-built → passed to `New` → discarded. No reference is held by the returned logger.

---

## Returned handle

### `*slog.Logger` (stdlib concrete pointer)

`New(opts Options) *slog.Logger` — the locked API surface returns the stdlib logger pointer. The handler chain attached to it is:

```
slog.Logger
   └── redactingHandler (private wrapper — message redaction + PC clearing)
          └── slog.JSONHandler  OR  slog.TextHandler (stdlib formatter, configured per Options)
```

The wrapper handler holds:
- a reference to the inner `slog.Handler`
- the resolved `Format` (so `Handle` knows whether to clear `Record.PC` for non-error records)

The wrapper is private — `redactingHandler` (lowercase) lives in `logger.go`; consumers see only `*slog.Logger`.

---

## Exported helpers

### `RedactString(s string) string`

Pure function: `string → string`. Behaviour:
- On entry, ensures `RedactPatterns` is compiled (calls the `sync.Once`-gated lazy initializer).
- For each compiled pattern, replaces every match (`(*regexp.Regexp).ReplaceAllString` with replacement `[redacted]`).
- Returns the result. If no pattern matched, the returned value is byte-identical to the input.

Idempotent: `RedactString(RedactString(s)) == RedactString(s)` for all `s`. The literal `[redacted]` does not match any of the four credential patterns.

Allocations: at most `len(RedactPatterns)` regex scans; allocates a new string only on a match.

---

### `RedactPatterns []*regexp.Regexp` (exported package-level slice)

Read-only after first initialisation. Order matches the source list in `redact_patterns.go`. Length equals the number of credential classes shipped (currently four).

| Index | Class                | Source                                        |
|-------|----------------------|-----------------------------------------------|
| 0     | Anthropic API key    | `docs/SECURITY.md` §1.1                       |
| 1     | OpenAI project key   | `docs/SECURITY.md` §1.1                       |
| 2     | GitHub PAT           | `docs/SECURITY.md` §1.1                       |
| 3     | AWS access key       | `docs/SECURITY.md` §1.1                       |

(Final concrete regex strings are pinned in tasks/implement; see research R-008 for intent.)

---

## Private handler types

### `redactingHandler` (private — `logger.go`)

```go
type redactingHandler struct {
    inner       slog.Handler
    suppressPCBelow slog.Level // == slog.LevelError when format is JSON, math.MinInt32-equivalent (always-keep) when text
    clearPCAlways   bool       // true when format is text — drop PC at every level
}
```

Implements `slog.Handler`:

| Method                                     | Behaviour |
|--------------------------------------------|-----------|
| `Enabled(ctx, lvl) bool`                   | Delegates to `inner.Enabled`. |
| `Handle(ctx, r) error`                     | (1) `r.Message = RedactString(r.Message)`. (2) If `clearPCAlways` OR `r.Level < suppressPCBelow`, set `r.PC = 0`. (3) Delegate to `inner.Handle(ctx, r)`. |
| `WithAttrs(attrs []slog.Attr) slog.Handler`| Returns `&redactingHandler{inner: inner.WithAttrs(attrs), ...}` — preserves the redaction layer through `.With(...)` chains. |
| `WithGroup(name string) slog.Handler`      | Returns `&redactingHandler{inner: inner.WithGroup(name), ...}`. |

Concurrency: stateless after construction (the two flags are set once in `New`); inherits stdlib handler concurrency-safety.

**Note on the PC-suppression flags**: the simpler, equivalent formulation is a single boolean `wantSourceOnError` paired with the format choice — the data-model representation here favours readability of the handler's `Handle` body. The implementation may collapse to a single field if it reads more cleanly; the locked API does not constrain the internal representation.

---

## Acceptance-criterion → entity mapping

| AC / SC               | Entity / behaviour                                                       |
|-----------------------|--------------------------------------------------------------------------|
| FR-001, SC-018        | `*slog.Logger` (stdlib concurrency safety inherited)                     |
| FR-002, SC-016        | `New` builds a fresh handler chain; never touches `slog.Default`          |
| FR-003, SC-017        | No `init()`; `RedactPatterns` populated lazily via `sync.Once`            |
| FR-004, R-009         | `Options.Out == nil` → `os.Stderr`                                       |
| FR-005                | `Format` enum + zero-value `FormatAuto`                                   |
| FR-006, SC-001/002    | `New` does TTY-detect via `golang.org/x/term.IsTerminal` when auto        |
| FR-007, SC-005/006    | `Options.Level` (zero value `slog.LevelInfo`) threaded into `slog.HandlerOptions.Level` |
| FR-008, SC-007        | `redactingHandler.Handle` keeps `r.PC` for JSON ERROR                    |
| FR-009, SC-008        | `redactingHandler.Handle` zeroes `r.PC` for JSON < ERROR                 |
| FR-010, SC-009        | Text format passes `AddSource: false` to inner handler; `r.PC` always cleared |
| FR-011, FR-012, FR-013 | `ReplaceAttr` calls `Value.Resolve()` then optional regex pass on string |
| FR-014, FR-015        | `redactingHandler.Handle` redacts message; `ReplaceAttr` redacts string attrs |
| FR-016                | `RedactString` returns input unchanged when no pattern matched           |
| FR-017                | `RedactPatterns` covers `docs/SECURITY.md` §1.1 verbatim (4 classes)     |
| FR-018, SC-010        | Sentinel-leak test (`TestLogger_RedactionSentinel`)                      |
| FR-019, SC-011..014   | Per-pattern positive tests (`TestRedactPattern_*`)                       |
| FR-020, SC-019        | Pathological-input tests in `redact_test.go`                             |

---

## Anti-model (what is NOT modelled)

- **No persisted records, no schemas at rest**: the package writes to the configured `io.Writer` and stops.
- **No request / response payloads**: no wire protocol exists.
- **No mutable shared state**: `RedactPatterns` is a one-time-populated slice; any mutation by external code is undefined behaviour and out of scope.
- **No `context.Context` plumbing**: the package's I/O entry points are the stdlib `slog.Logger.{Info,Warn,Error,...}` methods, which already accept context. The constructor itself does no I/O.
- **No errors**: `New` returns `*slog.Logger` only — no error path exists. The locked API does not surface compilation failures because the patterns are compile-time constants validated by tests.
