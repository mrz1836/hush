# Quickstart — SDD-25 Lifecycle Integration Harness

## Run the suite

```bash
# from repo root
magex test:race -tags=integration ./tests/integration/...

# or directly
go test -race -tags=integration ./tests/integration/...
```

Suite target: **17/17 scenario test functions green (15 scenarios + Scenario 9 split + Scenario 11 split, per spec FR-002); under 120 s wall-clock; zero race-detector findings**.

## Verify no flake (5-run gate)

```bash
for i in 1 2 3 4 5; do
  magex test:race -tags=integration ./tests/integration/... || break
done
```

Five consecutive PASS results gates the SDD-25 chunk completion.

## Run a single scenario

```bash
go test -race -tags=integration -run Test_Scenario_05 ./tests/integration/...
```

## Default-build invisibility (spec FR-008)

```bash
# Should compile zero files in tests/integration:
go test ./tests/integration/...
# expected output: "no Go files in /Users/.../tests/integration"
```

If the suite leaks into a default build, the `//go:build integration` tag is missing somewhere — every harness file AND every test file must carry the tag.

## Add a new scenario (process)

> Adding a scenario beyond the 15 (17 test functions) means the SPEC + LIFECYCLE-SCENARIOS docs + spec FR-002 list MUST be updated first. Do NOT add an 18th `Test_Scenario_*` function as a side channel — the 17-function list locked in spec FR-002 is the contract.

If, instead, you need to add a regression test inside an existing scenario:

1. Locate the `Test_Scenario_NN_<slug>` function in `tests/integration/scenarios_test.go`.
2. Add the new assertion **before** the four mandatory contract assertions (A/B/C/D) at the bottom of the function. The four contracts always run last.
3. If the new assertion needs a harness helper that does not yet exist, add the helper to the appropriate harness file (per the allocation in [research.md §1](research.md#1-harness-file-allocation)) — do not create a seventh harness file.
4. Run the 5-flake gate.

## Add a new harness builder

Use this only when a scenario surfaces a need the existing six builders cannot serve. Process:

1. Decide which of the six files owns the concern (see [research.md §1](research.md#1-harness-file-allocation)).
2. Add the builder to that file. Every builder MUST satisfy the four properties in [contracts/harness-api.md §Builder contract](contracts/harness-api.md#builder-contract).
3. Update [data-model.md §4](data-model.md#4-harness-types-test-fixture-entity-model) with the new type + method table.
4. The `harness` package's exported surface is intentionally not frozen at the symbol level (per the SDD-25 PACKAGE-MAP entry); add the type or method and document its contract, but resist exposing new package-level mutable state.

## Investigate a scenario failure

1. **Run with `-v`** to see the assertion that failed plus the harness’s labeled stream dumps:
   ```bash
   go test -race -tags=integration -v -run Test_Scenario_05 ./tests/integration/...
   ```
2. **Audit-subsequence failures** print the recorded sequence with the missing event highlighted. If a documented event is absent, the underlying chunk's audit emission is missing — surface to the chunk owner (do NOT loosen the assertion).
3. **Sentinel-leak failures** print the offending stream label + byte offset + 64-byte context window. The byte offset points at the leak; trace the byte through the harness's stream-capture path to its source.
4. **Goroutine-leak failures** print a labeled stack dump of every leaked goroutine. The goroutine owner is responsible for the explicit termination condition (Constitution IX).
5. **Status-socket-shape failures** print the raw socket bytes plus the FR-12 field that was missing or malformed. The SDD-22 locked DTO is the source of truth — fix the production emission, not the assertion.

## Common gotchas

- **Adding `t.Parallel()` at the scenario top** — forbidden by spec FR-022 (suite runs serially at the top). Internal `t.Parallel` is allowed only where the scenario owns disjoint mutable state.
- **Hitting a real upstream URL** — the harness's `http.Client` is wired with a `RoundTripper` that errors on any host outside the registered httptest endpoints. If a scenario fails with "host not allowed", the production code is making a request the harness hasn't mocked — wire a mock in `TestServer.MockValidator`.
- **`time.Sleep` for a "documented transition"** — forbidden by plan §R-4. Use the harness's `WaitState(ctx, deadline)` helper, which uses a bounded `runtime.Gosched` poll, or drive the transition directly (e.g. invoke `TriggerRefresh` or advance the fake clock).
- **A scenario that "almost passes"** — there is no almost. The four contracts are mandatory and binary. A scenario failing Contract D (sentinel-absence) means a real production code path is leaking a secret; the fix is in the production code, not the test.

## Reference

- spec: [spec.md](spec.md)
- plan: [plan.md](plan.md)
- research: [research.md](research.md)
- data model: [data-model.md](data-model.md)
- harness API contract: [contracts/harness-api.md](contracts/harness-api.md)
- scenario assertions contract: [contracts/scenario-assertions.md](contracts/scenario-assertions.md)
- source-of-truth scenarios: `docs/LIFECYCLE-SCENARIOS.md`
- AC-10 row to update: `docs/AC-MATRIX.md`
- SDD chunk contract: `docs/sdd/SDD-25.md`
