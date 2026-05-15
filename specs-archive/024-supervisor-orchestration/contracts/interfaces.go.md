# Contract: Validator / Alerts / Watchdog interfaces (SDD-24)

**Package**: `github.com/mrz1836/hush/internal/supervise`
**File**: `lifecycle_interfaces.go`

These three single-method interfaces are defined at the consumer
(`package supervise` — the orchestrator) per Constitution IX. The
no-op defaults are unexported types and are used automatically when
`Deps` has nil for the corresponding field.

---

## 1. `Validator`

```go
// Validator validates one freshly-fetched (or Grace-resident) secret
// for one scope. The orchestrator calls Validate(ctx, scope, *SecureBytes)
// once per scope before each child start, and once per scope when
// re-fetching after a silent refill.
//
// Implementations MUST:
//   - Use `secret.Use(func(b []byte) { ... })` to access plaintext.
//   - Issue at most one HTTP request with a documented timeout.
//   - Return nil on success.
//   - Return a wrapped error on failure; the wrapper MUST name the
//     scope but MUST NOT include the secret value.
//
// SDD-26 supplies the five v0.1.0 builtins (anthropic, anthropic-oauth,
// openai, google-ai, github). This chunk ships only the no-op default.
type Validator interface {
    Validate(ctx context.Context, scope string, secret *securebytes.SecureBytes) error
}
```

**No-op default**:

```go
type noopValidator struct{}
func (noopValidator) Validate(context.Context, string, *securebytes.SecureBytes) error {
    return nil
}
```

**Wiring**: `Deps.Validators map[string]Validator`. Keys are scope
names. Missing key → no-op for that scope (the validator step still
runs and audits the call, but always succeeds). Nil map → no-op for
every scope.

---

## 2. `Alerts`

```go
// Alerts is the operator-visible alert sink. The orchestrator calls
// Emit at exactly 10 documented sites (see AlertClass below). Each
// Emit call is synchronous from the orchestrator's perspective;
// implementations MUST NOT block. SDD-28 supplies the rendering layer.
type Alerts interface {
    Emit(ctx context.Context, class AlertClass, payload AlertPayload)
}
```

**No-op default**:

```go
type noopAlerts struct{}
func (noopAlerts) Emit(context.Context, AlertClass, AlertPayload) {}
```

**Wiring**: `Deps.Alerts`. Nil → no-op default.

---

## 3. `AlertClass` enum (LOCKED at exactly 10 values)

```go
type AlertClass int

const (
    AlertClassValidatorFailure         AlertClass = iota + 1
    AlertClassExit78
    AlertClassVaultRejectedJWT
    AlertClassRefillFailed
    AlertClassDiscordUnavailableOnClaim
    AlertClassRefreshDenied
    AlertClassRefreshTimeout
    AlertClassGraceEntered
    AlertClassLogPatternMatch
    AlertClassBootTimeout
)

func (c AlertClass) String() string { /* per data-model.md §4 */ }
```

**Anti-extension**: SDD-28 MUST NOT add an 11th value without a spec
amendment (spec FR-026-016).

---

## 4. `AlertPayload`

```go
// AlertPayload carries non-secret labels for every Alerts.Emit call.
// Structurally cannot carry secret bytes — every field is a string,
// and the orchestrator never sources any field from secret material.
type AlertPayload struct {
    Scope      string // failed scope name; "" when N/A (e.g. BootTimeout)
    ErrorClass string // closed set: "transient" | "unknown_jti" |
                      //             "discord_unavailable" | "deny" |
                      //             "timeout" | "cancelled" | ""
    Reason     string // human-readable phrase from the closed phrase map
}
```

---

## 5. `Watchdog`

```go
// Watchdog observes child stderr lines. Implementations match operator-
// configured log patterns and emit AlertClassLogPatternMatch on hit.
// MUST be alert-only — MUST NOT influence state-machine transitions
// (Constitution V, spec FR-026-013a). SDD-27 supplies the pattern
// engine.
type Watchdog interface {
    OnStderrLine(ctx context.Context, line []byte)
}
```

**No-op default**:

```go
type noopWatchdog struct{}
func (noopWatchdog) OnStderrLine(context.Context, []byte) {}
```

**Wiring**: orchestrator passes a `*lineSplittingWriter` (see
`lifecycle_child.go`) as `ChildConfig.Stderr`. The writer fans bytes
to both the operator stderr sink AND a line-buffered observer that
calls `Deps.Watchdog.OnStderrLine(ctx, line)` per emitted line. SDD-20's
`Child.drainLoop` remains the sole reader (spec FR-026-030).

The `line` argument MUST NOT include the trailing `\n`. Lines longer
than `stderrLineCap` (64 KiB) are truncated; the observer sees only
the first `stderrLineCap` bytes.

---

## 6. AlertClass ↔ Emission site mapping

| AlertClass | Orchestrator emission site |
|-----------|----------------------------|
| `AlertClassValidatorFailure` | inside the validator-fail branch in boot/silent-refill, after `Validate` returns non-nil |
| `AlertClassExit78` | inside childExit dispatch when `code == Exit78` |
| `AlertClassVaultRejectedJWT` | inside silent-refill OR refresh swap when `errors.Is(err, ErrJTIUnknown)` |
| `AlertClassRefillFailed` | inside post-running silent-refill on any non-JTI error (FR-026-010a) |
| `AlertClassDiscordUnavailableOnClaim` | inside boot /claim attempt when 503 body's `error` == `"discord_unavailable"` |
| `AlertClassRefreshDenied` | inside refreshDone handler when result.deny is true |
| `AlertClassRefreshTimeout` | inside refreshDone handler when result.err is a timeout |
| `AlertClassGraceEntered` | when grace-cache restart path activates (StateRunning → StateGraceRestart transition) |
| `AlertClassLogPatternMatch` | NEVER emitted by orchestrator directly — SDD-27's Watchdog implementation calls Emit on this class via the same `Deps.Alerts` handle |
| `AlertClassBootTimeout` | inside boot-retry exhaustion path before returning from Run |

---

## 7. Test contract

Every test that injects a non-default Validator / Alerts / Watchdog
MUST do so via `Deps`. Tests MUST NOT reach into private fields of
Lifecycle. The default no-ops are package-private and visible to the
package-internal tests; non-package tests rely on injection only.

```go
// Example: a controllable alerts recorder for unit tests.
type recordingAlerts struct {
    mu     sync.Mutex
    events []recordedAlert
}
type recordedAlert struct {
    class   AlertClass
    payload AlertPayload
}
func (r *recordingAlerts) Emit(_ context.Context, class AlertClass, p AlertPayload) {
    r.mu.Lock()
    defer r.mu.Unlock()
    r.events = append(r.events, recordedAlert{class, p})
}
```

`TestLifecycle_ValidatorFailureBlocksChildStart` and the eight other
"emit one alert" tests construct a `*recordingAlerts` and assert
`len(r.events) == 1 && r.events[0].class == expected`.
