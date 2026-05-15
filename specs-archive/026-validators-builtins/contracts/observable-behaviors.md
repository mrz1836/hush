# Phase 1 Contracts — Observable Behaviors (SDD-26)

**Feature:** 026-validators-builtins
**Date:** 2026-05-13
**Spec:** [../spec.md](../spec.md) · **Plan:** [../plan.md](../plan.md) · **Data model:** [../data-model.md](../data-model.md) · **Chunk doc:** [../../../docs/sdd/SDD-26.md](../../../docs/sdd/SDD-26.md)

This file is the black-box contract list for the package: each
B-V-N entry is one observable behavior the implementation MUST
satisfy and the test that asserts it. The list is split into
**shared** behaviors (apply to all five validators equally — the
test runs once at the package level) and **per-provider** behaviors
(the same shape replicated across all five providers — the test
suite covers each).

Behavior IDs use the pattern `B-V-<area>-<n>`:

- `B-V-IF-*` — Validator interface invariants
- `B-V-REG-*` — Registry invariants
- `B-V-ERR-*` — Sentinel error invariants
- `B-V-DOR-*` — Shared HTTP machinery (`doRequest`, `classifyTransportError`)
- `B-V-LOG-*` — slog record schema
- `B-V-SEC-*` — Secret-handling invariants (Constitution X)
- `B-V-FIX-*` — Test-fixture / live-provider invariants
- `B-V-P-<provider>-*` — Per-provider behavior (× 5)

---

## Shared behaviors — applied at the package level (one test, one outcome)

### B-V-IF-1 — Validator interface has exactly one method

The exported `Validator` interface declares exactly one method
(`Validate`), of signature
`Validate(ctx context.Context, secret *securebytes.SecureBytes) error`.

- **Test:** `TestValidator_InterfaceHasOneMethod`
- **Source:** spec FR-001, data-model V-1
- **Assertion:** `reflect.TypeOf((*Validator)(nil)).Elem().NumMethod() == 1`

### B-V-IF-2 — All five concrete types satisfy `Validator`

Each of the five `New<Provider>` constructors returns a value that
satisfies the `Validator` interface (compile-time guard via
`var _ Validator = (*<provider>Validator)(nil)` in each provider
file).

- **Test:** `TestValidator_InterfaceSatisfied_Anthropic`, `_AnthropicOAuth`, `_OpenAI`, `_GoogleAI`, `_GitHub`
- **Source:** spec FR-001, data-model V-2
- **Assertion:** compile-time guard + `var _ Validator = New<Provider>(nil)` in test

### B-V-REG-1 — All five names resolve to non-nil validators

`NewRegistry(nil).Get(name)` returns `(non-nil Validator, true)` for
each of `"anthropic"`, `"anthropic-oauth"`, `"openai"`,
`"google-ai"`, `"github"`.

- **Test:** `TestRegistry_AllFiveNamesPresent`
- **Source:** spec FR-010, US-5 AS-1
- **Assertion:** table-driven for the five names; each case asserts non-nil + true

### B-V-REG-2 — Unknown names resolve to `(nil, false)`

`Get` returns `(nil, false)` for: `""`, `"Anthropic"`, `"GITHUB"`,
`" openai "`, `"nonsense"`, `"anthropic-oauth-extra"`, and any
case variant.

- **Test:** `TestRegistry_GetUnknownName_FalseFound`
- **Source:** spec FR-011, US-5 AS-2, Spec Clarification Q2
- **Assertion:** table-driven negatives

### B-V-REG-3 — Recognised name set is exactly the five

Enumerating the registry's keys (via an internal `Names() []string`
helper exposed in `export_test.go` OR via reflection over the
unexported `byName` field) yields exactly the five FR-010 strings.

- **Test:** `TestRegistry_ExactlyFiveNames` (SC-007)
- **Source:** spec FR-010, FR-011, US-5 AS-3, SC-007
- **Assertion:** set equality against `{"anthropic", "anthropic-oauth", "openai", "google-ai", "github"}`

### B-V-REG-4 — `Get` is race-clean

100 goroutines × 100 invocations of `Get("openai")` produce no race
report under `-race`.

- **Test:** `TestRegistry_GetIsRaceClean`
- **Source:** spec FR-016, FR-017, Constitution VIII
- **Assertion:** `-race` clean

### B-V-ERR-1 — Three sentinels are pairwise distinct

`ErrStaleCredential != ErrValidatorTimeout`,
`ErrStaleCredential != ErrValidatorNetwork`,
`ErrValidatorTimeout != ErrValidatorNetwork` (identity comparison
on the error interface values).

- **Test:** `TestPackage_SentinelsArePairwiseDistinct`
- **Source:** spec FR-002, US-2 AS-4
- **Assertion:** `if a == b { t.Fatal(...) }` × 3

### B-V-ERR-2 — Sentinel error strings are literal

`ErrStaleCredential.Error()`, `ErrValidatorTimeout.Error()`,
`ErrValidatorNetwork.Error()` are exactly the locked literal
strings (data-model §5 declarations) — no per-call interpolation.

- **Test:** `TestPackage_SentinelStringsAreLiteral`
- **Source:** data-model S-2, S-3
- **Assertion:** string equality against the declared literals

### B-V-LOG-1 — Success path emits exactly one DEBUG record

For any 2xx response, `Validate` emits exactly one `slog` record at
DEBUG level with attributes `{validator: <name>, outcome: "success",
status: <2xx int>}` and the constant message `"validator outcome"`.

- **Test:** `TestPackage_LogRecordSchema_Success`
- **Source:** spec FR-020, data-model H-4
- **Assertion:** captured handler records have len == 1, level == Debug, expected attrs

### B-V-LOG-2 — Failure paths emit exactly one WARN record

For any of the three failure paths, `Validate` emits exactly one
`slog` record at WARN level with attributes
`{validator: <name>, outcome: "stale"|"timeout"|"network", status: <int>}`.
The `status` attribute is omitted when there is no HTTP response
(transport-level failure, pre-cancel fast-path, destroyed-SecureBytes).

- **Test:** `TestPackage_LogRecordSchema_Failure`
- **Source:** spec FR-020, data-model H-4
- **Assertion:** captured records len == 1, level == Warn, attrs per the schema

### B-V-LOG-3 — Log attributes are an allow-list

No `slog` record emitted by the package contains any of: `error`,
`url`, `request`, `response`, `header`, `secret`, `credential`,
`scope`, `body`. Only `validator`, `outcome`, `status`.

- **Test:** `TestPackage_LogAttrsAreAllowList`
- **Source:** spec FR-008, FR-020, data-model H-5
- **Assertion:** for every captured record, iterate attrs and assert key ∈ {validator, outcome, status}

### B-V-SEC-1 — No `string(secret)` in non-test code

A source-grep across all non-`_test.go` files in
`internal/supervise/validators/` matches **zero** occurrences of
`string(secret`, `string(creds`, `string(credential`,
`fmt.Sprintf("%s", secret`, `slog.Any("secret"`, or the
credential value in any `errors.New` / `fmt.Errorf` literal.

- **Test:** `TestPackage_NoStringConversionsOfSecret`
- **Source:** spec SC-005, US-3 AS-4
- **Assertion:** filepath.Walk + bytes.Contains for each pattern

### B-V-SEC-2 — No `*http.Request` / `*http.Header` passed to a byte sink

A source-grep + AST scan asserts no code path passes
`*http.Request`, `*http.Header`, or `http.Header` to a `slog` /
`fmt.Errorf` / `errors.New` / `io.Writer.Write` call.

- **Test:** `TestPackage_NoRequestObjectInLogOrError`
- **Source:** spec FR-008, US-4 AS-2
- **Assertion:** Go-source AST traversal in the test

### B-V-SEC-3 — All builders allocate-copy-zero

Each of the five `set<Provider>Auth` builders allocates a fresh
`[]byte`, copies prefix + secret, calls `req.Header.Set`, then
zeros every byte before returning. Verified by Go-source AST
scan: each builder body contains exactly one `make([]byte, ...)`,
exactly one `req.Header.Set` call, and exactly one
`for i := range buf { buf[i] = 0 }` zero-loop (or equivalent
`clear(buf)` call) before the final `return`.

- **Test:** `TestPackage_AllBuildersZeroLocalBuffer`
- **Source:** spec FR-007, data-model B-3
- **Assertion:** `go/parser` + `go/ast` traversal in the test; each builder function body matches the locked pattern

### B-V-FIX-1 — No live-provider hosts in test code

A source-grep across all `*_test.go` files asserts every occurrence
of `api.anthropic.com`, `api.openai.com`, `api.github.com`,
`generativelanguage.googleapis.com` is inside a `rewriteTransport`
literal or a companion constant declared at the top of the test
file — never inside an `http.Client.Do` / `http.Get` / `http.Post`
target.

- **Test:** `TestPackage_NoLiveProviderHosts` (SC-004)
- **Source:** spec FR-014, US-6 AS-2, SC-004
- **Assertion:** filepath.Walk + bytes.Contains + AST scan of CallExpr targets

### B-V-FIX-2 — No `go.mod` change

A grep + parse of `go.mod` asserts no new `require` line was added
by this chunk. Verified by `go list -m -json all` at test time
asserting only the prior dependency set is present.

- **Test:** `TestPackage_ZeroNewDependencies`
- **Source:** spec FR-018, Constitution XI
- **Assertion:** parse `go.mod`, compare against the locked dependency set

### B-V-FIX-3 — Default timeout is 5s

`effectiveClient(nil).Timeout == 5 * time.Second`. When the caller
supplies a non-nil client, `effectiveClient` returns the
caller-supplied client verbatim (no timeout override).

- **Test:** `TestPackage_DefaultClientTimeoutIs5s`,
  `TestPackage_CallerSuppliedClientReturnedVerbatim`
- **Source:** spec FR-012, Spec Clarification Q1
- **Assertion:** direct field comparison

---

## Per-provider behaviors — applied to each of the five (one set, five test files)

For each provider `P ∈ {Anthropic, AnthropicOAuth, OpenAI, GoogleAI, GitHub}`,
the test file `<provider>_test.go` asserts every behavior below.
The provider-specific value of the FR-010 name string is denoted
`<p>` (e.g. `<p> = "openai"`).

### B-V-P-`<p>`-1 — Happy path returns nil

When the fixture responds with HTTP 200, `Validate` returns nil.
The fixture's handler-invocation counter equals 1 (FR-019).

- **Test:** `TestValidator_<P>_HappyPath_200`
- **Source:** spec US-1 AS-1, FR-004
- **Assertion:** `err == nil`, handler counter == 1, log record at DEBUG

### B-V-P-`<p>`-2 — 401 returns `ErrStaleCredential`

When the fixture responds with HTTP 401, `Validate` returns an
error satisfying `errors.Is(err, ErrStaleCredential)`. No other
sentinel matches.

- **Test:** `TestValidator_<P>_StaleCredential_401`
- **Source:** spec US-1 AS-2, FR-004
- **Assertion:** `errors.Is(err, ErrStaleCredential) == true`; `errors.Is(err, ErrValidatorTimeout) == false`; `errors.Is(err, ErrValidatorNetwork) == false`

### B-V-P-`<p>`-3 — 403 returns `ErrStaleCredential`

When the fixture responds with HTTP 403, same as above.

- **Test:** `TestValidator_<P>_StaleCredential_403`
- **Source:** spec US-1 AS-3, FR-004
- **Assertion:** same as B-V-P-`<p>`-2

### B-V-P-`<p>`-4 — 500 / 502 / 503 / 429 return `ErrValidatorNetwork`

When the fixture responds with any non-2xx-non-401/403 status, the
returned error satisfies `errors.Is(err, ErrValidatorNetwork)` and
**not** `ErrStaleCredential` or `ErrValidatorTimeout`. Spec
Clarification Q3.

- **Test:** `TestValidator_<P>_NetworkError_5xx` (table-driven over 500, 502, 503, 429)
- **Source:** spec US-2 AS-3, FR-004, Spec Clarification Q3
- **Assertion:** `errors.Is(err, ErrValidatorNetwork) == true`; the other two sentinels are not matched

### B-V-P-`<p>`-5 — Timeout returns `ErrValidatorTimeout`

When the fixture sleeps longer than the configured client timeout
(test uses a 100ms-timeout client with a 500ms-sleep handler),
`Validate` returns an error satisfying
`errors.Is(err, ErrValidatorTimeout)`.

- **Test:** `TestValidator_<P>_Timeout`
- **Source:** spec US-2 AS-1, FR-005
- **Assertion:** `errors.Is(err, ErrValidatorTimeout) == true`; not the other two

### B-V-P-`<p>`-6 — Connection refused returns `ErrValidatorNetwork`

When the fixture is constructed then closed before `Validate` is
called (`httptest.NewServer(...).Close()`), `Validate` returns an
error satisfying `errors.Is(err, ErrValidatorNetwork)`.

- **Test:** `TestValidator_<P>_NetworkError_Refused`
- **Source:** spec US-2 AS-2, FR-005
- **Assertion:** `errors.Is(err, ErrValidatorNetwork) == true`

### B-V-P-`<p>`-7 — 3xx redirect classified as `ErrValidatorNetwork`

When the fixture responds with HTTP 302 + a Location header
pointing to a second fixture endpoint, the validator MUST NOT
follow the redirect (FR-021); it MUST classify the 302 as
`ErrValidatorNetwork`. The second fixture's handler-invocation
counter MUST remain at 0 (FR-019).

- **Test:** `TestValidator_<P>_Redirect3xx_ClassifiedAsNetwork`
- **Source:** spec FR-021, US-2 edge "HTTP redirect (3xx)", Spec Clarification Q9
- **Assertion:** `errors.Is(err, ErrValidatorNetwork) == true`; second fixture counter == 0

### B-V-P-`<p>`-8 — Pre-cancelled context returns ≤ 50ms, 0 outbound requests

When the caller passes a context that is already cancelled (or
whose deadline is in the past), `Validate` returns within 50ms
wall-clock and the fixture's handler-invocation counter remains
at 0.

- **Test:** `TestValidator_<P>_CtxCancelledBeforeSend_NoHandlerInvocation`
- **Source:** spec SC-008, FR-013, edge "Context cancellation before HTTP send"
- **Assertion:** elapsed < 50ms; fixture counter == 0; error satisfies one of the network/timeout sentinels per FR-013

### B-V-P-`<p>`-9 — Mid-flight context cancellation returns promptly

When the fixture sleeps for 5s and the caller cancels the context
after 100ms, `Validate` returns within 250ms with an error
satisfying `errors.Is(err, ErrValidatorTimeout)` or
`errors.Is(err, ErrValidatorNetwork)` per FR-013's classification
rule.

- **Test:** `TestValidator_<P>_CtxCancelledMidFlight`
- **Source:** spec FR-013, edge "Context cancellation mid-flight"
- **Assertion:** elapsed < 250ms; error is one of the two transient sentinels

### B-V-P-`<p>`-10 — Single outbound request per call

For every status-code-mapping test above, the fixture's
handler-invocation counter is exactly 1 (the validator does not
internally retry — FR-019).

- **Test:** `TestValidator_<P>_SingleRequest` (table-driven over 200/401/403/500)
- **Source:** spec FR-019, Spec Clarification Q7
- **Assertion:** atomic counter == 1 after the Validate call

### B-V-P-`<p>`-11 — Concurrent invocations race-clean

8 goroutines × 16 invocations of `Validate` against a shared
fixture (each goroutine using its own *SecureBytes) produce no
race report under `-race` and every call returns the expected
verdict.

- **Test:** `TestValidator_<P>_Concurrent`
- **Source:** spec FR-017, US-edge "Concurrent invocations"
- **Assertion:** `-race` clean; all 128 verdicts match expected

### B-V-P-`<p>`-12 — Destroyed SecureBytes returns `ErrValidatorNetwork` with preserved chain

When the caller passes a `*SecureBytes` whose `Destroy` has
already been called, `Validate` returns an error satisfying BOTH
`errors.Is(err, ErrValidatorNetwork)` AND
`errors.Is(err, securebytes.ErrDestroyed)`. The fixture's
handler-invocation counter remains at 0.

- **Test:** `TestValidator_<P>_DestroyedSecureBytes`
- **Source:** spec edge "SecureBytes already destroyed", Spec Clarification Q6
- **Assertion:** `errors.Is(err, ErrValidatorNetwork) == true`, `errors.Is(err, securebytes.ErrDestroyed) == true`, counter == 0

### B-V-P-`<p>`-13 — Sentinel-leak: 401 path

When the caller passes a `*SecureBytes` wrapping the sentinel
`SECRET_SHOULD_NEVER_APPEAR_26` and the fixture returns 401, the
returned error's `Error()` and every wrapped error's `Error()`
contains zero occurrences of the sentinel. The captured slog
output (DEBUG-level handler) contains zero occurrences of the
sentinel across all records and all attributes.

- **Test:** `TestValidator_<P>_NoLeakOnError`
- **Source:** spec FR-015, SC-006, US-3 AS-1, US-3 AS-2, US-4 AS-1
- **Assertion:**
  - `err = v.Validate(ctx, secret)` → walk error chain via `errors.Unwrap`; no `Error()` contains sentinel
  - captured handler buffer: no record's serialised JSON contains sentinel

### B-V-P-`<p>`-14 — Name string matches FR-010 lowercase exact match

The validator's internal name field (visible in the slog record's
`validator` attribute) matches the locked FR-010 string exactly:
`anthropic`, `anthropic-oauth`, `openai`, `google-ai`, `github`.

- **Test:** `TestValidator_<P>_NameIsLockedString`
- **Source:** spec FR-010, Spec Clarification Q2
- **Assertion:** captured slog record's `validator` attribute string equality

### B-V-P-`<p>`-15 — Authorization header set exactly once with the correct shape

The fixture inspects the inbound request's headers and asserts:
- For anthropic: `x-api-key` is present with value equal to the
  test secret bytes; `anthropic-version` is `"2023-06-01"`;
  no `Authorization` header is present.
- For anthropic-oauth: `Authorization` is `"Bearer <secret>"`;
  `anthropic-version` is `"2023-06-01"`; no `x-api-key`.
- For openai: `Authorization` is `"Bearer <secret>"`.
- For google-ai: `x-goog-api-key` equals the test secret bytes;
  no `Authorization`.
- For github: `Authorization` is `"token <secret>"`;
  `Accept` is `"application/vnd.github+json"`.

In each case the credential bytes ARE present in the request as
transmitted (the validator's job IS to transmit the credential to
the provider) but the package's own log + error chain are
sentinel-clean (B-V-P-`<p>`-13).

- **Test:** `TestValidator_<P>_AuthHeaderShape`
- **Source:** research.md R-003, R-006
- **Assertion:** fixture handler captures `r.Header` and asserts exact equality

### B-V-P-`<p>`-16 — Empty credential forwarded, not short-circuited

When the caller passes a `*SecureBytes` wrapping a zero-length byte
slice, the validator MUST forward the (empty) credential to the
fixture and classify based on the fixture's response. It MUST NOT
short-circuit to `ErrStaleCredential` based on local "obviously
empty" logic.

- **Test:** `TestValidator_<P>_EmptyCredentialForwarded`
- **Source:** spec edge "Empty credential"
- **Assertion:** fixture's handler-invocation counter == 1; the inbound request's auth-header value is `<prefix>` only (no secret content)

---

## Behavior → test → spec traceability

| Behavior count | Test count per provider | Total test count |
|----------------|-------------------------|------------------|
| Shared: 17 (B-V-IF/REG/ERR/LOG/SEC/FIX) | — | 17 |
| Per-provider: 16 (B-V-P-`<p>`-1..16) | 16 | 16 × 5 = 80 |
| **Total** | | **≈ 97 named tests** |

The quickstart file [../quickstart.md](../quickstart.md) §4 lists
every test by name. The /speckit-tasks phase (Phase 4) generates a
task per test that writes the test BEFORE its corresponding
implementation per Constitution VIII (TDD-first).

---

## Anti-contracts (behaviors that MUST be absent)

| Anti-contract | Test |
|---------------|------|
| Validator hits live provider hosts in tests | B-V-FIX-1 (TestPackage_NoLiveProviderHosts) |
| Package introduces a new `go.mod` dependency | B-V-FIX-2 (TestPackage_ZeroNewDependencies) |
| Validator logs the credential value or the Authorization header | B-V-LOG-3 (TestPackage_LogAttrsAreAllowList) + B-V-P-`<p>`-13 (sentinel-leak) |
| Validator includes the credential value in any returned error | B-V-P-`<p>`-13 (sentinel-leak across the unwrap chain) |
| Validator's `Validate` issues > 1 outbound HTTP request | B-V-P-`<p>`-10 (single-request counter) |
| Validator follows HTTP redirects | B-V-P-`<p>`-7 (3xx classified as network; second-fixture counter == 0) |
| Validator panics on operator-supplied input | not directly tested; any panic in a `Validate` call would fail every per-provider test |
| Validator mutates the caller-supplied `*http.Client` | not directly tested; B-V-P-`<p>`-11 (concurrent) would surface a race if mutation occurred |
| Validator calls `Destroy` on the caller's `*SecureBytes` | code review (Destroy is never called in non-test code) |
| Registry mutates after construction | B-V-REG-4 (race-clean) would surface; code review confirms `byName` is written exactly once in `NewRegistry` |
