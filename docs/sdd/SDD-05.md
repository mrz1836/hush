# SDD-05 — `internal/logging` (slog setup + redaction enforcement)

**Phase:** 1
**Package:** `internal/logging`
**Files:** `logger.go`, `redact.go`, `redact_patterns.go`, `*_test.go`
**Branch:** `005-logging` (created by the `before_specify` git hook)
**Blocked by:** SDD-02
**Blocks:** every chunk thereafter
**Primary AC:** indirect (Constitution Principle X)
**Coverage target:** 95%

**Behaviour contracts (MUST):**
- Use stdlib `log/slog` only (no logrus / zap / others — Constitution XI)
- TTY detection via `golang.org/x/term` (allowed dep) → text format on TTY, JSON otherwise
- `ReplaceAttr` handler chain: (1) call `LogValuer` on values (so SecureBytes redacts itself), (2) `RedactString` string values as a regex backstop
- Default level `INFO`
- JSON format adds source location for `ERROR`; text format never does
- `RedactPatterns` covers every regex in the `docs/SECURITY.md` threat-model row (Anthropic key, GitHub PAT, AWS access key, etc.)

**Anti-contracts (MUST NOT):**
- Mutate global `slog.Default`
- Print to stderr unless `opts.Out` specifies it
- Use `init()` (Constitution IX)

**Tests required:**
- Unit: `TestNew_TTYDetectionPicksText`, `TestNew_NonTTYPicksJSON`, `TestRedactPattern_AnthropicKey`, `TestRedactPattern_GitHubPAT`, `TestRedactPattern_AWSAccessKey` (one test per documented pattern)
- Sentinel-leak: `TestLogger_RedactionSentinel` — log a `SecureBytes` wrapping `SECRET_SHOULD_NEVER_APPEAR_5` via the configured logger; capture output; assert sentinel absent

**Constitutional principles in scope:** IX (idiomatic Go, no `init`), X (redaction enforcement), XI (no new logging deps)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `type Options struct { Level slog.Level; Format Format; Out io.Writer }`
- `type Format int`  (`FormatAuto`, `FormatText`, `FormatJSON`)
- `func New(opts Options) *slog.Logger`
- `func RedactString(s string) string`  — pattern-based backstop
- `var RedactPatterns []*regexp.Regexp`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-05 (internal/logging:
slog setup + redaction enforcement) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principle X — secrets MUST NEVER reach logs)
- /Users/mrz/projects/hush/docs/OPERATIONS.md  (logging tier definitions, format expectations)
- /Users/mrz/projects/hush/docs/SECURITY.md  (the threat-model row listing pattern-based redaction targets)
- /Users/mrz/projects/hush/docs/sdd/SDD-05.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/logging package is the project-wide structured logger.
It produces text on a TTY and JSON otherwise, enforces type-driven
redaction (LogValuer on every value) plus a regex backstop for raw
strings, and is consumed by every other internal package.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The logger MUST never emit a value that implements LogValuer
  without first calling LogValuer on it (so SecureBytes redacts
  itself everywhere).
- The logger MUST scan plain string values for known credential
  patterns (Anthropic API key, GitHub PAT, AWS access key, etc.,
  per docs/SECURITY.md) and replace matches with "[redacted]".
- TTY-attached invocations get human-readable text output;
  pipe / file / non-TTY invocations get JSON.
- The default level is INFO; the operator can configure it.
- ERROR-level JSON entries include source location; text-level
  entries never do (noise on a TTY).
- The package MUST NOT mutate slog.Default (callers receive a
  configured *slog.Logger and pass it explicitly).

The spec MUST NOT encode HOW (no library names beyond stdlib
references, no specific regex syntax). Those are plan-phase.

Acceptance criterion: indirect — supports Constitution Principle X
(no secrets in logs) for every chunk that follows.

Action — run exactly one command:
  /speckit-specify "internal/logging: project-wide stdlib slog logger; auto-detect TTY (text vs JSON); enforce type-driven redaction via LogValuer plus a regex backstop for known credential patterns; configurable level; never mutate slog.Default"

The before_specify hook will create branch 005-logging.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract. Otherwise leave the marker —
/speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-05 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-05.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-05 (internal/logging) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; X and XI are load-bearing)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/logging — the API contract you will lock)
- /Users/mrz/projects/hush/docs/OPERATIONS.md  (logging tier definitions, format expectations)
- /Users/mrz/projects/hush/docs/SECURITY.md  (the redaction-pattern list — every entry must be encoded as a RedactPatterns regex)
- /Users/mrz/projects/hush/docs/sdd/SDD-05.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/logging
- Files: logger.go (New + Format type + Options), redact.go
  (RedactString implementation), redact_patterns.go (compiled
  RedactPatterns slice), logger_test.go, redact_test.go
- Exported API:
    type Options struct { Level slog.Level; Format Format; Out io.Writer }
    type Format int        // FormatAuto, FormatText, FormatJSON
    func New(opts Options) *slog.Logger
    func RedactString(s string) string
    var RedactPatterns []*regexp.Regexp

Implementation contract (HOW — locked):
- Stdlib log/slog only. Constitution XI: no logrus, no zap.
- Allowed deps: log/slog, io, os, regexp, golang.org/x/term.
- TTY detection: golang.org/x/term.IsTerminal(int(os.Stdout.Fd()))
  when opts.Format == FormatAuto.
- ReplaceAttr handler: receives every Attr; if Value is a slog.Value
  whose underlying type implements LogValuer, call LogValuer first;
  then if the resulting Value.Kind() == KindString, run
  RedactString over the string.
- RedactPatterns is a package-level slice of compiled regexp.Regexp,
  built ONCE in package init... wait — no init() (Constitution IX).
  Use sync.Once gated by a package-level helper called from
  RedactString.
- RedactString returns the input untouched if no pattern matches;
  otherwise returns "[redacted]" (replace the entire matched
  substring, not just the credential portion).
- Pattern list (one regex each, derived from docs/SECURITY.md):
  Anthropic API key (sk-ant-*), GitHub PAT (ghp_*), AWS access
  key (AKIA[0-9A-Z]{16}), Google AI key (AIza*), generic JWT
  (header.payload.signature shape).

Coverage target: 95%.
Constitutional principles in scope: IX, X, XI.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-05 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-05.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 95%. Tests required: TestNew_TTYDetectionPicksText, TestNew_NonTTYPicksJSON, TestNew_DefaultLevelInfo, TestNew_DoesNotMutateSlogDefault, TestRedactPattern_AnthropicKey, TestRedactPattern_GitHubPAT, TestRedactPattern_AWSAccessKey, TestRedactPattern_GoogleAIKey, TestRedactPattern_JWT, and the sentinel-leak test TestLogger_RedactionSentinel wrapping SECRET_SHOULD_NEVER_APPEAR_5. Final phase MUST include magex format:fix, magex lint, magex test:race."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-05 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-05.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 95% on internal/logging:
     go test -cover ./internal/logging/
3. Confirm TestLogger_RedactionSentinel passed and
   SECRET_SHOULD_NEVER_APPEAR_5 is absent from any captured log.
4. Append "Exported API — locked at SDD-05" section to
   docs/PACKAGE-MAP.md under internal/logging listing the five
   exported symbols / types from the chunk doc.
5. Mark SDD-05 status `done` in docs/SDD-PLAYBOOK.md.
6. (No AC-MATRIX update — this chunk supports Principle X
   indirectly, not a numbered AC.)

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/logging/ docs/PACKAGE-MAP.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(logging): slog setup + LogValuer + regex redaction backstop (SDD-05)"

Final message: confirm gates passed, race-clean, coverage ≥ 95%,
TTY detection works, every redaction pattern from docs/SECURITY.md
has a passing test, sentinel absent in TestLogger_RedactionSentinel,
PACKAGE-MAP + SDD-PLAYBOOK updated, and the combined commit created.
```
