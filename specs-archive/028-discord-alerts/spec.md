# Feature Specification: Discord Alert Surface (8 Classes + Tiered Routing + Rate Limit)

**Feature Branch**: `028-discord-alerts`
**Created**: 2026-05-13
**Status**: Draft
**Input**: User description: "internal/discord/alerts: 8 named alert classes (per docs/LIFECYCLE-SCENARIOS.md Required Alert Classes) and 3 tiers (Critical→DM owner, Warning→audit channel, Info→audit log only); per-supervisor and per-pattern rate limits; templates with distinct visual labels and zero secret-value leakage; tier-class binding is fixed (no auto-promotion)"

## Overview

This chunk delivers the operator-facing alert surface for hush. Every event
worth telling the operator about — Discord approval prompts, daemon refresh
nudges, validator stale failures, child exit-78 stale signals, log-pattern
watchdog matches, Discord connectivity changes, and boot-timeout failures —
flows through a single typed `Alert` value and is routed by tier:

- **Critical alerts** are direct-messaged to the configured owner.
- **Warning alerts** are posted to the configured audit channel.
- **Info alerts** are written to the audit log only — they never trigger
  any Discord network call.

Per-supervisor and per-pattern rate limiters apply to every class so that a
flapping daemon or a noisy log pattern cannot drown the operator. Templates
carry distinct, human-readable visual labels per class, and alert bodies
NEVER include any secret value, token value, JWT, or other credential
material. The binding between an alert class and its tier is fixed at
package level: a Warning can never auto-promote to Critical, and Critical
can never auto-demote to Warning.

This is the operator-visible Discord surface used by every supervisor
lifecycle event downstream of approval (AC-10), and the alert-surface half
of AC-3.

## Clarifications

### Session 2026-05-13

- Q: How does the `perSupervisorBucket` / `perPatternBucket` `time.Duration` parameter map onto rate-limit semantics? → A: Minimum-interval debounce — each key permits one successful route per duration; any subsequent alert for the same key within that interval returns `ErrAlertRateLimited`. Implicit capacity is 1; no separate token-count parameter exists.
- Q: What per-pattern key does the router use when `Alert.Pattern` is empty (true for 7 of the 8 classes)? → A: Class-name fallback — when `Pattern == ""`, the router uses `string(AlertClass)` as the per-pattern key, giving each pattern-less class its own isolated per-pattern bucket; the `log-pattern stale warning` class continues to use the operator-supplied pattern verbatim.
- Q: How does the router handle Discord transport failure (retry policy + rate-limit interaction + typed error surface)? → A: Single-shot send (zero internal retries), commit-on-success debounce (the per-supervisor and per-pattern debounce timestamps are recorded **only** after a successful Discord call, so a transport failure does not consume the slot), and a typed sentinel `ErrAlertTransport` wrapping the underlying transport error so callers can distinguish transport failure from rate limiting via `errors.Is`.
- Q: How does a template render an Alert when `Pattern` and/or `Detail` are empty strings? → A: Omit-empty-lines — each template renders only the fields that are non-empty. The label prefix and `SupervisorName` are the rendered floor; `MachineName`, `Pattern`, and `Detail` each appear only when their value is non-empty. No placeholder text, no trailing whitespace, no "missing value" surface.
- Q: What slog level does the router emit for each Route outcome (success vs. failure, across tiers)? → A: DEBUG on success for Critical and Warning tiers; INFO on success for Info tier (Info-tier delivery is itself the operational log surface per FR-008); WARN on transport failure (`ErrAlertTransport`) for any tier; no record on `ErrAlertRateLimited` (the caller logs the suppression per FR-016). Exactly one slog record per Route call. Attributes restricted to the allow-list in FR-024.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Critical alert reaches the operator's phone (Priority: P1)

The operator (vault owner) needs to know immediately when something requires
a human decision: a daemon refresh prompt, a stale-credential signal from a
validator, a child exit-78 stale signal, or a boot-timeout failure.
Critical-tier alerts are direct-messaged to the configured owner so that the
operator's phone surfaces a notification regardless of which Discord channel
they happen to be reading.

**Why this priority**: Without this path, the entire approval/rotation
workflow stalls. The operator must be reachable through the same
out-of-band channel the approval flow already uses. Constitution V
("Staleness is Visible, Failure is Loud") and Principle X ("Discord alert
tiers") both depend on this being reliable.

**Independent Test**: A supervisor (or test harness) emits a Critical-tier
alert through the router. The expected outcome is a direct message to the
configured owner with a class-specific visual label, no secret value in the
body, and no audit-channel post for that alert.

**Acceptance Scenarios**:

1. **Given** a router configured with an owner user identifier and an
   audit channel, **When** a Critical-tier alert is routed, **Then** the
   owner receives a direct message containing the class-specific label
   prefix and the redacted alert body, and the audit channel receives no
   post for that alert.
2. **Given** a Critical-tier alert containing a credential identifier (e.g.
   scope name) in its detail field, **When** the router renders the alert,
   **Then** the rendered message contains the credential's **name** but
   never the credential's **value**, token, JWT, signature, or any
   substring of secret material.
3. **Given** the rate limiter has not been exhausted for the alert's
   supervisor and pattern keys, **When** the alert is routed, **Then** the
   router returns no error and the message is delivered exactly once.

---

### User Story 2 — Warning alert reaches the audit channel, not the operator's DM (Priority: P1)

Operationally relevant events that do not require an immediate human
decision (validator failure that did not yet exhaust the boot budget, a
log-pattern watchdog match, a Discord disconnect/reconnect notice) are
posted to the configured audit channel rather than DMed. The operator can
review the channel on their own schedule. Critically, a Warning alert is
NEVER auto-promoted to Critical: the tier is fixed by class, not by content
or frequency.

**Why this priority**: Without a distinct tier for warnings, every
operational event would either page the operator (training them to ignore
DMs) or vanish silently (training them to distrust the system). Constitution
X mandates this split, and Principle V depends on it remaining loud-but-
proportionate.

**Independent Test**: A supervisor emits a Warning-tier alert through the
router. The expected outcome is exactly one post to the audit channel with
the class-specific visual label, no DM to the owner, and no auto-promotion
to Critical regardless of how many times the same class arrives.

**Acceptance Scenarios**:

1. **Given** a router configured with an owner and an audit channel,
   **When** a Warning-tier alert is routed, **Then** the audit channel
   receives a single post with the class-specific label and redacted body,
   and the owner receives no direct message.
2. **Given** the same Warning-tier class is routed N consecutive times
   (within rate-limit budget), **When** each call returns success,
   **Then** every delivered alert remains Warning-tier — the router NEVER
   re-routes a Warning to the owner DM destination based on count,
   recency, content, or any runtime signal.
3. **Given** a Warning-tier alert whose detail field carries operator-
   supplied metadata (a pattern name, a process identifier, a timestamp),
   **When** the router renders the alert, **Then** the rendered body
   contains only the operator-supplied metadata fields and never the
   value of any secret or token.

---

### User Story 3 — Info alert is recorded without any Discord network call (Priority: P2)

Routine, low-value events (informational lifecycle records that nonetheless
deserve an audit row) flow through the same `Alert` type for uniformity,
but Info-tier alerts MUST NOT trigger any Discord API call at all — neither
a DM nor an audit-channel post. They are written to the operational log
only. This keeps the audit channel and DM destination clean and prevents
the operator from being trained to ignore Discord noise.

**Why this priority**: The unified `Alert` type lets every caller speak the
same shape, but if every Alert hit Discord, the channel would become
unreadable. Discord-rate-limit and operator-attention budgets are scarce
resources; Info-tier preserves both.

**Independent Test**: A router is configured with a Discord approver whose
test double **fails the test if invoked**. An Info-tier alert is routed.
The router returns success, the log records the alert at INFO level, and
the approver was never called.

**Acceptance Scenarios**:

1. **Given** a router whose Discord transport is a strict test double
   that fails the test on any invocation, **When** an Info-tier alert is
   routed, **Then** the call returns success, the operational log records
   the alert at INFO level, and the Discord transport is NEVER invoked.
2. **Given** an Info-tier alert is routed, **When** the rate limiter is
   consulted, **Then** the limiter is still consulted (Info is rate-
   limited like every other class — no class bypasses rate limits) and
   the alert is suppressed with `ErrAlertRateLimited` if either bucket
   is exhausted.

---

### User Story 4 — Rate limiter prevents flooding from a single supervisor or repeated pattern (Priority: P1)

A misbehaving daemon (rapid crash loop, runaway log emission, flapping
network) can produce many alerts in quick succession. The router enforces
two independent token-bucket budgets per call: one keyed by the
supervisor name and one keyed by the alert's pattern identifier. Either
bucket being exhausted causes the router to return a sentinel
`ErrAlertRateLimited`; the caller is responsible for logging the
suppression. No class is exempt from rate-limit enforcement.

**Why this priority**: Without rate limits, a single supervisor in a hot
loop could exhaust the Discord API budget, drown the operator, and
disable the entire alert pipeline for other supervisors. Both keys are
required because two supervisors with the same pattern still need
independent budgets, and one supervisor with multiple patterns needs to
isolate noisy patterns from quiet ones.

**Independent Test**: A router is configured with a short debounce
interval. Two alerts are routed in rapid succession sharing the same
supervisor key (or pattern key). The first succeeds; the second
returns `ErrAlertRateLimited`. After the debounce interval elapses
since the last successful route, the next call succeeds again.

**Acceptance Scenarios**:

1. **Given** a router with per-supervisor debounce interval D, **When**
   two alerts are routed for the same supervisor inside interval D,
   **Then** the first returns success and the second returns
   `ErrAlertRateLimited` from the per-supervisor limiter; once D
   elapses since the last successful route, the next alert succeeds.
2. **Given** a router with per-pattern debounce interval D, **When**
   two alerts matching the same pattern are routed inside interval D
   — regardless of which supervisor emitted them — **Then** the first
   returns success and the second returns `ErrAlertRateLimited` from
   the per-pattern limiter; once D elapses since the last successful
   route, the next alert succeeds.
3. **Given** a per-supervisor bucket exhausted for supervisor A,
   **When** an alert is routed for supervisor B (sharing nothing),
   **Then** the alert for supervisor B succeeds (buckets are isolated
   per key).
4. **Given** any alert class (Critical, Warning, or Info),
   **When** the alert is routed and either bucket is exhausted,
   **Then** the router returns `ErrAlertRateLimited` — no class
   bypasses rate-limit enforcement.

---

### User Story 5 — Each alert class has a distinct, human-readable visual label (Priority: P2)

When the operator sees an alert on their phone or in the audit channel,
they must be able to tell at a glance which class fired without parsing
the body. Each of the 8 alert classes carries a distinct, descriptive
label prefix (e.g. a tier-and-class bracketed tag) that is unique within
the set of 8.

**Why this priority**: The eight classes overlap in subject matter
(several are about stale credentials). Distinct labels turn a glance into
the correct mental triage; without them, the operator must read every
body to know whether to act.

**Independent Test**: Render every alert class once and assert that every
rendered label is distinct from every other class's label and uniquely
identifies the class.

**Acceptance Scenarios**:

1. **Given** the 8 alert classes, **When** each class is rendered through
   the template machinery, **Then** the rendered label prefix of each
   class is distinct from every other class's label prefix.
2. **Given** a rendered alert for any class, **When** the operator scans
   the message, **Then** the label prefix uniquely identifies both the
   tier (visually) and the class (by name), without requiring the
   operator to read the body.

---

### Edge Cases

- **Unknown class passed to the router.** The router receives an `Alert`
  whose class is outside the 8 named constants (defensive — should never
  happen if the caller used the public constants). The router MUST
  return a typed error and MUST NOT route the alert, MUST NOT call
  Discord, and MUST NOT decrement any rate-limit bucket.
- **Discord transport temporarily unavailable.** A Critical or Warning
  alert is routed while Discord is disconnected. The router surfaces
  the typed sentinel `ErrAlertTransport` (wrapping the underlying
  transport error) to the caller; it does NOT silently downgrade to
  Info, does NOT retry internally (single-shot per FR-012b), and
  does NOT record a debounce timestamp for either bucket on
  transport failure (commit-on-success per FR-012a). The caller
  may choose to retry later through its own lifecycle.
- **Empty `Detail` or `Pattern` field.** The Alert is well-formed but
  carries empty operator-supplied metadata. The router renders the
  alert using omit-empty-lines semantics (FR-021): the class label
  prefix and `SupervisorName` are always present; `MachineName`,
  `Pattern`, and `Detail` lines are emitted only when their values
  are non-empty. Rendering MUST NOT panic, MUST NOT substitute
  placeholder text that looks like a missing value, and MUST remain
  free of any secret-shaped substring.
- **Concurrent calls from multiple supervisors.** Two supervisors emit
  alerts simultaneously through the same router. Rate-limit buckets are
  consulted concurrently and isolated per supervisor key; no race
  produces a double-decrement or a missed enforcement.
- **Rate-limiter window straddles a system clock jump.** The rate
  limiter uses a monotonic time source; bucket refill remains correct
  across wall-clock changes (NTP adjustment, manual time set, DST).
- **Alert body would contain a secret-shaped substring if rendered
  naïvely.** The template machinery only formats the explicitly-listed
  metadata fields (supervisor name, machine name, pattern, detail). Any
  Alert with a credential value smuggled into one of those fields would
  be a caller bug; the package documents the allow-list and tests prove
  none of the template format strings reference any field outside that
  allow-list.
- **Class arriving with an Info tier where Critical is documented (or
  vice versa).** This cannot happen at runtime: the class-to-tier
  binding is a single fixed package-level table. A test enforces that
  every class is mapped to exactly one tier and that the mapping
  matches the documented binding verbatim.

## Requirements *(mandatory)*

### Functional Requirements

#### Alert classes and tier binding

- **FR-001**: The package MUST expose exactly **8 named alert classes**
  matching the "Required Alert Classes" set documented in
  `docs/LIFECYCLE-SCENARIOS.md`:
  1. approval request
  2. daemon refresh request
  3. validator stale failure
  4. child exit-78 stale failure
  5. log-pattern stale warning
  6. Discord disconnected
  7. Discord reconnected
  8. vault/server unreachable at boot timeout
- **FR-002**: The package MUST expose exactly **3 named tiers**:
  `Critical`, `Warning`, `Info`. No other tier exists in v0.1.0.
- **FR-003**: The package MUST define a **fixed, immutable, package-
  level binding** from each of the 8 classes to exactly one tier. The
  binding is documented and traces back to Constitution Principle X
  and `docs/OPERATIONS.md` alert-tier rules. The binding is enforced
  by code (not by convention) and is asserted by a tier-binding test
  for every class.
- **FR-004**: The package MUST NOT auto-promote any Warning alert to
  Critical, MUST NOT auto-demote any Critical to Warning, MUST NOT
  re-tier based on content, frequency, recency, or any runtime
  signal. The class determines the tier; nothing else does.
- **FR-005**: Adding a 9th class or removing one of the 8 classes is
  out of scope for v0.1.0; it requires a chunk-level amendment.

#### Routing by tier

- **FR-006**: When a `Critical`-tier alert is routed, the router MUST
  deliver the rendered message to the configured owner via a
  direct-message destination, and MUST NOT post the alert to the audit
  channel.
- **FR-007**: When a `Warning`-tier alert is routed, the router MUST
  post the rendered message to the configured audit channel, and MUST
  NOT direct-message the owner.
- **FR-008**: When an `Info`-tier alert is routed, the router MUST write
  the rendered message to the operational log at INFO level only and
  MUST NOT trigger any Discord network call (no DM, no channel post).
- **FR-009**: The router MUST handle an alert with an **unknown class**
  defensively: return a typed error, do NOT contact Discord, do NOT
  decrement the rate-limit buckets, do NOT panic. This path exists for
  safety only — the public constants prevent it at compile time for
  in-package callers.

#### Rate-limit enforcement

- **FR-010**: The router MUST apply a **per-supervisor rate limit**
  keyed by the alert's supervisor identifier. The per-supervisor
  budget is a single `time.Duration` configured at router
  construction. Semantics: **minimum-interval debounce** — each
  supervisor key permits at most one successful route per duration;
  any subsequent alert for the same supervisor inside that interval
  returns `ErrAlertRateLimited`. Implicit capacity is 1; no
  separate token-count parameter exists.
- **FR-011**: The router MUST apply a **per-pattern rate limit** keyed
  by the alert's pattern identifier. The per-pattern budget is a
  single `time.Duration` configured at router construction.
  Semantics: **minimum-interval debounce** — each pattern key
  permits at most one successful route per duration; any subsequent
  alert for the same pattern inside that interval returns
  `ErrAlertRateLimited`. Implicit capacity is 1; no separate
  token-count parameter exists.
- **FR-011a**: When the alert's `Pattern` field is empty (true for
  7 of the 8 classes, which carry no natural pattern identifier),
  the router MUST substitute `string(AlertClass)` as the per-
  pattern key. This isolates each pattern-less class into its own
  per-pattern bucket so that, for example, a Discord-disconnect
  alert cannot debounce a validator-stale alert. The
  `log-pattern stale warning` class is the one class expected to
  carry a non-empty operator-supplied pattern; for that class the
  operator-supplied value is used verbatim and no class-name
  fallback occurs.
- **FR-012**: Either bucket being exhausted MUST cause the router to
  return the sentinel error `ErrAlertRateLimited` without contacting
  Discord, without writing the alert to the operational log as
  delivered, and without consuming any further bucket capacity.
- **FR-012a**: The router MUST use **commit-on-success** debounce
  semantics: the per-supervisor and per-pattern debounce
  timestamps are recorded **only after** a successful Discord
  delivery (for Critical and Warning tiers) or a successful Info-
  tier log record. A transport failure (see FR-012b) MUST NOT
  record either debounce timestamp, so the next attempt for the
  same key is not artificially blocked by a prior failed delivery.
- **FR-012b**: The router MUST perform a **single-shot** send to
  the Discord transport — zero internal retries. A transport
  failure MUST surface to the caller as a typed sentinel
  `ErrAlertTransport` that wraps the underlying transport error,
  so callers can distinguish transport failure from rate limiting
  via `errors.Is(err, ErrAlertRateLimited)` versus
  `errors.Is(err, ErrAlertTransport)`. Retry policy is the
  caller's responsibility (e.g. supervisor lifecycle owns
  back-off and re-emission decisions).
- **FR-013**: Rate-limit enforcement applies to **every** tier
  (Critical, Warning, Info) and **every** class. No alert is exempt.
- **FR-014**: Rate-limit buckets are **isolated per key**: exhausting
  the budget for supervisor A MUST NOT affect supervisor B's budget;
  exhausting the budget for pattern X MUST NOT affect pattern Y.
- **FR-015**: Rate-limit refill timing MUST be based on a **monotonic
  time source** so that NTP adjustments, manual clock changes, and
  DST transitions do not cause incorrect early refill or starvation.
- **FR-016**: When the router returns `ErrAlertRateLimited`, the
  **caller** is responsible for logging the suppression. The router
  itself MUST NOT spam the operational log with one suppression
  record per blocked alert.

#### Templates and visual labels

- **FR-017**: Each of the 8 alert classes MUST have a **distinct,
  human-readable label prefix** that uniquely identifies the class
  within the rendered message. No two classes share a label.
- **FR-018**: Every label prefix MUST be visually distinguishable at a
  glance (i.e. distinct enough that the operator does not have to
  read the body to triage).
- **FR-019**: Each class MUST have a **format string with named-field
  placeholders** that map only to operator-safe fields:
  supervisor name, machine name, pattern identifier, and operator-
  supplied detail metadata. The format strings are the **only**
  surface through which Alert fields reach a rendered message.
- **FR-020**: Template format strings MUST NEVER reference any field
  that could carry a credential value, token value, JWT, signature,
  or any other secret material. The package documents the allow-list
  of fields a template may reference, and a test asserts the
  templates only reference allow-listed fields.
- **FR-021**: Rendering an Alert with empty metadata fields MUST NOT
  panic, MUST NOT substitute placeholder text that looks like a
  missing secret, and MUST produce a message that still uniquely
  identifies the class via its label prefix. Concretely:
  **omit-empty-lines** — each template renders only the fields
  whose values are non-empty. The label prefix and the
  `SupervisorName` field are the rendered floor (always present);
  `MachineName`, `Pattern`, and `Detail` each appear only when
  their value is non-empty. The rendered output MUST NOT contain
  placeholder strings such as `<missing>`, `?`, or a trailing
  `key: ` with empty value.

#### Zero secret leakage

- **FR-022**: The rendered body of any alert (Critical, Warning, or
  Info) MUST NEVER contain a secret value, token value, JWT, secret
  fingerprint, signature, or any cryptographic material. The package
  documents this invariant and a sentinel-byte test per class proves
  the rendered output does not contain a known sentinel byte string.
- **FR-023**: Error messages returned by the router (including
  `ErrAlertRateLimited`, unknown-class typed errors, and transport
  errors) MUST NEVER contain secret material. Errors carry failure
  mode and identifier (class name, supervisor name) only.
- **FR-024**: Log records emitted by the router (rate-limit
  suppression, Info-tier delivery records, transport failure
  records) MUST NEVER include secret material; structured log
  attributes are an explicit allow-list and exclude any credential-
  carrying field.
- **FR-024a**: The router MUST emit **exactly one** structured log
  record per `Route` call, with level determined by outcome and
  tier:
  - Success, tier = Critical → level **DEBUG**.
  - Success, tier = Warning → level **DEBUG**.
  - Success, tier = Info → level **INFO** (Info-tier delivery is
    itself the operational log surface per FR-008).
  - Failure with `ErrAlertTransport` (any tier) → level **WARN**.
  - Failure with `ErrAlertRateLimited` (any tier) → **no record**;
    the caller logs the suppression per FR-016.
  - Failure with unknown-class typed error → level **WARN**.
  Log attributes MUST be drawn from the FR-024 allow-list:
  `{class, tier, supervisor, machine, pattern, outcome}` — no
  `detail` body, no rendered message, no credential-carrying
  field, no `*http.Request` / `*http.Response` / error wrapping
  the credential.

#### Behaviour and boundaries

- **FR-025**: The router exposes a single entry-point that accepts a
  typed `Alert` value and returns `nil` on success or a typed error
  on failure. There is NO variant that accepts loose strings, an
  `interface{}` payload, a `map`, or any unstructured form.
- **FR-026**: The router MUST be safe for concurrent use by multiple
  callers (multiple supervisors share a single router instance).
- **FR-027**: Construction of the router MUST take the owner DM
  destination, the audit channel identifier, the two rate-limit
  budgets (per-supervisor and per-pattern), and a structured logger.
  No globals, no `init()`, no package-level mutable state.

### Key Entities

- **Alert** — A typed value describing one operator-visible event.
  Carries: the `AlertClass` (one of 8), the `Tier` (derived from
  class — callers MAY set it but the router does not trust caller-
  supplied tier and re-derives from class), the supervisor name,
  the machine name, the pattern identifier, the operator-supplied
  detail metadata, and a timestamp. Carries NO credential material.
- **AlertClass** — A typed enumeration of 8 named values, one per
  required alert class. The enumeration is closed in v0.1.0.
- **Tier** — A typed enumeration of 3 named values: `Critical`,
  `Warning`, `Info`. The enumeration is closed in v0.1.0.
- **Router** — The single entry-point. Holds the configured owner
  DM destination, audit channel identifier, the two rate-limit
  budgets, and a structured logger. Routes each Alert by tier,
  enforces rate limits, renders the alert via the per-class
  template, and returns success or a typed error.
- **ErrAlertRateLimited** — A sentinel error returned when either
  the per-supervisor or per-pattern bucket is exhausted. Callers
  inspect it via `errors.Is` and log the suppression themselves.
- **ErrAlertTransport** — A sentinel error returned when the
  Discord transport fails for a Critical or Warning tier delivery.
  Wraps the underlying transport error so the caller can
  introspect via `errors.Is` / `errors.As`. Distinct from
  `ErrAlertRateLimited`; transport failure does NOT consume a
  debounce slot (FR-012a).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: All **8 alert classes** documented in
  `docs/LIFECYCLE-SCENARIOS.md` "Required Alert Classes" are exported
  by the package as named constants; no additional and no missing
  classes (counted from the public symbol set).
- **SC-002**: Exactly **3 tier constants** are exported: Critical,
  Warning, Info — no more, no fewer.
- **SC-003**: The class-to-tier mapping is **fixed and 1-to-1**:
  every one of the 8 classes maps to exactly one tier; a tier-
  binding test asserts every (class, expected-tier) pair against
  the documented binding, and all 8 assertions pass.
- **SC-004**: For Critical-tier routing, the configured owner DM
  destination receives the alert and the audit channel receives
  nothing for that alert; for Warning-tier routing, the audit
  channel receives the alert and the owner DM receives nothing;
  for Info-tier routing, neither Discord destination is contacted.
  All three routing assertions pass without flakes.
- **SC-005**: The Info-tier routing test uses a Discord transport
  test double that fails the test if invoked; the test passes,
  proving Info-tier alerts NEVER trigger a Discord network call.
- **SC-006**: A per-supervisor rate-limit test routes two alerts
  for the same supervisor key inside the debounce interval; the
  first returns success and the second returns
  `ErrAlertRateLimited`. A per-pattern rate-limit test does the
  same for the pattern key. Both pass.
- **SC-007**: Rate-limit isolation: alerts for a different
  supervisor (or pattern) succeed while the first key's bucket is
  exhausted; this is asserted by a dedicated test.
- **SC-008**: A render-snapshot test exists for every one of the 8
  alert classes (8 tests). Each asserts: the class's label prefix
  is present, the label prefix is unique across the 8 classes, and
  the rendered body contains only operator-safe field values.
- **SC-009**: A sentinel-byte test seeds known marker bytes into
  every field that template machinery could touch (supervisor
  name, machine name, pattern, detail) AND a known secret marker
  into the test's environment, then renders every class and
  asserts the rendered output contains the operator-safe markers
  but **never** the secret marker. The assertion passes for all
  8 classes.
- **SC-010**: An unknown-class defensive test passes an Alert with
  a class outside the 8 constants and asserts a typed error is
  returned, no Discord call is made, and no rate-limit bucket is
  decremented.
- **SC-010a**: A transport-failure test routes a Critical-tier
  alert through a Discord transport double that returns an
  injected error. The router returns `ErrAlertTransport` wrapping
  the injected error (assertable via `errors.Is` and
  `errors.As`), the per-supervisor and per-pattern debounce
  timestamps are NOT recorded, and a follow-up route for the
  same keys (with a working transport) succeeds without waiting
  for the debounce interval. The same test repeats for a
  Warning-tier alert against the audit-channel transport.
- **SC-011**: Test coverage on `internal/discord/alerts` is **≥ 90%**
  by `magex test:race`, race-clean.
- **SC-012**: A concurrent-use test exercises the router from
  multiple goroutines (multiple supervisor keys, multiple pattern
  keys) under `-race`; it passes flake-free.
- **SC-013**: Operator triage time: given a rendered alert sample
  for any class, an operator can identify the firing class within
  one glance at the label prefix without reading the body. Verified
  qualitatively at PR review; measurable as "each class label
  prefix is unique within the rendered output set".

## Assumptions

- The 8 alert class names enumerated in `docs/LIFECYCLE-SCENARIOS.md`
  "Required Alert Classes" are the closed, authoritative set for
  v0.1.0; SDD-28 does not add, rename, or remove any class.
- The fixed class-to-tier mapping is determined by Constitution
  Principle X ("Discord alert tiers") and `docs/OPERATIONS.md`
  alert-tier rules; the exact per-class binding is locked at
  `/speckit-plan` time (one-time decision, then immutable).
- A working `discord.Approver`-style direct-message transport
  (delivered by SDD-11) is the input dependency; the alerts package
  consumes it, not the other way around.
- A working audit-channel transport (also delivered by SDD-11) is
  available for Warning-tier routing; the alerts package consumes
  it, not the other way around.
- The `Alert.Detail` field carries **operator-supplied** metadata
  only (pattern names, identifiers, timestamps, scope names). It
  NEVER carries credential values. Callers passing credential
  values into `Detail` are a caller bug; the package documents
  this contract and the sentinel-byte test in SC-009 will catch
  the bug if a caller violates it.
- Per-supervisor and per-pattern rate-limit budgets are configured
  at router construction (caller-supplied bucket window). Their
  default values are operational tuning parameters out of scope
  for this spec; the requirement is that both budgets exist and
  both are enforced.
- Constitution V (Loud failure / staleness visible) and Principle X
  (Observability & Redaction) are load-bearing for this chunk: any
  deviation from the requirements above is a constitutional
  violation, not an implementation detail.
- The implementation language and library choices (slog vs another
  logger, discordgo SDK call patterns, token-bucket vs leaky-bucket
  algorithm) are HOW-level decisions deferred to `/speckit-plan`.
  This spec only constrains observable behaviour.
- The package does NOT own audit-row writing for AC-10 lifecycle
  events; the audit chain (SDD-13) and supervisor orchestration
  (SDD-24) own audit persistence. The alerts package's logging is
  operational, not the cryptographic audit chain.
