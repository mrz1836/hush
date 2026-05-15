# Implementation Plan: Project-Wide Structured Logger with Redaction Enforcement

**Branch**: `005-logging` | **Date**: 2026-04-27 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/005-logging/spec.md`

## Summary

Build `internal/logging`: a tiny, stdlib-only package that returns a configured `*slog.Logger` whose handler chain refuses to render secrets. Two redaction rails compose: (1) a type-driven rail that resolves any `slog.LogValuer` value before rendering (so the SDD-02 `SecureBytes` container automatically prints `[redacted]`), and (2) a regex backstop that scans every emitted string — message and string attributes — against the four credential patterns enumerated verbatim in `docs/SECURITY.md` §1.1 (Anthropic `sk-ant-`, OpenAI `sk-proj-`, GitHub `ghp_`, AWS `AKIA[0-9A-Z]{16}`) and replaces every match with the literal `[redacted]`.

Output format auto-detects the destination (text on TTY, JSON otherwise) via `golang.org/x/term.IsTerminal`. Default level is `INFO`. Source location is included on `ERROR` records in JSON only — a wrapper handler clears `Record.PC` before non-error JSON records reach the inner handler so the stdlib's source machinery yields nothing. The constructor never mutates `slog.Default`; the package exports no `init()`. `RedactPatterns` is a package-level `[]*regexp.Regexp` lazily built once via `sync.Once` (constitutionally permitted as read-only sentinel-class data, parallel to `var Err... = errors.New(...)`).

Exported API (locked in `docs/PACKAGE-MAP.md` once SDD-05 ships):

```go
type Options struct {
    Level  slog.Level
    Format Format
    Out    io.Writer
}

type Format int
const (
    FormatAuto Format = iota
    FormatText
    FormatJSON
)

func New(opts Options) *slog.Logger
func RedactString(s string) string
var RedactPatterns []*regexp.Regexp
```

## Technical Context

**Language/Version**: Go 1.26.1 (per `go.mod`; floor-pinned per Constitution IX).
**Primary Dependencies**: stdlib `log/slog`, `io`, `os`, `regexp`, `sync`, `runtime`; one new direct dep `golang.org/x/term` (trusted-baseline — see research R-006).
**Storage**: N/A — operational logger writes to a caller-supplied `io.Writer` (default `os.Stderr`); no persistence, no buffering beyond what stdlib slog handlers provide.
**Testing**: `go test ./internal/logging/...` (table-driven unit tests per `.github/tech-conventions/testing-standards.md`); `go test -race`; coverage measured via `go test -cover` and reported through `codecov.yml`. No fuzz target is mandated by Constitution VIII for this chunk (the redaction backstop is a candidate for future fuzzing but not a release-gate fuzz target — see research R-007).
**Target Platform**: macOS (darwin) and Linux server hosts (project-wide v0.1.0). Windows is out of scope (spec).
**Project Type**: Internal Go library package under `internal/`. No external API surface.
**Performance Goals**: O(n·k) per log record where n = total emitted-string length and k = number of compiled patterns (4). At INFO-default cardinality (≪1 kHz on a vault host) this is non-measurable; no benchmark gate at SDD-05.
**Constraints**: (a) ZERO panics on any input the redaction rail can see (FR-020). (b) ZERO bytes of any matched credential or wrapped sentinel survive in captured output (FR-018, FR-019). (c) Concurrent emission must be race-clean under `-race` (SC-018, mirroring stdlib slog handler concurrency). (d) No mutable package-level state, no `init()` (Constitution IX).
**Scale/Scope**: ~5 source files (logger.go, redact.go, redact_patterns.go, logger_test.go, redact_test.go), single-binary scope, internal-only consumption. Estimated ≤ 250 LOC of production code; tests dominate by line count.

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

The package's load-bearing principles per the chunk doc are **IX (Idiomatic Go Discipline)**, **X (Observability & Redaction)**, and **XI (Native-First, Minimal Dependencies)**.

| Principle | Gate | Plan compliance |
|-----------|------|-----------------|
| **IX — Idiomatic Go Discipline** | No `init()` | Pattern slice is built lazily inside `RedactString` via `sync.Once`; no `init()` function exists in the package. |
| | No mutable package-level state | `RedactPatterns` is exported and populated exactly once on first call to `RedactString`, then never mutated. The pattern is read-only post-init — the same constitutional class as exported sentinel `var Err... = errors.New(...)` declarations elsewhere in the project (see SDD-01 `internal/keys`). |
| | Accept interfaces, return concrete types | `New` returns the concrete `*slog.Logger`; `Options` is plain data; only stdlib interfaces (`io.Writer`, `slog.Handler`) are accepted. |
| | Context propagation | The package contains no I/O entry points beyond what `slog.Logger`'s level methods already accept; no `context.Context` is stored or threaded by this package. |
| | Panic policy | Library code returns no errors; `New` is total over its `Options`. The redaction wrapper handler does not `recover()`; per spec, a misbehaving `LogValuer` is a defect of the wrapping type, not this package (matches stdlib behaviour). |
| | Goroutine discipline | Package spawns no goroutines. Concurrency safety inherits from stdlib slog handlers and `sync.Once`. |
| **X — Observability & Redaction** | Structured logging via `log/slog`; no third-party logger | Stdlib `log/slog` is the sole rendering library. |
| | Type-driven secret redaction | `slog.Value.Resolve()` (which invokes `LogValuer` transitively) is called inside `ReplaceAttr` before the regex backstop runs, guaranteeing `SecureBytes` and any future `LogValuer` type renders as `[redacted]` at every depth (recursion is automatic via stdlib handler iteration over `slog.Group`). |
| | No secret values in errors | `New` returns no error; nothing in this package can leak via an error path. |
| | Audit log separate from operational log | Spec lists audit emission as out-of-scope. This chunk produces operational logs only; the audit layer is a distinct package (Layer 6, future chunk). |
| | Discord alert tier dispatch | Out of scope per spec. This chunk produces structured records only; routing is upstream. |
| **XI — Native-First, Minimal Dependencies** | Prefer stdlib | Logger is stdlib `log/slog`. Regex is stdlib `regexp`. Sync is stdlib `sync`. |
| | New direct dependency justification | `golang.org/x/term` is the only new direct dep. `golang.org/x` is the stdlib-extension layer in the trusted-sources hierarchy (`.github/tech-conventions/dependency-management.md`). It is the canonical Go answer for the "is this fd a TTY?" question without CGO. Justification documented in research R-006; the PR description for the SDD-05 implement commit will repeat the justification. |
| | No new crypto dep | None — no crypto in this package. |
| | govulncheck / gitleaks | Repo-wide CI gates already cover this; nothing new this chunk requires. |

**Initial result**: PASS. No deviations require Complexity Tracking entries.

## Project Structure

### Documentation (this feature)

```text
specs/005-logging/
├── plan.md              # This file (/speckit-plan command output)
├── spec.md              # /speckit-specify output (already present)
├── research.md          # Phase 0 output (this command)
├── data-model.md        # Phase 1 output (this command)
├── quickstart.md        # Phase 1 output (this command)
├── contracts/
│   └── api.md           # Phase 1 output — exported-symbol contract
├── checklists/          # already populated by /speckit-checklist
└── tasks.md             # Phase 2 output (/speckit-tasks command — NOT created here)
```

### Source Code (repository root)

```text
internal/
└── logging/
    ├── logger.go             # Options, Format, New, redactingHandler wrapper
    ├── redact.go             # RedactString + sync.Once-gated lazy init helper
    ├── redact_patterns.go    # raw pattern strings + compileRedactPatterns()
    ├── logger_test.go        # TestNew_*, TestLogger_RedactionSentinel, source-location, level, slog.Default invariance, concurrency
    └── redact_test.go        # TestRedactPattern_* (one per pattern from docs/SECURITY.md §1.1), TestRedactString_NoMatch, multi-match, adjacent-match, edge cases

go.mod                        # adds golang.org/x/term direct dep
go.sum                        # checksum row added
```

**Structure Decision**: Single-package internal library at `internal/logging/`. Five source files matching the SDD-05 chunk doc verbatim. The wrapper handler type (`redactingHandler`) lives alongside `New` in `logger.go` — it is implementation detail, not part of the locked API. `RedactPatterns` and the lazy compilation helper live in two files only because the chunk doc names them separately; conceptually they are one unit.

## Post-Design Constitution Re-check

*Re-evaluated after Phase 1 (data-model, contracts, quickstart) was authored.*

| Principle | Phase 1 introduced | Re-check |
|-----------|--------------------|----------|
| **IX** | A private `redactingHandler` wrapper holding two scalar config fields and the inner stdlib `slog.Handler`. Stateless after construction; spawns no goroutines; defines no `init()`. `WithAttrs` / `WithGroup` rewrap to preserve the redaction layer through derived loggers. | PASS — no new globals, no new mutability, no `init()`, no goroutines, no CGO. |
| **X** | The handler chain redacts (a) the record's message string in the wrapper's `Handle`, and (b) string-kind attribute values in the inner handler's `ReplaceAttr` after `Value.Resolve()`. Recursion through `slog.Group` is delegated to the stdlib's per-attribute iteration of `ReplaceAttr`. Source-location is included for ERROR records in JSON only via `Record.PC` clearing. | PASS — both rails are testable, deterministic, and present in the contract document. |
| **XI** | Phase 1 introduced no new dependency beyond the `golang.org/x/term` already justified in research R-006. The wrapper handler uses stdlib types (`slog.Handler`, `slog.Record`, `slog.Level`) only. | PASS — dependency surface unchanged from initial gate. |

**Final result**: PASS. No deviations require Complexity Tracking entries.

## Complexity Tracking

> No Constitution Check violations. This section is intentionally empty.
