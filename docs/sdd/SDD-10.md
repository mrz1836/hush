# SDD-10 — `internal/server` (router + middleware + startup checks + SIGHUP atomic reload + lifecycle)

**Phase:** 3
**Package:** `internal/server`
**Files:** `server.go`, `router.go`, `middleware.go`, `startup_checks.go`, `reload.go`, `*_test.go`, `integration_test.go`
**Branch:** `010-server-skeleton` (created by the `before_specify` git hook)
**Blocked by:** SDD-03, SDD-05, SDD-06, SDD-07, SDD-08, SDD-09
**Blocks:** SDD-12, SDD-13, SDD-14
**Primary AC:** AC-1, AC-2 (SIGHUP reload half), AC-8
**Coverage target:** 95%

**Behaviour contracts (MUST):**
- Use `net/http` (Constitution XI: stdlib first); router is stdlib `http.ServeMux` at `/h/<prefix>/...` for v0.1.0
- `atomic.Pointer[vault.Store]` for SIGHUP-safe vault swap — old store's `Destroy` is called 30s after swap to allow in-flight requests to drain
- Startup check execution order: `clock_sync → file_modes → tailscale_bind → state_dir`; refuse to start on first failure with explicit error
- `Approver` interface placeholder — SDD-11 swaps in real Discord-backed `Approver`
- Recover middleware logs panic with stack but never includes request body

**Anti-contracts (MUST NOT):**
- Bind to `0.0.0.0` ever
- Allow `init()` functions
- Hold a `Context` in a struct field (Constitution IX)

**Tests required:**
- Unit: `TestStartupChecks_RefusesPublicBind`, `TestStartupChecks_RefusesLooseFileMode`, `TestStartupChecks_RefusesUnsyncedClock`, `TestStartupChecks_OrderedExecution`, `TestMiddleware_RequestIDStable`, `TestMiddleware_IPAllowListBlocks`, `TestMiddleware_RecoverNoBodyInLog`
- Integration (`//go:build integration`): `TestSIGHUP_AtomicReload` — start server with vault A → SIGHUP with vault B → in-flight request sees A → new request sees B → vault A zeroed after drain
- Race: `TestVaultPointerSwap_NoRace`

**Constitutional principles in scope:** III, VI (Tailscale-only bind), VIII, IX (no `init`, no ctx in struct), X (no bodies in panic logs), XI (stdlib first)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `type Server struct { ... }`
- `type Deps struct { Cfg *config.Server; VaultPtr *atomic.Pointer[vault.Store]; TokenStore token.Store; Approver discord.Approver; Logger *slog.Logger; ... }`
- `func New(deps Deps) (*Server, error)`
- `func (s *Server) Run(ctx context.Context) error`
- `func (s *Server) ReloadVault(ctx context.Context, newPath string, key *securebytes.SecureBytes) error`  (SIGHUP entry)

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-10 (internal/server: router
+ middleware + startup checks + SIGHUP atomic reload + lifecycle) of
the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles III, VI, VIII, IX, X; Security Requirements)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-8, FR-10, FR-15, FR-16, AC-1, AC-2, AC-8)
- /Users/mrz/projects/hush/docs/API.md  (full)
- /Users/mrz/projects/hush/docs/ARCHITECTURE.md  (server lifecycle)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-1/2/8 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-10.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/server package is the server skeleton: HTTP router,
middleware stack (request ID, IP allow-list, panic recover), the
ordered startup checks that refuse to launch on a misconfigured
host, and the SIGHUP-driven atomic vault reload that swaps the
in-memory store without dropping in-flight requests. It is the
chassis on which SDD-12 (claim) and SDD-13 (secret/revoke/health)
mount their handlers, and the runtime SDD-14 (cmd/hush serve)
launches.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The server MUST refuse to start if any startup check fails:
  clock unsynced, vault file mode laxer than 0600 or parent
  laxer than 0700, listen address not Tailscale CGNAT, state
  directory unsafe. Each failure produces a distinct, named
  error and a non-zero exit.
- Startup checks MUST execute in a deterministic order:
  clock → file modes → bind → state dir. The first failure
  short-circuits.
- SIGHUP triggers a vault reload: the new vault is loaded at
  the configured path; on success, the in-memory store pointer
  is atomically swapped; the old store is Destroyed after a
  drain window so in-flight requests still see the old data.
- The recover middleware MUST log panic + stack but MUST NEVER
  include the request body in the log entry.
- The Approver dependency is an interface placeholder this chunk
  defines; SDD-11 wires in the real Discord-backed implementation.

The spec MUST NOT encode HOW (no library names beyond stdlib
references, no specific net/http vs other-router choice). Those
are plan-phase.

Acceptance criteria: AC-1, AC-2 (SIGHUP reload half), AC-8 (startup
hardening).

Action — run exactly one command:
  /speckit-specify "internal/server: HTTP server skeleton with stdlib router, middleware stack (request ID, IP allow-list, panic recover that never logs request bodies), ordered startup checks (clock → file modes → Tailscale bind → state dir, refuse to start on first failure), and SIGHUP-driven atomic vault reload with drain window"

The before_specify hook will create branch 010-server-skeleton.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-10 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-10.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-10 (internal/server) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; III/VI/VIII/IX/X/XI all load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-8, FR-10, FR-15, FR-16, AC-1, AC-2, AC-8)
- /Users/mrz/projects/hush/docs/API.md  (full route table — your router must reflect it)
- /Users/mrz/projects/hush/docs/ARCHITECTURE.md  (server lifecycle, SIGHUP reload diagram)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/server — the API contract you will lock)
- /Users/mrz/projects/hush/docs/sdd/SDD-10.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/server
- Files: server.go (Server struct + Run + lifecycle), router.go
  (stdlib http.ServeMux registration), middleware.go (request
  ID, IP allow-list, panic recover), startup_checks.go (ordered
  checks), reload.go (SIGHUP handler + atomic swap), server_test.go,
  middleware_test.go, startup_checks_test.go, reload_test.go,
  integration_test.go (//go:build integration)
- Exported API:
    type Server struct { ... }
    type Deps struct { Cfg *config.Server; VaultPtr *atomic.Pointer[vault.Store]; TokenStore token.Store; Approver discord.Approver; Logger *slog.Logger; AuditWriter audit.Writer; Clock func() time.Time; ... }
    func New(deps Deps) (*Server, error)
    func (s *Server) Run(ctx context.Context) error
    func (s *Server) ReloadVault(ctx context.Context, newPath string, key *securebytes.SecureBytes) error

Implementation contract (HOW — locked):
- net/http only; router is stdlib http.ServeMux mounted at
  /h/<prefix>/... (prefix from cfg). Constitution XI: no chi,
  no gorilla/mux, no echo.
- vault.Store accessed via *atomic.Pointer[vault.Store] held in
  Deps. ReloadVault: load new store → atomic.Pointer.Store →
  spawn drain goroutine that sleeps cfg.ReloadDrainWindow
  (default 30s) then Destroy()s the old store.
- Startup checks: each is a function (ctx) error; checks are
  invoked in the documented order from a slice; first error
  returned. Each check has a typed sentinel error.
- Approver interface declared here:
    type Approver interface { RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error) }
  The full ApprovalRequest / Decision types live here too;
  SDD-11 implements with BotApprover.
- Middleware order: request ID → IP allow-list → panic recover
  → handler. Recover logs panic + debug.Stack() at ERROR with
  request_id; the request body is NEVER part of the log entry.
- ctx context.Context flows through Run → handler context;
  Run blocks until ctx cancels then triggers http.Server.Shutdown.

Coverage target: 95%.
Constitutional principles in scope: III, VI, VIII, IX, X, XI.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-10 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-10.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 95%. Tests required: TestStartupChecks_RefusesPublicBind, TestStartupChecks_RefusesLooseFileMode, TestStartupChecks_RefusesUnsyncedClock, TestStartupChecks_OrderedExecution (clock first, state-dir last), TestMiddleware_RequestIDStable, TestMiddleware_IPAllowListBlocks, TestMiddleware_RecoverLogsStackNoBody, TestVaultPointerSwap_NoRace, and integration test TestSIGHUP_AtomicReload (//go:build integration) — start with vault A, SIGHUP with vault B, in-flight req sees A, new req sees B, vault A zeroed after drain. Final phase MUST include magex format:fix, magex lint, magex test:race, and magex test:race -tags=integration."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-10 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-10.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Integration tests:
     magex test:race -tags=integration
3. Verify coverage ≥ 95% on internal/server:
     go test -cover ./internal/server/
4. Confirm TestSIGHUP_AtomicReload demonstrates: in-flight request
   sees old vault, new request sees new vault, old vault zeroed
   after drain window.
5. Append "Exported API — locked at SDD-10" section to
   docs/PACKAGE-MAP.md under internal/server listing the locked
   API from the chunk doc.
6. Update docs/AC-MATRIX.md AC-1, AC-2, AC-8 rows with the new
   test file paths.
7. Mark SDD-10 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/server/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(server): router + middleware + startup checks + SIGHUP reload (SDD-10)"

Final message: confirm gates passed (unit + integration), race-clean,
coverage ≥ 95%, every startup check has positive + negative tests,
SIGHUP reload integration test green, AC-1/2/8 rows updated,
SDD-PLAYBOOK updated, and the combined commit created.
```
