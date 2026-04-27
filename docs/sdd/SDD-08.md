# SDD-08 — `internal/transport/sign` (ECDSA canonical-JSON request signing + nonce + timestamp)

**Phase:** 2
**Package:** `internal/transport/sign`
**Files:** `canonical.go`, `sign.go`, `verify.go`, `nonce.go`, `timestamp.go`, `*_test.go`, `verify_fuzz_test.go`
**Branch:** `008-transport-sign` (created by the `before_specify` git hook)
**Blocked by:** SDD-01
**Blocks:** SDD-12, SDD-16
**Primary AC:** AC-7 (Layer 4 — request signing + replay protection)
**Coverage target:** 100%; **fuzz target #4** (request signature payload)

**Behaviour contracts (MUST):**
- `CanonicalJSON` sorts keys alphabetically at every depth; uses `json.RawMessage` for already-canonical chunks; rejects `NaN` / `Inf`
- `Sign`: go-bitcoin Bitcoin-style ECDSA over `SHA-256(canonical)`
- `NonceCache` backed by `sync.Map` + sweep goroutine; goroutine started by `Run(ctx)` with explicit cancellation per Constitution IX
- `IsFreshTimestamp` uses `time.Now()` (testable via injectable clock)

**Anti-contracts (MUST NOT):**
- Use stdlib `encoding/json` without sorting (gotcha: stdlib does NOT sort map keys for `json.Marshal` of struct — but DOES for `map[string]any`; tests must cover both)
- Start any goroutine without an explicit `Run(ctx)` entry point
- Log nonces

**Tests required:**
- Unit: canonicalisation determinism (10 known shapes), `Sign`+`Verify` round-trip, wrong-key rejection, nonce-replay rejection, expired-nonce-allowed-after-sweep, timestamp-too-old, timestamp-future-skew
- Fuzz: `FuzzVerifyRequest` ≥60s clean — random JSON + signature; assert no panic, every error typed
- Race: `TestNonceCache_ConcurrentAdd` — N goroutines `Add`-ing the same nonce; exactly one returns `firstSeen=true`

**Constitutional principles in scope:** III (Layer 4), VIII (100% + fuzz target #4), IX (no `init`, explicit goroutine lifecycle), X (no nonces in logs)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `func CanonicalJSON(v any) ([]byte, error)`
- `func Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error)`
- `func Verify(ctx context.Context, key *ecdsa.PublicKey, payload, sig []byte) error`
- `type NonceCache interface { Add(ctx context.Context, nonce string, ttl time.Duration) (firstSeen bool, err error); Run(ctx context.Context) }`
- `func NewNonceCache() NonceCache`
- `func IsFreshTimestamp(ts time.Time, skew time.Duration) bool`
- `var ErrSignatureInvalid, ErrNonceReplay, ErrTimestampStale`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. All
commits for this chunk are deferred to a single combined commit at the
end of Prompt 5 (Implement). Do not commit between phases.

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-08 (internal/transport/sign:
ECDSA canonical-JSON request signing + nonce + timestamp) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principle III Layer 4, VIII)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-6, AC-7)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 4, request replay protection)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-7 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-08.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/transport/sign package gives the client and server a
canonical request-signing protocol: a deterministic JSON
canonicalisation, an ECDSA signature over its SHA-256, and a
nonce+timestamp pair that defeats replay attacks. It is consumed by
SDD-12 (server /claim verification) and SDD-16 (hush request).

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- A canonical JSON encoder MUST produce byte-identical output for
  semantically-equal inputs regardless of map iteration order.
- Sign and Verify operate on the SHA-256 hash of canonical JSON.
- Replay defence MUST combine BOTH a nonce cache (each nonce
  accepted exactly once within its TTL) AND a timestamp freshness
  check (configurable skew window).
- Each replay-defence failure mode is a distinct, named error.
- The nonce cache's sweep goroutine MUST be explicitly started
  via Run(ctx) and stop on ctx cancel — no implicit goroutines.
- Concurrent Add of the same nonce by N goroutines MUST result
  in exactly ONE caller seeing firstSeen=true.

The spec MUST NOT encode HOW (no library names, no specific hash
function naming beyond "SHA-256"). Those are plan-phase.

Acceptance criterion: AC-7 (Layer 4 — request signing + replay
protection).

Action — run exactly one command:
  /speckit-specify "internal/transport/sign: canonical-JSON encoding + ECDSA sign/verify over SHA-256(canonical) + nonce cache (sweep goroutine started by explicit Run(ctx)) + timestamp freshness check; concurrent-safe nonce Add with exactly-one firstSeen=true semantics"

The before_specify hook will create branch 008-transport-sign.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-08 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-08.md.

Run: /speckit-clarify

```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-08 (internal/transport/sign)
of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; III/VIII/IX/X are load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-6, AC-7)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Layer 4, request replay protection — the canonical example matters)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/transport — the API contract you will lock)
- /Users/mrz/projects/hush/docs/sdd/SDD-08.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- Package: internal/transport/sign
- Files: canonical.go (CanonicalJSON), sign.go (Sign), verify.go
  (Verify), nonce.go (NonceCache), timestamp.go (IsFreshTimestamp),
  canonical_test.go, sign_test.go, verify_test.go, nonce_test.go,
  timestamp_test.go, verify_fuzz_test.go
- Exported API:
    func CanonicalJSON(v any) ([]byte, error)
    func Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error)
    func Verify(ctx context.Context, key *ecdsa.PublicKey, payload, sig []byte) error
    type NonceCache interface { Add(ctx context.Context, nonce string, ttl time.Duration) (firstSeen bool, err error); Run(ctx context.Context) }
    func NewNonceCache() NonceCache
    func IsFreshTimestamp(ts time.Time, skew time.Duration) bool
    var ErrSignatureInvalid, ErrNonceReplay, ErrTimestampStale

Implementation contract (HOW — locked):
- CanonicalJSON: sort keys at every depth; use a recursive
  encoder that walks reflect.Value and writes sorted output.
  Reject NaN/Inf via math.IsNaN/IsInf checks. Test with both
  struct (gotcha: stdlib does NOT sort struct field order
  beyond declared order) AND map[string]any (gotcha: stdlib
  DOES sort map keys for json.Marshal but only at top level).
- Sign uses github.com/bitcoinschema/go-bitcoin Bitcoin-style
  ECDSA over SHA-256(payload). NO new crypto deps.
- Verify mirrors Sign; returns ErrSignatureInvalid for any
  failure (no leaking signature shape via timing).
- NonceCache backed by sync.Map of nonce → expiry; Run(ctx)
  spawns a single sweep goroutine that ticks every 30s and
  removes expired entries. Run returns when ctx cancels —
  the goroutine writes a "stopped" log line on exit.
- Add is atomic via sync.Map.LoadOrStore semantics:
  load-existing → return firstSeen=false; store-new → return
  firstSeen=true. Concurrent N-goroutine Add on the same
  nonce: exactly one wins (test it).
- IsFreshTimestamp uses an injectable clock interface (function
  variable defaulting to time.Now); tests freeze the clock.

Coverage target: 100%. Fuzz target: FuzzVerifyRequest (60s gate).
Constitutional principles in scope: III, VIII, IX, X, XI.

Run: /speckit-plan

```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-08 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-08.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 100%. Tests required: TestCanonical_SortsAtAllDepths (10 known shapes), TestCanonical_RejectsNaN, TestCanonical_RejectsInf, TestCanonical_StructAndMap (both gotcha cases), TestSign_VerifyRoundTrip, TestVerify_WrongKeyFails, TestNonce_AddNewReturnsFirstSeen, TestNonce_AddDuplicateReturnsReplay, TestNonce_ExpiredAllowedAfterSweep, TestNonceCache_ConcurrentAdd (race-clean, exactly one firstSeen=true), TestTimestamp_FreshAccepted, TestTimestamp_TooOldRejected, TestTimestamp_FutureSkewRejected. Fuzz: FuzzVerifyRequest — random JSON + signature, no panic, errors typed. Final phase MUST include magex format:fix, magex lint, magex test:race, and go test -fuzz=FuzzVerifyRequest -fuzztime=60s ./internal/transport/sign/"

```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-08 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-08.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Fuzz (60s minimum, no crashes / no new bug corpus):
     go test -fuzz=FuzzVerifyRequest -fuzztime=60s ./internal/transport/sign/
3. Verify coverage = 100% on internal/transport/sign:
     go test -cover ./internal/transport/sign/
4. Confirm canonicalisation matches the example in
   docs/SECURITY.md Layer 4 (run a manual snapshot or compare).
5. Confirm TestNonceCache_ConcurrentAdd is race-clean and exactly
   one goroutine sees firstSeen=true.
6. Append "Exported API — locked at SDD-08" section to
   docs/PACKAGE-MAP.md under internal/transport listing the locked
   API from the chunk doc.
7. Update docs/AC-MATRIX.md AC-7 row (Layer 4) with the new test
   file paths.
8. Mark SDD-08 status `done` in docs/SDD-PLAYBOOK.md.

Make one combined commit:
  git add internal/transport/sign/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(transport/sign): canonical-JSON sign/verify + nonce + timestamp (SDD-08)"

Final message: confirm gates passed, fuzz 60s clean, coverage =
100%, canonicalisation matches the docs/SECURITY.md example,
nonce cache race-clean with exactly-one firstSeen=true, AC-7 row
updated, SDD-PLAYBOOK updated, and the combined commit created.
```
