# SDD-CATALOG — full chunk catalog with agent prompts

> The authoritative in-repo catalog of every SDD chunk that builds hush
> v0.1.0. Each chunk has: scope, files, blockers/blocks, behaviour
> contracts, anti-contracts, test gates, AC mapping, principle mapping,
> coverage target, and a **ready-to-paste agent prompt**.
>
> The companion files:
> - [`docs/SDD-PLAYBOOK.md`](SDD-PLAYBOOK.md) — at-a-glance index + status dashboard
> - [`docs/AC-MATRIX.md`](AC-MATRIX.md) — AC ↔ chunk ↔ test path mapping
> - [`docs/IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) — phase rationale + dependency direction
> - [`.specify/memory/constitution.md`](../.specify/memory/constitution.md) — non-negotiable principles

---

## How to use this catalog

1. Pick a ready chunk from [`docs/SDD-PLAYBOOK.md`](SDD-PLAYBOOK.md) (one with all its blockers `done`).
2. Open this file, jump to `### SDD-NN`.
3. Copy the **Agent Prompt** block at the bottom of that chunk.
4. Open a fresh Claude Code session in the repo root.
5. Paste the prompt verbatim. The agent runs `/speckit-specify` →
   `/speckit-plan` → `/speckit-tasks`, then TDD-implements, then runs gates.
6. The agent updates `SDD-PLAYBOOK.md` and `AC-MATRIX.md` before opening its PR.

---

## Cross-cutting requirements (apply to every chunk)

- **Phase-1/2 chunks freeze public API:** Every chunk in Phase 1 (SDD-01..06)
  and Phase 2 (SDD-07..09) ends with appending an `Exported API — locked at
  SDD-NN` section to [`docs/PACKAGE-MAP.md`](PACKAGE-MAP.md). Consumers in
  Phase 3+ only reference the locked section.
- **Sentinel-leak tests:** Wherever a chunk handles a secret value, it MUST
  inject `SECRET_SHOULD_NEVER_APPEAR_<chunk_id>` and assert that the
  sentinel is absent from logs, errors, and any HTTP response. Use the
  helper from `internal/testutil` (SDD-04).
- **Constitutional principles:** Every chunk audits itself against the
  principles in `.specify/memory/constitution.md` it touches, listed
  explicitly per chunk below.
- **TDD:** Tests are written first per `tasks.md` from `/speckit-tasks`,
  fail before implementation begins, then pass.
- **Gates:** Every chunk's gate before merge: `magex format:fix && magex
  lint && magex test:race`. Fuzz chunks add `go test -fuzz=Fuzz<Name>
  -fuzztime=60s ./...`. Sentinel-leak chunks assert the sentinel is absent.

---

## Agent-prompt template

Every chunk's `Agent Prompt` block follows this structure:

```
You are implementing SDD-NN of the hush project.

Pre-reads (ALL mandatory, in this order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md
- /Users/mrz/projects/hush/docs/SPEC.md (focus: §<N>)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (focus: <package>)
- /Users/mrz/projects/hush/docs/<additional doc>
- /Users/mrz/projects/hush/docs/AC-MATRIX.md (current state)

Workflow:
1. /speckit-specify "<chunk title>"
2. /speckit-plan
3. /speckit-tasks
4. Implement TDD per tasks.md
5. Run gates: magex format:fix && magex lint && magex test:race
6. (Fuzz chunks only) go test -fuzz=Fuzz<Name> -fuzztime=60s ./...
7. Append "Exported API — locked at SDD-NN" to docs/PACKAGE-MAP.md (Phase 1–2 only)
8. Update docs/AC-MATRIX.md with the AC rows this chunk satisfies
9. Update docs/SDD-PLAYBOOK.md status to done
10. Commit: feat(<scope>): <one-line summary> referencing SDD-NN

Scope (build exactly this; nothing more):
- Package: <go path>
- Files: <list>
- Public API to export: <list>

Inputs (must already exist; verify before starting):
- SDD-XX delivered <package>

Behaviour contracts (MUST):
- <bullet>

Anti-contracts (MUST NOT):
- <bullet>

Tests required (write FIRST, ensure they fail, then implement):
- Unit: <list>
- Fuzz (if applicable): <list>
- Sentinel-leak (if applicable): inject SECRET_SHOULD_NEVER_APPEAR_<N>; assert absent
- Integration (if applicable): <list>

Acceptance criteria from constitution (cite by ID):
- AC-<N>

Constitutional principles in scope (cite by Roman numeral):
- <list>

Coverage target: <pct>%

When complete, post a checklist confirming each gate passed.
```

The remainder of this catalog fills in this template for SDD-01..SDD-32.

---

# Phase 1 — Cryptographic and storage core

---

## SDD-01 — `internal/keys` (Argon2id + BIP32 HD derivation)

**Phase:** 1
**Package:** `internal/keys`
**Files:** `derive.go`, `paths.go`, `client.go`, `fingerprint.go`, `*_test.go`, `derive_fuzz_test.go`
**Branch:** `001-keys-derivation`
**Blocked by:** none
**Blocks:** SDD-03, SDD-07, SDD-08, SDD-09 (indirectly), SDD-15
**Primary AC:** AC-7 (Bitcoin crypto: BIP32 hierarchy)
**Coverage target:** 100%

**Behaviour contracts (MUST):**
- Argon2id master seed via `time=4`, `memory=256*1024 KiB`, `threads=4`, `keyLen=64` (Constitution III + Security Requirements; non-negotiable)
- BIP32 derivation paths from `docs/SPEC.md` FR-3:
  - `m/44'/7743'/0'` → JWT signing (secp256k1)
  - `m/44'/7743'/1'` → vault encryption (32 bytes for AES-256)
  - `m/44'/7743'/2'` → audit signing (secp256k1)
  - `m/44'/7743'/3'/{machine_index}` → per-agent client keypair
- Use `github.com/bitcoinschema/go-bitcoin/v2` + `golang.org/x/crypto/argon2` only (Constitution XI: no other crypto deps without amendment)
- 16-hex-char public key fingerprint helper for client-key registration UX
- All exported functions take `context.Context` first (Constitution IX)

**Anti-contracts (MUST NOT):**
- Persist any derived material to disk
- Use `math/rand`
- Allow passphrase < 12 bytes (Constitution Security Requirements)
- Import `internal/*` (this is a leaf package)

**Tests:**
- Unit: deterministic round-trip; wrong passphrase → different keys; machine-index isolation; zero-length salt rejected; sub-12-char passphrase rejected
- Fuzz: `FuzzDeriveMaster` ≥60s clean — random passphrase + salt; assert no panic, deterministic re-derivation, output length 64
- Race: `go test -race` clean

**Agent Prompt:**

```
You are implementing SDD-01 of the hush project.

Pre-reads (ALL mandatory, in this order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles III, VIII, IX, XI)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-2, FR-3, FR-6, AC-7)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/keys section)
- /Users/mrz/projects/hush/docs/SECURITY.md (Layer 1 + Layer 5)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md (§2 fuzz target list)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md (current state)

Workflow:
1. /speckit-specify "internal/keys: Argon2id + BIP32 HD derivation"
2. /speckit-plan
3. /speckit-tasks
4. TDD-implement
5. magex format:fix && magex lint && magex test:race
6. go test -fuzz=FuzzDeriveMaster -fuzztime=60s ./internal/keys/
7. Append "Exported API — locked at SDD-01" section to docs/PACKAGE-MAP.md (under internal/keys)
8. Update docs/AC-MATRIX.md row for AC-7 with this chunk's test files
9. Update docs/SDD-PLAYBOOK.md status to done for SDD-01
10. Commit: feat(keys): Argon2id KDF + BIP32 HD derivation (SDD-01)

Scope:
- Package: internal/keys
- Files: derive.go, paths.go, client.go, fingerprint.go, derive_test.go, paths_test.go, client_test.go, derive_fuzz_test.go
- Exported API:
  - func DeriveMasterSeed(ctx context.Context, passphrase, salt []byte) ([]byte, error)
  - func DeriveJWTSigningKey(seed []byte) (*ecdsa.PrivateKey, error)
  - func DeriveVaultEncKey(seed []byte) ([]byte, error)
  - func DeriveAuditSigningKey(seed []byte) (*ecdsa.PrivateKey, error)
  - func DeriveClientKey(seed []byte, machineIndex uint32) (*ecdsa.PrivateKey, error)
  - func PublicKeyFingerprint(pub *ecdsa.PublicKey) string
  - var ErrPassphraseTooShort, ErrSaltMissing (sentinel errors per Principle IX)

Inputs: none (foundation chunk).

Behaviour contracts (MUST):
- Use github.com/bitcoinschema/go-bitcoin/v2 for secp256k1 + BIP32; golang.org/x/crypto/argon2 for KDF
- Argon2id params: time=4, memory=256*1024 KiB (256 MB), threads=4, keyLen=64 — NON-NEGOTIABLE
- Reject passphrase < 12 bytes with ErrPassphraseTooShort BEFORE invoking Argon2id
- Reject salt != 16 bytes with ErrSaltMissing
- context.Context first parameter on every exported function
- All derivations deterministic given same inputs

Anti-contracts (MUST NOT):
- Write any file
- Log the passphrase, the seed, the salt, or any derived key (Constitution X)
- Use math/rand
- Import internal/* (this is leaf)
- Convert []byte secret material to string anywhere in this package

Tests (write FIRST):
- Unit (table-driven, deterministic): TestDeriveMasterSeed_Deterministic, TestDeriveMasterSeed_RejectsShortPassphrase, TestDeriveMasterSeed_RejectsBadSalt, TestDeriveJWTSigningKey_Path, TestDeriveClientKey_MachineIndexIsolation, TestPublicKeyFingerprint_Stable
- Fuzz: FuzzDeriveMaster — random passphrase (≥12 bytes) + 16-byte salt; assert no panic, deterministic re-derivation, output length 64
- Race: go test -race ./internal/keys/

Acceptance criteria: AC-7 (Bitcoin crypto: this chunk delivers BIP32 derivation only).

Constitutional principles in scope: III (Layer 1), VIII (100% coverage), IX (idiomatic Go), X (no logging secrets), XI (no new crypto deps beyond go-bitcoin + golang.org/x/crypto)

Coverage target: 100% (security-critical per Constitution VIII).

Post a final checklist confirming: tests pass with -race, fuzz ran 60s clean, coverage = 100%, docs/PACKAGE-MAP.md updated, docs/AC-MATRIX.md updated, docs/SDD-PLAYBOOK.md updated, branch pushed.
```

---

## SDD-02 — `internal/vault/securebytes` (mlocked memory + zero-on-destroy)

**Phase:** 1
**Package:** `internal/vault/securebytes`
**Files:** `securebytes.go`, `securebytes_darwin.go`, `securebytes_linux.go`, `securebytes_test.go`
**Branch:** `002-securebytes`
**Blocked by:** none (parallel-safe with SDD-01)
**Blocks:** SDD-03, SDD-07, SDD-13, SDD-16, SDD-21
**Primary AC:** AC-7 (Layer 5 — secure memory)
**Coverage target:** 100%

**Behaviour contracts (MUST):**
- `SecureBytes` type wrapping `[]byte` with `mlock`; zero-on-`Destroy`; runtime finalizer
- `slog.LogValuer` returning `slog.StringValue("[redacted]")` (Constitution X — type-driven redaction)
- `fmt.Stringer` returning `"[redacted]"`
- `json.Marshaler` returning `[]byte("[redacted]")`
- Borrow-checked access via `Use(func(b []byte))` only — no exposed `Bytes()` accessor

**Anti-contracts (MUST NOT):**
- Expose underlying `[]byte` directly
- Allow construction from `string`
- Use cgo (Constitution IX — `CGO_ENABLED=0`)

**Tests:**
- Unit: zeroing behaviour, redaction in slog/fmt/json, double-Destroy idempotency, post-Destroy use returns ErrDestroyed, Use scope-bounding, finalizer triggers on GC
- Sentinel-leak: log a SecureBytes wrapping `SECRET_SHOULD_NEVER_APPEAR_2`; assert absent from output
- Race: TestSecureBytes_ConcurrentUse with `-race`

**Agent Prompt:**

```
You are implementing SDD-02 of the hush project.

Pre-reads (ALL mandatory):
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles III, VIII, X, XI)
- /Users/mrz/projects/hush/docs/SECURITY.md (Layer 5 + Go runtime known limitation)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/vault — securebytes subpackage)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md (§5 redaction tests)

Workflow:
1. /speckit-specify "internal/vault/securebytes: mlocked secure memory"
2. /speckit-plan
3. /speckit-tasks
4. TDD-implement (darwin + linux build-tagged files)
5. magex format:fix && magex lint && magex test:race
6. Append "Exported API — locked at SDD-02" to docs/PACKAGE-MAP.md
7. Update docs/AC-MATRIX.md
8. Update docs/SDD-PLAYBOOK.md status
9. Commit: feat(vault/securebytes): mlocked secure memory + redaction (SDD-02)

Scope:
- Package: internal/vault/securebytes
- Files: securebytes.go (cross-platform API + finalizer), securebytes_darwin.go, securebytes_linux.go, securebytes_test.go
- Exported API:
  - type SecureBytes (opaque)
  - func New(b []byte) (*SecureBytes, error)
  - func (sb *SecureBytes) Use(fn func(b []byte)) error
  - func (sb *SecureBytes) Len() int
  - func (sb *SecureBytes) Destroy() error
  - func (sb *SecureBytes) LogValue() slog.Value
  - func (sb *SecureBytes) String() string
  - func (sb *SecureBytes) MarshalJSON() ([]byte, error)
  - var ErrDestroyed

Inputs: none (parallel with SDD-01).

Behaviour contracts (MUST):
- mlock backing memory on construction; munlock on Destroy
- Zero on Destroy AND on runtime.SetFinalizer trigger
- Use(fn) is the ONLY read path; the []byte handed to fn must NOT escape
- Constructor accepts ONLY []byte; zero the input []byte immediately after copy
- LogValue/String/MarshalJSON all return "[redacted]"

Anti-contracts (MUST NOT):
- Provide string-accepting constructor
- Provide Bytes() accessor
- Use unsafe outside the OS-specific mlock wrappers
- Use cgo (CGO_ENABLED=0)

Tests (TDD):
- Unit: zeroing, redaction in slog/fmt/json, double-Destroy idempotency, post-Destroy ErrDestroyed, Use scope-bounding
- Sentinel-leak: log a SecureBytes wrapping "SECRET_SHOULD_NEVER_APPEAR_2"; capture slog output via slogtest; assert sentinel absent
- Race: TestSecureBytes_ConcurrentUse — N goroutines calling Use, race detector clean
- Finalizer: TestSecureBytes_FinalizerZerosOnGC — force GC, verify zeroing triggered

Acceptance criteria: AC-7 (Layer 5).

Constitutional principles: III (Layer 5), VIII (100%), IX (idiomatic), X (type-driven redaction), XI (CGO=0).

Coverage target: 100%.

Final checklist must include: redaction sentinel test passed, mlock works on darwin AND linux, finalizer triggers in TestSecureBytes_FinalizerZerosOnGC.
```

---

## SDD-03 — `internal/vault` (HUSH file format + AES-256-GCM + atomic write)

**Phase:** 1
**Package:** `internal/vault`
**Files:** `file.go`, `codec.go`, `store.go`, `permissions.go`, `*_test.go`, `vault_fuzz_test.go`
**Branch:** `003-vault-format`
**Blocked by:** SDD-01, SDD-02
**Blocks:** SDD-10, SDD-13, SDD-17, SDD-25
**Primary AC:** AC-2 (vault round-trip + SIGHUP reload)
**Coverage target:** 100%; **fuzz target #1** (Constitution VIII)

**Behaviour contracts (MUST):**
- HUSH binary format per `docs/SPEC.md` FR-2: 4-byte magic `HUSH`, 1-byte version `0x01`, 16-byte salt, 12-byte AES-GCM nonce, ciphertext+tag
- Plaintext payload `[]Secret{Name, Value (SecureBytes), Description}` — JSON encode/decode without `string` materialisation of secret values
- `Save`: write to `<path>.tmp` → fsync → atomic rename → mode `0600`; verify parent dir mode `0700`
- `Load`: enforce file mode `0600` and parent `0700`; refuse laxer
- Sentinel-typed errors (`ErrBadMagic`, `ErrBadVersion`, `ErrShortHeader`, `ErrAuthFailed`, `ErrFilePermsLoose`)

**Anti-contracts (MUST NOT):**
- Allow secret values through `string` (custom JSON unmarshaller mandatory)
- Log secret names alongside values
- Persist intermediate plaintext to disk during `Save`

**Tests:**
- Unit: round-trip with N=0,1,5,500 secrets; wrong-passphrase → ErrAuthFailed; truncated file at every header boundary; loose file mode → refuse; atomic save observation
- Fuzz: `FuzzVaultDecode` ≥60s clean — random byte stream; assert no panic, no >50MB alloc, every error typed
- Sentinel-leak: TestVault_NoLeakInError — pack `SECRET_SHOULD_NEVER_APPEAR_3`; trigger ErrAuthFailed; assert absent from `err.Error()` and logs
- Race: `TestStore_ConcurrentGet` — 100 goroutines, race detector clean

**Agent Prompt:**

```
You are implementing SDD-03 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles III, VIII, X, XI; Security Requirements table)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-2, FR-10, FR-15, AC-2)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/vault)
- /Users/mrz/projects/hush/docs/SECURITY.md (Encryption at Rest, Layer 5 SecureBytes integration)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md (§2 fuzz target #1)

Workflow: standard speckit cycle, magex gates, `go test -fuzz=FuzzVaultDecode -fuzztime=60s`. Append locked API to docs/PACKAGE-MAP.md. Update docs/AC-MATRIX.md row for AC-2.

Scope:
- Package: internal/vault
- Files: file.go (HUSH header struct + parse/write), codec.go (AES-256-GCM seal/open), store.go (in-memory Store with Get/Names/Destroy), permissions.go (file/dir mode checks), tests + vault_fuzz_test.go
- Exported API:
  - type Secret struct { Name, Description string; Value *securebytes.SecureBytes }
  - type Store interface { Get(name string) (*securebytes.SecureBytes, error); Names() []string; Destroy() error }
  - func Load(ctx context.Context, path string, vaultKey *securebytes.SecureBytes) (Store, error)
  - func Save(ctx context.Context, path string, vaultKey *securebytes.SecureBytes, secrets []Secret) error
  - var ErrBadMagic, ErrBadVersion, ErrShortHeader, ErrAuthFailed, ErrFilePermsLoose, ErrSecretNotFound

Inputs (verify exist): SDD-01 internal/keys, SDD-02 internal/vault/securebytes.

Behaviour contracts (MUST):
- HUSH magic = []byte{0x48, 0x55, 0x53, 0x48}; version byte = 0x01
- 16-byte salt, 12-byte AES-GCM nonce — both crypto/rand on Save
- Plaintext payload format: JSON array of {name, value, description}; value is base64-encoded ON THE WIRE only, decoded into SecureBytes via custom UnmarshalJSON that bypasses string allocation
- Save: write to <path>.tmp same dir → fsync → os.Rename → set mode 0600 → verify parent 0700
- Load: O_RDONLY; stat; refuse if mode != 0600 OR parent != 0700
- Store.Get returns a NEW SecureBytes (copy from internal storage) so callers can Destroy independently

Anti-contracts (MUST NOT):
- Use string for secret values (custom JSON unmarshaller is mandatory)
- Persist plaintext to disk
- Skip fsync before rename
- Log secret values OR full secret names+values together
- Vendor dependencies (Constitution XI: no /vendor)

Tests (write first):
- Unit: TestVault_RoundTrip_{0,1,5,500}Secrets, TestVault_LoadWrongPass_ReturnsAuthFailed, TestVault_LoadTruncatedAtMagic_ShortHeader, TestVault_LoadTruncatedAtSalt_ShortHeader, TestVault_LoadTruncatedAtNonce_ShortHeader, TestVault_LoadTruncatedCiphertext_AuthFailed, TestVault_LoadLooseFileMode_PermsLoose, TestVault_LoadLooseParentMode_PermsLoose, TestVault_SaveAtomic_NoIntermediate, TestVault_SaveSetsMode0600
- Fuzz: FuzzVaultDecode — go test -fuzz=FuzzVaultDecode -fuzztime=60s; goal: no panic, no >50MB alloc, every error typed
- Sentinel-leak: TestVault_NoLeakInError — pack SECRET_SHOULD_NEVER_APPEAR_3; trigger ErrAuthFailed via wrong key; assert sentinel absent from err.Error() AND captured log
- Race: TestStore_ConcurrentGet — 100 goroutines, race detector clean

Acceptance criteria: AC-2 (vault round-trip; SIGHUP reload is SDD-10).

Constitutional principles: III (Layer 5 + Encryption at Rest), VIII (fuzz target #1, 100% coverage), X (redaction discipline), XI (no /vendor, CGO=0).

Coverage target: 100%.

Final checklist: fuzz 60s clean, coverage 100%, sentinel-leak passed, locked API appended to PACKAGE-MAP.md, AC-MATRIX.md row updated for AC-2, SDD-PLAYBOOK.md status updated.
```

---

## SDD-04 — `internal/testutil` (test fixtures + sentinel helpers + harness primitives)

**Phase:** 1
**Package:** `internal/testutil`
**Files:** `vault_fixture.go`, `keys_fixture.go`, `sentinel.go`, `discord_stub.go`, `*_test.go`
**Branch:** `004-testutil`
**Blocked by:** SDD-01, SDD-02, SDD-03
**Blocks:** SDD-25 + every server/cli/supervisor test chunk
**Primary AC:** indirect support for AC-9
**Coverage target:** 80% (test infra)

**Agent Prompt:**

```
You are implementing SDD-04 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (VIII)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md (§3 layout, §5 sentinel pattern)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (no entry yet — you create one)

Workflow: standard speckit cycle. Append a NEW internal/testutil entry to docs/PACKAGE-MAP.md. Update SDD-PLAYBOOK.md status.

Scope:
- Package: internal/testutil
- Files: vault_fixture.go, keys_fixture.go, sentinel.go, discord_stub.go, doc.go, *_test.go
- Exported API:
  - func NewTestVault(t *testing.T, secrets map[string]string) (path string, vaultKey *securebytes.SecureBytes, cleanup func())
  - func NewTestKeys(t *testing.T) (masterSeed []byte)  // deterministic
  - func SentinelSecret(n int) string
  - func AssertSentinelAbsent(t *testing.T, sentinel, haystack string)
  - type DiscordStub struct { ApproveAll bool; Calls []ApprovalCall; ... }
  - func NewDiscordStub() *DiscordStub
  - (DiscordStub satisfies the future internal/discord.Approver interface — define a minimal local interface here that SDD-11 will widen)

Inputs: SDD-01, SDD-02, SDD-03.

Behaviour contracts (MUST):
- Every fixture uses t.Cleanup so leaks fail loudly
- DiscordStub supports per-test scenario programming: ApproveAll=true → auto-approve; or programmable per-call response queue
- Deterministic seed for NewTestKeys uses hardcoded "hush-test-seed-NEVER-USE-IN-PROD" passphrase + salt — never used outside tests

Anti-contracts (MUST NOT):
- Create files outside t.TempDir()
- Persist any state between tests

Tests: unit only.

Acceptance: indirect support for AC-9.

Coverage target: 80%.

Final checklist: PACKAGE-MAP.md has new internal/testutil entry; all fixtures t.Cleanup-safe.
```

---

## SDD-05 — `internal/logging` (slog setup + redaction enforcement)

**Phase:** 1
**Package:** `internal/logging`
**Files:** `logger.go`, `redact.go`, `redact_patterns.go`, `*_test.go`
**Branch:** `005-logging`
**Blocked by:** SDD-02
**Blocks:** every chunk thereafter
**Primary AC:** indirect (Principle X)
**Coverage target:** 95%

**Agent Prompt:**

```
You are implementing SDD-05 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principle X, IX)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/logging)
- /Users/mrz/projects/hush/docs/OPERATIONS.md (logging tier definitions)

Workflow: standard speckit cycle. Append locked API to docs/PACKAGE-MAP.md. Update SDD-PLAYBOOK.md.

Scope:
- Package: internal/logging
- Files: logger.go, redact.go, redact_patterns.go, *_test.go
- Exported API:
  - type Options struct { Level slog.Level; Format Format; Out io.Writer }
  - type Format int  // FormatAuto, FormatText, FormatJSON
  - func New(opts Options) *slog.Logger
  - func RedactString(s string) string  // pattern-based backstop
  - var RedactPatterns []*regexp.Regexp

Inputs: SDD-02 (uses securebytes.LogValuer behaviour).

Behaviour contracts (MUST):
- Use stdlib log/slog only (no logrus / zap — Constitution XI)
- TTY detection via golang.org/x/term (allowed dep)
- ReplaceAttr handler chain: (1) call LogValuer on values, (2) RedactString string values as backstop
- Default level INFO
- JSON format adds source location for ERROR; text format never does

Anti-contracts (MUST NOT):
- Mutate global slog.Default
- Print to stderr unless opts.Out specifies it

Tests:
- Unit: TestNew_TTYDetectionPicksText, TestNew_NonTTYPicksJSON, TestRedactPattern_AnthropicKey, TestRedactPattern_GitHubPAT, TestRedactPattern_AWSAccessKey
- Sentinel-leak: log a SecureBytes wrapping SECRET_SHOULD_NEVER_APPEAR_5 via the configured logger; capture output; assert absent

Acceptance: supports Principle X.

Coverage target: 95%.

Final checklist: TTY detection works; sentinel test passes; regex patterns from docs/SECURITY.md threat-model row are all covered.
```

---

## SDD-06 — `internal/config` (server TOML schema + validation)

**Phase:** 1
**Package:** `internal/config`
**Files:** `server.go`, `defaults.go`, `validate.go`, `paths.go`, `*_test.go`, `server_fuzz_test.go`
**Branch:** `006-config-server`
**Blocked by:** SDD-05
**Blocks:** SDD-10, SDD-15
**Primary AC:** AC-1, AC-8
**Coverage target:** 95%; **fuzz target #5** (TOML parse)

**Agent Prompt:**

```
You are implementing SDD-06 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles VI, VIII, IX)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md (server config — entire section)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-8, FR-15, AC-8)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/config)

Workflow: speckit cycle, magex gates, fuzz 60s, append locked API + AC-1/AC-8 row + SDD-PLAYBOOK update.

Scope:
- Package: internal/config
- Files: server.go (struct), defaults.go (constants), validate.go (rule engine), paths.go (filesystem checks), server_test.go, validate_test.go, server_fuzz_test.go
- Exported API:
  - type Server struct { ... fields per docs/CONFIG-SCHEMA.md ... }
  - func LoadServer(ctx context.Context, path string) (*Server, error)
  - func (s *Server) Validate() error
  - var DefaultArgonTime, DefaultArgonMemoryMB, DefaultArgonThreads, ... (typed constants)
  - var ErrTailscaleBindRequired, ErrPathPrefixInvalid, ErrStateDirUnsafe, ErrSupervisorTTLOutOfRange, ... (sentinels)

Inputs: SDD-05.

Behaviour contracts (MUST):
- github.com/pelletier/go-toml/v2 with Decoder.DisallowUnknownFields(true)
- Validate listen_addr by parsing into netip.Addr; reject IsLoopback / IsUnspecified / public; allow only Tailscale CGNAT (100.64.0.0/10) per Constitution VI
- Refuse argon_memory_mb < 256 (Constitution III non-negotiable)
- Build absolute paths from state_dir; reject audit_log paths outside state_dir
- All errors are typed sentinels

Anti-contracts (MUST NOT):
- Read from environment variables (Constitution Security Requirements)
- Store any secret in the Server struct (bot token fetched from Keychain in SDD-10)
- Allow listen_addr 0.0.0.0 ever

Tests:
- Unit: per-field positive + negative; full minimal config + full maximal config
- Fuzz: FuzzServerTOML — feed go-fuzz random bytes; assert no panic, every error path produces a typed error
- Coverage: 95%

Acceptance: AC-1, AC-8.

Constitutional principles: VI, VIII, IX, X.

Final checklist: every default in docs/CONFIG-SCHEMA.md is asserted; fuzz 60s clean; AC-MATRIX rows for AC-1/AC-8 updated; SDD-PLAYBOOK status updated.
```

---

# Phase 2 — Session and transport core

---

## SDD-07 — `internal/token` (ES256K JWT issuance + validation + store)

**Phase:** 2
**Package:** `internal/token`
**Files:** `claims.go`, `issue.go`, `validate.go`, `store.go`, `revoke.go`, `alg_es256k.go`, `*_test.go`, `validate_fuzz_test.go`
**Branch:** `007-token-jwt`
**Blocked by:** SDD-01, SDD-02, SDD-06
**Blocks:** SDD-12, SDD-13, SDD-23
**Primary AC:** AC-4
**Coverage target:** 100%; **fuzz target #2** (JWT parse/validate)

**Agent Prompt:**

```
You are implementing SDD-07 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles III, IV, VIII)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-4, FR-9, AC-4)
- /Users/mrz/projects/hush/docs/SECURITY.md (Layer 2, JWT claims table)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/token)

Workflow: speckit cycle, magex gates, fuzz 60s, append locked API + AC-4 row + SDD-PLAYBOOK.

Scope:
- Package: internal/token
- Files: claims.go (Claims struct + SessionType enum), issue.go (Issue + custom ES256K method registration via sync.Once), validate.go, store.go, revoke.go, alg_es256k.go (signing method), *_test.go, validate_fuzz_test.go
- Exported API:
  - type SessionType string  // SessionInteractive, SessionSupervisor
  - type Claims struct { jwt.RegisteredClaims; Scope []string; ClientIP string; RequestID string; MaxUses int; EphemeralPubKey string; SessionType SessionType }
  - type Token struct { JTI string; Encoded string; ExpiresAt time.Time; SessionType SessionType; MaxUses int }
  - type Store interface { Add(t *Token) error; Get(jti string) (*Token, error); ConsumeUse(jti string) error; Revoke(jti string) error; Cleanup(ctx context.Context) }
  - func NewStore() Store
  - func Issue(ctx context.Context, signKey *ecdsa.PrivateKey, params IssueParams) (*Token, error)
  - func Validate(ctx context.Context, encoded string, verifyKey *ecdsa.PublicKey, store Store, requestIP string, requestedSecret string) (*Claims, error)
  - var ErrAlgorithmUnsupported, ErrTokenRevoked, ErrTokenExhausted, ErrIPMismatch, ErrScopeViolation, ErrUnknownSessionType, ErrTokenExpired

Inputs: SDD-01, SDD-02, SDD-06.

Behaviour contracts (MUST):
- Register ES256K method via jwt.RegisterSigningMethod ONCE via sync.Once gated by a Register() function called by Issue/Validate (NOT init() — Constitution IX bans init())
- Reject jwt.SigningMethodNone and any non-ES256K alg explicitly
- crypto/rand for jti (UUIDv4)
- Validate's IP comparison uses netip.Addr equality
- Store uses sync.RWMutex; ConsumeUse decrements atomically (or returns ErrTokenExhausted) — race tests must pass

Anti-contracts (MUST NOT):
- Use init()
- Use mutable package globals (sync.Once is the bounded exception)
- Cache verify keys globally — accept as parameter
- Log encoded JWT strings

Tests:
- Unit (every claim validation branch): TestIssue_Interactive, TestIssue_Supervisor, TestValidate_HappyPath, TestValidate_ExpiredJWT, TestValidate_WrongIP, TestValidate_OutOfScope, TestValidate_AlgConfusion_None_Refused, TestValidate_AlgConfusion_HS256_Refused, TestValidate_UnknownSessionType_Refused, TestStore_RevokedJTI_Refused, TestStore_ExhaustedInteractive_Refused, TestStore_SupervisorIgnoresMaxUses, TestStore_CleanupRemovesExpired
- Fuzz: FuzzJWTValidate — random JWT-shaped bytes; assert no panic
- Race: TestStore_ConcurrentDecrement (multiple goroutines decrementing max_uses on the same jti — exactly N decrements observed, no double-decrement)

Acceptance: AC-4.

Coverage target: 100%.

Final checklist: alg-confusion attacks rejected (none, HS256 — both tested), supervisor TTL-only behaviour proven, race detector clean, fuzz 60s clean.
```

---

## SDD-08 — `internal/transport/sign` (ECDSA canonical-JSON request signing + nonce + timestamp)

**Phase:** 2
**Package:** `internal/transport/sign`
**Files:** `canonical.go`, `sign.go`, `verify.go`, `nonce.go`, `timestamp.go`, `*_test.go`, `verify_fuzz_test.go`
**Branch:** `008-transport-sign`
**Blocked by:** SDD-01
**Blocks:** SDD-12, SDD-16
**Primary AC:** AC-7 (Layer 4)
**Coverage target:** 100%; **fuzz target #4** (request signature payload)

**Agent Prompt:**

```
You are implementing SDD-08 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (III Layer 4, VIII)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-6, AC-7)
- /Users/mrz/projects/hush/docs/SECURITY.md (Layer 4, request replay protection)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/transport)

Workflow: speckit cycle, magex gates, fuzz 60s, append locked API + AC-7 row + SDD-PLAYBOOK.

Scope:
- Package: internal/transport/sign
- Files: canonical.go, sign.go, verify.go, nonce.go, timestamp.go, *_test.go, verify_fuzz_test.go
- Exported API:
  - func CanonicalJSON(v any) ([]byte, error)
  - func Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error)
  - func Verify(ctx context.Context, key *ecdsa.PublicKey, payload, sig []byte) error
  - type NonceCache interface { Add(ctx context.Context, nonce string, ttl time.Duration) (firstSeen bool, err error); Run(ctx context.Context) }
  - func NewNonceCache() NonceCache
  - func IsFreshTimestamp(ts time.Time, skew time.Duration) bool
  - var ErrSignatureInvalid, ErrNonceReplay, ErrTimestampStale

Inputs: SDD-01.

Behaviour contracts (MUST):
- CanonicalJSON sorts keys alphabetically at every depth; uses json.RawMessage for already-canonical chunks; rejects NaN/Inf
- Sign: go-bitcoin Bitcoin-style ECDSA over SHA-256(canonical)
- NonceCache backed by sync.Map + sweep goroutine; goroutine started by Run(ctx) with explicit cancellation per Constitution IX
- IsFreshTimestamp uses time.Now() (testable via injectable clock)

Anti-contracts (MUST NOT):
- Use stdlib encoding/json without sorting (gotcha: stdlib does NOT sort map keys for json.Marshal of struct — but DOES for map[string]any; tests must cover both)
- Start any goroutine without an explicit Run(ctx) entry point
- Log nonces

Tests:
- Unit: canonicalisation determinism (10 known shapes), sign+verify round-trip, wrong-key rejection, nonce-replay rejection, expired-nonce-allowed-after-sweep, timestamp-too-old, timestamp-future
- Fuzz: FuzzVerifyRequest — random JSON + signature; assert no panic, errors typed
- Race: nonce cache concurrent Add (N goroutines, exactly one returns firstSeen=true for the same nonce)

Acceptance: AC-7 (Layer 4).

Coverage target: 100%.

Final checklist: canonicalisation matches the example in docs/SECURITY.md Layer 4; nonce cache race-clean; fuzz 60s clean.
```

---

## SDD-09 — `internal/transport/ecies` (ECIES encrypt/decrypt for secret responses)

**Phase:** 2
**Package:** `internal/transport/ecies`
**Files:** `ecies.go`, `*_test.go`, `decrypt_fuzz_test.go`
**Branch:** `009-transport-ecies`
**Blocked by:** SDD-02
**Blocks:** SDD-13, SDD-16
**Primary AC:** AC-7 (Layer 3)
**Coverage target:** 100%; **fuzz target #3** (ECIES decrypt input)

**Agent Prompt:**

```
You are implementing SDD-09 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (III Layer 3, VIII)
- /Users/mrz/projects/hush/docs/SECURITY.md (Layer 3)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-5, AC-7)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/transport)

Workflow: speckit cycle, magex gates, fuzz 60s, append locked API + AC-7 row + SDD-PLAYBOOK.

Scope:
- Package: internal/transport/ecies
- Files: ecies.go, ecies_test.go, decrypt_fuzz_test.go
- Exported API:
  - func Encrypt(ctx context.Context, recipientPub *ecdsa.PublicKey, plaintext []byte) ([]byte, error)
  - func Decrypt(ctx context.Context, recipientPriv *ecdsa.PrivateKey, envelope []byte) (*securebytes.SecureBytes, error)
  - var ErrECIESDecryptFailed, ErrECIESEnvelopeTooShort

Inputs: SDD-02.

Behaviour contracts (MUST):
- Implementation uses go-bitcoin ECIES helpers
- Encrypt's plaintext input is zeroed by caller; this package zeroes its OWN intermediate buffers
- Decrypt returns a fresh SecureBytes; caller owns Destroy()
- Errors are typed; never include any byte from envelope or plaintext in err.Error()

Anti-contracts (MUST NOT):
- Accept string plaintext
- Return []byte plaintext (must be SecureBytes-wrapped)
- Cache or memoize keys

Tests:
- Unit: round-trip with multiple sizes (1B, 1KB, 1MB), wrong-key fails, mangled envelope fails, empty plaintext rejected
- Fuzz: FuzzECIESDecrypt — random envelope bytes; assert no panic
- Sentinel-leak: TestECIES_NoLeakOnError — encrypt SECRET_SHOULD_NEVER_APPEAR_9; mangle envelope; assert sentinel absent from err.Error()

Acceptance: AC-7 (Layer 3).

Coverage target: 100%.

Final checklist: round-trip across sizes, fuzz 60s clean, sentinel-leak passed.
```

---

# Phase 3 — Server control plane

---

## SDD-10 — `internal/server` (router + middleware + startup checks + SIGHUP atomic reload + lifecycle)

**Phase:** 3
**Package:** `internal/server`
**Files:** `server.go`, `router.go`, `middleware.go`, `startup_checks.go`, `reload.go`, `*_test.go`
**Branch:** `010-server-skeleton`
**Blocked by:** SDD-03, SDD-05, SDD-06, SDD-07, SDD-08, SDD-09
**Blocks:** SDD-12, SDD-13, SDD-14
**Primary AC:** AC-1, AC-2, AC-8
**Coverage target:** 95%

**Agent Prompt:**

```
You are implementing SDD-10 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (III, VI, VIII, IX, X; Security Requirements)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-8, FR-10, FR-15, FR-16, AC-1, AC-2, AC-8)
- /Users/mrz/projects/hush/docs/API.md (full)
- /Users/mrz/projects/hush/docs/ARCHITECTURE.md (server lifecycle)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/server)

Workflow: speckit cycle, magex gates, append locked API + AC-1/2/8 rows + SDD-PLAYBOOK update.

Scope:
- Package: internal/server
- Files: server.go (Server struct + lifecycle), router.go (stdlib http.ServeMux at /h/<prefix>/...), middleware.go, startup_checks.go (clock/perms/bind), reload.go (SIGHUP handler), *_test.go, integration_test.go
- Exported API:
  - type Server struct { ... }
  - type Deps struct { Cfg *config.Server; VaultPtr *atomic.Pointer[vault.Store]; TokenStore token.Store; Approver discord.Approver; Logger *slog.Logger; ... }
  - func New(deps Deps) (*Server, error)
  - func (s *Server) Run(ctx context.Context) error
  - func (s *Server) ReloadVault(ctx context.Context, newPath string, key *securebytes.SecureBytes) error  // SIGHUP entry

Inputs: SDD-03, SDD-05, SDD-06, SDD-07.

Behaviour contracts (MUST):
- Use net/http (Constitution XI: stdlib first); router is stdlib http.ServeMux for v0.1.0
- atomic.Pointer[vault.Store] for SIGHUP-safe swap — old store's Destroy is called 30s after swap to allow in-flight requests to drain
- Startup check execution order is: clock_sync → file_modes → tailscale_bind → state_dir; refuse to start on first failure with explicit error
- Approver interface placeholder; SDD-11 swaps in real Discord-backed Approver
- Recover middleware logs panic with stack but never includes request body

Anti-contracts (MUST NOT):
- Bind to 0.0.0.0 ever
- Allow init() functions
- Hold a Context in a struct field (Constitution IX)

Tests:
- Unit: TestStartupChecks_RefusesPublicBind, TestStartupChecks_RefusesLooseFileMode, TestStartupChecks_RefusesUnsyncedClock, TestMiddleware_RequestIDStable, TestMiddleware_IPAllowListBlocks
- Integration (//go:build integration): TestSIGHUP_AtomicReload (start server with vault A → SIGHUP with vault B → in-flight request sees A, new request sees B → vault A zeroed)
- Race: TestVaultPointerSwap_NoRace

Acceptance: AC-1, AC-2 (SIGHUP), AC-8 (startup hardening).

Coverage target: 95%.

Final checklist: all startup checks have positive + negative tests; SIGHUP reload integration test green with -race; locked API appended; AC-MATRIX + SDD-PLAYBOOK updated.
```

---

## SDD-11 — `internal/discord` (Approver interface + bot connection + disconnect monitoring)

**Phase:** 3
**Package:** `internal/discord`
**Files:** `bot.go`, `approver.go`, `monitor.go`, `render.go`, `ratelimit.go`, `*_test.go`
**Branch:** `011-discord-bot`
**Blocked by:** SDD-05, SDD-06, SDD-10
**Blocks:** SDD-12, SDD-28
**Primary AC:** AC-3
**Coverage target:** 85%

**Agent Prompt:**

```
You are implementing SDD-11 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (II, V, VIII, IX, X)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-7, FR-19, AC-3)
- /Users/mrz/projects/hush/docs/SECURITY.md (Discord trust boundary, bot disconnect monitoring)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md ([discord] section + supervisor_dm_rate_limit)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/discord)

Workflow: speckit cycle, magex gates, append locked API + AC-3 row + SDD-PLAYBOOK.

Scope:
- Package: internal/discord
- Files: bot.go, approver.go (interface + BotApprover impl), monitor.go (WebSocket health), render.go (DM templates), ratelimit.go, *_test.go
- Exported API:
  - type Approver interface { RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error) }
  - type ApprovalRequest struct { MachineName, ClientIP, Reason string; Scope []string; RequestedTTL time.Duration; SessionType token.SessionType; SupervisorName string }
  - type Decision struct { Approved bool; ApprovedTTL time.Duration; Reason string }
  - type BotApprover struct { ... }
  - func NewBotApprover(ctx context.Context, cfg BotConfig, logger *slog.Logger) (*BotApprover, error)
  - var ErrDiscordUnavailable, ErrApprovalDenied, ErrApprovalTimeout, ErrRateLimited

Inputs: SDD-05, SDD-06.

Behaviour contracts (MUST):
- BotConfig fields: Token *securebytes.SecureBytes, OwnerID string, AppID string, AuditChannelID string (optional), DMRateLimit time.Duration
- Bot token read once via internal/keychain wrapper, immediately wrapped in SecureBytes
- Use github.com/bwmarrin/discordgo for connection + interaction handlers
- DM rendering: distinct visual labels for INTERACTIVE vs [DAEMON]
- RequestApproval blocks until user clicks Approve/Deny OR ctx times out OR ErrDiscordUnavailable
- WebSocket unexpected close → ErrDiscordUnavailable (caller maps to 503)
- Rate limiter is per-(supervisor name + machine fingerprint) keyed; default 1 per 5 min

Anti-contracts (MUST NOT):
- Read bot token from env var
- Auto-approve under any circumstance (Constitution II non-negotiable)
- Use init()
- Hold ctx in struct field

Tests:
- Unit: TestApprovalRender_InteractiveLabel, TestApprovalRender_DaemonLabel, TestRateLimit_BlocksSecondPromptWithin5Min, TestDecisionRouting_ApproveDenyTimeout (uses fake discordgo.Session; no live Discord)
- Race: monitor goroutine race-clean

Acceptance: AC-3.

Coverage target: 85%.

Final checklist: bot token never present as string in any test fixture; locked API appended; AC-3 row updated.
```

---

## SDD-12 — Server `/claim` handler

**Phase:** 3
**Package:** `internal/server`
**Files:** `claim_handler.go`, `claim_handler_test.go`, `claim_handler_integration_test.go`
**Branch:** `012-server-claim-handler`
**Blocked by:** SDD-07, SDD-08, SDD-10, SDD-11
**Blocks:** SDD-13, SDD-25
**Primary AC:** AC-1, AC-3, AC-4
**Coverage target:** 95%

**Agent Prompt:**

```
You are implementing SDD-12 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (II, IV, VIII, X)
- /Users/mrz/projects/hush/docs/API.md (POST /claim spec)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-7, FR-9, FR-19, AC-1, AC-3, AC-4)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (Scenario 1, Scenario 10)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (internal/server)

Workflow: speckit cycle, magex gates, append AC-1/3/4 rows + SDD-PLAYBOOK.

Scope:
- Package: internal/server
- Files: claim_handler.go, claim_handler_test.go, claim_handler_integration_test.go (//go:build integration)
- Handler is registered via Server.RegisterHandlers entry point defined in SDD-10.

Inputs: SDD-07, SDD-08, SDD-10, SDD-11.

Behaviour contracts (MUST):
- Validate request: JSON shape per docs/API.md; canonicalise → verify signature → check nonce/timestamp → check IP allowlist
- Cap TTL to config-defined max per session_type
- Call Approver.RequestApproval; map decision:
  - Approve → token.Issue + 200 with JSON {jwt, expires_at, jti}
  - Deny → 403 {error: "denied"}
  - Timeout → 408 {error: "approval_timeout"}
  - ErrDiscordUnavailable → 503 {error: "discord_unavailable"} (Constitution II — fail closed, no auto-approve fallback)
- Audit event for every outcome
- Error responses: no echoing of request body fields beyond request_id

Anti-contracts (MUST NOT):
- Fall back to auto-approve on Discord error
- Include nonce or signature in error responses
- Log JWT contents

Tests:
- Unit: TestClaim_BadSignature_403, TestClaim_NonceReplay_403, TestClaim_StaleTimestamp_403, TestClaim_DiscordTimeout_408, TestClaim_DiscordUnavailable_503, TestClaim_Approved_IssuesJWT, TestClaim_SupervisorRequest_DaemonLabel
- Integration: full flow with DiscordStub from SDD-04
- Sentinel-leak: TestClaim_ErrorBodyNoSentinel — build a request with reason=SECRET_SHOULD_NEVER_APPEAR_12; force ErrSignatureInvalid; assert sentinel absent from response body and logs

Acceptance: AC-1, AC-3, AC-4.

Coverage target: 95%.

Final checklist: 503 on Discord unavailable proven; sentinel-leak passed; AC-MATRIX + SDD-PLAYBOOK updated.
```

---

## SDD-13 — Server `/s`, `/revoke`, `/hz` handlers + audit log

**Phase:** 3
**Package:** `internal/server` (handlers) + `internal/audit` (new)
**Files:** `internal/server/{secret_handler.go,revoke_handler.go,health_handler.go,*_test.go}`; `internal/audit/{chain.go,writer.go,discord_mirror.go,*_test.go}`
**Branch:** `013-server-handlers-and-audit`
**Blocked by:** SDD-09, SDD-12
**Blocks:** SDD-25, SDD-28
**Primary AC:** AC-1, AC-2, AC-4, AC-7
**Coverage target:** server handlers 95%; audit chain 100%

**Agent Prompt:**

```
You are implementing SDD-13 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (III Layer 6, IV, VIII, X)
- /Users/mrz/projects/hush/docs/API.md (GET /s, POST /revoke, GET /hz)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-9, FR-14, FR-17, AC-1, AC-2, AC-4, AC-7)
- /Users/mrz/projects/hush/docs/SECURITY.md (Layer 6 audit chain)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md ([server] discord_audit_channel_id, audit_log)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (Scenario 7, Scenario 13)

Workflow: speckit cycle, magex gates, append locked API for internal/audit + AC-1/2/4/7 rows + SDD-PLAYBOOK.

Scope:
- Packages: internal/server (handlers), internal/audit (new)
- Files: internal/server/{secret_handler.go,revoke_handler.go,health_handler.go,*_test.go}; internal/audit/{chain.go,writer.go,discord_mirror.go,*_test.go}
- Exported API (audit):
  - type Event struct { Seq uint64; Time time.Time; Action string; Data map[string]any; PrevHash, Hash, Signature string }
  - type Writer interface { Append(ctx context.Context, action string, data map[string]any) error; Run(ctx context.Context) error }
  - func NewWriter(ctx context.Context, path string, signKey *ecdsa.PrivateKey, mirror *DiscordMirror, logger *slog.Logger) (Writer, error)
  - type DiscordMirror struct { ... }
  - var ErrAuditChainBroken

Inputs: SDD-01, SDD-07, SDD-09, SDD-10, SDD-11.

Behaviour contracts (MUST):
- /s/<name> handler validates token via token.Validate (with scope=name and IP=remote); decrements max_uses for interactive
- /s response body is the raw ECIES envelope (Content-Type: application/octet-stream)
- /revoke handler accepts a signed JSON body {jti, timestamp, nonce, signature}; verify with same client key registry as /claim
- /hz returns {status:"ok", uptime, secrets_count, active_tokens, discord_connected}; reachable WITHOUT JWT (G3 trust: Tailscale only)
- Audit Writer: single goroutine, buffered chan, every event hash-chained + signed; canonicalise data via SDD-08's CanonicalJSON before hashing
- Audit DiscordMirror is best-effort: log WARN on mirror failure, never block append

Anti-contracts (MUST NOT):
- Return decrypted secret in any error path
- Place secret name in audit data alongside its value (only the name)
- Drop audit events under backpressure (block instead)

Tests:
- Unit: TestSecret_HappyPath_ECIESPayload, TestSecret_ExpiredJWT_401, TestSecret_OutOfScope_403, TestSecret_WrongIP_401, TestSecret_ExhaustedInteractive_401, TestSecret_SupervisorIgnoresMaxUses, TestRevoke_HappyPath, TestRevoke_BadSignature_403, TestHealth_NoAuth_OK, TestAuditChain_HashLinkContiguous, TestAuditChain_SignatureValid, TestAuditChain_BreakDetectedOnTamper
- Sentinel-leak: TestSecret_ErrorBodyNoSentinel, TestAudit_RecordNoSecretValue
- Race: audit writer concurrent writes — exactly N records with monotonic seq

Acceptance: AC-1, AC-2 (vault round-trip via /s), AC-4 (revoke + token enforcement), AC-7 (ECIES on the wire).

Coverage target: 95% (server handlers); 100% (audit chain — security-critical hash-chain integrity).

Final checklist: ECIES end-to-end verified in unit test; audit chain tamper test breaks correctly; AC-MATRIX + SDD-PLAYBOOK updated.
```

---

## SDD-14 — `internal/cli` root + global flags + (`serve`, `health`, `version`, `revoke`)

**Phase:** 3
**Package:** `internal/cli` + `cmd/hush`
**Files:** `cmd/hush/main.go`; `internal/cli/{root.go,serve.go,health.go,version.go,revoke.go,output.go,flags.go,exit_codes.go,*_test.go}`
**Branch:** `014-cli-root-and-server-cmds`
**Blocked by:** SDD-10, SDD-11, SDD-12, SDD-13
**Blocks:** SDD-15, SDD-16, SDD-17, SDD-23
**Primary AC:** AC-1
**Coverage target:** 85%

**Agent Prompt:**

```
You are implementing SDD-14 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles VII, IX)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-1, FR-16, FR-17, FR-21, AC-1)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (cmd/hush + internal/cli)
- /Users/mrz/projects/hush/docs/API.md (GET /hz)

Workflow: speckit cycle, magex gates, append locked API + AC-1 row + SDD-PLAYBOOK.

Scope:
- Files: cmd/hush/main.go; internal/cli/{root.go,serve.go,health.go,version.go,revoke.go,output.go,flags.go,exit_codes.go,*_test.go}
- Subcommands implemented in this chunk: serve, health, version, revoke

Inputs: SDD-10..SDD-13.

Behaviour contracts (MUST):
- cobra root command name "hush"; global flags --config, --verbose, --quiet, --no-color
- Output: TTY → text; non-TTY → JSON; --no-color forces no ANSI
- ExitCode constants: ExitOK=0, ExitErr=1, ExitInputErr=2, ExitAuth=3, ExitNotFound=4, ExitPerm=5, ExitConfigStale=78
- "hush serve" passphrase resolution per docs/SPEC.md FR-16: stdin pipe → TTY prompt → fail
- "hush health" handles connection refused with explicit message + ExitErr
- "hush revoke" requires --server and --jti; signs request via SDD-08

Anti-contracts (MUST NOT):
- Use viper (Constitution VII)
- Read passphrase from env var
- Print secret values

Tests:
- Unit: flag wiring, output formatter, exit code mapping
- Integration: TestServe_StartAndShutdown (start in goroutine, SIGTERM, expect clean exit)

Acceptance: AC-1.

Coverage target: 85%.

Final checklist: all four subcommands runnable via `hush <cmd> --help`; build version injection works; AC-MATRIX + SDD-PLAYBOOK updated.
```

---

# Phase 4 — Interactive CLI path

---

## SDD-15 — `hush init` (server + client modes; macOS Keychain ACL integration)

**Phase:** 4
**Package:** `internal/cli` + `internal/keychain`
**Files:** `internal/cli/init.go`, `internal/keychain/{keychain.go,keychain_darwin.go,keychain_linux.go,*_test.go}`
**Branch:** `015-init-and-keychain`
**Blocked by:** SDD-01, SDD-03, SDD-14
**Blocks:** SDD-16, SDD-29
**Primary AC:** AC-1, AC-6
**Coverage target:** 85%

**Agent Prompt:**

```
You are implementing SDD-15 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles I, III, VII; Security Requirements Keychain ACLs)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-3, FR-22, AC-1, AC-6)
- /Users/mrz/projects/hush/docs/SECURITY.md (Keychain ACLs)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md (server defaults)

Workflow: speckit cycle, magex gates, append AC-1/6 rows + SDD-PLAYBOOK.

Scope:
- internal/cli/init.go (server + client subcommands)
- internal/keychain/* (new package for OS keychain wrapper with ACL support)
- Files: keychain.go (interface), keychain_darwin.go (security CLI shellout), keychain_linux.go (zalando/go-keyring wrapper), *_test.go

Inputs: SDD-01, SDD-03, SDD-14.

Behaviour contracts (MUST):
- Passphrase ≥ 12 chars (Constitution Security Requirements)
- Salt is crypto/rand 16 bytes
- Server mode writes config.toml mode 0600 with all defaults from docs/CONFIG-SCHEMA.md
- Bot token + vault passphrase stored via `security add-generic-password -s hush-discord -a hush -T /usr/local/bin/hush -w <token>` (or test-injectable equivalent — for tests, an in-process fake Keychain)
- Client mode requires --machine-index flag; conflicts with server mode
- Print public key fingerprint to stdout in copy-pasteable format

Anti-contracts (MUST NOT):
- Read passphrase from env var or arg
- Skip the ACL flag
- Generate a passphrase for the user

Tests:
- Unit: TestInitServer_RefusesShortPassphrase, TestInitServer_CreatesVaultWith0600, TestInitClient_RequiresMachineIndex, TestInitClient_StoresInKeychainWithACL (skip-if-not-darwin)
- Integration: full init dance in tempdir
- macOS-specific tests skip on linux via build tags

Acceptance: AC-1, AC-6.

Coverage target: 85%.

Final checklist: init twice in tempdir produces deterministic tree structure; Keychain ACL flag verified by inspecting the generated security command.
```

---

## SDD-16 — `hush request` (interactive; ECIES decrypt; --exec injection)

**Phase:** 4
**Package:** `internal/cli`
**Files:** `internal/cli/request.go`, `internal/cli/exec.go`, `*_test.go`
**Branch:** `016-cli-request`
**Blocked by:** SDD-08, SDD-09, SDD-13, SDD-15
**Blocks:** SDD-25
**Primary AC:** AC-5, AC-6
**Coverage target:** 90%

**Agent Prompt:**

```
You are implementing SDD-16 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (I, IV, VII)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-1 (request), FR-22, AC-5, AC-6)
- /Users/mrz/projects/hush/docs/SECURITY.md (--format eval warning rationale)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (Scenario 1)

Workflow: speckit cycle, magex gates, append AC-5/6 rows + SDD-PLAYBOOK.

Scope: internal/cli/request.go, internal/cli/exec.go, *_test.go.

Inputs: SDD-08, SDD-09, SDD-13, SDD-15.

Behaviour contracts (MUST):
- Flags: --server, --scope (csv), --reason, --ttl, --exec, --format (eval only), --max-uses
- Read client signing key from Keychain (SDD-15) via internal/keychain
- Build canonical-JSON claim (SDD-08), sign, POST /claim, await response
- On approval: for each scope name fetch /s/<name>, ECIES-decrypt (SDD-09), wrap in SecureBytes
- --exec path: build child env from SecureBytes (use SecureBytes.Use(fn) to copy bytes into child env at exec syscall time), exec.Cmd.Run, propagate exit code
- --format eval: print export NAME='%s' for each secret (single quotes, escape ' inside the value); also emit a stderr WARNING per Constitution VII
- Neither flag: error + ExitInputErr

Anti-contracts (MUST NOT):
- Write secret values to disk (no cache files, no temp files)
- Print secret values to stdout unless --format eval is explicit
- Cache JWT to disk

Tests:
- Unit: TestRequest_RequiresExecOrFormat, TestRequest_FormatEvalEmitsStderrWarning, TestRequest_ExecInjectsEnvVars, TestRequest_PostExecZeroesEphemeralKey
- Integration: full flow with DiscordStub.ApproveAll
- Sentinel-leak: TestRequest_ExecOnlyChildHasSecret — child process echoes env, parent's logs assert sentinel absent

Acceptance: AC-5, AC-6.

Coverage target: 90%.

Final checklist: --format eval warning verified in stderr; sentinel-leak passed (child has it, parent doesn't); ECIES round-trip integration test green.
```

---

## SDD-17 — `hush secret` (add/remove/list/rotate; interactive TTY enforcement)

**Phase:** 4
**Package:** `internal/cli`
**Files:** `internal/cli/secret.go`, `*_test.go`
**Branch:** `017-cli-secret`
**Blocked by:** SDD-03, SDD-15
**Blocks:** SDD-25
**Primary AC:** AC-1, AC-2
**Coverage target:** 85%

**Agent Prompt:**

```
You are implementing SDD-17 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles VII, X; Security Requirements management commands)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-10, AC-1, AC-2)
- /Users/mrz/projects/hush/docs/SECURITY.md ("Rogue process runs hush secret add")

Workflow: speckit cycle, magex gates, append AC-1/2 rows + SDD-PLAYBOOK.

Scope: internal/cli/secret.go (subcommands add, remove, list, rotate), *_test.go.

Inputs: SDD-03, SDD-15.

Behaviour contracts (MUST):
- All write subcommands refuse if stdin is not a TTY (golang.org/x/term.IsTerminal)
- Hidden input via term.ReadPassword (no echo)
- list output: text "NAME — description" or JSON [{name, description}]
- rotate: signal PID via syscall.Kill(pid, SIGHUP) if PID file present at <state_dir>/hush.pid; tolerate missing PID file

Anti-contracts (MUST NOT):
- Accept value via flag (--value foo)
- Read value from stdin pipe
- Print secret values

Tests:
- Unit: TestSecret_AddRefusesPipedStdin, TestSecret_AddTTYHappyPath, TestSecret_ListNoValues, TestSecret_RotateAtomic, TestSecret_RotateSendsSIGHUP

Acceptance: AC-1, AC-2.

Coverage target: 85%.

Final checklist: piped-stdin refusal proven on darwin AND linux; SIGHUP delivery integration-tested with a fake server.
```

---

# Phase 5 — Supervisor lifecycle

---

## SDD-18 — `internal/supervise/config` (per-supervisor TOML schema + validation)

**Phase:** 5
**Package:** `internal/supervise/config`
**Files:** `config.go`, `defaults.go`, `validate.go`, `*_test.go`, `config_fuzz_test.go`
**Branch:** `018-supervise-config`
**Blocked by:** SDD-06
**Blocks:** SDD-19, SDD-21, SDD-23, SDD-29, SDD-30
**Primary AC:** AC-10
**Coverage target:** 95%; **fuzz target #5** (TOML parse)

**Agent Prompt:**

```
You are implementing SDD-18 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (IV, V, VIII)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md (Supervisor Config File — entire section)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-11, AC-10)

Workflow: speckit cycle, magex gates, fuzz 60s, append locked API + AC-10 row + SDD-PLAYBOOK.

Scope: internal/supervise/config — config.go, defaults.go, validate.go, *_test.go, config_fuzz_test.go.

Inputs: SDD-06.

Behaviour contracts (MUST):
- go-toml/v2 with DisallowUnknownFields
- All fields per docs/CONFIG-SCHEMA.md (root + [child] + [discord] + [validators] + [watchdog])
- Validator names limited to {anthropic, anthropic-oauth, openai, google-ai, github}
- grace.window <= 4h
- refresh_window format "HH:MM-HH:MM" with start < end
- command first element absolute path

Anti-contracts (MUST NOT):
- Allow unknown validator names (silent ignore is wrong — explicit error)
- Skip refresh_window validation

Tests: full unit + fuzz 60s.

Acceptance: AC-10.

Coverage target: 95%.

Final checklist: every default in docs/CONFIG-SCHEMA.md is asserted; fuzz 60s clean.
```

---

## SDD-19 — `internal/supervise` state machine + transitions + store

**Phase:** 5
**Package:** `internal/supervise`
**Files:** `state.go`, `*_test.go`
**Branch:** `019-supervise-state`
**Blocked by:** SDD-07, SDD-18
**Blocks:** SDD-20, SDD-21, SDD-25
**Primary AC:** AC-10
**Coverage target:** 95%

**Agent Prompt:**

```
You are implementing SDD-19 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (IV, V, VIII, IX, X)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-11)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (state diagrams in Scenarios 2..15)

Workflow: speckit cycle, magex gates, append AC-10 rows + SDD-PLAYBOOK.

Scope: internal/supervise/state.go, state_test.go.

Inputs: SDD-07, SDD-18.

Behaviour contracts (MUST):
- type State string with constants StateFetching, StateRunning, StateAwaitingApproval, StateGraceRestart, StateStopped
- type Store struct with mu sync.RWMutex
- Transition(ctx, event Event) error — table-driven; rejects illegal transitions with ErrInvalidTransition
- Snapshot() returns defensive copy for status socket use
- Token field is *securebytes.SecureBytes wrapping the encoded JWT

Anti-contracts (MUST NOT):
- Allow caller to read mutable internal fields directly
- Spin a goroutine here (state is data only)

Tests: every transition + race + snapshot defensiveness.

Acceptance: AC-10.

Coverage target: 95%.

Final checklist: state-table matrix matches docs/LIFECYCLE-SCENARIOS.md; race detector clean.
```

---

## SDD-20 — `internal/supervise/child` (fork/exec + signal forwarding + exit-78 + process-group death-watch)

**Phase:** 5
**Package:** `internal/supervise`
**Files:** `child.go`, `child_darwin.go`, `child_linux.go`, `*_test.go`
**Branch:** `020-supervise-child`
**Blocked by:** SDD-19
**Blocks:** SDD-21, SDD-25
**Primary AC:** AC-10
**Coverage target:** 90%

**Agent Prompt:**

```
You are implementing SDD-20 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (IV, IX)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-11, AC-10)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (Scenarios 2, 3, 4, 5)

Workflow: speckit cycle, magex gates, append AC-10 row + SDD-PLAYBOOK.

Scope: internal/supervise/child.go (cross-platform), child_darwin.go (kqueue death-watch), child_linux.go (Pdeathsig), *_test.go.

Inputs: SDD-19.

Behaviour contracts (MUST):
- os/exec; SysProcAttr.Setpgid=true; PR_SET_PDEATHSIG=SIGTERM on linux
- Signal forwarding via dedicated goroutine started in Start; cancellable via ctx
- Exit78 constant; Wait returns (exitCode int, signal syscall.Signal, err error)
- stdout/stderr pipes have a 64KB ring; if watchdog isn't consuming, drop oldest

Anti-contracts (MUST NOT):
- Use shell parsing (no /bin/sh -c); cmd[0] must be absolute path
- Cache child handles after Wait

Tests:
- Unit: TestChild_StartAndWait_HappyPath, TestChild_Exit78Detection, TestChild_SignalForwardingSIGTERM, TestChild_PgidIsolation_KillingPgKillsChildren, TestChild_StdoutPipeNonBlocking
- Race: TestChild_ConcurrentWaitOK

Acceptance: AC-10.

Coverage target: 90%.

Final checklist: exit-78 detection on darwin AND linux; pgid isolation prevents orphan children; signal forwarding integration-tested.
```

---

## SDD-21 — `internal/supervise` refill + refresh + grace cache

**Phase:** 5
**Package:** `internal/supervise`
**Files:** `refill.go`, `refresh.go`, `grace.go`, `*_test.go`
**Branch:** `021-supervise-refill-refresh`
**Blocked by:** SDD-09, SDD-13, SDD-19
**Blocks:** SDD-23, SDD-25
**Primary AC:** AC-10
**Coverage target:** 95%

**Agent Prompt:**

```
You are implementing SDD-21 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (IV, V, VIII)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-11, AC-10)
- /Users/mrz/projects/hush/docs/SECURITY.md (§6 grace-window tradeoff)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (Scenarios 3, 8, 9, 11)
- /Users/mrz/projects/hush/docs/DAEMONS.md (refresh window tuning, grace tradeoff)

Workflow: speckit cycle, magex gates, append AC-10 row + SDD-PLAYBOOK.

Scope: refill.go, refresh.go, grace.go, *_test.go.

Inputs: SDD-09, SDD-13, SDD-19.

Behaviour contracts (MUST):
- Refill: GET /s/<name> for each scope using cached JWT; ECIES-decrypt; if any returns 401-unknown-jti → state→awaiting-approval; else hand to child
- Refresh: cron-like scheduler within configured window; T-30 fallback if window passed and TTL near expiry
- Grace: holds last-decrypted set in *securebytes.SecureBytes per secret name; expires after grace.window with Destroy
- Boot retry: try connect to server; on failure exp-backoff; cap total at boot_retry_timeout; never burn Discord prompts during boot
- DM rate limit: per-supervisor token bucket, default 1/5min

Anti-contracts (MUST NOT):
- Convert cached secrets to string at any point
- Use grace cache when grace.cache_secrets_for_restart=false
- Schedule refresh outside the configured window without T-30 fallback active

Tests:
- Unit: TestRefill_SilentOnCleanExit, TestRefill_401UnknownJTITransitions, TestRefresh_FiresInWindow, TestRefresh_T30MinFallback, TestGrace_UsesCacheOnExpiredJWT, TestGrace_TTLCapAt4h, TestBootRetry_BackoffRespected, TestDMRateLimit_DropsExcess
- Race: refresh scheduler clean

Acceptance: AC-10.

Coverage target: 95%.

Final checklist: scenarios 3, 8, 9, 11 each have a passing unit test; race-clean.
```

---

## SDD-22 — `internal/supervise` pidfile + status socket

**Phase:** 5
**Package:** `internal/supervise`
**Files:** `pidfile.go`, `socket.go`, `socket_darwin.go`, `socket_linux.go`, `*_test.go`
**Branch:** `022-supervise-pidfile-socket`
**Blocked by:** SDD-19
**Blocks:** SDD-23, SDD-25
**Primary AC:** AC-10
**Coverage target:** 95%

**Agent Prompt:**

```
You are implementing SDD-22 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (V)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-11, FR-12, AC-10)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md (status_socket, pid_file)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (Scenarios 12, 14)

Workflow: speckit cycle, magex gates, append AC-10 row + SDD-PLAYBOOK.

Scope: pidfile.go, socket.go, socket_darwin.go, socket_linux.go, *_test.go.

Inputs: SDD-19.

Behaviour contracts (MUST):
- PID file via golang.org/x/sys flock (LOCK_EX|LOCK_NB)
- Socket at platform-correct path; mode 0600; parent dir mode 0700 created if needed
- Status response is exactly the JSON shape in docs/SPEC.md FR-12
- Socket server graceful shutdown on ctx cancel

Anti-contracts (MUST NOT):
- Use HTTP-on-localhost (Constitution V — FS perms are the auth)
- Add bearer-token auth on the socket
- Allow non-root agent processes to bind without 0600 enforcement

Tests:
- Unit: TestPidFile_FlockExclusive, TestPidFile_DuplicateRefused, TestSocket_Mode0600, TestSocket_ParentMode0700, TestSocket_StatusJSONShape

Acceptance: AC-10.

Coverage target: 95%.

Final checklist: socket path correct on both OSes; mode enforcement proven.
```

---

## SDD-23 — `hush supervise` + `hush client status` + `hush client refresh` CLI

**Phase:** 5
**Package:** `internal/cli`
**Files:** `internal/cli/supervise.go`, `internal/cli/client.go`, `*_test.go`
**Branch:** `023-cli-supervise-and-client`
**Blocked by:** SDD-14, SDD-18, SDD-19, SDD-20, SDD-21, SDD-22
**Blocks:** SDD-25
**Primary AC:** AC-10
**Coverage target:** 85%

**Agent Prompt:**

```
You are implementing SDD-23 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (IV, V, VII)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-11, FR-12, FR-22, AC-10)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md (Supervisor Config File)
- /Users/mrz/projects/hush/docs/DAEMONS.md (status socket usage)

Workflow: speckit cycle, magex gates, append AC-10 row + SDD-PLAYBOOK.

Scope: internal/cli/supervise.go (supervise subcommand orchestrator), internal/cli/client.go (status + refresh subcommands), *_test.go.

Inputs: SDD-14, SDD-18..SDD-22.

Behaviour contracts (MUST):
- supervise wires together state, child, refill/refresh/grace, pidfile, socket (orchestrator only — no business logic added here)
- --dry-run prints rendered /claim payload and exits 0
- --grace-window override flag takes precedence over config; --no-cache forces strict mode
- client status: TTY → human summary; pipe → JSON (auto)
- client refresh: send "refresh" command to the supervisor Unix socket; receive ack/error

Anti-contracts (MUST NOT):
- Move business logic from internal/supervise here
- Add per-OS branches in this file (delegate to internal/supervise/{darwin,linux})

Tests:
- Unit: flag wiring, dry-run path, status output formatting
- Integration: dry-run round-trip with a fake supervisor config and DiscordStub

Acceptance: AC-10.

Coverage target: 85%.

Final checklist: dry-run produces machine-parseable output; client status pretty-print readable.
```

---

## SDD-24 — (reserved orchestration glue; default skipped)

> Reserved for orchestration glue if SDD-25's lifecycle harness reveals seams. Initial assumption: skip; re-evaluate after SDD-25 surfaces gaps. Mark closed/skipped during execution if unnecessary.

**Status:** skipped by default
**Coverage target:** N/A

---

## SDD-25 — Lifecycle integration harness (15 scenarios; explicit AC-10 owner)

**Phase:** 5
**Package:** `tests/integration/`
**Files:** `tests/integration/{lifecycle_test.go, scenarios_test.go, harness/*.go}` (build-tagged `//go:build integration`)
**Branch:** `025-lifecycle-harness`
**Blocked by:** ALL of SDD-01..SDD-23
**Blocks:** SDD-31
**Primary AC:** AC-9, AC-10 (the 15 lifecycle scenarios — explicit owner)
**Coverage target:** 15/15 scenarios green; suite < 120s on developer laptop

**Agent Prompt:**

```
You are implementing SDD-25 of the hush project.

This chunk owns AC-10 (15 lifecycle scenarios). It is the largest test deliverable in the project. Plan carefully.

Pre-reads (MANDATORY in full):
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles VIII non-negotiable; Acceptance Criteria → required test types matrix)
- /Users/mrz/projects/hush/docs/SPEC.md (AC-10 specifically)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (Scenarios 1–15 — entire doc)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md (§5 sentinel pattern, §7 lifecycle scenario tests)
- /Users/mrz/projects/hush/docs/DAEMONS.md (48h walkthrough)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md (every internal/* — you wire all of them)

Workflow: speckit cycle (this is a "feature" with 15 sub-tasks). magex test:race -tags=integration must pass. Append AC-9/AC-10 rows pointing to this suite as the authoritative test path. Update SDD-PLAYBOOK.

Scope:
- tests/integration/harness/{vault.go, supervisor.go, discord.go, child.go} — reusable test harness
- tests/integration/scenarios_test.go — 15 named tests
- All under //go:build integration

Inputs: SDD-01..SDD-23.

Behaviour contracts (MUST):
- Each scenario is a single test function named Test_Scenario_NN_<slug>
- Harness uses internal/testutil (SDD-04) for vault fixtures, sentinel helpers, Discord stub
- Every scenario asserts:
  1. Final supervisor/server state matches expected
  2. Audit log records expected events in expected order
  3. Status socket JSON matches expected shape (when supervisor scenario)
  4. AssertSentinelAbsent on all captured logs
- Scenarios 1–15 from docs/LIFECYCLE-SCENARIOS.md — all 15 implemented

Anti-contracts (MUST NOT):
- Hit any external network (Discord, Anthropic, etc.) — all mocked
- Skip a scenario due to "complexity"
- Use t.Parallel inside a scenario that mutates shared state

Acceptance: AC-9 (test infra completeness), AC-10 (15 scenarios).

Coverage target: 15/15 scenarios green; suite runs in <120s on a developer laptop.

Final checklist: 15/15 green with -race, no flake on 5 consecutive runs, AC-MATRIX rows updated to reference each scenario test by name.
```

---

# Phase 6 — Validators + alerts

---

## SDD-26 — `internal/supervise/validators` (interface + 5 builtins)

**Phase:** 6
**Package:** `internal/supervise/validators`
**Files:** `validators.go`, `anthropic.go`, `anthropic_oauth.go`, `openai.go`, `google_ai.go`, `github.go`, `*_test.go`
**Branch:** `026-validators-builtins`
**Blocked by:** SDD-21
**Blocks:** SDD-25 (some scenarios), SDD-27, SDD-28
**Primary AC:** AC-10 (FR-13)
**Coverage target:** 90%

**Agent Prompt:**

```
You are implementing SDD-26 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (V, VIII)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-13, AC-10)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (Scenario 6)
- /Users/mrz/projects/hush/docs/DAEMONS.md (validator authoring)

Workflow: speckit cycle, magex gates, append AC-10 row + SDD-PLAYBOOK.

Scope: internal/supervise/validators package; one file per provider; tests use httptest.Server fixtures.

Inputs: SDD-02 (SecureBytes), SDD-21 (registry consumer).

Behaviour contracts (MUST):
- Validator interface uses SecureBytes (no string), copies into ephemeral []byte via Use(fn) at HTTP call time, then immediately zeroes the buffer
- Registry: Get(name string) returns the validator by config name
- Timeout default 5s, configurable via NewWithClient
- Errors are typed: ErrStaleCredential (401/403), ErrValidatorTimeout, ErrValidatorNetwork

Anti-contracts (MUST NOT):
- Hit live provider APIs in tests
- Log secret value or bearer header
- Run validators on the vault server

Tests: full per-provider list; net/http/httptest based.

Acceptance: AC-10 (FR-13).

Coverage target: 90%.

Final checklist: 5 validators wired; sentinel-leak tests confirm no value in error messages.
```

---

## SDD-27 — `internal/supervise/watchdog` (log-pattern alert-only)

**Phase:** 6
**Package:** `internal/supervise`
**Files:** `watchdog.go`, `*_test.go`
**Branch:** `027-watchdog`
**Blocked by:** SDD-20
**Blocks:** SDD-28
**Primary AC:** AC-10
**Coverage target:** 90%

**Agent Prompt:**

```
You are implementing SDD-27 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (V)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-11, AC-10)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md ([watchdog])
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (Scenario 15)

Workflow: speckit cycle, magex gates, append AC-10 row + SDD-PLAYBOOK.

Scope: internal/supervise/watchdog.go, watchdog_test.go.

Inputs: SDD-20.

Behaviour contracts (MUST):
- Goroutine started via Run(ctx) — explicit cancellation (Constitution IX)
- Pattern matching via regexp (compiled once, reused)
- Rate limit per-pattern token bucket
- Emit alert via the alert channel (typed Event sent to SDD-28's channel)

Anti-contracts (MUST NOT):
- Trigger any state transition (alert-only)
- Drop alerts silently — log WARN when rate-limited

Tests: full unit list above.

Acceptance: AC-10.

Coverage target: 90%.

Final checklist: rate limit tested; never-restart proven.
```

---

## SDD-28 — `internal/discord/alerts` (8 alert classes + tiered routing + DM rate limit + refresh nudges)

**Phase:** 6
**Package:** `internal/discord/alerts`
**Files:** `alerts.go`, `templates.go`, `ratelimit.go`, `*_test.go`
**Branch:** `028-discord-alerts`
**Blocked by:** SDD-11, SDD-27
**Blocks:** SDD-25 (alert assertions)
**Primary AC:** AC-3, AC-10
**Coverage target:** 90%

**Agent Prompt:**

```
You are implementing SDD-28 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (V, X)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md (Required Alert Classes section)
- /Users/mrz/projects/hush/docs/OPERATIONS.md (alert tiers)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-7, AC-3, AC-10)

Workflow: speckit cycle, magex gates, append AC-3/10 rows + SDD-PLAYBOOK.

Scope: internal/discord/alerts/{alerts.go, templates.go, ratelimit.go, *_test.go}.

Inputs: SDD-11, SDD-27.

Behaviour contracts (MUST):
- type Alert struct { Class AlertClass; Tier Tier; ... fields }
- 8 named AlertClass constants (per docs/LIFECYCLE-SCENARIOS.md "Required Alert Classes")
- 3 Tier constants: TierCritical, TierWarning, TierInfo
- Templates: distinct label prefixes; format string per class with named-field placeholders
- Routing: Critical → DM owner; Warning → audit channel; Info → audit log only (no Discord call)
- Rate limiters per supervisor and per pattern

Anti-contracts (MUST NOT):
- Auto-promote a Warning to Critical
- Skip rate-limit for any class

Tests: render snapshot per class, tier routing correct, rate-limit blocks excess.

Acceptance: AC-3, AC-10.

Coverage target: 90%.

Final checklist: all 8 classes have a sample DM rendered; rate limit asserted.
```

---

# Phase 7 — Deployment

---

## SDD-29 — Deploy artifacts (launchd plist, systemd unit, install.sh, generic supervisor launcher template)

**Phase:** 7
**Package:** `deploy/`
**Files:** `deploy/{hush.plist, hush.service, install.sh, supervise-launch.sh.template}`
**Branch:** `029-deploy-artifacts`
**Blocked by:** SDD-15, SDD-23
**Blocks:** SDD-30, SDD-32
**Primary AC:** AC-1, AC-6, AC-10
**Coverage target:** N/A (smoke test only)

**Agent Prompt:**

```
You are implementing SDD-29 of the hush project (open-source release).

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles I, IV, XI)
- /Users/mrz/projects/hush/docs/OPERATIONS.md (deployment topology, runbooks)
- /Users/mrz/projects/hush/docs/SECURITY.md (Keychain ACLs)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-11 — supervisor pattern is for daemons, NOT hush request --exec)
- /Users/mrz/projects/hush/docs/DAEMONS.md (multi-daemon pattern)

Workflow: speckit cycle (mostly tasks list — minimal spec/plan), magex gates, append AC-1/6/10 rows + SDD-PLAYBOOK.

Scope: deploy/{hush.plist, hush.service, install.sh, supervise-launch.sh.template}.

Inputs: SDD-15, SDD-23.

Behaviour contracts (MUST):
- install.sh idempotent
- install.sh adds tmutil exclusion on macOS (Principle XI — vault is ephemeral, never backed up)
- Keychain entries use `-T /usr/local/bin/hush` ACL
- launchd plist + systemd unit BOTH set non-root user
- supervise-launch.sh.template execs `hush supervise` (NOT `hush request --exec`); placeholders `<NAME>` / `<KEYCHAIN_ITEM>` are clearly marked for operator substitution

Anti-contracts (MUST NOT):
- Use `hush request --exec` for daemons (would re-prompt on every restart — defeats Principle IV)
- Skip the tmutil exclusion (Principle XI non-negotiable)
- Run as root
- Hard-code any operator's specific daemon names in committed files

Tests:
- bash -n parsing
- shellcheck clean (if shellcheck available)
- install.sh runs idempotently in tempdir

Acceptance: AC-1, AC-6, AC-10.

Final checklist: tmutil addexclusion present in install.sh; supervise-launch.sh.template uses `hush supervise` (no hush request --exec for daemons); template placeholders are clearly marked; no operator-specific names in any committed file.
```

---

## SDD-30 — Generic example supervisor TOML + Tailscale ACL + clean-machine checklist

**Phase:** 7
**Package:** `deploy/examples/` + `docs/`
**Files:** `deploy/examples/supervisors/example-daemon.toml`, `docs/TAILSCALE-ACLS.md` (already created), `docs/CLEAN-MACHINE.md` (already created)
**Branch:** `030-examples-and-tailscale`
**Blocked by:** SDD-18, SDD-29
**Blocks:** SDD-32
**Primary AC:** AC-6, AC-8, AC-10
**Coverage target:** N/A (config + docs)

**Agent Prompt:**

```
You are implementing SDD-30 of the hush project (open-source release).

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (I, VI)
- /Users/mrz/projects/hush/docs/CONFIG-SCHEMA.md (Supervisor Config File)
- /Users/mrz/projects/hush/docs/SPEC.md (FR-11)
- /Users/mrz/projects/hush/docs/DAEMONS.md (multi-daemon pattern)
- /Users/mrz/projects/hush/docs/TAILSCALE-ACLS.md (existing operator-agnostic ACL guide)
- /Users/mrz/projects/hush/docs/CLEAN-MACHINE.md (existing operator-agnostic checklist)

Workflow: speckit cycle (config only — TOML), append AC-6/8/10 rows + SDD-PLAYBOOK.

Scope: deploy/examples/supervisors/example-daemon.toml (canonical generic template).

Inputs: SDD-18, SDD-29.

Behaviour contracts (MUST):
- example-daemon.toml is fully commented, fully generic; uses placeholder secret names like EXAMPLE_API_KEY_1
- example-daemon.toml validates against SDD-18 loader as-is
- Reference docs/TAILSCALE-ACLS.md and docs/CLEAN-MACHINE.md from the example's comments

Anti-contracts (MUST NOT):
- Hard-code any operator's specific secret names, daemon names, hostnames, or Tailscale tags
- Reference any private/internal project name

Tests: TestExamples_GenericTOMLValidates (in internal/supervise/config tests).

Acceptance: AC-6, AC-8, AC-10.

Final checklist: example-daemon.toml validates; no operator-specific names committed.
```

> **Note (originator overlay, not part of OSS deliverable):** if the project originator wants project-specific supervisor configs (e.g. for an internal daemon), those belong in a private fork or sibling overlay repo, NOT in the public `hush` repo. SDD-30 ships only the generic template.

---

# Phase 8 — Release

---

## SDD-31 — Release gates (coverage + 6 fuzz + magex + go-pre-commit + govulncheck + gitleaks + CGO=0 + no /vendor)

**Phase:** 8
**Package:** CI / repo-level
**Files:** `.github/workflows/*.yml`, `.golangci.json` review, `.goreleaser.yml` review
**Branch:** `031-release-gates`
**Blocked by:** SDD-25 + every prior chunk
**Blocks:** SDD-32
**Primary AC:** AC-9
**Coverage target:** project-wide ≥ 90%; security-critical packages 100%

**Agent Prompt:**

```
You are implementing SDD-31 of the hush project.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (Principles VIII, XI; Code Quality Gates)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md (entire — coverage targets, fuzz targets, gates)
- Existing files: .github/workflows/*, .golangci.json, .goreleaser.yml

Workflow: speckit cycle, magex gates, append AC-9 row + SDD-PLAYBOOK.

Scope: CI workflows, lint config review, GoReleaser review, coverage gate, fuzz CI cron.

Inputs: SDD-01..SDD-30 (every package must already have its tests).

Behaviour contracts (MUST):
- CI matrix: macOS-arm64, ubuntu-amd64; Go 1.26
- Workflow steps: magex format:fix --check, magex lint, magex test:race, go test -fuzz on each fuzz target (cron + 30s smoke per PR), go-pre-commit, govulncheck, gitleaks, coverage report with codecov upload
- .goreleaser.yml: CGO_ENABLED=0 in env; darwin/linux × amd64/arm64
- A check that fails CI if /vendor exists
- Coverage threshold check: total ≥ 90%, security-critical pkgs = 100%

Anti-contracts (MUST NOT):
- Skip fuzz targets to make CI faster (run as cron if PR is too slow)
- Disable race detector
- Allow CGO

Tests: a green CI run is the test.

Acceptance: AC-9.

Coverage target: project-wide ≥ 90%; security-critical 100%.

Final checklist: every gate green on a sample PR; codecov badge updated; AC-MATRIX.md AC-9 row points to this workflow file.
```

---

## SDD-32 — Open-source release: README + DAEMONS + repo-level OSS files + docs polish + GoReleaser + v0.1.0 tag

**Phase:** 8
**Package:** `docs/` + repo root
**Files:** `README.md` (new), `docs/DAEMONS.md` (already created — verify), `LICENSE` (verify), `CONTRIBUTING.md` (already created — verify), `CODE_OF_CONDUCT.md` (already created — verify), `SECURITY.md` at repo root (already created — verify), `.github/ISSUE_TEMPLATE/{bug_report.md, feature_request.md}` (already created — verify), `.github/PULL_REQUEST_TEMPLATE.md` (already created — verify), `docs/{ARCHITECTURE.md, API.md, SECURITY.md, OPERATIONS.md}` polish, `.goreleaser.yml` final, git tag `v0.1.0`
**Branch:** `032-release-v010`
**Blocked by:** SDD-31
**Blocks:** none (final chunk)
**Primary AC:** AC-1
**Coverage target:** N/A

**Agent Prompt:**

```
You are implementing SDD-32 of the hush project — the v0.1.0 OSS release. This is the final chunk.

Pre-reads:
- /Users/mrz/projects/hush/.specify/memory/constitution.md (entire — final compliance check)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md (must be 100% green before tagging)
- /Users/mrz/projects/hush/docs/SDD-PLAYBOOK.md (every chunk must be closed)
- All existing /docs/* files
- /Users/mrz/projects/hush/README.md (already created — verify accuracy)
- /Users/mrz/projects/hush/CONTRIBUTING.md, CODE_OF_CONDUCT.md, SECURITY.md (already created — verify accuracy)
- /Users/mrz/projects/hush/.github/* (templates already created — verify)

Workflow: speckit cycle (verify + polish + release), final magex gates, tag v0.1.0 ONLY after AC-MATRIX is fully green.

Scope (verify and polish; most files already exist):
- README.md (verify quick-start runs on a clean VM/container)
- docs/DAEMONS.md (verify accurate)
- LICENSE (verify present at repo root; create from MIT or Apache-2.0 if missing — confirm with the project owner before committing a license choice)
- CONTRIBUTING.md, CODE_OF_CONDUCT.md, repo-root SECURITY.md (verify accurate; polish)
- .github/ templates (verify)
- docs/{ARCHITECTURE.md, API.md, SECURITY.md, OPERATIONS.md} polish (link check, version stamp, code-fence sanity, no operator-specific names)
- .goreleaser.yml final tweaks (signed checksums)
- git tag v0.1.0 + GoReleaser publish

Inputs: SDD-31 green; every prior chunk's AC-MATRIX row green.

Behaviour contracts (MUST):
- README.md follows the 15-section structure documented in /Users/mrz/.claude/plans/read-this-document-and-bright-globe.md SDD-32; quick-start tested on a fresh macOS or Linux box
- DAEMONS.md, CONTRIBUTING.md, CODE_OF_CONDUCT.md, repo-root SECURITY.md are accurate and operator-agnostic
- Repo-root SECURITY.md does NOT duplicate docs/SECURITY.md (different documents — disclosure policy vs threat model)
- All polished docs are operator-agnostic
- Pre-tag check: docs/AC-MATRIX.md has every AC-1..AC-10 row marked complete with test paths
- Pre-tag check: every fuzz target in Constitution VIII has a 60s-clean run recorded in CI logs
- v0.1.0 tag is annotated; release notes auto-generated from commit log following conventional-commits
- GoReleaser produces signed artifacts (sigstore/cosign or signed SHA256SUMS minimum)

Anti-contracts (MUST NOT):
- Make the repo public (Constitution: the project owner transitions to public manually)
- Tag v0.1.0 if any AC row is incomplete or any CI gate is red
- Commit any operator-specific names in any OSS deliverable
- Auto-publish to homebrew tap or any package index without explicit project-owner go-ahead
- Duplicate docs/SECURITY.md content into the repo-root SECURITY.md

Final checklist:
- README.md exists with all 15 sections, all links resolve, quick-start verified on a clean VM/container
- LICENSE, CONTRIBUTING.md, CODE_OF_CONDUCT.md, repo-root SECURITY.md all present and accurate
- .github/ templates committed
- DAEMONS.md complete
- All /docs/ polished, link-checked, version-stamped, operator-agnostic
- AC-MATRIX 100% green
- CI green on master
- GoReleaser dry-run produces darwin+linux × amd64+arm64 binaries with signed checksums
- v0.1.0 tag created (annotated)
- Repo still private (the project owner flips to public manually)
```

---

## End of catalog

For dependency direction visualised, see [`docs/IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md).
For the current status of each chunk, see [`docs/SDD-PLAYBOOK.md`](SDD-PLAYBOOK.md).
For AC ↔ chunk ↔ test mapping, see [`docs/AC-MATRIX.md`](AC-MATRIX.md).
