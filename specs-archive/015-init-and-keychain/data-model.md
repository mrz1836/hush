# Phase 1 — Data Model: SDD-15

This feature is a CLI bootstrap; the "data model" describes the
artifacts init **produces**, the in-memory entities init manipulates
during a single invocation, and the validation rules attached to
each. There are no long-lived runtime data structures introduced by
this chunk.

---

## 1. Persistent artifacts produced by `hush init server`

### 1.1 Vault file (`<state_dir>/secrets.vault`)

| Property | Value |
|---|---|
| Path | `<expanded state_dir>/secrets.vault` (default `~/.hush/secrets.vault`) |
| Created by | `vault.Save(ctx, path, vaultKey, []vault.Secret{})` (SDD-03) |
| File mode | `0600` (enforced by `vault.Save`) |
| Content | HUSH magic + version byte + 16-byte salt + 12-byte nonce + AES-256-GCM-encrypted empty secret list |
| Salt source | `crypto/rand.Read` (16 bytes; passed into `vault.Save`'s salt slot via the package's existing API) |
| Pre-existence guard | Init refuses with `errVaultExists` if the file exists at start of run (FR-012) |

**Validation rules**:
- Salt MUST be exactly 16 bytes (`saltLen` in `internal/keys`).
- Salt MUST be drawn freshly per init run; no salt reuse across runs.
- File MUST NOT exist before init runs.

**State transitions**: absent → created (atomic). Init never modifies
or rotates an existing vault file.

### 1.2 Server config file (`<state_dir>/config.toml`)

| Property | Value |
|---|---|
| Path | `<expanded state_dir>/config.toml` |
| Created by | init via atomic-write (`config.toml.tmp` → `fsync` → `rename` → `chmod 0600`) |
| File mode | `0600` |
| Content | TOML rendered from a fully-populated `serverDecoded` struct (every required field present; every documented default supplied) |
| Pre-existence guard | `O_EXCL` on the `.tmp` write rejects a leftover from a killed prior run; init refuses if `config.toml` itself exists |

**Required fields and their default sources** (from `docs/CONFIG-SCHEMA.md`,
each constant lives in `internal/config/defaults.go`):

| TOML key | Default constant | Operator-prompted? |
|---|---|---|
| `server.listen_addr` | _none — operator prompted_ | yes (FR-009 documented default = "no-default" prompt) |
| `server.path_prefix` | generated (12-char `crypto/rand` URL-safe) | no |
| `server.state_dir` | `DefaultStateDir = "~/.hush"` | no |
| `server.audit_log` | `DefaultAuditLog = "~/.hush/audit.jsonl"` | no |
| `server.discord_owner_id` | _none — operator prompted_ | yes |
| `server.client_registry` | `DefaultClientRegistry = "~/.hush/clients.json"` | no |
| `server.discord_audit_channel_id` | `""` (optional, omitted from output if empty) | no |
| `discord.bot_token_keychain_item` | `"hush-discord"` | no |
| `discord.application_id` | _none — operator prompted_ | yes |
| `crypto.argon_time` | `DefaultArgonTime = 4` | no |
| `crypto.argon_memory_mb` | `DefaultArgonMemoryMB = 256` | no |
| `crypto.argon_threads` | `DefaultArgonThreads = 4` | no |
| `crypto.jwt_default_ttl` | `"8h"` | no |
| `crypto.max_interactive_ttl` | `"12h"` | no |
| `crypto.max_supervisor_ttl` | `"20h"` | no |
| `crypto.default_max_uses` | `50` | no |
| `crypto.nonce_ttl` | `"60s"` | no |
| `crypto.clock_skew` | `"30s"` | no |
| `crypto.claim_approval_timeout` | `"60s"` (DefaultClaimApprovalTimeout) | no |
| `network.require_tailscale` | `true` | no |
| `network.allowed_cidrs` | `["100.64.0.0/10"]` | no |
| `network.health_bind` | _inherits from `listen_addr`_ | no |
| `security.require_file_mode_checks` | `true` | no |
| `security.require_keychain_acl` | `true` | no |
| `security.require_ntp_sync` | `true` | no |
| `security.max_clock_drift` | `"60s"` | no |

**Validation**: after writing, init re-loads the file via
`config.LoadServer(ctx, path)` and asserts no validation error
returns. This guarantees the generated file is round-trip-valid
against SDD-06's loader (SC-009).

**State transitions**: absent → created (atomic). Init never modifies
or rotates an existing config file.

### 1.3 Keychain items

Three items, all with hush-binary-only ACL on macOS, none on Linux
(Linux init refuses; see research §2):

| Item | Service | Account | Value source | Created in mode |
|---|---|---|---|---|
| Vault passphrase | `"hush-vault-passphrase"` | `"hush-server"` | `*securebytes.SecureBytes` from TTY prompt | server |
| Discord bot token | `cfg.Discord.BotTokenKeychainItem` (`"hush-discord"`) | `"hush-server"` | `*securebytes.SecureBytes` from TTY prompt | server |
| Per-machine client key | `"hush-client"` | `"machine-<N>"` | `*securebytes.SecureBytes` from `serializeECPrivKey(clientKey)` | client |

**ACL contract**: every item is created with the absolute path of the
running `hush` binary (`os.Executable()`) passed as the `acl`
argument to `Keychain.Store`. The Darwin impl translates this to
`security add-generic-password ... -T <abs-path>`. The Linux impl
ignores the `acl` argument because Linux init never reaches `Store`
(see §1.5 below).

**Pre-existence guard**: `Retrieve` is called for each
`(service, account)` BEFORE `Store`. A non-`ErrKeychainItemNotFound`
positive result causes init to exit with `errKeychainItemExists`
(naming the conflicting `(service, account)` pair).

---

## 2. Persistent artifacts produced by `hush init client`

### 2.1 Per-machine client key keychain item

Already enumerated in §1.3 above. Client mode produces no other
on-disk or keychain artifact.

### 2.2 Stdout fingerprint line

| Property | Value |
|---|---|
| Stream | `os.Stdout` |
| Format | `"SHA256:" + base64.RawStdEncoding.EncodeToString(sha256.Sum256(SEC1Compressed(pub)))` |
| Length | 7 (`SHA256:`) + 43 (43-char base64 of 32-byte digest) = **50 characters** |
| Trailing newline | exactly one `\n` |
| Decorations | none (no leading whitespace, no trailing space, no surrounding text) |

This is a transient output artifact — the operator copy-pastes it
into the server's registered-clients list (SDD-29 territory).

---

## 3. In-memory entities (lifetime: single init invocation)

### 3.1 Passphrase

| Property | Value |
|---|---|
| Type | `*securebytes.SecureBytes` |
| Lifetime | from `term.ReadPassword` return until `Destroy()` in deferred cleanup |
| Validation | byte length ≥ 12; rejected with `errPassphraseTooShort` (mapped to `ExitInputErr`) before any KDF call |
| Confirmation | second `term.ReadPassword` read; mismatch returns `errPassphraseMismatch` |
| Logging discipline | `*securebytes.SecureBytes` already implements `LogValue` → `[redacted]`; never converted to `string` |

### 3.2 Master seed

| Property | Value |
|---|---|
| Type | `[]byte` (64 bytes) |
| Source | `keys.DeriveMasterSeed(ctx, passphrase, salt)` |
| Lifetime | from derivation until `zeroBytes(masterSeed)` in deferred cleanup |
| Logging discipline | never logged; never returned in any error message |

### 3.3 Subkeys (server mode)

| Subkey | Type | Source | Used for |
|---|---|---|---|
| JWT signing | `*ecdsa.PrivateKey` | `keys.DeriveJWTSigningKey(seed)` | NOT used by init — derived for verification of round-trip serialization only (test path); discarded on init exit |
| Vault enc | `[]byte` (32 B) → wrapped in `*securebytes.SecureBytes` | `keys.DeriveVaultEncKey(seed)` | passed to `vault.Save` |
| Audit signing | `*ecdsa.PrivateKey` | `keys.DeriveAuditSigningKey(seed)` | NOT used by init |

Init only needs the vault-encryption subkey to call `vault.Save`. The
others exist conceptually (the master-seed derivation is the same one
`serve` will perform later) but init does not exercise them. The
audit log is created lazily by `audit.NewWriter` inside `serve`, not
by init.

### 3.4 Client signing key (client mode)

| Property | Value |
|---|---|
| Type | `*ecdsa.PrivateKey` (secp256k1) |
| Source | `keys.DeriveClientKey(seed, machineIndex)` |
| Serialization | `priv.D.FillBytes(buf[:32])` produces 32-byte fixed-width big-endian; wrapped in `*securebytes.SecureBytes` |
| Lifetime | from derivation until `Destroy()` after `Keychain.Store` returns |

The public half (`priv.PublicKey`) is the input to `sshStyleFingerprint`
(research §3); it is non-secret and printed to stdout once.

---

## 4. Operator-input entities

### 4.1 Machine index (client mode)

| Property | Value |
|---|---|
| Type | `uint32` |
| Source | `--machine-index` flag value, parsed via `strconv.ParseUint(s, 10, 32)` |
| Validation | non-negative (parser already rejects negatives); range `[0, 2^32-1]` (full BIP32 child range) |
| Required? | yes — absence returns `errMissingFlag` (mapped to `ExitInputErr`) |

### 4.2 Operator-prompted server fields

Server-mode init prompts for fields **without** a documented default
(`listen_addr`, `discord_owner_id`, `application_id`). All other
fields use their schema-documented default values. Each prompt:

- writes its label to `os.Stderr` (so stdout is reserved for the
  fingerprint line, even though server mode doesn't print one — keeps
  the convention consistent with client mode);
- reads the input via `bufio.Scanner` over `os.Stdin` (NOT
  `term.ReadPassword`, because these values are non-secret);
- rejects empty input with a re-prompt up to 3 times, then
  `errMissingFlag` if all attempts are blank.

The Discord bot token is the exception: it IS a secret and is read
via `term.ReadPassword` on the same TTY (FR-010 + FR-005a).

---

## 5. Error-class entities (sentinel errors added to `internal/cli`)

| Sentinel | Mapped exit code | Trigger |
|---|---|---|
| `errVaultExists` | `ExitErr` | Vault file already present at start of server init |
| `errConfigExists` | `ExitErr` | `config.toml` already present at start of server init |
| `errKeychainItemExists` | `ExitErr` | Pre-existing `(service, account)` pair detected |
| `errPassphraseTooShort` | `ExitInputErr` | Passphrase shorter than 12 bytes |
| `errPassphraseMismatch` | `ExitInputErr` | Confirmation entry differs from first entry |
| `errNoTTY` | `ExitInputErr` | `os.Stdin` is not a terminal |
| `errPlatformACLUnsupported` | `ExitErr` | `runtime.GOOS != "darwin"` (or any future platform without per-binary ACL) |
| `errMissingFlag` | `ExitInputErr` | _existing_ — also used for missing `--machine-index` |

All sentinel messages are static category strings (Constitution X).
None embed the passphrase, the bot token, the master seed, or the
client private key.

---

## 6. Relationships

```
operator
  ├─ prompts ─→ Passphrase ─KDF→ MasterSeed ─BIP32→ {VaultEncKey, ClientKey}
  ├─ prompts ─→ BotToken                                        │       │
  └─ prompts ─→ {ListenAddr, OwnerID, AppID}                    │       │
                                                                ▼       ▼
                                                          Vault file   Keychain
                                                                       (item per role)
```

The flow is strictly one-directional: operator → secrets → derived
key material → on-disk + keychain artifacts. No reads from existing
state (other than the existence-guard `os.Stat` calls and the
`Keychain.Retrieve` collision probes).

---

## 7. Phase 1 completion check

This data model is sufficient when an implementation agent can answer
the following without further consultation:

- Where does each artifact go on disk? **§1**
- What mode is each file written at, and how? **§1.1, §1.2**
- What `(service, account)` pair backs each keychain item? **§1.3**
- What is the lifetime and zeroization point of each in-memory
  secret? **§3**
- Which input fields are operator-prompted vs schema-defaulted? **§1.2 table**
- What sentinel error fires for each rejection path, and how does it
  map to the locked exit codes? **§5**
