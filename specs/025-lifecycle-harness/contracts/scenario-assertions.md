# Contract — Scenario assertion shape (SDD-25)

Every `Test_Scenario_NN_<slug>` function MUST satisfy four assertion contracts before returning. A scenario that omits any of the four is non-compliant (FR-025-10) — even if it otherwise passes.

This document is the locked contract; reviewers verify each scenario against the four contracts before approving the chunk.

---

## Contract A — Final-state assertion (FR-025-6)

Every scenario MUST assert that the supervisor (or, for interactive-only scenarios, the server) ended in the documented final state.

| Scenario class | Assertion shape |
|----------------|-----------------|
| Supervisor (Scenarios 2–15) | `supervise.State` value from `TestSupervisor.Status().State` equals the documented state in [data-model.md §2](../data-model.md#2-the-15-scenarios--final-state--audit-events-catalogue). Additional documented facts (scope health, child PID, discord-connected) asserted via dedicated `Status()` field checks. |
| Interactive-only (Scenario 1) | Compound assertion per [data-model.md §3](../data-model.md#3-scenario-1-compound-final-state-clarification-4): (a) health-endpoint flags, (b) child exit code, (c) token-store state, (d) approval DM count. Four explicit assertions, no merge. |

Required call: exactly one of —
- `harness.AssertSupervisorState(t, sup, supervise.StateRunning)` (or the documented value)
- `harness.AssertScenario1Compound(t, server, child, discord, expectedExit int)`

Failure mode: scenario fails with a labeled diff showing actual vs expected state.

---

## Contract B — Audit subsequence assertion (FR-025-7, FR-025-23, FR-025-24)

Every scenario MUST assert two things about the audit log:

**B.1 Subsequence ordering.** The documented audit events appear in the documented order in the recorded log. Intervening unmentioned events are tolerated (spec Clarification 1).

```
harness.AssertAuditSubsequence(t, server.ReadAudit(), []string{
    "session_requested",
    "session_approved",
    "supervisor_session_claimed",
    "secret_fetched",
})
```

**B.2 Hash-chain continuity.** The on-disk audit chain verifies end-to-end (every record's `prev_hash` equals the prior record's `hash`; every signature verifies with the audit public key).

```
harness.AssertAuditChainContinuity(t, vault.AuditPath(), keys.AuditVerifyKey)
```

Failure mode: B.1 prints the recorded sequence with the missing/mis-ordered event highlighted. B.2 prints the offending `seq` + chain-break reason.

---

## Contract C — Status-socket JSON shape (FR-025-8)

Every **supervisor** scenario MUST assert that the status-socket JSON matches the FR-12 shape AND that the field values reflect the documented projection.

```
raw := sup.StatusRaw()
doc := harness.AssertStatusShape(t, raw)  // unmarshal into locked DTO; fails if any FR-12 field missing
require.Equal(t, "running", doc.State)
require.Equal(t, []string{"ANTHROPIC_API_KEY"}, doc.ScopeHealthy)
require.Empty(t, doc.ScopeStale)
require.True(t, doc.DiscordConnected)
```

Interactive-only scenarios (Scenario 1, Scenario 10/Interactive subtest) skip this contract — there is no supervisor and no socket. Their data-model rows are marked "No" or "Interactive subtest: no" accordingly.

Failure mode: `AssertStatusShape` fails if any FR-12 field is absent (no `omitempty` tolerated per spec Assumptions); field-value assertions fail with a labeled diff.

---

## Contract D — Sentinel-absence assertion (FR-025-9, FR-025-25, FR-025-26)

Every scenario MUST end with exactly one cross-stream `AssertSentinelAbsent` call covering every captured byte stream the scenario produced.

```
harness.AssertSentinelAbsent(t, sentinel,
    logs.Bytes(),               // operational slog output
    server.RawAudit(),          // audit JSONL bytes
    sup.StatusRaw(),            // raw status-socket bytes
    discord.AlertsRaw(),        // every recorded Discord alert payload
    child.Stdout(), child.Stderr(),  // captured child output
    errorMessages(t),           // every error.Error() string surfaced by the scenario
)
```

Where `errorMessages(t)` is a per-scenario helper collecting every error message string the scenario surfaced into a slice (the harness offers `harness.CollectErrors(...)` for this).

The sentinel value is `testutil.SentinelSecret(N)` for a per-scenario `N` (≥ 1 unique per concurrent goroutine, though the suite runs serially). At least one scope in every scenario carries this sentinel as its plaintext value.

Failure mode: `AssertSentinelAbsent` fails with the offending stream label + byte offset + 64-byte context window.

---

## Composition rule

The four assertions execute in order **A → B → C → D** at the end of the scenario body. They are independent: a failure in A does not skip B, C, D (`t.Fatal` is reserved for setup; assertions use `require.X` / `assert.X` and let the scenario surface all failures it can). The harness offers a `harness.AssertAll4(t, …)` convenience that runs all four in this order, but scenarios are free to expand it inline if a documented assertion needs custom shape.

## Anti-shapes (NOT allowed)

| Anti-shape | Why it's banned |
|------------|-----------------|
| `t.Skip()` for a missing audit event | FR-025-7 + edge-case row 1 — the audit log is a normative contract |
| `assert.Contains` to soften an audit-order check | violates FR-025-23 (sequence, not set) |
| A scenario that omits Contract D | FR-025-10 (all four mandatory) |
| Inline byte-stream re-marshal before sentinel-absence check | FR-025-26 requires assertion over *captured* bytes; re-marshalling could hide a leak |
| A scenario that asserts against `*supervise.Store` directly instead of the status socket | FR-025-8 requires the *socket* shape — the wire is the contract |
| Asserting the strict equal of recorded == documented audit events | violates Clarification 1 (intervening events tolerated) |
