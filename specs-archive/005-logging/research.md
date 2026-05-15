# Phase 0 Research: `internal/logging`

**Feature**: 005-logging — project-wide stdlib slog logger with redaction enforcement
**Date**: 2026-04-27

This document resolves every technical decision the plan depends on. Each entry follows the **Decision / Rationale / Alternatives considered** format. There are no remaining `NEEDS CLARIFICATION` markers in the spec; the only spec clarification (Session 2026-04-27) selected Option A — the four credential patterns enumerated in `docs/SECURITY.md` §1.1 verbatim.

---

## R-001 — How to enforce LogValuer resolution before regex redaction

**Decision**: Inside the handler's `ReplaceAttr` callback, call `a.Value = a.Value.Resolve()` first, then test `a.Value.Kind() == slog.KindString` and run `RedactString` over the resulting string. Reassign and return.

**Rationale**: `slog.Value.Resolve()` walks the `LogValuer` chain (handling the case where a `LogValue()` returns another `LogValuer`) and is idempotent. The stdlib `JSONHandler` and `TextHandler` both call `Resolve()` themselves before invoking `ReplaceAttr`, so the explicit call is defensive belt-and-suspenders — it guarantees correct behaviour even if a future custom handler implementation forgets to resolve. Recursion into `slog.Group` attributes is provided automatically by the stdlib: the handler iterates each group member and invokes `ReplaceAttr` per member, with the `groups []string` parameter naming the path. We do not need to recurse manually.

**Alternatives considered**:
- *Skip explicit `Resolve()`, rely on stdlib*: rejected. The chunk-doc HOW contract is "call LogValuer first if it implements it" — the explicit call is contractual and the cost is one branch.
- *Custom handler that walks attributes itself*: rejected. Reproducing stdlib's group recursion is fragile and adds surface for bugs without behavioural gain.

---

## R-002 — How to redact the record's message string (not just attributes)

**Decision**: Wrap the formatting handler (`slog.JSONHandler` or `slog.TextHandler`) in a thin `redactingHandler` whose `Handle(ctx, r)` rewrites `r.Message = RedactString(r.Message)` before delegating to the inner handler. `Enabled`, `WithAttrs`, `WithGroup` all delegate; `WithAttrs`/`WithGroup` rewrap so the redaction layer survives derived loggers.

**Rationale**: `ReplaceAttr` operates only on `slog.Attr` values; the record's message field is not an attribute and never passes through `ReplaceAttr`. Spec FR-014 explicitly requires redaction of the message text. A wrapper handler is the minimal stdlib-aligned way to intercept the message. `slog.Record` is a value type so mutating its `Message` field on a local copy does not affect the caller's record (`Logger.log` already passes a fresh record per emit).

**Alternatives considered**:
- *Pre-redact message at the call site*: rejected. The whole point of the package is to refuse-by-construction; relying on caller discipline defeats the type-driven guarantee.
- *Replace `slog.Logger` with a custom wrapper*: rejected. The locked API returns `*slog.Logger` (the stdlib concrete type) so callers can use `.With`, `.WithGroup`, `slog.Default`-style ergonomics unchanged. Wrapping the handler preserves the API.

---

## R-003 — How to include source location for ERROR-only in JSON, never in text

**Decision**: For the JSON inner handler, set `slog.HandlerOptions.AddSource = true`. For the text inner handler, set `AddSource = false`. In the wrapper `redactingHandler.Handle`, before delegating, check the format and the level: if format is JSON and `r.Level < slog.LevelError`, set `r.PC = 0` on the local record copy. The stdlib JSON handler treats `PC == 0` as "no source available" and emits no `source` attribute.

**Rationale**: `slog.HandlerOptions.AddSource` is a single boolean — there is no built-in "include source only for these levels" knob. `ReplaceAttr` is called with a `*slog.Source` value when source is added, but `ReplaceAttr`'s signature does not expose the record's level (only the attribute and its containing groups), so a level-conditional decision cannot be made there. Clearing `r.PC` before delegation is the cleanest stdlib-aligned mechanism: the source machinery short-circuits when `PC == 0`. This satisfies FR-008 (source on JSON ERROR), FR-009 (no source on JSON DEBUG/INFO/WARN), FR-010 (no source on text at any level).

**Alternatives considered**:
- *Use `ReplaceAttr` with a closure that captures the record's level via shared state*: rejected. The shared state introduces a data race the moment the logger is used concurrently; a per-emit shared mutable variable is precisely what Constitution IX bans.
- *Two parallel handler instances and route in the wrapper*: rejected. Pre-applied attrs/groups (`.With`, `.WithGroup`) make this much harder to keep in sync — every derived logger would need to maintain two parallel chains.
- *Drop the source attribute in `ReplaceAttr` based on the source's filename*: rejected. No reliable way to test level from `ReplaceAttr`.

---

## R-004 — TTY detection mechanism

**Decision**: When `opts.Format == FormatAuto`, type-assert the writer to `*os.File` (using `if f, ok := opts.Out.(*os.File); ok`) and call `golang.org/x/term.IsTerminal(int(f.Fd()))`. If the assertion fails (the writer is `bytes.Buffer`, a custom writer, a pipe wrapper, etc.), default to `FormatJSON`. If the writer is a `*os.File` and `IsTerminal` returns true, choose `FormatText`; otherwise `FormatJSON`.

**Rationale**: Spec FR-006 mandates "a non-file writer MUST be treated as not a terminal". The `*os.File` type-assertion captures exactly that case; `IsTerminal` then handles the file-vs-pipe distinction for actual file descriptors. `golang.org/x/term` is the canonical Go answer for this question — see R-006 for the dependency justification.

**Alternatives considered**:
- *`os.Stat(stdout).Mode()&os.ModeCharDevice != 0`*: rejected. `ModeCharDevice` is not portable across all macOS terminals (Apple's pty behaviour has corner cases); `golang.org/x/term.IsTerminal` calls the `isatty(3)` syscall directly via `syscall.Termios`, which is the reliable check.
- *Heuristic on environment (`TERM` set, etc.)*: rejected. Environment-based TTY detection is unreliable in launchd/systemd contexts; the spec explicitly calls out launchd/systemd as a target deployment mode.

---

## R-005 — Where to construct the compiled regex slice (no `init()`)

**Decision**: A package-level `var redactPatternsOnce sync.Once` plus `var RedactPatterns []*regexp.Regexp` (exported per the locked API). A package-private helper `func ensurePatterns()` calls `redactPatternsOnce.Do(compileRedactPatterns)`. `RedactString` calls `ensurePatterns()` on entry. `compileRedactPatterns` populates `RedactPatterns` from the raw pattern source list in `redact_patterns.go`.

**Rationale**: Constitution IX bans `init()`. Lazy initialisation via `sync.Once` is the idiomatic Go alternative for "compile once, share read-only across the process". The cost is one atomic load on every call to `RedactString`, which is negligible. `RedactPatterns` is exported because the locked API names it — its read-only-after-first-use property is the sentinel-class exception (parallel to `var ErrSecretNotFound = errors.New(...)`). Linters (`gochecknoglobals`) may flag it; the package will accept a single-line `//nolint:gochecknoglobals` annotation **only if** lint actually fires, and the annotation will cite the locked API obligation.

**Alternatives considered**:
- *Compile inline on every call*: rejected. The four `regexp.Regexp` compilations on every log emission would be wasteful; the compiled patterns are immutable.
- *Pass patterns as a constructor parameter*: rejected. The locked API does not include a pattern parameter; the chunk doc explicitly fixes the surface.
- *Build patterns in `New` and store on the handler*: rejected. `RedactString` is a public top-level function (locked API) that must work without a constructor having run. The only way to satisfy both "no init" and "RedactString is callable standalone" is `sync.Once`.

---

## R-006 — `golang.org/x/term` dependency justification

**Decision**: Add `golang.org/x/term` as a direct dependency in `go.mod` for the SDD-05 implement commit.

**Rationale**: Constitution XI requires every new direct dependency to satisfy: (a) maintainer activity, (b) supply-chain provenance, (c) transitive dependency footprint, (d) why no stdlib option suffices.
- *(a)* Maintained by the Go team; release cadence tied to Go toolchain releases.
- *(b)* Hosted under `golang.org/x/`, the canonical Go-team supplementary-package namespace. Module path is `golang.org/x/term`; checksums are published via Go's transparency log.
- *(c)* Single transitive: `golang.org/x/sys` (already a direct dep of this project per `go.mod`). No additional transitive surface.
- *(d)* The Go standard library does NOT provide a portable `IsTerminal` primitive in any package. Reimplementing the check would require platform-conditional CGO (or hand-written `syscall.Termios` calls) in this project — Constitution IX bans CGO for release binaries. `golang.org/x/term` is the sanctioned solution.

The trusted-sources hierarchy in `.github/tech-conventions/dependency-management.md` lists `golang.org/x/...` as effectively part of the stdlib-extension tier. The PR description for the SDD-05 implement commit will repeat the justification per Constitution XI.

**Alternatives considered**:
- *Hand-rolled `syscall` call*: rejected. CGO is forbidden; pure-Go `syscall.Termios` would still need per-OS conditionals (`securebytes_darwin.go` / `securebytes_linux.go` style), reproducing what `golang.org/x/term` already provides cleanly.
- *Vendor a minimal IsTerminal*: rejected. Constitution IX forbids vendoring (`/vendor` is forbidden).

---

## R-007 — Fuzz target stance for the redaction backstop

**Decision**: SDD-05 ships unit tests only. No mandatory fuzz target is added at this chunk.

**Rationale**: Constitution VIII enumerates exactly six mandatory fuzz targets for v0.1.0 — none of them is the logging redaction backstop. The redaction patterns are simple regular expressions backed by the stdlib `regexp` engine (which is itself extensively fuzzed upstream), and the backstop has no parser surface of its own. Spec SC-019 ("backstop is total") will be exercised by table-driven unit tests covering pathological inputs (very long strings, many matches, adjacent matches, UTF-8 boundaries). A future chunk MAY add a discretionary fuzz target if a defect class emerges; that is a follow-up, not a SDD-05 obligation.

**Alternatives considered**:
- *Add a `FuzzRedactString` target now*: deferred. It is welcome additional safety, but the chunk-doc Tests Required list does not name a fuzz target and the constitutional fuzz mandate does not cover this surface. We avoid scope creep at SDD-05.

---

## R-008 — Pattern set: the spec clarification overrides the chunk-prompt's expanded list

**Decision**: Ship four compiled patterns:

| Class | Regex source (anchored to byte sequence; not anchored to start/end of string) |
|-------|--------------------------------------------------------------------------------|
| Anthropic API key | `sk-ant-[A-Za-z0-9_\-]+` |
| OpenAI project key | `sk-proj-[A-Za-z0-9_\-]+` |
| GitHub PAT | `ghp_[A-Za-z0-9]+` |
| AWS access key | `AKIA[0-9A-Z]{16}` |

Concrete regex syntax is finalised in tasks/implement; the table above is the intent.

**Rationale**: Spec clarification Session 2026-04-27 chose Option A: ship the four classes named verbatim in `docs/SECURITY.md` §1.1 (the threat-model row that drives the project's existence). The plan-phase prompt earlier suggested a broader list including Google AI keys (`AIza...`) and a generic JWT shape. The spec is the authoritative WHAT contract; the spec narrowed the pattern set with the explicit reasoning that "any drift must land in `docs/SECURITY.md` first and only then expand the package's pattern set". The plan honours the spec.

This is a deliberate, in-scope divergence from the chunk-doc's earlier Prompt-4 (Tasks) list which named tests for Google AI and JWT. The Tasks phase that runs after this Plan must adjust those test names to match the four-pattern set; the Tasks-phase prompt should be amended accordingly. (Action: when the Tasks-phase session runs, the test list shifts from `TestRedactPattern_GoogleAIKey` / `TestRedactPattern_JWT` to `TestRedactPattern_OpenAIProjectKey`, retaining `TestRedactPattern_AnthropicKey` / `TestRedactPattern_GitHubPAT` / `TestRedactPattern_AWSAccessKey`.)

**Alternatives considered**:
- *Ship the broader 5-pattern list*: rejected by the spec clarification. The constitutional source-of-truth is `docs/SECURITY.md`; widening here without first widening that doc would invert the dependency.
- *Ship a richer set under a feature flag*: rejected. Out-of-scope per spec ("a pluggable pattern set at runtime" is explicitly out-of-scope).

---

## R-009 — Default output destination

**Decision**: When `opts.Out == nil`, the constructor uses `os.Stderr`.

**Rationale**: Spec FR-004 ("when the option is unset, the destination MUST default to the process's standard error stream") and Assumption #3 ("standard error is the conventional channel for operational logs"). Stderr is unbuffered, not interleaved with the program's stdout (preserving the rule that command output is parseable when piped), and inherited intact by launchd/systemd unit log collection.

**Alternatives considered**:
- *Default to `os.Stdout`*: rejected. Stdout is the channel for command output (e.g. `--format eval` injection text); polluting it with logs breaks pipe contracts.
- *Require the caller to pass a writer explicitly*: rejected. Operator ergonomics — most callers want stderr, and forcing a parameter trains them to pass `os.Stderr` literally everywhere with no behavioural gain.

---

## R-010 — Concurrency model

**Decision**: The package adds no synchronisation beyond `sync.Once` for pattern compilation. The wrapper `redactingHandler` is stateless after construction; all concurrency safety derives from the underlying stdlib `slog.JSONHandler` / `slog.TextHandler`, which are documented as safe for concurrent use.

**Rationale**: Spec SC-018 demands race-clean concurrent emission. The stdlib slog handlers serialise writes to the underlying writer using their internal mutex; this is sufficient. Adding a redundant mutex in `redactingHandler.Handle` would only add lock contention without correctness gain.

**Alternatives considered**:
- *Add a per-handler mutex*: rejected. Redundant; adds contention.
- *Use channel-serialised writes*: rejected. Out of scope; stdlib's behaviour is the right answer.

---

## R-011 — Test fixtures for representative credential samples

**Decision**: Tests use synthesised credential samples that match each pattern but are NOT real credentials — for example `sk-ant-fake0123456789abcdef`, `ghp_fakeABCDEF0123456789`, `AKIAFAKEFAKEFAKEFAKE` (16 uppercase-or-digit chars), `sk-proj-fake0123456789abcdef`. The test asserts (a) that the synthesised sample appears nowhere in captured output and (b) that the output contains at least one occurrence of `[redacted]`.

**Rationale**: A test must be deterministic and must not embed any genuine secret. The synthesised samples satisfy each pattern's regex without resembling any real key beyond the prefix. Tests must avoid embedding even plausible-looking keys to prevent gitleaks / CI scanners from flagging the test file itself as a leak — `fake` infixes plus the project's existing gitleaks allowlist patterns (if any) keep CI green.

**Alternatives considered**:
- *Use real credentials from a stale account*: rejected. Constitution + project ethos forbids it.
- *Use programmatically-generated samples per run*: rejected. Determinism > novelty for unit tests; failures must be reproducible.

---

## R-012 — `slog.Default` invariance test

**Decision**: `TestNew_DoesNotMutateSlogDefault` captures `slog.Default()`'s pointer (or, more precisely, calls `slog.Default()` and compares the returned `*slog.Logger`'s identity) before and after constructing several `New(opts)` instances with diverse options. Asserts the pointer is unchanged.

**Rationale**: Spec FR-002 + SC-016 demand observable proof that `slog.Default` is not mutated. The stdlib `slog.Default()` returns the package-level default logger; if `New` were to call `slog.SetDefault` the returned pointer would change. A simple before/after pointer-equality assertion is sufficient and avoids any reliance on internal slog state.

**Alternatives considered**:
- *Reflect into the stdlib slog package to verify state*: rejected. Brittle and unnecessary; the pointer-identity test is the simplest signal.
- *Run in a separate process*: rejected. Excessive — the assertion is a single comparison.

---

## R-013 — Where the chunk's edge cases fit in the test file layout

**Decision**: `logger_test.go` houses the constructor, format, level, source-location, slog.Default, and concurrency tests. `redact_test.go` houses pattern-positive (one per pattern), pattern-negative (no match), multi-match, adjacent-match, UTF-8 boundary, very-long-string, and the sentinel-leak smoke test (`TestLogger_RedactionSentinel` — even though it exercises the full logger, its purpose is the redaction guarantee, so it lives next to the redaction tests).

**Rationale**: Keeps each test file focused on a single responsibility surface. The sentinel-leak test is a load-bearing acceptance criterion (SC-010) and is the primary safety net the project relies on; placing it in `redact_test.go` keeps it adjacent to the rails it exercises. `redact_test.go` may import `logger.go` — internal package, no cycle.

**Alternatives considered**:
- *Single test file*: rejected. Two files reflect the two-source-file production split and reads more cleanly.
- *Sentinel-leak test in `logger_test.go`*: acceptable but slightly worse — it conceptually tests the rails, not the constructor.
