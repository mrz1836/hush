# SDD-02 — `internal/vault/securebytes` (mlocked memory + zero-on-destroy)

**Phase:** 1
**Package:** `internal/vault/securebytes`
**Files:** `securebytes.go`, `securebytes_darwin.go`, `securebytes_linux.go`, `securebytes_test.go`
**Branch:** `002-securebytes` (created by the `before_specify` git hook)
**Blocked by:** none (parallel-safe with SDD-01)
**Blocks:** SDD-03, SDD-07, SDD-13, SDD-16, SDD-21
**Primary AC:** AC-7 (Layer 5 — secure memory)
**Coverage target:** 100%

**Behaviour contracts (MUST):**
- `SecureBytes` type wrapping `[]byte` with `mlock`; zero-on-`Destroy`; runtime finalizer also zeros on GC
- `slog.LogValuer` returning `slog.StringValue("[redacted]")` (Constitution X — type-driven redaction)
- `fmt.Stringer` returning `"[redacted]"`
- `json.Marshaler` returning `[]byte("[redacted]")`
- Borrow-checked access via `Use(func(b []byte))` only — the `[]byte` handed to `fn` MUST NOT escape
- Constructor accepts ONLY `[]byte`; zero the input `[]byte` immediately after copy
- `Destroy` is idempotent; post-`Destroy` use returns `ErrDestroyed`

**Anti-contracts (MUST NOT):**
- Expose underlying `[]byte` directly (no `Bytes()` accessor)
- Allow construction from `string`
- Use `cgo` (Constitution IX — `CGO_ENABLED=0`)
- Use `unsafe` outside the OS-specific mlock wrappers

**Tests required:**
- Unit: zeroing on Destroy, redaction in slog/fmt/json, double-`Destroy` idempotency, post-`Destroy` returns `ErrDestroyed`, `Use` scope-bounding
- Sentinel-leak: `TestSecureBytes_RedactionSentinel` — log a SecureBytes wrapping `SECRET_SHOULD_NEVER_APPEAR_2` via `slogtest`; assert sentinel absent from output
- Finalizer: `TestSecureBytes_FinalizerZerosOnGC` — force GC, verify zeroing triggered
- Race: `TestSecureBytes_ConcurrentUse` — N goroutines calling `Use`, `-race` clean

**Constitutional principles in scope:** III (Layer 5), VIII (100% coverage + TDD), IX (idiomatic Go, CGO=0), X (type-driven redaction), XI (no new deps)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `type SecureBytes` (opaque)
- `func New(b []byte) (*SecureBytes, error)`
- `func (sb *SecureBytes) Use(fn func(b []byte)) error`
- `func (sb *SecureBytes) Len() int`
- `func (sb *SecureBytes) Destroy() error`
- `func (sb *SecureBytes) LogValue() slog.Value`
- `func (sb *SecureBytes) String() string`
- `func (sb *SecureBytes) MarshalJSON() ([]byte, error)`
- `var ErrDestroyed`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. Do NOT
chain them in one session — speckit persists each artifact to disk
(`spec.md`, `plan.md`, `tasks.md`, plus `.specify/feature.json`) so
fresh sessions reload state without losing fidelity.

The `extensions.yml` git hooks auto-commit each artifact. Accept those
in Prompts 1, 3, 4. In Prompt 2 accept only if `spec.md` changed.
**Decline** the `after_implement` auto-commit in Prompt 5 — that prompt
makes one combined commit covering code + doc updates.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-02 (internal/vault/securebytes:
mlocked memory + zero-on-destroy) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles III, VIII, X — these encode the non-negotiable ACs this chunk must satisfy)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 5 — secure memory + Go runtime known limitation)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-7 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-02.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/vault/securebytes package provides a SecureBytes type
that wraps a byte slice in mlocked memory, zeroes the contents on
explicit Destroy AND on garbage collection (via a runtime finalizer),
and renders as "[redacted]" in every standard logging / formatting /
serialisation path. It is the foundation for every secret-handling
package downstream (vault payloads, JWT tokens, ECIES envelopes,
client keys, supervisor grace cache).

The spec MUST encode these acceptance-level (WHAT) requirements.
Treat each as non-negotiable — if /speckit-specify's "informed
guesses" would soften any of them, override the guess to match this
list:

- A SecureBytes value protects its underlying bytes from swap
  (memory locked) AND from accidental disclosure via logging,
  formatting, or JSON serialisation (always renders "[redacted]").
- The bytes MUST be zeroed when the value is explicitly destroyed
  AND when the value is garbage-collected.
- The ONLY read path is a borrow-checked callback — there is no
  way to extract the raw bytes by accessor.
- Construction takes ONLY a byte slice (never a string), and the
  caller's input slice is zeroed immediately after the copy.
- Destroy is idempotent; using a destroyed value returns a distinct,
  named error.
- The implementation works on macOS and Linux without cgo
  (Constitution IX — CGO_ENABLED=0 across the project).

The spec MUST NOT encode HOW (no library names, no syscall names,
no file layout, no Go-specific idioms). Those are plan-phase
concerns.

Acceptance criterion: AC-7 (Layer 5 — secure memory).

Action — run exactly one command:
  /speckit-specify "internal/vault/securebytes: provide a SecureBytes type that mlocks its bytes, zeroes them on destroy and on GC, and renders as [redacted] in every standard log/format/JSON path; the only read path is a borrow-checked callback"

The before_specify hook will create branch 002-securebytes.
Confirm the branch was created.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. If the contract
dictates the answer, fill it in. Otherwise leave the marker —
/speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-02 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-02.md (the chunk contract
— consult it if /speckit-clarify surfaces an ambiguity that the
contract already answers).

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-02 (internal/vault/securebytes:
mlocked memory + zero-on-destroy) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check against this)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 5 + the documented Go runtime limitation around mlock + GC)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/vault — securebytes subpackage entry, the API contract you will lock)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md  (§5 redaction tests, sentinel pattern)
- /Users/mrz/projects/hush/docs/sdd/SDD-02.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check gate — if it fires, fix the plan to comply,
do NOT bypass.

Scope:
- Package: internal/vault/securebytes
- Files: securebytes.go (cross-platform API + finalizer wiring),
  securebytes_darwin.go (mlock/munlock via golang.org/x/sys/unix),
  securebytes_linux.go (mlock/munlock via golang.org/x/sys/unix),
  securebytes_test.go
- Exported API:
    type SecureBytes  (opaque, pointer-only usage)
    func New(b []byte) (*SecureBytes, error)
    func (sb *SecureBytes) Use(fn func(b []byte)) error
    func (sb *SecureBytes) Len() int
    func (sb *SecureBytes) Destroy() error
    func (sb *SecureBytes) LogValue() slog.Value
    func (sb *SecureBytes) String() string
    func (sb *SecureBytes) MarshalJSON() ([]byte, error)
    var ErrDestroyed

Implementation contract (HOW — locked):
- Use golang.org/x/sys/unix Mlock/Munlock — NO cgo, NO unsafe
  outside the syscall wrappers. Build-tagged per-OS files
  (securebytes_darwin.go, securebytes_linux.go).
- New copies the input slice into a freshly-allocated buffer,
  mlocks it, then zeroes the input slice. Constructor signature
  takes []byte (NOT string).
- Use(fn) takes a closure; pass the underlying []byte; the
  closure MUST be documented as "do not retain the slice past
  this call". Return ErrDestroyed if already destroyed.
- Destroy: zero the buffer (volatile-style write), munlock, mark
  destroyed. Idempotent.
- runtime.SetFinalizer wired in New to call Destroy on GC.
- LogValue/String/MarshalJSON all return the literal "[redacted]";
  none of them touch the underlying bytes.
- All concurrent operations protected by a sync.Mutex; the Mutex
  protects the destroyed flag and the buffer pointer.

Coverage target: 100%.
Constitutional principles in scope: III (Layer 5), VIII (100%),
IX (idiomatic Go, CGO=0), X (type-driven redaction), XI (no new deps).

Note (from docs/SECURITY.md): the Go runtime's stack/heap copy may
defeat mlock guarantees in pathological cases. Document this
limitation in the package doc.go, but do NOT add bandaid mitigations
beyond the documented design.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-02 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-02.md.

NOTE: /speckit-tasks defaults to NO test tasks unless explicitly
told otherwise. This project is TDD-mandatory (Constitution VIII).
Pass TDD as the command argument.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 100%. The test list MUST cover: zeroing on Destroy, redaction in slog/fmt/json, double-Destroy idempotency, post-Destroy ErrDestroyed, Use scope-bounding, TestSecureBytes_FinalizerZerosOnGC (force GC), TestSecureBytes_ConcurrentUse (race-clean), and the sentinel-leak test TestSecureBytes_RedactionSentinel wrapping SECRET_SHOULD_NEVER_APPEAR_2. Final phase MUST include magex format:fix, magex lint, magex test:race."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-02 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-02.md (the chunk contract
— re-consult it if you start to drift mid-implementation).

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Verify coverage = 100% on internal/vault/securebytes:
     go test -cover ./internal/vault/securebytes/
3. Confirm the sentinel-leak test
   (TestSecureBytes_RedactionSentinel) passed and
   SECRET_SHOULD_NEVER_APPEAR_2 is absent from any captured log.
4. Append "Exported API — locked at SDD-02" section to
   docs/PACKAGE-MAP.md under internal/vault (securebytes subpackage)
   listing the nine exported symbols from the chunk doc.
5. Update docs/AC-MATRIX.md AC-7 row with the new test file paths
   (Layer 5 entry).
6. Mark SDD-02 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/vault/securebytes/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(vault/securebytes): mlocked secure memory + redaction (SDD-02)"

Final message: confirm gates passed, race-clean, coverage = 100%,
sentinel absent, mlock works on darwin AND linux, finalizer triggers
in TestSecureBytes_FinalizerZerosOnGC, the three docs updated, and
the combined commit created.
```
