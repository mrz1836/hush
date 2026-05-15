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
| SDD-06 (config) | `internal/config/server_test.go`, `internal/config/validate_test.go` — `TestServer_FullMinimalConfig`, `TestServer_FullMaximalConfig`, `TestServer_RejectsLoopback`, `TestServer_RejectsPublic`, `TestServer_AcceptsTailscaleCGNAT`, `TestServer_RejectsArgonMemoryUnder256`, `TestServer_RejectsAuditLogOutsideStateDir`, `TestServer_RejectsUnknownField`, and 40+ more | verified by SDD-06 |
| SDD-10 (server skeleton) | `internal/server/integration_test.go::TestStartupChecks_HappyPath`, `TestRun_GracefulShutdown_DrainsInflight`; `internal/server/router_test.go::TestRouter_PrefixMount`; `internal/server/server_test.go::TestServer_ZeroAuditOnStartupOK`, `TestRun_AlreadyRun` | done (chassis only) |
| SDD-12 (claim handler) | `internal/server/claim_handler_test.go::TestClaim_Approved_IssuesJWT`, `TestClaim_TTLCappedAtConfigMax`, `TestClaim_SupervisorRequest_DaemonLabel`, `TestClaim_BadRequest_400`, `TestClaim_RegisterHandlers_MountsClaimRoute`; `internal/server/claim_handler_integration_test.go::TestClaim_Integration_FullFlow_DiscordStub` | done |
| SDD-13 (other handlers) | `internal/server/health_handler_test.go::TestHealth_NoAuth_OK`, `TestHealth_DiscordConnectedFlag`, `TestHealth_VaultLoadedFalseDuringStartup`, `TestHealth_NoSentinelLeak`, `TestHealth_ActiveTokensCount`, `TestHealth_ZeroUptimeBeforeRun` | done |
| SDD-14 (cli root + serve/health/version/revoke) | `internal/cli/exit_codes_test.go::TestExitCodes_*`; `internal/cli/output_test.go::TestOutput_*`; `internal/cli/root_test.go::TestRoot_GlobalFlagsWired`, `TestRoot_VerboseQuietConflict_ExitInputErr`, `TestNoViperImport`, `TestExecute_PropagatesContextCancellation`, `TestServe_NeverReadsEnv`; `internal/cli/serve_test.go::TestServe_PassphraseFromStdinPipe`, `TestServe_PassphraseFromTTYPrompt`, `TestServe_NoStdinNoTTY_ExitInputErr`, `TestServe_ZeroByteStdinPipe`, `TestServe_OutputNoSentinel`, `TestStripPOSIXLineEnd`, `TestLoadBotToken_ItemNameValidation`, `TestExpandTilde`, `TestReadVaultSalt`, `TestRunServe_MissingConfig_ExitInputErr`; `internal/cli/serve_chassis_test.go::TestRunServe_ChassisLifecycle`; `internal/cli/serve_integration_test.go::TestServe_StartAndShutdown`, `TestServe_BadPassphrase_ExitAuth` (//go:build integration); `internal/cli/health_test.go::TestHealth_HappyPath`, `TestHealth_PartialHealth_ExitErr`, `TestHealth_ConnectionRefusedExplicitMessage`, `TestHealth_5xxServerError_ExitErr`, `TestHealth_NoAuthRequired`, `TestHealth_OutputNoSentinel`, `TestHealth_MissingServerFlag`; `internal/cli/version_test.go::TestExecute_VersionPrintsBuildVersion`, `TestVersion_NonTTYJSONShape_ThreeKeys`, `TestVersion_DevPlaceholderWhenUnset`, `TestVersion_AlwaysExitsOK`, `TestVersion_NoColorIrrelevant`; `internal/cli/revoke_test.go::TestRevoke_SignedRequestPosted`, `TestRevoke_BadStatusMapsToExitCode`, `TestRevoke_MissingFlags_ExitInputErr`, `TestRevoke_MalformedJTI_ExitInputErr`, `TestRevoke_ConnectionRefused_ExitErr`, `TestRevoke_5xxBodyExcerptSanitized`, `TestRevoke_OutputNoSentinel`, `TestRevoke_TTYSuccessMessage_NonTTY_JSONShape`, `TestRevoke_NonceUniquePerCall`, `TestRevoke_NeverPrintsSigningKey`; `internal/cli/coverage_extras_test.go::TestEphemeralRevokeKey`, `TestMapSessionType`, `TestDiscordApproverAdapter_TranslatesDecisionsAndErrors`, `TestPrintErr`, `TestClassifyTransportErr`, `TestMark`, `TestRunHealth_TimeoutMessage`, `TestNewHealthCmd_RunE_RoutesThroughOutputContext`, `TestNewRevokeCmd_RunE_FailsCloseConnection`, `TestLoadBotToken_KeychainAbsent`, `TestNewProductionBotApprover_BadKeychain`. Coverage ≥ 85 % on `internal/cli`. | done |
| SDD-15 (init) | `internal/cli/init_test.go` — `TestInitServer_CreatesVaultWith0600`, `TestInitServer_CreatesConfigWithAllDefaults`, `TestInitServer_StoresVaultPassphraseInKeychain`, `TestInitServer_StoresBotTokenInKeychain`, `TestInitServer_RefusesShortPassphrase`, `TestInitServer_RejectsConfirmationMismatch`, `TestInitServer_RejectsNonTTYStdin`, `TestInitServer_RefusesPreExistingVault`, `TestInitServer_RefusesPreExistingConfig`, `TestInitServer_RefusesPreExistingKeychainItem`, `TestInitServer_PathPrefixGenerated12CharsURLSafe`, `TestInitServer_RoundTripsConfigViaLoadServer`, `TestInitServer_RefusesPlatformWithoutACL`, `TestInitServer_AtomicWriteConfigToml`, `TestInitServer_NeverReadsPassphraseFromEnv`, `TestInitServer_NeverLeaksPassphraseToOutput`, `TestInitServer_NeverLeaksBotTokenToOutput`; `internal/cli/init_integration_test.go::TestInit_FullDanceInTempDir` (//go:build integration). Coverage ≥ 85 % on the init portion of `internal/cli`. | done |
| SDD-17 (`hush secret` — CLI surface for vault management) | `internal/cli/secret_test.go` — `TestSecret_HelpDoesNotMentionValueFlags`, `TestSecret_RootMounts`, `TestSecret_RegistersUnderRoot`, `TestSecret_NoSecretFlagsDeclared`, `TestSecret_AddRefusesPipedStdin`, `TestSecret_AddRefusesValueFlag`, `TestSecret_AddRefusesSecretFlag`, `TestSecret_AddRefusesPasswordFlag`, `TestSecret_AddInvalidName`, `TestSecret_AddTTYHappyPath`, `TestSecret_AddConfirmationMismatch`, `TestSecret_AddDuplicateRefuses`, `TestSecret_AddPassphraseFailureSurfacesAuthCode` (write-half of vault round-trip; locks the operator-facing CLI surface for `add`). Coverage 88.6% on `secret.go`. | done |
| SDD-29 (deploy artefacts) | `deploy/hush.plist` (launchd plist invokes `hush serve --config /usr/local/etc/hush/config.toml`); `deploy/hush.service` (systemd unit `ExecStart=/usr/local/bin/hush serve --config /etc/hush/config.toml`); `deploy/install.sh` (lays binary at `/usr/local/bin/hush` and copies the service file into place — references the same `hush serve` CLI surface); `tests/deploy/smoke_test.go::TestDeploy_PlistParsesAsXML` (proves `ProgramArguments[0] == /usr/local/bin/hush`); `tests/deploy/smoke_test.go::TestDeploy_ServiceParsesAsINI` (proves `ExecStart=` begins with `/usr/local/bin/hush`); `tests/deploy/install_test.go::TestDeploy_InstallIdempotent` (proves the install path materialises on a fresh tempdir) | done |
| **SDD-25 lifecycle harness** | `tests/integration/scenarios_test.go::Test_Scenario_01_FirstInteractive` (proves end-to-end startup and `/hz` responsiveness) | pending |

---

## AC-2 — Vault round-trip + SIGHUP reload

**SPEC reference:** `hush secret add NAME` → `hush secret list` → `hush
secret rotate NAME` → SIGHUP hot-reload preserves all other secrets and
atomically swaps the rotated value with no in-flight request failures.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-03 (vault file format) | `internal/vault/file_test.go::TestVault_RoundTrip_{0,1,5,500}Secrets`, `TestVault_LoadWrongPass_ReturnsAuthFailed`, `TestVault_LoadTruncated*`, `TestVault_LoadLoose*`, `TestVault_Save*`, `TestVault_NoLeakInError`; `internal/vault/codec_test.go`; `internal/vault/store_test.go::TestStore_*`; `internal/vault/permissions_test.go::TestCheck*` | green |
| SDD-03 fuzz | `internal/vault/vault_fuzz_test.go::FuzzVaultDecode` (60s clean, no panic, ≤50 MiB, every error typed) | green |
| SDD-03 race | `internal/vault/store_test.go::TestStore_ConcurrentGet` (100 goroutines, race-clean) | green |
| SDD-03 sentinel-leak | `internal/vault/file_test.go::TestVault_NoLeakInError` (`SECRET_SHOULD_NEVER_APPEAR_3` absent from `err.Error()` and captured slog) | green |
| _Note_ | SIGHUP reload half of AC-2 remains SDD-10's responsibility | — |
| SDD-10 (SIGHUP atomic reload) | `internal/server/integration_test.go::TestSIGHUP_AtomicReload`; `internal/server/reload_test.go::TestReloadVault_HappyPath_SwapsPointer`, `TestReloadVault_FailedReload_PointerUnchanged`, `TestReloadVault_DrainWindowDestroysOnce`, `TestReloadVault_Serialised_TwoSighupsBackToBack`, `TestReloadVault_DuringShutdown_ReturnsErrShuttingDown`, `TestVaultPointerSwap_NoRace` | done |
| SDD-13 (audit chain on rotation) | `internal/audit/chain_test.go::TestAuditChain_HashLinkContiguous`, `TestAuditChain_SignatureValid`, `TestAuditChain_BreakDetectedOnTamper`, `TestAuditChain_BreakDetectedOnDelete`, `TestAuditChain_BreakDetectedOnForgedSignature`, `TestAuditChain_GenesisPrevHashIsDomainSeparated`, `TestAuditChain_HashCoversCanonicalEventWithoutHashOrSignature`; `internal/audit/writer_test.go::TestAuditChain_ResumesFromTail` | done |
| SDD-17 (`hush secret` — write half of vault round-trip) | `internal/cli/secret_test.go` — `TestSecret_AddTTYHappyPath`, `TestSecret_AddDuplicateRefuses`, `TestSecret_RemoveAtomic`, `TestSecret_RemoveAbsent`, `TestSecret_RemoveTokenMismatch`, `TestSecret_ListNoValues`, `TestSecret_ListJSONOutput`, `TestSecret_ListTTYOutput`, `TestSecret_ListSortedAscending`, `TestSecret_ListEmptyVault`, `TestSecret_RotateAtomic`, `TestSecret_RotateSendsSIGHUP`, `TestSecret_RotateMissingPIDTolerant`, `TestSecret_RotateStalePIDTolerant`, `TestSecret_RotateUnreadablePIDTolerant`, `TestSecret_RotateNotOurUserTolerant`, `TestSecret_FileModeAfterAdd`, `TestSecret_FileModeAfterRotate`, `TestSecret_AuditLogOmitsSecretBytes`, `TestSecret_ErrorsDoNotLeakSecretBytes`. Coverage 88.6% on `secret.go`. | done |
| **SDD-25** | `tests/integration/scenarios_test.go::Test_Scenario_13_RotationMidSession` | pending |

---

## AC-3 — Discord approval flow

**SPEC reference:** `hush request --scope X --reason Y --ttl 1h --exec
"env | grep X"` triggers a DM to the configured approver, waits for
approval, and on approval injects the secret into the child process whose
stdout shows the secret value. Denial returns exit 3 with no secret leak.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-11 (Discord bot + Approver interface) | `internal/discord/approver_test.go` — `TestApprover_BotApproverImplementsApprover`, `TestApprovalRequest_DaemonRequiresSupervisorName`, `TestDefaultDMRateLimit_FiveMinutes`; `internal/discord/render_test.go` — `TestApprovalRender_InteractiveLabel`, `TestApprovalRender_DaemonLabel`, `TestApprovalRender_DaemonIncludesSupervisorName`, `TestApprovalRender_VisuallyDistinctFromInteractive`, `TestApprovalRender_AllRequestFieldsPresent`, `TestApprovalRender_NeverIncludesToken`; `internal/discord/ratelimit_test.go` — `TestRateLimit_BlocksSecondPromptWithin5Min`, `TestRateLimit_AllowsAfterWindow`, `TestRateLimit_AlreadyPendingDenies`, `TestRateLimit_CommitNoopWhenNoPending`, `TestRateLimit_PerKeyIsolation`, `TestRateLimit_InteractiveKeyedByClientIP`, `TestRateLimit_TransportUnavailableDoesNotConsumeToken`, `TestRateLimit_DeliveryFailureRefundsToken`, `TestRateLimit_ZeroDMRateLimitUsesDefault`, `TestRateLimit_UsesMonotonicClock`; `internal/discord/monitor_test.go` — `TestMonitor_DisconnectSurfacesUnavailable`, `TestMonitor_DisconnectUnblocksInFlightRequest`, `TestMonitor_ReconnectRestoresAvailability`, `TestMonitor_ReconnectBackoffCappedAt60s`, `TestMonitor_ResumedFlipsAvailable`, `TestBackoffDelay_EdgeCases`, `TestMonitor_ReconnectLoopHandlesOpenFailures`, `TestMonitor_GoroutineExitsOnCtxCancel`; `internal/discord/bot_test.go` — `TestNewBotApprover_ValidatesConfig`, `TestNewBotApprover_DestroyedTokenRejected`, `TestNewBotApprover_BootDownStartsUnavailable`, `TestDecisionRouting_{Approve,Deny,Timeout,CtxCancelled,FirstActionWins}`, `TestInteractionHandler_IgnoresNonComponentEvents`, `TestBotApprover_DisconnectFastPath`, `TestBotApprover_NeverAutoApprovesOnDiscordError`, `TestBotApprover_NoAutoApproveKnobExists`, `TestBotApprover_TokenAbsentFromAllArtifacts`, `TestBotApprover_RaceClean`; `internal/discord/audit_test.go` — `TestAuditChannel_{AllFiveLifecycleEventsMirrored,FailureDoesNotBlockApproval,NoTokenInPayload,DisabledWhenIDEmpty}` | green |
| SDD-12 (claim handler approval flow) | `internal/server/claim_handler_test.go::TestClaim_DiscordTimeout_408`, `TestClaim_DiscordUnavailable_503`, `TestClaim_NoAutoApproveKnobExists`, `TestClaim_Denied_403`, `TestClaim_RateLimited_429`, `TestClaim_UnknownOutcome_503`, `TestClaim_AuditEventEmittedForEveryOutcome`, `TestClaim_ErrorBodyNoSentinel`; `internal/server/claim_handler_integration_test.go::TestClaim_Integration_FullFlow_DiscordStub` | done |
| SDD-28 (8 alert classes + tiered routing + per-supervisor/per-pattern debounce) | `internal/discord/alerts/alerts_test.go` — `TestAlertClass_ExportedSet`, `TestTier_ExportedSet`, `TestNewRouter_NilGuards`, `TestAlert_ApprovalRequest_TierBinding`, `TestAlert_DaemonRefreshRequest_TierBinding`, `TestAlert_ValidatorStaleFailure_TierBinding`, `TestAlert_ChildExit78StaleFailure_TierBinding`, `TestAlert_LogPatternStaleWarning_TierBinding`, `TestAlert_DiscordDisconnected_TierBinding`, `TestAlert_DiscordReconnected_TierBinding`, `TestAlert_VaultUnreachableAtBootTimeout_TierBinding`, `TestRoute_CriticalSendsDM`, `TestRoute_CriticalTransportFailureRefundsBuckets`, `TestRoute_WarningPostsToAuditChannel`, `TestRoute_WarningTransportFailureRefundsBuckets`, `TestRoute_InfoLogsOnly_NoDiscordCall`, `TestRoute_UnknownClass_TypedError`, `TestRoute_CallerSuppliedTierIgnored`, `TestRoute_SlogLevelMatrix`, `TestRoute_SlogAttributeAllowList`, `TestRoute_SentinelDisjointness`, `TestRoute_ConcurrentSafety`, `TestRouter_ZeroNewDependencies`, `TestRoute_AlertTime_NotInSlogRecord`; `internal/discord/alerts/templates_test.go` — 8 `TestAlert_<Class>_RenderSnapshot` (one per class) + `TestTemplate_LabelPrefixUniqueAndStable` + `TestTemplate_OmitEmptyLines` + `TestAlert_NoSecretByteLeakage`; `internal/discord/alerts/ratelimit_test.go` — `TestRateLimit_PerSupervisorBlocksExcess`, `TestRateLimit_PerPatternBlocksExcess`, `TestRateLimit_PerKeyIsolation`, `TestRateLimit_EmptyPatternUsesClassFallback`, `TestRateLimit_AppliesToInfoTier`, `TestRateLimit_MonotonicClock`; `internal/discord/bot_alerts_test.go::TestBotApprover_SatisfiesAlertsSender` (compile-time guard + DM/channel transport via session shim); race-clean; 98.9% statement coverage on `internal/discord/alerts/` | done |
| **SDD-25** | `tests/integration/scenarios_test.go::Test_Scenario_01_FirstInteractive`, `Test_Scenario_10_DiscordUnavailable` | pending |

---

## AC-4 — JWT lifecycle (IP-bind, max-uses, revoke, claims)

**SPEC reference:** After approval, the issued JWT (a) is rejected from a
different IP, (b) is rejected after `max_uses` fetches, (c) can be revoked
via `hush revoke --jti`, (d) carries `session_type` in its claims.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-07 (JWT issue/validate/store) | `internal/token/issue_test.go` — `TestIssue_Interactive`, `TestIssue_Supervisor`, `TestIssue_FreshJTIPerCall`, `TestIssue_HeaderAlg`, `TestIssue_RejectsUnknownSessionType`, `TestIssue_RejectsInvalidParams`, `TestIssue_RespectsCancelledContext`, `TestIssue_NilSignKey`, `TestGenerateJTI_RandReaderError`, `TestIssue_SignFailure`; `internal/token/validate_test.go` — `TestValidate_HappyPath`, `TestValidate_HappyPath_Supervisor`, `TestValidate_DecrementsInteractive`, `TestValidate_RespectsCancelledContext`, `TestValidate_WrongIP`, `TestValidate_IPSemanticallyEqual`, `TestValidate_MalformedRequestIP_Refused`, `TestValidate_AlgConfusion_None_Refused`, `TestValidate_AlgConfusion_HS256_Refused`, `TestValidate_MalformedHeader_Refused`, `TestValidate_ExpiredJWT`, `TestValidate_OutOfScope`, `TestValidate_UnknownSessionType_Refused`, `TestValidate_MalformedClaimIP_Refused`, `TestValidate_BadSignature`, `TestValidate_NoLeakOnError`; `internal/token/store_test.go` — `TestNewStore_Defaults`, `TestStore_ExhaustedInteractive_Refused`, `TestStore_SupervisorIgnoresMaxUses`, `TestStore_AddOnRevokedJTI_Refused`, `TestStore_ConsumeUse_ExpiredRecord`, `TestStore_ConcurrentDecrement`, `TestStore_CleanupRemovesExpired`, `TestStore_CleanupConcurrentWithValidate`, `TestStore_CleanupNeverTouchesRevoked`, `TestStore_CleanupReturnsOnContextDone`, `TestStore_ConsumeUse_RevokedSetHit`; `internal/token/revoke_test.go` — `TestStore_RevokedJTI_Refused`, `TestStore_RevokeIsIdempotent`, `TestStore_RevokedSurvivesCleanup`; `internal/token/alg_es256k_test.go` — `TestRegisterOnce_Concurrent`, `TestES256KMethod_RoundTrip`; `internal/token/claims_test.go` — `TestSessionType_Vocabulary`, `TestClaims_JSONRoundTrip`; `internal/token/errors_test.go` — `TestErrors_DistinctIdentities` | green |
| SDD-07 fuzz | `internal/token/validate_fuzz_test.go::FuzzJWTValidate` (60 s clean, no panic, every error a typed sentinel) | green |
| SDD-12 | `internal/server/claim_handler_test.go::TestClaim_Approved_IssuesJWT`, `TestClaim_TTLCappedAtConfigMax`, `TestClaim_SupervisorRequest_DaemonLabel` (JWT carries capped TTL, session_type, max_uses) | done |
| SDD-13 | `internal/server/secret_handler_test.go::TestSecret_WrongIP_401`, `TestSecret_ExhaustedInteractive_401`, `TestSecret_RevokedJWT_401`, `TestSecret_OutOfScope_403`, `TestSecret_ExpiredJWT_401`, `TestSecret_SupervisorIgnoresMaxUses`, `TestSecret_HappyPath_ECIESPayload`; `internal/server/revoke_handler_test.go::TestRevoke_HappyPath`, `TestRevoke_BadSignature_403`, `TestRevoke_UnknownJTI_403_AsBadSignature`, `TestRevoke_ReplayedNonce_403`, `TestRevoke_StaleTimestamp_403`, `TestRevoke_IdempotentReRevocation_200_StaticBody` | done |
| **SDD-25** | `tests/integration/scenarios_test.go::Test_Scenario_07_VaultRestart` | pending |

---

## AC-5 — `hush request --exec` injection safety

**SPEC reference:** With `--exec`, secrets exist only in the child
process's environment. The ephemeral private key is zeroed from the client's
memory after fetch. With `--format eval` AND no `--exec`, a stderr warning
is printed.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-16 (`hush request`) | `internal/cli/request_test.go::TestRequest_RequiresExecOrFormat_NoNetwork`, `TestRequest_ExecInjectsEnvVars`, `TestRequest_ExecPropagatesChildExitCode`, `TestRequest_PostExecZeroesEphemeralKey`, `TestRequest_PartialFetchFailureAbortsBeforeChild`, `TestRequest_NeverWritesJWTToDisk`, `TestRequest_NeverWritesSecretToDisk`, `TestRequest_LogsNeverContainSecretValue`, `TestRequest_ErrorsDoNotLeakSecretBytes`, `TestRequest_ExecOnlyChildHasSecret` (sentinel-leak); `internal/cli/request_integration_test.go::TestRequest_FullFlowWithDiscordStubApproveAll` (//go:build integration) | done |
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
| SDD-15 (init + Keychain) | `internal/cli/init_test.go` — `TestInitClient_RequiresMachineIndex`, `TestInitClient_RejectsNegativeMachineIndex`, `TestInitClient_RejectsOversizedMachineIndex`, `TestInitClient_StoresInKeychainViaFake`, `TestInitClient_PrintsFingerprintOneLine`, `TestInitClient_DeterministicAcrossRuns`, `TestInitClient_DistinctInputsProduceDistinctFingerprints`, `TestInitClient_RefusesPreExistingKeychainItem`, `TestInitClient_ConflictsWithServerMode`, `TestInitClient_RejectsConfirmationMismatch`, `TestInitClient_NoStderrOnSuccess`, `TestInitClient_NeverLeaksDerivedKeyToOutput`; `internal/cli/init_integration_test.go::TestInit_FullDanceInTempDir` (//go:build integration). | done |
| SDD-15 (keychain wrapper) | `internal/keychain/keychain_test.go` — `TestKeychain_StoreRetrieveRoundTrip`, `TestKeychain_DeleteRemoves`, `TestKeychain_StoreRefusesDuplicate`, `TestKeychain_FakeDestroyZeroes`, `TestKeychain_NewReturnsInterface`, `TestPerBinaryACLSupported_ReportsPerPlatform`; `internal/keychain/keychain_darwin_test.go` (//go:build darwin) — `TestKeychainDarwin_ConstructedSecurityCommand`, `TestKeychainDarwin_StoreReturnsItemExistsOn45`, `TestKeychainDarwin_RetrieveExitCode44IsNotFound`, `TestKeychainDarwin_RetrieveExitCode51IsPermissionDenied`, `TestKeychainDarwin_RetrieveParsesStdoutPayload`, `TestKeychainDarwin_DeleteSucceedsAndIsNotIdempotent`, `TestKeychainDarwin_StoreSecretViaStdinNotArgv`, `TestPerBinaryACLSupported_Darwin`. Coverage 89 %. | done |
| SDD-16 (`hush request` keychain consumer) | `internal/cli/request_test.go::TestRequest_ClientKeyFromKeychainNotEnv` (proves the per-`--machine-index` keychain account is the only signing-key source — env var of the same name does NOT bleed through), `TestRequest_KeychainMissExitErr` (locked stderr message refers to the supplied `--machine-index`), `TestRequest_FormatEvalEmitsStderrWarning` (--format eval byte-equal stderr WARNING per docs/SECURITY.md §6), `TestRequest_FormatEvalEscapesSingleQuote` (single-quote round-trip via bash); `internal/cli/request_integration_test.go::TestRequest_FullFlowFormatEvalIntegration` (//go:build integration) | done |
| SDD-29 (deploy artefacts) | `deploy/install.sh` (banner prints `security add-generic-password -T "/usr/local/bin/hush" ...` — the operator-runnable invocation that binds Keychain ACL to the resolved binary path; install.sh creates ZERO Keychain entries itself per FR-003 lock); `tests/deploy/install_test.go::TestDeploy_InstallIdempotent` (asserts `assertBannerKeychainACL` — banner contains exactly one `-T "<resolved-binary-path>"`, zero `-T "*"` wildcards, zero `-A` allow-all-apps tokens — on macOS); `tests/deploy/install_test.go::TestDeploy_InstallBannerByteIdentical` (FR-001 banner regression guard) | done |
| SDD-30 (TOML example) | `internal/supervise/config/config_test.go::TestExamples_GenericTOMLValidates` | pending |

---

## AC-7 — End-to-end ECIES, no plaintext on the wire

**SPEC reference:** A captured HTTP response body to `/h/{prefix}/s/{name}`
contains no plaintext secret value. Decrypting with the wrong ephemeral
private key fails cleanly.

| Owning chunk | Test path | Status |
|--------------|-----------|--------|
| SDD-01 (BIP32 derivation + ephemeral keys) | `internal/keys/derive_test.go`, `internal/keys/paths_test.go`, `internal/keys/client_test.go`, `internal/keys/fingerprint_test.go`, `internal/keys/derive_fuzz_test.go` | done |
| SDD-02 (Layer 5 — mlocked secure memory + zero-on-destroy) | `internal/vault/securebytes/securebytes_test.go` — `TestSecureBytes_New_CopiesAndZeroesInput`, `TestSecureBytes_Use_DeliversPayload`, `TestSecureBytes_Render_RedactsAllPaths`, `TestSecureBytes_RedactionSentinel`, `TestSecureBytes_Destroy_ZeroesAndIdempotent`, `TestSecureBytes_PostDestroy_ReturnsErrDestroyed`, `TestSecureBytes_FinalizerZerosOnGC`, `TestSecureBytes_ConcurrentUse` | done |
| SDD-08 (request signing) | `internal/transport/sign/canonical_test.go`, `internal/transport/sign/sign_test.go`, `internal/transport/sign/verify_test.go`, `internal/transport/sign/nonce_test.go`, `internal/transport/sign/timestamp_test.go`, `internal/transport/sign/errors_test.go` | green |
| SDD-08 fuzz | `internal/transport/sign/verify_fuzz_test.go::FuzzVerifyRequest` (60s clean, no panic, every error a typed sentinel) | green |
| SDD-09 (ECIES) | `internal/transport/ecies/ecies_test.go` — `TestECIES_RoundTrip_1B`, `TestECIES_RoundTrip_1KB`, `TestECIES_RoundTrip_1MB`, `TestECIES_EncryptIsRandomised`, `TestECIES_EnvelopeMeetsMinSize`, `TestECIES_NoPlaintextSubstringInEnvelope`, `TestECIES_DecryptWrongKey_Fails`, `TestECIES_DecryptMangledEnvelope_Fails`, `TestECIES_DecryptTruncatedEnvelope_Fails`, `TestECIES_DecryptAppendedByte_Fails`, `TestECIES_DecryptEmptyEnvelope_TooShort`, `TestECIES_DecryptReturnsSecureBytes`, `TestECIES_EncryptZeroesInternalBuffersOnSuccess`, `TestECIES_EncryptZeroesInternalBuffersOnError`, `TestECIES_EncryptDoesNotMutateCallerSlice`, `TestECIES_EncryptRejectsEmpty`, `TestECIES_EncryptRejectsNilPub`, `TestECIES_EncryptRejectsWrongCurvePub`, `TestECIES_EncryptRespectsCancelledContext`, `TestECIES_DecryptRespectsCancelledContext`, `TestECIES_DecryptRespectsDeadlineContext`, `TestECIES_NoLeakOnError`, `TestECIES_ConcurrentRoundTrip`; `internal/transport/ecies/internals_test.go` — whitebox helper + seam-injection tests | green |
| SDD-09 fuzz | `internal/transport/ecies/decrypt_fuzz_test.go::FuzzECIESDecrypt` (60s clean, no panic, every error a typed sentinel) | green |
| SDD-13 (server `/s` handler ECIES output) | `internal/server/secret_handler_test.go::TestSecret_HappyPath_ECIESPayload`, `TestSecret_ErrorBodyNoSentinel`; `internal/audit/writer_test.go::TestAudit_RecordNoSecretValue` | done |
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
| SDD-06 (config validation) | `internal/config/server_test.go`, `internal/config/validate_test.go` — `TestServer_RejectsLoopback`, `TestServer_RejectsUnspecified`, `TestServer_RejectsPublic`, `TestServer_RejectsRequireTailscaleFalse`, `TestServer_RejectsArgonMemoryUnder256`, `TestServer_RejectsAuditLogOutsideStateDir`, `TestServer_RejectsMissingStateDir`, `TestServer_RejectsStateDirNotADirectory`, and 35+ more | verified by SDD-06 |
| SDD-06 fuzz | `internal/config/server_fuzz_test.go::FuzzServerTOML` (60s clean, no panics, every error a typed sentinel) | verified by SDD-06 |
| SDD-10 (startup checks) | `internal/server/startup_checks_test.go::TestStartupChecks_RefusesPublicBind`, `TestStartupChecks_RefusesEmptyHostBind`, `TestStartupChecks_RefusesAddrNotOnInterface`, `TestStartupChecks_TailscaleBind_ListerError`, `TestStartupChecks_RefusesLooseFileMode`, `TestStartupChecks_RefusesLooseDirMode`, `TestStartupChecks_SkipsFileModesWhenDisabled`, `TestStartupChecks_RefusesUnsyncedClock`, `TestStartupChecks_RefusesClockDriftOver60s`, `TestStartupChecks_RefusesProbeError`, `TestStartupChecks_SkipsClockSyncWhenDisabled`, `TestStartupChecks_RefusesMissingStateDir`, `TestStartupChecks_RefusesStateDirIsFile`, `TestStartupChecks_RefusesStateDirSymlink`, `TestStartupChecks_OrderedExecution`, `TestStartupChecks_AuditEmitsRefused` | done |
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
| SDD-31 (release gates) | `.github/workflows/ci.yml` (per-PR matrix: format-check + lint + test:race + pre-commit + govulncheck + gitleaks + 30 s × 6 fuzz smoke + codecov upload + no-vendor + no-CGO + coverage-threshold) · `.github/workflows/fuzz-cron.yml` (nightly 300 s × 6 fuzz, linux-amd64 matrix-by-target, crashing-input artefacts FR-022) · `.github/workflows/release.yml` (tag → goreleaser + cosign keyless via OIDC, manifest-only) · `.github/scripts/coverage-threshold/` (project ≥ 90 % + 100 % on the seven security-critical packages + FR-016 byte-equality against constitution fenced block) · `.github/scripts/govulncheck-filter/` (FR-008 waiver authority via `.govulncheck-allow.yml`) | done |
| **SDD-25** (provides the integration coverage that lifts AC-10 paths) | `tests/integration/scenarios_test.go` (17/17 green with `-race`); harness skeleton + Scenario 14 (`Test_Scenario_14_DuplicateStart`) green in chunk 1 — remaining 16 scenarios surface as `t.Fatalf` until upstream wiring lands | pending |

**Required fuzz targets (Constitution VIII §2):**

| # | Target | Owning chunk |
|---|--------|--------------|
| 1 | Vault file decode | SDD-03 (`FuzzVaultDecode`) |
| 2 | JWT parse/validate | SDD-07 (`FuzzJWTValidate`) |
| 3 | ECIES decrypt input | SDD-09 (`FuzzECIESDecrypt`) |
| 4 | Request signature payload | SDD-08 (`FuzzVerifyRequest`) |
| 5 | Supervisor config TOML | SDD-18 (`FuzzSuperviseTOML` — `internal/supervise/config/config_fuzz_test.go`; **done** 60s clean) |
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

**Locked symbol-name list** (per spec FR-002 — 17 functions; 15 scenarios with Scenario 9 split into Strict/Grace and Scenario 11 split into Ready/BootTimeout):

| # | Scenario | Test name (`tests/integration/scenarios_test.go::…`) | Status |
|---|----------|-----------|--------|
| 1 | First interactive shell request | `Test_Scenario_01_InteractiveShellRequest` | pending |
| 2 | First daemon bootstrap | `Test_Scenario_02_FirstDaemonBootstrap` | pending |
| 3 | Clean child exit → silent refill | `Test_Scenario_03_CleanChildExitRefill` | pending |
| 4 | Child crash within valid session TTL | `Test_Scenario_04_ChildCrashRefill` | pending |
| 5 | Child exit 78 stale-credential contract | `Test_Scenario_05_ChildExit78Stale` | pending |
| 6 | Validator catches bad secret before child start | `Test_Scenario_06_ValidatorFailure` | pending |
| 7 | Vault server restart (401 unknown-jti) | `Test_Scenario_07_VaultRestartInvalidatesSession` | pending |
| 8 | Daytime refresh-window prompt | `Test_Scenario_08_DaytimeRefresh` | pending |
| 9a | Overnight expiry — strict mode | `Test_Scenario_09_OvernightExpiry_Strict` | pending |
| 9b | Overnight expiry — grace-cache mode | `Test_Scenario_09_OvernightExpiry_Grace` | pending |
| 10 | Discord unavailable during new claim | `Test_Scenario_10_DiscordUnavailable` | pending |
| 11a | Tailscale boot retry succeeds | `Test_Scenario_11_TailscaleReady` | **green** (SDD-25 chunk 2) |
| 11b | Tailscale boot retry exhausts budget | `Test_Scenario_11_BootTimeout` | **green** (SDD-25 chunk 2) |
| 12 | Agent status check before long task | `Test_Scenario_12_AgentStatusCheck` | **green** (SDD-25 chunk 2) |
| 13 | Secret rotated on vault host during active daemon session | `Test_Scenario_13_MidSessionRotation` | pending |
| 14 | Duplicate supervisor start attempt | `Test_Scenario_14_DuplicateStart` | **green** (SDD-25 chunk 1) |
| 15 | Log-pattern watchdog sees auth failure string | `Test_Scenario_15_LogPatternMatch` | pending |

**SDD-25 chunk-1 delivery (this PR)**: harness scaffolding shipped —
`tests/integration/lifecycle_test.go` (`TestMain` + child-mode dispatcher +
loopback `RoundTripper` allow-list), `tests/integration/scenarios_test.go`
(17 locked `Test_Scenario_*` symbols), `tests/integration/harness/` (6-file
inventory: `log_capture.go`, `vault.go`, `discord.go`, `child.go`,
`server.go`, `supervisor.go`), and **Scenario 14 green under `-race` with
5/5 flake-free runs**. The remaining 16 scenarios surface as `t.Fatalf`
"harness wiring not yet complete" failures on every invocation per spec
FR-001 (no `t.Skip` permitted) — those failures are the load-bearing
operator signal that AC-10 is still partially unmet. AC-10 status reaches
`green` only when all 17 pass on a fully-shipped upstream.

**Supporting chunks** (provide the building blocks for these scenarios):

- SDD-18 (supervisor config TOML — **done**: `internal/supervise/config/config_test.go`, `internal/supervise/config/validate_test.go`, `internal/supervise/config/config_fuzz_test.go`; ≥95% coverage; 60s fuzz clean)
- SDD-19 (state machine — **done**: `internal/supervise/state.go`, `internal/supervise/state_test.go`; 15 tests T-01..T-15 covering the locked 5×15 transition table — 19 legal cells via `TestStore_LegalTransitions`, 56 illegal cells via `TestStore_IllegalTransitionErr`; defensive snapshot + race-clean concurrent test; `*SecureBytes` redaction proven by `TestStore_TokenLogValueRedacts`; 100% coverage on `state.go`)
- SDD-20 (child fork/exec, signal forwarding, exit-78 detection, pgid death-watch — **done**: `internal/supervise/child.go`, `internal/supervise/child_linux.go`, `internal/supervise/child_darwin.go`, `internal/supervise/child_test.go`, `internal/supervise/child_internal_test.go`, `internal/supervise/child_linux_test.go`, `internal/supervise/child_darwin_test.go`, `internal/supervise/child_darwin_internal_test.go`; tests `TestChild_LoggerNilPanicsAtNewChild`, `TestChild_RejectsEmptyCommand`, `TestChild_RejectsRelativeCommand`, `TestChild_StartAndWait_HappyPath`, `TestChild_Wait_NonZeroExitCodeVerbatim`, `TestChild_Wait_TerminatingSignalDistinct`, `TestChild_Exit78Detection`, `TestChild_SignalForwardingSIGTERM`, `TestChild_ForwardAfterExit_ErrChildNotStarted`, `TestChild_ForwardingGoroutineExitsOnCtxCancel`, `TestChild_StdoutPipeNonBlocking`, `TestChild_OverflowWarning_OneEpisodePerStream`, `TestChild_ConcurrentWaitOK`, `TestChild_RestartCycles_NoGoroutineLeak`, `TestChild_PgidIsolation_KillingPgKillsChildren`, `TestChild_PIDReturnsZeroBeforeStartAndAfterWait`, `TestChild_DoubleWait_LoserGetsErrChildNotStarted`, `TestChild_WaitBeforeStart`, `TestChild_StartFailsForBadAbsolutePath`, `TestChild_DrainLoopRecoversFromSinkPanic`, plus internal `Test*RingBuffer*` and `TestForward_CoalescesOnFullBuffer`, plus build-tagged `TestChild_LinuxPdeathsig` (linux) and `TestChild_DarwinDeathWatch` (darwin); race-clean; ≥90% coverage on `child{,_linux,_darwin}.go`)
- SDD-21 (refill, refresh, grace cache — **done**: `internal/supervise/refill.go`, `internal/supervise/refresh.go`, `internal/supervise/grace.go`, `internal/supervise/refill_test.go`, `internal/supervise/refresh_test.go`, `internal/supervise/grace_test.go`, `internal/supervise/helpers_test.go`; tests covering Lifecycle Scenarios 3 (`TestRefill_SilentOnCleanExit`), 7 (`TestRefill_401UnknownJTITransitions`), 8 (`TestRefresh_FiresInWindow`, `TestRefresh_FiresOnStartIfInsideWindow`, `TestRefresh_T30MinFallback`), 9 (`TestGrace_UsesCacheOnExpiredJWT`, `TestGrace_TTLCapAt4h`), 11 (`TestBootRetry_BackoffRespected`, `TestBootRetry_NeverPromptsDiscord`); race-clean (`TestRefresh_StopsOnCtxCancel` asserts no goroutine leak); ≥97% coverage on the three new files; marker-byte tests `TestRefill_NeverStringifiesDecryptedBytes` + `TestGrace_NeverRendersValueAsString` + `TestRefill_BearerTokenNeverLeaksToLogs` lock the Constitution X invariant)
- SDD-22 (pidfile, status socket — **done**: `internal/supervise/pidfile.go`, `internal/supervise/socket.go`, `internal/supervise/socket_darwin.go`, `internal/supervise/socket_linux.go`, `internal/supervise/pidfile_test.go`, `internal/supervise/socket_test.go`; Lifecycle Scenarios 12 (agent status check) and 14 (duplicate supervisor refused) each have a passing unit test in this chunk — Scenario 12 covered by `TestSocket_StatusJSONShape` + `TestSocket_StatusJSONFromSnapshot` + `TestSocket_PreAttachDefaultsRenderShapeConformant` + `TestSocket_StoreSnapshotPath`; Scenario 14 covered by `TestPidFile_FlockExclusive` + `TestPidFile_DuplicateRefused`; full SDD-22 test set: `TestPidFile_FlockExclusive`, `TestPidFile_DuplicateRefused`, `TestPidFile_StaleAcquired`, `TestPidFile_ReleaseRemovesFile`, `TestPidFile_WritesOwnPID`, `TestPidFile_Mode0600`, `TestPidFile_ParentMode0700Created`, `TestPidFile_ParentLooseRefuses`, `TestPidFile_OpenFailureWhenPathIsDirectory`, `TestPidFile_NilReceiverReleaseReturnsAlreadyReleased`, `TestPidFile_ReleaseUnlockErrorOnClosedFd`, `TestPidFile_ReleaseRemoveErrorSurfaced`, `TestSocket_StatusJSONShape`, `TestSocket_StatusJSONFromSnapshot`, `TestSocket_TokenInResponseRedacted`, `TestSocket_PreAttachDefaultsRenderShapeConformant`, `TestSocket_GracefulShutdownOnCtx`, `TestSocket_ConnectionForceClosedOnCtxCancel`, `TestSocket_RebindAfterStop`, `TestSocket_RunSecondCallReturnsErrAlreadyRunning`, `TestSocket_NoGoroutineLeak`, `TestSocket_PreviousSocketCleanedUp`, `TestSocket_Mode0600`, `TestSocket_ParentMode0700`, `TestSocket_ParentLooseRefuses`, `TestSocket_NoTCPListenerOrHTTPServer`, `TestNewStatusServer_NilLoggerPanics`, `TestSocket_RenderStatusLastAuthFailureNonNil`, `TestSocket_EnsureParentNotDirectory`, `TestSocket_StoreSnapshotPath`, `TestSocket_RunFailsWhenStaleNonEmptyDirAtPath`, `TestSocket_RunFailsListenOnImpossiblePath`, `TestSocket_HandlerRecoversFromInputsPanic`, `TestSocket_RunFailsParentMkdirAll`, `TestSocket_EnsureParentStatError`, `TestSocket_DefaultRuntimeDirFallback`, plus fuzz target `FuzzStatusJSON_Encode`; race-clean; mode `0600` socket / `0700` parent enforcement proven by `TestSocket_Mode0600` + `TestPidFile_Mode0600` + `TestSocket_ParentMode0700` + `TestPidFile_ParentMode0700Created`; Token redaction in status response proven by `TestSocket_TokenInResponseRedacted` (Constitution X / FR-022-13); static "no TCP / no HTTP / no bearer" guard via `TestSocket_NoTCPListenerOrHTTPServer`)
- SDD-23 (supervise + client status + client refresh CLIs — **done**: `internal/cli/supervise.go`, `internal/cli/client.go`, `internal/cli/supervise_test.go`, `internal/cli/client_test.go`, `internal/cli/supervise_integration_test.go`; AC-10 Scenarios 12 (agent status check) covered by `TestClientStatus_TTYHumanSummary` + `TestClientStatus_PipeJSON` + `TestClientStatus_JsonFlagOverridesTTY` + `TestClientStatus_AutoDetectSingleSocket`; AC-10 Scenario 13 (mid-session refresh) covered by `TestClientRefresh_AckMapsToExitOK` + `TestClientRefresh_ErrorMapsToExitErr` + supervise-side verb dispatch `TestSocket_VerbRefreshInvokesHandler` + `TestSocket_VerbRefreshErrorIsSerialised`; AC-10 Scenario 14 (duplicate supervisor refused) covered by `TestSupervise_DuplicateStartRefused`; SDD-23 orchestration discipline tests `TestSupervise_OrchestrationDelegatesToInternalSupervise` + `TestSupervise_PerOSGrepClean` + `TestClientStatus_PerOSGrepClean`; sentinel-leak tests `TestSupervise_NoSecretInErrorMessages` + `TestClientStatus_NoSecretInOutput` + `TestClientRefresh_NoSecretInOutput`; integration test `TestSuperviseIntegration_DryRunWithDiscordStub` proves no Discord call on dry-run; race-clean; ~94% per-function coverage on `supervise.go` + `client.go`)
- SDD-24 (supervisor orchestration glue — **done**: `internal/supervise/lifecycle.go`, `internal/supervise/lifecycle_audit.go`, `internal/supervise/lifecycle_boot.go`, `internal/supervise/lifecycle_child.go`, `internal/supervise/lifecycle_refresh.go`, `internal/supervise/lifecycle_interfaces.go`, `internal/supervise/lifecycle_test.go`, `internal/supervise/lifecycle_testutil_test.go`; AC-10 precondition unblocked — composes the SDD-19..22 primitives into the documented daemon lifecycle behind the locked `Lifecycle`/`Deps`/`NewLifecycle`/`Run` surface plus the three single-method consumer interfaces `Validator`/`Alerts`/`Watchdog`; orchestrator tests: `TestLifecycle_BootSubmitsClaim`, `TestLifecycle_BootRetrySucceedsAfterNFailures`, `TestLifecycle_BootRetryTimeoutExhausted`, `TestLifecycle_ClaimDeniedTransitionsToAwaitingApproval`, `TestLifecycle_ClaimDiscordUnavailableEmitsAlert`, `TestLifecycle_ValidatorFailureBlocksChildStart`, `TestLifecycle_ChildExitZeroTriggersSilentRefill`, `TestLifecycle_ChildExitNonZeroTriggersSilentRefill`, `TestLifecycle_ChildExit78EmitsStaleAlertNoRestart`, `TestLifecycle_RefillJTIUnknownTransitionsToAwaitingApproval`, `TestLifecycle_RefillTransientErrorPostRunningEmitsRefillFailed`, `TestLifecycle_RefresherTickSubmitsFreshClaim`, `TestLifecycle_GracefulShutdownDrainsChild`, `TestLifecycle_BootRetryShutdownNeverContactsDiscord`, `TestLifecycle_RunIsSingleShot`, `TestLifecycle_NoBusinessLogicLeakage`, `TestLifecycle_StatusSocketRefreshInFetchingRejects`; race-clean; audit-vocabulary reconciliation locked by `TestSpecFR14AuditSync` in `internal/audit/chain_test.go`; row remains `pending` until SDD-25 ships its 15-scenario harness green)
- SDD-26 (pre-flight credential validators — **done** (FR-13 / Lifecycle Scenario 6 supervisor-side gate): `internal/supervise/validators/validators.go`, `internal/supervise/validators/anthropic.go`, `internal/supervise/validators/anthropic_oauth.go`, `internal/supervise/validators/openai.go`, `internal/supervise/validators/google_ai.go`, `internal/supervise/validators/github.go`, `internal/supervise/validators/export_test.go`, `internal/supervise/validators/validators_test.go`, `internal/supervise/validators/anthropic_test.go`, `internal/supervise/validators/anthropic_oauth_test.go`, `internal/supervise/validators/openai_test.go`, `internal/supervise/validators/google_ai_test.go`, `internal/supervise/validators/github_test.go`; 103 named tests (18 shared + 17 × 5 per-provider) including `TestValidator_InterfaceHasOneMethod`, `TestRegistry_AllFiveNamesPresent`, `TestRegistry_GetUnknownName_FalseFound`, `TestRegistry_ExactlyFiveNames`, `TestRegistry_GetIsRaceClean`, `TestPackage_SentinelsArePairwiseDistinct`, `TestPackage_SentinelStringsAreLiteral`, `TestPackage_LogRecordSchema_Success`, `TestPackage_LogRecordSchema_Failure`, `TestPackage_LogAttrsAreAllowList`, `TestPackage_NoStringConversionsOfSecret`, `TestPackage_NoRequestObjectInLogOrError`, `TestPackage_AllBuildersZeroLocalBuffer`, `TestPackage_NoLiveProviderHosts`, `TestPackage_ZeroNewDependencies`, `TestPackage_DefaultClientTimeoutIs5s`, `TestPackage_CallerSuppliedClientReturnedVerbatim`, `TestExport_SetLoggerForTest_AllProvidersCovered`; per-provider (× 5: Anthropic/AnthropicOAuth/OpenAI/GoogleAI/GitHub) `TestValidator_InterfaceSatisfied_<P>`, `TestValidator_<P>_HappyPath_200`, `TestValidator_<P>_StaleCredential_401`, `TestValidator_<P>_StaleCredential_403`, `TestValidator_<P>_NetworkError_5xx`, `TestValidator_<P>_Timeout`, `TestValidator_<P>_NetworkError_Refused`, `TestValidator_<P>_Redirect3xx_ClassifiedAsNetwork`, `TestValidator_<P>_CtxCancelledBeforeSend_NoHandlerInvocation`, `TestValidator_<P>_CtxCancelledMidFlight`, `TestValidator_<P>_SingleRequest`, `TestValidator_<P>_Concurrent`, `TestValidator_<P>_DestroyedSecureBytes`, `TestValidator_<P>_NoLeakOnError` (SC-006 sentinel-leak `SECRET_SHOULD_NEVER_APPEAR_26` absent from `err.Error()` AND every captured slog record), `TestValidator_<P>_NameIsLockedString`, `TestValidator_<P>_AuthHeaderShape`, `TestValidator_<P>_EmptyCredentialForwarded`; race-clean; 98.1% statement coverage on `internal/supervise/validators/` — exceeds 90% chunk-doc target; SDD-24 routing of `ErrStaleCredential` into `AlertClassValidatorFailure` → `[STALE] Validator Failure` Discord DM + `awaiting-approval` transition remains outside this chunk's scope)
- SDD-27 (watchdog — **done** (alert-only): `internal/supervise/watchdog/watchdog.go`, `internal/supervise/watchdog/watchdog_test.go`; AC-10 Scenario 15 emission half covered by 24 named tests including the seven chunk-doc-mandated names — `TestWatchdog_PatternMatchEmitsAlert`, `TestWatchdog_NoMatchNoAlert`, `TestWatchdog_RateLimitBlocksExcess` (asserts WARN per suppression — sentinel-byte assertion proves Q2 no-line-content invariant), `TestWatchdog_NeverTransitionsState` (alert-only proof via `recordingStateDouble` + `go list` import audit asserting zero forbidden `internal/supervise/*` sub-paths), `TestWatchdog_RunStopsOnCtxCancel` (SC-004 250ms cancel budget + `runtime.NumGoroutine` baseline), `TestWatchdog_ConcurrentLogIngest` (8 producers × 500 ingests race-clean), `TestWatchdog_PrecompiledPatternsReused` (FR-008 + SC-006 — `Pattern.Regex` pointer identity preserved across defensive copy + `testing.AllocsPerRun` snapshot); plus `TestWatchdog_NewWatchdog_ValidPatternSet`, `TestWatchdog_NewWatchdog_DuplicatePatternName`, `TestWatchdog_NewWatchdog_InvalidInputs`, `TestWatchdog_EmptyPatternSetIsBenign`, `TestWatchdog_MultipleMatchesOnSameLine`, `TestWatchdog_MultipleSpansSingleEmit`, `TestWatchdog_RunSingleShot`, `TestWatchdog_IngestAfterRunReturnIsNoop`, `TestWatchdog_SC001_EmitLatencyUnder100ms`, `TestWatchdog_BucketRefillsAfterInterval`, `TestWatchdog_PerPatternBudgetIsolation`, `TestWatchdog_IngestNonBlockingWhenQueueFull`, `TestWatchdog_QueueFullDropEpisodeOnceWARN`, `TestWatchdog_AlertOutputSaturatedDropsWARN`, `TestWatchdog_NoSecureBytesStringConversion`, `TestWatchdog_ZeroNewDependencies`, `TestWatchdog_SatisfiesSuperviseInterface`; race-clean; 96.4% statement coverage on the new sub-package; SDD-28 routing of the emitted `Event` into `[STALE] Log Pattern Match` Discord alerts remains outside this chunk's scope)
- SDD-29 (deploy artefacts — **done** (daemon-lifecycle delivery half): `deploy/hush.plist` (launchd job that loads `hush serve` at boot, `UserName=_hush` non-root, `RunAtLoad=true`/`KeepAlive=true` — the lifecycle entry point on macOS); `deploy/hush.service` (systemd unit, `User=@HUSH_USER@` substituted by install.sh, `Restart=on-failure` + `NoNewPrivileges`/`ProtectSystem=strict`/`ProtectHome=true`/`PrivateTmp=true` hardening — the lifecycle entry point on Linux); `deploy/install.sh` (idempotent installer with `tmutil addexclusion` Constitution-XI hard-fail on missing tmutil; state-dir `0700`-owned by `${HUSH_USER}`; zero Keychain entries created — operator runs the banner's `security` invocation separately); `deploy/supervise-launch.sh.template` (generic per-daemon launcher operators copy + customise; execs `hush supervise` exclusively, refuses to run with unsubstituted placeholders via pre-flight `grep` guard exiting 78 — defends Constitution IV's TTL discipline against the `hush request --exec` re-prompt trap); validated by `tests/deploy/install_test.go` (idempotency + unsupported-OS + missing-binary + missing-tmutil + byte-identical-banner) and `tests/deploy/smoke_test.go` (plist XML parse + unit INI parse + launcher-uses-supervise + no-operator-specific-names + every shell file `bash -n` parses); race-clean; 10 named `TestDeploy_*` functions all green under `magex test:race -tags=integration -run TestDeploy_ ./tests/deploy/...`; SDD-25 wires the supervisor-lifecycle scenarios that exercise the deployed daemons)
- SDD-28 (Discord alert surface — **done** (AC-3 + AC-10 emission half): `internal/discord/alerts/alerts.go`, `internal/discord/alerts/templates.go`, `internal/discord/alerts/ratelimit.go`, `internal/discord/alerts/alerts_test.go`, `internal/discord/alerts/templates_test.go`, `internal/discord/alerts/ratelimit_test.go`, `internal/discord/bot_alerts.go`, `internal/discord/bot_alerts_test.go`; 8 closed `AlertClass` constants verbatim from `docs/LIFECYCLE-SCENARIOS.md` §"Required alert classes" lines 301-314; 3-tier router (Critical → owner DM, Warning → audit channel, Info → slog INFO; zero Discord network call); per-supervisor + per-pattern minimum-interval debounce with commit-on-success semantics; alert emission half of Scenarios 2 (`AlertClassDaemonRefreshRequest`), 5 (`AlertClassChildExit78StaleFailure`), 6 (`AlertClassValidatorStaleFailure`), 8 (`AlertClassDaemonRefreshRequest`), 10 (`AlertClassDiscordDisconnected`/`AlertClassDiscordReconnected`), 11 (`AlertClassVaultUnreachableAtBootTimeout`), 15 (`AlertClassLogPatternStaleWarning`); 8 `TestAlert_<Class>_RenderSnapshot` + 8 `TestAlert_<Class>_TierBinding` + 6 rate-limit tests + tier-routing tests proving Critical-DM/Warning-channel/Info-no-Discord (`TestRoute_InfoLogsOnly_NoDiscordCall` fails if any Sender method is invoked) + sentinel-byte test `TestAlert_NoSecretByteLeakage` proving no credential-shaped substring survives any render path + `TestRouter_ZeroNewDependencies` AST-import audit; 25 named tests total; race-clean; 98.9% statement coverage on `internal/discord/alerts/` — exceeds 90% chunk-doc target; SDD-25 wires the producer→delivery mapping in the lifecycle layer)

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
