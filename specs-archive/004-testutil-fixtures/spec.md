# Feature Specification: Test Fixtures, Sentinel Helpers, and Programmable Discord Approval Stub

**Feature Branch**: `004-testutil-fixtures`
**Created**: 2026-04-27
**Status**: Draft
**Input**: User description: "internal/testutil: provide deterministic test keys, a programmable Discord approval stub, a sentinel-string helper plus AssertSentinelAbsent, and a temp-dir-scoped vault fixture; every helper is leak-safe via t.Cleanup and never persists state between tests"

## Clarifications

### Session 2026-04-27

- Q: When the Discord stub has `ApproveAll = true` AND a non-empty programmed-decisions queue, which one wins? → A: The queue is consumed first; `ApproveAll` covers every call after the queue is exhausted.
- Q: When `ApproveAll = false` AND the programmed-decisions queue is fully consumed, what does the stub do on the next approval call? → A: Fail the test handle immediately with an "unexpected call" message that names the request's identifying attributes.

## Overview

The `internal/testutil` package is the project's shared test harness.
Every other package inside `internal/` that writes tests against the
vault, the keys derivation, the request handler, the supervisor state
machine, the Discord approval flow, or the audit emitter obtains its
fixtures from this package and never builds its own. The package has
one job: give every downstream test a uniform, leak-safe, fully
deterministic set of primitives so the project can meet its
constitutional coverage bar (Principle VIII — TDD plus 100% coverage
on security-critical packages) without each test author having to
re-invent — and accidentally weaken — the same fixtures.

The harness ships five primitives. A deterministic test-keys helper
derives a master seed from a hardcoded test-only passphrase and a
fixed salt, so any test that needs key material gets the same bytes
on every run. A temp-dir-scoped vault fixture writes a real on-disk
vault file (in the project's HUSH file format) inside the test's
temporary directory, returns the file path, the vault key in a Layer
5 secure container, and a cleanup function that zeroes the key. A
sentinel-string helper produces a recognisable, deliberately
unmistakable byte sequence used by every redaction or no-leak test
in the project. A companion assertion fails the test if that
sentinel appears anywhere in a supplied haystack of captured output.
A programmable Discord approval stub satisfies the minimal approval
interface that downstream packages depend on, supporting both an
auto-approve-all scenario shape and a per-call programmable response
queue, while recording every call it receives for post-test
inspection.

The package's acceptance criterion is **indirect**: it underpins
**AC-9** (coverage and fuzz targets in `docs/SPEC.md`) by being the
load-bearing dependency every other chunk's test coverage relies on.
Its release-gate contribution is the leak-safety invariant — every
fixture this package ships registers its own per-test cleanup
callback so that missing cleanup is a loud test failure rather than
a silent leak between tests, and no fixture writes any byte outside
the test's temporary directory.

The package is a **test-only dependency**. It MUST NOT be importable
by any production code path inside the binary; only test files
(`*_test.go`) under `internal/` may depend on it. This is a
constitutional consequence of Principle IX (no globals, no `init()`,
explicit dependency wiring): a test harness leaking into production
is precisely the class of mistake the linter set is configured to
catch.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — A test obtains deterministic test keys with no setup (Priority: P1)

A downstream test that exercises a key-derivation, signing, or
encryption code path needs a master seed it can hand to the code
under test. The author MUST be able to call a single helper with the
test handle and receive a master seed of the project's standard
length, derived from a known test-only passphrase plus a fixed salt,
without supplying either themselves. Two invocations of that helper
in two different tests MUST produce byte-identical output, so any
key-material assertion in the project is repeatable across runs and
across machines.

**Why this priority**: Determinism is the precondition for every
other test in the project that touches key material. A flaky seed
would manifest as flaky JWT issuance, flaky ECIES round-trips, and
flaky vault encryption — every higher layer would inherit the
flakiness. The constitutional 100%-on-security-critical bar
(Principle VIII) is unreachable if the seed is non-deterministic.

**Independent Test**: A test invokes the test-keys helper twice in a
row, captures the two byte sequences it returns, and asserts they
are byte-identical. A second test invokes the helper, captures the
sequence, then re-invokes it from a fresh subtest and asserts the
sequence is the same.

**Acceptance Scenarios**:

1. **Given** a fresh test handle,
   **When** the caller invokes the test-keys helper,
   **Then** the helper returns a non-empty master seed of the
   project's standard length without requiring the caller to
   supply a passphrase or salt.
2. **Given** the same caller invoking the test-keys helper twice
   in the same test,
   **When** the two return values are compared byte-for-byte,
   **Then** they are byte-identical.
3. **Given** two different tests in the same package each invoking
   the test-keys helper,
   **When** the two return values are compared byte-for-byte,
   **Then** they are byte-identical.
4. **Given** the test-keys helper as imported by any caller,
   **When** the helper's underlying passphrase and salt are
   inspected,
   **Then** they are a hardcoded test-only literal that names
   itself as test-only (the literal MUST contain a substring
   identifying the seed as never-for-production), guaranteeing
   the master seed cannot be confused with a real key.

---

### User Story 2 — A test obtains a real on-disk vault populated with chosen secrets (Priority: P1)

A test that exercises the vault load path, the SIGHUP reload path,
the request handler, or any code that needs a populated vault on
disk MUST be able to call a single helper with the test handle and
a map of secret name → secret value, and receive: the absolute path
to a real HUSH-format vault file containing exactly those secrets;
the vault key (a Layer 5 secure container) used to encrypt it; and
a cleanup function. The vault file MUST live inside the test's
temporary directory so that test parallelism is safe and the
filesystem is left clean. The cleanup function MUST zero the bytes
of the vault key, so a leaking reference cannot expose key material
after the test ends.

**Why this priority**: The vault is the project's single largest
test surface (Constitution Principle III, Layer 7 — the project's
custom file format). Every test that proves a vault property —
round-trip, atomic write, SIGHUP reload, fuzz-decode, audit-chain
append — needs a real on-disk vault to point at. Building that
fixture by hand in every test invites drift: one test author would
omit the cleanup, one would skip key zeroing, one would write the
file outside the temp dir. A single shared fixture removes that
class of mistake.

**Independent Test**: A test calls the vault fixture with a map of
two named secrets, captures the returned path, opens the file using
the vault load path under test, asserts the two secrets are present
and decrypt to the supplied values, then asserts the file resides
inside the test's temporary directory. A second test invokes the
fixture, captures a reference to the returned vault key's bytes,
runs the cleanup callback, and asserts the key bytes have been
overwritten with zeros.

**Acceptance Scenarios**:

1. **Given** the vault fixture invoked with a non-empty map of
   secret name → secret value,
   **When** the caller opens the returned file path with the
   project's vault load path,
   **Then** every supplied secret is present in the loaded vault
   and decrypts byte-for-byte to the supplied value.
2. **Given** the vault fixture invoked with any input,
   **When** the resolved file path is inspected,
   **Then** the path is a descendant of the test handle's
   temporary directory.
3. **Given** the vault fixture's cleanup callback,
   **When** the callback is invoked (either explicitly or by the
   test framework's per-test cleanup hook),
   **Then** the bytes that previously held the vault key are
   overwritten with zeros and the temporary directory containing
   the vault file is removed.
4. **Given** the vault fixture invoked from inside a parallel
   subtest,
   **When** several sibling subtests invoke the fixture
   concurrently,
   **Then** each subtest receives a path inside its own
   temporary directory and no subtest observes another subtest's
   vault file.

---

### User Story 3 — A test obtains a recognisable sentinel and asserts it never appears in captured output (Priority: P1)

Every redaction test, no-leak test, log-format test, and audit-output
test in the project depends on the same pattern: inject a value the
production code MUST never expose verbatim into the system, run the
code path, capture the output, and assert the value is absent. The
package MUST ship a sentinel-string helper that takes an integer
discriminator and returns a recognisable literal byte sequence that
no upstream library, log line, or accidental constant could collide
with. The package MUST also ship a companion assertion that fails
the supplied test handle when the sentinel substring appears
anywhere in the supplied haystack, and that prints enough context
on failure to locate the leak in the haystack.

**Why this priority**: The sentinel-pattern test (Testing Strategy
§5) is the project's primary instrument for proving the
constitutional no-leak guarantee in Principle X. If two test
authors invent two different sentinel formats, one of them will
collide with a real string somewhere — a UUID, an attribute key,
the literal text "secret" — and produce a false positive that
trains the team to disable the assertion. A single shared sentinel
factory removes that class of mistake at the source.

**Independent Test**: A positive-case test invokes the sentinel
helper, asks the assertion to check a haystack that does not
contain the sentinel, and asserts the test handle reports no
failure. A negative-case test invokes the sentinel helper, asks
the assertion to check a haystack that does contain the sentinel,
and asserts the test handle reports a failure naming the sentinel
substring.

**Acceptance Scenarios**:

1. **Given** the sentinel helper invoked with an integer index,
   **When** the returned string is inspected,
   **Then** the string is a literal byte sequence containing a
   recognisable, unmistakable marker substring (for example,
   `SECRET_SHOULD_NEVER_APPEAR_<n>`) that names the index, has no
   embedded whitespace or punctuation that could cause a substring
   search to miss a match, and is unlikely to collide with any
   non-test text in the project.
2. **Given** two invocations of the sentinel helper with two
   distinct integer indices,
   **When** the two returned strings are compared,
   **Then** they are distinct strings, each containing its own
   integer index in the marker substring.
3. **Given** the sentinel-absent assertion invoked with a haystack
   containing zero occurrences of the sentinel substring,
   **When** the assertion runs against a fresh test handle,
   **Then** the test handle reports no failure.
4. **Given** the sentinel-absent assertion invoked with a haystack
   containing at least one occurrence of the sentinel substring,
   **When** the assertion runs against a fresh test handle,
   **Then** the test handle reports a failure whose message
   identifies the sentinel substring and locates at least one
   match within the haystack so the operator can see the leak.

---

### User Story 4 — A test programs a Discord approval stub with a specific decision sequence and inspects the calls it received (Priority: P1)

Every test that exercises a code path requiring Discord approval
(the request flow, the supervisor lifecycle, the watchdog escalation
path) MUST be able to substitute a stub for the real Discord
approver, configure the stub's behaviour declaratively for that
test, and inspect the calls the code under test issued. The stub
MUST support **two scenario shapes**, exclusive of each other or
composed: an auto-approve-all mode in which every approval request
is approved; and a programmable per-call response queue in which the
test author pre-loads a sequence of decisions (approve, deny, mute)
that the stub returns in order. The stub MUST record every approval
call it receives — at minimum, the request's identifying attributes
(requester host, requested scopes, session type, TTL/use limit) —
so the test can assert after the fact that the code under test
issued exactly the calls expected.

**Why this priority**: Discord is the project's only human-in-the-
loop gate (Constitution Principle II); every test that exercises the
approval boundary needs a deterministic, programmable substitute
because (a) hitting real Discord from CI is a non-starter (network
flake, rate limits, leaking test traffic into the operator's
phone), and (b) the variety of approval-flow scenarios — first
approval, denial, mute-and-approve, queue-of-mixed-decisions — is
beyond what an auto-approve-only stub can express. Without a
programmable queue, the supervisor's denial path and the watchdog's
escalation path cannot be tested at all.

**Independent Test**: A test instantiates the Discord stub in
auto-approve mode, drives one approval request through the code
under test, asserts the request was approved, and then asserts the
stub's recorded-calls list contains exactly one entry whose
identifying attributes match the request the code issued. A second
test instantiates the stub with a programmed three-decision queue
(approve, deny, approve), drives three sequential approval requests,
and asserts each request received the corresponding decision in
order. A third test instantiates the stub and asserts that no
helper in the package ever attempts to dial, connect to, or read
from any external network endpoint.

**Acceptance Scenarios**:

1. **Given** the Discord stub instantiated with the auto-approve-all
   mode enabled,
   **When** the code under test issues an approval request through
   the stub,
   **Then** the stub returns an approve decision for that request.
2. **Given** the Discord stub instantiated with a non-empty
   programmed sequence of decisions,
   **When** the code under test issues approval requests in order,
   **Then** the stub returns each programmed decision in the order
   the sequence was pre-loaded.
3. **Given** the Discord stub at any point during a test,
   **When** the test inspects the stub's recorded-calls list,
   **Then** the list contains one entry per approval request the
   code under test issued, in the order issued, each entry
   carrying the identifying attributes of the request (requester
   host, requested scopes, session type, TTL/use limit).
4. **Given** the Discord stub used from many goroutines
   concurrently,
   **When** the goroutines issue approval requests in parallel,
   **Then** the stub serialises its internal state changes such
   that no recorded-call entry is corrupted, no decision is
   returned to the wrong caller, and no data race is observable
   under the race detector.
5. **Given** the Discord stub at any point during a test,
   **When** any helper in this package is invoked,
   **Then** no helper attempts to dial, connect to, or otherwise
   contact any network endpoint, and no helper requires network
   connectivity to succeed.

---

### User Story 5 — Every fixture is leak-safe via the test framework's per-test cleanup hook (Priority: P1)

Every helper this package ships MUST register its own per-test
cleanup callback against the test handle the caller hands in. The
caller MUST NOT need to remember to call a cleanup function, MUST
NOT need to write `defer` statements around helper invocations, and
MUST NOT need to know which fixtures have side-effects to release.
A fixture that fails to register its cleanup MUST be detectable as
a test failure rather than a silent leak that surfaces hours later
as a flaky neighbouring test.

**Why this priority**: A test harness whose helpers leak temp dirs,
key bytes, mutex locks, or recorded state across tests is worse
than no harness at all — it produces flaky tests that are hard to
diagnose (the failing test is rarely the one that leaked), and it
trains the project to ignore intermittent failures. The
constitutional 100%-on-security-critical bar (Principle VIII) and
the redaction guarantee (Principle X) both rely on every test
running in clean state. The leak-safety invariant is what makes
that achievable.

**Independent Test**: A test invokes every fixture this package
ships in turn, allows the test framework's per-test cleanup hook to
run, then in a fresh subsequent test asserts that no temp dir
created by the previous test still exists, no vault key bytes still
hold their pre-cleanup contents, and no Discord stub recorded state
survives. A second test asserts that none of this package's
helpers expose a manual cleanup function the caller is required to
invoke — every helper either returns no cleanup or returns one
that has already been registered for automatic invocation.

**Acceptance Scenarios**:

1. **Given** a fixture from this package invoked inside a test,
   **When** the test exits (pass, fail, or skip),
   **Then** the fixture's cleanup runs automatically without the
   caller invoking it explicitly.
2. **Given** the vault fixture invoked inside a test,
   **When** the test exits,
   **Then** the temporary directory containing the vault file is
   removed and the bytes of the returned vault key are
   overwritten with zeros.
3. **Given** the Discord stub instantiated inside a test,
   **When** the test exits,
   **Then** the stub's internal state (recorded calls, pre-loaded
   decision queue) is no longer reachable from any subsequent
   test in the same package.
4. **Given** a hypothetical fixture variant that fails to register
   its cleanup,
   **When** the package's own self-tests run,
   **Then** the missing cleanup is surfaced as a test failure in
   this package, not as a silent leak observable only in a
   downstream package.

---

### User Story 6 — The package is a test-only dependency and never persists state between tests (Priority: P2)

The package MUST be importable only from test files inside the
project's `internal/` tree. Production code MUST NOT depend on it,
either directly or transitively. The package MUST NOT hold any
mutable package-level state that survives a test's exit; every
helper's state MUST be scoped to the test handle that requested it
or to a value the helper returns to that test. The package MUST
NOT contain an `init()` function and MUST NOT install any
side-effect at import time (no global writers, no global registries,
no shared mutexes outside helper-local scope).

**Why this priority**: Constitution Principle IX bans globals,
`init()`, and mutable package-level state across the project; the
test harness inherits that ban. Principle I forbids any production
code path from depending on test-only artefacts (a test harness on
the agent is, by definition, a file at rest the malware threat
model would scan). Together they make the test-only constraint a
constitutional invariant, not a stylistic preference.

**Independent Test**: A repository-wide search asserts that no
production source file (any `*.go` outside `*_test.go`) imports
this package. A second test inspects the package source and
asserts there is no `init()` function and no exported or
unexported package-level mutable variable. A third test invokes a
helper twice from two sequential top-level tests and asserts that
no state set in the first test is observable in the second.

**Acceptance Scenarios**:

1. **Given** the project's full source tree,
   **When** a search is run for production files (any `*.go`
   that is not a `*_test.go`) importing this package,
   **Then** the search returns zero results.
2. **Given** the package source as imported by any caller,
   **When** the package is loaded,
   **Then** no `init()` function runs and no package-level
   mutable variable is observable.
3. **Given** any helper in this package invoked from a first
   test,
   **When** the same helper is invoked from a second sequential
   test in the same package,
   **Then** the second invocation observes none of the first
   invocation's internal state.

---

### Edge Cases

- **Empty secrets map**: The vault fixture invoked with an empty
  map of secret name → value MUST still produce a valid HUSH-format
  vault file (containing zero secrets), a valid vault key, and a
  cleanup callback. Downstream tests that exercise the empty-vault
  load path depend on this.
- **Sentinel index of zero or negative**: The sentinel helper
  invoked with index 0 MUST return a well-formed sentinel string
  (the index renders as `0` in the marker substring). Negative
  indices MUST not panic; the resulting string remains
  recognisable and distinct from any positive-index sentinel.
- **Sentinel-absent assertion against an empty haystack**: An empty
  haystack contains zero occurrences of any sentinel; the
  assertion MUST report no failure.
- **Vault fixture invoked from a parallel subtest tree**: Each
  subtest's call MUST receive its own temporary directory and its
  own vault file path; no two parallel subtests share filesystem
  state.
- **Discord stub with both auto-approve enabled and a programmed
  queue**: When both `ApproveAll = true` and a non-empty
  programmed-decisions queue are configured, the stub MUST consume
  the queue first (one decision per call, in pre-loaded order) and
  fall back to the `ApproveAll` approve decision for every call
  received after the queue is exhausted. This composition lets the
  test author program the interesting prefix and let the tail
  auto-approve.
- **Discord stub with auto-approve disabled and queue exhausted**:
  When the test author has set `ApproveAll = false` and the
  programmed queue has been fully consumed, the stub MUST fail
  the test handle immediately on the next approval call with an
  "unexpected call" message that names the request's identifying
  attributes (requester host, requested scopes, session type,
  TTL/use limit). The stub MUST NOT silently default to deny and
  MUST NOT block waiting for a future decision; the missing
  programmed decision is a test-setup defect and surfaces loudly
  at the call site.
- **Test-keys helper invoked concurrently from many subtests**:
  The helper MUST be safe to call from many goroutines in parallel
  subtests. Every concurrent call MUST observe the same byte
  sequence; no caller MUST see partial or interleaved output.
- **Vault key cleanup runs after the cleanup of the temporary
  directory**: The cleanup callback registered by the vault
  fixture MUST zero the vault key bytes regardless of whether the
  temporary-directory removal callback has already run. The two
  cleanups MUST not depend on each other's ordering.
- **Multiple invocations of the same fixture inside one test**:
  Calling the vault fixture twice inside one test MUST produce two
  distinct vault files in two distinct paths within the same
  temporary directory; both cleanups MUST register independently.
- **A helper's cleanup callback panics**: Out of scope for this
  package's contract — the cleanup callbacks the package
  registers MUST not themselves panic on any reachable input. A
  bug that causes one to panic is a defect in this package's
  implementation, not a documented behaviour.

## Requirements *(mandatory)*

### Functional Requirements

**Test-keys helper**

- **FR-001**: The package MUST expose a test-keys helper that
  accepts the test handle as its first parameter and returns a
  master seed of the project's standard length without requiring
  any other input.
- **FR-002**: The test-keys helper MUST derive its output from a
  hardcoded test-only passphrase whose source literal contains a
  substring identifying it as never-for-production
  (`hush-test-seed-NEVER-USE-IN-PROD`) and from a fixed salt of
  the project's standard salt length. The passphrase and salt
  MUST be invariant across runs and across machines.
- **FR-003**: Two invocations of the test-keys helper from any
  two callers MUST return byte-identical master seeds. The helper
  MUST be safe for concurrent invocation from parallel subtests.

**Vault fixture**

- **FR-004**: The package MUST expose a vault fixture that
  accepts the test handle as its first parameter and a map of
  secret name → secret value as its second parameter. The
  fixture MUST return: the absolute path to a vault file that
  exists on disk; the vault key as a Layer 5 secure container
  (the `SecureBytes` primitive established by SDD-02); and a
  cleanup callback.
- **FR-005**: The vault file written by the fixture MUST be a
  real, parseable HUSH-format file (the file format established
  by SDD-03) containing exactly the supplied secrets and
  decryptable with the returned vault key.
- **FR-006**: The vault file's path MUST be a descendant of the
  test handle's temporary directory. No byte MUST be written
  outside that temporary directory.
- **FR-007**: The cleanup callback returned by the vault fixture
  MUST be registered against the test handle's per-test cleanup
  hook before the fixture returns. The caller MUST NOT need to
  invoke the cleanup explicitly.
- **FR-008**: When the cleanup callback runs, the bytes of the
  vault key (the secure container's underlying memory) MUST be
  overwritten with zeros. This requirement MUST hold regardless
  of whether the temporary-directory removal callback has already
  run.
- **FR-009**: The vault fixture MUST accept an empty secrets map
  and produce a vault file containing zero secrets, with a valid
  vault key and a registered cleanup callback.

**Sentinel helper and assertion**

- **FR-010**: The package MUST expose a sentinel helper that
  accepts an integer index and returns a string. The returned
  string MUST contain a recognisable marker substring that names
  the integer index (for example, `SECRET_SHOULD_NEVER_APPEAR_<n>`)
  and MUST NOT contain any whitespace, punctuation, or character
  that could cause a substring search to miss a match.
- **FR-011**: For any two distinct integer indices, the sentinel
  helper MUST return two distinct strings, each containing its
  own index in the marker substring.
- **FR-012**: The package MUST expose a sentinel-absent assertion
  that accepts the test handle, the sentinel substring, and a
  haystack string. When the haystack contains zero occurrences
  of the sentinel substring, the assertion MUST report no
  failure on the test handle.
- **FR-013**: When the haystack contains at least one occurrence
  of the sentinel substring, the assertion MUST report a failure
  on the test handle. The failure message MUST identify the
  sentinel substring and locate at least one match within the
  haystack so the operator can see the leak.

**Discord approval stub**

- **FR-014**: The package MUST expose a Discord approval stub
  satisfying the minimal approval interface that downstream
  packages depend on. The stub's behaviour MUST be configurable
  per-test through two scenario shapes: an auto-approve-all
  mode, and a programmable per-call response queue of decisions
  (approve, deny, approve-and-mute).
- **FR-015**: When the auto-approve-all mode is enabled and the
  programmed queue is empty, every approval request the stub
  receives MUST return an approve decision.
- **FR-016**: When a non-empty programmed queue is configured,
  the stub MUST return decisions from the queue in the order
  the queue was pre-loaded; each call consumes one decision.
  When `ApproveAll = true` and a non-empty programmed queue are
  configured simultaneously, the queue MUST be consumed first
  (one decision per call, in pre-loaded order); after the queue
  is exhausted, `ApproveAll` covers every subsequent call.
- **FR-017**: The stub MUST record every approval request it
  receives in an inspectable list of recorded calls. Each
  recorded entry MUST carry the identifying attributes of the
  request (requester host, requested scopes, session type,
  TTL/use limit) and the order in which it was received.
- **FR-018**: The stub MUST be safe for concurrent invocation
  from many goroutines. Concurrent approval requests MUST NOT
  produce a corrupted recorded-calls list, MUST NOT return a
  decision to the wrong caller, and MUST NOT exhibit any data
  race observable under the race detector.
- **FR-018a**: When `ApproveAll = false` and the programmed
  queue is empty (either never populated or fully consumed), the
  next approval call the stub receives MUST fail the test handle
  immediately with an "unexpected call" message that names the
  request's identifying attributes (requester host, requested
  scopes, session type, TTL/use limit). The stub MUST NOT
  silently default to deny and MUST NOT block waiting for a
  future decision.
- **FR-019**: The stub MUST NOT dial, connect to, or otherwise
  contact any network endpoint. No helper in this package MUST
  require network connectivity to succeed.

**Leak-safety and isolation**

- **FR-020**: Every helper this package exposes that creates a
  resource (temporary file, allocated key bytes, internal mutex,
  recorded state) MUST register a cleanup callback against the
  test handle's per-test cleanup hook before the helper returns.
  The caller MUST NOT need to invoke any returned cleanup
  explicitly.
- **FR-021**: Every cleanup callback registered by this package's
  helpers MUST run automatically when the test handle's per-test
  cleanup hook fires (test pass, fail, or skip).
- **FR-022**: No helper in this package MUST create a file
  outside the test handle's temporary directory.
- **FR-023**: No helper in this package MUST persist any state
  observable to a subsequent sequential test in the same package.

**Test-only constraint**

- **FR-024**: The package MUST NOT contain an `init()` function.
  It MUST NOT install any side-effect at import time (no global
  writer registration, no global registry mutation, no
  package-level mutable variable observable to callers).
- **FR-025**: The package MUST be importable only from test files
  (`*_test.go`) inside the project's `internal/` tree. No
  production source file MUST import this package, directly or
  transitively. The constraint MUST be enforceable by repository
  search and by linter configuration.

### Key Entities

- **Test-keys helper** — A function that returns a deterministic
  master seed derived from a hardcoded test-only passphrase and
  a fixed salt. Carries no per-invocation state; the same call
  produces the same bytes every time, on every machine.
- **Vault fixture** — A composite of three returned values: an
  on-disk HUSH-format vault file path, the vault key in a Layer
  5 secure container, and a cleanup callback registered with
  the test handle. Scoped to a single test handle and its
  temporary directory.
- **Sentinel helper** — A function that maps an integer
  discriminator to a recognisable, unmistakable byte sequence
  no production string is permitted to collide with. Stateless
  and deterministic.
- **Sentinel-absent assertion** — A function that fails the
  supplied test handle when the supplied sentinel substring
  appears in the supplied haystack. Carries no state of its
  own; its only effect is on the test handle.
- **Discord approval stub** — A composite of behaviour
  configuration (auto-approve mode, programmed response queue),
  an inspectable list of recorded approval calls, and the
  approval-interface implementation that consumes the
  configuration when invoked. Scoped to a single test handle
  and serialised internally for concurrent use.
- **Approval-call record** — One entry in the Discord stub's
  recorded-calls list. Carries the identifying attributes of a
  single approval request (requester host, requested scopes,
  session type, TTL/use limit) and its position in the order of
  received calls.
- **Approval interface (minimal local form)** — The narrow
  approval-related contract this package's stub satisfies. Its
  only purpose is to let downstream tests substitute the stub
  for the real Discord approver before SDD-11 introduces the
  full interface in `internal/discord`.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001 (Test-keys determinism)**: Two sequential invocations
  of the test-keys helper, in any combination of test handles,
  produce byte-identical master seeds.
- **SC-002 (Test-keys provenance)**: The test-keys helper's
  underlying passphrase literal contains a recognisable
  never-for-production substring (specifically
  `hush-test-seed-NEVER-USE-IN-PROD`), so the master seed
  cannot be confused with a real key during a code review.
- **SC-003 (Vault fixture round-trip)**: A vault fixture
  invocation with a non-empty secrets map produces a file that,
  when opened with the project's vault load path and the
  returned vault key, decrypts every supplied secret to its
  exact supplied value.
- **SC-004 (Vault fixture path containment)**: For every vault
  fixture invocation, the returned file path is a descendant of
  the test handle's temporary directory.
- **SC-005 (Vault key zeroed on cleanup)**: After the vault
  fixture's cleanup callback runs, the underlying bytes of the
  returned vault key are observably zero.
- **SC-006 (Vault fixture parallel safety)**: Many parallel
  subtests invoking the vault fixture concurrently each receive
  a distinct path inside their own temporary directory; no
  subtest observes another subtest's vault file.
- **SC-007 (Empty-secrets vault)**: A vault fixture invocation
  with an empty secrets map produces a parseable vault file
  containing zero secrets, with a valid vault key and a
  registered cleanup.
- **SC-008 (Sentinel format stability)**: For every integer
  index, the sentinel helper returns a string containing the
  marker substring `SECRET_SHOULD_NEVER_APPEAR_` immediately
  followed by the integer index, with no embedded whitespace
  or punctuation.
- **SC-009 (Sentinel uniqueness)**: For any two distinct integer
  indices, the sentinel helper returns two distinct strings,
  each containing its own index.
- **SC-010 (Sentinel-absent positive case)**: The sentinel-absent
  assertion run against a haystack containing zero occurrences
  of the sentinel substring reports no failure on the test
  handle.
- **SC-011 (Sentinel-absent negative case)**: The sentinel-absent
  assertion run against a haystack containing at least one
  occurrence of the sentinel substring reports a failure on the
  test handle, and the failure message identifies the sentinel
  substring and locates the match.
- **SC-012 (Discord stub auto-approve)**: With auto-approve mode
  enabled and the programmed queue empty, every approval
  request the stub receives returns an approve decision.
- **SC-013 (Discord stub programmable queue)**: With a
  non-empty programmed queue of N decisions, N sequential
  approval requests return the N decisions in pre-loaded order.
- **SC-014 (Discord stub call recording)**: For every approval
  request the stub receives, the recorded-calls list grows by
  exactly one entry whose attributes match the request's
  identifying attributes.
- **SC-015 (Discord stub concurrent safety)**: Many goroutines
  issuing approval requests through one stub concurrently
  complete under race-detector instrumentation with zero
  reported data races, no corrupted recorded-call entries, and
  every issued request paired with exactly one recorded entry.
- **SC-016 (No network in any helper)**: No helper in the
  package, when run on a host with all outbound network
  connections blocked, produces a different result than when
  run on a host with full network connectivity.
- **SC-017 (Automatic cleanup)**: For every helper that returns
  a cleanup callback (or whose construction allocates a
  resource), the cleanup runs without the caller invoking it
  explicitly when the test handle's per-test cleanup hook
  fires.
- **SC-018 (Temp-dir containment)**: A repository search after
  every test in this package's self-test suite finds zero files
  written by any helper outside the test handles' temporary
  directories.
- **SC-019 (No persistent state)**: A helper invoked from a
  first test, then re-invoked from a second sequential test in
  the same package, observes none of the first invocation's
  internal state (recorded calls, key bytes, file paths,
  programmed decisions).
- **SC-020 (No `init()`)**: The package source contains no
  `init()` function. Importing the package from any caller
  produces no observable side-effect on any package-level
  mutable state.
- **SC-021 (Test-only enforcement)**: A repository search for
  production source files (`*.go` not ending in `_test.go`)
  importing this package returns zero results, and the lint
  configuration blocks any future addition.
- **SC-022 (Self-test coverage ≥ 80%)**: The package's own
  self-test suite covers at least 80% of the package's
  statements, the relaxed bar applicable to test-infrastructure
  packages whose primary value surfaces only when consumed by
  downstream tests.

## Assumptions

- **SDD-01 contract is in place**: The `internal/keys` package
  exposes a deterministic master-seed derivation function that
  this package's test-keys helper invokes with the hardcoded
  test-only passphrase and salt. The output length and
  derivation parameters are governed by SDD-01.
- **SDD-02 contract is in place**: The Layer 5 secure-container
  primitive (`SecureBytes`, SDD-02) provides allocation, byte
  exposure to controlled callers, and explicit zeroing. The
  vault fixture's returned vault key uses this primitive, and
  the cleanup callback's zeroing relies on its zero method.
- **SDD-03 contract is in place**: The `internal/vault` package
  exposes a save path that writes a valid HUSH-format vault
  file from a secrets map and a vault key. The vault fixture
  invokes this save path; this package does not reproduce vault
  serialisation logic.
- **The project's test framework provides per-test handles with
  per-test cleanup hooks and per-test temporary directories**:
  Every helper in this package takes such a handle as its first
  parameter and registers cleanup callbacks against it. The
  framework guarantees the cleanup hook runs once per test on
  pass, fail, or skip; the temporary directory is removed after
  all cleanups complete.
- **The project's standard salt length and master-seed length
  are stable**: The test-keys helper produces output sized per
  the project's standard. Changing those sizes is governed by
  the SDD-01 contract; this package follows whatever values
  SDD-01 publishes.
- **A single shared test-only passphrase is acceptable for every
  test**: All tests across the project that need test key
  material derive from the same hardcoded passphrase and salt.
  The master seed is therefore not unique per test — it is
  unique per project. This is intentional: the determinism
  property requires it.
- **No helper requires platform-specific behaviour**: All
  helpers run identically on the project's two supported
  platforms (macOS arm64, Linux amd64). The vault fixture's
  filesystem behaviour depends only on the test framework's
  per-test temporary directory primitive.
- **Internal-only consumption**: The package is consumed only
  by `*_test.go` files inside the project's `internal/` tree.
  It is not part of any external API contract; the helper
  signatures are governed by the chunk's API contract and
  `docs/PACKAGE-MAP.md`.

## Out of Scope

- **Real Discord approver implementation**: The Discord
  approval stub satisfies only the minimal approval interface
  this package needs. The full approver — Discord bot wiring,
  interactive button rendering, callback handling, alert tier
  dispatch — lives in SDD-11 (`internal/discord`). This
  package's stub MUST NOT pretend to implement it.
- **Real key derivation parameters**: The test-keys helper uses
  a hardcoded passphrase and salt deliberately distinct from
  any production parameters. Production key derivation
  parameters (Argon2id cost factors, salt provenance) are
  governed by SDD-01.
- **Cryptographic protocol fixtures (JWT, ECIES, request
  signing)**: Fixtures for higher-layer crypto protocols are
  out of scope. This package supplies the primitives (test
  keys, vault file, sentinels, approval stub); higher-layer
  fixtures are introduced in their owning chunks.
- **Integration test orchestration helpers**: This package
  supplies unit-test primitives. Integration-test orchestration
  (multi-process supervision, network fakes, real
  filesystem-state assertions) is out of scope and is
  introduced in the integration-test chunk if and when needed.
- **Fuzz harness primitives**: Fuzz targets are owned by the
  packages whose code they exercise (Constitution Principle
  VIII names six mandatory fuzz targets, each owned by its
  surrounding chunk). This package supplies no fuzz-specific
  primitives.
- **A fixture that creates files outside the test handle's
  temporary directory**: Forbidden by FR-022. Any future
  helper that needs such a file MUST live in a separate
  package with explicit cleanup semantics, not in this one.
- **A pluggable per-helper logger**: This package's helpers
  emit no log records of their own. Failure modes are
  reported through the test handle's failure mechanism, not
  through the project's `log/slog` logger.
- **Cross-process or cross-test fixture sharing**: Every
  helper's state is scoped to one test handle. Sharing
  fixtures across tests is forbidden by FR-023.
- **A test-keys helper variant that takes a custom passphrase
  or salt**: Out of scope for v0.1.0. The shared-determinism
  property of the helper requires a single hardcoded pair;
  per-call customisation would defeat that property.
- **Windows support**: Out of scope for v0.1.0 (project-wide).

## Dependencies

- **SDD-01 (`internal/keys`)** — The deterministic master-seed
  derivation function the test-keys helper invokes with the
  hardcoded test-only passphrase and salt.
- **SDD-02 (`internal/vault/securebytes`)** — The Layer 5
  secure-container primitive used as the type of the vault
  fixture's returned vault key, and whose zero method the
  fixture's cleanup callback invokes.
- **SDD-03 (`internal/vault`)** — The HUSH-file-format save
  path the vault fixture invokes to produce its on-disk vault
  file.
- **Constitution Principle I (Zero Files at Rest on Agent
  Machines)** — Forbids any production code path from
  importing this test-only package; FR-025 encodes the rule.
- **Constitution Principle VIII (Testing Discipline)** — The
  package's reason for existing: it underpins every
  downstream chunk's ability to meet the constitutional
  coverage bar. The acceptance criterion this chunk
  contributes to (AC-9) is indirect: every subsequent chunk
  depends on the harness primitives defined here.
- **Constitution Principle IX (Idiomatic Go Discipline)** —
  The no-`init()`, no-globals, accept-handles-explicitly
  rules this package's surface inherits.
- **Constitution Principle X (Observability & Redaction)** —
  The sentinel helper and sentinel-absent assertion are the
  primary instruments downstream packages use to prove the
  no-leak guarantee in their own tests.
- **`docs/TESTING-STRATEGY.md` §3 (test layers)** — Names
  this package's package layout (`internal/...`) and the
  per-package test file pattern.
- **`docs/TESTING-STRATEGY.md` §5 (sentinel pattern)** —
  Defines the sentinel-injection assertion pattern this
  package's sentinel helper and assertion implement
  canonically.
- **Downstream packages blocked on this one**: SDD-25 and
  every server, CLI, and supervisor chunk that follows; the
  full fan-out is documented in `docs/sdd/SDD-PLAYBOOK.md`.
