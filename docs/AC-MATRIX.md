# AC-MATRIX — acceptance criteria, chunk ownership, test paths

> Authoritative mapping from `docs/SPEC.md §6` acceptance criteria → SDD
> chunks that satisfy them → integration test paths that prove them.
>
> **This file is the v0.1.0 release gate.** No `v0.1.0` tag is created
> while any AC row below is incomplete (status not `green`).
>
> Each chunk's PR updates the relevant rows. Reviewers verify the rows
> before approving merge.

---

## Status legend

- `pending` — chunk not yet started
- `in-progress` — chunk underway; tests partially in place
- `green` — chunk complete; tests authoritative for this AC pass with
  `magex test:race` (and integration suite for AC-9/AC-10)
- `blocked` — chunk has unresolved external dependency

---

## AC-1 — `hush serve` startup

**SPEC reference:** `docs/SPEC.md` §6 AC-1 — A fresh `hush init` followed by
`hush serve` produces a running vault server that responds to
`GET /h/{prefix}/hz` over Tailscale within 5 seconds.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-06 (config) | `internal/config/server_test.go::TestLoadServer_*` | pending |
| SDD-10 (server skeleton) | `internal/server/integration_test.go::TestServe_StartAndShutdown` | pending |
| SDD-12 (claim handler) | `internal/server/claim_handler_test.go::TestClaim_Approved_IssuesJWT` | pending |
| SDD-13 (other handlers) | `internal/server/health_handler_test.go::TestHealth_NoAuth_OK` | pending |
| SDD-14 (cli root + serve) | `internal/cli/serve_test.go::TestServe_StartAndShutdown` | pending |
| SDD-15 (init) | `internal/cli/init_test.go::TestInitServer_CreatesVaultWith0600` | pending |
| **SDD-25 lifecycle harness** | `tests/integration/scenarios_test.go::Test_Scenario_01_FirstInteractive` (proves end-to-end startup and `/hz` responsiveness) | pending |

---

## AC-2 — Vault round-trip + SIGHUP reload

**SPEC reference:** `hush secret add NAME` → `hush secret list` → `hush
secret rotate NAME` → SIGHUP hot-reload preserves all other secrets and
atomically swaps the rotated value with no in-flight request failures.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-03 (vault file format) | `internal/vault/vault_test.go::TestVault_RoundTrip_*`, `TestVault_SaveAtomic_NoIntermediate` | pending |
| SDD-03 fuzz | `internal/vault/vault_fuzz_test.go::FuzzVaultDecode` (≥60s clean in CI) | pending |
| SDD-10 (SIGHUP atomic reload) | `internal/server/integration_test.go::TestSIGHUP_AtomicReload` | pending |
| SDD-13 (audit chain on rotation) | `internal/audit/chain_test.go::TestAuditChain_HashLinkContiguous` | pending |
| SDD-17 (`hush secret`) | `internal/cli/secret_test.go::TestSecret_RotateAtomic`, `TestSecret_RotateSendsSIGHUP` | pending |
| **SDD-25** | `tests/integration/scenarios_test.go::Test_Scenario_13_RotationMidSession` | pending |

---

## AC-3 — Discord approval flow

**SPEC reference:** `hush request --scope X --reason Y --ttl 1h --exec
"env | grep X"` triggers a DM to the configured approver, waits for
approval, and on approval injects the secret into the child process whose
stdout shows the secret value. Denial returns exit 3 with no secret leak.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-11 (Discord bot + Approver interface) | `internal/discord/approver_test.go::TestApprovalRender_*`, `TestRateLimit_*` | pending |
| SDD-12 (claim handler approval flow) | `internal/server/claim_handler_test.go::TestClaim_DiscordTimeout_408`, `TestClaim_DiscordUnavailable_503` | pending |
| SDD-28 (8 alert classes) | `internal/discord/alerts/alerts_test.go` (per-class tests) | pending |
| **SDD-25** | `tests/integration/scenarios_test.go::Test_Scenario_01_FirstInteractive`, `Test_Scenario_10_DiscordUnavailable` | pending |

---

## AC-4 — JWT lifecycle (IP-bind, max-uses, revoke, claims)

**SPEC reference:** After approval, the issued JWT (a) is rejected from a
different IP, (b) is rejected after `max_uses` fetches, (c) can be revoked
via `hush revoke --jti`, (d) carries `session_type` in its claims.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-07 (JWT issue/validate/store) | `internal/token/token_test.go::TestValidate_WrongIP`, `TestStore_ExhaustedInteractive_Refused`, `TestStore_RevokedJTI_Refused`, `TestStore_SupervisorIgnoresMaxUses` | pending |
| SDD-07 fuzz | `internal/token/validate_fuzz_test.go::FuzzJWTValidate` | pending |
| SDD-12 | `internal/server/claim_handler_test.go::TestClaim_Approved_IssuesJWT` | pending |
| SDD-13 | `internal/server/secret_handler_test.go::TestSecret_WrongIP_401`, `TestSecret_ExhaustedInteractive_401`; `internal/server/revoke_handler_test.go::TestRevoke_HappyPath` | pending |
| **SDD-25** | `tests/integration/scenarios_test.go::Test_Scenario_07_VaultRestart` | pending |

---

## AC-5 — `hush request --exec` injection safety

**SPEC reference:** With `--exec`, secrets exist only in the child
process's environment. The ephemeral private key is zeroed from the client's
memory after fetch. With `--format eval` AND no `--exec`, a stderr warning
is printed.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-16 (`hush request`) | `internal/cli/request_test.go::TestRequest_RequiresExecOrFormat`, `TestRequest_FormatEvalEmitsStderrWarning`, `TestRequest_ExecInjectsEnvVars`, `TestRequest_PostExecZeroesEphemeralKey`, `TestRequest_ExecOnlyChildHasSecret` (sentinel-leak) | pending |
| **SDD-25** | `tests/integration/scenarios_test.go::Test_Scenario_01_FirstInteractive` | pending |

---

## AC-6 — Per-machine client keys + Keychain ACL

**SPEC reference:** `hush init --client --machine-index N` produces a
unique client key per N. Reusing the same N from a different passphrase
produces a different key. Keychain entries are ACL-restricted to
`/usr/local/bin/hush`.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-01 | `internal/keys/client_test.go::TestDeriveClientKey_MachineIndexIsolation` | pending |
| SDD-15 (init + Keychain) | `internal/cli/init_test.go::TestInitClient_RequiresMachineIndex`, `TestInitClient_StoresInKeychainWithACL` (darwin only) | pending |
| SDD-15 (keychain wrapper) | `internal/keychain/keychain_darwin_test.go::TestKeychain_StoresWithACL` | pending |
| SDD-29 (install.sh) | `deploy/install.sh` smoke test (idempotent re-run) | pending |
| SDD-30 (TOML example) | `internal/supervise/config/config_test.go::TestExamples_GenericTOMLValidates` | pending |

---

## AC-7 — End-to-end ECIES, no plaintext on the wire

**SPEC reference:** A captured HTTP response body to `/h/{prefix}/s/{name}`
contains no plaintext secret value. Decrypting with the wrong ephemeral
private key fails cleanly.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-01 (BIP32 derivation + ephemeral keys) | `internal/keys/derive_test.go` | pending |
| SDD-08 (request signing) | `internal/transport/sign/*_test.go` | pending |
| SDD-08 fuzz | `internal/transport/sign/verify_fuzz_test.go::FuzzVerifyRequest` | pending |
| SDD-09 (ECIES) | `internal/transport/ecies/ecies_test.go::TestECIES_*`, `TestECIES_NoLeakOnError` (sentinel-leak) | pending |
| SDD-09 fuzz | `internal/transport/ecies/decrypt_fuzz_test.go::FuzzECIESDecrypt` | pending |
| SDD-13 (server `/s` handler ECIES output) | `internal/server/secret_handler_test.go::TestSecret_HappyPath_ECIESPayload` | pending |
| SDD-16 (`hush request` decrypt) | `internal/cli/request_test.go::TestRequest_ExecInjectsEnvVars` | pending |
| **SDD-25** | `tests/integration/scenarios_test.go::Test_Scenario_01_FirstInteractive` (asserts no plaintext on the wire) | pending |

---

## AC-8 — Server hardening

**SPEC reference:**
- Server refuses to start with `listen_addr=0.0.0.0`.
- Server refuses to start with empty `allowed_client_ips`.
- Server refuses to start with empty `registered_client_keys` unless
  `client_signature_required: false`.
- Server refuses to start if any file in `~/.hush/` is more permissive than `0600`.
- Server refuses to start if NTP-unsynced or drift > 60s.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-06 (config validation) | `internal/config/server_test.go::TestLoadServer_RefusesPublicBind`, `TestLoadServer_RefusesEmptyAllowedClients`, etc. | pending |
| SDD-06 fuzz | `internal/config/server_fuzz_test.go::FuzzServerTOML` | pending |
| SDD-10 (startup checks) | `internal/server/startup_checks_test.go::TestStartupChecks_RefusesPublicBind`, `TestStartupChecks_RefusesLooseFileMode`, `TestStartupChecks_RefusesUnsyncedClock` | pending |
| SDD-30 (Tailscale ACL doc) | `docs/TAILSCALE-ACLS.md` accurate; reviewer checks the example | pending |

---

## AC-9 — Test coverage + fuzz (release gate)

**SPEC reference:** `magex test:race` reports ≥ 90% repo coverage and
≥ 100% for crypto/key/JWT/ECIES/signing packages. `magex fuzz` runs vault
decrypt + ECIES decrypt + JWT validate fuzz targets for ≥ 60s each without
crash.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-04 (testutil — supports all coverage) | `internal/testutil/*_test.go` | pending |
| SDD-31 (release gates) | `.github/workflows/release-gates.yml` (green run + codecov badge); CI cron with 6 fuzz targets ≥ 60s clean | pending |
| **SDD-25** (provides the integration coverage that lifts AC-10 paths) | `tests/integration/scenarios_test.go` (15/15 green with `-race`) | pending |

**Required fuzz targets (Constitution VIII §2):**

| # | Target | Owning chunk |
|---|--------|--------------|
| 1 | Vault file decode | SDD-03 (`FuzzVaultDecode`) |
| 2 | JWT parse/validate | SDD-07 (`FuzzJWTValidate`) |
| 3 | ECIES decrypt input | SDD-09 (`FuzzECIESDecrypt`) |
| 4 | Request signature payload | SDD-08 (`FuzzVerifyRequest`) |
| 5 | Supervisor config TOML | SDD-18 (`FuzzSupervisorTOML`) |
| 6 | Status socket JSON encoding (only if custom parsing exists) | SDD-22 (optional) |

**Coverage targets per package** (from Constitution VIII matrix):

| Package | Target |
|---------|--------|
| `internal/keys` | 100% |
| `internal/vault` + `internal/vault/securebytes` | 100% |
| `internal/token` | 100% |
| `internal/transport/sign` | 100% |
| `internal/transport/ecies` | 100% |
| `internal/audit` | 100% |
| `internal/server` (handlers) | 95% |
| `internal/supervise` (state, refill, refresh, grace, child) | 95% |
| `internal/discord` | 85% |
| `internal/cli` | 85% |
| `internal/config` | 95% |
| `internal/logging` | 95% |
| Project-wide | ≥ 90% |

---

## AC-10 — Supervisor lifecycle (15 named scenarios)

**SPEC reference:** The supervisor integration suite passes the 15
scenarios documented in `docs/LIFECYCLE-SCENARIOS.md`.

**Owning chunk for the integration harness:** **SDD-25** (explicit
AC-10 owner — the lifecycle integration harness).

| # | Scenario | Test name | Status |
|---|----------|-----------|--------|
| 1 | First interactive shell request | `Test_Scenario_01_FirstInteractive` | pending |
| 2 | First daemon bootstrap | `Test_Scenario_02_DaemonBootstrap` | pending |
| 3 | Clean child exit → silent refill | `Test_Scenario_03_CleanExitSilentRefill` | pending |
| 4 | Child crash within valid session TTL | `Test_Scenario_04_ChildCrashSilentRefill` | pending |
| 5 | Child exit 78 stale-credential contract | `Test_Scenario_05_Exit78StaleCreds` | pending |
| 6 | Validator catches bad secret before child start | `Test_Scenario_06_ValidatorBlocksChild` | pending |
| 7 | Vault server restart (401 unknown-jti) | `Test_Scenario_07_VaultRestart` | pending |
| 8 | Daytime refresh-window prompt | `Test_Scenario_08_DaytimeRefresh` | pending |
| 9 | Overnight expiry with and without grace cache | `Test_Scenario_09_OvernightExpiry_{Strict,Grace}` | pending |
| 10 | Discord unavailable during new claim | `Test_Scenario_10_DiscordUnavailable` | pending |
| 11 | Tailscale boot retry / startup ordering recovery | `Test_Scenario_11_TailscaleBootRetry` | pending |
| 12 | Agent status check before long task | `Test_Scenario_12_StatusGate` | pending |
| 13 | Secret rotated on vault host during active daemon session | `Test_Scenario_13_RotationMidSession` | pending |
| 14 | Duplicate supervisor start attempt | `Test_Scenario_14_DuplicateSupervisor` | pending |
| 15 | Log-pattern watchdog sees auth failure string | `Test_Scenario_15_LogPatternAlert` | pending |

**Supporting chunks** (provide the building blocks for these scenarios):

- SDD-18 (supervisor config TOML)
- SDD-19 (state machine)
- SDD-20 (child fork/exec, exit-78 detection)
- SDD-21 (refill, refresh, grace cache)
- SDD-22 (pidfile, status socket)
- SDD-23 (supervise + client status + client refresh CLIs)
- SDD-26 (validators)
- SDD-27 (watchdog)
- SDD-28 (alert classes)

---

## Review checklist (each PR)

A PR closing or partially closing a chunk MUST update the relevant rows
in this file with:

1. The exact test path(s) that prove the AC.
2. The status (`pending` → `in-progress` → `green` as appropriate).
3. Any blocked-on items (cite chunk IDs).

The reviewer MUST:

- Verify the cited tests exist and pass with `magex test:race`.
- Verify fuzz targets have a 60s+ clean run recorded in CI for the
  current PR or a recent run.
- Reject the PR if rows are not updated.

---

## Cross-references

- Chunk catalog with full prompts: [`docs/SDD-CATALOG.md`](SDD-CATALOG.md)
- Chunk index + status: [`docs/SDD-PLAYBOOK.md`](SDD-PLAYBOOK.md)
- Test strategy: [`docs/TESTING-STRATEGY.md`](TESTING-STRATEGY.md)
- Acceptance criteria source: [`docs/SPEC.md`](SPEC.md) §6
- Constitutional principle VIII (Testing Discipline): [`.specify/memory/constitution.md`](../.specify/memory/constitution.md)
