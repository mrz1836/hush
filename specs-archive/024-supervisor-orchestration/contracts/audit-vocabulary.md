# Contract: Audit-event vocabulary (SDD-24)

**File**: `internal/audit/chain.go` — extended with 12 new constants.
**Anchor**: spec FR-026-014, SC-026-008.

This contract locks the exact set of `audit.Action*` constants the
orchestrator emits. The file `internal/audit/chain.go` is **append-only**
per the header comment "Future SDDs MAY append (never repurpose)" (line
33 of chain.go).

---

## 1. New constants (12 — appended to the existing block)

```go
// Supervisor lifecycle — added in SDD-24.
const (
    ActionSupervisorSessionClaimed    = "supervisor_session_claimed"
    ActionSupervisorSessionRefreshed  = "supervisor_session_refreshed"
    ActionSupervisorSilentRefill      = "supervisor_silent_refill"
    ActionSupervisorChildCleanExit    = "supervisor_child_clean_exit"
    ActionSupervisorChildExitCrash    = "supervisor_child_exit_crash"
    ActionSupervisorChildExit78       = "supervisor_child_exit_78"
    ActionSupervisorAwaitingApproval  = "supervisor_awaiting_approval"
    ActionSupervisorStaleAlert        = "supervisor_stale_alert"
    ActionSupervisorGraceEntered      = "supervisor_grace_entered"
    ActionSupervisorGraceExited       = "supervisor_grace_exited"
    ActionSupervisorBootTimeout       = "supervisor_boot_timeout"
    ActionClientRefreshInvoked        = "client_refresh_invoked"
)
```

## 2. Reused constants (3 — no change)

The orchestrator emits these existing constants where appropriate:

| Constant | When orchestrator emits |
|----------|------------------------|
| `ActionSecretRetrieved` | NOT emitted by orchestrator — emitted server-side in the vault server's `/s/{name}` handler. Documented here for cross-reference. |
| `ActionDiscordDisconnected` | NOT emitted by orchestrator — emitted server-side. The orchestrator infers Discord-unavailability from the /claim 503 body and emits `AlertClassDiscordUnavailableOnClaim`. |
| `ActionDiscordReconnected` | NOT emitted by orchestrator — emitted server-side. |

The three are listed in spec FR-026-014's "Reused from the existing
constants block" entry; this contract clarifies that "reused" means
"the orchestrator MAY rely on these being emitted by other code paths"
and NOT "the orchestrator emits them itself".

## 3. SPEC.md §FR-14 amendment (Plan-phase ADR-1)

Implement-phase MUST add the two missing supervisor-scope names to
`docs/SPEC.md` §FR-14's audit-event list:

- `supervisor_child_exit_crash`
- `supervisor_boot_timeout`

The other 10 supervisor-scope names already appear in §FR-14's list
(see SPEC.md lines 192-196). After the amendment, §FR-14 ↔
`internal/audit/chain.go` ↔ orchestrator emissions agree 1:1
(SC-026-008 verification target).

## 4. Emission-site cross-reference (orchestrator)

| Constant | Source location (post-implement) |
|----------|----------------------------------|
| `ActionSupervisorSessionClaimed` | `lifecycle_boot.go` — after JWT persist |
| `ActionSupervisorSessionRefreshed` | `lifecycle_refresh.go` — after successful refresh swap |
| `ActionSupervisorSilentRefill` | `lifecycle_child.go` — after silent refill returns nil |
| `ActionSupervisorChildCleanExit` | `lifecycle_child.go` — childExit dispatch when code == 0 |
| `ActionSupervisorChildExitCrash` | `lifecycle_child.go` — childExit dispatch when code != 0 && code != Exit78 |
| `ActionSupervisorChildExit78` | `lifecycle_child.go` — childExit dispatch when code == Exit78 |
| `ActionSupervisorAwaitingApproval` | `lifecycle_audit.go` — common helper invoked from every stale path |
| `ActionSupervisorStaleAlert` | `lifecycle_audit.go` — common helper invoked alongside every Alerts.Emit |
| `ActionSupervisorGraceEntered` | `lifecycle_child.go` — on StateGraceRestart entry |
| `ActionSupervisorGraceExited` | `lifecycle_child.go` — on StateGraceRestart exit |
| `ActionSupervisorBootTimeout` | `lifecycle_boot.go` — on boot-retry exhaustion |
| `ActionClientRefreshInvoked` | `lifecycle_refresh.go` — when status-socket refresh verb is consumed |

## 5. Anti-contract

- The orchestrator MUST NOT emit any audit action name that is NOT a
  declared `audit.Action*` constant. The unit test
  `TestLifecycle_NoBusinessLogicLeakage` includes a grep assertion that
  every string-literal in `lifecycle*.go` ending in `_` and quoted-as
  a likely audit action MUST resolve to a constant from this contract.
- No `Data` map key may carry secret material (see data-model.md §8).
- No existing constant is repurposed or renamed (audit/chain.go header
  contract).

## 6. Verification

The implementation-phase test suite includes `TestSpecFR14AuditSync` —
a docs/spec-test that:

1. Parses the `Action*` constants block from
   `internal/audit/chain.go`.
2. Parses the `Required event types` paragraph from
   `docs/SPEC.md` §FR-14.
3. Asserts the supervisor-scope subset is identical
   (after the §FR-14 amendment).

This test is the SC-026-008 mechanical check.
