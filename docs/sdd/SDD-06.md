# SDD-06 — `internal/config` (server TOML schema + validation)

**Phase:** 1
**Package:** `internal/config`
**Files:** `server.go`, `defaults.go`, `validate.go`, `paths.go`, `*_test.go`, `server_fuzz_test.go`
**Branch:** `006-config-server` (created by the `before_specify` git hook)
**Blocked by:** SDD-05
**Blocks:** SDD-10, SDD-15
**Primary AC:** AC-1, AC-8
**Coverage target:** 95%; **fuzz target #5** (TOML parse)

**Behaviour contracts (MUST):**
- `github.com/pelletier/go-toml/v2` Decoder with `DisallowUnknownFields(true)`
- Validate `listen_addr` by parsing into `netip.Addr`; reject `IsLoopback`, `IsUnspecified`, public IPs; allow ONLY Tailscale CGNAT (`100.64.0.0/10`) per Constitution VI
- Refuse `argon_memory_mb < 256` (Constitution III non-negotiable)
- Build absolute paths from `state_dir`; reject `audit_log` paths outside `state_dir`
- All errors are typed sentinel errors

**Anti-contracts (MUST NOT):**
- Read from environment variables (Constitution Security Requirements)
- Store any secret in the `Server` struct (bot token fetched from Keychain in SDD-10)
- Allow `listen_addr 0.0.0.0` ever
- Use `init()` (Constitution IX)

**Tests required:**
- Unit (per-field positive + negative): `TestServer_RejectsLoopback`, `TestServer_RejectsPublic`, `TestServer_AcceptsTailscaleCGNAT`, `TestServer_RejectsArgonMemoryUnder256`, `TestServer_RejectsAuditLogOutsideStateDir`, `TestServer_RejectsUnknownField`, `TestServer_FullMinimalConfig`, `TestServer_FullMaximalConfig`
- Fuzz: `FuzzServerTOML` ≥60s clean — random byte stream into `LoadServer`; assert no panic, every error path produces a typed error

**Constitutional principles in scope:** III (Argon2id minimums), VI (Tailscale-only bind), VIII (TDD + fuzz target #5), IX (no `init`), X (no secrets in config struct)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `type Server struct { ... }`  (fields per `docs/CONFIG-SCHEMA.md`)
- `func LoadServer(ctx context.Context, path string) (*Server, error)`
- `func (s *Server) Validate() error`
- `var DefaultArgonTime, DefaultArgonMemoryMB, DefaultArgonThreads, ...`  (typed constants)
- `var ErrTailscaleBindRequired, ErrPathPrefixInvalid, ErrStateDirUnsafe, ErrSupervisorTTLOutOfRange, ...`  (sentinel errors)

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-06 (internal/config:
server TOML schema + validation) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles III, VI, VIII — security minimums + Tailscale-only bind)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (server config — entire section)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-8, FR-15, AC-8)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-1 + AC-8 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-06.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/config package owns the server-side TOML configuration
file: schema, defaults, validation, and path-safety checks. It is
loaded once at server startup (SDD-10) and at hush init (SDD-15);
typed sentinel errors guide operators to a working config without
ever crashing on bad input.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The server config has a strict, documented schema. Unknown
  fields are rejected at load time (typo defence).
- The listen address MUST resolve to a Tailscale CGNAT address
  (100.64.0.0/10). Loopback, unspecified (0.0.0.0 / [::]), and
  public IPs are explicit rejections with distinct, named errors.
- Argon2id memory MUST be at least 256 MiB; smaller values are
  rejected with a distinct, named error (Constitution III).
- The audit log path MUST resolve underneath the state directory.
  Out-of-tree audit log paths are rejected with a distinct,
  named error (path traversal defence).
- The config struct MUST NOT contain any secret value. The
  Discord bot token is fetched at server startup via the
  Keychain wrapper, not read from this struct.
- The loader MUST NOT consult environment variables for any
  secret-bearing field.

The spec MUST NOT encode HOW (no library names beyond stdlib
references, no specific TOML decoder choice). Those are plan-phase.

Acceptance criteria: AC-1 (server CLI hardness — config gates), AC-8
(startup hardening).

Action — run exactly one command:
  /speckit-specify "internal/config: load + validate the hush server TOML config; strict schema (unknown fields rejected); Tailscale-only bind enforcement; Argon2id minimum-memory enforcement; audit-log-inside-state-dir enforcement; never reads secrets from env or stores them in the config struct"

The before_specify hook will create branch 006-config-server.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-06 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-06.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-06 (internal/config: server
TOML schema + validation) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; III/VI/VIII/IX/X are load-bearing)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md  (entire server section — every field documented here MUST be in the struct, with the documented default)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-8, FR-15, AC-8)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/config — the API contract you will lock)
- /Users/mrz/projects/hush/docs/sdd/SDD-06.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/config
- Files: server.go (Server struct + Format helpers), defaults.go
  (typed constants for every documented default), validate.go
  (rule engine producing typed errors), paths.go (filesystem
  path-safety checks), server_test.go, validate_test.go,
  server_fuzz_test.go
- Exported API:
    type Server struct { ... fields per docs/CONFIG-SCHEMA.md ... }
    func LoadServer(ctx context.Context, path string) (*Server, error)
    func (s *Server) Validate() error
    var DefaultArgonTime, DefaultArgonMemoryMB, DefaultArgonThreads,
        DefaultListenAddr, DefaultStateDir, DefaultAuditLog,
        DefaultSupervisorTTLMin, DefaultSupervisorTTLMax, ... (typed)
    var ErrTailscaleBindRequired, ErrPathPrefixInvalid,
        ErrStateDirUnsafe, ErrSupervisorTTLOutOfRange,
        ErrArgonMemoryTooLow, ErrUnknownField, ErrAuditLogEscape, ...

Implementation contract (HOW — locked):
- TOML decoder: github.com/pelletier/go-toml/v2 with
  Decoder.DisallowUnknownFields(true). Constitution XI: this dep
  is already locked.
- listen_addr validation uses net/netip; check IsLoopback,
  IsUnspecified, and CGNAT membership (100.64.0.0/10).
- audit_log path validation: resolve to absolute via
  filepath.Abs, then check filepath.HasPrefix(audit_log, state_dir).
- Defaults applied AFTER decode — every absent field gets the
  documented default constant.
- No init(); load order is deterministic via explicit calls.
- Fuzz target FuzzServerTOML feeds random bytes to LoadServer;
  assert no panic, every returned error is one of the named
  sentinels (or wraps one).

Coverage target: 95%. Fuzz target: FuzzServerTOML (60s gate).
Constitutional principles in scope: III, VI, VIII, IX, X, XI.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-06 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-06.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 95%. Tests required: TestServer_RejectsLoopback, TestServer_RejectsPublic, TestServer_AcceptsTailscaleCGNAT, TestServer_RejectsArgonMemoryUnder256, TestServer_RejectsAuditLogOutsideStateDir, TestServer_RejectsUnknownField, TestServer_FullMinimalConfig, TestServer_FullMaximalConfig. Every default in docs/CONFIG-SCHEMA.md MUST have an asserting test. Fuzz: FuzzServerTOML — random byte stream into LoadServer, no panic, every error path typed. Final phase MUST include magex format:fix, magex lint, magex test:race, and go test -fuzz=FuzzServerTOML -fuzztime=60s ./internal/config/"

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-06 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-06.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Fuzz (60s minimum, no crashes / no new bug corpus):
     go test -fuzz=FuzzServerTOML -fuzztime=60s ./internal/config/
3. Verify coverage ≥ 95% on internal/config:
     go test -cover ./internal/config/
4. Append "Exported API — locked at SDD-06" section to
   docs/PACKAGE-MAP.md under internal/config listing the locked
   API from the chunk doc (struct, functions, default constants,
   sentinel errors).
5. Update docs/AC-MATRIX.md AC-1 and AC-8 rows with the new
   test file paths.
6. Mark SDD-06 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/config/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(config): server TOML schema + validation + fuzz (SDD-06)"

Final message: confirm gates passed, fuzz 60s clean, coverage ≥
95%, every default in docs/CONFIG-SCHEMA.md is asserted, AC-MATRIX
rows for AC-1/AC-8 updated, SDD-PLAYBOOK updated, and the combined
commit created.
```
