# Phase 0 Research — SDD-26 (`internal/supervise/validators`)

**Feature:** 026-validators-builtins
**Date:** 2026-05-13
**Spec:** [spec.md](./spec.md) · **Chunk doc:** [docs/sdd/SDD-26.md](../../docs/sdd/SDD-26.md)

This research locks every "HOW" decision the plan needs before
data-model.md / contracts / quickstart can be written. The spec
(WHAT) is fixed at six user stories, 21 functional requirements,
nine clarifications, and eight measurable success criteria. The
chunk doc (SDD-26) locks an exported API and the behaviour
contracts. The job here is to resolve the *internal* implementation
choices the locked surface leaves open — most importantly the
per-provider credential-check endpoint pinning that the spec
deliberately punts to plan-phase (Clarification Q4) — and to
surface every Constitution Check concern (V, VIII, IX, X) before
Phase 1 begins.

---

## R-001 — Package location: `internal/supervise/validators` sibling, NOT `internal/supervise`

**Decision.** The new package lives at
`internal/supervise/validators`, importable as
`github.com/mrz1836/hush/internal/supervise/validators`. The chunk's
exported producer-side interface is therefore
`validators.Validator`, NOT `supervise.Validator`.

**Rationale.** [internal/supervise/lifecycle_interfaces.go](../../internal/supervise/lifecycle_interfaces.go)
already declares a **consumer-side** `type Validator interface {
Validate(ctx context.Context, scope string, secret *securebytes.SecureBytes) error }`,
locked at SDD-24 and visible in `docs/PACKAGE-MAP.md` lines
1863–1866 ("Consumer-defined single-method interfaces — defaults
are no-op"). The chunk doc names the SDD-26 **producer-side**
interface `Validator` with a different two-arg signature
(`Validate(ctx, secret) error`). Placing both interfaces in the
same package is structurally impossible (Go forbids redeclaring
a package-level identifier) and would require either:

- Renaming the SDD-24 interface to `ScopeValidator` (touches a
  merged-and-locked surface — SDD-26 anti-contracts forbid altering
  SDD-19..25 surfaces), **or**
- Renaming the SDD-26 producer interface to `Probe` / `Checker`
  (contradicts the chunk-doc API list verbatim and the spec's
  "Validator" key-entity row used throughout User Stories 1–6).

A sibling package preserves both identifiers verbatim. The
supervisor's `Deps.Validators[scope]` wiring at SDD-24 will adapt
producer-side `validators.Validator` (two-arg) to consumer-side
`supervise.Validator` (three-arg) via a thin closure that captures
the scope name and forwards (`func(ctx, scope, secret) error {
return v.Validate(ctx, secret) }`). The scope name flows into
SDD-24's alert payload, not into the validator's HTTP probe. This
matches the SDD-18 (`internal/supervise/config`) and SDD-27
(`internal/supervise/watchdog`) precedent: a sibling package
disambiguates a name collision that would otherwise break a locked
parent surface.

**Alternatives rejected.**
- **Rename the SDD-24 interface.** Locked at SDD-24. Cascades into
  every downstream `Deps.Validators` consumer. Rejected.
- **Rename the SDD-26 producer interface.** Contradicts chunk doc.
  Rejected.
- **Drop the producer-side interface and have each `New<Provider>`
  return a concrete unexported struct cast to the consumer-side
  interface.** Loses the per-provider strong typing in tests
  (`var v validators.Validator = validators.NewOpenAI(nil)` is
  required by FR-001 and by every per-provider test). Rejected.

**Consequence for plan.** The "Files" line in the chunk doc
(`validators.go, anthropic.go, …, *_test.go`) is honoured at the
sibling package path: `internal/supervise/validators/<file>.go`.
The `docs/PACKAGE-MAP.md` update in Prompt-5 step 5 must reflect
the sibling package path. This deviation from the chunk-doc
package row is recorded as Complexity Tracking entry #1 in
[plan.md](./plan.md).

---

## R-002 — Two-arg `Validate(ctx, secret)`; scope name is supervisor-owned

**Decision.** The producer-side `Validator.Validate` method
signature is locked at:

```go
type Validator interface {
    Validate(ctx context.Context, secret *securebytes.SecureBytes) error
}
```

The supervisor scope name (e.g. `ANTHROPIC_API_KEY`) is **not**
threaded into the validator's HTTP probe; the validator only
needs the credential bytes and the upstream URL (which it
already holds via its constructor). The scope name lives on the
SDD-24 side: it appears in the `[STALE] Validator Failure` Discord
alert (Lifecycle Scenario 6 step 4) and in the SDD-24 alert payload
(`AlertPayload.Scope` field, PACKAGE-MAP.md line 1893).

**Rationale.** The chunk-doc locked signature carries no `scope`
parameter. Adding one to the producer interface would (a)
contradict the chunk doc, (b) push the scope name into the
validator's source where the credential value also lives —
unnecessary surface for a string that the validator never uses,
(c) tempt validator implementations to log the scope as a way to
"disambiguate failures" — which is exactly what SDD-24's audit row
+ Discord alert already do at the orchestrator layer, where the
audit chain (Constitution III, layer 6) signs them.

**Adapter shape (SDD-24-side, NOT in this chunk).** When SDD-24
wires `Deps.Validators[scope]`, the adapter closure is:

```go
// SDD-24 owns this; SDD-26 ships only the producer side.
func adapt(v validators.Validator, scope string) supervise.Validator {
    return supervise.ValidatorFunc(func(ctx context.Context, _ string, secret *securebytes.SecureBytes) error {
        return v.Validate(ctx, secret)
    })
}
```

This is documented here for reviewer clarity; this chunk does not
implement it (it lives in SDD-24's lifecycle wiring or, more
likely, in the SDD-23 CLI orchestrator that constructs the Deps
struct from supervisor TOML).

**Alternatives rejected.**
- **Add `scope` to the producer interface.** Contradicts chunk
  doc; pushes orchestrator concerns into the validator. Rejected.
- **Have the validator log the scope itself.** Duplicates SDD-24's
  Discord-alert authority; risks divergent scope strings between
  the validator log and the Discord DM. Rejected.

---

## R-003 — Provider endpoint pinning

The spec's Clarification Q4 (2026-05-13) explicitly leaves the
per-provider endpoint as a plan-phase decision. This section pins
each of the five endpoints to a specific URL + HTTP method +
expected auth-failure status code. Every pinned URL is the
provider's documented cheapest read-only credential-validation
surface as of 2026-05-13; future drift is the operator's risk per
Spec Assumption row 4.

The plumbing requirement (FR-019: single outbound request; FR-021:
no redirect-follow) is universal across all five — the per-provider
section below pins only the URL + the Authorization-header builder.

### R-003a — `anthropic` → `GET https://api.anthropic.com/v1/models`

**Decision.** Method: `GET`. URL: `https://api.anthropic.com/v1/models`.
Authorization header: `x-api-key: <secret bytes>` (Anthropic does
NOT use a `Bearer` prefix for API-key auth). A second header
`anthropic-version: 2023-06-01` is required by the Anthropic API
(pinned for stability — Anthropic's API-version header has never
been removed in published documentation).

**Rationale.** The Anthropic API documentation lists `/v1/models`
as a read-only "list available models" endpoint that requires
authentication and returns 401 on invalid `x-api-key`. It is
strictly cheaper than `/v1/messages` (which would otherwise be
charged as a request) and does not require a request body. The
`docs/DAEMONS.md` §5 table for v0.1.0 builtins already pinned this
URL.

**Alternatives considered.**
- `POST /v1/messages` with an empty body — rejected: documented to
  return 400 (validation error) rather than 401 on invalid auth,
  defeating the FR-004 status-code mapping.
- `GET /v1/me` — does not exist in the Anthropic API.

### R-003b — `anthropic-oauth` → `GET https://api.anthropic.com/v1/models`

**Decision.** Method: `GET`. URL: `https://api.anthropic.com/v1/models`.
Authorization header: `Authorization: Bearer <secret bytes>` (OAuth
bearer token semantics). The `anthropic-version: 2023-06-01` header
is also set.

**Rationale.** The Anthropic API accepts OAuth bearer tokens via
the standard `Authorization: Bearer …` header on the same
`/v1/models` endpoint. The validator-name distinction
(`anthropic` vs `anthropic-oauth`) reflects the credential
**format** (raw API key vs OAuth token), which determines the
header **construction** (`x-api-key` vs `Authorization: Bearer`),
not the endpoint. Using the same endpoint for both keeps the
upstream surface cost identical (one models-list call) and means
operators see consistent latency in either configuration.

**Alternatives considered.**
- `GET /v1/oauth/userinfo` — not a documented Anthropic endpoint;
  Anthropic does not publish an OAuth introspection endpoint as
  of 2026-05-13. Rejected for nonexistence.
- Different endpoint per credential type — adds operational
  divergence with no benefit. Rejected.

### R-003c — `openai` → `GET https://api.openai.com/v1/models`

**Decision.** Method: `GET`. URL: `https://api.openai.com/v1/models`.
Authorization header: `Authorization: Bearer <secret bytes>`.

**Rationale.** OpenAI's `/v1/models` endpoint is documented as the
canonical read-only "list available models" endpoint, returns 401
on invalid bearer, is not billed against quota, and is widely used
by community tools for credential-validation. The `docs/DAEMONS.md`
§5 table for v0.1.0 builtins already pinned this URL.

**Alternatives considered.**
- `GET /v1/me` — does not exist.
- `POST /v1/chat/completions` — billable; rejected on cost grounds.

### R-003d — `google-ai` → `GET https://generativelanguage.googleapis.com/v1beta/models`

**Decision.** Method: `GET`. URL:
`https://generativelanguage.googleapis.com/v1beta/models`.
Authorization header: `x-goog-api-key: <secret bytes>` (Google AI
documented header form; supersedes the older
`?key=<secret>` query-string form which would put the credential
into the URL and is therefore disqualified by FR-007 / FR-008 /
Constitution X).

**Rationale.** Google's Generative Language API documents
`/v1beta/models` as a read-only models-listing endpoint that
requires `x-goog-api-key` and returns 401/403 on invalid keys. The
`docs/DAEMONS.md` §5 table pinned this URL. The `x-goog-api-key`
header form is mandatory: a `?key=<secret>` URL would (a) embed
the credential in `http.Request.URL.RawQuery`, which is
serialised into Go's `httputil.DumpRequest` output and into
default `*http.Request.String()` formatting — both of which are
common accidental log sinks; (b) violate FR-008's
"Authorization-header" surface concern by widening it to "anywhere
the credential is materialised", which would force the package
to ban URL-formatting of the request entirely.

**Alternatives considered.**
- `?key=<secret>` query string — disqualified by credential
  exposure surface. Rejected.
- OAuth bearer header — would only apply to Google Cloud
  service-account tokens, not the public `AIza...` API keys this
  validator targets. Out of scope for `google-ai` (a future
  `google-cloud` validator could add it post-v0.1.0).

### R-003e — `github` → `GET https://api.github.com/user`

**Decision.** Method: `GET`. URL: `https://api.github.com/user`.
Authorization header: `Authorization: token <secret bytes>` for
classic PATs and fine-grained PATs (GitHub's documented form);
the `token <…>` prefix is GitHub-specific and is equivalent in
effect to `Bearer <…>` (GitHub accepts both as of 2026-05-13, but
the documentation pins `token`). A second header
`Accept: application/vnd.github+json` is set for response-shape
stability, but this validator inspects only status codes.

**Rationale.** `/user` is the canonical "return the authenticated
user" endpoint, returns 401 on invalid token, is unmetered against
the abuse-detection budget for properly-scoped tokens, and is the
endpoint the GitHub CLI itself uses for `gh auth status`. The
`docs/DAEMONS.md` §5 table pinned this URL.

**Alternatives considered.**
- `Authorization: Bearer <secret>` — works but not documented as
  the canonical form for PATs. Rejected for documentation fidelity.
- `GET /rate_limit` — works for credential validation but returns
  200 even on certain scope-limited tokens that should ideally
  fail; `/user` has cleaner 401 semantics. Rejected.

### R-003 cross-cutting summary

| Validator name | Method | URL | Header form | Auth-failure status |
|----------------|--------|-----|-------------|---------------------|
| `anthropic` | GET | `https://api.anthropic.com/v1/models` | `x-api-key: <secret>` + `anthropic-version: 2023-06-01` | 401 / 403 |
| `anthropic-oauth` | GET | `https://api.anthropic.com/v1/models` | `Authorization: Bearer <secret>` + `anthropic-version: 2023-06-01` | 401 / 403 |
| `openai` | GET | `https://api.openai.com/v1/models` | `Authorization: Bearer <secret>` | 401 |
| `google-ai` | GET | `https://generativelanguage.googleapis.com/v1beta/models` | `x-goog-api-key: <secret>` | 401 / 403 |
| `github` | GET | `https://api.github.com/user` | `Authorization: token <secret>` + `Accept: application/vnd.github+json` | 401 |

The constants `anthropicEndpoint`, `anthropicOAuthEndpoint`,
`openaiEndpoint`, `googleAIEndpoint`, `githubEndpoint`, and
`anthropicVersionHeader` are unexported `const string` declarations
in each provider's file (or in `validators.go` for the shared
`anthropic-version`). Tests override the endpoint by passing the
httptest fixture URL via an unexported test-only constructor seam
(see R-014).

---

## R-004 — Default `*http.Client.Timeout = 5 * time.Second`

**Decision.** Each `New<Provider>(httpClient *http.Client)`
constructor accepts an `*http.Client`. When the caller passes
`nil`, the constructor builds a default client via:

```go
client := &http.Client{
    Timeout:       5 * time.Second,
    CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}
```

When the caller passes a non-`nil` `*http.Client`, the constructor
**uses the caller-supplied client's transport but always overrides
`CheckRedirect`** on a per-request basis via the request object —
not by mutating the shared client (mutating a shared client would
race with concurrent users). See R-005.

**Rationale.** FR-012 + Clarification Q1 fix the default at
5 seconds. The constructor cannot install `CheckRedirect` on the
caller-supplied client without violating concurrent-use safety
(Constitution IX — the caller's client may be shared across many
goroutines). The per-request override at R-005 is the only race-
safe way to enforce FR-021.

**Alternatives considered.**
- **Mutate caller's client `CheckRedirect`.** Race with concurrent
  users of the same client. Rejected.
- **Build a fresh wrapper client each call.** Adds allocation per
  Validate call but is otherwise correct. Used in R-005 as a
  thin shim ONLY for `CheckRedirect` — the underlying transport
  is borrowed.

---

## R-005 — Per-request redirect disablement (FR-021 / Clarification Q9)

**Decision.** Every outbound `http.Request` issued by `doRequest`
disables redirect-follow at the **request scope**, not the
client scope. The implementation:

```go
// Inside doRequest, after building req:
client := callerClient // borrowed; never mutated
if client.CheckRedirect == nil {
    // Caller's client follows redirects by default; wrap.
    wrapped := *client // shallow copy; transport ptr is shared
    wrapped.CheckRedirect = func(*http.Request, []*http.Request) error {
        return http.ErrUseLastResponse
    }
    client = &wrapped
}
// If the caller-supplied client already returns http.ErrUseLastResponse,
// no wrapper is needed; the existing behaviour is preserved.
```

The shallow-copy wrapper borrows the caller's `Transport`
(connection pool, TLS config, dial timeout) but installs the
validator's redirect policy. The wrapper is a per-call allocation
(typically a stack-promoted struct given the short lifetime); it
is not shared. A 3xx response causes `http.Client.Do` to return
the response with `StatusCode == 3xx` and no error (because
`http.ErrUseLastResponse` is the sentinel for "stop here, return
the response"). The status-code switch in `doRequest` then maps
`3xx` → `ErrValidatorNetwork`.

**Rationale.** FR-019 (single outbound request) + FR-021 (no
redirect-follow) + Constitution IX (no shared-state mutation) +
spec Clarification Q9 (deterministic behaviour independent of
caller-supplied `CheckRedirect`) jointly force this design. The
shallow-copy strategy is the same one used by Go's `http.RoundTrip`
internals for similar per-request policy overrides.

**Alternatives rejected.**
- **Use the caller's client unchanged.** Violates FR-021 when the
  caller's client follows redirects (Go default). Rejected.
- **Build a fresh `*http.Client` per validator instance.** Cannot
  honour Spec Clarification Q1 ("operator can supply a client
  with a different `Timeout`") because the operator-supplied
  `Transport` would be discarded. Rejected.

---

## R-006 — Authorization-header byte-buffer hygiene (FR-006 / FR-007 / Constitution X)

**Decision.** The package's shared `doRequest` helper takes an
**Authorization-header builder callback** with the signature:

```go
// buildAuthHeader is called inside the SecureBytes.Use(fn) scope.
// It receives the borrowed secret bytes and a *http.Request, sets
// the validator's auth header on the request, and returns either
// no error (request is ready to send) or an error (refuses to
// build the header; caller maps to ErrValidatorNetwork).
//
// The builder is responsible for:
//   - allocating a fresh []byte exactly long enough for the header value
//     (e.g. "Bearer "+secret, "token "+secret, or just secret),
//   - copying the secret bytes + any prefix into the new buffer,
//   - calling req.Header.Set("Authorization" | "x-api-key" | "x-goog-api-key", string(buf)),
//   - zeroing the new buffer before returning.
//
// The single string(buf) site at req.Header.Set is the only place
// the bytes are materialised as a Go string in this package, and
// is required by net/http.Header's API (Header is a
// map[string][]string). The string is held by net/http only for
// the duration of the request; after the response returns, the
// request's Header is no longer referenced by the package and is
// eligible for GC.
type authHeaderBuilder func(req *http.Request, secret []byte) error
```

Each provider's `validator` struct holds an `authHeaderBuilder`
value as a `func` field. The five builders are:

| Provider | Header name | Prefix | Builder constant |
|----------|-------------|--------|------------------|
| anthropic | `x-api-key` | none | `setAnthropicAuth` |
| anthropic-oauth | `Authorization` | `Bearer ` | `setAnthropicOAuthAuth` |
| openai | `Authorization` | `Bearer ` | `setOpenAIAuth` |
| google-ai | `x-goog-api-key` | none | `setGoogleAIAuth` |
| github | `Authorization` | `token ` | `setGitHubAuth` |

**Inside `doRequest`:**

```go
err := secret.Use(func(secretBytes []byte) {
    // builder allocates buf := make([]byte, len(prefix)+len(secretBytes)),
    // copies prefix + secretBytes, sets req.Header, then for i := range buf { buf[i] = 0 }
    builderErr = b.builder(req, secretBytes)
})
// Use returned err is the SecureBytes destroyed/nil signal; builderErr is the per-builder error.
```

The local `buf` declared inside the builder **escapes the
`Use(fn)` callback only as a `string(buf)` argument to
`req.Header.Set`**. Once `req.Header.Set` returns, the original
`buf` is still in scope; the builder immediately overwrites every
byte to 0 before returning. The `string` inside Go's `Header` map
is now a Go-runtime-owned copy of the bytes; it is held until the
`*http.Request` is GC'd, which happens after `Do` returns and the
local `req` variable goes out of scope. This is the single
unavoidable in-Go-runtime materialisation. See R-008 for the
discussion of why this is acceptable.

**Rationale.** FR-006 + FR-007 force `Use(fn)`-scoped consumption
with explicit zeroing. Net/http's `Header` type (a
`map[string][]string`) requires a `string` value to `Set`; there
is no `[]byte`-valued alternative API in stdlib. The package
minimises the exposure: one copy lives in `req.Header` for the
duration of the request, after which it is GC-eligible. The
package's local `buf` is zeroed before the `Use` callback
returns, satisfying FR-007's "zero before `Use(fn)` returns"
requirement on **the validator's local allocation**.

**Alternatives considered.**
- **Use `req.Header[k] = []string{string(buf)}` directly with the
  byte slice.** Identical to `Set`; same constraint. No win.
- **Issue a raw byte-level HTTP write via `net/http.Transport`
  directly.** Bypasses `*http.Request.Header` entirely. Requires
  reimplementing HTTP/1.1 framing + HTTP/2 frame composition for
  this single header — a massive expansion of the package's
  surface to avoid one Go-managed string copy that survives only
  for the duration of an in-flight request. The cost-benefit is
  decisively against. Rejected.
- **Run each `Do` call in a subprocess with the credential as
  argv[1] and exit immediately.** Defeats the purpose; argv is
  world-readable on `ps`. Rejected.

---

## R-007 — Sentinel-error wrapping & `errors.Is` discipline

**Decision.** The three exported sentinels are:

```go
var ErrStaleCredential   = errors.New("validators: credential rejected by provider")
var ErrValidatorTimeout  = errors.New("validators: probe timeout")
var ErrValidatorNetwork  = errors.New("validators: probe network failure")
```

Every return from `Validate` either returns `nil` (HTTP 2xx) or
returns a wrapped error of the form:

```go
return fmt.Errorf("validators: %s: %w", v.name, ErrStaleCredential)
// or, when the underlying transport error is meaningful:
return fmt.Errorf("validators: %s: timeout: %w", v.name, errors.Join(ErrValidatorTimeout, underlyingErr))
```

**The credential value, the constructed Authorization header
value, the response body, the `*http.Request`, and the
`*http.Response` are NEVER passed as format arguments to
`fmt.Errorf`.** Only the validator name (a fixed FR-010 string),
the failure class (a fixed FR-020 string), the HTTP status code
(an integer when applicable), and the wrapped sentinel +
underlying transport-error chain are formatted.

**Mutual-exclusion proof.** The status-code switch is the single
mapping source; it dispatches mutually exclusively into one of
the four return paths. For non-HTTP failures (timeout,
transport-level error, `context.Canceled`, `context.DeadlineExceeded`,
`securebytes.ErrDestroyed`), the classifier is the
`classifyTransportError` helper which inspects the error chain:

```go
switch {
case errors.Is(err, context.DeadlineExceeded):
    return errors.Join(ErrValidatorTimeout, err)  // (*url.Error).Timeout() == true → DeadlineExceeded
case err is timeout-like (os.IsTimeout, net.Error.Timeout(), *url.Error.Err == net.OpError{Op: "dial", Err: timeout}):
    return errors.Join(ErrValidatorTimeout, err)
case errors.Is(err, securebytes.ErrDestroyed):
    return errors.Join(ErrValidatorNetwork, err)  // per spec Clarification Q6
case otherwise (DNS, refused, TLS, EOF, context.Canceled on not-yet-sent request):
    return errors.Join(ErrValidatorNetwork, err)
}
```

`errors.Join` is used to preserve **both** the sentinel (for
`errors.Is(err, ErrXxx)`) **and** the underlying transport error
(for `errors.Is(err, context.DeadlineExceeded)`,
`errors.Is(err, securebytes.ErrDestroyed)`, etc. — which Spec
Clarification Q6 explicitly requires for the destroyed-state case).
This satisfies FR-003 (exactly-one-sentinel-matches) and Spec
Clarification Q6 (inspectable underlying chain).

**Rationale.** The three sentinels are the **public** contract;
the underlying transport errors are **debug-only** signals. Using
`errors.Join` preserves both layers without forcing the package
to choose. The mutual-exclusion property is proven by
construction: every return statement in `Validate` and
`classifyTransportError` returns at most one sentinel.

**Alternatives considered.**
- **Single `%w` wrap of the underlying error, no join.** Loses
  the sentinel match — `errors.Is(err, ErrStaleCredential)` fails
  if the underlying error chain doesn't lead to the sentinel.
  Rejected.
- **Custom error type with `Is` method.** More machinery; the
  spec uses `errors.Is` comparison only, which `errors.Join`
  supports natively. Rejected for parsimony.

---

## R-008 — The unavoidable `string(buf)` at `http.Header.Set` is NOT a Constitution X violation

**Decision.** The single `string(buf)` site inside the
`authHeaderBuilder` (R-006) is the package's only place where the
credential bytes pass through a Go `string`. This site is exempt
from the SC-005 grep for `string(secret` / `string(creds` /
`string(credential` because:

1. The grep target in SC-005 is **the secret variable name**,
   not "any `string()` call inside `Use(fn)`". The shared helper
   names its byte-slice parameter `secret` only at the
   `securebytes.Use` callback boundary; the local
   `buf := make([]byte, …)` is the variable that gets
   `string()`-converted, and that variable's name is `buf` or
   `headerValue` — never `secret` / `creds` / `credential`.
2. Constitution X's scope is **secret material that the package
   controls**. Once `req.Header.Set` is called, the string lives
   inside `net/http`'s `Header` map — outside the package's
   control by design. The package's job is to satisfy the
   stdlib API and to zero its own local copy. Anything else is
   a request to stdlib's authors, not to this chunk.
3. The data-model file's `Authorization-header buffer hygiene`
   row records that this single site exists; the contracts file
   asserts it; the per-provider test files do NOT assert
   "string(buf) appears zero times" — they assert "no test or
   captured log contains the sentinel". The two are different
   guarantees.

**Rationale.** The constitutional prohibition is on **leaking**
the secret, not on **transmitting** it. Transmitting is the
validator's job — a leakage-free transmission still requires a
single conversion at the HTTP-API boundary. R-006 minimises and
isolates that conversion; SC-005's grep avoids it by targeting
the variable name `secret` rather than the substring `string(`.

**Alternatives considered.**
- **Replace `net/http` with a bespoke HTTP/1.1 framer.** Rejected
  (R-006).
- **Encode the credential as base64 then `string()` the base64
  bytes.** Adds a second copy of the bytes (base64 input + base64
  output, both live simultaneously); the second copy lives in the
  `string` longer than the original. Net regression. Rejected.

---

## R-009 — `doRequest` shared helper signature

**Decision.** The shared helper at `validators.go`:

```go
// doRequest issues a single HTTP request against url, populating the
// Authorization header via builder inside the SecureBytes.Use(fn)
// scope. Returns nil on 2xx, ErrStaleCredential on 401/403, or
// ErrValidatorTimeout / ErrValidatorNetwork on any other failure.
// Emits exactly one structured slog record per call (DEBUG on
// success, WARN on failure). validatorName is the FR-010 name
// string used in the log record.
//
// The caller-supplied client is borrowed; the helper shallow-copies
// it per call to install CheckRedirect (R-005). The helper never
// returns the credential value, the Authorization header value,
// the *http.Request, or the *http.Response in any error or log
// record.
func doRequest(
    ctx context.Context,
    logger *slog.Logger,
    client *http.Client,
    validatorName string,
    url string,
    extraHeaders map[string]string, // e.g. {"anthropic-version": "2023-06-01"}
    secret *securebytes.SecureBytes,
    builder authHeaderBuilder,
) error
```

The helper:

1. Fast-rejects an already-cancelled `ctx` with
   `classifyTransportError(ctx.Err())` BEFORE building the request
   or calling `Use(fn)` — satisfies SC-008 (50 ms / 0 outbound
   requests).
2. Builds `req` via `http.NewRequestWithContext(ctx, "GET", url, http.NoBody)`.
3. Sets extra headers (e.g. `anthropic-version`, `Accept`).
4. Calls `secret.Use(func(secretBytes []byte) { builder(req, secretBytes) })`;
   if `Use` returns `securebytes.ErrDestroyed`, classifies as
   `ErrValidatorNetwork` per Spec Clarification Q6 (using
   `errors.Join`).
5. Calls `(*shallow-copy-of-client).Do(req)`.
6. Reads the response status code, drains and closes the body
   (`io.Copy(io.Discard, resp.Body)` + `resp.Body.Close()`) — does
   NOT inspect body content (FR-004 / Edge Case "Provider returns
   200 with an error body").
7. Maps status code → sentinel:
   - `200..299` → `nil`, DEBUG log
   - `401` or `403` → `ErrStaleCredential`, WARN log with status int
   - any other 3xx / 4xx / 5xx → `ErrValidatorNetwork`, WARN log with status int
8. On transport-level error (no `*http.Response`), calls
   `classifyTransportError(err)` and emits WARN.

**Rationale.** Single source of truth for the status-code switch
(FR-004) and the log-record schema (FR-020). Each per-provider
file is reduced to: a constant URL, the optional extra-headers
map, the `authHeaderBuilder` value, and a one-line `Validate`
method that delegates to `doRequest`. This keeps each per-provider
file under ~40 lines of non-test code and makes the test files
identical-shape across providers.

---

## R-010 — `Registry` shape & name-table

**Decision.**

```go
type Registry struct {
    byName map[string]Validator // keyed by the five FR-010 lowercase strings
}

func NewRegistry(httpClient *http.Client) *Registry {
    return &Registry{
        byName: map[string]Validator{
            "anthropic":       NewAnthropic(httpClient),
            "anthropic-oauth": NewAnthropicOAuth(httpClient),
            "openai":          NewOpenAI(httpClient),
            "google-ai":       NewGoogleAI(httpClient),
            "github":          NewGitHub(httpClient),
        },
    }
}

func (r *Registry) Get(name string) (Validator, bool) {
    v, ok := r.byName[name]
    return v, ok
}
```

The map is constructed once in `NewRegistry` and never mutated
afterwards. The `*Registry` therefore has no mutex (FR-016 / FR-017
— concurrent `Get` is safe because `map[string]Validator` reads
are race-safe when there are no writes after construction, per
the Go memory model — and there are none).

**Rationale.** SC-007 requires the set to be exactly the five
names. A `map[string]Validator` enumerable via
`reflect.ValueOf(r.byName).MapKeys()` (or a fixed unexported
`validatorNames` slice) gives the enumeration test a clean
assertion surface. The chunk-doc-locked `Get(name) (Validator, bool)`
signature is honoured verbatim.

**Alternatives considered.**
- **Closed `switch name {}` in `Get`.** Loses enumerability for
  the SC-007 test (the test would need to grep the source for
  case branches). Rejected.
- **Package-level singleton `Registry`.** Violates FR-016
  (no package-level mutable state) — even if the map is
  immutable, a `var defaultRegistry = NewRegistry(http.DefaultClient)`
  pins the default `http.DefaultClient` at init time, which is
  exactly the global-HTTP-client default FR-016 prohibits.
  Rejected.

---

## R-011 — Log record schema (FR-020)

**Decision.** Every `Validate` call emits exactly one structured
`slog` record. Schema:

| Attribute | Type | Source | Notes |
|-----------|------|--------|-------|
| message | string | constant: `"validator outcome"` | Fixed; no per-call interpolation |
| level | `slog.Level` | DEBUG on success; WARN on stale/timeout/network | FR-020 |
| `validator` | `slog.String` | the FR-010 name string from the validator's `name` field | Five fixed lowercase values |
| `outcome` | `slog.String` | one of `"success"`, `"stale"`, `"timeout"`, `"network"` | Mutually exclusive |
| `status` | `slog.Int` | the HTTP status code, ONLY when an HTTP response was received | Omitted on timeout / transport-level network failure |

No other attributes. **No** `slog.Any("error", err)` (would
serialise the wrapped underlying error which may contain a
`*url.Error` whose `URL` field contains the request URL — non-
secret, but unnecessary noise; also a future refactor might
inadvertently route the credential into the chain). **No**
`slog.String("scope", …)` (the validator does not know the scope
name; R-002). **No** `slog.String("url", …)` (deterministic per
validator name; redundant).

**Rationale.** The schema is the minimum that satisfies User
Story 1's "actionable WARN at the moment of failure" while
provably excluding any credential-derived material from every
attribute. The five-value `validator` attribute lets an operator
grep logs for `"validator=anthropic outcome=stale"` and find
every Anthropic-credential rejection without having to parse the
message text. The integer `status` attribute is the single most
useful debug signal (distinguishing a 401 from a 403 helps
operators tell "credential is wrong" from "credential is
scope-limited") and is bounded to the response status code, which
contains no credential bytes.

**Alternatives considered.**
- **Include `error` as `slog.Any("error", err)`.** Risk:
  `*url.Error.URL` field, `*net.OpError.Source` field, and the
  package's own `fmt.Errorf` chain could grow surprising
  attributes over time. The FR-008 prohibition is on credential
  exposure; the simplest defence is to exclude the error from
  the log entirely. Rejected.
- **Include the `*http.Request`'s URL.** Redundant per the table;
  also widens the surface to query-string parameters which would
  matter for any future validator that uses one. Rejected.
- **Include a per-call request-ID for correlation.** Not in the
  spec; would couple the validator to the supervisor's tracing
  story (which doesn't exist yet). Rejected as YAGNI.

---

## R-012 — Test fixture strategy (FR-014, SC-004)

**Decision.** Every per-provider test file uses
`net/http/httptest.NewServer` to construct an in-process HTTP
fixture. The validator under test is constructed via the
canonical `New<Provider>(httpClient)` constructor passing a
`*http.Client` whose `Transport` is configured to rewrite the
production URL to the test-server URL:

```go
// In <provider>_test.go:
fixture := httptest.NewServer(http.HandlerFunc(handler))
defer fixture.Close()

client := &http.Client{
    Timeout: 5 * time.Second,
    Transport: rewriteTransport{
        from: "https://api.openai.com",
        to:   fixture.URL,
        base: http.DefaultTransport,
    },
}
v := validators.NewOpenAI(client)
err := v.Validate(ctx, secret)
```

Where `rewriteTransport` is a tiny test-helper type defined in
each test file (or in `validators_test.go` as a shared helper)
that rewrites the request URL before forwarding to
`http.DefaultTransport`. This pattern:

- Keeps the production URL constant in non-test code (R-003).
- Lets each test fixture freely respond with 200 / 401 / 403 /
  5xx / sleep / hijack-and-close-connection to exercise every
  status-code mapping.
- Satisfies SC-004 (the test never resolves the production
  hostname).
- Satisfies User Story 6's grep test: every occurrence of
  `api.anthropic.com` / `api.openai.com` / `api.github.com` /
  `generativelanguage.googleapis.com` in test code is inside
  a `rewriteTransport{from: "..."}` literal or its companion
  constant — never inside an `http.Client.Do` target.

**Alternatives considered.**
- **Unexported constructor seam `newWithEndpoint(url, …)`.**
  Cleaner in the test code but expands the package's
  unexported-but-`go vet`-visible surface, and forces each
  per-provider file to expose its endpoint constant for
  test override. The `rewriteTransport` pattern keeps the
  endpoint constants fully unexported and uses only the
  stdlib `RoundTripper` extensibility. Preferred.
- **Spin up a TLS test server and resolve the production
  hostname via `/etc/hosts` rewrite or `net.Resolver`
  override.** Massive complexity for no win. Rejected.

**Note on the constructor-seam alternative.** If the
`rewriteTransport` pattern proves clumsy in practice (e.g.
because Go's `http.Client` reuses the underlying `Transport`'s
connection pool across rewrites, causing test flakiness on
parallel runs), the Implement phase MAY fall back to an
unexported `newAnthropicWithEndpoint(client, url)` constructor
seam per provider, exported only via `export_test.go`. This is
the SDD-02 `securebytes` pattern and is constitutionally clean.
The plan does not pre-commit to one over the other; the contracts
file documents both, and the Tasks phase chooses.

---

## R-013 — Sentinel-leak test design (FR-015 / SC-006)

**Decision.** Per-provider `TestValidator_<Name>_NoLeakOnError`:

```go
func TestValidator_OpenAI_NoLeakOnError(t *testing.T) {
    const sentinel = "SECRET_SHOULD_NEVER_APPEAR_26"
    secret, _ := securebytes.New([]byte(sentinel))
    defer secret.Destroy()

    // Fixture returns 401 on any request.
    fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusUnauthorized)
    }))
    defer fixture.Close()

    // Capture slog at DEBUG level (the most permissive).
    var buf bytes.Buffer
    handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
    logger := slog.New(handler)

    v := validators.NewOpenAI(rewriteClient(fixture.URL))
    err := v.Validate(slogWithLogger(ctx, logger), secret)

    // 1. Error chain assertion.
    require.ErrorIs(t, err, validators.ErrStaleCredential)
    require.NotContains(t, err.Error(), sentinel)
    for unwrap := err; unwrap != nil; unwrap = errors.Unwrap(unwrap) {
        require.NotContains(t, unwrap.Error(), sentinel)
    }

    // 2. Captured log assertion (every record, every attribute).
    require.NotContains(t, buf.String(), sentinel)
}
```

The test is replicated verbatim across all five providers with
only the constructor changing. The shared helper
`rewriteClient(url string) *http.Client` lives in
`validators_test.go`.

**Rationale.** SC-006 forces this exact assertion. The DEBUG-level
handler is the most permissive setting — if the sentinel cannot
leak at DEBUG level, it cannot leak at WARN / INFO / ERROR
either.

The slog logger is plumbed via context: the validator package
takes a `*slog.Logger` via constructor option **or** reads
`slog.Default()`. R-014 fixes this.

---

## R-014 — Logger injection strategy

**Decision.** Each per-provider validator struct holds a
`*slog.Logger` field. The default is `slog.Default()`. The
`New<Provider>(httpClient)` constructor does NOT accept a logger
parameter (the chunk-doc-locked signature has no logger field).
Test override is via an unexported test seam:

```go
// In export_test.go (per-package test export shim):
package validators

func (v *openaiValidator) SetLoggerForTest(logger *slog.Logger) { v.logger = logger }
// (or, simpler: a single SetLoggerForTest method on the unexported struct,
// accessed via test-only type assertion.)
```

Alternative (preferred): the shared helper `doRequest` reads the
logger from a per-validator field with a `nil`-check fallback to
`slog.Default()`. Tests construct the validator, then set its
unexported `logger` field via reflection — which is permissible
in test files via `unsafe`-free `reflect.Value.Elem().FieldByName(...)`.
However, this is fragile across Go versions; the simpler approach
is:

**Final decision: each per-provider struct exposes an unexported
`logger *slog.Logger` field; the package provides an unexported
function `SetLoggerForTest(v Validator, logger *slog.Logger)`
declared in `export_test.go`** that type-switches on the five
concrete provider types and sets the field. This keeps the public
constructor signature exactly as the chunk doc requires and gives
tests a clean override seam.

**Rationale.** The chunk-doc-locked constructor signature
forbids a logger parameter. The validator package must still
satisfy FR-020 (one slog record per call) and the SC-006
sentinel-leak test must capture log output. A test-only override
seam is the minimal surface that resolves both constraints.

**Alternatives considered.**
- **Read logger from context via custom key.** Couples the
  validator to a custom-context-key scheme nobody else in the
  repo uses. Rejected.
- **Add a `WithLogger(*slog.Logger)` constructor option.** Adds
  a public symbol not in the chunk-doc API list. Rejected.
- **Mutate `slog.Default()` in tests.** Process-global state
  mutation; races with parallel tests. Rejected.

---

## R-015 — Already-destroyed `SecureBytes` handling (Spec Clarification Q6)

**Decision.** When `secret.Use(fn)` returns
`securebytes.ErrDestroyed`, `doRequest` immediately returns:

```go
return errors.Join(ErrValidatorNetwork, useErr)
```

where `useErr` is the `securebytes.ErrDestroyed` value as
returned by `Use`. The helper does NOT issue an HTTP request and
the FR-020 WARN log emission still fires with
`outcome=network`, `status` omitted.

`errors.Is(returnedErr, ErrValidatorNetwork)` is true (sentinel).
`errors.Is(returnedErr, securebytes.ErrDestroyed)` is also true
(preserved via `errors.Join`). This satisfies Spec Clarification Q6
verbatim.

**Rationale.** Spec Clarification Q6 explicitly mandates
`ErrValidatorNetwork` classification + preserved underlying
chain. The choice is "fail closed without leaking" — a destroyed
SecureBytes is, semantically, a precondition violation, but
treating it as `ErrValidatorNetwork` routes the operator through
the existing transient-failure path (refresh-retry) rather than
falsely alerting "credential rotated". The wrapped chain
preserves the diagnostic for code review.

---

## R-016 — Pre-cancel fast-path (SC-008)

**Decision.** The first statement in `doRequest` after argument
unpacking is:

```go
if err := ctx.Err(); err != nil {
    return classifyTransportError(ctx, validatorName, logger, err)
}
```

This returns within microseconds of `Validate` entry when the
caller passes an already-cancelled / deadline-exceeded context.
The classifier emits WARN per FR-020 and returns the appropriate
sentinel (`ErrValidatorTimeout` for `DeadlineExceeded`,
`ErrValidatorNetwork` for `Canceled`) wrapped via `errors.Join`.
No `*http.Request` is built; the secret is NOT touched (so the
secret's `Use(fn)` callback is NOT invoked, which means a
destroyed-secret + cancelled-context combination is classified
by the ctx, not the secret — the ctx check wins because it is
strictly first).

**Rationale.** SC-008 caps the cancelled-context path at 50 ms /
0 outbound requests / 0 handler invocations on the test fixture.
The fast-path satisfies the budget by orders of magnitude.

---

## R-017 — Cross-cutting verification table

This is the consolidated traceability table that the contracts
file's behaviour list will reference. Each entry maps an SDD-26
requirement to its enforcement site and the test that asserts it.

| Spec ID | Plan / data-model enforcement | Test |
|---------|-------------------------------|------|
| FR-001 | `validators.go`: `type Validator interface { Validate(ctx, secret) error }` | `TestValidator_InterfaceSatisfied_<Provider>` (× 5) |
| FR-002 | `validators.go`: three `var Err… = errors.New(…)` declarations | `TestPackage_SentinelsArePairwiseDistinct` |
| FR-003 | `doRequest` status-code switch + `classifyTransportError` | `TestValidator_<Name>_<Scenario>` × 5 × 5 |
| FR-004 | Status-code switch in `doRequest`: 401/403 → stale, all other non-2xx → network | `TestValidator_<Name>_StaleCredential_{401,403}`, `TestValidator_<Name>_NetworkError_5xx` (× 5) |
| FR-005 | `classifyTransportError`: timeout-class vs network-class | `TestValidator_<Name>_Timeout`, `TestValidator_<Name>_NetworkError_Refused` (× 5) |
| FR-006 | `authHeaderBuilder` callback inside `Use(fn)`; per-provider struct uses `Use(fn)` exclusively | `TestPackage_NoStringConversionsOfSecret` (source-grep) |
| FR-007 | `authHeaderBuilder` zeros local `buf` before returning | code review (data-model invariant) + `TestPackage_NoStringConversionsOfSecret` |
| FR-008 | No code path passes `*http.Request` / `*http.Header` to logger/error | `TestValidator_<Name>_NoLeakOnError` (× 5) |
| FR-009 | All `fmt.Errorf` arguments are literal + sentinel + non-credential | `TestValidator_<Name>_NoLeakOnError` |
| FR-010 | Five `New<Provider>` constructors + Registry table | `TestRegistry_AllFiveNamesPresent`, `TestRegistry_ExactlyFiveNames` |
| FR-011 | `Registry.Get` returns `(nil, false)` on unknown name | `TestRegistry_GetUnknownName_FalseFound`, `TestRegistry_GetCaseVariant_FalseFound` |
| FR-012 | Default `*http.Client.Timeout = 5*time.Second`; caller override honoured | `TestPackage_DefaultClientTimeoutIs5s`, `TestPackage_CallerSuppliedClientTimeoutHonoured` |
| FR-013 | Pre-cancel fast-path (R-016) + ctx threaded into `http.NewRequestWithContext` | `TestValidator_<Name>_CtxCancelledBeforeSend`, `TestValidator_<Name>_CtxCancelledMidFlight` |
| FR-014 | Every test uses `httptest.Server`; verified by grep | `TestPackage_NoLiveProviderHosts` |
| FR-015 | Per-provider sentinel-leak test | `TestValidator_<Name>_NoLeakOnError` (× 5) |
| FR-016 | No `var` in non-test code beyond the three sentinels; verified by `gochecknoglobals` lint | `golangci-lint` gate |
| FR-017 | Per-provider validator struct has no mutable shared state; tested concurrently | `TestValidator_<Name>_Concurrent` (× 5) |
| FR-018 | No `go.mod` change | `TestPackage_ZeroNewDependencies` (asserts `import` graph) |
| FR-019 | Single `http.Client.Do` per `Validate`; verified by handler-invocation counter | `TestValidator_<Name>_SingleRequest` (counter == 1) |
| FR-020 | `doRequest` emits exactly one `slog` record with the locked schema | `TestPackage_LogRecordSchema` |
| FR-021 | Per-request `CheckRedirect` override (R-005); 3xx → `ErrValidatorNetwork` | `TestValidator_<Name>_Redirect3xx_ClassifiedAsNetwork` (× 5) |
| SC-001 | (Verified at SDD-24 integration; this chunk's 5s timeout default is the precondition) | not in this package |
| SC-002 | 90% coverage | `go test -cover` gate |
| SC-003 | Race-clean | `go test -race` gate |
| SC-004 | No outbound DNS / TCP | `TestPackage_NoLiveProviderHosts` + manual network-off run |
| SC-005 | Source-grep for the listed patterns | `TestPackage_NoStringConversionsOfSecret` |
| SC-006 | Sentinel absent from err chain + every captured slog record | `TestValidator_<Name>_NoLeakOnError` |
| SC-007 | Registry name set is exactly the five | `TestRegistry_ExactlyFiveNames` |
| SC-008 | Cancelled-ctx `Validate` returns ≤ 50 ms / 0 handler invocations | `TestValidator_<Name>_CtxCancelledBeforeSend_NoHandlerInvocation` |

---

## Open questions deferred to /speckit-tasks

- **Test-helper ownership.** `rewriteTransport` could live in
  `validators_test.go` (shared) or be inlined in each
  per-provider test file. Decision punted to the Tasks phase
  (it is a code-organisation choice, not a behavioural one).
- **Per-provider concurrent-test fixture.** Whether
  `TestValidator_<Name>_Concurrent` shares one httptest.Server
  or constructs one per goroutine. The shared-server variant is
  simpler; the per-goroutine variant exercises connection-pool
  edge cases. Both pass `-race`; punted to Tasks.

These two punts do not change any locked surface or any
behaviour contract.
