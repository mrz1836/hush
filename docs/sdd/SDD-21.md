# SDD-21 — `internal/supervise` refill + refresh + grace cache

**Phase:** 5
**Package:** `internal/supervise`
**Files:** `refill.go`, `refresh.go`, `grace.go`, `*_test.go`
**Branch:** `021-supervise-refill-refresh` (created by the `before_specify` git hook)
**Blocked by:** SDD-09, SDD-13, SDD-19
**Blocks:** SDD-23, SDD-25
**Primary AC:** AC-10
**Coverage target:** 95%

**Behaviour contracts (MUST):**
- **Refill**: GET `/s/<name>` for each scope using cached JWT; ECIES-decrypt; if any returns 401-unknown-jti → state→`awaiting-approval`; else hand to child
- **Refresh**: cron-like scheduler within configured window; T-30 fallback if window passed and TTL near expiry
- **Grace**: holds last-decrypted set in `*securebytes.SecureBytes` per secret name; expires after `grace.window` with `Destroy`
- **Boot retry**: try connect to server; on failure exp-backoff; cap total at `boot_retry_timeout`; never burn Discord prompts during boot
- **DM rate limit**: per-supervisor token bucket, default 1/5min

**Anti-contracts (MUST NOT):**
- Convert cached secrets to `string` at any point
- Use grace cache when `grace.cache_secrets_for_restart=false`
- Schedule refresh outside the configured window without T-30 fallback active

**Tests required:**
- Unit: `TestRefill_SilentOnCleanExit`, `TestRefill_401UnknownJTITransitions`, `TestRefresh_FiresInWindow`, `TestRefresh_T30MinFallback`, `TestGrace_UsesCacheOnExpiredJWT`, `TestGrace_TTLCapAt4h`, `TestGrace_DisabledWhenConfigFalse`, `TestBootRetry_BackoffRespected`, `TestBootRetry_NeverPromptsDiscord`, `TestDMRateLimit_DropsExcess`
- Race: refresh scheduler clean

**Constitutional principles in scope:** IV (TTL-bound + grace cap), V (operator-visible alerts via DM rate limit), VIII (95% coverage + TDD), X (no string materialisation of cached secrets)

**Exported API to lock in PACKAGE-MAP.md (this chunk — extends internal/supervise entry):**
- `type Refiller struct { ... }`
- `func NewRefiller(client *http.Client, store *Store, logger *slog.Logger) *Refiller`
- `func (r *Refiller) Refill(ctx context.Context, scopes []string) error`
- `type Refresher struct { ... }`
- `func NewRefresher(window string, ttl time.Duration, refill func(context.Context) error, logger *slog.Logger) *Refresher`
- `func (r *Refresher) Run(ctx context.Context) error`
- `type Grace struct { ... }`
- `func NewGrace(window time.Duration, enabled bool) *Grace`
- `func (g *Grace) Get(name string) (*securebytes.SecureBytes, bool)`
- `func (g *Grace) Set(name string, value *securebytes.SecureBytes)`
- `var ErrJTIUnknown, ErrBootTimeout`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-21 (internal/supervise:
refill + refresh + grace cache) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles IV, V, VIII — TTL discipline, operator visibility)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11, AC-10)
- /Users/mrz/projects/hush/docs/SECURITY.md  (§6 grace-window tradeoff — read this carefully)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenarios 3 stale credential, 8 refresh window, 9 boot retry, 11 grace cache)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (refresh window tuning, grace tradeoff)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-10 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-21.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
This chunk adds the supervisor's three credential-lifecycle
helpers: Refill (fetch the current set of secrets from the server
using the cached JWT), Refresh (schedule the next claim within the
configured window), and Grace (hold last-decrypted values in
mlocked memory so a clean restart doesn't always burn a Discord
prompt). Plus the boot-retry protocol that prevents Discord-prompt
flooding while the server is unreachable, and the per-supervisor
DM rate limiter.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- Refill calls GET /s/<name> for each requested scope. If the
  cached JWT returns 401-unknown-jti for any name, the state
  machine transitions to awaiting-approval (operator must
  re-approve via Discord).
- Refresh fires within the operator-configured window (e.g.
  03:00-05:00) by default. If the window has already passed
  for today AND the TTL is near expiry, a T-30-minute fallback
  fires (so the daemon never runs out before the next window).
- Grace cache holds the last-decrypted set in mlocked
  SecureBytes per secret name; entries expire after
  grace.window (capped at 4 hours per Constitution IV).
- Grace cache is disabled entirely when
  grace.cache_secrets_for_restart=false.
- Boot retry: try connect to server with exponential backoff;
  total cap = boot_retry_timeout. NEVER prompt Discord during
  boot retry — the server may simply be down.
- DM rate limiter: per-supervisor token bucket (default 1
  prompt per 5 minutes); excess attempts drop with a WARN log.

The spec MUST NOT encode HOW (no library names, no specific
scheduler implementation). Those are plan-phase.

Acceptance criterion: AC-10 (supervisor lifecycle).

Action — run exactly one command:
  /speckit-specify "internal/supervise: Refill (GET /s/<name> per scope, ECIES decrypt, 401-unknown-jti → awaiting-approval); Refresh (cron-like in operator window, T-30 fallback); Grace (mlocked SecureBytes per name, ≤4h cap, disabled when config false); boot retry (exp backoff, never prompts Discord); per-supervisor DM rate limiter (default 1/5min)"

The before_specify hook will create branch 021-supervise-refill-refresh.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-21 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-21.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-21 (internal/supervise refill
+ refresh + grace) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; IV/V/VIII/X load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-11)
- /Users/mrz/projects/hush/docs/SECURITY.md  (§6 grace-window tradeoff — operator visibility vs availability)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenarios 3, 8, 9, 11 — every scenario must have a passing test in this chunk)
- /Users/mrz/projects/hush/docs/DAEMONS.md  (refresh window tuning, grace tradeoff — operator-facing knobs)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/supervise — extending the SDD-19 entry)
- /Users/mrz/projects/hush/docs/sdd/SDD-21.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/supervise
- Files: refill.go (Refiller + Refill), refresh.go (Refresher
  + Run + scheduler), grace.go (Grace cache), refill_test.go,
  refresh_test.go, grace_test.go
- Exported API:
    type Refiller struct { ... }
    func NewRefiller(client *http.Client, store *Store, logger *slog.Logger) *Refiller
    func (r *Refiller) Refill(ctx context.Context, scopes []string) error
    type Refresher struct { ... }
    func NewRefresher(window string, ttl time.Duration, refill func(ctx context.Context) error, logger *slog.Logger) *Refresher
    func (r *Refresher) Run(ctx context.Context) error
    type Grace struct { ... }
    func NewGrace(window time.Duration, enabled bool) *Grace
    func (g *Grace) Get(name string) (*securebytes.SecureBytes, bool)
    func (g *Grace) Set(name string, value *securebytes.SecureBytes)
    var ErrJTIUnknown, ErrBootTimeout

Implementation contract (HOW — locked):
- Refill: for each scope, GET <server>/s/<name> with Bearer JWT.
  HTTP 401 with body {"error":"unknown_jti"} → ErrJTIUnknown,
  caller transitions state. Successful response: ECIES.Decrypt
  (SDD-09) → SecureBytes → write to Grace.Set + hand pointer
  to child env builder (the env builder lives in SDD-20/23).
- Refresher.Run: parses window via the same parser as SDD-18
  uses; computes next-fire from time.Now() within the window;
  if today's window has passed AND ttl - now < 30m, schedule
  immediately (T-30 fallback). Loop on a time.Timer; stop on
  ctx cancel.
- Grace: map[string]*securebytes.SecureBytes guarded by
  sync.RWMutex; each Set records the entry's expiry
  (now + window, capped at 4h via min(window, 4h)). A sweeper
  goroutine started by NewGrace's caller (NOT NewGrace itself —
  Constitution IX) Destroys expired entries.
- Boot retry: implementation lives in supervise.go (added later
  by SDD-23 orchestrator) but the helper Refill must be
  callable in a loop with caller-managed exp-backoff. Document
  the contract in the godoc.
- DM rate limit: not in this chunk — it's already in SDD-11
  (BotApprover). This chunk respects ErrRateLimited from
  SDD-11 and surfaces it as a logged WARN, never a state
  transition.
- Cached secrets MUST NEVER appear as a Go string anywhere in
  this chunk (Constitution X).

Coverage target: 95%.
Constitutional principles in scope: IV, V, VIII, IX, X.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-21 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-21.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 95%. Tests required: TestRefill_SilentOnCleanExit, TestRefill_401UnknownJTITransitions, TestRefill_NetworkErrorIsRetryable, TestRefresh_FiresInWindow (use injected clock), TestRefresh_T30MinFallback (window passed + ttl near expiry), TestRefresh_StopsOnCtxCancel (race-clean), TestGrace_UsesCacheOnExpiredJWT, TestGrace_TTLCapAt4h, TestGrace_DisabledWhenConfigFalse, TestGrace_SweeperDestroysExpired, TestBootRetry_BackoffRespected (orchestrator-side smoke; full coverage at SDD-23), TestBootRetry_NeverPromptsDiscord (no Approver call). Final phase MUST include magex format:fix, magex lint, magex test:race."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-21 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-21.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 95% on internal/supervise (refill/refresh/
   grace portions):
     go test -cover ./internal/supervise/ -run "Refill|Refresh|Grace"
3. Confirm Scenarios 3, 8, 9, 11 from docs/LIFECYCLE-SCENARIOS.md
   each have a passing unit test in this chunk.
4. Confirm refresh scheduler is race-clean
   (TestRefresh_StopsOnCtxCancel).
5. Append "Exported API — locked at SDD-21" extension to the
   internal/supervise entry in docs/PACKAGE-MAP.md listing the
   Refiller/Refresher/Grace API from the chunk doc.
6. Update docs/AC-MATRIX.md AC-10 row with the new test file paths.
7. Mark SDD-21 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add internal/supervise/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(supervise): refill + refresh window + grace cache (SDD-21)"

Final message: confirm gates passed, race-clean, coverage ≥ 95%,
Scenarios 3/8/9/11 each have passing tests, AC-10 row updated,
SDD-PLAYBOOK updated, and the combined commit created.
```
