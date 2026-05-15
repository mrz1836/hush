# Data Model — SDD-04 (`internal/testutil`)

**Status**: complete. This document enumerates every entity the
package defines, every state transition that entity participates in,
the validation rules attached to each field, and the interactions
between entities. Together with `contracts/testutil-api.md` and
`research.md`, it locks the design before implementation begins.

---

## §1 — Test-keys helper

### Entity: cached master seed

| Field | Type | Visibility | Origin | Lifetime |
|-------|------|------------|--------|----------|
| `cachedSeed` | `[]byte` (length 64) | unexported package-level | `keys.DeriveMasterSeed(ctx, testPassphrase, testSalt)` | process lifetime (set once via `sync.Once`, never mutated, never destroyed) |
| `seedOnce` | `sync.Once` | unexported package-level | zero-value | process lifetime |
| `testPassphrase` | `[]byte` (length 32) | unexported package-level | literal `"hush-test-seed-NEVER-USE-IN-PROD"` | immutable |
| `testSalt` | `[]byte` (length 16) | unexported package-level | hex literal `0102030405060708090A0B0C0D0E0F10` | immutable |

**Validation rules**:
- `testPassphrase` length MUST be ≥ 12 (SDD-01 `ErrPassphraseTooShort`
  guard). Verified at compile-review time: the 32-byte literal
  satisfies it trivially.
- `testSalt` length MUST be exactly 16 (SDD-01 `ErrSaltMissing`
  guard). Verified at compile-review time: the 16-byte literal
  satisfies it.
- `cachedSeed` length MUST be exactly 64 after derivation (SDD-01
  `keyLen=64` lock). Verified by an assertion in the
  `TestNewTestKeys_Length` self-test.

**State transitions**:

```text
        ┌──────────────┐
        │ uninitialised│
        └──────┬───────┘
               │  first NewTestKeys call → seedOnce.Do(derive)
               ▼
        ┌──────────────┐
        │ initialised  │  (cachedSeed = 64 bytes, never written again)
        └──────────────┘
```

**Concurrency**: `seedOnce.Do` serialises the first call; subsequent
reads of `cachedSeed` are safe because (a) the variable is only ever
written once, inside the `Do` closure, and (b) `sync.Once`'s memory
ordering guarantees that the write is visible to every subsequent
reader. `NewTestKeys` returns a defensive copy
(`out := make([]byte, 64); copy(out, cachedSeed)`) so a caller's
`clear(out)` cannot poison subsequent callers.

---

## §2 — Vault fixture

### Entity: `vault.Secret` (consumed, not defined here)

| Field | Type | Origin |
|-------|------|--------|
| `Name` | `string` | from the caller's `map[string]string` keys |
| `Description` | `string` | empty string (`""`) — the fixture does not propagate descriptions; downstream tests that need descriptions must use `vault.Save` directly |
| `Value` | `*securebytes.SecureBytes` | constructed via `securebytes.New([]byte(callerValue))` inside the fixture |

**Validation rules**:
- Name MUST satisfy SDD-03's `validateName` (non-empty, ≤256 bytes,
  printable ASCII 0x20–0x7E). The fixture does NOT pre-validate;
  it lets `vault.Save` return `vault.ErrInvalidName` so the test
  fails with the right sentinel.
- Description is always empty here; satisfies SDD-03's
  `validateDescription` trivially.
- Value MUST be non-nil; the fixture constructs it from the
  caller's string, so `nil` is impossible.

### Entity: vault file

| Field | Type | Origin |
|-------|------|--------|
| `path` | `string` | `filepath.Join(t.TempDir(), "test.vault")` |
| parent dir | filesystem | created by `t.TempDir()` with mode `0o700` |
| file mode | `fs.FileMode` | `0o600` (set by `vault.Save`) |
| contents | HUSH-format envelope | written by `vault.Save` (4-byte magic + 1-byte version + 16-byte salt + 12-byte nonce + AES-256-GCM ciphertext+tag) |

**Validation rules**: the file passes `vault.Load` (round-trip)
with the returned `vaultKey` after `vault.Save` returns. Verified
by `TestNewTestVault_RoundTrip`.

### Entity: vault key

| Field | Type | Origin |
|-------|------|--------|
| `vaultKey` | `*securebytes.SecureBytes` (length 32) | `securebytes.New(keys.DeriveVaultEncKey(seed))` |

**State transitions**:

```text
                     ┌─────────────────┐
                     │ live (mlocked,  │
                     │ Len() == 32,    │
                     │ Use() succeeds) │
                     └────────┬────────┘
                              │  cleanup() OR returned closure invoked
                              ▼
                     ┌─────────────────┐
                     │ destroyed       │
                     │ (zeroed buffer, │
                     │ Len() == 0,     │
                     │ Use() returns   │
                     │ ErrDestroyed)   │
                     └─────────────────┘
                              │  cleanup() invoked again (idempotent — no-op, returns nil)
                              ▼
                     ┌─────────────────┐
                     │ destroyed       │
                     └─────────────────┘
```

**Concurrency**: `vaultKey` is a `*securebytes.SecureBytes`, which
serialises every mutation through its own `sync.Mutex`. The fixture
itself adds no concurrency primitives — the only concurrent access
patterns are (a) the test code reading `vaultKey` while the cleanup
runs, which is safe because cleanup runs after the test body returns,
and (b) parallel subtests each owning their own `vaultKey` (no
sharing — each `t.TempDir()` returns a per-subtest path).

### Entity: value containers (one per supplied secret)

| Field | Type | Origin |
|-------|------|--------|
| each entry | `*securebytes.SecureBytes` | `securebytes.New([]byte(callerValue))` inside the fixture |

**Lifecycle**: each container is registered with `t.Cleanup` (its
`Destroy` method is called once at test exit). The `vault.Save` call
borrows each container via `Use(fn)` for the duration of the JSON
encode + AES-GCM seal; after `Save` returns, the containers can be
destroyed without affecting the on-disk file (the ciphertext is the
sole copy on disk; the plaintext lives only in the live containers).

---

## §3 — Sentinel helper and assertion

### Entity: sentinel string

| Field | Type | Origin |
|-------|------|--------|
| return value | `string` | `fmt.Sprintf("SECRET_SHOULD_NEVER_APPEAR_%d", n)` |

**Validation rules**:
- The string MUST start with the literal prefix
  `"SECRET_SHOULD_NEVER_APPEAR_"` (verified by
  `TestSentinelSecret_FormatStability`).
- The string MUST contain the integer `n` (in `%d` decimal
  rendering) immediately after the prefix (verified by
  `TestSentinelSecret_IndexEcho`).
- The string MUST contain no whitespace and no characters outside
  `[A-Z_0-9-]` (the `-` admitted only when `n < 0`). Verified by
  `TestSentinelSecret_NoWhitespacePunct`.

**State**: stateless. Each call computes the string and returns it.

### Entity: sentinel-absent assertion

| Input | Type | Constraint |
|-------|------|------------|
| `t` | `*testing.T` | non-nil |
| `sentinel` | `string` | typically the result of `SentinelSecret(n)` |
| `haystack` | `string` | any byte content (logs, error messages, captured output) |

**Behaviour**:
- If `strings.Contains(haystack, sentinel)` is false, the assertion
  is a no-op (test handle records no failure).
- If true, the assertion calls `t.Errorf` with: the sentinel
  substring, the byte offset of the first match
  (`strings.Index(haystack, sentinel)`), and a 64-byte context
  window centred on the match (clamped to `[0, len(haystack))`).

**State**: stateless. The only side-effect is on the supplied test
handle.

---

## §4 — Discord stub

### Entity: `Decision`

| Value | Underlying type | Meaning |
|-------|-----------------|---------|
| `DecisionApprove` | `Decision` (`int`) | `0` — request approved |
| `DecisionDeny` | `Decision` (`int`) | `1` — request denied |
| `DecisionApproveMute` | `Decision` (`int`) | `2` — request approved AND alert is muted for the session TTL |

**State**: enum, immutable, comparable with `==`.

### Entity: `ApprovalRequest`

| Field | Type | Description |
|-------|------|-------------|
| `RequesterHost` | `string` | name of the host issuing the request (from the request signing context) |
| `Scopes` | `[]string` | list of secret-name scopes requested (e.g. `["anthropic-prod"]`) |
| `SessionType` | `string` | `"interactive"` or `"supervisor"` |
| `TTL` | `time.Duration` | requested session TTL |
| `MaxUses` | `int` | requested per-session use cap; `0` if not applicable (e.g. supervisor TTL-only sessions) |

**Validation rules**: none enforced by the stub — the stub accepts
whatever the caller passes (this is intentional: the stub is a test
substitute, not a validator). The production `Approver` in SDD-11
will validate these fields; the stub records them verbatim for
post-test inspection.

**Helper method**: `LimitDescription() string` formats the TTL +
MaxUses pair into a human-readable string (`"20h0m0s, 50 uses"` or
`"20h0m0s, TTL-only"`) for use in the unexpected-call failure
message.

### Entity: `ApprovalCall`

| Field | Type | Description |
|-------|------|-------------|
| `Request` | `ApprovalRequest` | a copy of the request as received |
| `Decision` | `Decision` | the decision returned to the caller |
| `Err` | `error` | the error returned to the caller (`nil` on the queue and `ApproveAll` paths; `ErrUnexpectedCall` on the failure path) |
| `Index` | `int` | zero-based position of this call in the recorded list (for ergonomic assertions) |

**State**: each entry is immutable after append. The recorded-calls
list is a `[]ApprovalCall` — `Calls()` returns a defensive copy so a
caller's `slices.Sort` cannot corrupt the canonical order.

### Entity: `DiscordStub`

| Field | Type | Visibility | Description |
|-------|------|------------|-------------|
| `ApproveAll` | `bool` | exported | tail-default for calls received after the queue is exhausted |
| `mu` | `sync.Mutex` | unexported | guards `responses` and `calls` |
| `responses` | `[]Decision` | unexported | FIFO queue of pre-loaded decisions; consumed head-first |
| `calls` | `[]ApprovalCall` | unexported | append-only recorded list |
| `t` | `*testing.T` | unexported | held for the lifetime of the test that constructed the stub; used only by the `RequestApproval` failure path |

**State transitions**:

```text
                     ┌────────────────────┐
                     │ live               │
                     │ (responses queue   │
                     │  may be non-empty, │
                     │  calls list grows  │
                     │  with each call)   │
                     └─────────┬──────────┘
                               │  test handle exits → t.Cleanup runs
                               ▼
                     ┌────────────────────┐
                     │ drained            │
                     │ (responses=[],     │
                     │  calls=[],         │
                     │  no further use    │
                     │  expected)         │
                     └────────────────────┘
```

The drained state is observable only through the cleanup callback
that `NewDiscordStub` registers. Callers MUST NOT touch the stub
after the test exits — by convention this is impossible (the test
handle is invalid post-exit).

**`Enqueue` method**: `Enqueue(decisions ...Decision)` appends each
argument to the `responses` queue under `mu.Lock`. Multiple calls
to `Enqueue` are additive — they extend the queue, not replace it.

**`Calls() []ApprovalCall` method**: returns a defensive copy of
the recorded calls list under `mu.Lock`.

**`RequestApproval(ctx, req) (Decision, error)` decision tree**:

```text
RequestApproval(ctx, req):
    mu.Lock(); defer mu.Unlock()
    if len(responses) > 0:
        d := responses[0]; responses = responses[1:]
        record(req, d, nil)
        return d, nil
    if ApproveAll:
        record(req, DecisionApprove, nil)
        return DecisionApprove, nil
    err := ErrUnexpectedCall
    t.Errorf("hush/testutil: unexpected Discord approval call: host=%q scopes=%v session=%q limit=%s",
             req.RequesterHost, req.Scopes, req.SessionType, req.LimitDescription())
    record(req, DecisionDeny, err)
    return DecisionDeny, err
```

**Concurrency**: every public method that touches `responses` or
`calls` acquires `mu` (deferred unlock). The `t.Errorf` call inside
the failure path is safe to invoke under the lock — `*testing.T`
methods are themselves goroutine-safe.

### Entity: `Approver` interface

```go
type Approver interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}
```

**Satisfied by**: `*DiscordStub` (and, eventually, the production
type defined by SDD-11 in `internal/discord`).

**Defined here, not elsewhere**: Constitution IX
("interfaces at the consumer, not the producer"). The consumer is
downstream test code that wants to inject the stub before SDD-11
exists.

### Entity: `ErrUnexpectedCall`

| Property | Value |
|----------|-------|
| Type | `error` (sentinel) |
| Declared as | `var ErrUnexpectedCall = errors.New("hush/testutil: unexpected approval call")` |
| Returned by | `RequestApproval` on the queue-empty + `!ApproveAll` path |
| Comparable via | `errors.Is(err, ErrUnexpectedCall)` |

**Note**: callers rarely compare against this sentinel because the
test handle has already been failed via `t.Errorf` by the time the
sentinel is returned. The sentinel is exported so a downstream test
that wants to assert "the stub returned the unexpected-call signal"
without inspecting the test handle's failure list can do so via
`errors.Is`.

---

## §5 — Cross-entity invariants

| Invariant | Where enforced | Test |
|-----------|----------------|------|
| Every fixture registers a `t.Cleanup` callback before returning | structural; verified by code review and by the self-test that asserts no temp-dir leak across sequential tests | `TestVaultFixture_NoLeakAcrossTests` |
| Every fixture takes `*testing.T` as its FIRST parameter | structural; enforced by the locked API contract | code review + `contracts/testutil-api.md` |
| The deterministic seed is byte-identical across two invocations | `sync.Once` cache + defensive copy | `TestNewTestKeys_Determinism` |
| The vault fixture's file path is a descendant of `t.TempDir()` | `filepath.Join(t.TempDir(), ...)` | `TestNewTestVault_PathContainment` |
| The vault key's underlying buffer is zeroed after cleanup | `*securebytes.SecureBytes.Destroy` (idempotent) | `TestNewTestVault_KeyZeroed` (post-cleanup `Len() == 0`) |
| The Discord stub is safe under `-race` with N goroutines | one `sync.Mutex` guards both queue and calls | `TestDiscordStub_Concurrent` (100 goroutines) |
| No production source file imports `internal/testutil` | `depguard` lint rule + self-test grep | `TestNoProductionImport` |
| The package contains no `init()` function | structural; verified by code review and by the self-test that walks the AST | `TestNoInit` (parses each `.go`, fails if any has an `init` decl) |
| The package contains no mutable package-level variable except the documented `sync.Once`-guarded seed cache | structural; AST walk in self-test counts top-level `var` declarations | `TestPackageGlobals` (allowlist of three: `seedOnce`, `cachedSeed`, `ErrUnexpectedCall`; everything else fails the test) |

---

## §6 — Lifecycle interactions across entities

### Lifecycle: a downstream test using `NewTestVault`

```text
1. test calls testutil.NewTestVault(t, {"foo": "bar"})
     │
     ├── seedOnce.Do(...) — first time only, ~1.5 s; subsequent calls ~0
     ├── seed = cachedSeed (defensive copy)
     ├── rawKey = keys.DeriveVaultEncKey(seed)  — 32 bytes
     ├── vaultKey = securebytes.New(rawKey)     — mlocked, type-redacted
     ├── valueSBs = [securebytes.New([]byte("bar"))]
     ├── path = filepath.Join(t.TempDir(), "test.vault")
     ├── vault.Save(ctx, path, vaultKey, [{Name: "foo", Value: valueSBs[0]}])
     ├── t.Cleanup(func() { vaultKey.Destroy(); for _, sb := range valueSBs { sb.Destroy() } })
     └── return path, vaultKey, cleanup
2. test body uses vaultKey + path
3. test returns
4. t.Cleanup runs:
     │
     ├── cleanup() destroys vaultKey + valueSBs (zeroes mlocked buffers, releases mlock)
     └── t.TempDir()'s removal callback fires, deleting the temp dir + the vault file
5. (if cleanup() was also invoked explicitly mid-test, the t.Cleanup callback is a no-op
    because Destroy is idempotent — no panic, no double-free, no error)
```

### Lifecycle: a downstream test using `DiscordStub` with a mixed queue + `ApproveAll`

```text
1. test calls stub := testutil.NewDiscordStub(t)
     │
     ├── stub.t = t
     ├── stub.ApproveAll = false (zero value)
     ├── stub.responses = nil
     ├── stub.calls = nil
     └── t.Cleanup(func() { stub.responses = nil; stub.calls = nil })
2. test calls stub.Enqueue(DecisionApprove, DecisionDeny)
     └── stub.responses = [Approve, Deny]
3. test sets stub.ApproveAll = true
4. code under test calls stub.RequestApproval(ctx, req1)
     ├── pops stub.responses[0] = Approve
     ├── records ApprovalCall{req1, Approve, nil, 0}
     └── returns (Approve, nil)
5. code under test calls stub.RequestApproval(ctx, req2)
     ├── pops stub.responses[0] = Deny
     ├── records ApprovalCall{req2, Deny, nil, 1}
     └── returns (Deny, nil)
6. code under test calls stub.RequestApproval(ctx, req3)
     ├── stub.responses is now empty
     ├── stub.ApproveAll is true → returns Approve
     ├── records ApprovalCall{req3, Approve, nil, 2}
     └── returns (Approve, nil)
7. test calls stub.Calls() → returns [{req1,Approve,nil,0}, {req2,Deny,nil,1}, {req3,Approve,nil,2}]
8. test asserts the call sequence; test exits
9. t.Cleanup runs: stub.responses + stub.calls drained
```

### Lifecycle: the unexpected-call failure path

```text
1. test calls stub := testutil.NewDiscordStub(t)  (stub.ApproveAll = false, queue empty)
2. code under test calls stub.RequestApproval(ctx, req1)
     ├── stub.responses is empty
     ├── stub.ApproveAll is false
     ├── err = ErrUnexpectedCall
     ├── stub.t.Errorf("hush/testutil: unexpected Discord approval call: ...")
     ├── records ApprovalCall{req1, DecisionDeny, err, 0}
     └── returns (DecisionDeny, ErrUnexpectedCall)
3. code under test sees the denial, takes its denial-handling path
4. test exits — t.Failed() is true because of the t.Errorf in step 2
5. t.Cleanup runs: stub.responses + stub.calls drained
```

---

## §7 — What this package does NOT model

For audit clarity:

- **Real Discord state** (channels, message IDs, button click
  callbacks, bot connection lifecycle) — out of scope; SDD-11.
- **Real session state** (active sessions, JWT bookkeeping,
  revocation lists) — out of scope; SDD-07.
- **Real key hierarchy beyond the master seed and the vault enc key**
  (JWT signing keys, audit signing keys, per-machine client keys) —
  out of scope here; downstream chunks that need them call SDD-01's
  derivation functions directly with the master seed this package
  returns.
- **Vault store lifecycle (Get/Names/Destroy)** — handled by
  `vault.Store` (SDD-03); this package only writes the file; tests
  load it themselves via `vault.Load`.
- **Logger redaction** — handled by `internal/logging` (SDD-05); this
  package supplies the sentinel infrastructure those tests use, but
  does not configure or assert against any logger.

---

**Phase 1 §data-model: complete.** Every entity has a defined
shape, lifecycle, and validation rule. Every cross-entity invariant
maps to a named test. Ready for `contracts/testutil-api.md` (the
locked exported surface) and `quickstart.md` (the consumer recipe).
