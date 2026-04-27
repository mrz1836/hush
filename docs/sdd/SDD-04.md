# SDD-04 — `internal/testutil` (test fixtures + sentinel helpers + harness primitives)

**Phase:** 1
**Package:** `internal/testutil`
**Files:** `vault_fixture.go`, `keys_fixture.go`, `sentinel.go`, `discord_stub.go`, `doc.go`, `*_test.go`
**Branch:** `004-testutil` (created by the `before_specify` git hook)
**Blocked by:** SDD-01, SDD-02, SDD-03
**Blocks:** SDD-25 + every server/cli/supervisor test chunk
**Primary AC:** indirect support for AC-9
**Coverage target:** 80% (test infrastructure)

**Behaviour contracts (MUST):**
- Every fixture uses `t.Cleanup` so leaks fail loudly
- `DiscordStub` supports per-test scenario programming: `ApproveAll=true` → auto-approve; OR a programmable per-call response queue
- `NewTestKeys` uses a hardcoded `"hush-test-seed-NEVER-USE-IN-PROD"` passphrase + salt — deterministic, never used outside tests
- `SentinelSecret(n)` returns the canonical `SECRET_SHOULD_NEVER_APPEAR_<n>` string; `AssertSentinelAbsent` asserts the sentinel is not present in the supplied haystack and reports a clean diff on failure
- `NewTestVault` produces a temp-dir-scoped vault file with the given secrets, plus a vaultKey `*SecureBytes` and a cleanup func

**Anti-contracts (MUST NOT):**
- Create files outside `t.TempDir()`
- Persist any state between tests
- Hit a real Discord (or any external network) from any helper

**Tests required:**
- Unit only: each helper has a self-test that exercises happy path + cleanup safety
- No fuzz, no sentinel-leak (this package IS the sentinel infrastructure)

**Constitutional principles in scope:** VIII (test infrastructure backing 100% coverage targets elsewhere)

**Exported API to lock in PACKAGE-MAP.md (this chunk — new entry):**
- `func NewTestVault(t *testing.T, secrets map[string]string) (path string, vaultKey *securebytes.SecureBytes, cleanup func())`
- `func NewTestKeys(t *testing.T) (masterSeed []byte)` — deterministic
- `func SentinelSecret(n int) string`
- `func AssertSentinelAbsent(t *testing.T, sentinel, haystack string)`
- `type DiscordStub struct { ApproveAll bool; Calls []ApprovalCall; ... }`
- `func NewDiscordStub() *DiscordStub`
- (Define a minimal local `Approver`-like interface here that SDD-11 will widen via the real Approver interface)

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. Do NOT
chain them in one session. All commits for this chunk are deferred
to a single combined commit at the end of Prompt 5 (Implement). Do
not commit between phases.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-04 (internal/testutil: test
fixtures + sentinel helpers + harness primitives) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principle VIII — TDD + 100% coverage; this package is the foundation that lets downstream chunks meet that bar)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md  (§3 layout, §5 sentinel pattern)
- /Users/mrz/projects/hush/docs/sdd/SDD-04.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/testutil package gives every downstream test a uniform,
leak-safe set of helpers: deterministic test keys, programmable
Discord approval stub, sentinel string generator, sentinel-absent
assertion, and a temp-dir-scoped vault fixture. It is purely a
testing dependency — never imported by production code.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- Every fixture is leak-safe via t.Cleanup; missing cleanup is a
  test failure, not a silent leak.
- The Discord stub supports two scenario shapes: auto-approve-all,
  AND a programmable per-call response queue; the stub records
  every call for post-test inspection.
- Test keys are deterministic — the same passphrase + salt
  ("hush-test-seed-NEVER-USE-IN-PROD") produces the same master
  seed every run.
- The sentinel infrastructure: SentinelSecret(n) returns a
  recognisable, unmistakable string ("SECRET_SHOULD_NEVER_APPEAR_<n>");
  AssertSentinelAbsent fails the test if the sentinel appears
  anywhere in the supplied haystack.
- NewTestVault creates a real on-disk HUSH-format vault inside
  t.TempDir() containing the supplied secrets, returns the vault
  path + vault key + cleanup; cleanup zeroes the key.

The spec MUST NOT encode HOW (no Go-specific package layout, no
testing-library names beyond stdlib testing). Those are plan-phase.

Acceptance criterion: indirect support for AC-9 (test infrastructure
completeness — the suite of helpers backing every other chunk's
test coverage).

Action — run exactly one command:
  /speckit-specify "internal/testutil: provide deterministic test keys, a programmable Discord approval stub, a sentinel-string helper plus AssertSentinelAbsent, and a temp-dir-scoped vault fixture; every helper is leak-safe via t.Cleanup and never persists state between tests"

The before_specify hook will create branch 004-testutil. Confirm
the branch was created.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract. Otherwise leave the marker —
/speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-04 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-04.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-04 (internal/testutil) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md  (§3 layout, §5 sentinel pattern — both load-bearing)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (no entry yet — you will create one)
- /Users/mrz/projects/hush/docs/sdd/SDD-04.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/testutil (NEW package — this chunk introduces it)
- Files: vault_fixture.go, keys_fixture.go, sentinel.go,
  discord_stub.go, doc.go, vault_fixture_test.go,
  keys_fixture_test.go, sentinel_test.go, discord_stub_test.go
- Exported API:
    func NewTestVault(t *testing.T, secrets map[string]string) (path string, vaultKey *securebytes.SecureBytes, cleanup func())
    func NewTestKeys(t *testing.T) (masterSeed []byte)
    func SentinelSecret(n int) string
    func AssertSentinelAbsent(t *testing.T, sentinel, haystack string)
    type DiscordStub struct { ApproveAll bool; Calls []ApprovalCall; ... }
    func NewDiscordStub() *DiscordStub
    type ApprovalCall struct { ... }   // one record per Discord call
    type Approver interface { ... }    // minimal local interface; SDD-11 will widen

Implementation contract (HOW — locked):
- All helpers take *testing.T as the first parameter and register
  t.Cleanup callbacks themselves — callers MUST NOT need to
  remember to call cleanup.
- NewTestVault uses internal/vault.Save (SDD-03) to write a real
  HUSH-format file inside t.TempDir(). vaultKey is derived via
  internal/keys (SDD-01) from the deterministic test passphrase.
  cleanup zeroes vaultKey.
- NewTestKeys uses internal/keys.DeriveMasterSeed with the
  hardcoded "hush-test-seed-NEVER-USE-IN-PROD" passphrase and a
  fixed 16-byte salt. Output deterministic across runs.
- SentinelSecret(n) returns the literal string
  "SECRET_SHOULD_NEVER_APPEAR_<n>" — keep it un-formatted (no
  spaces, no punctuation that would slip past a substring search).
- AssertSentinelAbsent uses strings.Contains — on failure, prints
  the haystack with the sentinel position highlighted.
- DiscordStub: ApproveAll bool; Responses []Decision (queue);
  Calls []ApprovalCall (recorded). RequestApproval pops from
  Responses if non-empty, else uses ApproveAll. Thread-safe via
  sync.Mutex.
- Local Approver interface only describes the methods DiscordStub
  satisfies; SDD-11 will define the real Approver in internal/discord.

Coverage target: 80% (test infrastructure — relaxed from 100%
because helpers without external surface area are exercised by
the consuming tests).
Constitutional principles in scope: VIII.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-04 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-04.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a self-test task for every helper BEFORE the helper implementation task. Coverage target: 80% (test infrastructure). Self-tests must cover: NewTestVault round-trip + cleanup safety, NewTestKeys determinism, SentinelSecret format stability, AssertSentinelAbsent positive + negative cases, DiscordStub queue exhaustion + ApproveAll fallback + thread-safety. Final phase MUST include magex format:fix, magex lint, magex test:race."

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-04 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-04.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage ≥ 80% on internal/testutil:
     go test -cover ./internal/testutil/
3. Confirm no fixture creates files outside t.TempDir() (manual
   audit — grep tests for `os.CreateTemp` or `ioutil.TempFile` and
   replace with t.TempDir if found).
4. Append a NEW internal/testutil entry to docs/PACKAGE-MAP.md
   with title "Exported API — locked at SDD-04" listing the
   eight exported symbols / types from the chunk doc.
5. Mark SDD-04 status `done` in docs/SDD-PLAYBOOK.md.
6. (No AC-MATRIX update — this chunk is indirect support, not
   an AC owner.)

Make one combined commit:
  git add internal/testutil/ docs/PACKAGE-MAP.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(testutil): test fixtures + sentinel helpers + Discord stub (SDD-04)"

Final message: confirm gates passed, race-clean, coverage ≥ 80%,
PACKAGE-MAP entry created with the locked API, SDD-PLAYBOOK
updated, and the combined commit created.
```
