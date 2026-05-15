---
description: "Task list for SDD-05 — internal/logging (slog setup + LogValuer + regex redaction backstop)"
---

# Tasks: Project-Wide Structured Logger with Redaction Enforcement

**Input**: Design documents from `/Users/mrz/projects/hush/specs/005-logging/`
**Prerequisites**: plan.md (✓), spec.md (✓), research.md (✓), data-model.md (✓), contracts/api.md (✓), quickstart.md (✓)

**Tests**: TDD-MANDATORY per Constitution VIII. Every behaviour contract has a test-writing task BEFORE the implementation task that satisfies it. Tests MUST be written first, MUST fail, then implementation makes them pass. Coverage target ≥ 95% on `internal/logging/`.

**Organization**: Tasks are grouped by the six user stories from `spec.md` so each story can be implemented and tested as an independent increment.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Different files, no dependency on incomplete tasks — safe to run in parallel.
- **[Story]**: User-story label (US1..US6) maps task to its story for traceability.
- File paths are absolute from the repository root.

## Path Conventions

- Production source: `internal/logging/{logger.go, redact.go, redact_patterns.go}`
- Tests: `internal/logging/{logger_test.go, redact_test.go}`
- Module root: `/Users/mrz/projects/hush/`

---

## Pattern set divergence (read first)

The chunk doc Prompt-4 instruction (`docs/sdd/SDD-05.md` lines 181–183) names `TestRedactPattern_GoogleAIKey` and `TestRedactPattern_JWT`. The spec clarification of 2026-04-27 (`spec.md` §Clarifications) explicitly chose **Option A** — ship the four patterns named verbatim in `docs/SECURITY.md` §1.1 (Anthropic `sk-ant-`, OpenAI project `sk-proj-`, GitHub PAT `ghp_`, AWS `AKIA[0-9A-Z]{16}`). Research R-008 ratifies the narrower set and explicitly directs Tasks to substitute `TestRedactPattern_OpenAIProjectKey` for the dropped Google AI and JWT tests. The task list below honours the spec/plan/research over the older chunk-doc prompt; Google AI and JWT tests are NOT generated.

If `docs/SECURITY.md` §1.1 ever expands, a follow-up SDD chunk widens the pattern set; this chunk does not.

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Wire the package directory and the one new dependency before any source file is written.

- [X] T001 Create the package directory and Go file skeletons at `/Users/mrz/projects/hush/internal/logging/{logger.go, redact.go, redact_patterns.go, logger_test.go, redact_test.go}` — each file MUST start with `package logging` and an empty body (no imports yet). No `init()` anywhere.
- [X] T002 [P] Add the single new direct dependency `golang.org/x/term` to `/Users/mrz/projects/hush/go.mod` and run `go mod tidy` from the repo root to populate `/Users/mrz/projects/hush/go.sum`. Justification per research R-006 (trusted-baseline `golang.org/x/...` namespace; sole stdlib-portable TTY-detection primitive without CGO).

**Checkpoint**: `go build ./internal/logging/` compiles a hollow package; `go.mod` carries `golang.org/x/term`.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Type declarations and skeleton symbols that EVERY user story phase depends on. No behaviour yet — the bodies are stubs that compile and let tests for later stories fail loudly with the right messages.

**⚠️ CRITICAL**: No user-story phase can begin until Phase 2 is complete.

- [X] T003 In `/Users/mrz/projects/hush/internal/logging/logger.go`, declare the locked exported types from `contracts/api.md`: `type Format int` with constants `FormatAuto`, `FormatText`, `FormatJSON` (in that order — zero value MUST equal `FormatAuto`); `type Options struct { Level slog.Level; Format Format; Out io.Writer }`. Imports: `io`, `log/slog`. No `New` body yet (declared in T010).
- [X] T004 [P] In `/Users/mrz/projects/hush/internal/logging/redact_patterns.go`, declare the raw pattern source list (a private `[]string` literal of four entries: Anthropic `sk-ant-[A-Za-z0-9_\-]+`, OpenAI project `sk-proj-[A-Za-z0-9_\-]+`, GitHub PAT `ghp_[A-Za-z0-9]+`, AWS access key `AKIA[0-9A-Z]{16}` per research R-008). Add the `var redactPatternsOnce sync.Once` gate and a private `compileRedactPatterns()` helper that populates the exported `RedactPatterns` slice from the raw list. No callers yet.
- [X] T005 [P] In `/Users/mrz/projects/hush/internal/logging/redact.go`, declare `var RedactPatterns []*regexp.Regexp` and a `func RedactString(s string) string` skeleton that calls `redactPatternsOnce.Do(compileRedactPatterns)` then returns `s` unchanged (stub — body filled in T016). Imports: `regexp`, `sync` (the latter via the shared `redactPatternsOnce` declared in T004).
- [X] T006 In `/Users/mrz/projects/hush/internal/logging/logger.go`, declare the private `type redactingHandler struct { inner slog.Handler; format Format }` and stub-implement the four `slog.Handler` methods (`Enabled`, `Handle`, `WithAttrs`, `WithGroup`) by delegating verbatim to `inner` (no redaction or PC clearing yet — those land in US1/US2/US4 phases).
- [X] T007 In `/Users/mrz/projects/hush/internal/logging/logger.go`, declare `func New(opts Options) *slog.Logger` with a minimal placeholder body that returns `slog.New(slog.NewJSONHandler(os.Stderr, nil))`. This makes the package compile end-to-end so test files in later phases can compile-and-fail against a working symbol surface. Real behaviour lands in US1..US6.

**Checkpoint**: `go vet ./internal/logging/` is clean; `go test ./internal/logging/...` runs zero tests successfully; every locked exported symbol from `contracts/api.md` is in place.

---

## Phase 3: User Story 1 — SecureBytes / LogValuer never leak (Priority: P1) 🎯 MVP

**Goal**: A logger built by `New` invokes `LogValue()` on every `slog.LogValuer` attribute (including inside `slog.Group` at any depth) before rendering, so an SDD-02 `SecureBytes` wrapping any byte sequence renders as the literal `[redacted]`.

**Independent Test**: `TestLogger_RedactionSentinel` constructs a logger pointed at an in-memory writer, builds a `SecureBytes` wrapping `SECRET_SHOULD_NEVER_APPEAR_5`, logs it under several attribute keys (and once inside a nested `slog.Group`), and asserts the captured bytes contain zero occurrences of the sentinel and at least one `[redacted]`.

### Tests for User Story 1 (TDD — write first, watch fail) ⚠️

- [X] T008 [P] [US1] Write `TestLogger_RedactionSentinel` in `/Users/mrz/projects/hush/internal/logging/redact_test.go` per the spec's User-Story-1 Independent Test. Wrap the literal sentinel `SECRET_SHOULD_NEVER_APPEAR_5` in an SDD-02 `securebytes.SecureBytes`, log it under at least three attribute keys at INFO via `New(Options{Format: FormatJSON, Out: &buf})`, and assert (`require.NotContains` on `buf.String()`) zero occurrences of the sentinel plus (`require.Contains`) at least one `[redacted]`. Cover SC-010 / FR-018.
- [X] T009 [P] [US1] Write `TestLogger_LogValuerInNestedGroup` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`: same sentinel wrapped in `SecureBytes`, attached as a member of a `slog.Group("creds", "value", sb)` two levels deep. Assert sentinel absent and `[redacted]` present. Covers FR-012 (recursion at every depth) + AC-3 of US1.
- [X] T010 [P] [US1] Write `TestLogger_CustomLogValuerHonoured` in `/Users/mrz/projects/hush/internal/logging/logger_test.go` using a tiny inline test type whose `LogValue()` returns `slog.StringValue("[redacted]")`. Confirms FR-011 / AC-2: any `LogValuer` value renders as the resolved string regardless of underlying bytes.

### Implementation for User Story 1

- [X] T011 [US1] Replace the T007 stub in `/Users/mrz/projects/hush/internal/logging/logger.go`: `New` MUST construct an inner `slog.JSONHandler` (default for now — TTY pick lands in US3) with a `HandlerOptions{ReplaceAttr: ...}` whose callback (a) calls `a.Value = a.Value.Resolve()` (research R-001), (b) if `a.Value.Kind() == slog.KindString` reassigns `a.Value = slog.StringValue(RedactString(a.Value.String()))`, (c) returns the modified `a`. Wrap the inner handler in `redactingHandler` (T006) and feed it to `slog.New`. Default `opts.Out` to `os.Stderr` when nil (FR-004).
- [X] T012 [US1] In `/Users/mrz/projects/hush/internal/logging/logger.go`, make `redactingHandler.WithAttrs` and `redactingHandler.WithGroup` rewrap so the redaction layer survives derived loggers (research R-002 — return `&redactingHandler{inner: h.inner.WithAttrs(attrs), format: h.format}` and similarly for `WithGroup`). Re-run T008/T009/T010 — all three must now pass.

**Checkpoint**: User Story 1 ships. Every `LogValuer` value (notably `SecureBytes`) renders as `[redacted]` at every depth, even through `.With(...)` and `.WithGroup(...)` chains. T008–T010 are green.

---

## Phase 4: User Story 2 — Regex backstop catches credential strings (Priority: P1)

**Goal**: Every emitted string (record message + every string attribute value) is scanned against the four shipped credential patterns and every match is replaced with `[redacted]`. No bytes of any matched credential survive.

**Independent Test**: One `TestRedactPattern_*` per pattern asserts that a representative sample, logged as either the message or a string attribute, never appears in captured output and is replaced by `[redacted]`. `TestRedactString_*` table tests cover edge cases (no match, multi-match, adjacent, UTF-8 boundaries, very long input).

### Tests for User Story 2 (TDD — write first, watch fail) ⚠️

- [X] T013 [P] [US2] Write `TestRedactPattern_AnthropicKey` in `/Users/mrz/projects/hush/internal/logging/redact_test.go`: synthesise a non-real sample matching `sk-ant-[A-Za-z0-9_\-]+` (e.g. `sk-ant-fake0123456789abcdef` per R-011), log it twice — once as the record message, once as a string attribute — assert the sample appears nowhere in `buf.String()` and `[redacted]` appears at least twice. Covers SC-011 / FR-019.
- [X] T014 [P] [US2] Write `TestRedactPattern_OpenAIProjectKey` in `/Users/mrz/projects/hush/internal/logging/redact_test.go` (substituted for the chunk-doc's older `TestRedactPattern_GoogleAIKey` per research R-008). Sample: `sk-proj-fake0123456789abcdef`. Covers SC-012.
- [X] T015 [P] [US2] Write `TestRedactPattern_GitHubPAT` in `/Users/mrz/projects/hush/internal/logging/redact_test.go`. Sample: `ghp_fakeABCDEF0123456789`. Covers SC-013.
- [X] T016 [P] [US2] Write `TestRedactPattern_AWSAccessKey` in `/Users/mrz/projects/hush/internal/logging/redact_test.go`. Sample: `AKIAFAKEFAKEFAKEFAKE` (16 uppercase-or-digit chars after the `AKIA` prefix). Covers SC-014.
- [X] T017 [P] [US2] Write the `RedactString` edge-case battery in `/Users/mrz/projects/hush/internal/logging/redact_test.go`: `TestRedactString_NoMatch` (input preserved byte-identical — FR-016), `TestRedactString_MultipleMatchesSameString` (each replaced — edge cases §3), `TestRedactString_AdjacentMatches` (no surviving bytes — edge cases §4), `TestRedactString_EmbeddedInSurroundingText` (only the credential is replaced — AC-6 of US2), `TestRedactString_LongInput` (10 KB input with one match in the middle — SC-019 / FR-020), `TestRedactString_UTF8Boundaries` (multi-byte sequences flanking the match — FR-020). All MUST complete without panic; all MUST satisfy "input unchanged on no match, all matches replaced on match".
- [X] T018 [P] [US2] Write `TestRedactString_Idempotent` in `/Users/mrz/projects/hush/internal/logging/redact_test.go` asserting `RedactString(RedactString(s)) == RedactString(s)` for inputs covering each pattern, the no-match case, and a multi-match case (contracts/api.md §RedactString idempotence clause).

### Implementation for User Story 2

- [X] T019 [US2] In `/Users/mrz/projects/hush/internal/logging/redact_patterns.go`, fill `compileRedactPatterns` so that on first call it populates `RedactPatterns` with `regexp.MustCompile(...)` of each of the four raw pattern strings declared in T004. Order: Anthropic, OpenAI project, GitHub PAT, AWS access key (matches data-model.md and contracts/api.md).
- [X] T020 [US2] In `/Users/mrz/projects/hush/internal/logging/redact.go`, replace the T005 `RedactString` stub with the real body: after the `sync.Once`, iterate every `*regexp.Regexp` in `RedactPatterns` and call `re.ReplaceAllString(s, "[redacted]")` chained through the running result. Returns the input byte-identical when no pattern matched (FR-016 / contracts/api.md §RedactString contract). Verify T013–T018 turn green.
- [X] T021 [US2] In `/Users/mrz/projects/hush/internal/logging/logger.go`, finish `redactingHandler.Handle` so it (a) takes a local copy of the record (`r := r` — `slog.Record` is a value type per research R-002), (b) sets `r.Message = RedactString(r.Message)`, (c) delegates to `h.inner.Handle(ctx, r)`. With T011's `ReplaceAttr` already covering string attributes, message + attribute strings are now both protected (FR-014 / FR-015).

**Checkpoint**: User Story 2 ships. Every shipped credential pattern is redacted from message and string-attribute paths. Every `TestRedactPattern_*` and edge-case `TestRedactString_*` is green.

---

## Phase 5: User Story 3 — TTY auto-detect picks text vs JSON (Priority: P1)

**Goal**: `FormatAuto` (the zero value) chooses `slog.TextHandler` when `Options.Out` is a `*os.File` whose fd is a terminal, and `slog.JSONHandler` otherwise. `FormatText` and `FormatJSON` force the corresponding format regardless of destination.

**Independent Test**: Build a logger twice — once with a TTY-attached `*os.File` (a pty fixture or a real `os.Stdout` redirection guard), once with a `bytes.Buffer`. Assert the first emits text, the second emits JSON. Two more tests force-override and assert format ignores destination shape.

### Tests for User Story 3 (TDD — write first, watch fail) ⚠️

- [X] T022 [P] [US3] Write `TestNew_TTYDetectionPicksText` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Create a pty pair via `github.com/creack/pty` if available, OR fall back to opening `/dev/tty` and skipping the test under non-TTY CI (`t.Skip` with a clear reason). Construct `New(Options{Format: FormatAuto, Out: ttyFile})`, log one INFO record, capture the bytes, assert the output is slog text format (contains `level=INFO` and `msg=` rather than the JSON `"level":"INFO"` shape). Covers SC-001.
  - Note: if no pty dep is acceptable per Constitution XI, the test MAY use a `*os.File` returned by `os.NewFile(uintptr(fd), name)` from a known-TTY fd captured at test start; the implementation detail is the test author's choice as long as the assertion holds. Document the chosen approach in the test file's package comment.
- [X] T023 [P] [US3] Write `TestNew_NonTTYPicksJSON` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Construct `New(Options{Format: FormatAuto, Out: &bytes.Buffer{}})`, log INFO, assert the output begins with `{` and parses as a single JSON object containing `level`, `msg`, `time`. Covers SC-002 / FR-006 ("non-file writer MUST be treated as not a terminal").
- [X] T024 [P] [US3] Write `TestNew_FormatTextOverride` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Construct `New(Options{Format: FormatText, Out: &bytes.Buffer{}})` (a non-TTY destination) and assert text format. Covers SC-003.
- [X] T025 [P] [US3] Write `TestNew_FormatJSONOverride` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Construct `New(Options{Format: FormatJSON, Out: <a TTY *os.File>})` (or equivalent) and assert JSON format. Covers SC-004.

### Implementation for User Story 3

- [X] T026 [US3] In `/Users/mrz/projects/hush/internal/logging/logger.go`, expand `New` to resolve the effective format: if `opts.Format == FormatText` → use `slog.NewTextHandler`. If `opts.Format == FormatJSON` (or any out-of-range int) → use `slog.NewJSONHandler`. If `opts.Format == FormatAuto`, type-assert `opts.Out` to `*os.File`; if the assertion succeeds AND `golang.org/x/term.IsTerminal(int(f.Fd()))` returns true, choose `slog.NewTextHandler`; otherwise `slog.NewJSONHandler` (research R-004). Continue to wrap the chosen inner handler in `redactingHandler` and feed `slog.New`. T022–T025 must all be green.

**Checkpoint**: User Story 3 ships. Auto-detect picks the right format for the destination; explicit overrides win regardless of destination.

---

## Phase 6: User Story 4 — Source location: ERROR-JSON only (Priority: P2)

**Goal**: JSON ERROR records carry `source` (file + line); JSON DEBUG/INFO/WARN do not; text records never do at any level.

**Independent Test**: Three tests — JSON-ERROR includes `source`, JSON-non-ERROR omits `source`, text-any-level omits `source`.

### Tests for User Story 4 (TDD — write first, watch fail) ⚠️

- [X] T027 [P] [US4] Write `TestNew_JSONErrorIncludesSource` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Build `New(Options{Format: FormatJSON, Out: &buf})`, emit one ERROR record, parse the JSON object, assert presence of a `source` field whose `file` ends in `logger_test.go` and whose `line` is plausible (>0). Covers SC-007 / FR-008.
- [X] T028 [P] [US4] Write `TestNew_JSONNonErrorOmitsSource` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Same setup; emit one record at each of DEBUG (after raising level), INFO, WARN. For each, parse the JSON and assert no `source` key is present. Covers SC-008 / FR-009.
- [X] T029 [P] [US4] Write `TestNew_TextOmitsSource` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Build `New(Options{Format: FormatText, Out: &buf, Level: slog.LevelDebug})`, emit DEBUG/INFO/WARN/ERROR; assert the output line for each contains no `source=` substring. Covers SC-009 / FR-010.

### Implementation for User Story 4

- [X] T030 [US4] In `/Users/mrz/projects/hush/internal/logging/logger.go`, when constructing the inner handler in `New`: pass `slog.HandlerOptions{Level: opts.Level, AddSource: true, ReplaceAttr: ...}` for the JSON branch; pass `slog.HandlerOptions{Level: opts.Level, AddSource: false, ReplaceAttr: ...}` for the text branch (research R-003). Persist the resolved `Format` on the constructed `redactingHandler{format: ...}`.
- [X] T031 [US4] In `/Users/mrz/projects/hush/internal/logging/logger.go`, extend `redactingHandler.Handle` to honour FR-008..FR-010 by clearing `r.PC` on the local record copy when (`h.format == FormatText`) OR (`h.format == FormatJSON && r.Level < slog.LevelError`). Stdlib's source machinery emits no `source` attribute when `PC == 0` (research R-003). Re-run T027–T029 — all three must be green.

**Checkpoint**: User Story 4 ships. ERROR records in JSON carry source; everything else does not.

---

## Phase 7: User Story 5 — Default level INFO + configurable (Priority: P2)

**Goal**: A logger built with the zero `Options{}` drops DEBUG and emits INFO/WARN/ERROR. Explicit `slog.LevelDebug` emits all four levels; explicit `slog.LevelError` emits only ERROR.

### Tests for User Story 5 (TDD — write first, watch fail) ⚠️

- [X] T032 [P] [US5] Write `TestNew_DefaultLevelInfo` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Build `New(Options{Format: FormatJSON, Out: &buf})` (no Level set — relies on `slog.Level` zero == `LevelInfo`). Emit one record at each level. Assert captured `buf` contains exactly three records, none of which is the DEBUG one. Covers SC-005 / FR-007.
- [X] T033 [P] [US5] Write `TestNew_ExplicitDebugLevel` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Build with `Level: slog.LevelDebug`; emit at every level; assert four records present including DEBUG. Covers SC-006 (DEBUG branch).
- [X] T034 [P] [US5] Write `TestNew_ExplicitErrorLevel` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Build with `Level: slog.LevelError`; emit at every level; assert only the ERROR record is present. Covers SC-006 (ERROR branch).

### Implementation for User Story 5

- [X] T035 [US5] In `/Users/mrz/projects/hush/internal/logging/logger.go`, ensure `New` threads `opts.Level` into the inner handler's `slog.HandlerOptions.Level` field (already wired during T030 — this task verifies and confirms with T032–T034 turning green). No additional code MAY be required if T030 was complete; otherwise wire it now.

**Checkpoint**: User Story 5 ships. Default level is INFO; level is configurable per logger; multiple loggers can hold different levels independently.

---

## Phase 8: User Story 6 — No mutation of slog.Default (Priority: P2)

**Goal**: `New` produces a configured `*slog.Logger` and changes nothing about `slog.Default`. The package contains no `init()`. Two loggers with conflicting options can run concurrently with no shared mutable state.

### Tests for User Story 6 (TDD — write first, watch fail) ⚠️

- [X] T036 [P] [US6] Write `TestNew_DoesNotMutateSlogDefault` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Capture `before := slog.Default()`; call `New` with several diverse `Options` combinations (json/text, info/debug/error, stderr/buffer); assert `slog.Default() == before` after each. Covers SC-016 / FR-002 (research R-012).
- [X] T037 [P] [US6] Write `TestPackage_NoInitFunction` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Statically verify there is no `init` function: implement the test as a build-time guard using `go/parser` to walk this package's source files (located via `runtime.Caller(0)` + `filepath.Dir`) and assert no `*ast.FuncDecl` named `init` is found. Covers SC-017 / FR-003 (research R-005).
- [X] T038 [P] [US6] Write `TestLogger_ConcurrentEmissionRaceFree` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Construct one logger, spawn ≥ 16 goroutines each calling INFO/WARN/ERROR ≥ 100 times concurrently against the same logger handle. The test asserts no panic and no error; the race detector (run via `go test -race` in T046) is the actual race-freedom oracle. Covers SC-018 / FR-001 (research R-010).
- [X] T039 [P] [US6] Write `TestLogger_TwoIndependentLoggersDoNotInterfere` in `/Users/mrz/projects/hush/internal/logging/logger_test.go`. Build logger A (Format: JSON, Level: Debug, Out: bufA); build logger B (Format: Text, Level: Error, Out: bufB). Use both concurrently. Assert bufA contains only JSON records (and includes DEBUG) and bufB contains only text records (and excludes everything below ERROR). Covers AC-2 of US6 / SC-018.

### Implementation for User Story 6

- [X] T040 [US6] In `/Users/mrz/projects/hush/internal/logging/logger.go` and `/Users/mrz/projects/hush/internal/logging/redact.go`, audit and confirm: (a) no call to `slog.SetDefault` anywhere in the package, (b) no `init()` function, (c) no mutable package-level state besides the one-shot `redactPatternsOnce` + the `RedactPatterns` slice it populates exactly once (the constitutionally-permitted sentinel-class pattern per research R-005). If a `//nolint:gochecknoglobals` annotation is required to silence lint on `RedactPatterns`, add it on the `var RedactPatterns` line with a comment citing the locked API obligation. Run T036–T039 — all four must be green.

**Checkpoint**: User Story 6 ships. The package never touches `slog.Default`, has no `init()`, and supports independent concurrent loggers with conflicting options.

---

## Phase 9: Polish & Cross-Cutting Concerns

**Purpose**: Cross-cutting validation, the constitutional gate trio, coverage, the load-bearing sentinel re-confirmation, and quickstart cross-check.

- [X] T041 [P] Run `go test -cover ./internal/logging/...` from `/Users/mrz/projects/hush/` and confirm coverage ≥ 95% on `internal/logging`. If under target, add table rows to existing `redact_test.go` / `logger_test.go` tests rather than creating new test files (keep per-file responsibility split per research R-013).
- [X] T042 [P] Confirm quickstart.md examples compile and run: in a scratch `quickstart_smoke_test.go` (deleted at end of task), copy each runnable example from `/Users/mrz/projects/hush/specs/005-logging/quickstart.md` §1, §2, §3, §4, §5 verbatim and ensure they all build and pass. Then delete the scratch test — the canonical tests already cover the same surface.
- [X] T043 Run `magex format:fix` from `/Users/mrz/projects/hush/` repo root. The command MUST exit 0 with no remaining diff inside `internal/logging/`.
- [X] T044 Run `magex lint` from `/Users/mrz/projects/hush/` repo root. The command MUST exit 0 with zero findings on `internal/logging/`. If `gochecknoglobals` flags `RedactPatterns`, the `//nolint:gochecknoglobals` annotation added in T040 should suffice; do not silence any other linter without a constitutional justification.
- [X] T045 Run `magex test:race` from `/Users/mrz/projects/hush/` repo root. The command MUST exit 0 with zero data races reported anywhere in `internal/logging/`. T038's concurrent-emission test is the load-bearing race exerciser.
- [X] T046 Re-confirm the sentinel invariant: re-run `go test -run TestLogger_RedactionSentinel ./internal/logging/...` and visually inspect `-v` output; assert the captured log buffer (printed by the test on failure) does not contain the literal `SECRET_SHOULD_NEVER_APPEAR_5` anywhere.

**Checkpoint**: Coverage ≥ 95%, race-clean, format-clean, lint-clean, sentinel-clean. The chunk is ready for the SDD-05 IMPLEMENT-phase combined commit.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: no dependencies — start immediately.
- **Phase 2 (Foundational)**: depends on Phase 1. BLOCKS every user-story phase.
- **Phase 3 (US1, P1)**: depends on Phase 2. The first MVP story.
- **Phase 4 (US2, P1)**: depends on Phase 2. Independent of US1's implementation (different code paths) but T021 modifies the same `Handle` body that US1 wired in T011 — sequence US1 → US2.
- **Phase 5 (US3, P1)**: depends on Phase 2. Independent of US1/US2 in source (touches `New`'s handler-pick branch only).
- **Phase 6 (US4, P2)**: depends on Phase 2. Touches `redactingHandler.Handle` PC-clear logic and inner-handler `AddSource` flag — sequence after US2/US3 to avoid `Handle`-body merge conflicts within a single working session.
- **Phase 7 (US5, P2)**: depends on Phase 2. Independent of US1/US2/US3/US4 once T030 wires the level. Trivially small.
- **Phase 8 (US6, P2)**: depends on Phase 2. Independent of all other stories (mostly tests + audit).
- **Phase 9 (Polish)**: depends on every preceding phase.

### User Story Dependencies

- **US1 (P1)**: independent of every other story.
- **US2 (P1)**: independent of every other story; T021 edits the same `Handle` method as US1 so commit US1 first.
- **US3 (P1)**: independent of every other story.
- **US4 (P2)**: independent of every other story; commit after US2 to avoid `Handle` body collisions.
- **US5 (P2)**: independent of every other story.
- **US6 (P2)**: independent of every other story.

### Within Each User Story

- Tests are written FIRST (TDD-mandatory per Constitution VIII) and confirmed FAILING before any implementation task in the same phase begins.
- Then implementation tasks land in listed order (model/types → handler wiring → constructor finishing).
- Story phase exits only when every test in the phase is green AND no other story's tests have regressed.

### Parallel Opportunities

- All `[P]` tasks within Phase 1 (T002 alongside T001 once T001's `mkdir` lands).
- All `[P]` tasks within Phase 2 (T004 / T005 are different files — T003 must land first because it adds package-imported types).
- All `[P]` test-writing tasks within a single user-story phase (different test names / different file regions).
- Phase 9's `[P]`-marked verification tasks T041, T042 can run alongside one another; T043/T044/T045 must run sequentially (each can mutate working-copy state).

---

## Parallel Example: User Story 2 tests

```bash
# Launch all US2 test-writing tasks together (different test functions in the same redact_test.go — co-author edits or sequential file edits work; conceptually parallel work):
Task: "Write TestRedactPattern_AnthropicKey in internal/logging/redact_test.go"
Task: "Write TestRedactPattern_OpenAIProjectKey in internal/logging/redact_test.go"
Task: "Write TestRedactPattern_GitHubPAT in internal/logging/redact_test.go"
Task: "Write TestRedactPattern_AWSAccessKey in internal/logging/redact_test.go"
Task: "Write the RedactString edge-case battery in internal/logging/redact_test.go"
Task: "Write TestRedactString_Idempotent in internal/logging/redact_test.go"
```

Within an LLM session: serialise these because they all edit the same file. Across human + LLM with separate work-items: parallelise — each owns one test name.

---

## Implementation Strategy

### MVP First (User Story 1 only — P1)

1. Phase 1: Setup (T001–T002).
2. Phase 2: Foundational (T003–T007).
3. Phase 3: User Story 1 (T008–T012).
4. **STOP and validate**: `go test -run TestLogger ./internal/logging/...` proves SecureBytes never leaks. The sentinel invariant — the project's load-bearing operational guarantee — is in force.
5. (Optional ship gate.)

### Incremental Delivery

1. Setup + Foundational → infrastructure ready.
2. US1 (P1) → MVP — the LogValuer rail is the project's primary defence.
3. US2 (P1) → backstop — the regex rail closes the mistake-path leak class.
4. US3 (P1) → format auto-detect — operator ergonomics.
5. US4 (P2) → ERROR source location.
6. US5 (P2) → level configuration.
7. US6 (P2) → no-mutation-of-default + concurrency proof.
8. Polish (Phase 9) → gates, coverage, sentinel re-confirmation.

### TDD Cadence (per user-story phase)

1. Write the test(s) for the phase. Run the test(s). Confirm RED.
2. Write the minimum implementation that turns them GREEN.
3. Re-run all preceding-phase tests. Confirm no regression.
4. Move on to the next phase.

---

## Notes

- Every test name listed in the user input is generated EXCEPT `TestRedactPattern_GoogleAIKey` and `TestRedactPattern_JWT`, which are dropped per the spec clarification of 2026-04-27 and replaced with `TestRedactPattern_OpenAIProjectKey`. This pivot is documented above and in `research.md` R-008.
- `TestLogger_RedactionSentinel` wraps the literal sentinel `SECRET_SHOULD_NEVER_APPEAR_5` per the user-input contract.
- The final phase invokes `magex format:fix`, `magex lint`, and `magex test:race` per the user-input mandate (T043, T044, T045).
- `[P]` markers indicate tasks editing different files OR different non-overlapping regions of the same test file with no implementation-order dependency — the implementer's judgement applies on whether to run them truly in parallel within one editing session.
- Every task path is absolute. Every task names a file. Every task identifies the spec / FR / SC / research entry it satisfies.
