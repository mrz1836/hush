# Tasks: hush request — interactive secret fetch (`--exec` | `--format eval`)

**Input**: Design documents from [`/specs/016-cli-request/`](./)
**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/cli-request.md](./contracts/cli-request.md), [quickstart.md](./quickstart.md)

**Tests**: TDD-mandatory per Constitution VIII. Every behaviour contract has a test-writing task **before** the corresponding implementation task. Tests MUST FAIL when first written; implementation tasks make them pass.

**Coverage target**: 90% on `internal/cli` (request portion). Sentinel-leak + key-zeroing paths reach 100%.

**Sentinel**: `internal/testutil.SentinelSecret(16)` → `SECRET_SHOULD_NEVER_APPEAR_16` (paired with `internal/testutil.AssertSentinelAbsent`).

**Organization**: Tasks are grouped by user story. Each story can be implemented and validated independently after Phase 2 (Foundational) completes. No new exported package-level symbols are added to `internal/cli`; the only new public surface is the cobra subcommand registration on the root tree (chunk contract).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Different files, no dependencies on incomplete tasks → can run in parallel
- **[Story]**: `[US1]`/`[US2]`/`[US3]` — user story phase tasks only
- All file paths are absolute or repo-root-relative

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Land empty file stubs for the new subcommand and confirm the gates are clean before any test or implementation work begins.

- [X] T001 Create [internal/cli/request.go](internal/cli/request.go) as an empty file containing only `package cli` and a 1–3 line top-of-file comment summarising the chunk's intent (no exported symbols, no helpers yet)
- [X] T002 Create [internal/cli/exec.go](internal/cli/exec.go) as an empty file containing only `package cli` and a 1–3 line top-of-file comment ("child-env construction + os/exec wrapper for hush request")
- [X] T003 Create [internal/cli/request_test.go](internal/cli/request_test.go) as an empty test file (`package cli`, `import "testing"`, no functions yet)
- [X] T004 Create [internal/cli/exec_test.go](internal/cli/exec_test.go) as an empty test file (`package cli`, `import "testing"`, no functions yet)
- [X] T005 Create [internal/cli/request_integration_test.go](internal/cli/request_integration_test.go) with a `//go:build integration` tag and `package cli` declaration (no functions yet)
- [X] T006 Run `magex format:fix` and `magex lint` from repo root to confirm the new empty files pass the gates clean before any test or impl work

**Checkpoint**: Five new files exist, all empty/stubbed, package compiles, gates clean.

---

## Phase 2: Foundational — sentinels, types, validator, subcommand registration (Blocking Prerequisites)

**Purpose**: All three user stories consume the same flag layer, sentinel set, mapErr wiring, and cobra subcommand registration. These MUST exist before any story-specific test can compile or assert exit codes.

**⚠️ CRITICAL**: No US1/US2/US3 task can begin until this phase is complete.

### Tests for Foundational (TDD — write FIRST, ensure FAIL before implementation)

- [X] T007 [P] Write `TestRequest_SubcommandRegisteredOnRoot` in [internal/cli/request_test.go](internal/cli/request_test.go) — build the cobra root via `newRoot()` (or equivalent test seam used by the existing serve/init tests), walk `root.Commands()`, assert one of them has `Use == "request"` (locks the chunk-contract registration)
- [X] T008 [P] Write `TestRequest_FlagSetMatchesContract` in [internal/cli/request_test.go](internal/cli/request_test.go) — locate the `request` command, walk `cmd.Flags().VisitAll`, assert the flag-name set is exactly `{server, scope, reason, ttl, max-uses, machine-index, exec, format}` (no extras, no missing) per [contracts/cli-request.md §2](specs/016-cli-request/contracts/cli-request.md)
- [X] T009 [P] Write `TestRequest_RequiredFlagsMarked` in [internal/cli/request_test.go](internal/cli/request_test.go) — assert each of `--server`, `--scope`, `--reason`, `--ttl`, `--max-uses`, `--machine-index` is marked required via cobra's `MarkFlagRequired` (cobra reports them in the `cmd.Flags().Lookup(name).Annotations[BashCompOneRequiredFlag]` slot or via the missing-flag error path)
- [X] T010 [P] Write `TestRequest_ParseAndValidateFlags_NeitherDeliveryMode` in [internal/cli/request_test.go](internal/cli/request_test.go) — call `parseAndValidateFlags` with all required flags set EXCEPT `--exec`/`--format`; assert returned error `errors.Is(err, errMissingExecOrFormat)`; assert `mapErr(err) == ExitInputErr` (2)
- [X] T011 [P] Write `TestRequest_ParseAndValidateFlags_BothDeliveryModes` in [internal/cli/request_test.go](internal/cli/request_test.go) — call validator with both `--exec=/bin/zsh` and `--format=eval`; assert `errors.Is(err, errExecAndFormatBothSet)`; assert `mapErr(err) == ExitInputErr`
- [X] T012 [P] Write `TestRequest_ParseAndValidateFlags_FormatNotEval` in [internal/cli/request_test.go](internal/cli/request_test.go) — call validator with `--format=json`; assert `errors.Is(err, errFormatNotEval)`; assert `mapErr(err) == ExitInputErr`
- [X] T013 [P] Write `TestRequest_ParseAndValidateFlags_MaxUsesTooLow` in [internal/cli/request_test.go](internal/cli/request_test.go) — call validator with `--scope=A,B,C` and `--max-uses=2`; assert `errors.Is(err, errMaxUsesTooLow)`; assert `mapErr(err) == ExitInputErr`
- [X] T014 [P] Write `TestRequest_ParseAndValidateFlags_HappyPathExec` in [internal/cli/request_test.go](internal/cli/request_test.go) — call validator with all required flags + `--exec=/bin/echo`; assert `err == nil`; assert returned `requestFlags.execProgram == "/bin/echo"`, `formatMode == ""`, `len(scope) == expected`
- [X] T015 [P] Write `TestRequest_ParseAndValidateFlags_HappyPathFormatEval` in [internal/cli/request_test.go](internal/cli/request_test.go) — call validator with `--format=eval`; assert `err == nil`; assert `formatMode == "eval"`, `execProgram == ""`
- [X] T016 [P] Write `TestRequest_ParseAndValidateFlags_ChildArgsAfterDoubleDash` in [internal/cli/request_test.go](internal/cli/request_test.go) — invoke the cobra command with argv `["request", "--exec=/bin/echo", "--", "a", "b", "c"]` plus the other required flags; assert validator captures `childArgs == ["a", "b", "c"]` (cobra's `cmd.ArgsLenAtDash()` boundary semantics, FR-008)
- [X] T017 [P] Write `TestRequest_MapErr_ChildExitCode` in [internal/cli/request_test.go](internal/cli/request_test.go) — wrap an `errChildExitCode{code: 7}` value in an error; assert `mapErr(err) == 7` (locks the new exit-code propagation path used by `--exec`)
- [X] T018 [P] Write `TestRequest_NoOsGetenvInRequestGo` in [internal/cli/request_test.go](internal/cli/request_test.go) — `os.ReadFile("request.go")` and `os.ReadFile("exec.go")`; assert neither contains the byte sequence `os.Getenv` (locks Constitution III §1, mirrors SDD-15's `TestInit_LintNoOsGetenv`)

### Implementation for Foundational

- [X] T019 Add five new sentinel errors `errMissingExecOrFormat`, `errExecAndFormatBothSet`, `errFormatNotEval`, `errMaxUsesTooLow`, `errChildExitCode` (the last is a struct wrapping an int code; implements `error`) to [internal/cli/exit_codes.go](internal/cli/exit_codes.go) with the exact stderr messages from [contracts/cli-request.md §4](specs/016-cli-request/contracts/cli-request.md)
- [X] T020 Wire the four input-validation sentinels through `mapErr` in [internal/cli/exit_codes.go](internal/cli/exit_codes.go) so each maps to `ExitInputErr` (2); wire `*errChildExitCode` so `mapErr` returns `errors.As`-extracted `code` int verbatim (preserves child's exit status per FR-010)
- [X] T021 Define unexported types in [internal/cli/request.go](internal/cli/request.go): `requestFlags` (per [data-model.md §1](specs/016-cli-request/data-model.md)), `claimWireRequest` (twelve JSON-tagged fields, [data-model.md §2](specs/016-cli-request/data-model.md)), `claimSignedPayload` (nine alphabetical fields, [data-model.md §2](specs/016-cli-request/data-model.md)), `claimWireResponse` (`jwt`, `expires_at`, `jti`), `claimWireError` (`error`, `request_id`)
- [X] T022 Define `requestDeps` struct in [internal/cli/request.go](internal/cli/request.go) with the seam fields from [data-model.md §5](specs/016-cli-request/data-model.md) (`keychain`, `httpClient`, `nowFn`, `randReader`, `hostnameFn`, `ephemeralKey`, `looker`, `runner`, `signalCtx`); add `productionRequestDeps()` returning the locked production wiring
- [X] T023 Implement `parseAndValidateFlags(cmd *cobra.Command, args []string) (requestFlags, error)` in [internal/cli/request.go](internal/cli/request.go): reads each flag value via `cmd.Flags().GetXxx`; CSV-splits `--scope`; enforces mutual exclusion (`errMissingExecOrFormat` xor `errExecAndFormatBothSet`); enforces `--format` literal value (`errFormatNotEval`); enforces `--max-uses >= len(scope)` (`errMaxUsesTooLow`); reads `cmd.ArgsLenAtDash()` to slice `args` into `childArgs`; emits **no** I/O on the failure path (FR-002, contract §9)
- [X] T024 Implement `newRequestCmd() *cobra.Command` skeleton in [internal/cli/request.go](internal/cli/request.go): `Use: "request"`, registers all eight flags via `StringVar`/`StringSliceVar`/`DurationVar`/`IntVar`/`Uint32Var`; calls `cmd.MarkFlagRequired` for each of the six required flags; `RunE` calls `parseAndValidateFlags` then dispatches to `runRequest(ctx, deps, flags)` (left as TODO body for now — implemented in US1/US2 phases)
- [X] T025 Register the subcommand on the cobra root: edit [internal/cli/root.go](internal/cli/root.go) to add `root.AddCommand(newRequestCmd())` alongside the existing entries (matches the ordering from `serve`/`health`/`version`/`revoke`/`init`)
- [X] T026 Run `go test ./internal/cli/... -run 'TestRequest_(SubcommandRegisteredOnRoot|FlagSetMatchesContract|RequiredFlagsMarked|ParseAndValidateFlags_|MapErr_ChildExitCode|NoOsGetenvInRequestGo)'` — all T007–T018 tests must PASS

**Checkpoint**: Subcommand registered on root; flag layer + validator + sentinel set in place; all three user stories can now begin in parallel.

---

## Phase 3: User Story 1 — Wrap a child program safely with `--exec` (Priority: P1) 🎯 MVP

**Goal**: Operator runs `hush request ... --exec <program>`, approves the Discord prompt, and the child program starts with the requested secrets in its environment. The parent's stdout/stderr/log contain zero secret bytes; the child's exit code becomes the parent's exit code; the ephemeral private key is zeroed before parent exit; no JWT or secret value reaches disk.

**Independent Test**: From [quickstart.md §1](specs/016-cli-request/quickstart.md) — drive `hush request --exec <env-printer>` against an in-process `*server.Server` (SDD-13) seeded with one secret, with `DiscordStub.ApproveAll` running. Assert the child's stdout contains the seeded secret value as `SCOPE_NAME=<value>`; assert parent's stdout/stderr/slog contain none of the secret bytes; assert `t.TempDir()` walk finds no file containing the JWT or any secret value; assert the child's exit code is propagated.

### Tests for User Story 1 (TDD — write FIRST, ensure FAIL before implementation) ⚠️

- [X] T027 [P] [US1] Write `TestRequest_ClientKeyFromKeychainNotEnv` in [internal/cli/request_test.go](internal/cli/request_test.go) — set `t.Setenv("HUSH_CLIENT_KEY", "SECRET_SHOULD_NEVER_APPEAR_16_envkey")`; populate a `keychain.FakeKeychain` with `(hush-client, machine-0)` holding a real secp256k1 32-byte scalar; drive a happy-path run with `--exec /bin/true --machine-index 0`; assert the signature on the `/claim` POST verifies under the keychain-stored key (NOT under any key derivable from the env var); assert the env-var sentinel does not appear anywhere in captured output (FR-004, SC-009)
- [X] T028 [P] [US1] Write `TestRequest_ClaimSessionTypeIsInteractive` in [internal/cli/request_test.go](internal/cli/request_test.go) — happy-path run against an `httptest.Server` capturing the `/claim` request body; decode the JSON; assert `session_type == "interactive"` (Constitution IV)
- [X] T029 [P] [US1] Write `TestRequest_ClaimWireShapeMatchesServer` in [internal/cli/request_test.go](internal/cli/request_test.go) — capture the POST body; decode into a generic `map[string]any`; assert the key set equals the twelve keys listed in [contracts/cli-request.md §5](specs/016-cli-request/contracts/cli-request.md): `client_key_fingerprint`, `ephemeral_pubkey`, `machine_name`, `nonce`, `reason`, `request_id`, `scope`, `session_type`, `signature`, `timestamp`, `ttl` (no extras, no missing)
- [X] T030 [P] [US1] Write `TestRequest_ClaimSignaturePayloadCanonical` in [internal/cli/request_test.go](internal/cli/request_test.go) — capture the wire body; reconstruct the nine-field signed-payload struct (alphabetical, excludes signature + fingerprint); recompute `sign.CanonicalJSON` over it; verify the signature with `sign.Verify` using the same keychain-stored client public key (proves canonicalisation matches the server's expectations)
- [X] T031 [P] [US1] Write `TestRequest_EphemeralPubKeyHexFormat` in [internal/cli/request_test.go](internal/cli/request_test.go) — capture wire body; assert `ephemeral_pubkey` matches regex `^[0-9a-f]{66}$` (66-char lowercase SEC1-compressed hex per research §6)
- [X] T032 [P] [US1] Write `TestRequest_NonceAndRequestIDFormat` in [internal/cli/request_test.go](internal/cli/request_test.go) — capture wire body; assert `nonce` matches `^[A-Za-z0-9_-]{43}$` and `request_id` matches `^[A-Za-z0-9_-]{32}$` (research §6, server's compiled regex set)
- [X] T033 [P] [US1] Write `TestRequest_ExecInjectsEnvVars` in [internal/cli/request_test.go](internal/cli/request_test.go) — drive `--exec` with a small Go program built into `t.TempDir()` (or `[internal/cli/testdata/echoenv/main.go](internal/cli/testdata/echoenv/main.go)` compiled at test time) that prints its `os.Environ()` to stdout; seed two scopes; capture child stdout via `cmd.Stdout = &buf`; assert each `SCOPE=value` appears verbatim in the child's output (AC-5, FR-007)
- [X] T034 [P] [US1] Write `TestRequest_ExecPropagatesChildExitCode` in [internal/cli/request_test.go](internal/cli/request_test.go) — drive `--exec /bin/sh -- -c 'exit 7'` (or a small test program that calls `os.Exit(7)`); assert `RunE` returns an error whose `errors.As(err, &*errChildExitCode{})` extracts `code == 7`; assert `mapErr(err) == 7` (FR-010)
- [X] T035 [P] [US1] Write `TestRequest_PostExecZeroesEphemeralKey` in [internal/cli/request_test.go](internal/cli/request_test.go) — wrap the `ephemeralKey` seam to record a pointer to the generated `*ecdsa.PrivateKey`; drive `--exec /bin/true` happy path; after `runRequest` returns, assert `priv.D.Sign() == 0` AND `bytes.Equal(priv.D.Bytes(), nil)` (zero magnitude, FR-009)
- [X] T036 [P] [US1] Write `TestRequest_NeverWritesJWTToDisk` in [internal/cli/request_test.go](internal/cli/request_test.go) — drive a happy-path run; capture the JWT string from the fake server's emitted response (the test owns both sides of the wire); after the run, walk `t.TempDir()` AND the user's home directory subset (`os.UserHomeDir()` if present, scoped to a test-controlled prefix) AND `os.TempDir()` for any file modified during the run; assert no such file contains the JWT byte sequence (FR-014, SC-004)
- [X] T037 [P] [US1] Write `TestRequest_NeverWritesSecretToDisk` in [internal/cli/request_test.go](internal/cli/request_test.go) — same as T036 but the body stuffed in `/s/<name>` is `SECRET_SHOULD_NEVER_APPEAR_16_payload`; walk for the sentinel; assert zero matches (FR-013, SC-001)
- [X] T038 [P] [US1] Write `TestRequest_PartialFetchFailureAbortsBeforeChild` in [internal/cli/request_test.go](internal/cli/request_test.go) — set up a fake `/s/<name>` that returns 200 for the first scope and 404 for the second; drive with `--exec /usr/bin/false` and `--scope A,B`; assert `runRequest` returns an error mapping to `ExitNotFound` (4); inject a `runner` seam that records whether `Cmd.Run` was invoked; assert it was NEVER called (FR-018, SC-010)
- [X] T039 [P] [US1] Write `TestRequest_DeniedOnDiscordExitsAuth` in [internal/cli/request_test.go](internal/cli/request_test.go) — fake server returns 403 with body `{"error":"denied","request_id":"abc"}` to `/claim`; assert `runRequest` returns an error such that `mapErr(err) == ExitAuth` (3); assert no decryption or `/s` call happened (FR-017)
- [X] T040 [P] [US1] Write `TestRequest_KeychainMissExitErr` in [internal/cli/request_test.go](internal/cli/request_test.go) — `keychain.FakeKeychain` empty; drive request with `--machine-index 0`; assert returned err is `errors.Is(err, keychain.ErrKeychainItemNotFound)`; assert stderr line matches the locked text from [contracts/cli-request.md §8](specs/016-cli-request/contracts/cli-request.md): `"hush: request: client key not found in keychain — run \`hush init client --machine-index <N>\` first"`
- [X] T041 [P] [US1] Write `TestRequest_TTLBoundsApprovalWait` in [internal/cli/request_test.go](internal/cli/request_test.go) — fake server's `/claim` handler blocks indefinitely; drive with `--ttl 100ms`; capture wall-clock; assert returned err satisfies `errors.Is(err, context.DeadlineExceeded)` (or wraps it); assert elapsed wall-clock ≥ 100ms and < 1s; assert mapped exit code `ExitErr` (1) (FR-005, contract §6)
- [X] T042 [P] [US1] Write `TestRequest_SIGINTDuringApprovalWaitZeroesKey` in [internal/cli/request_test.go](internal/cli/request_test.go) — inject a `signalCtx` seam that returns a context already cancelled (simulating SIGINT during the `/claim` POST); record the `*ecdsa.PrivateKey` pointer; drive request; assert returned err satisfies `errors.Is(err, context.Canceled)`; assert the recorded `priv.D.Sign() == 0` after `runRequest` returns (FR-021)
- [X] T043 [P] [US1] Write `TestRequest_LogsNeverContainSecretValue` in [internal/cli/request_test.go](internal/cli/request_test.go) — capture parent's slog handler buffer; drive happy-path run with secret value `SECRET_SHOULD_NEVER_APPEAR_16_log`; assert `internal/testutil.AssertSentinelAbsent(t, sentinel, slogBuf.Bytes())` passes (Constitution X, FR-016)
- [X] T044 [P] [US1] Write `TestRequest_ErrorsDoNotLeakSecretBytes` in [internal/cli/request_test.go](internal/cli/request_test.go) — for each documented failure mode (keychain miss, transport down, claim 403, claim 408, /s 404, /s 401), drive the path and assert the resulting `err.Error()` does NOT contain the seeded secret value or the JWT body — uses `AssertSentinelAbsent` (Constitution X)
- [X] T045 [P] [US1] Write `TestRequest_NoCallsToOSGetenvAtRuntime` in [internal/cli/request_test.go](internal/cli/request_test.go) — companion to T018; this runtime check sets `t.Setenv("HUSH_*", "...")` for the full set of sensitive-looking envs (`HUSH_PASSPHRASE`, `HUSH_CLIENT_KEY`, `HUSH_SERVER`, `HUSH_TTL`, `HUSH_MACHINE_INDEX`) and drives a happy-path run; assert no flag value was satisfied by an env var by capturing the wire body and confirming each field equals the supplied flag value, not the sentinel (FR-020 + Constitution III §1)
- [X] T046 [P] [US1] Write `TestRequest_ExecOnlyChildHasSecret` (sentinel-leak) in [internal/cli/request_test.go](internal/cli/request_test.go) — full in-process `*server.Server` (SDD-13) seeded with `SENTINEL_SCOPE = "SECRET_SHOULD_NEVER_APPEAR_16"`; build the `echoenv` test helper into `t.TempDir()` via `go build`; drive `hush request --exec <echoenv> --scope SENTINEL_SCOPE ...`; capture parent's stdout, parent's stderr, parent's slog buffer, child's stdout into separate `bytes.Buffer`s; assert (a) child's stdout contains `SENTINEL_SCOPE=SECRET_SHOULD_NEVER_APPEAR_16`, (b) `AssertSentinelAbsent(t, sentinel, parentStdout)` passes, (c) same for parentStderr, (d) same for slogBuf, (e) `filepath.Walk(t.TempDir())` reports zero files containing the sentinel (mandatory test from SDD-16; Principle I+X)

### Implementation for User Story 1

- [X] T047 [US1] Create [internal/cli/testdata/echoenv/main.go](internal/cli/testdata/echoenv/main.go) — a 10–20 line Go program that calls `os.Environ()` and writes each entry verbatim to `os.Stdout` with a trailing `\n`; used by T033 + T046 to verify child env injection without invoking a shell
- [X] T048 [US1] Implement `retrieveClientKey(ctx context.Context, kc keychain.Keychain, machineIndex uint32) (*ecdsa.PrivateKey, error)` in [internal/cli/request.go](internal/cli/request.go) — calls `kc.Retrieve(ctx, "hush-client", fmt.Sprintf("machine-%d", machineIndex))`; inside a single `Use(fn)` callback, calls `secp256k1.PrivKeyFromBytes(scalar).ToECDSA()`; `Destroy()` the SecureBytes inside the same defer; map `keychain.ErrKeychainItemNotFound` and `ErrKeychainPermissionDenied` to the locked stderr lines from contract §8
- [X] T049 [US1] Implement `generateEphemeralKey(rand io.Reader) (*ecdsa.PrivateKey, error)` in [internal/cli/request.go](internal/cli/request.go) — calls `secp256k1.GeneratePrivateKey()` (note: the function reads from `crypto/rand` internally, but the seam is preserved for tests via the `randReader` field on `requestDeps` calling a wrapper that delegates); converts via `.ToECDSA()`
- [X] T050 [US1] Implement `compressedEphemeralPubHex(priv *ecdsa.PrivateKey) string` in [internal/cli/request.go](internal/cli/request.go) — produces the SEC1-compressed 33-byte form, hex-lowercase-encoded to 66 chars (matches the helper used by `init.go::sec1Compress` per research §3 — locally re-implemented to avoid coupling)
- [X] T051 [US1] Implement `buildClaimPayload(flags requestFlags, ephemeralPubHex string, deps requestDeps) (claimSignedPayload, error)` in [internal/cli/request.go](internal/cli/request.go): generates `nonce` (32 random bytes → `base64.RawURLEncoding`, 43 chars), `request_id` (24 random bytes → `base64.RawURLEncoding`, 32 chars), `timestamp` (`deps.nowFn().UTC().Format(time.RFC3339Nano)`), `machine_name` (sanitized `deps.hostnameFn()`, ≤64 chars matching `[A-Za-z0-9._-]`); fills the nine alphabetical fields per [data-model.md §2](specs/016-cli-request/data-model.md)
- [X] T052 [US1] Implement `signAndWrapClaim(ctx context.Context, clientKey *ecdsa.PrivateKey, payload claimSignedPayload) (claimWireRequest, error)` in [internal/cli/request.go](internal/cli/request.go): runs `sign.CanonicalJSON(payload)` → bytes; runs `sign.Sign(ctx, clientKey, canonical)` → signature; computes `keys.PublicKeyFingerprint(&clientKey.PublicKey)` (16-char lowercase hex); base64-std encodes the signature; assembles the twelve-key `claimWireRequest` envelope per [contracts/cli-request.md §5](specs/016-cli-request/contracts/cli-request.md)
- [X] T053 [US1] Implement `postClaim(ctx context.Context, deps requestDeps, server string, body claimWireRequest) (claimWireResponse, error)` in [internal/cli/request.go](internal/cli/request.go): JSON-encodes body; POSTs to `<server>/claim` via `deps.httpClient`; `Content-Type: application/json`; reads response; on 200 unmarshals into `claimWireResponse`; on 4xx/5xx unmarshals into `claimWireError` and maps the `error` field via the table in [contracts/cli-request.md §6](specs/016-cli-request/contracts/cli-request.md) (`bad_request`/`bad_signature`/`denied`/`approval_timeout`/`rate_limited`/`discord_unavailable` → mapped sentinels for mapErr); on transport error reuses `classifyTransportErr` from `health.go`/`revoke.go`
- [X] T054 [US1] Implement `fetchSecrets(ctx context.Context, deps requestDeps, server string, jwt *securebytes.SecureBytes, ephPriv *ecdsa.PrivateKey, scope []string) ([]*securebytes.SecureBytes, error)` in [internal/cli/request.go](internal/cli/request.go): for each `name` in `scope`, calls `GET <server>/s/<name>` with `Authorization: Bearer <jwt>` (jwt header set inside a `Use(fn)` callback so the bytes don't escape into a long-lived `string`); octet-stream body fed to `ecies.Decrypt(ctx, ephPriv, body)` → `*SecureBytes`; on any error mid-loop, return immediately with the partial slice already destroyed (deferred `Destroy()` loop) AND error mapped per contract §6 (401 → ExitAuth, 403 → ExitAuth, 404 → ExitNotFound)
- [X] T055 [US1] Implement `buildChildEnv(scope []string, secrets []*securebytes.SecureBytes, parentEnv []string) ([]string, error)` in [internal/cli/exec.go](internal/cli/exec.go): start from `parentEnv` (filtered to remove any pre-existing entry whose name matches a scope name); for each `i, name := range scope`, call `secrets[i].Use(func(b []byte) { entry := name + "=" + string(b); env = append(env, entry) })`; return env (the `string` allocations are a documented residual risk per SECURITY.md §6); FR-007: scope-name entries override the parent's
- [X] T056 [US1] Implement `runChild(ctx context.Context, deps requestDeps, program string, childArgs []string, env []string) error` in [internal/cli/exec.go](internal/cli/exec.go): resolve `program` via `deps.looker`; construct `exec.CommandContext(ctx, resolvedPath, childArgs...)`; assign `cmd.Path = resolvedPath` defensively; `cmd.Env = env`; `cmd.Stdin = os.Stdin`; `cmd.Stdout = os.Stdout`; `cmd.Stderr = os.Stderr`; call `deps.runner(cmd)`; on `*exec.ExitError`, wrap and return `&errChildExitCode{code: exitErr.ExitCode()}`; on other errors return classified
- [X] T057 [US1] Implement `runRequest(ctx context.Context, deps requestDeps, flags requestFlags, stdout, stderr io.Writer) error` in [internal/cli/request.go](internal/cli/request.go) — the orchestration described in [plan.md §7 + data-model.md §7](specs/016-cli-request/data-model.md): retrieve client key → generate ephemeral → build+sign → `signal.NotifyContext` → `context.WithDeadline(now+ttl)` → POST claim → fetch secrets → switch on `flags.modeOf()`: `"exec"` → `buildChildEnv` + `runChild` (this story); `"eval"` → handled in US2; defer chain zeroes ephemeral D, destroys JWT SB, destroys each secret SB, calls cancel
- [X] T058 [US1] Wire `runRequest` from `newRequestCmd().RunE` in [internal/cli/request.go](internal/cli/request.go) (replace the TODO body from T024); construct `productionRequestDeps()`; pass `cmd.Context()`, `cmd.OutOrStdout()`, `cmd.ErrOrStderr()`
- [X] T059 [US1] Run `go test ./internal/cli/... -run 'TestRequest_(ClientKeyFromKeychainNotEnv|ClaimSessionTypeIsInteractive|ClaimWireShapeMatchesServer|ClaimSignaturePayloadCanonical|EphemeralPubKeyHexFormat|NonceAndRequestIDFormat|ExecInjectsEnvVars|ExecPropagatesChildExitCode|PostExecZeroesEphemeralKey|NeverWritesJWTToDisk|NeverWritesSecretToDisk|PartialFetchFailureAbortsBeforeChild|DeniedOnDiscordExitsAuth|KeychainMissExitErr|TTLBoundsApprovalWait|SIGINTDuringApprovalWaitZeroesKey|LogsNeverContainSecretValue|ErrorsDoNotLeakSecretBytes|NoCallsToOSGetenvAtRuntime|ExecOnlyChildHasSecret)'` — all T027–T046 must PASS

**Checkpoint**: `hush request --exec <program>` is fully functional — operator can wrap a child shell with secrets in env; AC-5 entry point reached; sentinel-leak test green.

---

## Phase 4: User Story 2 — Emit shell-evalable exports with `--format eval` (Priority: P2)

**Goal**: Operator runs `hush request ... --format eval`, approves, and `hush request` writes one `export NAME='value'` line per scope to stdout AND emits the locked stderr WARNING explaining the shell-history risk. Single quotes in secret values are correctly escaped so `eval` of the output recovers the original bytes.

**Independent Test**: From [quickstart.md §2](specs/016-cli-request/quickstart.md) — drive `hush request --format eval` against an in-process `*server.Server` seeded with two secrets (one of which contains a literal `'` byte); capture stdout into a tmpfile, capture stderr into a buffer; `bash -c "set -u; eval \"$(cat $tmpfile)\"; echo \"$NAME1\"; echo \"$NAME2\""` recovers both secrets verbatim; stderr buffer's first line equals the locked WARNING string.

### Tests for User Story 2 (TDD — write FIRST, ensure FAIL before implementation) ⚠️

- [X] T060 [P] [US2] Write `TestRequest_FormatEvalEmitsStderrWarning` in [internal/cli/request_test.go](internal/cli/request_test.go) — drive `--format eval` happy-path against a fake server seeded with one secret; capture stderr into `bytes.Buffer`; assert the buffer's `String()` contains the byte-equal locked WARNING from [docs/SECURITY.md §6](docs/SECURITY.md) and [contracts/cli-request.md §3](specs/016-cli-request/contracts/cli-request.md): `"WARNING: --format eval prints secret values to stdout. They may be captured by terminal scrollback, tmux, or script. Use --exec whenever possible.\n"` (FR-012, AC-6, SC-007)
- [X] T061 [P] [US2] Write `TestRequest_FormatEvalEscapesSingleQuote` in [internal/cli/request_test.go](internal/cli/request_test.go) — seed a secret with value `pa'ss"wo$rd\nwith all the things` (contains `'`, `"`, `$`, `\n`, `\\`); drive `--format eval`; capture stdout; assert exactly one line of the form `export NAME='pa'\''ss"wo$rd<actual newline>with all the things'\n`; further: pipe stdout through `bash -c "$(cat -)"` and `printenv NAME` and assert byte-equal recovery of the original value (FR-011, SC-006)
- [X] T062 [P] [US2] Write `TestRequest_FormatEvalOneLinePerScope` in [internal/cli/request_test.go](internal/cli/request_test.go) — seed three scopes; drive `--format eval`; capture stdout; assert exactly three `\n`-terminated lines; assert each line is prefixed with `export ` followed by the scope name (in `--scope` order, not server-ordered)
- [X] T063 [P] [US2] Write `TestRequest_FormatEvalWarningGoesToStderrNotStdout` in [internal/cli/request_test.go](internal/cli/request_test.go) — drive `--format eval`; capture stdout and stderr into separate buffers; assert the WARNING string is **absent** from stdout (FR-012)
- [X] T064 [P] [US2] Write `TestRequest_FormatEvalNoChildProcessSpawned` in [internal/cli/request_test.go](internal/cli/request_test.go) — inject a `runner` seam that records calls; drive `--format eval`; assert `runner` was never called (FR-019 — `--exec` and `--format` are mutually exclusive at the dispatch layer)
- [X] T065 [P] [US2] Write `TestRequest_FormatEvalWarningEvenWhenStdoutPiped` in [internal/cli/request_test.go](internal/cli/request_test.go) — drive `--format eval` with the stdout writer set to a `*bytes.Buffer` (simulating a pipe to `eval`); capture stderr separately; assert the WARNING is still present on stderr regardless of stdout's destination (FR-012, SC-007)
- [X] T066 [P] [US2] Write `TestRequest_FormatEvalDoesNotLeakSecretToParentSlog` in [internal/cli/request_test.go](internal/cli/request_test.go) — drive `--format eval` with secret `SECRET_SHOULD_NEVER_APPEAR_16_eval`; capture parent's slog buffer; assert `AssertSentinelAbsent` over slog buffer (parent's logger MUST NOT log the secret even though stdout legitimately receives it) — Constitution X
- [X] T067 [P] [US2] Write `TestRequest_FormatEvalEmptyValuePreservesQuotes` in [internal/cli/request_test.go](internal/cli/request_test.go) — seed a secret with empty bytes `[]byte("")`; drive `--format eval`; assert output line is exactly `export NAME=''\n` (no degenerate empty-string handling)
- [X] T068 [P] [US2] Write `TestRequest_FormatEvalPostExecZeroesEphemeralKey` in [internal/cli/request_test.go](internal/cli/request_test.go) — companion to T035 for the eval path; record `*ecdsa.PrivateKey` pointer; drive `--format eval`; after return assert `priv.D.Sign() == 0` (FR-009 applies to both modes)

### Implementation for User Story 2

- [X] T069 [US2] Implement `escapeShellSingleQuote(raw []byte) string` in [internal/cli/exec.go](internal/cli/exec.go) — replaces every `'` byte with the four-byte sequence `'\''`; returns the result as a Go `string`; documented residual risk per SECURITY.md §6 (the eval contract requires plaintext to cross to a `string` exactly once)
- [X] T070 [US2] Implement `renderEvalLine(name string, raw []byte) string` in [internal/cli/exec.go](internal/cli/exec.go) — returns `"export " + name + "='" + escapeShellSingleQuote(raw) + "'\n"` (FR-011)
- [X] T071 [US2] Define the locked WARNING string as a package-private constant in [internal/cli/exec.go](internal/cli/exec.go): `const formatEvalWarning = "WARNING: --format eval prints secret values to stdout. They may be captured by terminal scrollback, tmux, or script. Use --exec whenever possible.\n"` (byte-equal asserted by T060)
- [X] T072 [US2] Implement `writeEvalExports(stdout, stderr io.Writer, scope []string, secrets []*securebytes.SecureBytes) error` in [internal/cli/exec.go](internal/cli/exec.go): for each `i, name := range scope`, call `secrets[i].Use(func(b []byte) { line := renderEvalLine(name, b); _, _ = io.WriteString(stdout, line) })`; after the loop, write `formatEvalWarning` to stderr; return any first I/O error encountered (FR-012)
- [X] T073 [US2] Wire the eval-mode dispatch into `runRequest` in [internal/cli/request.go](internal/cli/request.go) — extend the switch from T057: `case "eval": return writeEvalExports(stdout, stderr, flags.scope, secrets)`
- [X] T074 [US2] Run `go test ./internal/cli/... -run 'TestRequest_FormatEval'` — all T060–T068 must PASS

**Checkpoint**: `hush request --format eval` is fully functional — operator can load secrets into the current shell with the documented risk acknowledgement; AC-6 entry point reached; locked WARNING wording verified byte-equal.

---

## Phase 5: User Story 3 — Refuse to run when neither delivery mode is chosen (Priority: P3)

**Goal**: A `hush request --scope X` invocation with no `--exec` and no `--format eval` exits non-zero with the locked stderr message and produces zero network traffic. Mutual exclusion is enforced at the input-validation layer before any keychain access, network call, or Discord prompt.

**Independent Test**: Run `hush request --scope X --reason r --ttl 1h --max-uses 1 --machine-index 0 --server https://example` with no delivery flag; assert exit code 2; assert stderr matches the locked literal text from [contracts/cli-request.md §4](specs/016-cli-request/contracts/cli-request.md); confirm no `/claim` POST was made (httptest.Server records zero requests).

### Tests for User Story 3 (TDD — write FIRST, ensure FAIL before implementation) ⚠️

- [X] T075 [P] [US3] Write `TestRequest_RequiresExecOrFormat_NoNetwork` in [internal/cli/request_test.go](internal/cli/request_test.go) — set up an `httptest.Server` with a counting handler; drive `runRequest` with all required flags except no `--exec`/`--format`; assert the returned err satisfies `errors.Is(err, errMissingExecOrFormat)`; assert handler call-count is 0; assert stderr line equals byte-for-byte `"hush: request: must specify --exec or --format eval\n"` (FR-002, SC-005)
- [X] T076 [P] [US3] Write `TestRequest_ExecOrFormatMutuallyExclusive_NoNetwork` in [internal/cli/request_test.go](internal/cli/request_test.go) — drive with both `--exec /bin/true` and `--format eval`; assert err matches `errExecAndFormatBothSet`; assert handler call-count is 0; assert stderr line equals `"hush: request: --exec and --format eval are mutually exclusive\n"` (FR-002)
- [X] T077 [P] [US3] Write `TestRequest_FormatRejectsNonEval_NoNetwork` in [internal/cli/request_test.go](internal/cli/request_test.go) — drive with `--format json`; assert err matches `errFormatNotEval`; assert handler call-count is 0; assert stderr line equals `"hush: request: --format only accepts the literal value \"eval\"\n"` (FR-003)
- [X] T078 [P] [US3] Write `TestRequest_MaxUsesTooLow_NoNetwork` in [internal/cli/request_test.go](internal/cli/request_test.go) — drive with `--scope A,B,C --max-uses 2 --exec /bin/true`; assert err matches `errMaxUsesTooLow`; assert handler call-count is 0; assert stderr line equals `"hush: request: --max-uses must be ≥ number of scopes\n"` (FR-001, AC-?)
- [X] T079 [P] [US3] Write `TestRequest_RefusesBeforeKeychainAccess` in [internal/cli/request_test.go](internal/cli/request_test.go) — inject a `keychain.FakeKeychain` whose `Retrieve` records every call; drive with no `--exec`/`--format`; assert returned err matches `errMissingExecOrFormat`; assert FakeKeychain's `Retrieve` was NEVER invoked (validation must run before any keychain I/O — FR-002)

### Implementation for User Story 3

- [X] T080 [US3] Audit `parseAndValidateFlags` (T023) and confirm mutual-exclusion + format-literal + max-uses checks happen BEFORE any I/O call. Add a top-of-function comment in [internal/cli/request.go](internal/cli/request.go) documenting the contract: "Pure-function validator; no I/O permitted on any path. Tests T075–T079 lock this property." If T075–T079 fail, the bug is that an I/O call is reachable on a validation-failure path; fix by hoisting the validation calls earlier.
- [X] T081 [US3] Confirm `runRequest` (T057) calls `parseAndValidateFlags` BEFORE constructing the HTTP context, BEFORE keychain access, BEFORE any signal-context wiring; the validator's error must short-circuit the function with no resource setup.
- [X] T082 [US3] Run `go test ./internal/cli/... -run 'TestRequest_(RequiresExecOrFormat_NoNetwork|ExecOrFormatMutuallyExclusive_NoNetwork|FormatRejectsNonEval_NoNetwork|MaxUsesTooLow_NoNetwork|RefusesBeforeKeychainAccess)'` — all T075–T079 must PASS

**Checkpoint**: Mutual-exclusion + `--format` literal + `--max-uses` validators refuse the run before any I/O; SC-005 satisfied; no Discord prompt is ever sent for a request that cannot succeed.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Integration test driving the full DiscordStub.ApproveAll happy path, doc updates, final gates per [SDD-16.md Prompt 5](docs/sdd/SDD-16.md), and the combined commit.

- [X] T083 [P] Write `TestRequest_FullFlowWithDiscordStubApproveAll` in [internal/cli/request_integration_test.go](internal/cli/request_integration_test.go) (build tag `integration`) — stand up an in-process `*server.Server` (SDD-13) seeded with two scopes (`SENTINEL_SCOPE` and `OTHER_SCOPE`); register a client key in the server's registry; drive `internal/testutil.DiscordStub.ApproveAll`; build the `echoenv` helper into `t.TempDir()`; drive `hush request --exec <echoenv> ...` end-to-end; assert child stdout contains both `SENTINEL_SCOPE=` and `OTHER_SCOPE=` env entries with the seeded values; assert parent's stdout/stderr/slog contain no secret bytes; assert the JTI emitted by the server appears in NO file under `t.TempDir()`; assert the run completes in < 5s (drives FR-005, FR-007, FR-009, FR-013, FR-014, FR-018, AC-5)
- [X] T084 [P] Write `TestRequest_FullFlowFormatEvalIntegration` in [internal/cli/request_integration_test.go](internal/cli/request_integration_test.go) (build tag `integration`) — same in-process server + DiscordStub.ApproveAll; drive `hush request --format eval`; capture stdout + stderr; pipe stdout into a `bash -c 'eval ...'` helper that recovers each scope; assert byte-equal recovery of every secret value; assert stderr contains the locked WARNING (drives FR-011, FR-012, AC-6)
- [X] T085 [P] Append an "Exported API — locked at SDD-16" section to [docs/PACKAGE-MAP.md](docs/PACKAGE-MAP.md) under the existing `internal/cli` entry: note that `request` subcommand is now mounted on the cobra root via `root.AddCommand(newRequestCmd())` in `Execute`; explicitly note **no new package-level exported symbols** were added to `internal/cli` (the cobra command tree IS the contract for this chunk)
- [X] T086 [P] Update [docs/AC-MATRIX.md](docs/AC-MATRIX.md) AC-5 row with `internal/cli/request_test.go::TestRequest_ExecInjectsEnvVars`, `internal/cli/request_test.go::TestRequest_ExecOnlyChildHasSecret`, and `internal/cli/request_integration_test.go::TestRequest_FullFlowWithDiscordStubApproveAll`
- [X] T087 [P] Update [docs/AC-MATRIX.md](docs/AC-MATRIX.md) AC-6 row with `internal/cli/request_test.go::TestRequest_FormatEvalEmitsStderrWarning`, `internal/cli/request_test.go::TestRequest_FormatEvalEscapesSingleQuote`, and `internal/cli/request_integration_test.go::TestRequest_FullFlowFormatEvalIntegration`
- [X] T088 [P] Mark SDD-16 status `done` in [docs/SDD-PLAYBOOK.md](docs/SDD-PLAYBOOK.md)
- [X] T089 [P] Update [docs/SDD-CATALOG.md](docs/SDD-CATALOG.md) SDD-16 row: status `done`, link to [internal/cli/request.go](internal/cli/request.go) and [internal/cli/exec.go](internal/cli/exec.go)
- [X] T090 Coverage check: `go test -cover ./internal/cli/ -run Request` must report ≥ 90% on the request portion of `internal/cli`; if below, identify uncovered lines via `go test -coverprofile=/tmp/cov.out ./internal/cli/ -run Request && go tool cover -func=/tmp/cov.out` and add focused tests
- [X] T091 Final gate: `magex format:fix` from repo root — must complete clean (auto-formats, must produce no diff)
- [X] T092 Final gate: `magex lint` from repo root — must complete clean (zero new lints; in particular, the `forbidigo` rule against `os.Getenv` MUST cover the new files)
- [X] T093 Final gate: `magex test:race` from repo root — full unit suite, race-clean (covers T026, T059, T074, T082)
- [X] T094 Final gate: `magex test:race -tags=integration` from repo root — integration suite, race-clean (drives T083, T084)
- [X] T095 Sentinel-leak smoke: re-run `go test ./internal/cli/... -run TestRequest_ExecOnlyChildHasSecret -v` and visually confirm `SECRET_SHOULD_NEVER_APPEAR_16` appears in the child's captured output AND is absent from the parent's; this is the constitutional canary for SDD-16
- [X] T096 WARNING-text smoke: re-run `go test ./internal/cli/... -run TestRequest_FormatEvalEmitsStderrWarning -v` and confirm the assertion is byte-equal against the literal `"WARNING: --format eval prints secret values to stdout. They may be captured by terminal scrollback, tmux, or script. Use --exec whenever possible.\n"`
- [X] T097 Combined commit per [SDD-16.md Prompt 5](docs/sdd/SDD-16.md): `git add internal/cli/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md docs/SDD-CATALOG.md specs/016-cli-request/tasks.md && git commit -m "feat(cli): hush request (interactive; ECIES; --exec | --format eval) (SDD-16)"`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Foundational)**: depends on Phase 1; **BLOCKS** all user stories — sentinel set, `requestFlags`/`requestDeps`, `parseAndValidateFlags`, and the cobra subcommand registration must compile before any US1/US2/US3 test can compile
- **Phase 3 (US1 — `--exec`)**: depends on Phase 2; **AC-5 entry point** → MVP candidate
- **Phase 4 (US2 — `--format eval`)**: depends on Phase 2; **AC-6 entry point**; can run in parallel with US1 if staffed (different functions in the same `request.go` / `exec.go` files → `[P]` only inside the test phase, not the implementation phase since both touch the same file)
- **Phase 5 (US3 — refuse-to-run)**: depends on Phase 2 only; the validation function exists already (T023); US3's tests assert no-network properties of the existing validator
- **Phase 6 (Polish)**: depends on Phases 3–5 complete

### User Story Dependencies

- **US1 + US2** are functionally independent (different mode dispatch branches) but share `runRequest` (T057) and `request.go` / `exec.go` files → tests `[P]`, implementation tasks sequential within each file
- **US3** is an audit/invariant story for the validation layer landed in Phase 2 (T023); its tests can technically run as soon as Phase 2 completes — listed third only to honour the priority ordering

### Within Each Story

- **TDD discipline**: every test task (T007–T018, T027–T046, T060–T068, T075–T079) MUST be written and observed FAIL before its corresponding implementation task is started. Implementation tasks are explicitly listed AFTER the tests within each phase.
- Sentinels and error wiring (T019, T020) come before the orchestration functions (T057) that return them.
- Helpers (T048–T056, T069–T072) come before the orchestration that calls them (T057, T073).

### Parallel Opportunities

- All [P]-marked tests within a phase touch independent test functions in the same `*_test.go` file → can be authored by separate sessions concurrently
- Foundational tests T007–T018 are all `[P]` in the same `request_test.go` — different `func Test...` blocks
- US1 tests T027–T046 are all `[P]` (twenty independent test functions)
- US2 tests T060–T068 are all `[P]` (nine independent test functions)
- US3 tests T075–T079 are all `[P]`
- Phase 6 doc-update tasks T085–T089 are `[P]` (different docs)
- Integration tests T083 + T084 are `[P]` (different test functions in the same `_integration_test.go` file)

---

## Parallel Example: User Story 1 Tests

```text
# Author all US1 test functions in parallel (single file, independent funcs):
Task T027 [P] [US1]: TestRequest_ClientKeyFromKeychainNotEnv
Task T028 [P] [US1]: TestRequest_ClaimSessionTypeIsInteractive
Task T029 [P] [US1]: TestRequest_ClaimWireShapeMatchesServer
Task T030 [P] [US1]: TestRequest_ClaimSignaturePayloadCanonical
Task T031 [P] [US1]: TestRequest_EphemeralPubKeyHexFormat
Task T032 [P] [US1]: TestRequest_NonceAndRequestIDFormat
Task T033 [P] [US1]: TestRequest_ExecInjectsEnvVars
Task T034 [P] [US1]: TestRequest_ExecPropagatesChildExitCode
Task T035 [P] [US1]: TestRequest_PostExecZeroesEphemeralKey
Task T036 [P] [US1]: TestRequest_NeverWritesJWTToDisk
Task T037 [P] [US1]: TestRequest_NeverWritesSecretToDisk
Task T038 [P] [US1]: TestRequest_PartialFetchFailureAbortsBeforeChild
Task T039 [P] [US1]: TestRequest_DeniedOnDiscordExitsAuth
Task T040 [P] [US1]: TestRequest_KeychainMissExitErr
Task T041 [P] [US1]: TestRequest_TTLBoundsApprovalWait
Task T042 [P] [US1]: TestRequest_SIGINTDuringApprovalWaitZeroesKey
Task T043 [P] [US1]: TestRequest_LogsNeverContainSecretValue
Task T044 [P] [US1]: TestRequest_ErrorsDoNotLeakSecretBytes
Task T045 [P] [US1]: TestRequest_NoCallsToOSGetenvAtRuntime
Task T046 [P] [US1]: TestRequest_ExecOnlyChildHasSecret
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Phase 1: Setup (T001–T006)
2. Phase 2: Foundational (T007–T026) — sentinels, types, validator, subcommand registration
3. Phase 3: User Story 1 (T027–T059) — full `hush request --exec` path
4. **STOP and VALIDATE**: drive `hush request --exec /bin/zsh ...` end-to-end against an in-process SDD-13 server with `DiscordStub.ApproveAll`; confirm child receives env vars and parent leaks no secret — proves AC-5
5. **Demo-ready** at this checkpoint — operator can wrap a shell with secrets even without the eval-mode shortcut

### Incremental Delivery

1. MVP: Phase 1 + Phase 2 + Phase 3 (US1) → AC-5 reached
2. Add Phase 4 (US2) → AC-6 reached → operator can load secrets into existing shell
3. Add Phase 5 (US3) → mutual-exclusion guard validated → security-review-ready
4. Phase 6 → integration tests, doc updates, gates, commit → SDD-16 done

### Parallel Team Strategy

After Phase 2 completes:
- Developer A: US1 (`--exec` mode) — large story, 20 tests + 12 impl tasks
- Developer B: US2 (`--format eval` mode) — 9 tests + 5 impl tasks
- Developer C: US3 (refuse-to-run) — 5 tests + 3 audit tasks; can land in a single sitting since the validator already exists from Phase 2

US1 + US2 share `runRequest` (T057, T073) — coordinate the merge so the eval branch lands cleanly into the same switch statement.

---

## Notes

- **TDD-mandatory**: every test task is listed BEFORE its implementation task within each phase. Verify tests FAIL before writing the implementation that makes them pass (Constitution VIII).
- **Sentinel discipline**: every secret-handling test uses `internal/testutil.SentinelSecret(16)` (`SECRET_SHOULD_NEVER_APPEAR_16`) and `AssertSentinelAbsent` over captured output streams.
- **Locked literal-text contracts**: every stderr message in [contracts/cli-request.md §3 / §4 / §8](specs/016-cli-request/contracts/cli-request.md) is byte-equal asserted by tests — changes require an SDD amendment.
- **No `os.Getenv` in request.go or exec.go**: enforced by T018 lint test + golangci-lint forbidigo rule.
- **No JWT or secret on disk**: enforced by T036 + T037 + T046 (the sentinel-leak canary).
- **WARNING wording is byte-locked**: the literal in [contracts/cli-request.md §3](specs/016-cli-request/contracts/cli-request.md) and [docs/SECURITY.md §6](docs/SECURITY.md) is asserted byte-equal by T060.
- **Coverage**: 90% on the request portion of `internal/cli`; sentinel-leak + key-zeroing paths reach 100%.
- **Integration tests** are gated by `//go:build integration` and exercised by `magex test:race -tags=integration` (T094).
- Commits are deferred to the single combined commit T097 at the end of Phase 6 per [SDD-16.md Prompt 5](docs/sdd/SDD-16.md).
