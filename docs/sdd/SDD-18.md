# SDD-18 — `internal/supervise/config` (per-supervisor TOML schema + validation)

**Phase:** 5
**Package:** `internal/supervise/config`
**Files:** `config.go`, `defaults.go`, `validate.go`, `*_test.go`, `config_fuzz_test.go`
**Branch:** `018-supervise-config` (created by the `before_specify` git hook)
**Blocked by:** SDD-06
**Blocks:** SDD-19, SDD-21, SDD-23, SDD-29, SDD-30
**Primary AC:** AC-10
**Coverage target:** 95%; **fuzz target #5** (TOML parse — distinct from SDD-06's server-config target)

**Behaviour contracts (MUST):**
- `go-toml/v2` decoder with `DisallowUnknownFields(true)`
- All fields per `docs/CONFIG-SCHEMA.md` (root + `[child]` + `[discord]` + `[validators]` + `[watchdog]`)
- Validator names limited to `{anthropic, anthropic-oauth, openai, google-ai, github}` — unknown names rejected
- `grace.window <= 4h`
- `refresh_window` format `"HH:MM-HH:MM"` with start `<` end
- `command` first element absolute path

**Anti-contracts (MUST NOT):**
- Allow unknown validator names (silent ignore is wrong — explicit error)
- Skip `refresh_window` validation
- Use `init()`

**Tests required:**
- Unit (full positive + negative per documented field): `TestSuperviseConfig_FullMinimal`, `TestSuperviseConfig_FullMaximal`, `TestSuperviseConfig_RejectsUnknownField`, `TestSuperviseConfig_RejectsUnknownValidator`, `TestSuperviseConfig_GraceWindowOver4h_Rejected`, `TestSuperviseConfig_RefreshWindowFormat`, `TestSuperviseConfig_RefreshWindowStartGEEnd_Rejected`, `TestSuperviseConfig_CommandFirstElementMustBeAbsolute`
- Fuzz: `FuzzSuperviseTOML` ≥60s clean — random bytes; assert no panic, every error path produces a typed error

**Constitutional principles in scope:** IV (TTL discipline + grace window cap), V (operator visibility — every validator must be explicitly named), VIII (95% coverage + fuzz target #5), IX (no `init`)

**Exported API to lock in PACKAGE-MAP.md (this chunk — new entry):**
- `type Supervisor struct { ... }`  (fields per `docs/CONFIG-SCHEMA.md` Supervisor Config File)
- `func Load(ctx context.Context, path string) (*Supervisor, error)`
- `func (s *Supervisor) Validate() error`
- `var DefaultGraceWindow, DefaultRefreshWindow, DefaultBootRetryTimeout, DefaultDMRateLimit, ...`
- `var ErrUnknownField, ErrUnknownValidator, ErrGraceWindowTooLong, ErrRefreshWindowFormat, ErrRefreshWindowOrder, ErrCommandPathRelative, ...`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-18 (internal/supervise/config:
per-supervisor TOML schema + validation) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles IV, V, VIII — TTL discipline, operator visibility, TDD)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (Supervisor Config File — entire section, every field is load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11, AC-10)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-18.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/supervise/config package owns the per-supervisor TOML
configuration: the daemon's identity, child command, validators,
grace window, refresh window, watchdog patterns, and Discord
routing. It is loaded once per supervisor process at startup, and
every downstream supervisor chunk (SDD-19..23, 26..28) depends on
its types and validation rules.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The supervisor config has a strict, documented schema (root +
  [child] + [discord] + [validators] + [watchdog] sections).
  Unknown fields and unknown validator names are rejected at
  load time with distinct, named errors.
- The set of allowed validator names is fixed: anthropic,
  anthropic-oauth, openai, google-ai, github. Anything else is
  a load-time error.
- grace.window MUST NOT exceed 4 hours (Constitution IV TTL
  discipline + Layer-6 audit window).
- refresh_window has the format "HH:MM-HH:MM" with start strictly
  earlier than end; both validation failures produce distinct,
  named errors.
- The child.command's first element MUST be an absolute path
  (no shell parsing, no PATH lookup).
- Every documented default in docs/CONFIG-SCHEMA.md MUST be
  asserted by a corresponding test.

The spec MUST NOT encode HOW (no library names, no specific TOML
decoder choice). Those are plan-phase.

Acceptance criterion: AC-10 (supervisor lifecycle).

Action — run exactly one command:
  /speckit-specify "internal/supervise/config: load + validate per-supervisor TOML; strict schema with unknown-field rejection; fixed validator allow-list (anthropic, anthropic-oauth, openai, google-ai, github); grace window cap (≤4h); refresh window format and ordering enforcement; child command first element must be absolute"

The before_specify hook will create branch 018-supervise-config.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-18 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-18.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-18 (internal/supervise/config)
of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; IV/V/VIII/IX load-bearing)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (Supervisor Config File — every field documented here MUST be in the struct with the documented default)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11, AC-10)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (no internal/supervise entry yet — you create it)
- /Users/mrz/projects/hush/docs/sdd/SDD-18.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/supervise/config (NEW)
- Files: config.go (Supervisor struct + sub-structs Child,
  Discord, Validators, Watchdog), defaults.go (typed constants),
  validate.go (rule engine), config_test.go, validate_test.go,
  config_fuzz_test.go
- Exported API:
    type Supervisor struct {
        Name string; Command Child; Discord Discord;
        Validators map[string]Validator; Watchdog Watchdog;
        Grace Grace; RefreshWindow string;
        BootRetryTimeout time.Duration; DMRateLimit time.Duration;
        ... (per docs/CONFIG-SCHEMA.md)
    }
    func Load(ctx context.Context, path string) (*Supervisor, error)
    func (s *Supervisor) Validate() error
    var DefaultGraceWindow time.Duration         // 4h cap default
    var DefaultRefreshWindow string              // documented value
    var DefaultBootRetryTimeout time.Duration
    var DefaultDMRateLimit time.Duration
    var ErrUnknownField, ErrUnknownValidator,
        ErrGraceWindowTooLong, ErrRefreshWindowFormat,
        ErrRefreshWindowOrder, ErrCommandPathRelative,
        ErrCommandEmpty, ...

Implementation contract (HOW — locked):
- TOML decoder: github.com/pelletier/go-toml/v2 with
  DisallowUnknownFields(true) (already locked by SDD-06).
- Validator allow-list: package-level map[string]struct{} of
  the five names. Unknown name during decode → ErrUnknownValidator
  with the offending name in the error (NOT the validator
  config value, which may contain credentials — only the name).
- grace.window enforcement: parse via time.ParseDuration; reject
  > 4*time.Hour with ErrGraceWindowTooLong.
- refresh_window: split on "-"; parse each as "15:04" via
  time.Parse; reject if start >= end.
- child.command: must be []string with at least one element;
  first element must be filepath.IsAbs.
- Defaults applied AFTER decode for every absent field.
- No init(); Load is the only entry point.
- Fuzz target FuzzSuperviseTOML — random bytes into Load;
  every error must be one of the named sentinels.

Coverage target: 95%. Fuzz target: FuzzSuperviseTOML (60s gate).
Constitutional principles in scope: IV, V, VIII, IX, X, XI.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-18 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-18.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 95%. Tests required: TestSuperviseConfig_FullMinimal, TestSuperviseConfig_FullMaximal, TestSuperviseConfig_RejectsUnknownField, TestSuperviseConfig_RejectsUnknownValidator, TestSuperviseConfig_GraceWindowOver4h_Rejected, TestSuperviseConfig_RefreshWindowFormat (parses HH:MM-HH:MM), TestSuperviseConfig_RefreshWindowStartGEEnd_Rejected, TestSuperviseConfig_CommandFirstElementMustBeAbsolute, TestSuperviseConfig_CommandEmpty_Rejected. Every default in docs/CONFIG-SCHEMA.md Supervisor section MUST have an asserting test. Fuzz: FuzzSuperviseTOML — random bytes, no panic, every error path typed. Final phase MUST include magex format:fix, magex lint, magex test:race, and go test -fuzz=FuzzSuperviseTOML -fuzztime=60s ./internal/supervise/config/"

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-18 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-18.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Fuzz (60s minimum, no crashes / no new bug corpus):
     go test -fuzz=FuzzSuperviseTOML -fuzztime=60s ./internal/supervise/config/
3. Verify coverage ≥ 95% on internal/supervise/config:
     go test -cover ./internal/supervise/config/
4. Confirm every default documented in docs/CONFIG-SCHEMA.md
   Supervisor section is asserted by a test.
5. Append "Exported API — locked at SDD-18" section to
   docs/PACKAGE-MAP.md as a NEW entry under internal/supervise/
   listing the locked API from the chunk doc (Supervisor struct,
   Load, Validate, Default* constants, Err* sentinels).
6. Update docs/AC-MATRIX.md AC-10 row with the new test file paths.
7. Mark SDD-18 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add internal/supervise/config/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(supervise/config): supervisor TOML schema + validation + fuzz (SDD-18)"

Final message: confirm gates passed, fuzz 60s clean, coverage ≥
95%, every default asserted, AC-10 row updated, SDD-PLAYBOOK
updated, and the combined commit created.
```
