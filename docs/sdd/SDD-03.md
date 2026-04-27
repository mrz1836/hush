# SDD-03 — `internal/vault` (HUSH file format + AES-256-GCM + atomic write)

**Phase:** 1
**Package:** `internal/vault`
**Files:** `file.go`, `codec.go`, `store.go`, `permissions.go`, `*_test.go`, `vault_fuzz_test.go`
**Branch:** `003-vault-format` (created by the `before_specify` git hook)
**Blocked by:** SDD-01, SDD-02
**Blocks:** SDD-10, SDD-13, SDD-17, SDD-25
**Primary AC:** AC-2 (vault round-trip + SIGHUP reload — SDD-10 owns the reload half)
**Coverage target:** 100%; **fuzz target #1** (Constitution VIII)

**Behaviour contracts (MUST):**
- HUSH binary format per `docs/SPEC.md` FR-2: 4-byte magic `HUSH` (`0x48 0x55 0x53 0x48`), 1-byte version `0x01`, 16-byte salt, 12-byte AES-GCM nonce, ciphertext+tag
- 16-byte salt + 12-byte nonce both generated via `crypto/rand` on `Save`
- Plaintext payload format: JSON array of `{name, value, description}`; value is base64-on-the-wire only, decoded into `SecureBytes` via custom `UnmarshalJSON` that bypasses `string` allocation
- `Save`: write to `<path>.tmp` (same dir) → `fsync` → `os.Rename` → set mode `0600`; verify parent dir mode `0700`
- `Load`: `O_RDONLY`, stat, refuse if file mode `!= 0600` OR parent mode `!= 0700`
- `Store.Get` returns a NEW `SecureBytes` (copy from internal storage) so callers can `Destroy` independently
- All errors are typed sentinels

**Anti-contracts (MUST NOT):**
- Use `string` for secret values (custom JSON unmarshaller is mandatory)
- Persist intermediate plaintext to disk during `Save`
- Skip `fsync` before `rename`
- Log secret values OR full secret names+values together
- Vendor dependencies (Constitution XI: no `/vendor`)

**Tests required:**
- Unit: `TestVault_RoundTrip_{0,1,5,500}Secrets`, `TestVault_LoadWrongPass_ReturnsAuthFailed`, `TestVault_LoadTruncatedAtMagic_ShortHeader`, `TestVault_LoadTruncatedAtSalt_ShortHeader`, `TestVault_LoadTruncatedAtNonce_ShortHeader`, `TestVault_LoadTruncatedCiphertext_AuthFailed`, `TestVault_LoadLooseFileMode_PermsLoose`, `TestVault_LoadLooseParentMode_PermsLoose`, `TestVault_SaveAtomic_NoIntermediate`, `TestVault_SaveSetsMode0600`
- Fuzz: `FuzzVaultDecode` ≥60s clean — random byte stream into `Load`; assert no panic, no >50MB allocation, every error path produces a typed error
- Sentinel-leak: `TestVault_NoLeakInError` — pack `SECRET_SHOULD_NEVER_APPEAR_3`; trigger `ErrAuthFailed` via wrong key; assert sentinel absent from `err.Error()` AND captured log
- Race: `TestStore_ConcurrentGet` — 100 goroutines, race detector clean

**Constitutional principles in scope:** III (Layer 5 + Encryption at Rest), VIII (fuzz target #1, 100% coverage, TDD), X (redaction discipline), XI (no `/vendor`, `CGO=0`)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `type Secret struct { Name, Description string; Value *securebytes.SecureBytes }`
- `type Store interface { Get(name string) (*securebytes.SecureBytes, error); Names() []string; Destroy() error }`
- `func Load(ctx context.Context, path string, vaultKey *securebytes.SecureBytes) (Store, error)`
- `func Save(ctx context.Context, path string, vaultKey *securebytes.SecureBytes, secrets []Secret) error`
- `var ErrBadMagic, ErrBadVersion, ErrShortHeader, ErrAuthFailed, ErrFilePermsLoose, ErrSecretNotFound`

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. Do NOT
chain them in one session. The `extensions.yml` git hooks auto-commit
each artifact (accept in Prompts 1, 3, 4; conditionally in Prompt 2;
**decline** in Prompt 5 — Prompt 5 makes one combined commit).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-03 (internal/vault: HUSH file
format + AES-256-GCM + atomic write) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles III, VIII, X — security non-negotiables and TDD)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-2, FR-10, FR-15, AC-2)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-2 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-03.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
The internal/vault package owns the on-disk vault file: a binary
HUSH-format envelope encrypted with AES-256-GCM, written atomically,
strictly file-mode-enforced, and decoded into an in-memory Store
that hands out secrets wrapped in SecureBytes. It is consumed by
SDD-10 (server SIGHUP reload), SDD-13 (server /s handler), SDD-17
(hush secret CLI), and SDD-25 (lifecycle harness).

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The vault file uses a binary HUSH format with: 4-byte magic,
  1-byte version, 16-byte salt, 12-byte AES-GCM nonce, then
  ciphertext+tag. Magic and version are fixed values.
- The plaintext payload is a list of named, described secret
  values. Secret values MUST never be materialised as strings —
  they live only in mlocked SecureBytes from the moment they
  leave the encrypted envelope.
- Save writes atomically: the on-disk file either contains the
  full new vault or remains the previous version. No reader
  can ever observe a partially-written vault.
- Save sets file mode 0600 and verifies parent directory mode is
  0700; Load refuses to open a file whose mode is laxer than
  0600 or whose parent is laxer than 0700.
- Wrong-passphrase decryption fails with a distinct, named error.
  Truncation at every header boundary fails with a distinct,
  named error. Loose file or parent permissions fail with a
  distinct, named error.
- The in-memory Store is safe for concurrent Get from many
  callers; each Get returns a fresh SecureBytes the caller owns.

The spec MUST NOT encode HOW (no library names, no Go-specific
package layout, no specific syscall names). Those are plan-phase
concerns.

Acceptance criterion: AC-2 (vault round-trip; SIGHUP reload is
SDD-10's half).

Action — run exactly one command:
  /speckit-specify "internal/vault: load and save the hush vault file (binary HUSH format, AES-256-GCM, atomic write with 0600 mode and 0700 parent), and serve secrets as SecureBytes-wrapped values via an in-memory Store"

The before_specify hook will create branch 003-vault-format.
Confirm the branch was created.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. If the contract
dictates the answer, fill it in. Otherwise leave the marker —
/speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-03 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-03.md (the chunk contract
— consult it if /speckit-clarify surfaces an ambiguity that the
contract already answers).

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-03 (internal/vault: HUSH format
+ AES-256-GCM + atomic write) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; III/VIII/X/XI are load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-2, FR-10, FR-15, AC-2)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/vault — the API contract you will lock)
- /Users/mrz/projects/hush/docs/SECURITY.md  (Encryption at Rest, Layer 5 SecureBytes integration)
- /Users/mrz/projects/hush/docs/TESTING-STRATEGY.md  (§2 fuzz target #1 — FuzzVaultDecode is yours)
- /Users/mrz/projects/hush/docs/sdd/SDD-03.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check gate — if it fires, fix the plan to comply,
do NOT bypass.

Scope:
- Package: internal/vault
- Files: file.go (HUSH header struct + parse/write), codec.go
  (AES-256-GCM seal/open), store.go (in-memory Store), permissions.go
  (file/dir mode checks), file_test.go, codec_test.go, store_test.go,
  permissions_test.go, vault_fuzz_test.go
- Exported API:
    type Secret struct { Name, Description string; Value *securebytes.SecureBytes }
    type Store interface { Get(name string) (*securebytes.SecureBytes, error); Names() []string; Destroy() error }
    func Load(ctx context.Context, path string, vaultKey *securebytes.SecureBytes) (Store, error)
    func Save(ctx context.Context, path string, vaultKey *securebytes.SecureBytes, secrets []Secret) error
    var ErrBadMagic, ErrBadVersion, ErrShortHeader, ErrAuthFailed, ErrFilePermsLoose, ErrSecretNotFound

Implementation contract (HOW — locked):
- HUSH magic = []byte{0x48, 0x55, 0x53, 0x48} ("HUSH"); version
  byte = 0x01. Encode both as named constants.
- 16-byte salt and 12-byte AES-GCM nonce; both generated via
  crypto/rand on Save (NEVER math/rand).
- Plaintext payload is a JSON array of {name, value, description}.
  The value field uses a custom UnmarshalJSON that decodes base64
  directly into a freshly-allocated []byte, then constructs a
  SecureBytes (which zeroes the input slice) — the secret value
  MUST NEVER appear as a Go string.
- Save flow: marshal JSON into a buffer → AES-256-GCM seal →
  write header+ciphertext to <path>.tmp in the SAME directory →
  fsync the file → os.Rename to <path> → os.Chmod 0600 → stat
  parent and refuse if not 0700.
- Load flow: O_RDONLY → stat → refuse if mode != 0600 or parent
  != 0700 → read header → validate magic/version/length → AES-256-GCM
  open → JSON unmarshal into a []Secret with custom UnmarshalJSON.
- Store.Get returns a NEW SecureBytes via internal copy (caller
  owns Destroy on the returned value). Store.Destroy zeroes
  every internal SecureBytes.
- AES-256-GCM via crypto/aes + crypto/cipher (stdlib).
  Constitution XI: no new crypto deps.
- internal/vault may import internal/vault/securebytes (its own
  subpackage) but no other internal/* outside its tree.

Coverage target: 100%. Fuzz target: FuzzVaultDecode (60s gate).
Constitutional principles in scope: III, VIII, X, XI.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-03 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-03.md.

NOTE: /speckit-tasks defaults to NO test tasks unless explicitly
told otherwise. This project is TDD-mandatory (Constitution VIII).
Pass TDD as the command argument.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 100%. Tests required: TestVault_RoundTrip_{0,1,5,500}Secrets, TestVault_LoadWrongPass_ReturnsAuthFailed, TestVault_LoadTruncatedAtMagic_ShortHeader, TestVault_LoadTruncatedAtSalt_ShortHeader, TestVault_LoadTruncatedAtNonce_ShortHeader, TestVault_LoadTruncatedCiphertext_AuthFailed, TestVault_LoadLooseFileMode_PermsLoose, TestVault_LoadLooseParentMode_PermsLoose, TestVault_SaveAtomic_NoIntermediate, TestVault_SaveSetsMode0600, TestStore_ConcurrentGet (100 goroutines, race-clean), and FuzzVaultDecode (no panic, no >50MB allocation, every error typed). Sentinel-leak: TestVault_NoLeakInError packs SECRET_SHOULD_NEVER_APPEAR_3 and asserts absence from err.Error() AND captured log. Final phase MUST include magex format:fix, magex lint, magex test:race, and go test -fuzz=FuzzVaultDecode -fuzztime=60s ./internal/vault/"

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-03 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-03.md (the chunk contract
— re-consult it if you start to drift mid-implementation).

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Fuzz (60s minimum, no crashes / no new bug corpus):
     go test -fuzz=FuzzVaultDecode -fuzztime=60s ./internal/vault/
3. Verify coverage = 100% on internal/vault:
     go test -cover ./internal/vault/
4. Confirm TestVault_NoLeakInError passed and
   SECRET_SHOULD_NEVER_APPEAR_3 is absent from any err.Error() and
   captured log output.
5. Append "Exported API — locked at SDD-03" section to
   docs/PACKAGE-MAP.md under internal/vault listing the five
   exported symbols + sentinels from the chunk doc.
6. Update docs/AC-MATRIX.md AC-2 row with the new test file paths
   (note: SIGHUP reload remains SDD-10's responsibility).
7. Mark SDD-03 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/vault/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(vault): HUSH file format + AES-256-GCM + atomic write (SDD-03)"

Final message: confirm gates passed, fuzz 60s clean, coverage =
100%, sentinel-leak absent, atomic-save observable in
TestVault_SaveAtomic_NoIntermediate, race-clean Store.Get, the
three docs updated, and the combined commit created.
```
