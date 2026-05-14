# Feature Specification: Pre-Flight Credential Validators (Interface + 5 Builtins)

**Feature Branch**: `026-validators-builtins`
**Created**: 2026-05-13
**Status**: Draft
**Input**: User description: "internal/supervise/validators: pre-flight credential checker interface and five builtins (anthropic, anthropic-oauth, openai, google-ai, github); credential is SecureBytes (never materialised as string); typed errors distinguish stale (401/403) vs timeout vs network; default 5s timeout; never logs value or Authorization header; tests use httptest, never live providers"

## Overview

When the supervisor receives a freshly-fetched credential from the vault
and is about to inject it into the child process's environment, it must
be able to answer one question first: **is this credential actually
accepted by the upstream provider right now?** A credential that was
valid a week ago but has since been rotated, revoked, or scoped down
will look fine to the supervisor — it is well-formed bytes from a
successful vault decrypt — but the child will burn cycles, emit error
logs, and eventually exit 78 once it tries to use it.

Validators close that gap. Each validator is a small, single-purpose
component that takes a credential, performs the cheapest read-only
auth probe the provider exposes, and returns one of four outcomes:
the credential is good, the credential is stale (provider rejected
it), the probe timed out, or the network failed. The supervisor uses
the validator's verdict to gate child start (Lifecycle Scenario 6):
if any validator returns a stale verdict, the supervisor MUST refuse
to start the child and MUST emit a `[STALE] Validator Failure` alert
naming the offending scope.

This chunk ships the validator interface plus the five built-in
implementations that match SDD-18's locked allow-list of validator
names: `anthropic`, `anthropic-oauth`, `openai`, `google-ai`,
`github`. Unknown validator names referenced in a supervisor TOML
remain a startup error (locked by SDD-18); this chunk does not
introduce a runtime-extension mechanism.

Because the credential is the load-bearing security material, the
validator interface is designed around a hard constraint: the
credential is presented to each validator as a `SecureBytes` value,
and the raw bytes MUST NEVER be materialised as a Go `string`. This
constraint is the consumer-side counterpart to the `SecureBytes`
guarantees locked by SDD-02 — it ensures no validator can leak the
credential through `string` aliasing, error formatting, structured
log fields, or stack traces.

## Clarifications

### Session 2026-05-13

- Q: When a validator is constructed without an explicit timeout override, what is the upper bound on the validator's wall-clock spent on a single `Validate` call? → A: 5 seconds. The constructor accepts an `*http.Client` for override (the operator can supply a client with a different `Timeout`), but the package-supplied default MUST be 5 seconds.
- Q: For the five builtins, is the registered validator name case-sensitive? → A: Yes — the names are the five fixed lowercase strings `anthropic`, `anthropic-oauth`, `openai`, `google-ai`, `github`. The registry MUST NOT accept any other casing (e.g. `Anthropic` or `GITHUB`); doing so would create a second source of truth that drifts from SDD-18's TOML allow-list and from the validator-failure alert text shown to the operator.
- Q: When a validator's upstream auth probe returns a status code that is neither 2xx nor 401/403 (for example 429, 500, 503), how is that classified — stale, network, or success-with-warning? → A: Network. Any HTTP response whose status code is not in the success range (2xx) and is not in the stale-credential range (401, 403) MUST be reported as `ErrValidatorNetwork`. Rationale: the credential itself was not rejected; the provider's surface is temporarily unhealthy. Mapping 5xx/429 to stale would block child start on what is clearly a provider-side incident, not a credential incident.
- Q: Are the providers' validator endpoints (whatever cheapest read-only auth probe each builtin uses) part of the public spec, or implementation detail of the plan phase? → A: Implementation detail. Specific URLs are pinned by the plan-phase research note (SDD-26 PLAN prompt) and recorded in the chunk's plan.md; they are external dependencies whose pinning belongs in the plan and is auditable in code review. The spec MUST NOT pin specific paths.
- Q: Are tests permitted to call live provider APIs under any circumstance — for example a manual integration job, a nightly cron, or a maintainer-local pre-release smoke test? → A: No. Every test in this package, including any future maintainer-local job, MUST drive each validator against an `httptest.Server` fixture. Live-provider calls in test code are a recurring source of flakes, surprise charges, and credential exposure; locking the prohibition into the spec keeps maintainers from quietly re-introducing them.
- Q: When the caller passes a `SecureBytes` whose `Destroy` method has already been called, how is the resulting failure classified relative to FR-003's "exactly one of four"? → A: Map to `ErrValidatorNetwork`. The destroyed-state error returned by `SecureBytes.Use(fn)` MUST be wrapped in the chain so `errors.Is(err, ErrValidatorNetwork)` is true and the underlying SDD-02 destroyed-state error remains inspectable. Rationale: keeps FR-003's four-state contract intact; routes through the supervisor's existing transient-failure handling rather than falsely alerting "credential rotated"; the wrapped chain preserves the precondition diagnostic for code review and debugging.
- Q: May a single `Validate` call retry internally on transient failures (5xx, 429, timeouts, transport errors), or is each call single-shot with retry policy owned by the caller? → A: Single-shot. Each `Validate` invocation MUST issue at most one outbound HTTP request; the configured 5-second timeout bounds that single attempt. The supervisor (caller) owns all retry policy through its existing refresh-retry / boot-retry paths. Rationale: keeps SC-001's 6-second budget meaningful and test-design simple; prevents two competing retry policies (validator-internal + supervisor) from interacting surprisingly during a provider incident; matches the validator's "pure function of (credential, single upstream response) → verdict" framing in the Key Entities section.
- Q: What logging contract does each validator owe — does it emit log records on its outcomes, and if so at what level and on which paths? → A: Exactly one structured `log/slog` record per `Validate` call. Success path emits at `DEBUG`; stale, timeout, and network outcomes emit at `WARN`. The record's fields MUST be limited to validator name (e.g. `"anthropic"`), outcome class (`"success" | "stale" | "timeout" | "network"`), and (when applicable) the upstream HTTP status code as an integer. No credential-derived field — name, value, header, request, response body, or any byte representation thereof — may appear in any field. Rationale: gives operators an actionable WARN at the moment of failure (where User Story 1's "fix the rotated credential" workflow needs visibility); keeps steady-state quiet at `INFO`+ production levels; makes the FR-015 / SC-006 sentinel-leak test target concrete (at least one captured record per call exists to scan); stays cleanly separated from SDD-24's audit-row responsibility (validator log is operational, audit row is forensic).
- Q: How does each validator handle HTTP 3xx responses from the upstream — follow redirects, treat as network error, or honour the operator's `*http.Client` redirect policy? → A: Disable redirect-follow in the validator's request flow. Any 3xx response from the upstream MUST be classified as `ErrValidatorNetwork` — without issuing a follow-up request. Rationale: makes redirect behaviour deterministic and independent of the operator's `*http.Client` configuration; aligns with FR-019's "at most one outbound HTTP request per invocation"; avoids Go's default cross-origin Authorization-header stripping, which would silently turn provider rerouting into false `ErrStaleCredential` alerts; matches the validator's "single probe, single verdict" framing.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Supervisor refuses to start child on rotated credential (Priority: P1)

Z rotated his Anthropic API key on the provider console yesterday but
forgot to run `hush secret rotate ANTHROPIC_API_KEY` on the vault
host. The daemon's nightly supervisor session expires at 05:30. At
09:15 the morning refresh DM lands; Z approves it from his phone.
The supervisor fetches the (now-stale) credential from the vault
under a fresh JWT and is about to inject it into the child. Before
it does, the supervisor invokes the configured `anthropic` validator
with the freshly-fetched credential. The validator probes the
Anthropic upstream, receives a 401 response, and returns a typed
stale-credential error. The supervisor refuses to start the child,
transitions to `awaiting-approval`, and emits a `[STALE] Validator
Failure` alert naming `ANTHROPIC_API_KEY`. Z fixes the secret on the
vault host, runs `hush client refresh --supervisor <daemon>`, and
the supervisor re-runs validators and starts the child cleanly.

**Why this priority**: This is the Scenario-6 deliverable from
`docs/LIFECYCLE-SCENARIOS.md` and the entire reason the validator
subsystem exists. Without it, the supervisor cannot distinguish
"vault returned bytes" from "child can actually authenticate" — the
exit-78 contract becomes the only safety net, costing minutes of
child startup, log noise, and operator confusion per occurrence.

**Independent Test**: Construct a validator pointed at an
`httptest.Server` fixture that returns 401; invoke `Validate` with a
`SecureBytes`-wrapped credential; observe that the returned error
matches the stale-credential sentinel exactly (`errors.Is` evaluates
to true).

**Acceptance Scenarios**:

1. **Given** a validator constructed for provider X and a `SecureBytes`-wrapped credential, **When** the provider responds with HTTP 200 to the validator's auth probe, **Then** `Validate` returns `nil`.
2. **Given** a validator constructed for provider X and a `SecureBytes`-wrapped credential, **When** the provider responds with HTTP 401, **Then** `Validate` returns an error that satisfies `errors.Is(err, ErrStaleCredential)`.
3. **Given** a validator constructed for provider X and a `SecureBytes`-wrapped credential, **When** the provider responds with HTTP 403, **Then** `Validate` returns an error that satisfies `errors.Is(err, ErrStaleCredential)`.
4. **Given** the supervisor wires the validator into its pre-child-start gate, **When** `Validate` returns `ErrStaleCredential`, **Then** the supervisor transitions to `awaiting-approval` and the child process is never spawned.

---

### User Story 2 — Validator distinguishes provider rejection from network failure (Priority: P1)

A daemon is running. The configured Anthropic refresh window opens.
The supervisor fetches a fresh credential and invokes the validator.
The provider's edge is briefly unavailable: the TCP connection is
refused, or DNS times out, or the TLS handshake fails. The
validator MUST report a network-class error — distinct from a stale
credential — so the supervisor's downstream logic can decide that
this is **not** a stale credential. A network error today probably
means "retry shortly" (and the supervisor can fall back to its
boot-retry / refresh-retry path), whereas a stale credential today
means "child cannot start until operator intervenes". Conflating
the two would either spam the operator with stale alerts during
brief upstream blips, or silently swallow a real auth failure
behind a network-retry policy.

The same distinction MUST hold for the timeout class: if the
configured timeout fires before the provider responds at all, the
validator MUST report a timeout error, not a stale error.

**Why this priority**: The whole point of typed errors in this
package is to let downstream decision-makers (supervisor lifecycle,
alert classifier) act differently in the three failure modes. If
the validator collapses them into a single opaque error, the
supervisor can do nothing better than "show a generic failure";
operators lose the actionable distinction between "fix the
credential" and "wait for upstream".

**Independent Test**: Construct a validator pointed at an
`httptest.Server` whose handler artificially sleeps longer than
the validator's configured timeout; invoke `Validate`; observe
that the returned error satisfies `errors.Is(err,
ErrValidatorTimeout)` and does NOT satisfy `errors.Is(err,
ErrStaleCredential)`. Then point a second validator at an
already-closed listener; invoke `Validate`; observe that the
returned error satisfies `errors.Is(err, ErrValidatorNetwork)` and
does NOT satisfy either of the other two sentinels.

**Acceptance Scenarios**:

1. **Given** a validator with a configured request timeout T, **When** the upstream probe does not respond within T, **Then** `Validate` returns an error that satisfies `errors.Is(err, ErrValidatorTimeout)`.
2. **Given** a validator pointed at an unreachable endpoint (connection refused, DNS failure, TLS failure), **When** `Validate` is invoked, **Then** the returned error satisfies `errors.Is(err, ErrValidatorNetwork)`.
3. **Given** a validator whose upstream responds with HTTP 500, 502, 503, or 429, **When** `Validate` is invoked, **Then** the returned error satisfies `errors.Is(err, ErrValidatorNetwork)` — provider-surface unhealthiness is not a credential rejection.
4. **Given** any failure-path result from `Validate`, **When** an operator (or test) compares the error against the three sentinels with `errors.Is`, **Then** exactly one sentinel matches; the three sentinels are pairwise distinct.

---

### User Story 3 — Credential never leaves SecureBytes as a Go string (Priority: P1)

The cardinal vault guarantee — locked by Principle X of the
constitution and proven by the `internal/vault/securebytes` test
suite — is that secret material lives in `SecureBytes` and never
appears in a Go `string`, because once material is a `string` the
runtime is free to copy it, intern it, retain it through escape
analysis, and surface it in stack dumps or log fields. Every
validator in this package consumes a `SecureBytes` value at the
entry to `Validate` and is responsible for honouring that
guarantee end-to-end: the bytes MUST be borrowed inside the
`SecureBytes.Use(fn)` scope, used to construct the outbound HTTP
authorisation header value as a byte slice, transmitted to the
provider, and the local byte buffer MUST be zeroed immediately
after the HTTP call returns. At no point in the validator's source
or in any code path the validator triggers may the bytes be
coerced to `string`. This is a property of every validator
implementation in the package, not a "nice to have" of the
interface.

**Why this priority**: A validator that leaks the credential
through a log field, an error message, or a stack trace defeats
the entire vault system the user already paid in complexity to
build. Every validator added to this package is a new opportunity
for that leak. The spec MUST encode the invariant strongly enough
that a reviewer can verify it from the spec alone, not from
implementation diligence.

**Independent Test**: Wrap a sentinel byte sequence
`SECRET_SHOULD_NEVER_APPEAR_26` in `SecureBytes` and invoke each
validator against an `httptest.Server` that returns 401. The
returned error's `Error()` string MUST NOT contain the sentinel.
Captured `slog` output produced during the call MUST NOT contain
the sentinel. The captured raw HTTP request body and headers
recorded by the `httptest.Server` MAY contain the sentinel
(because the credential is the credential — transmitting it is
the validator's job), but the sentinel MUST NOT appear in any of
the package's logged output or in error chains the package
returns.

**Acceptance Scenarios**:

1. **Given** any validator in the package and a credential `SecureBytes` wrapping a sentinel byte sequence, **When** the upstream returns 401 and `Validate` returns its error, **Then** the sentinel is absent from `err.Error()` and from every wrapped error in the chain.
2. **Given** any validator in the package and a credential `SecureBytes` wrapping a sentinel byte sequence, **When** `Validate` is invoked through a slog logger configured to capture all levels, **Then** the sentinel is absent from every emitted log record's message and structured fields.
3. **Given** any validator in the package, **When** the implementation is reviewed, **Then** the source file contains exactly one `SecureBytes.Use(fn)` invocation per credential consumption site, and the credential byte buffer inside the `Use(fn)` scope is zeroed before the function returns.
4. **Given** any validator in the package, **When** the implementation is reviewed, **Then** the source contains zero `string(secret)` conversions of the credential, zero `fmt.Sprintf("%s", secret)` formatting of the credential, and zero `%v` / `%+v` formatting of the credential in any logged or returned value.

---

### User Story 4 — Validator never logs the Authorization header (Priority: P1)

Even after the credential bytes themselves are protected by the
`SecureBytes` constraint, a careless validator can leak through a
different surface: the outbound HTTP request's `Authorization`
header is a fully-materialised byte slice (it has to be, to be
transmitted), and reflection-based loggers, HTTP-client tracing
hooks, or generic `http.Request` dumpers can serialise that
header into operational logs. The vault guarantee evaporates
whether the leak is the raw credential bytes or a `Bearer ...`
prefix containing them.

This story tightens the surface: no validator in this package may
log, format, or transmit-to-an-`slog`-handler the value of the
`Authorization` header (or any other request header whose value
is derived from the credential bytes). The package's own log
emissions MAY identify a validator by name and report its
outcome (success / stale / timeout / network) and pin the
provider's response status code, but MUST NEVER include the
header value or anything derived from it.

**Why this priority**: This is the next-most-likely leak vector
after raw-string materialisation; it is the one that the
`SecureBytes` type cannot defend against because the header is
constructed for transmission and is therefore plain `[]byte` at
the moment of leak.

**Independent Test**: Configure each validator with a credential
containing a unique sentinel; route the validator's `slog` output
to a capturing handler; invoke `Validate` against an
`httptest.Server`; assert the sentinel substring does not appear
in any captured log line. Repeat with a debug-level `slog`
handler to ensure no debug-only field leaks the header.

**Acceptance Scenarios**:

1. **Given** any validator and a credential `SecureBytes`, **When** `Validate` runs against an `httptest.Server`, **Then** the credential value (and any prefix concatenation thereof) is absent from every emitted log record at every log level.
2. **Given** any validator, **When** the implementation is reviewed, **Then** no code path passes an `*http.Request` or its `Header` map to a logger, error formatter, or other byte-producing sink.

---

### User Story 5 — Five fixed names matching SDD-18's allow-list (Priority: P1)

SDD-18 (supervisor TOML) locks the allow-list of validator names
operators may reference in their config: `anthropic`,
`anthropic-oauth`, `openai`, `google-ai`, `github`. Any other
name is a TOML-validation error at supervisor startup. This
chunk MUST ship exactly those five builtins, named exactly those
five strings, and the package's name-to-validator registry MUST
expose precisely that set. Adding a sixth name in this chunk
would create a name that is valid in this package but invalid in
the TOML — a confusing operator surface. Shipping fewer than
five would leave an SDD-18-valid name with no implementation —
a startup-time crash. The two surfaces are coupled and this
chunk closes the coupling.

**Why this priority**: The validator allow-list is a coordinated
contract between the TOML parser and the validator subsystem.
Drift between them is a guaranteed operator-facing crash on
upgrade. The spec MUST encode the exact set so a reviewer can
verify the coupling without crawling two packages.

**Independent Test**: Construct the registry; assert
`Get("anthropic")` returns a non-nil validator and a `true`
found-flag; repeat for `anthropic-oauth`, `openai`, `google-ai`,
`github`; assert `Get("nonsense")` returns `nil` and `false`;
assert that no other names are returned by an enumeration of
the registry's keys.

**Acceptance Scenarios**:

1. **Given** a freshly-constructed registry, **When** `Get` is called with each of the five fixed names, **Then** each call returns a non-nil `Validator` and `true`.
2. **Given** a freshly-constructed registry, **When** `Get` is called with any string that is not one of the five fixed names (including misspellings, case variants, and the empty string), **Then** the call returns `nil` and `false`.
3. **Given** a freshly-constructed registry, **When** its set of recognised names is enumerated, **Then** the resulting set is exactly `{anthropic, anthropic-oauth, openai, google-ai, github}` — no more, no fewer.

---

### User Story 6 — Tests never touch live provider APIs (Priority: P2)

Tests in this package would be tempting to write against the
real provider endpoints — the URLs are pinned, the responses
are documented, the API keys are sitting in a developer's vault.
This is forbidden. Live tests are flaky (provider rate limits,
provider incidents, provider auth-token expiry), expensive
(metered billing), unsafe (leaks the test-credential value
into provider logs), and untrustworthy as a regression signal
(a flake masquerades as a real failure). Every test in this
package — unit, integration, future maintainer-local
pre-release smoke test — MUST drive each validator against an
`httptest.Server` fixture, never against a real provider.

**Why this priority**: This is the difference between a test
suite that protects future maintainers and one that erodes
their confidence. Encoding it in the spec keeps it from
quietly regressing during a future "we just need to test it
end-to-end" pressure moment.

**Independent Test**: Grep every test file in the package for
any of the production provider hostnames pinned in the plan
phase; assert that every occurrence is inside an
`httptest.Server` URL-construction site, never inside an
`http.Client.Do` or `http.Get` target.

**Acceptance Scenarios**:

1. **Given** the package's test suite, **When** it is executed in isolation with the network disconnected, **Then** every test passes.
2. **Given** the package's test suite, **When** it is grep'd for production provider hostnames, **Then** every occurrence is part of a constructed `httptest.Server` URL or a constant declared next to one — never a target of an HTTP call.

---

### Edge Cases

- **Empty credential**: A `SecureBytes` wrapping a zero-length byte slice is passed to `Validate`. The validator MUST treat this as a normal credential value (forwarding it to the provider, which will reject it) and MUST NOT special-case it into an early `ErrStaleCredential` short-circuit. Rationale: the validator's job is to ask the provider; introducing a local "obviously empty" rejection adds a second source of truth that drifts from the provider's view.
- **Context cancellation before HTTP send**: The caller passes a `context.Context` that is already cancelled. The validator MUST honour the cancellation and return promptly with an error that wraps `context.Canceled` (mapping into one of the typed sentinels: timeout-class for `context.DeadlineExceeded`, network-class for `context.Canceled` on a not-yet-issued request). It MUST NOT issue an outbound HTTP request when the context is already done.
- **Context cancellation mid-flight**: The caller cancels the context after the HTTP request has been sent. The validator MUST abandon the request promptly and return a typed error. The classification (timeout vs network) MUST follow the same rule as the pre-send case.
- **Concurrent invocations**: A single validator instance is invoked from multiple goroutines simultaneously with distinct `SecureBytes` values. Each invocation MUST complete independently with the correct verdict; there MUST be no shared mutable state in the validator that could cross-contaminate one credential's result with another's.
- **`SecureBytes` already destroyed**: The caller passes a `SecureBytes` whose `Destroy` method has already been called. The validator MUST surface this cleanly (via the `Use(fn)` callback's error path) and MUST NOT panic, leak partial bytes, or otherwise tunnel through SDD-02's destroyed-state contract. The resulting error MUST be classified as the network class — `errors.Is(returnedErr, ErrValidatorNetwork)` MUST be true, and the underlying SDD-02 destroyed-state error MUST be present in the wrapped chain so `errors.Is(returnedErr, <SDD-02 destroyed sentinel>)` is also true for code-review and diagnostic purposes. No outbound HTTP request MUST be issued.
- **Provider returns 200 with an error body**: Some providers occasionally return HTTP 200 with a JSON body indicating an auth problem (e.g. `{"error": "invalid_token"}`). The validator MUST classify by HTTP status only; a 200 body means the credential is valid, regardless of payload content. Inspecting response bodies for provider-specific error fields is out of scope (it varies per provider, drifts often, and is a maintenance trap). If a provider's documented contract is "200 + error body", that is a provider-side ambiguity for the operator to resolve in their config, not for the validator to second-guess.
- **HTTP redirect (3xx)**: The provider returns a 3xx redirect. The validator MUST classify this as `ErrValidatorNetwork` without issuing a follow-up request. Redirect-follow MUST be explicitly disabled in the validator's request flow regardless of the operator-supplied `*http.Client`'s redirect policy, so the validator's behaviour is deterministic and FR-019 (single outbound request) is preserved. Rationale: Go's default `http.Client` follows redirects but strips the `Authorization` header on cross-origin hops, which would surface a stripped follow as 401 and misclassify provider rerouting as a stale credential.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The package MUST expose a `Validator` interface whose sole method is `Validate(ctx context.Context, secret *securebytes.SecureBytes) error`. The interface MUST NOT have additional methods.
- **FR-002**: The package MUST expose three pairwise-distinct exported sentinel errors — `ErrStaleCredential`, `ErrValidatorTimeout`, `ErrValidatorNetwork` — that callers may compare against returned errors via `errors.Is`.
- **FR-003**: Every `Validate` implementation MUST classify its outcome into exactly one of four mutually-exclusive states: success (return `nil`), stale (return an error that satisfies `errors.Is(err, ErrStaleCredential)`), timeout (return an error that satisfies `errors.Is(err, ErrValidatorTimeout)`), or network (return an error that satisfies `errors.Is(err, ErrValidatorNetwork)`). No call MUST be able to return an error that satisfies more than one sentinel or none of them.
- **FR-004**: Every `Validate` implementation MUST classify HTTP status 401 and HTTP status 403 as the stale-credential class, and MUST classify every non-2xx response that is not 401 or 403 (including 429 and all 5xx) as the network class. HTTP 2xx is success.
- **FR-005**: Every `Validate` implementation MUST classify a request that exceeds its configured timeout as the timeout class, and MUST classify every other transport-level failure (DNS failure, connection refused, TLS handshake failure, mid-flight reset, unreachable endpoint) as the network class.
- **FR-006**: The package MUST consume the credential exclusively through the `SecureBytes.Use(fn)` API. Implementations MUST NOT call any method on the `SecureBytes` value (or any wrapper around it) that returns a Go `string` view of the bytes. Implementations MUST NOT construct a Go `string` from the bytes via conversion (e.g. `string(b)`), formatting (e.g. `fmt.Sprintf`), or any reflection path.
- **FR-007**: Inside each `Use(fn)` scope, the validator MUST copy the borrowed bytes into a freshly-allocated `[]byte` exactly long enough to construct the outbound HTTP authorisation header value, MUST use that buffer to issue the request, and MUST zero the buffer (overwrite every byte to 0) before the `Use(fn)` callback returns. The buffer MUST NOT escape the `Use(fn)` scope.
- **FR-008**: The package MUST NOT emit any log record (at any level via the `log/slog` package or otherwise) that contains the credential value, any substring of the credential value, the constructed authorisation header value, or any `http.Request` / `http.Header` object whose fields include the credential.
- **FR-009**: The package MUST NOT include the credential value, any substring of the credential value, the authorisation header value, or any byte-equal representation of either in the `Error()` string of any returned error, or in any error wrapped in the returned error's chain.
- **FR-010**: The package MUST expose five built-in `Validator` implementations corresponding to the five fixed names locked by SDD-18: `anthropic`, `anthropic-oauth`, `openai`, `google-ai`, `github`. The package MUST NOT ship additional builtins in this chunk and MUST NOT ship fewer.
- **FR-011**: The package MUST expose a registry type whose `Get(name string)` method returns the registered `Validator` and a `true` found-flag when `name` is one of the five fixed lowercase strings listed in FR-010, and returns a nil `Validator` and a `false` found-flag for every other input (including case variants, whitespace-padded variants, the empty string, and any other arbitrary string).
- **FR-012**: The package MUST set a default request timeout of 5 seconds for every built-in validator. The package MUST allow operators to override the default by accepting an `*http.Client` (or equivalent injection point) in each validator's constructor; when an operator-supplied client is passed, the validator MUST use that client's timeout instead of the default.
- **FR-013**: Every `Validate` implementation MUST honour the caller-supplied `context.Context`. A context that is already cancelled at the entry to `Validate` MUST result in a prompt return with a typed error (per FR-005); a context cancelled mid-flight MUST cause the in-flight request to abort promptly and return a typed error.
- **FR-014**: Every test in the package MUST drive validators against `net/http/httptest.Server` instances or equivalent in-process HTTP test fixtures. No test in the package MUST make outbound HTTP requests to a real provider endpoint.
- **FR-015**: For every built-in validator, the package MUST include a sentinel-leak test that wraps a fixed sentinel byte sequence (`SECRET_SHOULD_NEVER_APPEAR_26`) in `SecureBytes`, triggers the validator's stale-credential code path against an `httptest.Server` returning 401, and asserts that the sentinel is absent from the returned error's chain AND from every captured `slog` record produced during the call.
- **FR-016**: The package MUST NOT register itself with any global HTTP client default, MUST NOT modify any process-wide HTTP transport, and MUST NOT introduce package-level mutable state. Each validator instance MUST be independently configurable and concurrently usable.
- **FR-017**: The five built-in validators MUST be safe for concurrent invocation from multiple goroutines on the same instance with distinct `SecureBytes` values. There MUST be no shared mutable state on a validator instance that could cross-contaminate one credential's verdict with another's.
- **FR-018**: The package MUST NOT introduce any new direct dependency in `go.mod`. The implementation surface MUST use only the Go standard library plus types already in scope of the repository's existing dependency set (notably the `SecureBytes` type from `internal/vault/securebytes`).
- **FR-019**: Every `Validate` implementation MUST issue at most one outbound HTTP request per invocation. Validators MUST NOT retry internally on any failure class (stale, timeout, network); the supervisor owns retry policy. The configured request timeout (default 5 seconds per FR-012) bounds that single attempt.
- **FR-020**: Every `Validate` implementation MUST emit exactly one structured `log/slog` record per invocation. The record MUST be at `DEBUG` level on the success path (return `nil`) and at `WARN` level on each of the three failure paths (stale, timeout, network). The record's structured fields MUST be limited to: validator name (the registered lowercase string from FR-010), outcome class (one of `"success"`, `"stale"`, `"timeout"`, `"network"`), and the upstream HTTP status code as an integer when an HTTP response was received. The record MUST NOT contain the credential value, any substring thereof, the authorisation header value, the `*http.Request`, the `*http.Response`, the response body, or any other byte sequence derived from the credential. This requirement is consumer-facing observability and is distinct from (and additive to) FR-008's prohibition on credential-derived log content.
- **FR-021**: Every `Validate` implementation MUST disable HTTP redirect-following for its outbound probe. Any 3xx response from the upstream MUST be classified as `ErrValidatorNetwork` and MUST NOT cause the validator to issue a follow-up request. This requirement holds regardless of the operator-supplied `*http.Client`'s `CheckRedirect` field — the validator MUST ensure redirect-follow is disabled for its own probes (e.g. by setting `CheckRedirect` on a per-request basis or by using a private request flow), so that FR-019's "at most one outbound HTTP request per invocation" is preserved and so that Go's default cross-origin `Authorization`-header stripping cannot misclassify a redirect into `ErrStaleCredential`.

### Key Entities

- **Validator**: A single-method interface whose `Validate` operation answers "is this credential currently accepted by the upstream provider?" and returns one of four typed outcomes (nil, stale, timeout, network). Stateless from the operator's perspective; concurrently invocable.
- **Registry**: A read-only lookup that maps each of the five fixed validator names to the corresponding `Validator` instance. Construction wires the five names; runtime calls are pure `Get(name)` lookups.
- **Credential**: A `SecureBytes` value supplied by the supervisor at the entry to each `Validate` call. Owned by the supervisor; borrowed by the validator only inside a `Use(fn)` scope; never materialised as a Go `string` anywhere in the validator's reach.
- **Typed error sentinels**: Three pairwise-distinct sentinel error values (`ErrStaleCredential`, `ErrValidatorTimeout`, `ErrValidatorNetwork`) that act as the public contract between the validator and downstream decision-makers (supervisor lifecycle, alert classifier).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A supervisor configured with at least one validator MUST refuse to start its child within 6 seconds of fetching a credential the upstream provider rejects with 401 or 403. The 6-second budget covers the 5-second validator timeout plus state-transition overhead.
- **SC-002**: The package's test suite MUST achieve at least 90% statement coverage when run with `go test -cover ./internal/supervise/validators/`, satisfying the Phase-6 chunk-doc coverage target and Principle VIII's "high" coverage band.
- **SC-003**: The package's test suite MUST pass under `go test -race` with zero data-race reports.
- **SC-004**: The package's test suite MUST complete with the host's network interface administratively disabled (no outbound DNS, no outbound TCP), proving that no test issues real network requests.
- **SC-005**: A grep across the package's source for any of the following patterns MUST produce zero matches in non-test code: `string(secret`, `string(creds`, `string(credential`, `fmt.Sprintf("%s", secret`, the authorization header value formatted into any `slog.Attr`, and the credential value in any `errors.New` or `fmt.Errorf` literal.
- **SC-006**: For every one of the five built-in validators, the dedicated sentinel-leak test MUST pass cleanly: feeding `SECRET_SHOULD_NEVER_APPEAR_26` through a 401 path yields a returned error whose `Error()` (and every wrapped error's `Error()`) does NOT contain that string, and no captured `slog` record at any level contains that string.
- **SC-007**: The registry's set of recognised names MUST be exactly `{anthropic, anthropic-oauth, openai, google-ai, github}` — verified by an enumeration test that fails if the set diverges in either direction.
- **SC-008**: A `Validate` call with an already-cancelled context MUST return within 50 ms (wall-clock) and MUST NOT issue an outbound HTTP request — verified by an `httptest.Server` whose handler-invocation counter remains at zero after the call.

## Assumptions

- The `SecureBytes` type and its `Use(fn)` API are stable as locked by SDD-02. This chunk consumes the type and does not re-litigate its contract.
- SDD-18's TOML allow-list of validator names (`anthropic`, `anthropic-oauth`, `openai`, `google-ai`, `github`) is the canonical name set. This chunk implements precisely that set; any future name addition is a coordinated change across both SDD-18 and this package.
- The supervisor (SDD-24 orchestration glue) is responsible for wiring validators into the pre-child-start gate and for emitting the `[STALE] Validator Failure` Discord alert when a validator returns `ErrStaleCredential`. This chunk only delivers the validator subsystem; it does not own the alert emission or the state transition.
- Each provider's "cheapest read-only credential-check endpoint" is a plan-phase decision documented in the chunk's plan.md. The endpoints are external dependencies whose stability is the operator's risk; the plan pins them and code review audits them.
- The audit-log entry that records "validator ran and verdict was X" is emitted by the supervisor (SDD-24), not by the validator itself. Validators are pure functions of (credential, upstream response) → typed verdict; they have no audit responsibilities.
- The validator subsystem runs on the supervisor host and reaches out to the public internet to probe each provider. This contradicts the vault-server's outbound-internet isolation but is the entire point of "validators on the supervisor, not on the vault" (`docs/DAEMONS.md` §5). The supervisor host is acceptable to have outbound internet; the vault host is not.
- Custom (operator-authored) validators are explicitly post-v0.1.0 (`docs/DAEMONS.md` §5). This chunk ships the five builtins and does not introduce a runtime extension API beyond what the `Validator` interface implies. The registry is closed to the five fixed names.
