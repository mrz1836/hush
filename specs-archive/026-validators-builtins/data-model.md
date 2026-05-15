# Phase 1 Data Model — SDD-26 (`internal/supervise/validators`)

**Feature:** 026-validators-builtins
**Date:** 2026-05-13
**Spec:** [spec.md](./spec.md) · **Research:** [research.md](./research.md) · **Chunk doc:** [docs/sdd/SDD-26.md](../../docs/sdd/SDD-26.md)

This document is the locked structural inventory for the chunk's
seven non-test files
(`validators.go`, `anthropic.go`, `anthropic_oauth.go`, `openai.go`,
`google_ai.go`, `github.go`). It pins struct shapes, field
semantics, ownership rules, and the per-entity invariants the
tests will assert. Exported field names and method signatures are
**locked**; unexported fields MAY be renamed without notice but
their semantics MUST NOT drift.

---

## Entity overview

| Entity | Kind | Purpose | Owns |
|--------|------|---------|------|
| `Validator` | exported interface | Producer surface: `Validate(ctx, *SecureBytes) error` | nothing |
| `Registry` | exported struct (opaque) | Read-only name → `Validator` lookup | unexported `byName map[string]Validator` (creates, never mutates) |
| `<provider>Validator` *(× 5, unexported)* | unexported struct | Per-provider concrete implementation | per-provider config: name, URL, header builder, extra headers, logger, http client |
| `authHeaderBuilder` *(unexported)* | function type | Authorization-header construction inside `Use(fn)` scope | nothing (callback) |
| `ErrStaleCredential` / `ErrValidatorTimeout` / `ErrValidatorNetwork` | exported sentinel errors | Public contract for `errors.Is` discrimination | nothing |

**Borrow vs own.** A "borrow" reference is one whose lifetime
exceeds the entity's; the entity MUST NOT close, free, or destroy
it. An "own" reference is one the entity creates and is
responsible for. The `*http.Client` accepted by each `New<Provider>`
constructor is a **borrow** — validators never mutate or close it.
The `*SecureBytes` passed to `Validate` is a **borrow** — validators
never call `Destroy` on it; consumption is exclusively via
`Use(fn)`.

---

## 1. `Validator` (exported interface)

### Interface shape (locked)

```go
// Validator answers "is this credential currently accepted by the
// upstream provider?" via a single read-only HTTP probe against the
// provider's documented credential-validation endpoint.
//
// Validators are stateless from the operator's perspective: each
// Validate call is a pure function of (credential, single upstream
// response) → typed verdict. Implementations are safe for concurrent
// invocation on the same instance with distinct *SecureBytes values
// (FR-017).
//
// The credential MUST be consumed exclusively via SecureBytes.Use(fn).
// Implementations MUST NOT materialise the credential as a Go string,
// log the credential value, include it in any returned error chain,
// or thread it through any other byte sink. See spec FR-006 .. FR-009
// and Constitution X.
//
// Validate returns nil on success (HTTP 2xx). On failure, the returned
// error satisfies exactly one of errors.Is(err, ErrStaleCredential),
// errors.Is(err, ErrValidatorTimeout), or errors.Is(err, ErrValidatorNetwork).
type Validator interface {
    Validate(ctx context.Context, secret *securebytes.SecureBytes) error
}
```

### Invariants

| ID | Invariant | Enforced where |
|----|-----------|----------------|
| V-1 | Method count is exactly one | Constitution IX (single-method interface); `TestValidator_InterfaceHasOneMethod` via `reflect.TypeOf((*Validator)(nil)).Elem().NumMethod() == 1` |
| V-2 | Concrete type implements `Validator` | Each per-provider file declares `var _ Validator = (*<provider>Validator)(nil)` as a compile-time guard |
| V-3 | Concurrent-safe on a single instance | Per-provider struct has no mutable fields (R-010, R-014); `TestValidator_<Name>_Concurrent` × 5 |

---

## 2. `Registry` (exported, opaque)

### Struct shape (locked)

```go
// Registry is the read-only lookup that maps each of the five fixed
// validator names (FR-010) to its corresponding *<provider>Validator.
// Constructed once via NewRegistry; not mutated thereafter; concurrent
// Get calls are safe (the underlying map is never written after
// construction, satisfying the Go memory-model race-freedom guarantee
// for read-only map access from multiple goroutines).
//
// The struct shape is intentionally opaque: callers consume the type
// only via NewRegistry + Get.
type Registry struct {
    byName map[string]Validator
}
```

### Invariants

| ID | Invariant | Enforced where |
|----|-----------|----------------|
| R-1 | `byName` is constructed exactly once, inside `NewRegistry`, and is never mutated thereafter | code review (single write site in `NewRegistry`); `TestRegistry_GetIsRaceClean` |
| R-2 | `byName` keys are exactly `{"anthropic", "anthropic-oauth", "openai", "google-ai", "github"}` — no more, no fewer | `TestRegistry_ExactlyFiveNames` (SC-007) |
| R-3 | `byName` values are non-nil `Validator` instances satisfying the interface | `TestRegistry_AllFiveNamesPresent` (each `Get` returns non-nil + true) |
| R-4 | `Get(name)` returns `(nil, false)` for any input not in `byName` | `TestRegistry_GetUnknownName_FalseFound` (covers misspellings, case variants, whitespace-padded, empty string) |
| R-5 | `Get` is read-only — no side effects, no allocation per call | code review |

### Constructor wiring

```go
// NewRegistry builds a Registry pre-populated with the five built-in
// validators. The supplied httpClient is shared across all five (each
// validator may shallow-copy it per call to override CheckRedirect —
// see research.md R-005). Passing nil yields a default
// *http.Client{Timeout: 5*time.Second, CheckRedirect: <ErrUseLastResponse>}.
func NewRegistry(httpClient *http.Client) *Registry {
    return &Registry{
        byName: map[string]Validator{
            anthropicName:      NewAnthropic(httpClient),
            anthropicOAuthName: NewAnthropicOAuth(httpClient),
            openaiName:         NewOpenAI(httpClient),
            googleAIName:       NewGoogleAI(httpClient),
            githubName:         NewGitHub(httpClient),
        },
    }
}

func (r *Registry) Get(name string) (Validator, bool) {
    v, ok := r.byName[name]
    return v, ok
}
```

The unexported `<provider>Name` constants are declared once at
package level in `validators.go`:

```go
const (
    anthropicName      = "anthropic"
    anthropicOAuthName = "anthropic-oauth"
    openaiName         = "openai"
    googleAIName       = "google-ai"
    githubName         = "github"
)
```

These five `const` declarations are compile-time constants
(Constitution IX exempt; not mutable globals).

---

## 3. `<provider>Validator` (unexported struct, × 5)

### Struct shape (per provider — illustrated for openai)

```go
// openaiValidator implements Validator for the OpenAI API.
// Endpoint pinning rationale: see research.md R-003c.
type openaiValidator struct {
    name    string                     // FR-010 lowercase name ("openai"); used in slog records
    url     string                     // pinned production URL; tests override via Transport rewriter
    builder authHeaderBuilder          // sets req.Header["Authorization"] = "Bearer "+secret
    extra   http.Header                // optional extra headers (nil for openai; non-nil for anthropic + github)
    client  *http.Client               // borrowed from constructor; never mutated
    logger  *slog.Logger               // borrowed; nil falls back to slog.Default() at use site
}

var _ Validator = (*openaiValidator)(nil)

func NewOpenAI(httpClient *http.Client) Validator {
    return &openaiValidator{
        name:    openaiName,
        url:     openaiEndpoint,
        builder: setOpenAIAuth,
        extra:   nil,
        client:  effectiveClient(httpClient),
    }
}

func (v *openaiValidator) Validate(ctx context.Context, secret *securebytes.SecureBytes) error {
    return doRequest(ctx, v.logger, v.client, v.name, v.url, v.extra, secret, v.builder)
}
```

The five providers share this shape; they differ only in:

| Provider | `name` const | `url` const | `builder` func | `extra` headers |
|----------|--------------|-------------|----------------|-----------------|
| anthropic | `anthropicName` | `anthropicEndpoint = "https://api.anthropic.com/v1/models"` | `setAnthropicAuth` (`x-api-key: <secret>`) | `{"anthropic-version": "2023-06-01"}` |
| anthropic-oauth | `anthropicOAuthName` | `anthropicEndpoint` (shared) | `setAnthropicOAuthAuth` (`Authorization: Bearer <secret>`) | `{"anthropic-version": "2023-06-01"}` |
| openai | `openaiName` | `openaiEndpoint = "https://api.openai.com/v1/models"` | `setOpenAIAuth` (`Authorization: Bearer <secret>`) | nil |
| google-ai | `googleAIName` | `googleAIEndpoint = "https://generativelanguage.googleapis.com/v1beta/models"` | `setGoogleAIAuth` (`x-goog-api-key: <secret>`) | nil |
| github | `githubName` | `githubEndpoint = "https://api.github.com/user"` | `setGitHubAuth` (`Authorization: token <secret>`) | `{"Accept": "application/vnd.github+json"}` |

### Invariants (per provider)

| ID | Invariant | Enforced where |
|----|-----------|----------------|
| P-1 | `name` matches the corresponding `<provider>Name` constant exactly (lowercase, no whitespace, exactly the FR-010 string) | code review + `TestValidator_<Name>_NameIsLockedString` |
| P-2 | `url` is the pinned `<provider>Endpoint` constant — no per-call URL construction | code review |
| P-3 | `builder` is the corresponding `set<Provider>Auth` function — not a closure capturing local state | code review |
| P-4 | All fields are immutable after construction — no setter methods (except the test-only logger seam) | `gochecknoglobals` + code review |
| P-5 | `Validate` body is a single-line delegation to `doRequest` — per-provider logic lives entirely in `builder` + `extra` | code review (per-provider file body length ≤ 40 LOC excluding doc comments) |

---

## 4. `authHeaderBuilder` (unexported function type)

### Type shape

```go
// authHeaderBuilder constructs and sets the Authorization header on
// req inside the SecureBytes.Use(fn) scope.
//
// The implementation MUST:
//   - allocate a fresh []byte exactly len(prefix)+len(secret) long;
//   - copy prefix + secret into the new buffer;
//   - call req.Header.Set(headerName, string(buf)) — the single
//     string(buf) site in the package, justified by net/http.Header's
//     stdlib API (research.md R-008);
//   - zero every byte of buf via for i := range buf { buf[i] = 0 }
//     before returning;
//   - return nil unless the caller-supplied bytes are structurally
//     invalid (empty is permitted — see spec Edge Cases "Empty credential").
//
// builderErr is captured by the doRequest helper, NOT returned from
// the Use(fn) callback (Use(fn) takes a no-return callback).
type authHeaderBuilder func(req *http.Request, secret []byte) error
```

### Per-provider builder bodies (illustrated for openai)

```go
// setOpenAIAuth sets the OpenAI-style Authorization header on req.
// Allocates a fresh []byte for the header value, copies "Bearer " +
// secret, sets the header, and zeros the local buffer before return.
func setOpenAIAuth(req *http.Request, secret []byte) error {
    const prefix = "Bearer "
    buf := make([]byte, len(prefix)+len(secret))
    copy(buf, prefix)
    copy(buf[len(prefix):], secret)
    req.Header.Set("Authorization", string(buf))
    for i := range buf {
        buf[i] = 0
    }
    return nil
}
```

Anthropic (raw API key — no prefix):

```go
func setAnthropicAuth(req *http.Request, secret []byte) error {
    buf := make([]byte, len(secret))
    copy(buf, secret)
    req.Header.Set("x-api-key", string(buf))
    for i := range buf {
        buf[i] = 0
    }
    return nil
}
```

Anthropic OAuth (`Bearer ` prefix), GitHub (`token ` prefix), and
Google AI (no prefix, different header name) follow the same
shape with the per-provider header name + prefix substituted.

### Invariants (per builder)

| ID | Invariant | Enforced where |
|----|-----------|----------------|
| B-1 | `buf := make([]byte, len(prefix)+len(secret))` is the only allocation in the builder; no `bytes.Buffer`, no `strings.Builder`, no `[]byte{prefix...}` literal | code review + source-grep test |
| B-2 | `req.Header.Set` is called exactly once per builder invocation | code review |
| B-3 | The zero-loop `for i := range buf { buf[i] = 0 }` is the last statement before `return nil` | code review + `TestPackage_AllBuildersZeroLocalBuffer` (Go-source-AST scan) |
| B-4 | The builder never returns a non-nil error in v0.1.0 (the spec's Edge Case row for "Empty credential" requires forwarding to the provider, not failing) | code review (the `error` return is reserved for future post-v0.1.0 extension; v0.1.0 builders always return nil) |
| B-5 | The local `buf` does not escape the builder scope (no return of buf, no slice-of-buf passed to long-lived collection) — verified by Go's escape analysis (`go build -gcflags='-m'` shows `buf escapes to heap` only because of the `string(buf)` conversion at line N, which is the documented exception) | code review |

---

## 5. Sentinel errors (exported)

### Declarations (locked)

```go
// ErrStaleCredential is returned when the upstream provider responds
// with HTTP 401 or HTTP 403 — the credential is well-formed bytes but
// the provider rejects it as no longer valid.
//
// Compare via errors.Is(err, ErrStaleCredential).
var ErrStaleCredential = errors.New("validators: credential rejected by provider")

// ErrValidatorTimeout is returned when the configured request timeout
// fires, or the supplied context.Context returns
// context.DeadlineExceeded.
//
// Compare via errors.Is(err, ErrValidatorTimeout).
var ErrValidatorTimeout = errors.New("validators: probe timeout")

// ErrValidatorNetwork is returned for every other failure: any non-2xx-
// non-401/403 HTTP response (including 3xx, 4xx-other, 5xx, 429),
// connection refused, DNS failure, TLS handshake failure, mid-flight
// reset, context.Canceled on a not-yet-sent request, or
// securebytes.ErrDestroyed (which is preserved in the wrapped chain
// per spec Clarification Q6).
//
// Compare via errors.Is(err, ErrValidatorNetwork).
var ErrValidatorNetwork = errors.New("validators: probe network failure")
```

### Invariants

| ID | Invariant | Enforced where |
|----|-----------|----------------|
| S-1 | Three pairwise-distinct identity-comparable error values | `TestPackage_SentinelsArePairwiseDistinct` (`ErrStaleCredential != ErrValidatorTimeout && != ErrValidatorNetwork`, etc.) |
| S-2 | `Error()` strings start with the package prefix `"validators: "` for log-grep affordance | code review |
| S-3 | `Error()` strings contain **zero** credential-derived content | trivially (constants); reinforced by `TestPackage_SentinelStringsAreLiteral` |
| S-4 | Every `Validate` return error is constructed via `errors.Join(ErrXxx, underlying)` or `fmt.Errorf("validators: %s: %w", name, ErrXxx)` — never a bare new error | code review + sentinel-leak tests |
| S-5 | The three sentinels are the only `var` declarations in non-test code in the package (Constitution IX exemption — sentinel-class read-only globals) | `gochecknoglobals` lint + code review |

---

## 6. Internal helpers (`validators.go`)

### `effectiveClient(*http.Client) *http.Client`

```go
// effectiveClient returns the caller-supplied client if non-nil,
// otherwise a default 5s-timeout client with redirect-follow disabled.
// The default client is constructed per Registry / New<Provider> call —
// no package-level singleton (FR-016).
func effectiveClient(c *http.Client) *http.Client {
    if c != nil {
        return c
    }
    return &http.Client{
        Timeout:       5 * time.Second,
        CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
    }
}
```

### `doRequest(ctx, logger, client, name, url, extra, secret, builder) error`

Single source of truth for: pre-cancel fast-path (R-016), request
construction with extra headers, `Use(fn)`-scoped builder
invocation, per-request `CheckRedirect` shallow-copy override
(R-005), `http.Client.Do` call, body drain + close, status-code
switch (FR-004), and `slog` record emission (FR-020).

Pseudocode (full body locked at implementation; tests assert
observable behaviour, not internal sequencing):

```go
func doRequest(
    ctx context.Context,
    logger *slog.Logger,
    client *http.Client,
    name string,
    url string,
    extra http.Header,
    secret *securebytes.SecureBytes,
    builder authHeaderBuilder,
) error {
    log := logger
    if log == nil {
        log = slog.Default()
    }

    // R-016 — pre-cancel fast-path.
    if err := ctx.Err(); err != nil {
        return classifyTransportError(ctx, log, name, err)
    }

    req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
    if err != nil {
        return classifyTransportError(ctx, log, name, err)
    }
    for k, vv := range extra {
        for _, v := range vv {
            req.Header.Add(k, v)
        }
    }

    var builderErr error
    if useErr := secret.Use(func(b []byte) {
        builderErr = builder(req, b)
    }); useErr != nil {
        // Spec Clarification Q6 — destroyed SecureBytes → network class with wrapped chain.
        return emitWarnAndWrap(log, name, outcomeNetwork, 0, errors.Join(ErrValidatorNetwork, useErr))
    }
    if builderErr != nil {
        return emitWarnAndWrap(log, name, outcomeNetwork, 0, errors.Join(ErrValidatorNetwork, builderErr))
    }

    // R-005 — per-request CheckRedirect override.
    effective := client
    if effective.CheckRedirect == nil {
        wrapped := *effective
        wrapped.CheckRedirect = func(*http.Request, []*http.Request) error {
            return http.ErrUseLastResponse
        }
        effective = &wrapped
    }

    resp, err := effective.Do(req)
    if err != nil {
        return classifyTransportError(ctx, log, name, err)
    }
    defer func() {
        _, _ = io.Copy(io.Discard, resp.Body)
        _ = resp.Body.Close()
    }()

    switch {
    case resp.StatusCode >= 200 && resp.StatusCode < 300:
        log.LogAttrs(ctx, slog.LevelDebug, "validator outcome",
            slog.String("validator", name),
            slog.String("outcome", outcomeSuccess),
            slog.Int("status", resp.StatusCode))
        return nil
    case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
        return emitWarnAndWrap(log, name, outcomeStale, resp.StatusCode,
            fmt.Errorf("validators: %s: stale: %w", name, ErrStaleCredential))
    default:
        return emitWarnAndWrap(log, name, outcomeNetwork, resp.StatusCode,
            fmt.Errorf("validators: %s: network: status %d: %w", name, resp.StatusCode, ErrValidatorNetwork))
    }
}
```

### `classifyTransportError(ctx, log, name, err) error`

```go
func classifyTransportError(ctx context.Context, log *slog.Logger, name string, err error) error {
    switch {
    case errors.Is(err, context.DeadlineExceeded):
        return emitWarnAndWrap(log, name, outcomeTimeout, 0, errors.Join(ErrValidatorTimeout, err))
    case isTimeout(err):
        return emitWarnAndWrap(log, name, outcomeTimeout, 0, errors.Join(ErrValidatorTimeout, err))
    default:
        return emitWarnAndWrap(log, name, outcomeNetwork, 0, errors.Join(ErrValidatorNetwork, err))
    }
}

// isTimeout inspects the error chain for net.Error.Timeout() == true.
func isTimeout(err error) bool {
    var ne net.Error
    if errors.As(err, &ne) {
        return ne.Timeout()
    }
    return false
}
```

### `emitWarnAndWrap` helper

```go
// emitWarnAndWrap centralises the FR-020 WARN log emission so all four
// failure-path return sites agree on the attribute schema.
//
// status == 0 means "omit the status attribute" (transport-level failure
// without an HTTP response; or pre-cancel fast-path; or destroyed-SecureBytes).
func emitWarnAndWrap(log *slog.Logger, name, outcome string, status int, returned error) error {
    attrs := []slog.Attr{
        slog.String("validator", name),
        slog.String("outcome", outcome),
    }
    if status > 0 {
        attrs = append(attrs, slog.Int("status", status))
    }
    log.LogAttrs(context.Background(), slog.LevelWarn, "validator outcome", attrs...)
    return returned
}
```

### Outcome constants

```go
const (
    outcomeSuccess = "success"
    outcomeStale   = "stale"
    outcomeTimeout = "timeout"
    outcomeNetwork = "network"
)
```

These four `const` declarations are compile-time constants
(Constitution IX exempt).

### Invariants (shared helpers)

| ID | Invariant | Enforced where |
|----|-----------|----------------|
| H-1 | `doRequest` is the single status-code-switch site in the package (other than `classifyTransportError`'s timeout classification) | code review + `TestPackage_StatusCodeSwitchInDoRequestOnly` (source-grep for `resp.StatusCode` outside `doRequest`) |
| H-2 | `emitWarnAndWrap` is the single WARN-emission site in the package | code review |
| H-3 | The DEBUG-level success emission in `doRequest` is the single DEBUG-emission site | code review |
| H-4 | `slog` attribute schema across all five emission sites (one DEBUG + four WARN) is exactly the FR-020 schema | `TestPackage_LogRecordSchema` (captures records via `slog.HandlerOptions{Level: slog.LevelDebug}` and asserts attribute set) |
| H-5 | No `slog.Any("error", err)`, no `slog.String("url", …)`, no `slog.Any("request", req)` anywhere | source-grep + `TestPackage_LogAttrsAreAllowList` |

---

## 7. Test-only seam (`export_test.go`)

```go
// export_test.go declares test-only seams that let tests inject
// a *slog.Logger into per-provider validators. The shape is the
// minimal one that lets every test capture exactly one slog record
// per Validate call without mutating slog.Default() (which would
// race with parallel tests).
package validators

func SetLoggerForTest(v Validator, logger *slog.Logger) {
    switch concrete := v.(type) {
    case *anthropicValidator:
        concrete.logger = logger
    case *anthropicOAuthValidator:
        concrete.logger = logger
    case *openaiValidator:
        concrete.logger = logger
    case *googleAIValidator:
        concrete.logger = logger
    case *githubValidator:
        concrete.logger = logger
    default:
        panic("validators: SetLoggerForTest called with unknown Validator type")
    }
}
```

The panic on the default branch is exempt from Constitution IX's
"library code returns errors" rule because `export_test.go` is
test-build-only and the panic guards an invariant that can only
fire under test development (someone adds a sixth provider and
forgets to extend the type switch).

### Invariants

| ID | Invariant | Enforced where |
|----|-----------|----------------|
| T-1 | `export_test.go` is the package's ONLY test-only export site; production code has no `SetXxxForTest` symbols | code review |
| T-2 | The type switch covers exactly the five provider concrete types | `TestExport_SetLoggerForTest_AllProvidersCovered` (calls `SetLoggerForTest` on each registry entry without panicking) |

---

## 8. Test fixture types (`validators_test.go`)

### `rewriteTransport`

```go
// rewriteTransport is a *http.RoundTripper that rewrites the request
// URL's scheme + host from "from" to "to" before forwarding to base.
// Used by tests to point a Validator (constructed with the pinned
// production URL) at a local httptest.Server.
type rewriteTransport struct {
    from string
    to   string
    base http.RoundTripper
}

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
    // Construct a copy with the rewritten URL; never mutate req.
    // ... (full body locked at test-helper implementation)
}
```

This type lives only in `validators_test.go` (shared test-helper
package-level). It is the canonical way to override the validator's
endpoint without exposing a non-chunk-doc constructor parameter.

### Invariants (test fixtures only)

| ID | Invariant | Enforced where |
|----|-----------|----------------|
| F-1 | Every `httptest.NewServer` invocation in test files is paired with a `defer fixture.Close()` | code review |
| F-2 | Every production URL appearing in test code is inside a `rewriteTransport{from: "<url>"}` literal or a companion constant — never inside an `http.Client.Do` target (FR-014 / SC-004) | `TestPackage_NoLiveProviderHosts` (test-source grep) |
| F-3 | The shared sentinel constant `sentinelLeakProbe = "SECRET_SHOULD_NEVER_APPEAR_26"` is declared once in `validators_test.go` and reused by all five sentinel-leak tests | code review |

---

## 9. State machine

The validator package has **no state machine**: each `Validate`
call is a pure function with no in-package side effects beyond
the single `slog` record. There is no per-validator-instance
state to transition through.

The registry has a degenerate one-state machine: `constructed`
(via `NewRegistry`). `Get` is a read-only query.

The implication for tests: there is no "ordering of operations"
to verify between calls. Concurrent-safety
(`TestValidator_<Name>_Concurrent`) is the only multi-call
property.

---

## 10. Cross-cutting traceability

| Constitution principle | Data-model enforcement |
|------------------------|------------------------|
| V — Loud failure | Three WARN-level emission sites (`outcome=stale`, `outcome=timeout`, `outcome=network`); zero failure-path silence (H-2) |
| VIII — Testing | Every entity carries explicit invariants enforced by named tests (V-1..V-3, R-1..R-5, P-1..P-5, B-1..B-5, S-1..S-5, H-1..H-5, T-1..T-2, F-1..F-3) |
| IX — Idiomatic Go | Single-method interface (V-1); sentinel-class globals only (S-5); zero `init()` (code review); ctx-first method signature (V on `Validator`); no shared mutable state (R-1, P-4) |
| X — Redaction | `*SecureBytes` is the only credential surface (V-1); `Use(fn)`-scoped builder consumption (B-1..B-5); log-attribute allow-list (H-4, H-5); no `string(secret)` in non-test code (B-1) |
| XI — Dependencies | Stdlib-only imports + `internal/vault/securebytes` (verified by `TestPackage_ZeroNewDependencies`) |

The contracts file lists the observable behaviours (B-V-N) the
tests assert; the quickstart file lists the named tests verbatim.
