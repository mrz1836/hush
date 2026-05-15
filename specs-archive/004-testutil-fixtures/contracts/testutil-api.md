# `internal/testutil` — Exported API contract (locked at SDD-04)

**Package import path**: `github.com/mrz1836/hush/internal/testutil`

**Visibility**: test-only. Production source files (`*.go` not
ending in `_test.go`) MUST NOT import this package, directly or
transitively. Enforced by `golangci-lint`'s `depguard` rule AND by
`TestNoProductionImport` in this package's self-test suite.

**Stability**: this contract is the locked surface for SDD-04.
Adding a new exported identifier requires either (a) a follow-on
SDD chunk that names this package or (b) an amendment to
`docs/PACKAGE-MAP.md`. Removing or changing a signature is a
breaking change to every downstream test that uses it.

---

## §1 — Exported identifiers (12 total)

### Test-keys helper

```go
// NewTestKeys derives the deterministic 64-byte master seed used by every
// test in the project. The seed is derived from a hardcoded test-only
// passphrase ("hush-test-seed-NEVER-USE-IN-PROD") and a fixed 16-byte salt
// using internal/keys.DeriveMasterSeed (Argon2id; SDD-01-locked parameters).
//
// The same call returns byte-identical bytes on every host and on every
// invocation within a process. The returned slice is a defensive copy;
// mutating it does not affect the package-level cache.
//
// Argon2id derivation runs at most once per test process — the result is
// memoised behind sync.Once. The first NewTestKeys call costs ~1.5 s; every
// subsequent call costs ~64 bytes of allocation.
//
// The helper takes *testing.T as the first parameter for symmetry with the
// rest of the package and to allow t.Helper() to attribute future failures
// (e.g. an ErrSaltMissing if SDD-01's signature changes) to the caller's
// line.
func NewTestKeys(t *testing.T) (masterSeed []byte)
```

### Vault fixture

```go
// NewTestVault writes a real HUSH-format vault file inside t.TempDir()
// containing exactly the supplied secrets, returns the absolute file path,
// the vault encryption key as a Layer 5 secure container, and a cleanup
// function.
//
// The cleanup function is BOTH returned AND registered automatically via
// t.Cleanup; callers are not required to invoke it. Calling it explicitly
// is harmless because *securebytes.SecureBytes.Destroy is idempotent.
//
// The vault file's path is a descendant of t.TempDir(), so test parallelism
// is safe and the filesystem is cleaned up by the test framework.
//
// The vault key is derived deterministically via the same path as
// NewTestKeys + internal/keys.DeriveVaultEncKey. After cleanup, the key's
// underlying mlocked buffer is zeroed (vaultKey.Len() returns 0).
//
// The fixture also wraps every supplied secret value in *SecureBytes and
// registers their Destroy methods with t.Cleanup, so plaintext value bytes
// do not outlive the test.
//
// An empty secrets map produces a parseable vault file containing zero
// secrets, with a valid vault key and a registered cleanup.
func NewTestVault(t *testing.T, secrets map[string]string) (path string, vaultKey *securebytes.SecureBytes, cleanup func())
```

### Sentinel helper and assertion

```go
// SentinelSecret returns the canonical sentinel string for the given index:
// "SECRET_SHOULD_NEVER_APPEAR_<n>", where <n> is the decimal rendering of n.
//
// The string contains no whitespace and no punctuation that could cause a
// substring search to miss a match. It is the canonical no-leak marker for
// every redaction test in the project (Testing Strategy §5).
//
// Negative and zero indices are accepted; the resulting string remains
// recognisable and distinct from any positive-index sentinel.
func SentinelSecret(n int) string

// AssertSentinelAbsent fails the test handle if sentinel appears anywhere in
// haystack (substring match via strings.Contains). On failure, the message
// names the sentinel substring, reports the byte offset of the first match,
// and prints a 64-byte context window around the match (clamped to haystack
// bounds) so the operator can see the leak in situ.
//
// On success (sentinel absent), the helper is a no-op.
//
// The helper calls t.Helper() so failures surface at the caller's line.
func AssertSentinelAbsent(t *testing.T, sentinel, haystack string)
```

### Discord stub

```go
// Decision is the outcome the Approver returns for a request.
type Decision int

const (
    DecisionApprove     Decision = iota // request approved
    DecisionDeny                        // request denied
    DecisionApproveMute                 // request approved AND alert is muted for the session TTL
)

// ApprovalRequest carries the identifying attributes of an approval request.
// The shape is intentionally narrow; SDD-11 will widen it when the
// production Approver lands in internal/discord.
type ApprovalRequest struct {
    RequesterHost string        // hostname of the requester
    Scopes        []string      // requested secret-name scopes
    SessionType   string        // "interactive" or "supervisor"
    TTL           time.Duration // requested session TTL
    MaxUses       int           // requested per-session use cap; 0 if not applicable
}

// LimitDescription renders the TTL+MaxUses pair as a human-readable string,
// for use in failure messages.
func (r ApprovalRequest) LimitDescription() string

// ApprovalCall is one entry in the DiscordStub's recorded-calls list.
type ApprovalCall struct {
    Request  ApprovalRequest // a copy of the request as received
    Decision Decision        // the decision returned to the caller
    Err      error           // the error returned to the caller (nil on success paths; ErrUnexpectedCall on the failure path)
    Index    int             // zero-based position of this call in the recorded list
}

// Approver is the minimal local approval interface DiscordStub satisfies.
// SDD-11 will introduce a wider production Approver in internal/discord.
//
// Defined here (the consumer side) per Constitution IX: downstream test
// code that wants to inject DiscordStub depends on this narrow interface
// without dragging in internal/discord (which does not yet exist).
type Approver interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// DiscordStub is the programmable Approver substitute. ApproveAll is the
// public scenario knob; the per-call response queue is fed via Enqueue and
// inspected via Calls.
//
// Concurrency: every public method is safe for concurrent invocation; the
// stub serialises through an unexported sync.Mutex.
type DiscordStub struct {
    // ApproveAll is the tail-default for calls received after the queue is
    // exhausted. When true and the queue is empty, RequestApproval returns
    // DecisionApprove. When false and the queue is empty, RequestApproval
    // fails the test handle and returns DecisionDeny + ErrUnexpectedCall.
    ApproveAll bool

    // unexported fields:
    //   mu        sync.Mutex
    //   responses []Decision
    //   calls     []ApprovalCall
    //   t         *testing.T
}

// NewDiscordStub constructs a stub bound to t. The stub registers a t.Cleanup
// callback that drains its internal queue and recorded-calls list, so a
// leaked stub reference cannot carry state into a sibling test.
func NewDiscordStub(t *testing.T) *DiscordStub

// Enqueue appends decisions to the stub's response queue. Multiple calls are
// additive (extend the queue, do not replace it). Safe for concurrent use.
func (s *DiscordStub) Enqueue(decisions ...Decision)

// Calls returns a defensive copy of the recorded-calls list, in the order
// the calls were received. Safe for concurrent use.
func (s *DiscordStub) Calls() []ApprovalCall

// RequestApproval implements Approver. Decision tree:
//   1. If the queue is non-empty, pop the head and return it.
//   2. Else, if ApproveAll, return DecisionApprove.
//   3. Else, fail the test handle via t.Errorf and return
//      DecisionDeny + ErrUnexpectedCall.
//
// Every call (including the failure path) appends an ApprovalCall to the
// recorded-calls list before returning.
func (s *DiscordStub) RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)

// ErrUnexpectedCall is returned by RequestApproval when the queue is
// exhausted and ApproveAll is false. Compare with errors.Is.
var ErrUnexpectedCall = errors.New("hush/testutil: unexpected approval call")
```

### Total exported surface count

| Kind | Identifier | Count |
|------|-----------|-------|
| Function | `NewTestKeys` | 1 |
| Function | `NewTestVault` | 1 |
| Function | `SentinelSecret` | 1 |
| Function | `AssertSentinelAbsent` | 1 |
| Function | `NewDiscordStub` | 1 |
| Type | `Decision` | 1 |
| Constant | `DecisionApprove`, `DecisionDeny`, `DecisionApproveMute` | 3 |
| Type | `ApprovalRequest` | 1 |
| Type | `ApprovalCall` | 1 |
| Type | `Approver` (interface) | 1 |
| Type | `DiscordStub` (struct) | 1 |
| Variable | `ErrUnexpectedCall` | 1 |
| **Total** | | **14** |

(The chunk contract names eight; the contract here documents 14 because
the supporting types — `Decision` enum + 3 constants, `ApprovalRequest`,
`ErrUnexpectedCall` — are entailed by the locked behaviour and are
disclosed here for transparency, matching the SDD-03 precedent of
documenting the strict-superset surface.)

---

## §2 — Behavioural guarantees mapped to tests

| Guarantee | Spec FR / SC | Test name |
|-----------|--------------|-----------|
| Test-keys helper returns 64-byte master seed | FR-001 | `TestNewTestKeys_Length` |
| Test-keys helper is deterministic across calls in the same process | FR-003, SC-001 | `TestNewTestKeys_Determinism` |
| Test-keys helper passphrase contains "NEVER-USE-IN-PROD" substring | FR-002, SC-002 | `TestNewTestKeys_PassphraseProvenance` |
| Test-keys helper is safe under concurrent invocation from many subtests | FR-003 | `TestNewTestKeys_Concurrent` (race-clean) |
| Vault fixture round-trips supplied secrets via vault.Load | FR-005, SC-003 | `TestNewTestVault_RoundTrip` |
| Vault fixture path is a descendant of t.TempDir() | FR-006, SC-004 | `TestNewTestVault_PathContainment` |
| Vault fixture cleanup zeroes the vault key | FR-008, SC-005 | `TestNewTestVault_KeyZeroed` |
| Vault fixture parallel-subtest isolation | edge case (parallel subtest tree), SC-006 | `TestNewTestVault_ParallelSubtests` |
| Vault fixture accepts an empty secrets map | FR-009, SC-007 | `TestNewTestVault_EmptySecrets` |
| Vault fixture cleanup is idempotent (returned closure + registered Cleanup) | edge case (vault key cleanup ordering), R-04 | `TestNewTestVault_CleanupIdempotent` |
| SentinelSecret format stability | FR-010, SC-008 | `TestSentinelSecret_FormatStability` |
| SentinelSecret uniqueness across distinct indices | FR-011, SC-009 | `TestSentinelSecret_Uniqueness` |
| SentinelSecret accepts zero and negative indices without panic | edge case (sentinel index of zero or negative) | `TestSentinelSecret_EdgeIndices` |
| AssertSentinelAbsent positive case (sentinel absent) | FR-012, SC-010 | `TestAssertSentinelAbsent_Absent` |
| AssertSentinelAbsent negative case (sentinel present) | FR-013, SC-011 | `TestAssertSentinelAbsent_Present` |
| AssertSentinelAbsent against an empty haystack | edge case (empty haystack) | `TestAssertSentinelAbsent_EmptyHaystack` |
| DiscordStub ApproveAll path | FR-015, SC-012 | `TestDiscordStub_ApproveAll` |
| DiscordStub queue path returns decisions in order | FR-016, SC-013 | `TestDiscordStub_Queue` |
| DiscordStub queue + ApproveAll composition (queue drains first) | clarification 1, FR-016 | `TestDiscordStub_QueueThenApproveAll` |
| DiscordStub records every call with identifying attributes | FR-017, SC-014 | `TestDiscordStub_CallRecording` |
| DiscordStub thread-safety under -race | FR-018, SC-015 | `TestDiscordStub_Concurrent` (100 goroutines) |
| DiscordStub fails the test handle on unexpected call (queue empty + !ApproveAll) | clarification 2, FR-018a | `TestDiscordStub_UnexpectedCall` (uses a captured sub-`testing.T`) |
| Stub never opens a network socket | FR-019, SC-016 | `TestDiscordStub_NoNetwork` (asserts no `net.Dial*` is reachable from any code path) |
| Every fixture registers t.Cleanup automatically | FR-020, FR-021, SC-017 | implicit in every other test (a leak would fail a sibling test); explicit assertion in `TestNoLeakAcrossTests` |
| No file is written outside t.TempDir() | FR-022, SC-018 | `TestNoFileOutsideTempDir` (asserts no `os.CreateTemp` or `os.MkdirTemp` use beyond t.TempDir-derived paths) |
| No state survives between sequential tests | FR-023, SC-019 | `TestNoLeakAcrossTests` (two sequential tests; the second observes nothing from the first) |
| Package contains no init() | FR-024, SC-020 | `TestNoInit` (AST walk) |
| Package is test-only — no production import | FR-025, SC-021 | `TestNoProductionImport` (AST walk over internal/) |

---

## §3 — Anti-contracts (MUST NOT)

The package and its helpers MUST NOT:

- Contain an `init()` function.
- Hold any mutable package-level state other than the documented
  `sync.Once`-guarded master seed cache (one `*sync.Once` + one
  `[]byte` + the immutable `testPassphrase` and `testSalt` slices
  + the sentinel `ErrUnexpectedCall`). The lint allowlist enforced
  by `TestPackageGlobals` makes this constraint executable.
- Open any network socket, dial any remote endpoint, or import
  any package that does (e.g. `net`, `net/http`, Discord SDKs).
  Verified by `TestDiscordStub_NoNetwork` and by the
  `internal/testutil` import declaration list.
- Create any file outside `t.TempDir()`.
- Persist any state observable to a subsequent sequential test in
  the same package (queue, recorded calls, file, key bytes).
- Be importable from any production source file (`*.go` not
  `*_test.go`). Enforced by `depguard` lint AND by
  `TestNoProductionImport`.
- Implement any production behaviour beyond a test substitute.
  In particular, `DiscordStub` MUST NOT pretend to be the production
  Discord approver — it satisfies only the narrow `Approver`
  interface this package defines.
- Log secret values. The package emits no `log/slog` records of its
  own.

---

## §4 — Dependency declaration

**Direct stdlib imports** (production files):

- `context` — for the ctx parameter of `Approver.RequestApproval`
  and the ctx passed into `keys.DeriveMasterSeed`.
- `errors` — for the `ErrUnexpectedCall` sentinel.
- `fmt` — for `SentinelSecret`'s `Sprintf` and the error messages.
- `os` — for the `t.TempDir()` filesystem interactions (transitively
  via `path/filepath`).
- `path/filepath` — for `filepath.Join(t.TempDir(), "test.vault")`.
- `strings` — for `strings.Contains` / `strings.Index` in
  `AssertSentinelAbsent`.
- `sync` — for the `sync.Once` master-seed cache and the `sync.Mutex`
  in `DiscordStub`.
- `testing` — for `*testing.T` parameters and `t.Cleanup` registration.
- `time` — for the `time.Duration` field of `ApprovalRequest`.

**Direct intra-repo imports** (production files):

- `github.com/mrz1836/hush/internal/keys` — `DeriveMasterSeed`,
  `DeriveVaultEncKey`.
- `github.com/mrz1836/hush/internal/vault` — `Save`, `Secret`.
- `github.com/mrz1836/hush/internal/vault/securebytes` — `New`,
  `*SecureBytes`, `Destroy`.

**Direct stdlib imports** (test files only):

- `bytes`, `errors`, `fmt`, `go/ast`, `go/parser`, `go/token`,
  `io/fs`, `os`, `path/filepath`, `strings`, `sync`, `sync/atomic`,
  `testing`, `time`.
- `github.com/mrz1836/hush/internal/vault` — for the round-trip
  `vault.Load` call.
- `github.com/mrz1836/hush/internal/vault/securebytes` — for the
  post-cleanup `Len()` assertion.

**No new go.mod entry is added** in this chunk. Constitution XI
satisfied trivially.

---

## §5 — Versioning

This contract is the v1.0.0 surface for `internal/testutil`. Any
subsequent change to the exported surface follows
`docs/PACKAGE-MAP.md`'s amendment process (the "locked at SDD-04"
banner pins this version). SDD-11 is expected to introduce the
production `internal/discord.Approver`; if its shape diverges from
the local `Approver` here, this package's `Approver` may be
deprecated in favour of an alias, but never silently changed.
