# Contract — Approver

The chassis declares the `Approver` interface and its associated
value types. SDD-11 implements the interface with a Discord-backed
`BotApprover`. SDD-12 (claim handler) is the first consumer.

This contract is locked once SDD-10 merges.

---

## Interface

```go
package server

import (
    "context"
    "net/netip"
    "time"
)

// Approver seeks the configured operator's decision on a fresh
// secret-session request. The chassis itself ships no concrete
// implementation; SDD-11 supplies BotApprover.
//
// Implementations MUST be safe for concurrent use — the chassis
// may invoke RequestApproval from multiple request goroutines.
type Approver interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}
```

---

## ApprovalRequest

```go
// ApprovalRequest is the parameter the chassis passes to an Approver.
// All fields are populated by the consumer (SDD-12 claim handler);
// the chassis itself does not invoke RequestApproval directly.
type ApprovalRequest struct {
    // RequestID is the chassis-assigned request identifier.
    // Used by Approver implementations for correlation.
    RequestID string

    // MachineName is the hostname the requesting client supplied
    // in the /claim payload.
    MachineName string

    // ClientIP is the socket-level peer address of the request.
    // Always set from the connection, never from a header.
    ClientIP netip.Addr

    // Scope is the requested set of secret names (alphabetical).
    Scope []string

    // Reason is the human-readable reason from the /claim payload.
    Reason string

    // SessionType distinguishes interactive shell sessions from
    // long-lived supervisor sessions. Visible to Approver so the
    // Discord DM can render the [DAEMON] label per FR-7.
    SessionType SessionType

    // RequestedTTL is the duration the client asked for. The
    // Approver may grant a smaller TTL via Decision.GrantedTTL.
    RequestedTTL time.Duration

    // Metadata is an open extension surface. The chassis treats it
    // as opaque. SDD-11 may use it for ephemeral_pubkey_fingerprint
    // or similar Discord-rendered detail. Values MUST NOT contain
    // secret material or request-body bytes (Constitution X).
    Metadata map[string]string
}
```

---

## SessionType

```go
type SessionType uint8

const (
    SessionInteractive SessionType = iota + 1
    SessionSupervisor
)

func (s SessionType) String() string {
    switch s {
    case SessionInteractive:
        return "interactive"
    case SessionSupervisor:
        return "supervisor"
    default:
        return "unknown"
    }
}
```

---

## Decision

```go
// Decision is the Approver's response.
type Decision struct {
    // Approved is true if and only if the operator clicked Approve.
    Approved bool

    // ApprovedAt is the wall-clock time of approval. Zero when
    // Approved is false.
    ApprovedAt time.Time

    // DeniedAt is the wall-clock time of denial. Zero when
    // Approved is true.
    DeniedAt time.Time

    // GrantedTTL is the TTL the consumer should use when issuing
    // the JWT. May be < ApprovalRequest.RequestedTTL when the
    // operator picked a shorter button.
    GrantedTTL time.Duration

    // ApproverID is an opaque identifier for the approver — the
    // Discord user ID for SDD-11, "test" for fakes, etc.
    ApproverID string

    // Reason is an optional free-text reason the operator may
    // have attached to a denial. Empty on approval.
    Reason string
}
```

---

## Error semantics

| Return shape | Meaning |
|--------------|---------|
| `(Decision{Approved: true, ...}, nil)` | Approved. Issue JWT. |
| `(Decision{Approved: false, DeniedAt: t, ...}, nil)` | Denied. Return 403 to client. |
| `(Decision{}, ctx.Err())` | Cancelled (timeout, shutdown). |
| `(Decision{}, otherError)` | Transport failure (Discord unreachable, etc.). Per `docs/SPEC.md` FR-20: respond 503. |

The chassis itself does not invoke `RequestApproval`; it only
defines the contract. SDD-12 is responsible for translating the
return shape into the HTTP response.

---

## Single-method discipline

Constitution IX: "Prefer single-method interfaces. Define
interfaces at the consumer." The chassis is the consumer of
approval; it accepts any value satisfying `Approver`. Adding new
methods to this interface is a breaking change; future approval
extensions (presets, multi-approver flows) MUST add new types
rather than expanding the existing one.

---

## Test fakes

Tests inside `internal/server/*_test.go` and (later) inside
SDD-12's claim-handler tests will supply a `fakeApprover`:

```go
type fakeApprover struct {
    mu       sync.Mutex
    calls    []ApprovalRequest
    decisions []Decision
    errs     []error
    idx      int
}

func (f *fakeApprover) RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error) {
    f.mu.Lock()
    defer f.mu.Unlock()
    f.calls = append(f.calls, req)
    if f.idx >= len(f.decisions) {
        return Decision{}, errors.New("fakeApprover: no scripted decision")
    }
    d, e := f.decisions[f.idx], f.errs[f.idx]
    f.idx++
    return d, e
}
```

The fake is concurrency-safe and call-recording — exactly enough
for User Story 7's acceptance scenarios.
