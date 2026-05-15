# Contract: `internal/logging` exported API

**Feature**: 005-logging
**Status**: Locked at SDD-05; mirrored into `docs/PACKAGE-MAP.md` once the implement commit lands.

This is the only contract this package exposes. Every internal package downstream of SDD-05 depends on these symbols and signatures. Changes after SDD-05 lands require a new SDD chunk and a constitutional amendment if any consumer relies on the surface.

---

## Package path

```
github.com/mrz1836/hush/internal/logging
```

---

## Exported symbols

### `type Format int`

```go
type Format int

const (
    FormatAuto Format = iota
    FormatText
    FormatJSON
)
```

**Contract**:
- `FormatAuto` is the zero value and means "auto-detect at construction time per FR-006".
- `FormatText` forces text format regardless of destination.
- `FormatJSON` forces JSON format regardless of destination.
- Any other integer value passed in `Options.Format` is treated as `FormatAuto`.

---

### `type Options struct`

```go
type Options struct {
    Level  slog.Level
    Format Format
    Out    io.Writer
}
```

**Contract**:
- `Level`: minimum level to emit. Zero value (`slog.LevelInfo`) is the default per FR-007.
- `Format`: format selector per `Format` contract above.
- `Out`: destination writer. `nil` means `os.Stderr` per FR-004.
- The constructor reads each field once; the value MAY be discarded by the caller after `New` returns.

---

### `func New(opts Options) *slog.Logger`

```go
func New(opts Options) *slog.Logger
```

**Inputs**: a `Options` value. All fields are optional; the zero value of `Options` produces a logger that writes JSON to `os.Stderr` at `INFO` level — auto-detection runs and reports "not a terminal" because `os.Stderr` redirected by Go-test machinery is not a TTY in test contexts (it would be a TTY when run interactively).

**Output**: a configured `*slog.Logger` with the package's redaction handler chain installed:
1. A wrapper handler that redacts the record's message string and (in JSON format) clears `Record.PC` for non-error records to suppress source location.
2. The stdlib `slog.JSONHandler` or `slog.TextHandler` configured with:
   - `Level: opts.Level` (or `slog.LevelInfo` when zero)
   - `AddSource: true` for JSON, `false` for text
   - `ReplaceAttr` callback that resolves `LogValuer` values then runs `RedactString` on string-kind values

**Side effects**: NONE on `slog.Default`. NONE on any package-level mutable state. The first call across the process triggers compilation of `RedactPatterns` via `sync.Once`; subsequent calls reuse the compiled slice.

**Concurrency**: the returned logger is safe for concurrent use by multiple goroutines (mirrors stdlib slog).

**Error handling**: `New` does not return an error. Inputs are total — any combination of `Options` produces a working logger.

---

### `func RedactString(s string) string`

```go
func RedactString(s string) string
```

**Inputs**: any UTF-8 (or otherwise) byte sequence as a Go `string`.

**Output**:
- If no pattern in `RedactPatterns` matches `s`, returns `s` byte-identical.
- If at least one pattern matches, returns a new string in which every match (across all patterns) has been replaced with the literal `[redacted]`. Multiple matches: each replaced. Adjacent matches: each replaced. Surrounding text: preserved.

**Side effects**: lazy-initialises `RedactPatterns` on first call across the process via `sync.Once`. Subsequent calls observe the same compiled slice.

**Concurrency**: safe for concurrent use. The `sync.Once` guarantees exactly-one compilation; reads of the resulting slice are race-free.

**Determinism**: pure function of `s`. Same input → same output across the process lifetime (patterns are compile-time constants).

**Idempotence**: `RedactString(RedactString(s)) == RedactString(s)` for all `s`.

---

### `var RedactPatterns []*regexp.Regexp`

```go
var RedactPatterns []*regexp.Regexp
```

**Contract**:
- Read-only after first initialisation. The package guarantees no in-package mutation after the `sync.Once` populates it. External mutation by consumers is undefined behaviour and forbidden.
- Length equals the number of credential classes shipped (currently 4 — see `docs/SECURITY.md` §1.1).
- Each element is a `*regexp.Regexp` matching exactly one credential class.
- May be `nil` if `RedactString` has not yet been called anywhere in the process. Downstream consumers SHOULD call `RedactString` first or rely on `New` (which builds a logger that calls `RedactString` on every record's message).

---

## Behavioural invariants (testable contract)

| Invariant | Spec ref | Test name (in tasks phase) |
|-----------|----------|----------------------------|
| Logger emits TEXT to a TTY destination under FormatAuto | SC-001 | `TestNew_TTYDetectionPicksText` |
| Logger emits JSON to a non-TTY destination under FormatAuto | SC-002 | `TestNew_NonTTYPicksJSON` |
| Default level is INFO; DEBUG is dropped, INFO/WARN/ERROR pass | SC-005 | `TestNew_DefaultLevelInfo` |
| `New` does not change `slog.Default()` | SC-016 | `TestNew_DoesNotMutateSlogDefault` |
| Source location is present on JSON ERROR | SC-007 | `TestNew_JSONErrorIncludesSource` |
| Source location is absent on JSON DEBUG/INFO/WARN | SC-008 | `TestNew_JSONNonErrorOmitsSource` |
| Source location is absent on TEXT at every level | SC-009 | `TestNew_TextOmitsSource` |
| Sentinel byte sequence wrapped in `SecureBytes` never appears in output | SC-010 | `TestLogger_RedactionSentinel` |
| Anthropic API key sample never appears in output | SC-011 | `TestRedactPattern_AnthropicKey` |
| OpenAI project key sample never appears in output | SC-012 | `TestRedactPattern_OpenAIProjectKey` |
| GitHub PAT sample never appears in output | SC-013 | `TestRedactPattern_GitHubPAT` |
| AWS access key sample never appears in output | SC-014 | `TestRedactPattern_AWSAccessKey` |
| `RedactString` is total over pathological inputs | SC-019 | `TestRedactString_LongInput`, `TestRedactString_AdjacentMatches`, `TestRedactString_UTF8Boundaries` |
| Concurrent emission is race-clean | SC-018 | `TestLogger_ConcurrentEmissionRaceFree` |

---

## Non-contract (explicit non-promises)

- **No exported handler type**. The redaction wrapper is private. Consumers MUST construct via `New` — they cannot inject a different inner handler.
- **No exported "configure once" function**. The package has no global default; every caller obtains an independent logger via `New`.
- **No registration / discovery**. Consumers receive `*slog.Logger` and thread it through their own constructors / context.
- **No log routing**. The logger writes to `Options.Out`. There is no built-in tee, fanout, multi-destination, or async-buffer.
- **No level threshold change at runtime**. Once `New` returns, the level is fixed for that logger. To change the level, build a new logger.

---

## Deprecation policy

This contract is **frozen** at SDD-05 ship. Adding a new pattern to `RedactPatterns` is non-breaking. Adding a new field to `Options` is non-breaking iff its zero value is backward-compatible. Removing or renaming any exported symbol requires a new SDD chunk; consumers numbered SDD-06+ all depend on the surface above.
