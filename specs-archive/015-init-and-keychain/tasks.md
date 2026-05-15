# Tasks: hush init — server + client bootstrap with OS-keychain ACL

**Input**: Design documents from [`/specs/015-init-and-keychain/`](./)
**Prerequisites**: [plan.md](./plan.md), [spec.md](./spec.md), [research.md](./research.md), [data-model.md](./data-model.md), [contracts/cli-init.md](./contracts/cli-init.md), [contracts/keychain-api.md](./contracts/keychain-api.md), [quickstart.md](./quickstart.md)

**Tests**: TDD-mandatory per Constitution VIII. Every behaviour contract has a test-writing task **before** the corresponding implementation task. Tests MUST FAIL when first written; implementation tasks make them pass.

**Coverage target**: 85% on both `internal/cli` (init portion) and `internal/keychain`. Sentinel-leak + passphrase-resolution paths reach 100%.

**Organization**: Tasks are grouped by user story. Each story can be implemented and validated independently after Phase 2 (Foundational) completes.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Different files, no dependencies on incomplete tasks → can run in parallel
- **[Story]**: `[US1]`/`[US2]`/`[US3]`/`[US4]` — user story phase tasks only
- All file paths are absolute or repo-root-relative

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Land the new `internal/keychain` package skeleton and the new direct dependency before any test or implementation work begins.

- [x] T001 Create new package directory [internal/keychain/](internal/keychain/) and write [internal/keychain/doc.go](internal/keychain/doc.go) with the package overview from [contracts/keychain-api.md](specs/015-init-and-keychain/contracts/keychain-api.md) (3–5 line package doc — no exported symbols yet)
- [x] T002 Add `github.com/zalando/go-keyring` as a direct dependency: `go get github.com/zalando/go-keyring@latest` then `go mod tidy`; verify [go.mod](go.mod) lists it under `require` (not `// indirect`) and [go.sum](go.sum) is updated; verify the only NEW transitive dep is `github.com/godbus/godbus/v5` (research §1)
- [x] T003 Run `magex format:fix` and `magex lint` from repo root to confirm the empty package and updated go.mod pass the gates clean before any test or impl work

**Checkpoint**: `internal/keychain` directory exists, package compiles empty, new dep is wired, gates clean.

---

## Phase 2: Foundational — Keychain interface, sentinels, FakeKeychain (Blocking Prerequisites)

**Purpose**: All four user stories consume `internal/keychain`. The interface, sentinel errors, capability probe, and `FakeKeychain` test seam MUST exist before any story-specific test can compile.

**⚠️ CRITICAL**: No US1/US2/US3/US4 task can begin until this phase is complete.

### Tests for Foundational (TDD — write FIRST, ensure FAIL before implementation)

- [x] T004 [P] Write `TestKeychain_StoreRetrieveRoundTrip` in [internal/keychain/keychain_test.go](internal/keychain/keychain_test.go) — uses `keychain.NewFake()`, stores a `*securebytes.SecureBytes` under `(service="svc", account="acct")` with `acl="/abs/hush"`, retrieves it, asserts byte-equal payload via `Use(fn)`, asserts `RecordedACL("svc", "acct") == "/abs/hush"`
- [x] T005 [P] Write `TestKeychain_DeleteRemoves` in [internal/keychain/keychain_test.go](internal/keychain/keychain_test.go) — store, delete, retrieve must return `errors.Is(err, keychain.ErrKeychainItemNotFound)`; second delete must also return `ErrKeychainItemNotFound` (non-idempotent contract per [contracts/keychain-api.md §1](specs/015-init-and-keychain/contracts/keychain-api.md))
- [x] T006 [P] Write `TestKeychain_StoreRefusesDuplicate` in [internal/keychain/keychain_test.go](internal/keychain/keychain_test.go) — store under `(s, a)`, second store under same `(s, a)` returns `errors.Is(err, keychain.ErrKeychainItemExists)`
- [x] T007 [P] Write `TestKeychain_FakeDestroyZeroes` in [internal/keychain/keychain_test.go](internal/keychain/keychain_test.go) — populate fake, call `Destroy()`, assert all stored `*securebytes.SecureBytes` were destroyed (via a probe value or post-destroy `Retrieve` returning `ErrKeychainItemNotFound`)
- [x] T008 [P] Write `TestKeychain_NewReturnsInterface` in [internal/keychain/keychain_test.go](internal/keychain/keychain_test.go) — calls `keychain.New(slog.Default())`, asserts non-nil and that the returned value satisfies `keychain.Keychain` (compile-time + runtime)

### Implementation for Foundational

- [x] T009 Define the `Keychain` interface (`Store`/`Retrieve`/`Delete` with the exact signatures from [contracts/keychain-api.md §1](specs/015-init-and-keychain/contracts/keychain-api.md)) in [internal/keychain/keychain.go](internal/keychain/keychain.go)
- [x] T010 Define the four sentinel errors `ErrKeychainItemNotFound`/`ErrKeychainItemExists`/`ErrKeychainPermissionDenied`/`ErrKeychainUnsupportedPlatform` in [internal/keychain/keychain.go](internal/keychain/keychain.go) with the exact messages from [contracts/keychain-api.md §4](specs/015-init-and-keychain/contracts/keychain-api.md)
- [x] T011 Implement `New(logger *slog.Logger) (Keychain, error)` factory in [internal/keychain/keychain.go](internal/keychain/keychain.go) that returns the platform-native impl; the actual platform constructors (`newDarwinKeychain` / `newLinuxKeychain`) are stubbed here and filled in by US4 platform tasks
- [x] T012 Implement `PerBinaryACLSupported() bool` in [internal/keychain/keychain.go](internal/keychain/keychain.go) — body is a single switch on `runtime.GOOS` returning `true` for `"darwin"`, `false` otherwise
- [x] T013 Implement `FakeKeychain` (struct + `NewFake()` + `Destroy()` + `RecordedACL(service, account)`) in [internal/keychain/keychain.go](internal/keychain/keychain.go) backed by `map[string]storedItem`; `Store` records the `acl` string for later assertion; `Retrieve` returns a fresh `*securebytes.SecureBytes` per call so the caller owns it; `Delete` is non-idempotent per the contract; `Destroy()` walks the map and zeroes every stored `*securebytes.SecureBytes`
- [x] T014 Run `go test ./internal/keychain/...` — T004–T008 must all PASS; coverage on the interface + fake at this point should already be ≥ 85% on the symbols added

**Checkpoint**: `internal/keychain` interface, sentinels, fake, and `New()` skeleton are in place. All four user stories can now begin in parallel.

---

## Phase 3: User Story 1 — Operator bootstraps the vault host (Priority: P1) 🎯 MVP

**Goal**: `hush init server` creates the encrypted vault, writes a 0600 `config.toml` populated with every documented default, and stores the vault passphrase + Discord bot token in the OS keychain with a hush-binary-only ACL.

**Independent Test**: From [quickstart.md §1](specs/015-init-and-keychain/quickstart.md) — drive `hush init server` in `t.TempDir` via PTY; assert `secrets.vault` and `config.toml` exist at mode `0600`; assert two keychain items exist on the in-process `FakeKeychain` with the absolute binary path recorded as ACL; assert a follow-up `hush serve` opens the vault on first try.

### Tests for User Story 1 (TDD — write FIRST, ensure FAIL before implementation) ⚠️

- [x] T015 [P] [US1] Write `TestInitServer_RefusesShortPassphrase` in [internal/cli/init_test.go](internal/cli/init_test.go) — drive PTY with passphrase `"short"` (5 bytes); assert exit code `ExitInputErr` (2); assert stderr matches the locked literal `"hush: init: passphrase must be at least 12 characters"` ([contracts/cli-init.md §2.3](specs/015-init-and-keychain/contracts/cli-init.md)); assert NO vault file, NO `config.toml`, NO keychain item created (FakeKeychain empty)
- [x] T016 [P] [US1] Write `TestInitServer_RejectsConfirmationMismatch` in [internal/cli/init_test.go](internal/cli/init_test.go) — drive PTY with two different ≥12-char passphrases; assert exit `ExitInputErr` (2); assert stderr matches `"hush: init: passphrase confirmation does not match"`; assert no artifact created (FR-004)
- [x] T017 [P] [US1] Write `TestInitServer_RejectsNonTTYStdin` in [internal/cli/init_test.go](internal/cli/init_test.go) — invoke with `os.Stdin` set to a `*os.File` that is NOT a terminal (e.g. an `os.Pipe()` reader); assert exit `ExitInputErr` (2); assert stderr matches `"hush: init: stdin must be an interactive terminal"` (FR-005, [contracts/cli-init.md §2.3](specs/015-init-and-keychain/contracts/cli-init.md))
- [x] T018 [P] [US1] Write `TestInitServer_CreatesVaultWith0600` in [internal/cli/init_test.go](internal/cli/init_test.go) — full happy-path PTY drive into `t.TempDir`; assert `os.Stat(<tmp>/secrets.vault).Mode().Perm() == 0o600` (FR-007); assert vault file size > 0 and starts with the SDD-03 magic header
- [x] T019 [P] [US1] Write `TestInitServer_CreatesConfigWithAllDefaults` in [internal/cli/init_test.go](internal/cli/init_test.go) — happy-path PTY drive; load the produced `config.toml` via `config.LoadServer(ctx, path)`; assert mode `0600`; assert **every** field listed in [data-model.md §1.2](specs/015-init-and-keychain/data-model.md) (every key from [docs/CONFIG-SCHEMA.md](docs/CONFIG-SCHEMA.md)) is present and equals its documented default — table-driven with one row per field (FR-009, SC-009)
- [x] T020 [P] [US1] Write `TestInitServer_StoresVaultPassphraseInKeychain` in [internal/cli/init_test.go](internal/cli/init_test.go) — assert `FakeKeychain.Retrieve(ctx, "hush-vault-passphrase", "hush-server")` returns a `*securebytes.SecureBytes` whose bytes equal the PTY-supplied passphrase (FR-011)
- [x] T021 [P] [US1] Write `TestInitServer_StoresBotTokenInKeychain` in [internal/cli/init_test.go](internal/cli/init_test.go) — assert `FakeKeychain.Retrieve(ctx, "hush-discord", "hush-server")` returns the PTY-supplied bot token bytes (FR-010); assert `FakeKeychain.RecordedACL("hush-discord", "hush-server")` equals the absolute binary path returned by the injected `binaryPath` seam
- [x] T022 [P] [US1] Write `TestInitServer_RefusesPreExistingVault` in [internal/cli/init_test.go](internal/cli/init_test.go) — pre-create `<tmp>/secrets.vault` with arbitrary bytes; run init server; assert exit `ExitErr` (1); assert stderr matches `"hush: init: vault already exists at <path>"` (FR-012); assert pre-existing file UNCHANGED (byte-equal, mtime stable)
- [x] T023 [P] [US1] Write `TestInitServer_RefusesPreExistingConfig` in [internal/cli/init_test.go](internal/cli/init_test.go) — pre-create `<tmp>/config.toml` with arbitrary bytes; run init server; assert exit `ExitErr` (1); assert stderr matches `"hush: init: config already exists at <path>"`; assert no vault file is written either (atomic-write invariant per [contracts/cli-init.md §4.4](specs/015-init-and-keychain/contracts/cli-init.md))
- [x] T024 [P] [US1] Write `TestInitServer_RefusesPreExistingKeychainItem` in [internal/cli/init_test.go](internal/cli/init_test.go) — pre-populate `FakeKeychain` with `(hush-vault-passphrase, hush-server)`; run init server; assert exit `ExitErr` (1); assert stderr matches `"hush: init: keychain item already exists for service=hush-vault-passphrase account=hush-server"` (Clarification 2026-05-03 Q1)
- [x] T025 [P] [US1] Write `TestInitServer_AtomicWriteConfigToml` in [internal/cli/init_test.go](internal/cli/init_test.go) — inject a `runner` seam that simulates a Sync/Rename failure between `config.toml.tmp` and `config.toml`; assert no `config.toml` exists after the failure; assert no leftover `.tmp` file; assert init exits non-zero ([contracts/cli-init.md §4.4](specs/015-init-and-keychain/contracts/cli-init.md))
- [x] T026 [P] [US1] Write `TestInitServer_PathPrefixGenerated12CharsURLSafe` in [internal/cli/init_test.go](internal/cli/init_test.go) — happy-path drive with seeded `randReader` seam; load resulting `config.toml`; assert `server.path_prefix` matches `^[A-Za-z0-9_-]{12}$` (research §9)
- [x] T027 [P] [US1] Write `TestInitServer_PromptOrderLocked` in [internal/cli/init_test.go](internal/cli/init_test.go) — inject a `ttyReader` seam that records the prompt label sequence; assert the recorded labels equal the locked sequence from [contracts/cli-init.md §2.2](specs/015-init-and-keychain/contracts/cli-init.md): `["Vault passphrase: ", "Confirm vault passphrase: ", "Listen address (e.g. 100.96.10.4:7743): ", "Discord owner ID (snowflake): ", "Discord application ID (snowflake): ", "Discord bot token: "]`
- [x] T028 [P] [US1] Write `TestInitServer_RoundTripsConfigViaLoadServer` in [internal/cli/init_test.go](internal/cli/init_test.go) — happy-path drive; call `config.LoadServer(ctx, generatedPath)` and assert no error returns (round-trip-validity per [data-model.md §1.2](specs/015-init-and-keychain/data-model.md))

### Implementation for User Story 1

- [x] T029 [US1] Define the `initDeps` struct (fields per [research.md §11](specs/015-init-and-keychain/research.md): `keychain`, `binaryPath`, `randReader`, `ttyReader`, `stateDirRoot`, `nowFn`) and the production binding helper in [internal/cli/init.go](internal/cli/init.go)
- [x] T030 [US1] Add init-specific sentinel errors (`errVaultExists`, `errConfigExists`, `errKeychainItemExists`, `errPassphraseTooShort`, `errPassphraseMismatch`, `errNoTTY`, `errPlatformACLUnsupported`; reuse existing `errMissingFlag`) to [internal/cli/exit_codes.go](internal/cli/exit_codes.go) and wire each to its locked exit code per [data-model.md §5](specs/015-init-and-keychain/data-model.md)
- [x] T031 [US1] Implement `readPassphraseTTY(in *os.File, prompt io.Writer, label string) ([]byte, error)` in [internal/cli/init.go](internal/cli/init.go) using `golang.org/x/term.IsTerminal` + `term.ReadPassword`; non-TTY → `errNoTTY`; returned bytes wrapped in `*securebytes.SecureBytes` at the caller
- [x] T032 [US1] Implement `readLineFromTTY(in *os.File, prompt io.Writer, label string) (string, error)` for non-secret prompts (listen_addr, owner ID, app ID) using `bufio.Scanner` over `os.Stdin`; reject empty input with up-to-3-attempts re-prompt then `errMissingFlag`
- [x] T033 [US1] Implement `confirmPassphrase(in *os.File, prompt io.Writer, first *securebytes.SecureBytes) error` — second `term.ReadPassword`; mismatch → `errPassphraseMismatch`; both `*securebytes.SecureBytes` destroyed before return on the error path (FR-004)
- [x] T034 [US1] Implement existence-guards `guardVaultAbsent`, `guardConfigAbsent`, `guardKeychainItemAbsent` in [internal/cli/init.go](internal/cli/init.go) (data-model §1.1, §1.2, §1.3); each returns the appropriate `err*Exists` sentinel with the conflicting path/pair embedded in the message
- [x] T035 [US1] Implement `writeConfigTOMLAtomic(path string, cfg *serverDecoded) error` in [internal/cli/init.go](internal/cli/init.go): marshal via `pelletier/go-toml/v2`; `O_WRONLY|O_CREATE|O_EXCL 0o600` on `<path>.tmp`; `f.Sync()`; `os.Rename`; defensive `os.Chmod(0o600)` (research §9)
- [x] T036 [US1] Implement `generatePathPrefix(r io.Reader) (string, error)` in [internal/cli/init.go](internal/cli/init.go) — reads 9 bytes from `r` (defaulting to `crypto/rand.Reader`), encodes via `base64.RawURLEncoding`, yields exactly 12 URL-safe characters
- [x] T037 [US1] Implement `buildServerDecodedFromDefaults(operatorInputs)` in [internal/cli/init.go](internal/cli/init.go) — populates **every** field from [data-model.md §1.2](specs/015-init-and-keychain/data-model.md) using the constants from `internal/config/defaults.go`; operator-supplied values for `listen_addr`/`discord_owner_id`/`application_id` slot in; `path_prefix` is the value from T036
- [x] T038 [US1] Implement `runInitServer(ctx context.Context, deps initDeps) error` orchestrating: TTY check → passphrase prompt+confirm → length check → operator-input prompts → bot token prompt → existence guards → master-seed derivation (`keys.DeriveMasterSeed`) → vault enc subkey (`keys.DeriveVaultEncKey`) → `vault.Save` → atomic config write → `Keychain.Store` for vault passphrase + bot token → round-trip-validate via `config.LoadServer`
- [x] T039 [US1] Implement `newInitServerCmd(deps *initDeps) *cobra.Command` in [internal/cli/init.go](internal/cli/init.go) wiring `Use: "server"`, no subcommand-specific flags, `RunE` calls `runInitServer`
- [x] T040 [US1] Implement `newInitCmd(deps *initDeps) *cobra.Command` (parent for `server`/`client`) in [internal/cli/init.go](internal/cli/init.go); register the parent on the root via [internal/cli/root.go](internal/cli/root.go) (`root.AddCommand(newInitCmd(...))`)
- [x] T041 [US1] Run `go test ./internal/cli/... -run 'TestInitServer_'` — all T015–T028 tests must PASS

**Checkpoint**: `hush init server` is fully functional — fresh-host bootstrap completes; AC-1 entry point reached.

---

## Phase 4: User Story 2 — Operator enrolls a new agent machine (Priority: P1)

**Goal**: `hush init client --machine-index N` derives the per-machine client key, stores it in the OS keychain with hush-binary-only ACL, and prints exactly one `SHA256:<base64>` fingerprint line to stdout.

**Independent Test**: From [quickstart.md §2](specs/015-init-and-keychain/quickstart.md) — drive `hush init client --machine-index 3` via PTY in `t.TempDir`; assert FakeKeychain has `(hush-client, machine-3)` with the binary path recorded as ACL; assert stdout is exactly one 50-char line matching `^SHA256:[A-Za-z0-9+/]{43}\n$`; assert determinism by running twice with same inputs.

### Tests for User Story 2 (TDD — write FIRST, ensure FAIL before implementation) ⚠️

- [x] T042 [P] [US2] Write `TestInitClient_RequiresMachineIndex` in [internal/cli/init_test.go](internal/cli/init_test.go) — invoke `hush init client` with no `--machine-index` flag; assert exit `ExitInputErr` (2); assert stderr matches `"hush: init: missing required flag: --machine-index"` ([contracts/cli-init.md §3.3](specs/015-init-and-keychain/contracts/cli-init.md)); assert no keychain item created
- [x] T043 [P] [US2] Write `TestInitClient_RejectsNegativeMachineIndex` in [internal/cli/init_test.go](internal/cli/init_test.go) — invoke with `--machine-index=-1`; assert exit `ExitInputErr` (2); assert stderr matches `"hush: init: --machine-index must be a non-negative integer"`
- [x] T044 [P] [US2] Write `TestInitClient_RejectsOversizedMachineIndex` in [internal/cli/init_test.go](internal/cli/init_test.go) — invoke with `--machine-index=4294967296` (uint32 max + 1); assert same parse error as T043
- [x] T045 [P] [US2] Write `TestInitClient_StoresInKeychainViaFake` in [internal/cli/init_test.go](internal/cli/init_test.go) — drive PTY with `--machine-index 3`; assert `FakeKeychain.Retrieve(ctx, "hush-client", "machine-3")` returns 32-byte serialized D; assert `RecordedACL("hush-client", "machine-3")` equals the injected `binaryPath` value (FR-016, FR-019)
- [x] T046 [P] [US2] Write `TestInitClient_StoresInKeychainWithACL` in [internal/cli/init_darwin_test.go](internal/cli/init_darwin_test.go) with `//go:build darwin` — drive PTY through the **real** Darwin `Keychain` impl with a `runner` seam that captures `cmd.Args`; assert the constructed argv contains `-T <abs-path-to-binary>`; assert `-w` is present (stdin password); assert no `-A` (allow-all) flag (FR-020, [contracts/keychain-api.md §6](specs/015-init-and-keychain/contracts/keychain-api.md))
- [x] T047 [P] [US2] Write `TestInitClient_PrintsFingerprintOneLine` in [internal/cli/init_test.go](internal/cli/init_test.go) — happy-path drive; capture stdout into a `bytes.Buffer`; assert exactly one `\n`; trim and assert regex `^SHA256:[A-Za-z0-9+/]{43}$`; assert `len(trimmed) == 50`; assert NO additional bytes after the trailing newline (FR-017, [contracts/cli-init.md §3.4](specs/015-init-and-keychain/contracts/cli-init.md))
- [x] T048 [P] [US2] Write `TestInitClient_DeterministicAcrossRuns` in [internal/cli/init_test.go](internal/cli/init_test.go) — run twice with the same scripted passphrase + `--machine-index 0`; between runs, call `FakeKeychain.Delete("hush-client", "machine-0")` so the second run's existence guard passes; assert stdout output is byte-identical (SC-004)
- [x] T049 [P] [US2] Write `TestInitClient_DistinctInputsProduceDistinctFingerprints` in [internal/cli/init_test.go](internal/cli/init_test.go) — three runs: (passphrase A, idx 0), (passphrase A, idx 1), (passphrase B, idx 0); assert all three fingerprints differ (SC-005)
- [x] T050 [P] [US2] Write `TestInitClient_RefusesPreExistingKeychainItem` in [internal/cli/init_test.go](internal/cli/init_test.go) — pre-populate `FakeKeychain` with `(hush-client, machine-3)`; invoke client init with `--machine-index 3`; assert exit `ExitErr` (1); assert stderr matches `"hush: init: keychain item already exists for service=hush-client account=machine-3"`
- [x] T051 [P] [US2] Write `TestInitClient_ConflictsWithServerMode` in [internal/cli/init_test.go](internal/cli/init_test.go) — verify mutual exclusivity is **structural** per research §6: invoke `hush init server client` (positional combination) and assert cobra returns "unknown command" non-zero; document via test comment that no flag combination can produce a conflict because the cobra tree separates the two subcommands (FR-018)
- [x] T052 [P] [US2] Write `TestInitClient_RejectsConfirmationMismatch` in [internal/cli/init_test.go](internal/cli/init_test.go) — Q4-locked behaviour: client mode also requires double-entry; mismatch → `ExitInputErr` with the same locked stderr text as T016
- [x] T053 [P] [US2] Write `TestInitClient_NoStderrOnSuccess` in [internal/cli/init_test.go](internal/cli/init_test.go) — happy-path drive (PTY-supplied prompts only); assert stderr after deducting prompt echoes is empty; the fingerprint is the **only** non-prompt output and it goes to stdout ([contracts/cli-init.md §3.3](specs/015-init-and-keychain/contracts/cli-init.md))

### Implementation for User Story 2

- [x] T054 [US2] Implement `sec1Compress(pub *ecdsa.PublicKey) []byte` in [internal/cli/init.go](internal/cli/init.go) — produces SEC1-compressed encoding (parity byte 0x02/0x03 + 32-byte X coordinate); local helper, NOT a modification to `internal/keys` (research §3 — SDD-01-locked surface stays untouched)
- [x] T055 [US2] Implement `sshStyleFingerprint(pub *ecdsa.PublicKey) string` in [internal/cli/init.go](internal/cli/init.go) returning `"SHA256:" + base64.RawStdEncoding.EncodeToString(sha256.Sum256(sec1Compress(pub)))` — exactly 50 characters total (research §3)
- [x] T056 [US2] Implement `serializeECPrivKey(priv *ecdsa.PrivateKey) *securebytes.SecureBytes` in [internal/cli/init.go](internal/cli/init.go) — `priv.D.FillBytes(buf[:32])` produces 32-byte fixed-width big-endian; wrap in `*securebytes.SecureBytes`; caller owns and must `Destroy` after the keychain `Store` returns
- [x] T057 [US2] Implement `runInitClient(ctx context.Context, deps initDeps, machineIndex uint32) error` orchestrating: TTY check → passphrase prompt+confirm → length check → existence guard for `(hush-client, machine-<N>)` → `keys.DeriveMasterSeed` → `keys.DeriveClientKey(seed, N)` → `serializeECPrivKey` → `Keychain.Store` with the binary path as ACL → print `sshStyleFingerprint(pub)` + "\n" to stdout → destroy all `*securebytes.SecureBytes`
- [x] T058 [US2] Implement `newInitClientCmd(deps *initDeps) *cobra.Command` in [internal/cli/init.go](internal/cli/init.go) — `Use: "client"`, exposes `--machine-index` as `Uint32Var`; `RunE` parses the flag, defends against missing-flag (cobra MarkFlagRequired) returning `errMissingFlag` mapped to `ExitInputErr`, and calls `runInitClient`
- [x] T059 [US2] Wire `newInitClientCmd` under `newInitCmd` parent in [internal/cli/init.go](internal/cli/init.go) — `parent.AddCommand(newInitClientCmd(deps))` alongside the server subcommand from T040
- [x] T060 [US2] Run `go test ./internal/cli/... -run 'TestInitClient_'` — all T042–T053 tests must PASS

**Checkpoint**: `hush init client --machine-index N` is fully functional — agent enrollment completes; AC-6 entry point reached.

---

## Phase 5: User Story 3 — Passphrase stays out of process arguments and environment (Priority: P1)

**Goal**: Constitutional non-negotiable — the passphrase is read **only** from the controlling TTY with echo suppressed; `os.Getenv` is never called for any passphrase-class value; no flag value is ever interpreted as a passphrase; no output stream contains the passphrase, bot token, derived seed, or per-machine private key.

**Independent Test**: Set `HUSH_PASSPHRASE=SECRET_SHOULD_NEVER_APPEAR_15` in the test environment, supply a different passphrase via PTY, run init server end-to-end; assert (a) the resulting vault opens with the PTY-supplied passphrase only, (b) no captured byte of stdout / stderr / slog output equals the sentinel.

### Tests for User Story 3 (TDD — write FIRST, ensure FAIL before implementation) ⚠️

- [x] T061 [P] [US3] Write `TestInitServer_NeverReadsPassphraseFromEnv` in [internal/cli/init_test.go](internal/cli/init_test.go) — call `t.Setenv("HUSH_PASSPHRASE", "SECRET_SHOULD_NEVER_APPEAR_15")`, also `t.Setenv("PASSPHRASE", "SECRET_SHOULD_NEVER_APPEAR_15")`; drive PTY with a real ≥12-char passphrase; assert init succeeds; assert the vault file decrypts under the PTY-supplied passphrase via `vault.Load`; assert it does NOT decrypt under the sentinel (FR-001, SC-007)
- [x] T062 [P] [US3] Write `TestInitServer_NeverLeaksPassphraseToOutput` in [internal/cli/init_test.go](internal/cli/init_test.go) — drive PTY with passphrase `SECRET_SHOULD_NEVER_APPEAR_15_xx`; capture stdout, stderr, and the slog `*slog.Handler`'s buffer; assert via `internal/testutil.AssertSentinelAbsent` that NO captured byte sequence contains the sentinel (FR-022, SC-006)
- [x] T063 [P] [US3] Write `TestInitServer_NeverLeaksBotTokenToOutput` in [internal/cli/init_test.go](internal/cli/init_test.go) — drive PTY with bot token `SECRET_SHOULD_NEVER_APPEAR_15_bot`; assert `AssertSentinelAbsent` over stdout+stderr+slog
- [x] T064 [P] [US3] Write `TestInitClient_NeverLeaksDerivedKeyToOutput` in [internal/cli/init_test.go](internal/cli/init_test.go) — drive client init; capture `FakeKeychain` stored bytes for `(hush-client, machine-N)`; capture stdout+stderr+slog; assert no overlapping ≥8-byte subsequence between the stored private-key bytes and the captured output (research §10)
- [x] T065 [P] [US3] Write `TestInit_LintNoOsGetenv` in [internal/cli/init_test.go](internal/cli/init_test.go) — `os.ReadFile("init.go")`; assert `bytes.Contains(content, []byte("os.Getenv"))` is **false** (CI-grep equivalent, [contracts/cli-init.md §4.1](specs/015-init-and-keychain/contracts/cli-init.md))
- [x] T066 [P] [US3] Write `TestInit_NoPassphraseFlag` in [internal/cli/init_test.go](internal/cli/init_test.go) — walk both subcommands' flag sets via `cmd.Flags().VisitAll`; assert no flag name contains `pass`, `secret`, or `key` substrings (FR-001 / [contracts/cli-init.md §4.2](specs/015-init-and-keychain/contracts/cli-init.md))
- [x] T067 [P] [US3] Write `TestInitServer_RejectsPipedStdin` in [internal/cli/init_test.go](internal/cli/init_test.go) — set `os.Stdin` to a `bytes.Reader` pipe containing a valid 16-char passphrase; assert init still exits `ExitInputErr` (2) with `"hush: init: stdin must be an interactive terminal"` (Q5 — TTY-only divergence from `serve`)
- [x] T068 [P] [US3] Write `TestInit_NeverGeneratesPassphrase` in [internal/cli/init_test.go](internal/cli/init_test.go) — grep init.go content for any token containing `Generate.*Pass` or any call to `keys.GeneratePassphrase` / `passphrase.Generate`; assert zero matches (FR-002)

### Implementation for User Story 3

- [x] T069 [US3] Audit [internal/cli/init.go](internal/cli/init.go) for any `os.Getenv` reference and remove if present; the lint test from T065 enforces this going forward
- [x] T070 [US3] Audit cobra flag definitions in [internal/cli/init.go](internal/cli/init.go) — neither `newInitServerCmd` nor `newInitClientCmd` defines a `--passphrase` / `--bot-token` / `--secret` flag; only `--machine-index` exists on the client subcommand
- [x] T071 [US3] Confirm `runInitServer` and `runInitClient` invoke `term.IsTerminal(int(os.Stdin.Fd()))` as the very first action and return `errNoTTY` on false BEFORE any prompt is written (research §7)
- [x] T072 [US3] Wire `*slog.Handler` carefully in init.go: ALL slog calls use `slog.LogValuer` redaction (`*securebytes.SecureBytes` already implements `LogValue() → "[redacted]"`); NEVER `string(secret)` and never `fmt.Sprintf("%s", secret)` in any log line
- [x] T073 [US3] Run `go test ./internal/cli/... -run 'TestInit.*Leak|TestInit.*Env|TestInit.*Stdin|TestInit.*Lint|TestInit_NoPassphraseFlag|TestInit_NeverGeneratesPassphrase'` — all T061–T068 must PASS

**Checkpoint**: Sentinel-leak invariant holds; passphrase isolation is verified end-to-end; SC-006 + SC-007 satisfied.

---

## Phase 6: User Story 4 — Keychain ACL enforcement (Priority: P2)

**Goal**: Every keychain item created by init carries a per-binary ACL; on platforms without per-binary ACL semantics, init refuses to run with a clear platform-incompatibility error rather than silently downgrading.

**Independent Test**: On macOS — drive client init through the real Darwin `Keychain` impl with a runner seam that captures argv; assert `-T <abs-path>` flag present, `-w` flag present, no `-A` flag. On Linux — invoke `hush init server`; assert exit `ExitErr` with `"hush: init: platform linux has no per-binary keychain ACL; init refuses to run"`; assert no vault, config, or keychain item created.

### Tests for User Story 4 (TDD — write FIRST, ensure FAIL before implementation) ⚠️

- [x] T074 [P] [US4] Write `TestKeychainDarwin_ConstructedSecurityCommand` in [internal/keychain/keychain_darwin_test.go](internal/keychain/keychain_darwin_test.go) with `//go:build darwin` — inject a `runner` seam (`func(*exec.Cmd) error`) that captures `cmd.Path` + `cmd.Args` without launching `/usr/bin/security`; call `Store(ctx, "svc", "acct", val, "/abs/hush")`; assert `cmd.Path == "/usr/bin/security"`; assert argv equals `["security", "add-generic-password", "-s", "svc", "-a", "acct", "-T", "/abs/hush", "-w"]`; assert `cmd.Stdin` reads to byte-equal `val`; assert `-A` is NOT in argv
- [x] T075 [P] [US4] Write `TestKeychainDarwin_StoreReturnsItemExistsOn45` in [internal/keychain/keychain_darwin_test.go](internal/keychain/keychain_darwin_test.go) with `//go:build darwin` — runner seam returns `*exec.ExitError` with code `45` (`SecKeychainErrDuplicateItem`); assert `Store` returns `errors.Is(err, ErrKeychainItemExists)`
- [x] T076 [P] [US4] Write `TestKeychainDarwin_RetrieveExitCode44IsNotFound` in [internal/keychain/keychain_darwin_test.go](internal/keychain/keychain_darwin_test.go) with `//go:build darwin` — runner returns exit code 44; assert `Retrieve` returns `errors.Is(err, ErrKeychainItemNotFound)` ([contracts/keychain-api.md §6](specs/015-init-and-keychain/contracts/keychain-api.md))
- [x] T077 [P] [US4] Write `TestKeychainDarwin_RetrieveExitCode51IsPermissionDenied` in [internal/keychain/keychain_darwin_test.go](internal/keychain/keychain_darwin_test.go) with `//go:build darwin` — runner returns exit code 51; assert `Retrieve` returns `errors.Is(err, ErrKeychainPermissionDenied)`
- [x] T078 [P] [US4] Write `TestKeychainDarwin_DeleteSucceedsAndIsNotIdempotent` in [internal/keychain/keychain_darwin_test.go](internal/keychain/keychain_darwin_test.go) with `//go:build darwin` — first runner call returns 0 (success); second returns 44; assert first `Delete` is nil; assert second returns `ErrKeychainItemNotFound`
- [x] T079 [P] [US4] Write `TestKeychainDarwin_StoreSecretViaStdinNotArgv` in [internal/keychain/keychain_darwin_test.go](internal/keychain/keychain_darwin_test.go) with `//go:build darwin` — drive `Store` with sentinel value `"PROC_LISTING_LEAK"`; assert sentinel is NOT in `cmd.Args` ([] join); assert sentinel IS readable from `cmd.Stdin` ([contracts/keychain-api.md §6](specs/015-init-and-keychain/contracts/keychain-api.md))
- [x] T080 [P] [US4] Write `TestKeychainLinux_ZalandoBackend` in [internal/keychain/keychain_linux_test.go](internal/keychain/keychain_linux_test.go) with `//go:build linux` — inject a fake `keyring` seam (interface wrapping `keyring.Set/Get/Delete`); call `Store`/`Retrieve`/`Delete`; assert each call routes to the corresponding fake-keyring method with the right `(service, account)` pair; assert `acl` argument is **discarded** ([contracts/keychain-api.md §7](specs/015-init-and-keychain/contracts/keychain-api.md))
- [x] T081 [P] [US4] Write `TestKeychainLinux_RetrieveErrNotFound` in [internal/keychain/keychain_linux_test.go](internal/keychain/keychain_linux_test.go) with `//go:build linux` — fake keyring returns `keyring.ErrNotFound`; assert `Retrieve` returns `errors.Is(err, ErrKeychainItemNotFound)`
- [x] T082 [P] [US4] Write `TestPerBinaryACLSupported_Darwin` in [internal/keychain/keychain_darwin_test.go](internal/keychain/keychain_darwin_test.go) with `//go:build darwin` — assert `keychain.PerBinaryACLSupported()` returns `true`
- [x] T083 [P] [US4] Write `TestPerBinaryACLSupported_Linux` in [internal/keychain/keychain_linux_test.go](internal/keychain/keychain_linux_test.go) with `//go:build linux` — assert `keychain.PerBinaryACLSupported()` returns `false`
- [x] T084 [P] [US4] Write `TestInit_RefusesOnNonDarwinPlatform` in [internal/cli/init_linux_test.go](internal/cli/init_linux_test.go) with `//go:build linux` — invoke `hush init server` (or any subcommand) on Linux; assert exit `ExitErr` (1); assert stderr matches `"hush: init: platform linux has no per-binary keychain ACL; init refuses to run"`; assert no vault, no config, no keychain item created (FR-020a)
- [x] T085 [P] [US4] Write `TestInit_GuardsPlatformBeforeAnyWrite` in [internal/cli/init_test.go](internal/cli/init_test.go) — inject `keychain.PerBinaryACLSupported` to return `false` via dependency-injection seam; invoke server init; assert exit `ExitErr`; assert NO `os.Stat` of vault path was performed (early refuse) — verify by setting an unwritable `stateDirRoot` and confirming the platform-incompatibility error preempts any filesystem error

### Implementation for User Story 4

- [x] T086 [US4] Implement `darwinKeychain` struct in [internal/keychain/keychain_darwin.go](internal/keychain/keychain_darwin.go) with `//go:build darwin`: `looker func(name string) (string, error)` (default `exec.LookPath`) and `runner func(*exec.Cmd) error` (default `(*exec.Cmd).Run`) seams per research §11
- [x] T087 [US4] Implement `(*darwinKeychain).Store` in [internal/keychain/keychain_darwin.go](internal/keychain/keychain_darwin.go) constructing `/usr/bin/security add-generic-password -s <s> -a <a> -T <acl> -w` with secret written to `cmd.Stdin` from inside `value.Use(func(b []byte) { cmd.Stdin = bytes.NewReader(b) })`; map exit code 45 → `ErrKeychainItemExists`
- [x] T088 [US4] Implement `(*darwinKeychain).Retrieve` in [internal/keychain/keychain_darwin.go](internal/keychain/keychain_darwin.go) — `/usr/bin/security find-generic-password -s <s> -a <a> -w`; parse trailing-newline-terminated stdout into a fresh `*securebytes.SecureBytes`; map exit 44 → `ErrKeychainItemNotFound`, 51 → `ErrKeychainPermissionDenied`
- [x] T089 [US4] Implement `(*darwinKeychain).Delete` in [internal/keychain/keychain_darwin.go](internal/keychain/keychain_darwin.go) — `/usr/bin/security delete-generic-password -s <s> -a <a>`; same exit-code mapping as Retrieve
- [x] T090 [US4] Implement `linuxKeychain` struct in [internal/keychain/keychain_linux.go](internal/keychain/keychain_linux.go) with `//go:build linux` wrapping `keyring.Set/Get/Delete` from `github.com/zalando/go-keyring`; map `keyring.ErrNotFound` → `ErrKeychainItemNotFound`; document the `acl` discard (research §2)
- [x] T091 [US4] Implement `(*linuxKeychain).Store` / `Retrieve` / `Delete` in [internal/keychain/keychain_linux.go](internal/keychain/keychain_linux.go) — the `*securebytes.SecureBytes`→string boundary stays inside a stack-local within the `Use` closure; never logged ([contracts/keychain-api.md §7](specs/015-init-and-keychain/contracts/keychain-api.md))
- [x] T092 [US4] Wire `keychain.PerBinaryACLSupported()` guard at the very top of `runInitServer` and `runInitClient` in [internal/cli/init.go](internal/cli/init.go) — fires **before** TTY check, **before** any existence guard, **before** any KDF call (research §2)
- [x] T093 [US4] Run `go test ./internal/keychain/... && go test ./internal/cli/... -run 'TestInit.*Platform|TestKeychainDarwin_|TestKeychainLinux_|TestPerBinaryACLSupported_'` — all T074–T085 tests must PASS on the appropriate platform

**Checkpoint**: Per-binary ACL is verified at the argv level on Darwin; Linux refuses up-front with no silent downgrade; AC-6 enforcement complete.

---

## Phase 7: Polish & Cross-Cutting Concerns

**Purpose**: Integration test, documentation updates, final gates per [SDD-15.md Prompt 5](docs/sdd/SDD-15.md), and the combined commit.

- [x] T094 [P] Write `TestInit_FullDanceInTempDir` in [internal/cli/init_integration_test.go](internal/cli/init_integration_test.go) with `//go:build integration` — runs the full quickstart §1 + §2 flow in `t.TempDir`: server init → assert artifacts → client init `--machine-index 0` → assert fingerprint stdout → second client init with same index → refusal → second client init with `--machine-index 1` → different fingerprint → final state inspection per [quickstart.md §4](specs/015-init-and-keychain/quickstart.md)
- [x] T095 [P] Append "Exported API — locked at SDD-15" section to [docs/PACKAGE-MAP.md](docs/PACKAGE-MAP.md): note `internal/cli` `init` parent + `server`/`client` subcommands; new `internal/keychain` entry listing `Keychain`, `New`, `PerBinaryACLSupported`, `NewFake`, and the four `Err*` sentinels
- [x] T096 [P] Update [docs/AC-MATRIX.md](docs/AC-MATRIX.md) AC-1 row with `internal/cli/init_test.go::TestInitServer_*` and `internal/cli/init_integration_test.go::TestInit_FullDanceInTempDir`; update AC-6 row with `internal/cli/init_test.go::TestInitClient_StoresInKeychainViaFake`, `internal/cli/init_darwin_test.go::TestInitClient_StoresInKeychainWithACL`, and `internal/keychain/keychain_darwin_test.go::TestKeychainDarwin_ConstructedSecurityCommand`
- [x] T097 [P] Mark SDD-15 status `done` in [docs/SDD-PLAYBOOK.md](docs/SDD-PLAYBOOK.md)
- [x] T098 [P] Update [docs/SDD-CATALOG.md](docs/SDD-CATALOG.md) SDD-15 row: status `done`, link to [internal/cli/init.go](internal/cli/init.go) and [internal/keychain/keychain.go](internal/keychain/keychain.go)
- [x] T099 Coverage check: `go test -cover ./internal/cli/ -run Init` must report ≥ 85% on the init code paths
- [x] T100 Coverage check: `go test -cover ./internal/keychain/` must report ≥ 85%; sentinel-leak + passphrase-resolution paths reach 100%
- [x] T101 Final gate: `magex format:fix` from repo root — must complete clean
- [x] T102 Final gate: `magex lint` from repo root — must complete clean (zero new lints)
- [x] T103 Final gate: `magex test:race` from repo root — full unit suite, race-clean
- [x] T104 Final gate: `magex test:race -tags=integration` from repo root — integration suite, race-clean (drives T094)
- [x] T105 Manual smoke (macOS only, if available): `hush init server` in a scratch tempdir; `security find-generic-password -s hush-discord -a hush-server` confirms the bot token is stored with the running binary's absolute path as ACL; `hush serve` opens the resulting vault on first attempt
- [x] T106 Determinism check: run `hush init server` twice in two fresh tempdirs with identical scripted inputs; assert generated `config.toml` byte-equal except for the random `path_prefix`; assert vault salts differ (per-run `crypto/rand`)
- [x] T107 Combined commit per SDD-15 Prompt 5: `git add internal/cli/ internal/keychain/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md docs/SDD-CATALOG.md specs/015-init-and-keychain/tasks.md go.mod go.sum && git commit -m "feat(cli,keychain): hush init server/client + Keychain ACL wrapper (SDD-15)"`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Phase 1 (Setup)**: No dependencies — start immediately
- **Phase 2 (Foundational)**: depends on Phase 1; **BLOCKS** all user stories — `internal/keychain` must compile and `FakeKeychain` must work before US1/US2/US3/US4 tests can compile
- **Phase 3 (US1 — Server bootstrap)**: depends on Phase 2; **AC-1 entry point** → MVP candidate
- **Phase 4 (US2 — Client enrollment)**: depends on Phase 2; **AC-6 entry point**; can run in parallel with US1 if staffed (different functions in the same `init.go` file → `[P]` only inside the test phase, not the implementation phase)
- **Phase 5 (US3 — Passphrase isolation)**: depends on Phase 3 + Phase 4 implementations existing (most US3 tests assert on the existing init code paths); the lint/grep tests T065/T066/T068 are pure asserts and can run earlier
- **Phase 6 (US4 — Keychain ACL)**: depends on Phase 2 (interface+sentinels); the darwin/linux platform impls land here, gated by `PerBinaryACLSupported()` which both US1/US2 already call
- **Phase 7 (Polish)**: depends on Phases 3–6 complete

### User Story Dependencies

- **US1 + US2** are functionally independent (different cobra subcommands, different code paths) but share `init.go` → tests are `[P]` (different test functions), implementation tasks are sequential within the file (Edits to the same file)
- **US3** is an audit/invariant story — it asserts properties of the code US1/US2 already produced; only the runtime behaviour tests T061–T064 require US1/US2 implementations to exist; the lint tests T065/T066/T068 can run any time after T029
- **US4** is independent of US1/US2 in tests (uses real Darwin/Linux `Keychain` impls or seamed runners) but the platform impls lock in the ACL behaviour US1/US2 rely on at runtime

### Within Each Story

- **TDD discipline**: every test task (T015–T028, T042–T053, T061–T068, T074–T085) MUST be written and observed FAIL before its corresponding implementation task is started. Implementation tasks are explicitly listed AFTER the tests within each phase.
- Sentinels and error wiring (T030) come before the orchestration functions (T038, T057) that return them.
- Helpers (T031–T037, T054–T056) come before the orchestration that calls them.

### Parallel Opportunities

- All [P]-marked tests within a phase touch independent test functions in the same `*_test.go` file → can be authored by separate sessions concurrently
- Foundational tests T004–T008 are all `[P]` in the same `keychain_test.go` — different `func Test...` blocks
- Phase 7 doc-update tasks T095–T098 are `[P]` (different docs)
- The four user stories (US1/US2/US3/US4) can be staffed in parallel after Phase 2 completes

---

## Parallel Example: User Story 1 Tests

```text
# Author all US1 test functions in parallel (single file, independent funcs):
Task T015 [P] [US1]: TestInitServer_RefusesShortPassphrase
Task T016 [P] [US1]: TestInitServer_RejectsConfirmationMismatch
Task T017 [P] [US1]: TestInitServer_RejectsNonTTYStdin
Task T018 [P] [US1]: TestInitServer_CreatesVaultWith0600
Task T019 [P] [US1]: TestInitServer_CreatesConfigWithAllDefaults
Task T020 [P] [US1]: TestInitServer_StoresVaultPassphraseInKeychain
Task T021 [P] [US1]: TestInitServer_StoresBotTokenInKeychain
Task T022 [P] [US1]: TestInitServer_RefusesPreExistingVault
Task T023 [P] [US1]: TestInitServer_RefusesPreExistingConfig
Task T024 [P] [US1]: TestInitServer_RefusesPreExistingKeychainItem
Task T025 [P] [US1]: TestInitServer_AtomicWriteConfigToml
Task T026 [P] [US1]: TestInitServer_PathPrefixGenerated12CharsURLSafe
Task T027 [P] [US1]: TestInitServer_PromptOrderLocked
Task T028 [P] [US1]: TestInitServer_RoundTripsConfigViaLoadServer
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Phase 1: Setup (T001–T003)
2. Phase 2: Foundational (T004–T014) — `internal/keychain` interface + FakeKeychain
3. Phase 3: User Story 1 (T015–T041) — full `hush init server`
4. **STOP and VALIDATE**: drive `hush init server` end-to-end in a scratch tempdir; immediately follow with `hush serve` opening the produced vault — proves AC-1
5. **Demo-ready** at this checkpoint — operator can bootstrap a vault host even without agent enrollment

### Incremental Delivery

1. MVP: Phase 1 + Phase 2 + Phase 3 (US1) → AC-1 reached
2. Add Phase 4 (US2) → AC-6 reached → operator can enroll agents
3. Add Phase 5 (US3) → constitutional invariants validated → security-review-ready
4. Add Phase 6 (US4) → real Darwin/Linux platform impls → Linux refusal in place
5. Phase 7 → integration test, gates, commit → SDD-15 done

### Parallel Team Strategy

After Phase 2 completes:
- Developer A: US1 (server bootstrap)
- Developer B: US2 (client enrollment)
- Developer C: US4 (Darwin/Linux platform impls)
- Developer D: US3 lint+invariant tests (can run before US1/US2 finish)

US3 runtime-leak tests (T061–T064) wait until A+B implementations exist; US3 lint/grep tests (T065/T066/T068) can land any time after Phase 2.

---

## Notes

- **TDD-mandatory**: every test task is listed BEFORE its implementation task within each phase. Verify tests FAIL before writing the implementation that makes them pass (Constitution VIII).
- **Build-tag tests**: `_darwin_test.go` files run only on macOS; `_linux_test.go` files run only on Linux. CI must run both platforms to cover both code paths.
- **Sentinel discipline**: every secret-handling test uses `internal/testutil.SentinelSecret(15)` (`SECRET_SHOULD_NEVER_APPEAR_15`) and `AssertSentinelAbsent` over captured output streams (research §10).
- **Locked literal-text contracts**: every stderr message in [contracts/cli-init.md §2.3 / §3.3](specs/015-init-and-keychain/contracts/cli-init.md) is byte-equal asserted by tests — changes require an SDD amendment.
- **No `os.Getenv` in init.go**: enforced by T065 lint test.
- **No passphrase generation**: enforced by T068 grep test.
- **Coverage**: 85% on both packages; 100% on the passphrase-resolution and sentinel-leak code paths (SDD-15 contract).
- Commits are deferred to the single combined commit T107 at the end of Phase 7 per [SDD-15.md Prompt 5](docs/sdd/SDD-15.md).
