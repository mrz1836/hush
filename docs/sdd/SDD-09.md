# SDD-09 — `internal/transport/ecies` (ECIES encrypt/decrypt for secret responses)

**Phase:** 2
**Package:** `internal/transport/ecies`
**Files:** `ecies.go`, `*_test.go`, `decrypt_fuzz_test.go`
**Branch:** `009-transport-ecies` (created by the `before_specify` git hook)
**Blocked by:** SDD-02
**Blocks:** SDD-13, SDD-16
**Primary AC:** AC-7 (Layer 3 — wire-level encryption of secret responses)
**Coverage target:** 100%; **fuzz target #3** (ECIES decrypt input)

**Behaviour contracts (MUST):**
- Implementation uses go-bitcoin ECIES helpers (already locked at SDD-01)
- `Encrypt`'s plaintext input is zeroed by caller; this package zeroes its OWN intermediate buffers
- `Decrypt` returns a fresh `SecureBytes`; caller owns `Destroy()`
- Errors are typed; never include any byte from envelope or plaintext in `err.Error()`

**Anti-contracts (MUST NOT):**
- Accept `string` plaintext
- Return `[]byte` plaintext (must be `SecureBytes`-wrapped)
- Cache or memoize keys

**Tests required:**
- Unit: round-trip with multiple sizes (1B, 1KB, 1MB), wrong-key fails, mangled envelope fails, empty plaintext rejected
- Fuzz: `FuzzECIESDecrypt` ≥60s clean — random envelope bytes; assert no panic
- Sentinel-leak: `TestECIES_NoLeakOnError` — encrypt `SECRET_SHOULD_NEVER_APPEAR_9`; mangle envelope; assert sentinel absent from `err.Error()`

**Constitutional principles in scope:** III (Layer 3), VIII (100% + fuzz target #3), X (no plaintext bytes in errors), XI (no new crypto deps)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `func Encrypt(ctx context.Context, recipientPub *ecdsa.PublicKey, plaintext []byte) ([]byte, error)`
- `func Decrypt(ctx context.Context, recipientPriv *ecdsa.PrivateKey, envelope []byte) (*securebytes.SecureBytes, error)`
- `var ErrECIESDecryptFailed, ErrECIESEnvelopeTooShort`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-09 (internal/transport/ecies:
ECIES encrypt/decrypt for secret responses) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principle III Layer 3, VIII)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 3 — wire-level secret-response encryption)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-5, AC-7)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-7 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-09.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/transport/ecies package encrypts secret-response
payloads end-to-end from the server to a per-request ephemeral
client key. The server uses Encrypt; the client uses Decrypt and
receives the plaintext as a freshly-allocated SecureBytes. It is
consumed by SDD-13 (server /s handler) and SDD-16 (hush request).

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- Encrypt takes a plaintext byte slice and a recipient public
  key; produces an opaque envelope byte slice.
- Decrypt takes an envelope and the recipient private key;
  produces a fresh SecureBytes that the caller owns and Destroys.
- A wrong-key Decrypt fails with a distinct, named error; a
  malformed/short envelope fails with a distinct, named error.
- Error messages MUST NEVER contain any byte from the envelope
  or the plaintext (sentinel-leak test enforces this).
- Encrypt zeroes its own intermediate buffers before returning.

The spec MUST NOT encode HOW (no library names, no specific
ECIES variant naming beyond "ECIES"). Those are plan-phase.

Acceptance criterion: AC-7 (Layer 3).

Action — run exactly one command:
  /speckit-specify "internal/transport/ecies: encrypt secret-response payloads from the server to a per-request ephemeral client key (ECIES); Decrypt returns a fresh SecureBytes; errors are typed and never include any envelope or plaintext byte"

The before_specify hook will create branch 009-transport-ecies.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-09 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-09.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-09 (internal/transport/ecies)
of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; III/VIII/X/XI are load-bearing)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 3 — required envelope shape, error-leak rules)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-5, AC-7)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/transport — the API contract you will lock)
- /Users/mrz/projects/hush/docs/sdd/SDD-09.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/transport/ecies
- Files: ecies.go (Encrypt + Decrypt), ecies_test.go,
  decrypt_fuzz_test.go
- Exported API:
    func Encrypt(ctx context.Context, recipientPub *ecdsa.PublicKey, plaintext []byte) ([]byte, error)
    func Decrypt(ctx context.Context, recipientPriv *ecdsa.PrivateKey, envelope []byte) (*securebytes.SecureBytes, error)
    var ErrECIESDecryptFailed, ErrECIESEnvelopeTooShort

Implementation contract (HOW — locked):
- Use github.com/bitcoinschema/go-bitcoin's ECIES helpers
  (already locked by SDD-01). NO new crypto deps (Constitution XI).
- Encrypt copies the input plaintext into an internal buffer,
  computes the envelope, then zeroes the internal buffer before
  returning. The caller's input slice is the caller's responsibility.
- Decrypt allocates a fresh []byte for the plaintext, wraps it in
  securebytes.New (which zeroes the input), and returns the
  SecureBytes. The caller owns Destroy.
- Error mapping: any go-bitcoin error → ErrECIESDecryptFailed;
  envelope length below the documented minimum → ErrECIESEnvelopeTooShort.
- Error messages MUST be static strings ("ECIES decrypt failed",
  "envelope too short") — NEVER format any envelope or plaintext
  byte into the message.
- ctx context.Context is the FIRST parameter (Constitution IX).

Coverage target: 100%. Fuzz target: FuzzECIESDecrypt (60s gate).
Constitutional principles in scope: III, VIII, IX, X, XI.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-09 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-09.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 100%. Tests required: TestECIES_RoundTrip_1B, TestECIES_RoundTrip_1KB, TestECIES_RoundTrip_1MB, TestECIES_DecryptWrongKey_Fails, TestECIES_DecryptMangledEnvelope_Fails, TestECIES_DecryptEmptyEnvelope_TooShort, TestECIES_DecryptReturnsSecureBytes (caller can Destroy without affecting source). Fuzz: FuzzECIESDecrypt — random envelope bytes, no panic. Sentinel-leak: TestECIES_NoLeakOnError encrypts SECRET_SHOULD_NEVER_APPEAR_9, mangles envelope, asserts absence from err.Error(). Final phase MUST include magex format:fix, magex lint, magex test:race, and go test -fuzz=FuzzECIESDecrypt -fuzztime=60s ./internal/transport/ecies/"

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-09 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-09.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Fuzz (60s minimum, no crashes / no new bug corpus):
     go test -fuzz=FuzzECIESDecrypt -fuzztime=60s ./internal/transport/ecies/
3. Verify coverage = 100% on internal/transport/ecies:
     go test -cover ./internal/transport/ecies/
4. Confirm TestECIES_NoLeakOnError passed and
   SECRET_SHOULD_NEVER_APPEAR_9 is absent from err.Error().
5. Append "Exported API — locked at SDD-09" section to
   docs/PACKAGE-MAP.md under internal/transport listing the locked
   API from the chunk doc.
6. Update docs/AC-MATRIX.md AC-7 row (Layer 3) with the new test
   file paths.
7. Mark SDD-09 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add internal/transport/ecies/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(transport/ecies): ECIES encrypt/decrypt of secret responses (SDD-09)"

Final message: confirm gates passed, fuzz 60s clean, coverage =
100%, round-trip across 1B/1KB/1MB sizes, sentinel-leak absent
from err.Error(), AC-7 row updated, SDD-PLAYBOOK updated, and
the combined commit created.
```
