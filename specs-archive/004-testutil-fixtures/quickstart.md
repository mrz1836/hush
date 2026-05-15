# Quickstart — `internal/testutil` consumer recipes

**Audience**: any author of a `*_test.go` file under
`internal/` that needs deterministic test keys, a populated vault on
disk, the canonical sentinel infrastructure, or a programmable
substitute for the Discord approver. This document is the recipe
list — start here, then refer to
`contracts/testutil-api.md` for the locked signatures and
`data-model.md` for the lifecycle details.

**Constraint**: this package is test-only. A `*.go` file (not
`*_test.go`) that imports `internal/testutil` will fail
`magex lint` (depguard rule) AND fail `magex test:race` (the
package's own `TestNoProductionImport` self-test).

---

## R-1 — Get a deterministic 64-byte master seed

```go
package somepkg_test

import (
    "testing"

    "github.com/mrz1836/hush/internal/keys"
    "github.com/mrz1836/hush/internal/testutil"
)

func TestSomething_UsesDerivedJWTKey(t *testing.T) {
    seed := testutil.NewTestKeys(t)

    jwtKey, err := keys.DeriveJWTSigningKey(seed)
    if err != nil {
        t.Fatalf("derive JWT key: %v", err)
    }

    // Use jwtKey... The bytes are byte-identical on every test run,
    // so any signature you produce is reproducible.
}
```

**What you get**:
- A 64-byte `[]byte` derived from the deterministic test passphrase
  + fixed salt via Argon2id (SDD-01-locked parameters).
- The same bytes every time, in every test, on every host.
- The first call in the process pays ~1.5 s for Argon2id; subsequent
  calls are ~0 (memoised).

**What you DON'T need to do**:
- No need to register a cleanup — the seed is a pure value.
- No need to supply a passphrase or salt — both are hardcoded.

---

## R-2 — Get a real on-disk vault populated with secrets

```go
package somepkg_test

import (
    "context"
    "testing"

    "github.com/mrz1836/hush/internal/testutil"
    "github.com/mrz1836/hush/internal/vault"
)

func TestSecretFetch_RoundTrip(t *testing.T) {
    path, vaultKey, _ := testutil.NewTestVault(t, map[string]string{
        "anthropic-prod": "sk-ant-xxxxxxxxxxxxxxxxx",
        "github-pat":     "ghp_yyyyyyyyyyyyyyyyy",
    })

    store, err := vault.Load(context.Background(), path, vaultKey)
    if err != nil {
        t.Fatalf("vault.Load: %v", err)
    }
    defer func() { _ = store.Destroy() }()

    sb, err := store.Get("anthropic-prod")
    if err != nil {
        t.Fatalf("store.Get: %v", err)
    }
    defer func() { _ = sb.Destroy() }()

    _ = sb.Use(func(b []byte) {
        if string(b) != "sk-ant-xxxxxxxxxxxxxxxxx" {
            t.Fatalf("round-trip mismatch")
        }
    })
}
```

**What you get**:
- `path`: absolute path to a real HUSH-format vault file inside
  `t.TempDir()`. The file mode is `0o600`, parent is `0o700` —
  satisfies `vault.Load`'s permission checks.
- `vaultKey`: `*securebytes.SecureBytes` (32 bytes, mlocked, type-
  redacted). Pass it directly to `vault.Load` / `vault.Save`.
- `cleanup`: a `func()` you can ignore; it has already been
  registered with `t.Cleanup`. Calling it explicitly is harmless
  (idempotent via `*SecureBytes.Destroy`).

**What happens at test exit**:
- The temp dir + vault file are removed.
- The vault key's mlocked buffer is zeroed and unlocked.
- Every value `*SecureBytes` constructed by the fixture is destroyed.

**Empty secrets map**:

```go
path, vaultKey, _ := testutil.NewTestVault(t, map[string]string{})
// path is a real, parseable vault file containing zero secrets.
// Useful for testing the empty-vault load path.
```

**Parallel subtests**:

```go
func TestParallelSubtests(t *testing.T) {
    for i := 0; i < 8; i++ {
        i := i
        t.Run("subtest", func(t *testing.T) {
            t.Parallel()
            path, _, _ := testutil.NewTestVault(t, map[string]string{
                "key": fmt.Sprintf("value-%d", i),
            })
            // Each subtest gets its own t.TempDir() — no sharing,
            // no clobbering, race-clean.
            _ = path
        })
    }
}
```

---

## R-3 — Inject a sentinel, run the code under test, assert no leak

```go
package somepkg_test

import (
    "bytes"
    "log/slog"
    "testing"

    "github.com/mrz1836/hush/internal/testutil"
)

func TestSomeHandler_NoSecretInLog(t *testing.T) {
    sentinel := testutil.SentinelSecret(7)
    // sentinel == "SECRET_SHOULD_NEVER_APPEAR_7"

    var buf bytes.Buffer
    logger := slog.New(slog.NewJSONHandler(&buf, nil))

    // Inject the sentinel into the secret value the handler sees:
    // (build whatever fixture your handler needs — a vault entry,
    // a JWT claim payload, an HTTP request body — with the sentinel
    // bytes as the secret value.)
    runHandler(logger, /* secret value: */ []byte(sentinel))

    // Assert the sentinel never appeared in the captured log output:
    testutil.AssertSentinelAbsent(t, sentinel, buf.String())
}
```

**What you get**:
- A guaranteed-recognisable, no-collision marker string.
- A failure message (if the assertion fails) that names the
  sentinel, reports the byte offset of the first match, and prints
  a 64-byte context window around the match.

**Distinct sentinels in one test**:

```go
sentinelA := testutil.SentinelSecret(1)
sentinelB := testutil.SentinelSecret(2)
// "SECRET_SHOULD_NEVER_APPEAR_1" and "SECRET_SHOULD_NEVER_APPEAR_2" — distinct.
testutil.AssertSentinelAbsent(t, sentinelA, capturedOutput)
testutil.AssertSentinelAbsent(t, sentinelB, capturedOutput)
```

**Empty haystack**:

```go
testutil.AssertSentinelAbsent(t, sentinel, "")
// No-op — empty haystack contains zero occurrences.
```

---

## R-4 — Stub the Discord approver with `ApproveAll`

```go
package somepkg_test

import (
    "context"
    "testing"

    "github.com/mrz1836/hush/internal/testutil"
)

// SUT (system under test) accepts the narrow Approver interface:
type Handler struct {
    approver testutil.Approver // or, post-SDD-11, internal/discord.Approver
}

func (h *Handler) ServeRequest(ctx context.Context, req testutil.ApprovalRequest) error {
    decision, err := h.approver.RequestApproval(ctx, req)
    if err != nil {
        return err
    }
    if decision == testutil.DecisionApprove {
        // approved path
    }
    return nil
}

func TestServeRequest_HappyPath(t *testing.T) {
    stub := testutil.NewDiscordStub(t)
    stub.ApproveAll = true

    h := &Handler{approver: stub}

    err := h.ServeRequest(context.Background(), testutil.ApprovalRequest{
        RequesterHost: "agent-1",
        Scopes:        []string{"anthropic-prod"},
        SessionType:   "interactive",
        TTL:           20 * time.Hour,
        MaxUses:       50,
    })
    if err != nil {
        t.Fatalf("ServeRequest: %v", err)
    }

    calls := stub.Calls()
    if len(calls) != 1 {
        t.Fatalf("want 1 recorded call, got %d", len(calls))
    }
    if calls[0].Decision != testutil.DecisionApprove {
        t.Fatalf("want Approve, got %v", calls[0].Decision)
    }
}
```

---

## R-5 — Stub the Discord approver with a programmed queue

```go
func TestServeRequest_DenialThenApproval(t *testing.T) {
    stub := testutil.NewDiscordStub(t)
    stub.Enqueue(testutil.DecisionDeny, testutil.DecisionApprove)

    h := &Handler{approver: stub}

    // First call: queued Deny.
    err := h.ServeRequest(context.Background(), reqA)
    if err != nil {
        t.Fatalf("call 1: unexpected err: %v", err)
    }
    // Second call: queued Approve.
    err = h.ServeRequest(context.Background(), reqB)
    if err != nil {
        t.Fatalf("call 2: unexpected err: %v", err)
    }

    calls := stub.Calls()
    if len(calls) != 2 {
        t.Fatalf("want 2 calls, got %d", len(calls))
    }
    if calls[0].Decision != testutil.DecisionDeny {
        t.Fatalf("call 0: want Deny, got %v", calls[0].Decision)
    }
    if calls[1].Decision != testutil.DecisionApprove {
        t.Fatalf("call 1: want Approve, got %v", calls[1].Decision)
    }
}
```

---

## R-6 — Compose a queue with `ApproveAll` for the tail

```go
func TestServeRequest_DenyThenAlwaysApprove(t *testing.T) {
    stub := testutil.NewDiscordStub(t)
    stub.Enqueue(testutil.DecisionDeny) // first call only
    stub.ApproveAll = true              // every subsequent call

    h := &Handler{approver: stub}

    // Call 1 — queued Deny.
    _ = h.ServeRequest(context.Background(), reqA)
    // Calls 2..N — ApproveAll fallback.
    for i := 0; i < 5; i++ {
        _ = h.ServeRequest(context.Background(), reqA)
    }

    calls := stub.Calls()
    if len(calls) != 6 {
        t.Fatalf("want 6 calls, got %d", len(calls))
    }
    if calls[0].Decision != testutil.DecisionDeny {
        t.Fatalf("call 0: want Deny, got %v", calls[0].Decision)
    }
    for i := 1; i < 6; i++ {
        if calls[i].Decision != testutil.DecisionApprove {
            t.Fatalf("call %d: want Approve, got %v", i, calls[i].Decision)
        }
    }
}
```

(This is the clarified composition — Spec Q1: "queue is consumed
first; `ApproveAll` covers every call after the queue is exhausted".)

---

## R-7 — Surface "unexpected call" as a loud test failure

```go
func TestServeRequest_UnexpectedExtraCall(t *testing.T) {
    stub := testutil.NewDiscordStub(t)
    stub.Enqueue(testutil.DecisionApprove) // expect EXACTLY one call
    // ApproveAll left at false (zero value) — no tail-default.

    h := &Handler{approver: stub}

    // Expected call.
    if err := h.ServeRequest(context.Background(), reqA); err != nil {
        t.Fatalf("expected call: %v", err)
    }

    // If the SUT erroneously calls the approver a second time,
    // the stub fails the test handle immediately with an
    // "unexpected call" message naming the request's identifying
    // attributes — Spec Q2 / FR-018a.
    //
    // Don't write the second call yourself; the failure is the
    // SUT's responsibility to surface. This recipe documents the
    // shape of the failure when it does happen.
}
```

(This is the clarified failure — Spec Q2: "fail the test handle
immediately with an 'unexpected call' message that names the
request's identifying attributes". The stub does NOT silently
default to deny, and does NOT block waiting for a future decision.)

---

## R-8 — Concurrent access (the stub is goroutine-safe)

```go
func TestStub_ConcurrentApprovals(t *testing.T) {
    stub := testutil.NewDiscordStub(t)
    stub.ApproveAll = true

    var wg sync.WaitGroup
    for i := 0; i < 100; i++ {
        wg.Add(1)
        go func(i int) {
            defer wg.Done()
            _, _ = stub.RequestApproval(context.Background(), testutil.ApprovalRequest{
                RequesterHost: fmt.Sprintf("agent-%d", i),
                Scopes:        []string{"scope"},
                SessionType:   "interactive",
                TTL:           time.Hour,
                MaxUses:       1,
            })
        }(i)
    }
    wg.Wait()

    if got := len(stub.Calls()); got != 100 {
        t.Fatalf("want 100 calls, got %d", got)
    }
}
```

Run with `go test -race` — the stub serialises through an internal
`sync.Mutex`; no data race is observable.

---

## R-9 — Anti-recipes (don't do this)

```go
// DON'T: import internal/testutil from a non-test file.
// internal/somepkg/foo.go (not foo_test.go):
import "github.com/mrz1836/hush/internal/testutil" // ❌ depguard fails magex lint

// DON'T: forget to take t as the first argument to your test
// helpers. Every helper in this package takes *testing.T first;
// follow that pattern in your own helpers, too.
func myTestHelper() (string, func()) { ... } // ❌ no t = no t.Cleanup = leak

// DON'T: try to share a vault file across two tests by reading
// the path from the first test and using it in the second. The
// temp dir is removed at the first test's exit; the path is
// invalid by the time the second test runs.

// DON'T: invoke the cleanup returned by NewTestVault when you've
// also let t.Cleanup register it. Both is fine (Destroy is
// idempotent) — but doing so just to "be safe" is noise. Trust
// the t.Cleanup registration; ignore the returned cleanup.

// DON'T: assert on the bytes inside the vault key. The key is a
// *securebytes.SecureBytes; its contents are not part of any
// public contract. Assert on round-trip correctness via vault.Load
// instead.

// DON'T: assume the deterministic seed will be the same after a
// SDD-01 parameter change. The seed is "deterministic for the
// SDD-01-locked Argon2id parameters". A future parameter change
// would change the seed — the test passphrase and salt are
// invariant, but the derived bytes depend on the KDF.
```

---

## R-10 — Where to look next

- `contracts/testutil-api.md` — locked exported signatures.
- `data-model.md` — entities, lifecycles, validation rules.
- `research.md` — design decisions and rejected alternatives.
- `spec.md` — WHAT requirements (FRs and SCs).
- `docs/sdd/SDD-04.md` — the chunk contract this plan implements.
- `docs/TESTING-STRATEGY.md` §5 — the canonical sentinel pattern
  this package's sentinel infrastructure implements.

---

**Phase 1 §quickstart: complete.** Every recipe maps to a
contract guarantee; every guarantee has a named test in
`contracts/testutil-api.md`.
