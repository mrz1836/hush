# Phase 0 Research — SDD-04 (`internal/testutil`)

**Status**: complete. All HOW questions are locked by the chunk
contract (`docs/sdd/SDD-04.md`) and by the upstream chunk contracts
SDD-01, SDD-02, SDD-03. This document records the decisions, the
rationale for each, and the alternatives that were considered and
rejected so a future maintainer can audit the design without
re-litigating it.

No `NEEDS CLARIFICATION` markers remain in `spec.md` after Session
2026-04-27 (two clarifying questions were resolved during
`/speckit-clarify`: the queue-vs-`ApproveAll` precedence, and the
queue-exhausted-with-`ApproveAll`-disabled behaviour). Both are
encoded as functional requirements in the spec and as test cases in
the contract.

---

## R-01 — Master-seed derivation: deterministic Argon2id with a hardcoded test passphrase + fixed salt

**Decision**: `NewTestKeys(t)` derives the test master seed by
calling
`keys.DeriveMasterSeed(ctx, []byte("hush-test-seed-NEVER-USE-IN-PROD"),
testSalt)`, where `testSalt` is the fixed 16-byte literal
`{0x01,0x02,0x03,0x04,0x05,0x06,0x07,0x08,0x09,0x0A,0x0B,0x0C,0x0D,0x0E,0x0F,0x10}`.
The `ctx` passed in is `context.Background()`. The returned 64-byte
seed is the same on every machine and on every test run.

**Rationale**:
- SDD-01 locks `DeriveMasterSeed`'s parameters
  (`Argon2id, time=4, memory=256 MiB, threads=4, keyLen=64`) and
  validates the inputs (`passphrase ≥ 12 bytes`, `salt == 16
  bytes`). The 32-byte literal `hush-test-seed-NEVER-USE-IN-PROD`
  satisfies the passphrase length, and the 16-byte literal salt
  satisfies the salt length, so the helper inherits SDD-01's
  determinism invariant by construction (Spec SC-001).
- The literal contains the substring `NEVER-USE-IN-PROD`, which
  satisfies Spec SC-002 — a code reviewer scanning the line knows the
  bytes cannot be confused with a real key.
- A fixed salt is acceptable here because the test seed is
  deliberately non-secret. In production, the salt provides
  domain-separation against rainbow tables; in tests, determinism is
  the priority and there is no production-equivalence claim to defend.

**Alternatives considered and rejected**:
- *Per-test salt or per-test passphrase*: defeats SC-001
  (determinism across tests). Rejected.
- *Bypass Argon2id entirely and ship a hardcoded 64-byte seed
  literal*: faster, but the seed would not pass through the SDD-01
  derivation path and therefore wouldn't exercise the same code
  shapes downstream (`keys.DeriveVaultEncKey` consumes the seed via
  BIP32; the chain only works on a real Argon2id-derived seed of
  exactly 64 bytes). Rejected.
- *A per-call Argon2id invocation with no caching*: clean but
  wall-clock-expensive (~1.5 s per call × hundreds of calls per CI
  run = tens of minutes added). Rejected in favour of the
  `sync.Once`-cached design (R-02).

---

## R-02 — Memoise the deterministic seed across the test process

**Decision**: A `sync.Once`-guarded unexported package-level
variable holds the derived 64-byte master seed for the lifetime of
the test process. `NewTestKeys(t)` calls
`once.Do(func() { cachedSeed, _ = keys.DeriveMasterSeed(ctx,
testPassphrase, testSalt) })` and then returns a defensive copy of
`cachedSeed` so a caller's `clear(seed)` cannot poison subsequent
callers.

**Rationale**:
- Argon2id at the locked cost (`time=4`, `memory=256 MiB`,
  `threads=4`) takes ~1.5 s per call on the project's CI machines.
  The test suite invokes `NewTestKeys` (directly and transitively
  via `NewTestVault`) hundreds of times per full run; recomputing on
  every call would extend CI by tens of minutes, defeating the
  constitutional gate `magex test:race` is meant to enforce.
- The cached bytes are deterministic, non-secret (`NEVER-USE-IN-PROD`
  is in the source passphrase), and never escape the process — they
  cannot leak between tests because the cache lives in the same
  process as the tests. Sharing across tests is *intentional* and
  spec-aligned (Spec SC-001 explicitly demands cross-test byte
  equivalence).
- Constitution IX forbids "mutable package-level state". The seed
  cache is set-once (`sync.Once`), monotonic-read (only `NewTestKeys`
  reads it), never destroyed, never mutated after the first set —
  the closest possible approximation to an immutable global within
  Go's syntactic constraints. The `//nolint:gochecknoglobals`
  exception is documented inline AND in the plan's Complexity
  Tracking section.

**Alternatives considered and rejected**:
- *Per-test cache via `t.Setenv` or a `*testing.T`-keyed `sync.Map`*:
  unsafe under `t.Parallel()` because the same package-level cache
  would still be the substrate; per-test memoisation gains nothing
  while adding complexity. Rejected.
- *No cache, recompute every call*: see R-01. Rejected (CI cost).
- *Compile-time constant 64-byte literal*: would require the
  developer to compute Argon2id by hand and paste the bytes, and
  to recompute on any SDD-01 parameter change — fragile, and a
  source of audit ambiguity ("does this literal match the current
  Argon2id parameters?"). Rejected.

---

## R-03 — Vault fixture: real HUSH file via `vault.Save` into `t.TempDir()`

**Decision**: `NewTestVault(t, secrets)` materialises the
`map[string]string` into `[]vault.Secret` (each value wrapped via
`securebytes.New`), derives the 32-byte AES-256-GCM key via
`keys.DeriveVaultEncKey(seed)` (where `seed` comes from
`NewTestKeys`'s cached path), wraps the raw key in
`*securebytes.SecureBytes` via `securebytes.New(rawKey)`, and calls
`vault.Save(ctx, path, vaultKey, secrets)` with
`path = filepath.Join(t.TempDir(), "test.vault")`. The fixture
registers a `t.Cleanup` callback that calls `Destroy` on the vault
key and on every value `*SecureBytes` it constructed.

**Rationale**:
- `vault.Save` is the only path through which a HUSH-format file is
  produced; bypassing it (e.g. by hand-constructing a header +
  ciphertext) would let this fixture drift from the SDD-03 contract
  silently. Using the production saver is the only way to keep
  downstream tests honest — if the format changes, the fixture
  changes with it automatically.
- `vault.Save` requires the parent directory to be `0o700`. Go's
  `t.TempDir()` creates the directory with `0o700` (verified by
  reading `os` stdlib docs), so no extra `chmod` is needed.
- `vault.Save` fsyncs and atomically renames; `t.Cleanup` will
  remove the temp dir at test exit, so no file leaks beyond the
  test handle's lifetime (Spec FR-006, FR-022).
- Wrapping each value in `*securebytes.SecureBytes` matches the
  `vault.Secret.Value` type contract (SDD-03 lock). The value
  containers are constructed inside the fixture (so the fixture
  owns destruction) and are added to the cleanup chain.
- The vault key is a `*securebytes.SecureBytes`, whose `Destroy`
  method zeroes the underlying mlocked buffer. The Spec FR-008
  "bytes overwritten with zeros" requirement is therefore inherited
  from SDD-02 via type composition; the fixture does not implement
  zeroing itself (it cannot — the raw key buffer is owned by
  `*SecureBytes`). The post-`Destroy` `Len()` returns 0, which is
  the SC-005-observable property.

**Alternatives considered and rejected**:
- *Hand-craft the HUSH envelope*: see above — drift risk. Rejected.
- *Skip the file write and return an in-memory `vault.Store`
  directly*: would not exercise the on-disk path, defeating the
  primary use case (SIGHUP reload tests, atomic-write tests).
  Rejected.
- *Reuse a single shared vault file across tests*: defeats Spec
  FR-022 (no state between tests) and Spec User Story 5
  (parallel-subtest isolation). Rejected.

---

## R-04 — Cleanup contract: returned closure AND `t.Cleanup` registration (idempotent)

**Decision**: `NewTestVault` returns a `cleanup func()` value AND
registers the same logic via `t.Cleanup` before returning. Calling
the returned closure explicitly is harmless because
`*securebytes.SecureBytes.Destroy` is idempotent (returns `nil` on
already-destroyed). The chunk contract's locked signature retains
the returned `cleanup` for ergonomic clarity: a caller that wants
an explicit early-cleanup point (e.g. a sub-test that destructively
mutates the vault file mid-test) can invoke it, and the deferred
`t.Cleanup` will then no-op.

**Rationale**:
- Spec FR-007 mandates automatic registration; Spec User Story 5
  says callers must not need to invoke cleanup. Both are satisfied
  by the `t.Cleanup` registration.
- The chunk contract's locked API includes a `cleanup func()` return
  value. Removing it would break the lock; keeping it as an
  ergonomic-but-redundant invocation point is the minimal compatible
  resolution. The idempotency of `Destroy` makes the dual-path safe.

**Alternatives considered and rejected**:
- *Drop the returned `cleanup` from the signature*: would deviate
  from the chunk contract's locked API. Rejected.
- *Make the returned `cleanup` a sentinel no-op*: confusing — the
  signature would imply behaviour that isn't there. Rejected.

---

## R-05 — Sentinel format: literal `SECRET_SHOULD_NEVER_APPEAR_<n>`

**Decision**: `SentinelSecret(n int) string` returns
`fmt.Sprintf("SECRET_SHOULD_NEVER_APPEAR_%d", n)`. Pure, stateless,
deterministic. Negative indices render as
`SECRET_SHOULD_NEVER_APPEAR_-1`, etc. — still a recognisable
substring, no panic.

**Rationale**:
- The literal is documented in `docs/TESTING-STRATEGY.md` §5
  ("inject sentinel secret value like
  `SECRET_SHOULD_NEVER_APPEAR_123`"). The chunk contract names this
  exact format. Aligning the helper with the documentation prevents
  format drift across the project.
- The marker substring contains no whitespace, no punctuation that
  would break a `strings.Contains` substring search, and no
  characters that any production code is plausibly going to emit
  legitimately (the all-caps `SECRET_SHOULD_NEVER_APPEAR_` prefix is
  effectively a namespace).
- The integer index lets a single test inject multiple sentinels
  and disambiguate which one leaked, on failure.

**Alternatives considered and rejected**:
- *Random UUID per call*: defeats determinism and complicates
  failure messages. Rejected.
- *A constant string with no index*: collides on the assertion
  message when a test injects multiple sentinels. Rejected.

---

## R-06 — Sentinel-absent assertion: `strings.Contains` with highlighted-context failure message

**Decision**: `AssertSentinelAbsent(t, sentinel, haystack)` calls
`t.Helper()`, then performs `if i := strings.Index(haystack,
sentinel); i >= 0 { t.Errorf(...) }`. On failure, the message
includes the sentinel substring, the byte offset of the first
match, and a 64-byte context window around the match (clamped to
haystack bounds), so the operator can see the leak in situ without
having to grep the captured haystack themselves.

**Rationale**:
- `strings.Contains` is the canonical substring search. `strings.Index`
  is a single pass that yields both the boolean and the offset; it
  costs nothing more than `Contains`.
- The 64-byte context window is the standard "git diff context"
  width and gives enough surrounding bytes to identify the leaking
  log line or error message.
- `t.Helper()` ensures the failure surfaces at the caller's line
  number, not inside this helper.

**Alternatives considered and rejected**:
- *`bytes.Contains` over a `[]byte` haystack*: forces every caller
  to convert `string → []byte`. The chunk contract names the type
  as `string`. Rejected.
- *Regex match*: heavier and unnecessary; the sentinel is a
  literal. Rejected.

---

## R-07 — `DiscordStub`: queue-first, then `ApproveAll`, then loud failure

**Decision**: `DiscordStub.RequestApproval(ctx, req)` consumes
decisions in this order:
1. If the unexported `responses` queue is non-empty, pop the head
   and return it.
2. Else, if `ApproveAll == true`, return `DecisionApprove`.
3. Else (queue empty AND `ApproveAll == false`), call
   `t.Errorf` naming the request's identifying attributes
   (`RequesterHost`, `Scopes`, `SessionType`, `LimitDescription`)
   and return `DecisionDeny, ErrUnexpectedCall`.

The recorded-calls list (`calls []ApprovalCall`) is appended to
unconditionally on every entry (including the failure path), so a
test can assert the stub's call-history even when one of the calls
was the unexpected one.

**Rationale**:
- The order is the spec's clarified resolution (Session 2026-04-27,
  Q1: "queue is consumed first; `ApproveAll` covers every call
  after the queue is exhausted").
- Step 3 is the spec's clarified resolution (Session 2026-04-27,
  Q2: "fail the test handle immediately with an 'unexpected call'
  message"). Failing loudly through `t.Errorf` is consistent with
  Constitution VIII (test-failure-as-signal): the stub does not
  silently default-deny (would mask test setup defects) and does
  not block (would deadlock the test).
- Returning `DecisionDeny + ErrUnexpectedCall` (rather than
  panicking) lets the code under test see a normal denial response
  and proceed to its denial-handling path, while the test handle
  records the failure that will surface at test exit. This is
  symmetric with how Go's `testing` package treats `t.Errorf` (test
  is marked failed but continues running, allowing the test author
  to see the full failure context, not just the first one).

**Alternatives considered and rejected**:
- *Panic on unexpected call*: would crash the test runner and
  obscure subsequent failures in the same test. Rejected.
- *Block on a channel waiting for a future decision*: would deadlock
  the test (no decisions are coming). Rejected.
- *Silent default-deny*: would let test-setup defects masquerade as
  legitimate denial-path coverage. Rejected explicitly by the spec.

---

## R-08 — `DiscordStub` thread-safety: a single `sync.Mutex`

**Decision**: One unexported `sync.Mutex` guards both the `responses`
queue and the `calls` recorded list. Every public method that
touches either acquires the mutex via `defer mu.Unlock()`.

**Rationale**:
- Spec FR-018 requires the stub to be safe under concurrent
  invocation with no data races under `-race`.
- A single mutex is simpler than two and has no measurable cost at
  the test-suite scale (the mutex is held for nanoseconds per call).
- A `sync.RWMutex` would be over-engineered: every method either
  pops from the queue or appends to the calls list — both
  write-paths — so the read-write distinction buys nothing.

**Alternatives considered and rejected**:
- *Lock-free implementation with atomics*: complex, no benefit
  at the scale tests run. Rejected.
- *Channel-based decision queue*: would force tests to seed the
  channel before each `RequestApproval` call, complicating the
  programming model. Rejected.

---

## R-09 — `Approver` interface: minimal, consumer-defined, single method

**Decision**: The `Approver` interface in `discord_stub.go` declares
exactly one method:

```go
type Approver interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}
```

`DiscordStub` is the only type in the project that satisfies this
interface as of SDD-04. SDD-11 (when it lands) will introduce the
production `Approver` in `internal/discord`; downstream tests can
either continue importing this local interface for substitution or
migrate to `internal/discord.Approver`.

**Rationale**:
- Constitution IX: "accept interfaces, return concrete types.
  Define interfaces at the consumer, not the producer." The
  consumer of the abstraction is downstream test code that wants to
  inject the stub. Defining the interface here lets that test code
  declare a parameter of type `testutil.Approver` without dragging
  in `internal/discord` (which doesn't exist yet).
- Single-method interfaces are preferred (Constitution IX). The
  production interface in SDD-11 will be wider; this minimal local
  one is intentionally narrow so the migration path is "add fields
  to `ApprovalRequest`" rather than "deal with a v0/v1 interface
  divergence".

**Alternatives considered and rejected**:
- *Skip the interface and have downstream tests depend directly on
  the concrete `*DiscordStub`*: forces every downstream test to
  expose `*DiscordStub` in its function signatures, leaking a test
  type into production-adjacent code. Rejected.
- *Define `Approver` in `internal/discord` ahead of SDD-11*: would
  pre-empt SDD-11's design and create a dependency cycle if SDD-11
  changes the interface shape. Rejected.

---

## R-10 — Test-only enforcement: `depguard` AND a self-test

**Decision**: Two layers of enforcement:
1. `golangci-lint`'s `depguard` rule is updated (in this chunk's
   PR, listed in the IMPLEMENT-phase release-step list) to forbid
   any non-`*_test.go` file from importing
   `github.com/mrz1836/hush/internal/testutil`. This is the
   lint-enforced half — every PR that violates the rule fails
   `magex lint` before review.
2. A self-test in this package walks the project's `internal/`
   tree, parses each `.go` file's import block (using
   `go/parser.ParseFile` with `parser.ImportsOnly`), and fails
   the test if any non-`*_test.go` file imports the package. This
   is the runtime-enforced half — every `magex test:race` run
   re-verifies the invariant.

**Rationale**:
- Spec FR-025 explicitly requires the constraint to be "enforceable
  by repository search and by linter configuration" — both halves.
- Belt-and-braces: `depguard` is the early warning (lint runs in
  pre-commit), the self-test is the absolute guarantee (test runs
  in CI). One is fast, the other is comprehensive.
- The self-test costs ~10 ms (parsing a few dozen files' import
  blocks), so the ongoing overhead is negligible.

**Alternatives considered and rejected**:
- *`depguard` only*: lint configurations can be silently weakened
  by a future PR. The self-test is the audit trail. Rejected as a
  single layer.
- *Self-test only*: lint catches the violation at edit-time, not
  at test-time, which is the better developer experience. Both
  layers cost almost nothing; ship both. Rejected as a single
  layer.

---

## R-11 — No fuzz target in this package

**Decision**: `internal/testutil` ships zero fuzz targets.

**Rationale**:
- The chunk contract is explicit: "Tests required: Unit only: each
  helper has a self-test that exercises happy path + cleanup
  safety. **No fuzz, no sentinel-leak (this package IS the
  sentinel infrastructure)**."
- Constitution VIII names six mandatory fuzz targets (vault decode,
  JWT parse/validate, ECIES decrypt, request signature, supervisor
  config TOML, status socket JSON). None of them lives in
  `internal/testutil`; each is owned by its surrounding chunk.
  Adding a seventh here would dilute the discipline.
- The package's correctness is observable through its self-tests
  AND through every downstream test that uses it — fuzz coverage
  here would be redundant.

**Alternatives considered and rejected**:
- *A `FuzzAssertSentinelAbsent` target*: would fuzz `strings.Contains`,
  which is a well-tested stdlib function. Rejected.

---

## R-12 — No new module dependencies

**Decision**: The package's only Go imports are stdlib
(`context`, `fmt`, `os`, `path/filepath`, `strings`, `sync`,
`testing`) plus the three already-locked intra-repo packages
(`internal/keys`, `internal/vault`, `internal/vault/securebytes`).
The self-test additionally imports `go/parser`, `go/ast`, and
`io/fs` for the test-only-import enforcement walk.

**Rationale**: Constitution XI ("native-first, minimal
dependencies") and the SDD-04 chunk contract's anti-contract.
Adding any dependency would require a written justification per
Constitution XI; none is needed.

**Alternatives considered and rejected**:
- *`github.com/stretchr/testify/require`*: already in `go.mod` as
  a test-only transitive (other packages use it). The harness
  could use it for assertion ergonomics, but the helpers' own
  self-tests are simple enough that stdlib `testing` suffices,
  and requiring callers to depend on testify transitively
  (because they're consuming this package) would expand the
  test-time dependency surface for every downstream package.
  Rejected for self-tests; downstream tests remain free to use
  `testify` themselves.

---

## Summary

Every locked HOW choice is satisfied by Go stdlib + the three
already-locked intra-repo packages. No new external dependency.
One narrow `sync.Once`-guarded mutable global is justified for
Argon2id memoisation across the test process; the exception is
documented in the plan's Complexity Tracking section AND inline in
the source. Two-layer enforcement of the test-only invariant
(`depguard` + self-test) satisfies Spec FR-025 and the
constitutional belt-and-braces standard.

**Phase 0 result: complete.** No `NEEDS CLARIFICATION` remains.
Ready for Phase 1 design artefacts.
