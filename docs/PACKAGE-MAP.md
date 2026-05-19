# Package Map

This file turns the approved spec into concrete package ownership.

Phase 0 is not done until an implementation agent can look at the repo layout and know where code belongs before writing it.

---

## Design goals

The package map exists to prevent three failures:

1. crypto logic leaking into handlers
2. supervisor lifecycle logic getting mixed with generic CLI glue
3. implementation agents inventing random package boundaries mid-build

The rule is simple:
- keep domain boundaries sharp
- keep secrets logic isolated
- keep transport, approval, vault, and lifecycle code separated

---

## Top-level layout

- `cmd/hush/` → binary entrypoint only
- `internal/cli/` → cobra commands, flag parsing, output adapters, command wiring
- `internal/config/` → config structs, TOML/YAML loading, defaults, validation
- `internal/keys/` → passphrase derivation, BIP32 hierarchy, client key registration/loading
- `internal/vault/` → encrypted vault file format, load/save/reload, secure bytes, secret store model
- `internal/token/` → JWT issue/parse/validate/revoke, jti bookkeeping, session policy
- `internal/transport/` → ECIES encryption, request signing/verification, nonce/timestamp replay protection
- `internal/server/` → HTTP router, middleware, request handlers, health checks, SIGHUP wiring
- `internal/discord/` → Discord DM approval flow, buttons, audit-channel delivery, alert rendering
- `internal/supervise/` → supervisor state machine, validator orchestration, refresh scheduler, status socket, child lifecycle
- `internal/logging/` → structured logger setup, redaction rules, audit log helpers

No business logic belongs in `cmd/hush/`.

---

## Package responsibilities

## `cmd/hush/`

Purpose:
- build the root cobra command
- call into `internal/cli`
- keep `main.go` minimal

Must contain:
- version/build metadata injection
- root command bootstrap
- top-level error handling and exit code mapping

Must not contain:
- crypto code
- direct HTTP handler logic
- vault parsing
- supervisor logic

### Exported API — locked at SDD-14

#### Functions

| Symbol | Description |
|--------|-------------|
| `func main()` | Two-line body: `os.Exit(cli.Execute(context.Background()))`. No business logic per the chunk contract. |

---

## `internal/cli/`

Purpose:
- expose user-facing commands cleanly
- translate CLI flags into internal config/input structs
- keep stdout/stderr rendering consistent

Expected command modules:
- `serve.go`
- `request.go`
- `supervise.go`
- `init.go`
- `secret.go`
- `client.go`
- `health.go`
- `revoke.go`
- `version.go`
- `root.go`

Expected helper modules:
- output formatting (`text`, `json`, `eval`)
- flag validation
- exit code normalization

Must not contain:
- direct secret decryption logic
- Discord SDK logic
- session store implementation

### Exported API — locked at SDD-14

#### Functions

| Symbol | Description |
|--------|-------------|
| `func Execute(ctx context.Context) int` | Builds the cobra root, dispatches the subcommand matching `os.Args`, and returns the resolved exit code (one of the seven `Exit*` constants). The caller (`cmd/hush/main.go`) is responsible for `os.Exit`. |

#### Constants (the public exit-code contract)

| Symbol | Value | Meaning |
|--------|-------|---------|
| `ExitOK` | `0` | Clean completion |
| `ExitErr` | `1` | Generic error: network failure, server 5xx, partial-health, panic recovery |
| `ExitInputErr` | `2` | Operator-input error: missing/conflicting flag, malformed `--jti`, no passphrase source |
| `ExitAuth` | `3` | Authentication failure: bad passphrase, signature rejected, JWT rejected |
| `ExitNotFound` | `4` | Missing entity: `--config` not found, server returned 404 for `--jti` |
| `ExitPerm` | `5` | OS-level permission denied: bind/file-mode rejected |
| `ExitConfigStale` | `78` | `EX_CONFIG` sysexits sentinel — **reserved** for the supervisor↔child contract delivered by SDD-15/SDD-23. **Never raised by any subcommand in this chunk.** |

#### Build identification (link-time injected)

| Symbol | Default | Description |
|--------|---------|-------------|
| `var Version string` | `"dev"` | Semantic version; GoReleaser injects via `-ldflags "-X .../cli.Version=..."`. |
| `var Commit string` | `"unknown"` | Short commit identifier. |
| `var Date string` | `"unknown"` | RFC 3339 build date. |

#### Subcommands delivered by SDD-14

| Subcommand | Synopsis | Behaviour summary |
|------------|----------|-------------------|
| `serve` | `hush serve --reload-on-vault-change` | Brings the vault online. Resolves passphrase via `stdin pipe → TTY prompt → ExitInputErr` (never `os.Getenv`). Composes the chassis from already-locked surfaces. SIGTERM/SIGINT graceful shutdown via `signal.NotifyContext`. Optional `--reload-on-vault-change` watches `secrets.vault` and triggers the same atomic reload path as SIGHUP after debounced vault rewrites. |
| `health` | `hush health --server <url>` | 5 s-bounded `GET /hz`. TTY: per-dimension table; pipe: server's JSON body verbatim. Partial-health → full summary + `ExitErr` (FR-017a). Connection-refused/timeout literal-text contract per SDD-14 contract §6. |
| `server-url` | `hush --config <path> server-url` | Loads server config and prints the canonical `http://<listen_addr>/h/<path_prefix>` URL to stdout. Intended for copy/paste and scripts so operators never parse TOML with `sed`. |
| `version` | `hush version` | Prints build metadata. TTY: human lines. Pipe: locked JSON shape `{"version","commit","date"}` with `dev`/`unknown`/`unknown` placeholders. Always `ExitOK`. |
| `revoke` | `hush revoke --server <url> --jti <uuid>` | Builds canonical `{jti, nonce, timestamp}`, signs via `internal/transport/sign`, POSTs to `/revoke`. Status → exit code: 200→0, 401/403→3, 404→4, 5xx/network→1. |

Subsequent CLI chunks (SDD-15 `init`, SDD-16 `request`, SDD-17 `secret`, SDD-23 `supervise`/`client`) mount on top of this skeleton.

### Exported API — locked at SDD-15

The `init` parent and its two subcommands are mounted under the SDD-14 cobra root via package-side-effect (`root.AddCommand(newInitCmd())` in `Execute`). No new exported symbols are added to `internal/cli` — the cobra command tree is the contract.

| Subcommand | Synopsis | Behaviour summary |
|------------|----------|-------------------|
| `init server` | `hush init server` | Bootstraps the vault host. Runs diagnostic preflight, reads passphrase + confirmation + Discord owner ID + application ID + optional approval/audit channel IDs + bot token from the controlling TTY unless `--non-interactive` is set. Writes `<state_dir>/secrets.vault` (mode `0600`) and `<state_dir>/config.toml` (mode `0600`, every default from `docs/CONFIG-SCHEMA.md`). Stores Keychain items with the running binary's absolute path as ACL when available; offers explicit recovery/env-token fallback for bot-token Keychain denial. Classifies pre-existing artifacts and prompts reuse/repair/archive/fail rather than silently overwriting; refuses on Linux (no per-binary ACL). On success, prints exact next commands using the real config path, `listen_addr`, generated `path_prefix`, client registry, and suggested client key-file path. |
| `init client --machine-index N` | `hush init client --machine-index 3` | Enrolls an agent. Derives the per-machine BIP32 client key, stores it in the OS keychain under `(hush-client, machine-N)` with the binary path as ACL, and prints exactly one `SHA256:<43-char-base64>` fingerprint line to stdout (50 chars + `\n`). If `--client-key-file` is supplied and Keychain storage fails, writes that key file as an explicit fallback and tells the operator to pass the same flag to `hush request`. |
| `smoke` | `hush smoke --state-dir ~/.hush-smoke --reset` | Runs the fake-secret end-to-end smoke workflow. It initializes isolated server state, uses the Discord bot token only for the temporary server, adds `HUSH_SMOKE_TEST=hello-from-hush`, enrolls a client with key-file fallback, starts a temporary server using `HUSH_DISCORD_BOT_TOKEN` env-token mode, waits for Discord approval, verifies the fake secret via `request --format eval`, prints a success line, and shuts the temporary server down. |
| `smoke clean` | `hush smoke clean` | Safely clears isolated smoke/test state. Archives `~/.hush-smoke` by default, accepts explicit generic smoke/test/validation state dirs with `--state-dir`, refuses non-smoke/test state such as `~/.hush`, and requires `--destroy --confirm 'destroy smoke'` for permanent deletion. |

The two subcommands are mutually exclusive **structurally** — the cobra command tree separates them, so no flag combination can produce a conflict.

### Exported API — locked at SDD-16

The `request` subcommand is mounted under the SDD-14 cobra root via package-side-effect (`root.AddCommand(newRequestCmd())` in `Execute`). **No new exported package-level symbols are added to `internal/cli`** — the cobra command tree IS the contract for this chunk.

| Subcommand | Synopsis | Behaviour summary |
|------------|----------|-------------------|
| `request` | `hush request --server <url> --scope <CSV> --reason <s> --ttl <dur> --max-uses <int> --machine-index <uint32> ( --exec <prog> [-- ARGS...] \| --format eval )` | Loads the per-machine client signing key from the OS keychain, generates a fresh secp256k1 ephemeral key, signs the canonical claim payload, POSTs `/claim`, awaits Discord approval (bounded by `--ttl`), ECIES-decrypts each requested secret. Then either (a) `--exec`: runs the supplied program with secrets injected as env vars; child exit code becomes the parent's exit code; OR (b) `--format eval`: prints `export NAME='value'` lines to stdout AND emits the locked stderr WARNING per `docs/SECURITY.md §6`. Mutually exclusive — neither set → `ExitInputErr` with no I/O. |

The two delivery modes are validated at the input layer; no keychain or network call happens before mutual-exclusion + `--max-uses ≥ len(--scope)` checks succeed.

### Exported API — locked at SDD-17

The `secret` parent and its four subcommands are mounted under the SDD-14 cobra root via package-side-effect (`root.AddCommand(newSecretCmd())` in `Execute`). **No new exported package-level symbols are added to `internal/cli`** — the cobra command tree IS the contract for this chunk. (One additive function — `vault.LoadSecrets` — is added to `internal/vault` so the CLI can read names + descriptions in one pass; see the `internal/vault/` section.)

| Subcommand | Synopsis | Behaviour summary |
|------------|----------|-------------------|
| `secret add NAME` | `hush secret add ANTHROPIC_API_KEY` | TTY-only. Prompts for passphrase, secret value (twice; mismatch → `ExitInputErr`), optional description. Refuses if the entry already exists (locked stderr message directs the operator to `hush secret rotate`). Atomic via `vault.Save` (SDD-03). |
| `secret remove NAME` | `hush secret remove FOO` | TTY-only. Prompts for passphrase, then the typed-name confirmation token. Mismatched token → `ExitInputErr`. Absent entry → `ExitNotFound` BEFORE the confirmation prompt. |
| `secret list` | `hush secret list` (TTY) / `hush secret list \| jq` (pipe) | TTY stdin gate (universal). Renders `NAME — description` on stdout-TTY (em-dash U+2014; bare `NAME` when description empty); JSON `[{"name":"…","description":"…"},…]` on stdout-pipe (locked field set). NEVER prints values — value `*SecureBytes` handles are `Destroy()`-ed before the renderer runs. Empty vault → stderr `(vault is empty)` on TTY, stdout `[]\n` on pipe. |
| `secret rotate` | `hush secret rotate` | TTY-only. Re-encrypts the vault file with a fresh nonce + salt (plaintext set preserved; SDD-03). Then signals a running server via `syscall.Kill(pid, SIGHUP)` if `<state_dir>/hush.pid` parses as a live, owner-signal-able PID. Tolerates absent / stale (`ESRCH`) / not-our-user (`EPERM`) / unreadable PID — emits a stderr WARN line and exits `0` in every branch. |

Universal invariants (every verb):

- **TTY-first refusal**: `term.IsTerminal(int(os.Stdin.Fd()))` is checked BEFORE any vault I/O, keychain read, or flag interpretation. Non-TTY stdin → `ExitInputErr` with the locked stderr message `hush: secret: this command requires an interactive TTY (rogue-process defence)` and a `secret_tty_refused` slog WARN record.
- **No `--value`-class flag**: `--value`, `--secret`, `--password`, `--description`, `--force`, `--yes`, `--no-confirm` are all structurally absent. Cobra rejects any such flag with its own "unknown flag" error before our code runs.
- **Name validation**: `add` and `remove` validate `NAME` against `^[A-Z_][A-Z0-9_]*$` length 1–64 BEFORE opening the vault file. Failing → `ExitInputErr`.
- **Audit log discipline**: every `add`/`remove`/`rotate` success emits a slog INFO record (`secret_added`/`secret_removed`/`vault_rotated`); security-relevant failures emit slog WARN (`secret_tty_refused`/`secret_passphrase_failed`/`secret_confirmation_mismatch`). `list` success is NOT audited (read-only). Routine input-validation refusals (bad name, missing arg, unknown flag) NOT logged. NO record EVER carries the secret value, the confirmation token, the passphrase, or any byte derived from them.

Test surface: 33 named tests in `internal/cli/secret_test.go`; 88.6% statement coverage on `secret.go` (target: 85%).

### Exported API — locked at SDD-23

The `supervise` subcommand and the `client` parent (with its two leaf
subcommands, `client status` and `client refresh`) are mounted under
the SDD-14 cobra root via package-side-effect:
`root.AddCommand(newSuperviseCmd())` and `root.AddCommand(newClientCmd())`
in `Execute`. **No new exported package-level symbols are added to
`internal/cli`** — the cobra command tree IS the contract for this
chunk.

Two new sentinel-class errors land in `internal/cli/exit_codes.go`
(`errInvalidGraceWindow`, `errSocketAmbiguous`, `errSocketUnreachable`,
`errSupervisorRefused`, `errDuplicateSupervisor`) and are wired through
`mapErr` to the locked exit-code taxonomy per data-model.md §5.

| Subcommand | Synopsis | Behaviour summary |
|------------|----------|-------------------|
| `supervise` | `hush supervise <config-path> [--dry-run] [--grace-window <dur>] [--no-cache]` | Orchestrates SDD-18 (config), SDD-19 (state.Store), SDD-20 (Child), SDD-21 (Refiller/Refresher/Grace), SDD-22 (PidFile/StatusServer). `--dry-run` builds the canonical /claim payload via `sign.CanonicalJSON` and exits 0 with NO Discord / vault contact / pidfile / socket binding. Normal start: signal.NotifyContext → AcquirePidFile → NewStore → NewGrace → NewRefiller → NewRefresher → NewStatusServer (AttachStatusInputs + AttachRefreshHandler) → 2 spawned goroutines (StatusServer + Refresher, each with owner / ctx / recover per Constitution IX) → wait on rootCtx.Done() → wg.Wait() → pidfile.Release() via defer. Flag overrides applied pre-side-effect: `--grace-window` validated `>0 && ≤4h` else `errInvalidGraceWindow→ExitInputErr`; `--no-cache` beats `--grace-window` per FR-023-14. Duplicate start wraps `supervise.ErrPidLocked` as `errDuplicateSupervisor` with the FR-023-6 message naming the pidfile path. |
| `client status` | `hush client status [--socket <path>] [--supervisor <name>] [--json]` | Resolves the supervisor socket path via the precedence rule `--socket > --supervisor → supervise.SocketPathForSupervisor > supervise.EnumerateSupervisorSockets()`. 2s ctx → dial unix → write `status\n` → read body. `--json` or non-TTY stdout → emit raw socket bytes verbatim (preserves SDD-22's locked DTO byte-for-byte). TTY stdout → unmarshal into `statusDoc` and render the locked human label format (`Supervisor:`, `State:`, `Child PID:`, `Child up:`, `Session expires:`, `Next refresh:`, `Healthy scopes:`, `Stale scopes:`, `Discord:`, `Last auth fail:`). |
| `client refresh` | `hush client refresh [--socket <path>] [--supervisor <name>]` | 90s ctx → resolveSocketPath → write `refresh\n` → unmarshal `{ok,error}` ack. `{"ok":true}` → ExitOK; `{"ok":false,"error":<msg>}` → `errSupervisorRefused → ExitErr` with msg on stderr. NO `--json` flag (FR-023-17a). |

Two minimal extensions land in `internal/supervise/`:

- `internal/supervise/socket.go` gains a verb-dispatch branch in the
  per-connection handler (`status` → existing render path; `refresh`
  → `attachRefreshHandler` callback). Two new exported wiring
  methods on `*StatusServer`: `AttachStatusInputs(StatusInputs)` and
  `AttachRefreshHandler(func(ctx context.Context) error)` (mirrors
  the package-private `attach` precedent, surfaced for the SDD-23
  orchestrator). The "default = status" fallback preserves the
  SDD-22 §2.5 advisory-payload backward-compatibility note.
- `internal/supervise/socket_{darwin,linux}.go` gain two production
  helpers: `SocketPathForSupervisor(name string) string` (per-OS
  scheme) and `EnumerateSupervisorSockets() ([]string, error)`.

Test surface: ~25 named tests in `internal/cli/supervise_test.go` +
`internal/cli/client_test.go`; one integration test
`TestSuperviseIntegration_DryRunWithDiscordStub` in
`internal/cli/supervise_integration_test.go`. Average per-function
coverage on `supervise.go` + `client.go`: ~94% (target: 85%).

---

## `internal/keychain/`

Path: `github.com/mrz1836/hush/internal/keychain`

Purpose: cross-platform OS keychain wrapper. Per-binary ACL is required (macOS `-T` flag); platforms without per-binary ACL semantics report `false` from `PerBinaryACLSupported()` and init refuses up-front.

Allowed importers: `internal/cli` only. No other internal package may import `internal/keychain`.

### Exported API — locked at SDD-15

```go
// Keychain is the platform-agnostic OS keychain operations contract.
type Keychain interface {
    Store(ctx context.Context, service, account string, value *securebytes.SecureBytes, acl string) error
    Retrieve(ctx context.Context, service, account string) (*securebytes.SecureBytes, error)
    Delete(ctx context.Context, service, account string) error
}

// New returns the platform-native Keychain. Caller MUST gate via
// PerBinaryACLSupported() before invoking Store.
func New(logger *slog.Logger) (Keychain, error)

// PerBinaryACLSupported reports whether the platform impl honours the
// `acl` argument as a per-binary access restriction. macOS: true. Linux: false.
func PerBinaryACLSupported() bool

// FakeKeychain is the in-process test seam. Production code MUST NOT use it.
type FakeKeychain struct{}
func NewFake() *FakeKeychain
func (f *FakeKeychain) Store(ctx, service, account, *securebytes.SecureBytes, acl string) error
func (f *FakeKeychain) Retrieve(ctx, service, account string) (*securebytes.SecureBytes, error)
func (f *FakeKeychain) Delete(ctx, service, account string) error
func (f *FakeKeychain) Destroy()
func (f *FakeKeychain) RecordedACL(service, account string) string

// Sentinel errors returned by every Keychain implementation.
var ErrKeychainItemNotFound = errors.New("hush/keychain: item not found")
var ErrKeychainItemExists = errors.New("hush/keychain: item already exists")
var ErrKeychainPermissionDenied = errors.New("hush/keychain: permission denied")
var ErrKeychainUnsupportedPlatform = errors.New("hush/keychain: per-binary ACL unsupported on this platform")
```

Darwin implementation shells out to `/usr/bin/security add-generic-password ... -T <acl> -w` with the secret on stdin (never argv). Linux implementation wraps `github.com/zalando/go-keyring` and is build-target-only — production callers refuse on Linux per FR-020a.

---

## `internal/config/`

Purpose:
- define exact config schema for server and supervisor modes
- provide defaults
- validate startup invariants before any sensitive work begins

Expected responsibilities:
- load server config
- load supervisor config
- normalize paths
- validate Tailscale-only bind requirements
- validate file modes and required fields
- validate refresh window syntax, validator declarations, and child command shape

Likely files:
- `server.go`
- `supervisor.go`
- `defaults.go`
- `validate.go`
- `paths.go`

Must not contain:
- HTTP handling
- crypto primitives
- provider API calls

### Exported API — locked at SDD-06

#### Types

| Symbol | Description |
|--------|-------------|
| `type Server struct` | Top-level server config; read-only after `LoadServer` returns |
| `type ServerSection struct` | `[server]` TOML table: `ListenAddr netip.AddrPort`, `PathPrefix`, `StateDir`, `AuditLog`, `DiscordOwnerID`, `ClientRegistry`, `DiscordAuditChannelID` |
| `type DiscordSection struct` | `[discord]` TOML table: `BotTokenKeychainItem` (item name only, not the token), `ApplicationID` |
| `type CryptoSection struct` | `[crypto]` TOML table: Argon2id params, JWT/TTL/nonce/skew durations |
| `type NetworkSection struct` | `[network]` TOML table: `RequireTailscale`, `AllowedCIDRs`, `HealthBind netip.AddrPort` |
| `type SecuritySection struct` | `[security]` TOML table: file-mode/keychain/NTP flags, `MaxClockDrift` |

#### Functions

| Symbol | Description |
|--------|-------------|
| `func LoadServer(ctx context.Context, path string) (*Server, error)` | Open + decode + materialise + validate; never returns partial *Server on error |
| `func (s *Server) Validate() error` | Run all validation rules; multi-violation via `errors.Join` |

#### Default constants (`var`, set-once)

| Symbol | Type | Value |
|--------|------|-------|
| `DefaultArgonTime` | `uint32` | `4` |
| `DefaultArgonMemoryMB` | `uint32` | `256` |
| `DefaultArgonThreads` | `uint8` | `4` |
| `MinArgonTime` | `uint32` | `4` |
| `MinArgonMemoryMB` | `uint32` | `256` |
| `MinArgonThreads` | `uint8` | `4` |
| `DefaultJWTTTL` | `time.Duration` | `8h` |
| `DefaultMaxInteractiveTTL` | `time.Duration` | `12h` |
| `DefaultMaxSupervisorTTL` | `time.Duration` | `20h` |
| `DefaultSupervisorTTLMax` | `time.Duration` | `24h` |
| `DefaultMaxUses` | `int` | `50` |
| `DefaultNonceTTL` | `time.Duration` | `60s` |
| `DefaultClockSkew` | `time.Duration` | `30s` |
| `DefaultStateDir` | `string` | `"~/.hush"` |
| `DefaultAuditLog` | `string` | `"~/.hush/audit.jsonl"` |
| `DefaultClientRegistry` | `string` | `"~/.hush/clients.json"` |
| `DefaultListenPort` | `int` | `7743` |
| `DefaultRequireTailscale` | `bool` | `true` |
| `DefaultAllowedCIDRs` | `[]string` | `["100.64.0.0/10"]` |
| `DefaultRequireFileModeChecks` | `bool` | `true` |
| `DefaultRequireKeychainACL` | `bool` | `true` |
| `DefaultRequireNTPSync` | `bool` | `true` |
| `DefaultMaxClockDrift` | `time.Duration` | `60s` |
| `MinPathPrefixLen` | `int` | `6` |
| `MaxPathPrefixLen` | `int` | `32` |
| `TailscaleCGNAT` | `netip.Prefix` | `100.64.0.0/10` |

#### Sentinel errors

| Symbol | Wraps | Triggered by |
|--------|-------|-------------|
| `ErrTOMLDecode` | go-toml/v2 inner error | syntax / type-mismatch decode errors |
| `ErrUnknownField` | go-toml/v2 strict-mode error | unknown / misspelled TOML key |
| `ErrMissingRequiredField` | — | absent required field after decode |
| `ErrInvalidDuration` | — | `time.ParseDuration` failure on any duration field |
| `ErrTailscaleBindRequired` | (umbrella) | parent of the three listen-addr family errors |
| `ErrListenLoopback` | `ErrTailscaleBindRequired` | loopback `listen_addr` or `health_bind` |
| `ErrListenUnspecified` | `ErrTailscaleBindRequired` | unspecified `0.0.0.0` / `[::]` |
| `ErrListenPublic` | `ErrTailscaleBindRequired` | public or non-CGNAT address |
| `ErrListenMalformed` | — | `netip.ParseAddrPort` failure |
| `ErrTailscaleRequired` | — | `require_tailscale = false` |
| `ErrPathPrefixInvalid` | — | `path_prefix` length or charset violation |
| `ErrAuditLogEscape` | — | `audit_log` resolves outside `state_dir` |
| `ErrStateDirNotFound` | `fs.ErrNotExist` | `state_dir` does not exist on disk |
| `ErrStateDirUnsafe` | — | `state_dir` is not a directory |
| `ErrArgonMemoryTooLow` | — | `argon_memory_mb < 256` |
| `ErrArgonTimeTooLow` | — | `argon_time < 4` |
| `ErrArgonThreadsTooLow` | — | `argon_threads < 4` |
| `ErrArgonMemoryTooHigh` | — | `argon_memory_mb > 4096` (DoS-via-config ceiling) |
| `ErrArgonTimeTooHigh` | — | `argon_time > 16` (DoS-via-config ceiling) |
| `ErrArgonThreadsTooHigh` | — | `argon_threads > 128` (DoS-via-config ceiling) |
| `ErrSupervisorTTLOutOfRange` | — | `max_supervisor_ttl` ≤ `jwt_default_ttl` OR > 24h |
| `ErrConfigFileMode` | — | config file's own perms loosen than 0600 (gated by `Security.RequireFileModeChecks`) |

SDD-18 will add `Supervisor`, `LoadSupervisor`, and related symbols to this package. SDD-18 MUST NOT alter any symbol above.

---

## `internal/keys/`

Purpose:
- own the full runtime key hierarchy
- ensure zero key files are needed anywhere on disk

Expected responsibilities:
- Argon2id master seed derivation
- BIP32 child key derivation
- secp256k1 key conversion for JWT signing, ECIES, ECDSA request auth
- machine-index keyed client identity derivation
- public key export/fingerprint helpers

Likely files:
- `derive.go`
- `paths.go`
- `client.go`
- `fingerprint.go`

Must not contain:
- HTTP request logic
- vault storage format
- Discord approval code

### Exported API — locked at SDD-01

```go
package keys

import (
    "context"
    "crypto/ecdsa"
)

// DeriveMasterSeed derives the 64-byte hush master seed from a passphrase and a
// 16-byte salt using Argon2id (time=4, memory=256 MiB, threads=4, keyLen=64).
// ctx is inspected once at entry; pre-cancellation returns ctx.Err() immediately.
func DeriveMasterSeed(ctx context.Context, passphrase, salt []byte) ([]byte, error)

// DeriveJWTSigningKey derives the secp256k1 ECDSA private key for JWT signing.
// BIP32 path: m/44'/7743'/0'.
func DeriveJWTSigningKey(seed []byte) (*ecdsa.PrivateKey, error)

// DeriveVaultEncKey derives the 32-byte AES-256-GCM vault encryption key.
// BIP32 path: m/44'/7743'/1'.
func DeriveVaultEncKey(seed []byte) ([]byte, error)

// DeriveAuditSigningKey derives the secp256k1 ECDSA private key for audit-log signing.
// BIP32 path: m/44'/7743'/2'.
func DeriveAuditSigningKey(seed []byte) (*ecdsa.PrivateKey, error)

// DeriveClientKey derives the per-machine client signing keypair.
// BIP32 path: m/44'/7743'/3'/{machineIndex}.
func DeriveClientKey(seed []byte, machineIndex uint32) (*ecdsa.PrivateKey, error)

// PublicKeyFingerprint returns the 16-char lowercase hex fingerprint of a secp256k1 public key.
// Algorithm: hex(sha256(SEC1_compressed(pub))[:8]).
func PublicKeyFingerprint(pub *ecdsa.PublicKey) string

// Sentinel errors — compare with errors.Is.
var (
    ErrPassphraseTooShort = errors.New("hush/keys: passphrase too short")
    ErrSaltMissing        = errors.New("hush/keys: salt missing or wrong length")
)
```

---

## `internal/vault/`

Purpose:
- own encrypted secret storage at rest
- keep plaintext secret handling constrained and explicit

Expected responsibilities:
- parse and write the `HUSH` vault file format
- AES-256-GCM encrypt/decrypt
- secure in-memory secret representation
- atomic save semantics
- SIGHUP reload support via full new-vault replacement
- zeroization hooks for replaced vault material

Likely files:
- `file.go`
- `codec.go`
- `store.go`
- `securebytes.go`
- `reload.go`
- `permissions.go`

Must not contain:
- Discord bot logic
- child-process supervision
- HTTP router setup

### Exported API — locked at SDD-02 (`securebytes` subpackage)

Path: `github.com/mrz1836/hush/internal/vault/securebytes`

```go
// SecureBytes wraps a binary payload under memory pinning (mlock), type-driven
// render redaction, and zero-on-destroy. Zero value is NOT valid; construct via New.
type SecureBytes struct{ /* unexported */ }

// New copies b into a fresh mlocked buffer, zeroes b, and returns the container.
// Registers a runtime finalizer that calls Destroy if the reference becomes unreachable.
func New(b []byte) (*SecureBytes, error)

// Use invokes fn with the container's mlocked buffer. fn MUST NOT retain the slice.
// Returns ErrDestroyed if the container has already been destroyed.
func (sb *SecureBytes) Use(fn func(b []byte)) error

// Len returns the payload length, or 0 after Destroy.
func (sb *SecureBytes) Len() int

// Destroy zeroes the buffer, munlocks it, and marks the container destroyed. Idempotent.
func (sb *SecureBytes) Destroy() error

// LogValue implements slog.LogValuer. Always returns slog.StringValue("[redacted]").
func (sb *SecureBytes) LogValue() slog.Value

// String implements fmt.Stringer. Always returns "[redacted]".
func (sb *SecureBytes) String() string

// MarshalJSON implements json.Marshaler. Always returns []byte(`"[redacted]"`).
func (sb *SecureBytes) MarshalJSON() ([]byte, error)

// ErrDestroyed is returned by Use on a destroyed container.
var ErrDestroyed = errors.New("hush/vault/securebytes: destroyed")
```

### Exported API — locked at SDD-03 (`internal/vault` package)

Path: `github.com/mrz1836/hush/internal/vault`

```go
// Secret is one named, described, value-bearing entry in the vault.
//
// Value MUST be non-nil and live (not destroyed) at the moment Save is called.
// Save does not retain a reference to the caller's *SecureBytes after it returns.
type Secret struct {
    Name        string
    Description string
    Value       *securebytes.SecureBytes
}

// Store is the in-memory view of a loaded vault. Implementations are safe for
// concurrent Get and Names from many goroutines. Get returns a fresh,
// independently-owned *SecureBytes per call. Destroy is idempotent.
type Store interface {
    // Get returns ErrSecretNotFound if name is absent, ErrStoreDestroyed after Destroy.
    Get(name string) (*securebytes.SecureBytes, error)
    // Names returns a defensive copy in stable load order.
    Names() []string
    // Destroy zeroes every internally-held *SecureBytes. Idempotent.
    Destroy() error
}

// Load reads, validates, and decrypts the vault file at path using vaultKey,
// returning a Store from which secrets can be retrieved.
func Load(ctx context.Context, path string, vaultKey *securebytes.SecureBytes) (Store, error)

// Save encrypts secrets to the vault file at path using vaultKey,
// committing the result atomically (write to <path>.tmp → fsync → rename → chmod 0600).
func Save(ctx context.Context, path string, vaultKey *securebytes.SecureBytes, secrets []Secret) error

// LoadSecrets (added in SDD-17) reads, validates, and decrypts the vault file
// returning the full Secret slice (Name, Description, Value). Use this for
// one-shot management operations that need access to descriptions + values
// in a single pass; the caller owns each Secret.Value *SecureBytes and MUST
// Destroy them.
func LoadSecrets(ctx context.Context, path string, vaultKey *securebytes.SecureBytes) ([]Secret, error)

// Sentinel errors — compare with errors.Is.
var (
    ErrBadMagic       = errors.New("hush/vault: bad magic")
    ErrBadVersion     = errors.New("hush/vault: bad version")
    ErrShortHeader    = errors.New("hush/vault: short header")
    ErrAuthFailed     = errors.New("hush/vault: authentication failed")
    ErrFilePermsLoose = errors.New("hush/vault: file permissions loose")
    ErrSecretNotFound = errors.New("hush/vault: secret not found")
    ErrStoreDestroyed = errors.New("hush/vault: store destroyed")
    ErrDuplicateName  = errors.New("hush/vault: duplicate secret name")
    ErrFileTooLarge   = errors.New("hush/vault: file too large")
    ErrInvalidName    = errors.New("hush/vault: invalid secret name or description")
)
```

---

## `internal/testutil/`

Purpose:
- provide deterministic, leak-safe test helpers for every downstream package
- MUST NOT be imported by any production source file

Expected responsibilities:
- deterministic test keys (Argon2id-derived, memoised)
- temp-dir-scoped vault fixture with real HUSH-format file
- sentinel-string generator and absent-assertion helper
- programmable Discord approval stub with response queue

Must not contain:
- production business logic
- network sockets of any kind
- `init()` functions

### Exported API — locked at SDD-04

Path: `github.com/mrz1836/hush/internal/testutil` *(test-only — `*_test.go` imports only)*

```go
// NewTestVault creates a real HUSH-format vault inside t.TempDir() and registers
// t.Cleanup to zero the vault key. Returns path, vaultKey, and an explicit cleanup.
func NewTestVault(t *testing.T, secrets map[string]string) (path string, vaultKey *securebytes.SecureBytes, cleanup func())

// NewTestKeys returns a deterministic 64-byte master seed (Argon2id, memoised per
// process). Two calls in any test or goroutine return byte-identical slices.
func NewTestKeys(t *testing.T) (masterSeed []byte)

// SentinelSecret returns the canonical SECRET_SHOULD_NEVER_APPEAR_<n> marker.
func SentinelSecret(n int) string

// AssertSentinelAbsent fails t if sentinel appears anywhere in haystack,
// reporting the byte offset and a 64-byte context window.
func AssertSentinelAbsent(t *testing.T, sentinel, haystack string)

// DiscordStub is a programmable, network-free Discord approval substitute.
// Use NewDiscordStub to construct one.
type DiscordStub struct {
    ApproveAll bool // tail-default after queue is exhausted
    // unexported: mu, responses, calls, t
}

// NewDiscordStub constructs a DiscordStub bound to t and registers t.Cleanup
// to drain the recorded calls and response queue.
func NewDiscordStub(t *testing.T) *DiscordStub

// ApprovalCall records one call to RequestApproval.
type ApprovalCall struct {
    Request  ApprovalRequest
    Decision Decision
    Err      error
    Index    int
}

// Approver is the narrow interface DiscordStub satisfies. SDD-11 widens this
// into the production Approver in internal/discord.
type Approver interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// Supporting types (entailed by the above API):
//
//   type Decision int   — DecisionApprove | DecisionDeny | DecisionApproveMute
//   type ApprovalRequest struct { RequesterHost, Scopes, SessionType, TTL, MaxUses }
//   var ErrUnexpectedCall error  — returned when queue empty and ApproveAll==false
```

---

## `internal/token/`

Purpose:
- own session policy and JWT lifecycle

Expected responsibilities:
- register `ES256K` signing method
- create interactive and supervisor tokens
- validate claims
- enforce TTL, scope, `session_type`, `client_ip`, `max_uses`
- maintain active/revoked/exhausted token bookkeeping
- expose token status to handlers/supervisor

Likely files:
- `claims.go`
- `issue.go`
- `validate.go`
- `store.go`
- `revoke.go`

Must not contain:
- ECIES payload encryption
- Discord UI formatting
- launchd/systemd specifics

### Exported API — locked at SDD-07

```go
// Package github.com/mrz1836/hush/internal/token

type SessionType string

const (
    SessionInteractive SessionType = "interactive"
    SessionSupervisor  SessionType = "supervisor"
)

type Claims struct {
    jwt.RegisteredClaims

    Scope           []string    `json:"scope"`
    ClientIP        string      `json:"client_ip"`
    RequestID       string      `json:"request_id"`
    MaxUses         int         `json:"max_uses"`
    EphemeralPubKey string      `json:"ephemeral_pubkey"`
    SessionType     SessionType `json:"session_type"`
}

type Token struct {
    JTI         string
    Encoded     string
    ExpiresAt   time.Time
    SessionType SessionType
    MaxUses     int
}

type IssueParams struct {
    Now             time.Time
    TTL             time.Duration
    Scope           []string
    ClientIP        string
    RequestID       string
    MaxUses         int
    EphemeralPubKey string
    SessionType     SessionType
}

type Store interface {
    Add(t *Token) error
    Get(jti string) (*Token, error)
    ConsumeUse(jti string) error
    Revoke(jti string) error
    Cleanup(ctx context.Context)
}

func NewStore() Store
func NewStoreWithTick(d time.Duration) Store

func Issue(ctx context.Context, signKey *ecdsa.PrivateKey, params IssueParams) (*Token, error)
func Validate(ctx context.Context, encoded string, verifyKey *ecdsa.PublicKey, store Store, requestIP string, requestedSecret string, opts ...ValidateOpt) (*Claims, error)

// Functional-options for Validate.
type ValidateOpt func(*validateConfig)

// WithClockSkew sets the symmetric clock-skew tolerance applied to the
// JWT exp/nbf check (jwt.WithLeeway). Caller-supplied skew <= 0 is ignored.
// Operationally this only affects the JWT parse layer; the in-memory store
// retains its strict expiry check on use as defense-in-depth.
func WithClockSkew(skew time.Duration) ValidateOpt

// Sentinel errors. Compare via errors.Is. Static messages; no JWT or key bytes.
//
// Granularity rule: each sentinel maps to a distinct operational class so
// that monitoring can alert on each independently. Do NOT collapse classes.
var ErrAlgorithmUnsupported error // header alg != ES256K
var ErrTokenMalformed       error // pre-parse failures: no separator, bad b64, bad JSON, jwt-lib parse error
var ErrSignatureInvalid     error // verification with the supplied public key failed
var ErrTokenExpired         error // exp claim has passed
var ErrTokenRevoked         error // jti is in the revoked set
var ErrTokenExhausted       error // interactive max_uses budget consumed
var ErrIPMismatch           error // requesting IP differs from claim's client_ip
var ErrScopeViolation       error // requested secret not in claim's scope
var ErrUnknownSessionType   error // session_type claim is unrecognised
var ErrInvalidIssueParams   error // caller supplied a zero/empty/malformed IssueParams field
var ErrJTIGeneration        error // OS RNG failed during JTI generation
var ErrSigningFailed        error // signing the JWT with the supplied key failed
```

Behavioural contract:
- ES256K signing method registered exactly once via a `sync.Once`-gated
  `Register()` helper invoked by `Issue` and `Validate` (no `init()`).
- Algorithm-confusion defence: `Validate` rejects header `alg` ≠
  `"ES256K"` (including `"none"` and `"HS256"`) BEFORE the keyfunc is
  consulted.
- INTERACTIVE tokens are TTL+max-uses bounded; SUPERVISOR tokens are
  TTL-only — `Issue` zeroes `MaxUses` for SUPERVISOR; `ConsumeUse`
  short-circuits before decrementing.
- Revocation persists for the lifetime of the store; `Cleanup` reclaims
  expired live records but never touches the revoked set.
- All errors are sentinel-class with static messages; no token, signing
  key, or verify key bytes appear in any error message (FR-014).

---

## `internal/transport/`

Purpose:
- own the security properties of request and response transport beyond Tailscale itself

Expected responsibilities:
- ECIES encrypt/decrypt helpers
- canonical request payload hashing/signing
- signature verification against registered client keys
- nonce cache / replay protection
- timestamp window validation
- safe wire payload structures

Likely files:
- `ecies.go`
- `sign.go`
- `verify.go`
- `nonce.go`
- `wire.go`

Must not contain:
- token issuance decisions
- handler routing
- provider validator logic

### Exported API — locked

> SDD-09 (`internal/transport/ecies`) fills the ECIES sub-package; the
> request-signing sibling is locked at SDD-08 (`internal/transport/sign`).

---

## `internal/transport/ecies` — Exported API (locked at SDD-09)

**Package path**: `github.com/mrz1836/hush/internal/transport/ecies`

**Contract document**: [`specs/009-transport-ecies/contracts/api.md`](../specs/009-transport-ecies/contracts/api.md)

```go
// Functions

func Encrypt(ctx context.Context, recipientPub *ecdsa.PublicKey, plaintext []byte) ([]byte, error)
func Decrypt(ctx context.Context, recipientPriv *ecdsa.PrivateKey, envelope []byte) (*securebytes.SecureBytes, error)

// Sentinel errors

var ErrECIESDecryptFailed       = errors.New("hush/transport/ecies: ECIES decrypt failed")
var ErrECIESEnvelopeTooShort    = errors.New("hush/transport/ecies: envelope too short")
var ErrECIESEmptyPlaintext      = errors.New("hush/transport/ecies: empty plaintext")
var ErrECIESInvalidRecipientKey = errors.New("hush/transport/ecies: invalid recipient key")
```

`Encrypt` produces an opaque BIE1 ECIES envelope (4-byte magic ‖ 33-byte
compressed ephemeral pubkey ‖ AES-256-CBC ciphertext ‖ 32-byte HMAC-SHA256
tag, minimum 85 bytes); `Decrypt` returns a fresh `*securebytes.SecureBytes`
whose lifetime the caller owns. Wrong key and tampered envelope share
`ErrECIESDecryptFailed` by design (FR-004 — no failure-shape leakage).

Future ECIES-adjacent helpers (e.g., a streaming `Decrypt` for very large
secrets) MAY land as additional symbols in this package without breaking the
existing contract; symbol REMOVAL or signature CHANGES require a new SDD chunk.

---

## `internal/transport/sign` — Exported API (locked at SDD-08)

**Package path**: `github.com/mrz1836/hush/internal/transport/sign`

**Contract document**: [`specs/008-transport-sign/contracts/api.md`](../specs/008-transport-sign/contracts/api.md)

```go
// Types

type RawMessage []byte            // escape hatch: verbatim canonical insertion

type NonceCache interface {
    Add(ctx context.Context, nonce string, ttl time.Duration) (firstSeen bool, err error)
    Run(ctx context.Context)
}

// Functions

func CanonicalJSON(v any) ([]byte, error)
func Sign(ctx context.Context, key *ecdsa.PrivateKey, payload []byte) ([]byte, error)
func Verify(ctx context.Context, key *ecdsa.PublicKey, payload, sig []byte) error
func NewNonceCache() NonceCache
func IsFreshTimestamp(ts time.Time, skew time.Duration) bool

// Sentinel errors

var ErrSignatureInvalid    = errors.New("hush/transport/sign: signature invalid")
var ErrNonceReplay         = errors.New("hush/transport/sign: nonce replay")
var ErrNonceEncoding       = errors.New("hush/transport/sign: nonce encoding invalid (length out of [8,128])")
var ErrNonceTTLInvalid     = errors.New("hush/transport/sign: nonce ttl must be positive")
var ErrTimestampStale      = errors.New("hush/transport/sign: timestamp outside freshness window")
var ErrCanonicalUnsupported = errors.New("hush/transport/sign: value cannot be canonicalised")
```

SDD-09 (`internal/transport/ecies`) will land as a sibling sub-package.
SDD-09 MUST NOT alter any symbol above.

---

## `internal/server/`

Purpose:
- expose the vault server HTTP interface cleanly
- compose config, vault, token, transport, and discord subsystems

Expected responsibilities:
- route registration under `/h/<prefix>/...`
- handlers for claim, secret fetch, revoke, health
- middleware for logging, panic safety, request IDs, auth extraction
- server startup checks
- SIGHUP vault reload entrypoint
- graceful shutdown and audit events

Likely files:
- `server.go`
- `router.go`
- `middleware.go`
- `claim_handler.go`
- `secret_handler.go`
- `revoke_handler.go`
- `health_handler.go`
- `reload.go`

Must not contain:
- Argon2id/BIP32 implementation
- supervisor child restart logic

### Exported API — locked

> Filled by SDD-10 (server skeleton + SIGHUP reload), SDD-12 (claim
> handler), and SDD-13 (other handlers + audit). Until then, this section
> is a placeholder.

### Exported API — locked at SDD-10

The chassis surface locked by SDD-10 (HTTP router, middleware stack, ordered
startup checks, SIGHUP atomic vault reload, graceful shutdown). Files:
[`internal/server/`](../internal/server/).

**Constructor / lifecycle**

- `func New(deps Deps) (*Server, error)` — performs zero I/O; nil-checks
  every required dep; returns matching `Err*` sentinel on a missing field.
- `func (s *Server) Run(ctx context.Context) error` — runs the lifecycle:
  startup checks → bind → serve → graceful shutdown. Single-call only;
  second call returns `ErrAlreadyRun`.
- `func (s *Server) ReloadVault(ctx context.Context, newPath string, key *securebytes.SecureBytes) error` —
  serialised: load → atomic swap → drain → destroy. Errors are wrapped
  sentinels (`ErrReloadFileMissing`, `ErrReloadDecryptFailed`,
  `ErrReloadInvalid`); active vault pointer unchanged on failure.
- `func (s *Server) Mount(method, path string, h http.Handler) error` —
  pre-Run-only handler registration under `/h/<prefix>/...`; post-Run
  returns `ErrAlreadyRun`.
- `func RequestID(ctx context.Context) string` — accessor for the
  chassis-assigned 32-char hex request ID. Returns `""` when ctx did not
  pass through the chassis middleware.

**Locked types**

- `type Server struct { /* unexported */ }`
- `type Deps struct { Cfg, VaultPtr, TokenStore, Approver, Logger,
   AuditWriter (required); Clock, ClockSyncProbe, InterfaceLister, Listener,
   VaultKey, LoadVaultFn, ReloadDrainWindow, ShutdownTimeout (optional) }`
- `type Approver interface { RequestApproval(ctx, ApprovalRequest) (Decision, error) }`
- `type ApprovalRequest struct { RequestID, MachineName, ClientIP, Scope,
   Reason, SessionType, RequestedTTL, Metadata }`
- `type Decision struct { Approved, ApprovedAt, DeniedAt, GrantedTTL,
   ApproverID, Reason }`
- `type SessionType uint8` with constants `SessionInteractive`,
  `SessionSupervisor`; `String()` returns `"interactive"`, `"supervisor"`,
  or `"unknown"`.
- `type AuditWriter interface { Write(ctx, AuditEvent) error }`
- `type AuditEvent struct { Type, At, RequestID, ClientIP, Detail }`
- `type AuditEventType string` with chassis-emitted constants:
  `AuditServerStart`, `AuditServerStop`, `AuditVaultReloaded`,
  `AuditFilePermCheckFailed`, `AuditAuthFailedNotAllowed`,
  `AuditPanicCaptured`.

**Sentinel errors**

- Construction: `ErrMissingConfig`, `ErrMissingVaultPtr`,
  `ErrMissingTokenStore`, `ErrMissingApprover`, `ErrMissingLogger`,
  `ErrMissingAuditWriter`.
- Lifecycle: `ErrAlreadyRun`, `ErrShuttingDown`.
- Startup checks: `ErrClockUnsynchronised`, `ErrFileModeLoose`,
  `ErrBindNotOnTailscale`, `ErrStateDirUnsafe`.
- Reload: `ErrReloadFileMissing`, `ErrReloadDecryptFailed`,
  `ErrReloadInvalid`, `ErrReloadInProgress`, `ErrReloadInternalNil`.
- Mount: `ErrMountNilHandler`, `ErrMountBadPath`, `ErrMountUnsupported`.
- Clock probe: `ErrClockProbeUnexpectedOutput`.

**Defaults (package-level constants)**

- `DefaultReloadDrainWindow = 30 * time.Second`
- `DefaultShutdownTimeout = 30 * time.Second`
- `DefaultReadHeaderTimeout = 10 * time.Second`
- `DefaultReadTimeout = 30 * time.Second`
- `DefaultWriteTimeout = 30 * time.Second`
- `DefaultIdleTimeout = 60 * time.Second`
- `DefaultClockSyncTimeout = 5 * time.Second`
- `MaxRequestBodyBytes = 64 << 10`

**Behaviour contracts (locked)**

- Startup-check order is `clock_sync → file_modes → tailscale_bind →
  state_dir`; first failure short-circuits.
- Middleware order is request ID → IP allow-list → body cap → panic
  recover → handler. Recover middleware logs panic + stack + request_id
  but never any byte of the request body.
- SIGHUP-driven reloads are serialised under a single mutex; each old
  store is destroyed exactly once after the configured drain window.

### Exported API — locked at SDD-12

POST `/claim` handler — see [docs/API.md](API.md) (locked at SDD-12).

The handler is registered via `(*Server).RegisterHandlers()`; the chassis
mounts `POST /h/<prefix>/claim` and runs the locked pipeline `shape →
canonical-JSON+verify → nonce → timestamp → IP allowlist → TTL cap →
Approver.RequestApproval → token.Issue`. Constitution II — no
configuration surface can map `ErrApproverUnavailable` to HTTP 200.

Additive Deps fields:
- `TokenIssuer` (required) — `func(ctx, token.IssueParams) (*token.Token, error)`
- `ClientKeyResolver` (optional, defaults to a file-loader over
  `Cfg.Server.ClientRegistry`)

Additive sentinels: `ErrApproverDenied`, `ErrApproverTimeout`,
`ErrApproverUnavailable`, `ErrApproverRateLimited`, `ErrClientUnknown`,
`ErrMissingTokenIssuer`. Additive audit-event type: `AuditClaimOutcome`.
Additive config field: `Crypto.ClaimApprovalTimeout` (default 60 s,
range [1 s, 10 min]).

### Exported API — locked at SDD-13

GET `/s/<name>`, POST `/revoke`, GET `/hz` handlers — see
[docs/API.md](API.md) (locked at SDD-13). All three are registered
through `(*Server).RegisterHandlers()` alongside `/claim`.

- `/s/<name>` — Bearer JWT → `token.Validate` → `vault.Store.Get` →
  `ecies.Encrypt` against the claim's ephemeral pubkey → octet-stream
  body. Constitution IV / Constitution X (interactive `MaxUses`
  decremented exactly once; supervisor never decremented; plaintext
  never appears in any error body or operational log).
- `/revoke` — signed body
  `{jti, nonce, timestamp, request_id?, machine_name?, client_key_fingerprint, signature}`
  → `sign.CanonicalJSON+Verify` against the chassis's
  `ClientKeyResolver` registry → `NonceCache.Add` → `IsFreshTimestamp`
  → `token.Store.RevokeIdempotent`. Idempotent re-revoke returns the
  identical 200 body; the audit chain distinguishes via
  `revoke_succeeded` vs `revoke_idempotent_already_revoked`. Unknown
  JTI / fingerprint maps to `bad_signature` (FR-015 anti-enumeration).
- `/hz` — no auth (Constitution VI: Tailscale is the auth perimeter);
  reports `{status, uptime, secrets_count, active_tokens,
  discord_connected, config_valid, vault_loaded, clock_in_sync}`.
  MUST NOT emit an audit event (FR-021a).

Additive Deps fields:
- `JWTVerifyKey *ecdsa.PublicKey` (required for `/s`) — public half of
  the BIP32-derived JWT signing key consulted by `token.Validate`.
- `DiscordHealth func() bool` (optional) — surfaced by `/hz`'s
  `discord_connected`. Nil reports `false` (fail-closed; R-009).

Additive sentinels: `ErrSecretMissing`. Additive `token.Store` methods:
`ActiveCount() int`, `RevokeIdempotent(jti string) (existed,
alreadyRevoked bool)`. Additive `*BotApprover` accessor:
`Connected() bool`. Additive type: `chassisAuditAdapter` (constructed
via `NewChassisAuditAdapter(audit.Writer) AuditWriter`).

## `internal/audit/`

Purpose:
- own the hush server's tamper-evident audit log (Constitution III
  Layer 6)
- hash-chained, ECDSA-signed event log written to disk and optionally
  mirrored to a Discord channel best-effort

Files:
- `chain.go` — `Event`, `Verify`, `ChainError`, sentinels, action
  constants, hash + sign helpers, genesis prevHash
- `writer.go` — `Writer` interface, `NewWriter`, single-goroutine
  rendezvous-based persistence
- `discord_mirror.go` — `DiscordMirror`, `MirrorSession` seam,
  best-effort goroutine

### Exported API — locked at SDD-13

Path: `github.com/mrz1836/hush/internal/audit`

```go
// Event is one record of the hash-chained, signed audit log.
type Event struct {
    Seq       uint64         `json:"seq"`
    Time      time.Time      `json:"time"`
    Action    string         `json:"action"`
    Data      map[string]any `json:"data,omitempty"`
    PrevHash  string         `json:"prev_hash"`
    Hash      string         `json:"hash"`
    Signature string         `json:"signature"`
}

// Writer is the producer-facing interface.
type Writer interface {
    Append(ctx context.Context, action string, data map[string]any) error
    Run(ctx context.Context) error
}

// NewWriter constructs a Writer.
func NewWriter(
    ctx context.Context,
    path string,
    signKey *ecdsa.PrivateKey,
    mirror *DiscordMirror,
    logger *slog.Logger,
) (Writer, error)

// Verify re-validates the on-disk chain end-to-end.
func Verify(path string, verifyKey *ecdsa.PublicKey) error

// DiscordMirror is the optional best-effort chat-platform publisher.
type DiscordMirror struct { /* unexported */ }

// MirrorSession is the narrow seam over *discordgo.Session.
type MirrorSession interface {
    ChannelMessageSendComplex(channelID string, data *discordgo.MessageSend, opts ...discordgo.RequestOption) (*discordgo.Message, error)
}

func NewDiscordMirror(channelID string, session MirrorSession) *DiscordMirror

// ChainError carries the offending Seq + Reason for chain breaks.
type ChainError struct { Seq uint64; Reason string; Err error }

// Sentinels.
var ErrAuditChainBroken
var ErrShutdown
var ErrChainTailUnreadable
var ErrInvalidPath
var ErrInvalidKey
var ErrInvalidLogger
var ErrEmptyAction
var ErrAlreadyRun
```

**Behaviour contracts (locked)**

- `Append` is rendezvous-synchronous (FR-033) and blocks under producer
  contention (FR-031). Returns nil only AFTER the event has been hashed,
  signed, written, and flushed to disk.
- `Hash = SHA-256(prevHash || sign.CanonicalJSON({Seq, Time, Action,
  Data, PrevHash}))`. `PrevHash` of Seq=1 is
  `sha256("hush.audit.chain.v1.genesis")`.
- `Signature = base64(ecdsa.SignASN1(audit-signing-key, Hash))`.
- `Verify` recomputes the chain end-to-end and surfaces `ErrAuditChainBroken`
  wrapped in a `*ChainError` at the first inconsistent Seq.
- Mirror is best-effort: 64-deep buffered channel, separate goroutine,
  non-blocking writer-side dispatch, no retries; failures log WARN with
  `seq` + `action` + error class only.
- `Data` MUST NOT carry secret values, JWT bytes, signature bytes,
  nonce bytes, bot tokens, or the audit signing key (FR-028, FR-029).

---

## `internal/discord/`

Purpose:
- keep approval UX, audit delivery, and alert formatting out of the core server package

Expected responsibilities:
- connect Discord bot
- render approval DMs and interactive buttons
- track pending claim requests
- map button clicks to approval/denial outcomes
- send audit-channel messages
- render refresh prompts and stale-credential alerts in distinct formats

Likely files:
- `bot.go`
- `approval.go`
- `buttons.go`
- `alerts.go`
- `audit.go`

Must not contain:
- vault decryption
- JWT signing
- supervisor process management

### Exported API — locked at SDD-11

Path: `github.com/mrz1836/hush/internal/discord`

```go
// Package github.com/mrz1836/hush/internal/discord

// Approver gates every secret-claim path. *BotApprover is the
// production implementation; tests may substitute alternative
// implementations.
type Approver interface {
    RequestApproval(ctx context.Context, req ApprovalRequest) (Decision, error)
}

// ApprovalRequest is the input to every approval call. SupervisorName
// MUST be non-empty when SessionType is token.SessionSupervisor and
// MUST be empty otherwise.
type ApprovalRequest struct {
    MachineName    string
    ClientIP       string
    Reason         string
    Scope          []string
    RequestedTTL   time.Duration
    SessionType    token.SessionType
    SupervisorName string
}

// Decision is returned by RequestApproval only on the operator-Approve
// path. v0.1.0: ApprovedTTL == request.RequestedTTL exactly,
// Reason == "" — the fields exist for forward-compatible UX.
type Decision struct {
    Approved    bool
    ApprovedTTL time.Duration
    Reason      string
}

// BotConfig parameterises NewBotApprover.
type BotConfig struct {
    Token          *securebytes.SecureBytes
    OwnerID        string
    AppID          string
    AuditChannelID string
    DMRateLimit    time.Duration
}

// BotApprover is the production Approver, backed by a *discordgo.Session.
type BotApprover struct{ /* unexported */ }

// NewBotApprover constructs a Discord-backed Approver. Validation
// failures return the bare matching ErrMissing* sentinel; transport-
// down at boot is NOT a construction error (FR-013a).
func NewBotApprover(ctx context.Context, cfg BotConfig, logger *slog.Logger) (*BotApprover, error)

// DefaultDMRateLimit is the default value applied when
// BotConfig.DMRateLimit ≤ 0 (FR-021).
const DefaultDMRateLimit = 5 * time.Minute

// Sentinel errors. Compare via errors.Is. Static category messages —
// no token bytes, no ApprovalRequest fields, no key material.
var ErrDiscordUnavailable error // (a) available flag false at entry; (b) delivery failure mid-call; (c) WebSocket disconnected with in-flight request
var ErrApprovalDenied     error // operator clicked Deny
var ErrApprovalTimeout    error // ctx deadline elapsed before any operator action — wraps context.DeadlineExceeded
var ErrRateLimited        error // bucket for (SupervisorName, ClientIP) key already delivered within the configured window
var ErrMissingToken       error // cfg.Token == nil OR cfg.Token.Len() == 0
var ErrMissingOwnerID     error // cfg.OwnerID == ""
var ErrMissingAppID       error // cfg.AppID == ""
var ErrMissingLogger      error // logger == nil
```

Behavioural contract:
- *BotApprover never returns Decision{Approved: true} except on the
  operator-Approve interaction path (Constitution II non-negotiable;
  asserted by TestBotApprover_NeverAutoApprovesOnDiscordError and
  TestBotApprover_NoAutoApproveKnobExists).
- The available flag is checked BEFORE the rate-limit bucket so a
  transport-unavailable request never consumes a token (FR-021a).
- The monitor goroutine is owned by the constructor's ctx; on
  cancellation it closes the session, drains pending channels with
  ErrDiscordUnavailable, and exits within ≤100 ms (FR-026).
- DM templates use distinct visual prefixes for interactive (✅) vs
  [DAEMON] (⚠) requests so the operator never approves the wrong
  request type silently (FR-006).
- Reconnect uses hush-controlled exponential backoff capped at 60 s,
  retrying indefinitely until the constructor's ctx is cancelled
  (FR-013b).
- Bot token flows through *securebytes.SecureBytes; sentinel error
  messages are static categories (Constitution X). Absence asserted
  by TestBotApprover_TokenAbsentFromAllArtifacts.

SDD-28 will add the alert-class catalogue + tiered routing as a
sibling sub-package (`internal/discord/alerts`). SDD-28 MUST NOT alter
any symbol above.

### Exported API — locked at SDD-28

Package path: `github.com/mrz1836/hush/internal/discord/alerts`
(sibling sub-package of `internal/discord/`).

```go
// 1 type + 8 constants
type AlertClass string
const (
    AlertClassApprovalRequest               AlertClass = "approval-request"
    AlertClassDaemonRefreshRequest          AlertClass = "daemon-refresh-request"
    AlertClassValidatorStaleFailure         AlertClass = "validator-stale-failure"
    AlertClassChildExit78StaleFailure       AlertClass = "child-exit-78-stale-failure"
    AlertClassLogPatternStaleWarning        AlertClass = "log-pattern-stale-warning"
    AlertClassDiscordDisconnected           AlertClass = "discord-disconnected"
    AlertClassDiscordReconnected            AlertClass = "discord-reconnected"
    AlertClassVaultUnreachableAtBootTimeout AlertClass = "vault-unreachable-at-boot-timeout"
)

// 1 type + 3 constants
type Tier int
const (
    TierCritical Tier = iota // 0 → owner DM
    TierWarning              // 1 → audit channel
    TierInfo                 // 2 → slog INFO; zero Discord network call
)

// caller payload
type Alert struct {
    Class          AlertClass
    Tier           Tier // informational; Router re-derives from Class (FR-004)
    SupervisorName string
    MachineName    string
    Pattern        string
    Detail         string
    Time           time.Time
}

// consumer-side transport seam (R-003). *discord.BotApprover satisfies
// it via additive methods in internal/discord/bot_alerts.go.
type Sender interface {
    SendOwnerDM(ctx context.Context, message string) error
    PostChannel(ctx context.Context, channelID, message string) error
}

// opaque router
type Router struct { /* unexported */ }

// Constructor panics on nil sender / nil logger (Constitution IX
// startup-wiring exception); zero/negative bucket durations fall back
// to DefaultBucketWindow.
func NewRouter(sender Sender, auditChannelID string,
    perSupervisorBucket, perPatternBucket time.Duration,
    logger *slog.Logger) *Router

func (r *Router) Route(ctx context.Context, alert Alert) error

const DefaultBucketWindow = 1 * time.Minute

// 3 sentinels
var ErrAlertRateLimited  = errors.New("hush/discord/alerts: rate limited")
var ErrAlertTransport    = errors.New("hush/discord/alerts: transport failed")
var ErrUnknownAlertClass = errors.New("hush/discord/alerts: unknown class")
```

Behaviour contract:
- Class→tier binding is immutable: ApprovalRequest=Critical,
  DaemonRefreshRequest=Critical, ValidatorStaleFailure=Warning,
  ChildExit78StaleFailure=Critical, LogPatternStaleWarning=Warning,
  DiscordDisconnected=Warning, DiscordReconnected=Info,
  VaultUnreachableAtBootTimeout=Critical.
- `Alert.Tier` is caller-informational; Router re-derives the
  authoritative tier from `Alert.Class` (FR-004).
- Per-supervisor + per-pattern minimum-interval debounce with
  commit-on-success semantics. Either bucket exhausted →
  `ErrAlertRateLimited` (NO slog record; caller logs per FR-016).
- Transport failure refunds both buckets (commit-on-success per
  FR-012a) and returns `errors.Join(ErrAlertTransport, underlying)`.
- Templates render only `{SupervisorName, MachineName, Pattern,
  Detail}` with omit-empty lines. `Alert.Time`, `Alert.Class`, and
  `Alert.Tier` are NEVER reachable from the rendered body
  (Constitution X — class is implicit in the label prefix).
- slog allow-list: `{class, tier, supervisor, machine, pattern,
  outcome}`; level matrix Critical/Warning success=DEBUG, Info
  success=INFO, transport failure / unknown class = WARN, rate-limit
  suppression = NO RECORD.
- Zero new go.mod dependencies; stdlib only (`context`, `errors`,
  `fmt`, `log/slog`, `strings`, `sync`, `time`). The alerts package
  NEVER imports `github.com/bwmarrin/discordgo`,
  `github.com/mrz1836/hush/internal/discord`, or
  `github.com/mrz1836/hush/internal/vault/securebytes`.
- Zero goroutines spawned; Route is synchronous end-to-end on the
  caller's goroutine.

Additive methods on the SDD-11 `*BotApprover` (added in
`internal/discord/bot_alerts.go` — non-locked-surface extensions; the
SDD-11 `Approver` interface is untouched):

```go
func (a *BotApprover) SendOwnerDM(ctx context.Context, message string) error
func (a *BotApprover) PostChannel(ctx context.Context, channelID, message string) error
var _ alerts.Sender = (*BotApprover)(nil) // compile-time guard
```

SDD-28 MUST NOT add a 9th alert class, remove an existing class, or
re-tier any of the 8 documented classes (FR-005).

---

## `internal/supervise/`

Purpose:
- implement the daemon lifecycle model that makes hush viable for any long-running daemon under launchd/systemd

Expected responsibilities:
- supervisor state machine
- child command launch/restart/stop
- JWT session retention for daemon sessions
- secret refetch and silent refill
- refresh-window scheduler
- grace-window cache policy
- validator registry and execution
- log-pattern watchdog (alert-only)
- local Unix status socket
- PID file + flock split-brain guard
- child exit-code 78 handling

Likely files:
- `supervisor.go`
- `state.go`
- `child.go`
- `refill.go`
- `refresh.go`
- `validators.go`
- `status_socket.go`
- `pidfile.go`
- `watchdog.go`

Must not contain:
- generic cobra wiring
- vault file parser details

### Exported API — locked

> Filled by SDD-18..SDD-23 (config, state machine, child lifecycle, refill
> + refresh + grace cache, pidfile + status socket, CLI orchestrator) and
> SDD-26 + SDD-27 (validators, watchdog). Until then, this section is a
> placeholder.

### Exported API — locked at SDD-18

Sub-package path: `github.com/mrz1836/hush/internal/supervise/config`

This package owns the per-supervisor TOML schema, defaults catalog, and
strict-mode validation. SDD-18's symbols are additive — no SDD-06
(`internal/config`) symbol is altered. The two packages share a TOML
decoder (pelletier/v2 with strict mode) but live in separate import
paths and have no cross-package dependency.

```go
// Public types — read-only after Load returns. No field carries a secret
// value (Constitution X / FR-014); reference fields hold non-secret labels
// only.
type Supervisor struct {
    Name                   string
    Reason                 string
    ServerURL              string
    ClientMachineIndex     uint32
    SessionType            string         // always "supervisor" after Validate
    RequestedTTL           time.Duration
    RefreshWindow          string         // canonical "HH:MM-HH:MM"
    RefreshNudgeBefore     time.Duration
    BootRetryTimeout       time.Duration
    CacheSecretsForRestart bool
    CacheGraceTTL          time.Duration  // 0 when CacheSecretsForRestart is false
    StatusSocket           string         // absolute, ~-expanded
    PIDFile                string         // absolute, ~-expanded
    LogLevel               string         // one of {debug, info, warn, error}
    Scope                  []string       // non-empty after Validate

    Child      Child
    Discord    DiscordRouting
    Validators map[string]Validator
    Watchdog   Watchdog
}

type Child struct {
    Command            []string
    WorkingDir         string
    EnvPassthrough     []string
    RestartOnCleanExit bool
    RestartOnExit78    bool
}

type DiscordRouting struct {
    DaemonLabel    string
    AlertChannelID string
}

type Watchdog struct {
    Enabled          bool
    Patterns         []string
    MaxAlertsPerHour int
}

type Validator string  // values constrained by validatorAllowList

// Entry points.
func Load(ctx context.Context, path string) (*Supervisor, error)
func (s *Supervisor) Validate() error

// Default constants — every value exactly equals the corresponding
// documented default in docs/CONFIG-SCHEMA.md "Supervisor config".
var (
    DefaultRequestedTTL              time.Duration  // 20 * time.Hour
    DefaultRefreshWindow             string         // "09:00-10:00"
    DefaultRefreshNudgeBefore        time.Duration  // 30 * time.Minute
    DefaultBootRetryTimeout          time.Duration  // 10 * time.Minute
    DefaultCacheSecretsForRestart    bool           // false
    DefaultGraceWindow               time.Duration  // 60 * time.Minute (cache-enabled)
    DefaultLogLevel                  string         // "info"
    DefaultRestartOnCleanExit        bool           // true
    DefaultRestartOnExit78           bool           // false
    DefaultWatchdogEnabled           bool           // true
    DefaultWatchdogMaxAlertsPerHour  int            // 6
    DefaultWatchdogPatterns          []string       // []string{} (non-nil empty)
    DefaultDMRateLimit               time.Duration  // 5 * time.Minute (forwarded to discord.BotConfig)
    MaxGraceWindow                   time.Duration  // 4 * time.Hour (Constitution IV cap)
    MaxRequestedTTL                  time.Duration  // 24 * time.Hour (v0.1.0 ceiling)
)

// Sentinel errors — every documented rejection category maps to exactly
// one. errors.Is is the only matching primitive. Sentinel messages are
// static category strings; ErrUnknownValidator's wrapping includes only
// the offending validator NAME (RHS), never the LHS secret name (FR-014
// + FR-020 + Constitution X).
var (
    ErrTOMLDecode             error
    ErrUnknownField           error
    ErrMissingRequiredField   error
    ErrInvalidDuration        error
    ErrUnknownValidator       error
    ErrGraceWindowTooLong     error
    ErrGraceTTLWithoutCache   error
    ErrRefreshWindowFormat    error
    ErrRefreshWindowOrder     error
    ErrCommandEmpty           error
    ErrCommandPathRelative    error
    ErrScopeEmpty             error
    ErrSessionTypeInvalid     error
    ErrRequestedTTLOutOfRange error
    ErrServerURLInvalid       error
    ErrLogLevelInvalid        error
    ErrWatchdogRateInvalid    error
)
```

Constitution principles in scope: IV (TTL discipline + grace-window cap),
V (operator visibility — validator allow-list explicit), VIII (TDD +
≥95% coverage + fuzz target #5 `FuzzSuperviseTOML`), IX (no `init`, no
mutable globals beyond sentinel-class `var`s, no goroutines), X (no
secret values in struct or errors), XI (zero new direct deps — reuses
`pelletier/go-toml/v2` from SDD-06).

### Exported API — locked at SDD-19

Path: `github.com/mrz1836/hush/internal/supervise`

This package owns the supervisor daemon's lifecycle state machine and
snapshot store — the single source of truth that SDD-20 (child fork/
exec), SDD-21 (refill / refresh / grace), and SDD-22 (status socket)
all consult. The state model holds NO goroutines and NO side-effects:
it is purely a guarded data type. Subsequent chunks add behaviour on
top of this surface without modifying it.

```go
// State is the supervisor's lifecycle state. Exactly five values are
// valid (FR-019-1). The string forms are part of the operator-visible
// contract (status socket JSON, audit log).
type State string

const (
    StateFetching         State = "fetching"
    StateRunning          State = "running"
    StateAwaitingApproval State = "awaiting-approval"
    StateGraceRestart     State = "grace-restart"
    StateStopped          State = "stopped"
)

// Event is the closed vocabulary of lifecycle events the state
// machine recognizes (FR-019-21). The string forms are part of the
// audit-log contract.
type Event string

const (
    EventFetchOK               Event = "fetch-ok"
    EventFetchAuthRequired     Event = "fetch-auth-required"
    EventClaimDenied           Event = "claim-denied"
    EventClaimUnavailable      Event = "claim-unavailable"
    EventValidatorFailed       Event = "validator-failed"
    EventBootRetryExhausted    Event = "boot-retry-exhausted"
    EventChildExitClean        Event = "child-exit-clean"
    EventChildExitCrash        Event = "child-exit-crash"
    EventChildExit78Stale      Event = "child-exit-78-stale"
    EventRefreshRequested      Event = "refresh-requested"
    EventGraceRestartTriggered Event = "grace-restart-triggered"
    EventGraceRestartOK        Event = "grace-restart-ok"
    EventGraceExpired          Event = "grace-expired"
    EventApprovalGranted       Event = "approval-granted"
    EventStopRequested         Event = "stop-requested"
)

// Clock is the wall-clock seam consulted on every successful
// transition (FR-019-20). Single-method interface defined at the
// consumer per Constitution IX. Production wires time.Now(); tests
// wire a fake.
type Clock interface {
    Now() time.Time
}

// Store is the supervisor's guarded state container. Safe for
// concurrent Transition and Snapshot from many goroutines. Construct
// via NewStore; the zero value is NOT usable. Owns no goroutines
// (FR-019-12) and triggers no side-effects (FR-019-13).
type Store struct{ /* opaque */ }

// NewStore returns a fresh Store in StateFetching with
// LastTransitionAt stamped from clock.Now(). Passing a nil clock is
// a programmer error and panics at construction.
func NewStore(ctx context.Context, clock Clock) *Store

// Transition applies event under the write lock. Legal transitions
// are table-driven from the locked 5×15 state-table; illegal
// transitions return ErrInvalidTransition wrapped with the source
// state and rejected event named (FR-019-15). EventStopRequested is
// idempotent from every state including StateStopped (FR-019-17).
// Rejected transitions leave the store unchanged (FR-019-6).
func (s *Store) Transition(ctx context.Context, event Event) error

// Snapshot returns a defensive-copy point-in-time view of the store
// (FR-019-7, FR-019-8). The Token pointer (if non-nil) is shared
// with the store but bytes are borrow-only via SecureBytes.Use.
func (s *Store) Snapshot() Snapshot

// Snapshot is the by-value view returned by Store.Snapshot. Renders
// Token as "[redacted]" through slog (Constitution X).
type Snapshot struct {
    State            State
    ChildPID         int
    LastTransitionAt time.Time
    Token            *securebytes.SecureBytes
    Reason           string
}

// ErrInvalidTransition is wrapped by Transition when no edge exists
// for the (currentState, event) pair, when the event is outside the
// closed vocabulary, or when both. Identifiable via errors.Is.
var ErrInvalidTransition = errors.New("supervise: invalid transition")
```

Wrapping form for invalid transitions:

```go
fmt.Errorf("supervise: %w (state=%s event=%s)", ErrInvalidTransition, current, event)
```

Anti-API (deliberately NOT exported in this chunk): `SetToken` (token
write seam deferred to SDD-21), `State()` / `ChildPID()` per-field
accessors (readers go through `Snapshot()`), `Reset()` / `Stop()` (no
escape hatch from `StateStopped` — operators construct a fresh
`Store`), package-level `var Now = time.Now` (clock seam is the
`Clock` interface, not a swappable global), and any string-event /
`LoadReader` entry point.

Constitution principles in scope: IV (TTL discipline through the
state model — child exit never reaches `stopped`), V (status socket
sees `Snapshot`, exit-78 and validator failure are first-class state
edges), VIII (TDD-mandatory, ≥95% coverage, race-clean), IX (no
`init`, no goroutines, single-method `Clock` interface at the
consumer, errors wrapped with `%w`), X (`Token` redacts via SDD-02's
`*SecureBytes` contract; no secret bytes in errors).

### Exported API — locked at SDD-20

Path: `github.com/mrz1836/hush/internal/supervise`

SDD-20 extends the `internal/supervise` package with the supervised
**child runner**: fork/exec a daemon in its own process group, drain
its stdout/stderr through a 64 KB FIFO ring per stream, forward
signals to the daemon's PGID via a per-`Start` goroutine, return
`(exitCode, signal, err)` from `Wait`, and provide kernel-enforced
parent-death cleanup on Linux (`Pdeathsig`) plus a best-effort
kqueue death-watch on Darwin (R-009 SIGKILL gap documented). No
SDD-18 (`config/`) or SDD-19 (`state.go`) symbol is altered.

```go
// ChildConfig is the input to NewChild. Reference-shared slices
// (Command, Env) are read-only from the layer's perspective; the
// layer never logs, inspects, or copies Env values (Constitution X).
type ChildConfig struct {
    Command []string     // argv; element 0 absolute path (FR-020-1/2/3)
    Env     []string     // KEY=VALUE pairs; consumed by execve
    Dir     string       // working directory; "" inherits supervisor CWD
    Stdout  io.Writer    // stdout sink; nil → discard
    Stderr  io.Writer    // stderr sink; nil → discard
    Logger  *slog.Logger // structured logger; non-nil required
}

// Child is a handle to a single supervised daemon process. Single-
// use: once Wait returns the exit disposition, the cached *exec.Cmd
// is cleared (FR-020-11) and every subsequent Wait/Forward call
// returns ErrChildNotStarted. Owns no goroutines at rest; per Start
// spawns 3 goroutines on linux (forwarding + 2 drain) or 5 on
// darwin (forwarding + 2 drain + kqueue blocker + waker), all
// joined via Child.wg in Wait. Concurrent Wait callers: exactly
// one wins the sync.Once race per Clarification 1.
type Child struct{ /* opaque */ }

// NewChild constructs a Child handle from cfg. Pure value
// constructor — no validation, no syscalls. Allocates two ring
// buffers of capacity 64 KB. Panics if cfg.Logger is nil
// (Constitution IX startup-wiring exemption).
func NewChild(cfg ChildConfig) *Child

// Start validates cfg.Command (returns ErrCommandEmpty or
// ErrCommandPathRelative), then forks/execs with
// SysProcAttr.Setpgid=true plus platform-specific death-watch
// attributes. Spawns the per-Start goroutines.
func (c *Child) Start(ctx context.Context) error

// Wait blocks until the daemon exits and returns the three-tuple
// disposition (FR-020-8). exitCode is verbatim — Exit78 surfaces
// as 78 with no remap (FR-020-9, FR-020-10). Concurrent / re-
// entrant callers receive (0, 0, ErrChildNotStarted).
func (c *Child) Wait() (exitCode int, signal syscall.Signal, err error)

// Forward sends sig to the daemon's process group via the per-
// Start forwarding goroutine. Returns ErrChildNotStarted if no
// live child exists at call time (Clarification 2).
func (c *Child) Forward(sig os.Signal) error

// PID returns the daemon's process ID, or 0 if no child is live
// (FR-020-11). Pure scalar read; not an error path.
func (c *Child) PID() int

// Exit78 is the project-wide stale-credential exit-code contract
// (FR-020-9, Constitution V). Callers compare against this
// constant rather than a magic number.
const Exit78 = 78

// Sentinel errors. All identifiable via errors.Is.
var (
    ErrChildNotStarted     = errors.New("supervise: child not started")     // every "no live child" case (Clarification 2, R-011)
    ErrCommandEmpty        = errors.New("supervise: command empty")          // FR-020-2
    ErrCommandPathRelative = errors.New("supervise: command path not absolute") // FR-020-3 (distinct from ErrCommandEmpty)
)
```

Wrapping forms:

```go
fmt.Errorf("supervise: %w", ErrCommandEmpty)
fmt.Errorf("supervise: %w (got %q)", ErrCommandPathRelative, cmd[0])
fmt.Errorf("supervise: %w", ErrChildNotStarted)
```

Anti-API (deliberately NOT exported, locked off):
`Restart()` (single-use is the contract; FR-020-11), per-component
`ExitCode()` / `Signal()` accessors (the three-tuple Wait return
is locked, FR-020-8), `Stdout() []byte` / `Stderr() []byte` (no
read-back of the bounded ring; sinks are operator-supplied), a
distinct `ErrChildExited` sentinel (forbidden by Clarification 2),
a struct-typed `ExitDisposition` return, a `Wait(ctx)`
ctx-cancellable variant (`cmd.Wait` is uncancellable; cancellation
flows through `Forward(SIGTERM)`), per-stream ring-size override
(64 KB locked), and any `cmd/test-helper-*` binary
(`os.Executable()` re-invocation per R-012).

Constitution principles in scope: IV (lifecycle integrity — child
exit never reaches `stopped`; supervisor decides via SDD-21), VIII
(TDD-mandatory; race-clean; ≥90% coverage on `child{,_linux,_darwin}.go`),
IX (`os/exec` stdlib; no shell parsing; no `init()`; sentinel-class
`var Err...` and `const Exit78`; every per-Start goroutine has
explicit termination + top-frame `recover`), X (no secret values
in errors; overflow `slog.Warn` carries stream label only — never
buffer contents).

### Exported API — locked at SDD-21

Path: `github.com/mrz1836/hush/internal/supervise`

SDD-21 extends the `internal/supervise` package with the supervisor's
**credential lifecycle helpers**: a per-scope HTTP+ECIES `Refiller`,
a window+T-30 `Refresher`, and a per-name `Grace` cache holding
last-decrypted `*SecureBytes` across child exits. Every decrypted
secret stays in `*SecureBytes` end-to-end — no `string(...)` of vault
payload anywhere. Refresher owns the chunk's only goroutine type
(its tick loop); Grace owns zero. The locked API is exactly **three
constructors + four methods + two sentinels + one Clarification-5
addition (`Evict`)**.

```go
// Refiller fetches per-scope ECIES envelopes from the vault server
// using the cached supervisor JWT, decrypts to *SecureBytes, and
// commits to Grace.Set on full success or destroys all decrypted
// material on any error (atomic). Caller (SDD-23) drives retries.
type Refiller struct{ /* opaque */ }

// NewRefiller constructs a Refiller. Panics on any nil dep.
// Locked 3-arg signature; Grace handle, ECIES private key, and
// server URL prefix are wired post-construction by the orchestrator
// via the package-private (*Refiller).attach.
func NewRefiller(client *http.Client, store *Store, logger *slog.Logger) *Refiller

// Refill fetches every name in scopes, ECIES-decrypts each response,
// and on full success commits via Grace.Set. Returns ErrJTIUnknown
// (wrapped) on 401+{"error":"unknown_jti"}; otherwise returns a
// wrapped underlying error. Never retries internally.
func (r *Refiller) Refill(ctx context.Context, scopes []string) error

// Refresher schedules at most one refill callback fire per (window,
// calendar-day) pair plus at most one T-30 fallback fire per session.
// Single-shot — Run returns errRefresherAlreadyRan on second call.
type Refresher struct{ /* opaque */ }

// NewRefresher panics on a window-string parse failure or nil deps.
// Locked 4-arg signature.
func NewRefresher(window string, ttl time.Duration, refill func(ctx context.Context) error, logger *slog.Logger) *Refresher

// Run drives the tick loop. Returns ctx.Err() on cancellation; never
// any other error. Spawns ZERO sub-goroutines; refill callback runs
// inline. Non-nil refill error counts as "issued" (FR-021-11a) — log
// WARN, advance lastFiredDay, never propagate.
func (r *Refresher) Run(ctx context.Context) error

// Grace is the per-supervisor cache of last-decrypted *SecureBytes
// keyed by name. Effective TTL is min(window, 4h). enabled=false or
// window<=0 produces a permanently empty cache (caller retains
// ownership of any value passed to Set).
type Grace struct{ /* opaque */ }

// NewGrace caps window at 4h. Owns ZERO goroutines (Constitution IX);
// expired entries are destroyed lazily inside Get.
func NewGrace(window time.Duration, enabled bool) *Grace

// Get returns the cached *SecureBytes for name, or (nil, false) when
// absent / expired / cache disabled. On TTL elapse, Get destroys the
// entry inline and removes the map slot (R-008 lazy-evict).
func (g *Grace) Get(name string) (*securebytes.SecureBytes, bool)

// Set records (name, value) with expiry now()+window. Overwrite
// destroys the prior entry first. Disabled / window<=0 → silent no-op.
func (g *Grace) Set(name string, value *securebytes.SecureBytes)

// Evict destroys the entry for name (if present) and removes the
// map slot. Absent name is a silent no-op. (Clarification 5 / FR-021-16.)
func (g *Grace) Evict(name string)

// Sentinel errors. Both identifiable via errors.Is.
var (
    ErrJTIUnknown   = errors.New("supervise: vault rejected JWT (unknown jti)")
    ErrBootTimeout  = errors.New("supervise: boot retry timeout exhausted")
)
```

Anti-API (deliberately NOT exported): `RunSweeper` on `Grace`
(R-008 final — lazy-evict suffices), `ErrTransient` sentinel
(R-006 — stdlib error types provide the typed-distinguishability
FR-021-4 demands), any `string(decryptedBytes)` site (Constitution X
strict — JWT bearer-header materialization inside
`Snapshot.Token.Use(func(b []byte){})` closure is the SOLE permitted
`string(...)` site, scoped to JWT material, not vault payload), an
`init()` (Constitution IX), package-level mutable state, any new
direct dependency in `go.mod`.

Constitution principles in scope: IV (4h grace cap; lifecycle
integrity — chunk emits typed errors only, SDD-23 drives transitions),
V (Refresher WARN on rate-limit drop = operator-visible loud failure),
VIII (TDD-mandatory; ≥95% coverage on the three new files;
race-clean), IX (no init, no globals, single Refresher tick goroutine
with owner+ctx+termination+recover, sentinel-class `var Err…`, errors
wrap with `%w`), X (`*SecureBytes` flow only — no `string(decryptedBytes)`;
type-driven redaction via `LogValue() → "[redacted]"`; marker-byte
capture tests assert no leakage).

### Exported API — locked at SDD-22

Path: `github.com/mrz1836/hush/internal/supervise`

SDD-22 extends the `internal/supervise` package with two operator-
visibility primitives — a flock-backed PID file and a Unix domain status
socket. Sentinel errors `ErrPidLocked`, `ErrSocketPermsLoose`, and
`ErrAlreadyRunning` are exported alongside; the `StatusInputs` interface
is the consumer-defined seam for FR-12 fields not held by SDD-19's
`Snapshot`.

```go
// PID file (flock-backed exclusive supervisor lock).
type PidFile struct{ /* opaque */ }
func AcquirePidFile(path string) (*PidFile, error)
func (p *PidFile) Release() error

// Status socket (Unix domain; FS perms are the auth).
type StatusServer struct{ /* opaque */ }
func NewStatusServer(socketPath string, store *Store, logger *slog.Logger) *StatusServer
func (s *StatusServer) Run(ctx context.Context) error

// Consumer-defined interface — orchestrator implements this and wires it
// post-construction via the package-private (*StatusServer).attach.
type StatusInputs interface {
    Name() string
    SessionExpiresAt() time.Time
    RefreshWindowNext() time.Time
    ScopeHealthy() []string
    ScopeStale() []string
    LastAuthFailure() *time.Time
    ChildUptime() time.Duration
    DiscordConnected() bool
}

// Sentinel errors — identifiable via errors.Is.
var (
    ErrPidLocked        = errors.New("supervise: pidfile already locked")
    ErrSocketPermsLoose = errors.New("supervise: parent directory mode laxer than 0700")
    ErrAlreadyRunning   = errors.New("supervise: status server already running")
)
```

Files: `pidfile.go` (PidFile + AcquirePidFile + Release + ErrPidLocked +
ErrSocketPermsLoose), `socket.go` (StatusServer + NewStatusServer + Run
+ ErrAlreadyRunning + StatusInputs + private statusJSON DTO + private
ensureParentMode0700 helper), `socket_darwin.go` / `socket_linux.go`
(build-tagged test-helper `defaultRuntimeDir()` per OS), plus
`pidfile_test.go` and `socket_test.go`.

Behaviour contracts (locked at SDD-22):
- `AcquirePidFile`: opens with mode `0600`, parent at `0700`, single
  `unix.Flock(LOCK_EX|LOCK_NB)` attempt; OS-released stale flock acquired
  cleanly with zero retries, zero PID-text parse, zero `kill(0)` probe;
  refused acquire returns wrapped `ErrPidLocked` without modifying live
  owner's record.
- `Release`: unlock → close → `os.Remove` (best-effort; `IsNotExist` is
  success).
- `NewStatusServer`: pure value constructor, ZERO syscalls; panics on
  nil logger (Constitution IX startup-wiring exemption).
- `Run`: pre-listen sequence = parent-perms check → stale-inode unlink
  → `net.ListenConfig.Listen("unix", ...)` → `os.Chmod(0o600)`. Single-
  shot per instance; second `Run` returns wrapped `ErrAlreadyRunning`.
- Graceful shutdown on `ctx.Done()` is sub-second: watcher closes the
  listener and force-closes every tracked in-flight conn under `s.mu`;
  every spawned goroutine joins via `sync.WaitGroup` before `Run`
  returns.
- Status response is the FR-12 JSON document with all 10 fields present;
  `Snapshot.Token` is intentionally not a field of the `statusJSON` DTO
  (Constitution X / FR-022-13). One `Store.Snapshot()` + one `inputs`
  projection per request (FR-022-16).

Anti-API (deliberately NOT exported): `(*StatusServer).Stop()` /
`Shutdown()` / `Restart()`, `(*PidFile).IsAcquired()` / `PID()` /
`Path()`, `WithStatusInputs(...)` / functional-options builders, public
`ListenStatus(...)` global entry point, exported `ErrAlreadyReleased`,
`init()` in any new file, package-level mutable globals.

Constitution principles in scope: V (operator visibility — status
socket; FS perms `0600` socket / `0700` parent ARE the auth — no
bearer-token, no HTTP, no TCP loopback; `ErrSocketPermsLoose` refuses
laxer parents loudly; `TestSocket_NoTCPListenerOrHTTPServer` static
byte-grep guards regression), VIII (TDD-mandatory, ~22 named tests +
`FuzzStatusJSON_Encode` seeded for the Constitution-VIII §6 mandatory
fuzz target; race-clean; coverage ≥95% on platform-shim, high-90s on
behavior files modulo defensive syscall-error returns that require
fault injection to reach), IX (sentinel errors `var Err… =
errors.New(…)`; error wrap `%w`; no `init()`; no globals; consumer-
defined `StatusInputs` interface; explicit goroutine inventory: 1
accept loop in `Run`'s frame + 1 watcher per `Run` + N per-conn
handlers, each with owner+ctx+termination+top-frame `recover`; pure-Go
CGO=0; no new direct go.mod dependency — `golang.org/x/sys` already
in module), X (no `string(decryptedBytes)`; `Snapshot.Token` never
marshalled — `statusJSON` DTO has no `Token` field; defense-in-depth
marker-byte test `TestSocket_TokenInResponseRedacted` proves no leak;
`slog` records carry mode/identifier only, never connection payload).

### Exported API — locked at SDD-24

SDD-24 (supervisor orchestration glue) appends new symbols inside
`internal/supervise/` that compose the SDD-19..22 primitives into the
documented daemon lifecycle. The SDD-19/20/21/22 sections above are
UNCHANGED. SDD-24's lock string: "one struct + one Deps + two
constructors/methods (`NewLifecycle`, `Run`) + three interfaces
(`Validator`, `Alerts`, `Watchdog`) + one enum (`AlertClass`, 10 values
LOCKED) + one payload struct (`AlertPayload`) + four sentinels
(`ErrLifecycleAlreadyRan`, `ErrValidatorFailed`,
`ErrRefillFailedPostRunning`, `ErrClaimDenied`)".

```go
// Lifecycle is the supervisor orchestrator. Construct via NewLifecycle;
// drive via Run(ctx). Single-shot.
type Lifecycle struct { /* opaque */ }

// Deps carries every injected dependency NewLifecycle requires.
type Deps struct {
    Logger          *slog.Logger
    HTTPClient      *http.Client
    Clock           Clock
    ClaimSigningKey *ecdsa.PrivateKey
    DecryptKey      *ecdsa.PrivateKey
    AuditWriter     audit.Writer
    PidFile         *PidFile
    Validators      map[string]Validator
    Alerts          Alerts
    Watchdog        Watchdog
    TailscaleProbe  func(ctx context.Context) error
    VaultHzProbe    func(ctx context.Context, serverURL string) error
    MachineName, EphemeralPubKeyHex, ClientKeyFingerprint string
    NowFn           func() time.Time
    NonceFn         func() string
    RequestIDFn     func() string
}

func NewLifecycle(ctx context.Context, cfg *config.Supervisor, deps Deps) *Lifecycle
func (l *Lifecycle) Run(ctx context.Context) error

// Consumer-defined single-method interfaces — defaults are no-op.
type Validator interface {
    Validate(ctx context.Context, scope string, secret *securebytes.SecureBytes) error
}
type Alerts interface {
    Emit(ctx context.Context, class AlertClass, payload AlertPayload)
}
type Watchdog interface {
    OnStderrLine(ctx context.Context, line []byte)
}

// AlertClass — LOCKED at exactly 10 values (FR-026-016). SDD-28 MUST
// NOT extend without a spec amendment.
type AlertClass int
const (
    AlertClassValidatorFailure AlertClass = iota + 1
    AlertClassExit78
    AlertClassVaultRejectedJWT
    AlertClassRefillFailed
    AlertClassDiscordUnavailableOnClaim
    AlertClassRefreshDenied
    AlertClassRefreshTimeout
    AlertClassGraceEntered
    AlertClassLogPatternMatch
    AlertClassBootTimeout
)
func (c AlertClass) String() string

// AlertPayload — 3 string fields; structurally cannot carry secret bytes.
type AlertPayload struct {
    Scope      string
    ErrorClass string
    Reason     string
}

// Sentinels.
var ErrLifecycleAlreadyRan      = errors.New("supervise: lifecycle already ran")
var ErrValidatorFailed          = errors.New("supervise: validator failed")
var ErrRefillFailedPostRunning  = errors.New("supervise: post-running refill failed")
var ErrClaimDenied              = errors.New("supervise: claim denied (terminal)")
```

Behaviour contracts (locked at SDD-24):

- Boot path: pidfile (acquired by caller) → spawn StatusServer + Refresher +
  claimRefreshLoop → boot-retry loop (Tailscale + vault `/hz`) with
  exponential backoff jittered ±20% capped at 30s/attempt, total budget
  `cfg.BootRetryTimeout`. Exhaustion emits `AlertClassBootTimeout` +
  `ActionSupervisorBootTimeout` and returns wrapped `ErrBootTimeout`.
- `/claim` submission: caller-side signed via `sign.CanonicalJSON` +
  `sign.Sign` (SDD-08); JWT stored via package-private `Store.setToken`.
  503 + `discord_unavailable` → `AlertClassDiscordUnavailableOnClaim` +
  retry inside boot budget. 401 / non-503 4xx → wrapped `ErrClaimDenied`.
- Validator pass: `Deps.Validators[scope].Validate` (nil → no-op) runs
  per-scope before child start. Failure → `AlertClassValidatorFailure` +
  `ActionSupervisorStaleAlert` + `ActionSupervisorAwaitingApproval`
  (`cause=validator`) + `EventValidatorFailed` transition.
- Child env build: exactly one `string(*SecureBytes)` site at the OS
  fork boundary (Constitution X — documented FR-026-008/FR-026-028 site).
- Child-exit dispatch: code `0` → silent refill + restart; non-zero
  non-`Exit78` → same silent refill + restart; `Exit78` → emit
  `AlertClassExit78` + `ActionSupervisorChildExit78` +
  `ActionSupervisorStaleAlert` + `ActionSupervisorAwaitingApproval`
  (`cause=exit_78`) → DO NOT restart.
- Silent refill: `ErrJTIUnknown` → `AlertClassVaultRejectedJWT` +
  awaiting-approval; any other refill error post-running →
  `AlertClassRefillFailed` + awaiting-approval (FR-026-010a; never
  auto-retried).
- Refresh tick: claimRefreshLoop submits a fresh signed `/claim`,
  atomically swaps the JWT via package-private `Store.setToken`, child
  PID unchanged. Deny → `AlertClassRefreshDenied`; timeout →
  `AlertClassRefreshTimeout`; both preserve existing session.
- Status-socket refresh verb: state-conditional dispatch per Plan §10 —
  `awaiting-approval` drives the full refill+validate+restart;
  `running`/`grace-restart` coalesce via the in-flight refresh coalescer;
  `fetching`/`stopped` reject with state ack. Emits
  `ActionClientRefreshInvoked`.
- Shutdown: ctx cancel → `Child.Forward(SIGTERM)` → 10s grace →
  `Child.Forward(SIGKILL)` → 5s wait → `wg.Wait()` → cli shim's
  `defer pidfile.Release()`. Hard ceiling 15s.
- Goroutine inventory: 5 goroutines (StatusServer.Run + Refresher.Run +
  childWaitLoop + claimRefreshLoop + mainLoop) — each carries
  owner + ctx + termination + top-frame `recover` per Constitution IX.

Audit-vocabulary reconciliation: SDD-24 appends 12 constants to
`internal/audit/chain.go` (`ActionSupervisorSessionClaimed`,
`…SessionRefreshed`, `…SilentRefill`, `…ChildCleanExit`,
`…ChildExitCrash`, `…ChildExit78`, `…AwaitingApproval`,
`…StaleAlert`, `…GraceEntered`, `…GraceExited`, `…BootTimeout`,
`ActionClientRefreshInvoked`). The block remains append-only per the
file's line-33 header.

### Exported API — locked at SDD-27

Sub-package path: `github.com/mrz1836/hush/internal/supervise/watchdog`

SDD-27 ships the log-pattern watchdog — alert-only by design. The
concrete `watchdog.Watchdog` satisfies the `supervise.Watchdog`
interface (locked at SDD-24) via the additive `OnStderrLine` adapter,
so the SDD-23 CLI wiring passes a `*watchdog.Watchdog` directly into
`Deps.Watchdog`. The watchdog has **zero authority** over the
supervisor state machine (spec FR-003, Constitution V): a match emits
a typed `Event` and nothing else. The package's source code does not
name `Store`, `Refiller`, `Refresher`, `Grace`, or `Lifecycle`, and
its import set is stdlib-only plus `internal/supervise` for the
compile-time interface guard (verified by
`TestWatchdog_ZeroNewDependencies`).

```go
// Pattern is an operator-named regex predicate paired with a per-pattern
// alert refill interval. Caller pre-compiles Regex (FR-008); RateLimit
// derives from config.Supervisor.Watchdog.MaxAlertsPerHour as
// time.Duration((3600/MaxAlertsPerHour) * float64(time.Second)).
type Pattern struct {
    Name      string
    Regex     *regexp.Regexp
    RateLimit time.Duration
}

// Event is the typed alert emitted on every non-suppressed match.
// Consumed by SDD-28's downstream alert router.
type Event struct {
    Pattern string
    Line    string
    Time    time.Time
}

// Watchdog is the single-instance, single-run pattern engine.
type Watchdog struct { /* opaque */ }

// NewWatchdog validates the pattern set (rejects empty names, nil regex,
// non-positive RateLimit, nil alerts channel, nil logger, and duplicate
// names per FR-007a). Empty patterns slice is permitted (FR-014).
func NewWatchdog(patterns []Pattern, alerts chan<- Event, logger *slog.Logger) (*Watchdog, error)

// Ingest defensively copies line and enqueues it for evaluation. Non-
// blocking from the caller's perspective (FR-010a): a full queue drops
// the line with episode-coalesced WARN bookkeeping (Clarification Q4).
// Post-Run-return ingests are silent no-ops (FR-009).
func (w *Watchdog) Ingest(line []byte)

// Run drives the matcher loop. Single-shot per *Watchdog (R-012). On
// ctx.Done(), pending lines are dropped (R-007), one INFO log records
// the drop count, and Run returns the wrapped ctx.Err() within SC-004's
// 250ms budget.
func (w *Watchdog) Run(ctx context.Context) error

// OnStderrLine satisfies the supervise.Watchdog interface (SDD-24) by
// delegating to Ingest. ADDITIVE beyond the chunk-doc API; recorded in
// plan.md Complexity Tracking entry #3.
func (w *Watchdog) OnStderrLine(ctx context.Context, line []byte)

// Sentinel errors — identifiable via errors.Is.
var (
    ErrAlreadyRan           = errors.New("watchdog: Run already invoked")
    ErrEmptyPatternName     = errors.New("watchdog: pattern name is empty")
    ErrDuplicatePatternName = errors.New("watchdog: duplicate pattern name")
    ErrNilPatternRegex      = errors.New("watchdog: pattern Regex is nil")
    ErrNonPositiveRateLimit = errors.New("watchdog: pattern RateLimit must be positive")
    ErrNilAlertsChannel     = errors.New("watchdog: alerts channel is nil")
    ErrNilLogger            = errors.New("watchdog: logger is nil")
)
```

Files: `internal/supervise/watchdog/watchdog.go` (single source file —
Pattern, Event, Watchdog, NewWatchdog, Ingest, Run, OnStderrLine, 7
sentinels, internal `bucketState`/`dropEpisode`, `lineChannelCapacity =
512`, compile-time `var _ supervise.Watchdog = (*Watchdog)(nil)`
guard), `internal/supervise/watchdog/watchdog_test.go` (24 named tests,
24-of-24 race-clean, ≥90% statement coverage).

Behaviour contracts (locked at SDD-27):

- `NewWatchdog`: pattern-set validation runs before any allocation;
  duplicate-name rejection (FR-007a/Clarification Q5) is enforced at the
  type boundary as defence in depth on top of SDD-18's config validator.
  Bucket state initialised tokens=1 + lastRefill=`time.Now()` so the
  first match per pattern always alerts (FR-004 "starts full").
- `Ingest`: cancelled-atomic short-circuit + mutex-guarded enqueue +
  defensive `[]byte` copy + non-blocking channel send. Successful
  enqueue that follows a drop-episode flushes a single queue-full WARN
  carrying `dropped_count` and `first_drop_at`; episode-end semantic per
  Clarification Q4.
- `Run`: single matcher goroutine = the goroutine running `Run`. Inner
  loop selects `<-ctx.Done()` and `<-w.lines`; the matcher evaluates
  each line against every pattern in order, applying lazy-refill token
  buckets keyed by `Pattern.Name`. Rate-limit suppression emits one
  WARN per suppressed match (FR-006). Alert-output saturation emits one
  WARN per drop (R-010) and consumes the token to keep the rate-limit
  path consistent. On cancel: enqueueMu held, cancelled atomic set,
  open drop-episode flushed, pending lines drained and INFO-logged,
  Run returns `fmt.Errorf("watchdog: run cancelled: %w", ctx.Err())`.
- `OnStderrLine`: pure forwarder to `Ingest`; ctx is intentionally
  discarded (Ingest's signature is chunk-doc locked).

Constitution principles in scope: V (every drop emits WARN, zero silent
suppression; alert-only proven by `TestWatchdog_NeverTransitionsState`
+ the no-`Store`/`Refiller`/`Refresher`/`Grace`/`Lifecycle` source-text
invariant); VIII (24 named tests TDD-first, race-clean, 96.4%
statement coverage — exceeds 90% chunk-doc target); IX (zero `init()`;
zero mutable package-level state; seven sentinel-class
`var Err... = errors.New(...)` globals — exempt per SDD-21 precedent;
one unexported `const lineChannelCapacity = 512`; single matcher
goroutine with explicit owner + ctx + termination + top-frame
`defer recover`; ctx-first `Run(ctx)`); X (no `internal/vault/securebytes`
import, no `*SecureBytes` field — verified by
`TestWatchdog_NoSecureBytesStringConversion`; the single `string(...)`
site is `string(line)` for non-secret `Event.Line` construction; all
three WARN emission sites exclude matched-line content per
Clarification Q2 — sentinel-byte assertions in
`TestWatchdog_RateLimitBlocksExcess`,
`TestWatchdog_QueueFullDropEpisodeOnceWARN`, and
`TestWatchdog_AlertOutputSaturatedDropsWARN` prove it); XI (zero new
direct `go.mod` dependencies — verified by
`TestWatchdog_ZeroNewDependencies`).

Anti-API (deliberately NOT exported): a `Clock` interface (test seam
is the unexported `now func() time.Time` field per Constitution IX
"interfaces at the consumer"); pattern compilation helpers (the caller
pre-compiles via `regexp.Compile` per FR-008 + R-015); reconfiguration
or pattern-set mutation entry points (FR-007 — a new pattern set
requires a fresh `*Watchdog`); package-level mutable state of any
kind; an `init()` function; closure of the alerts channel (R-011 —
SDD-28 routes multi-producer alerts and would break if any single
producer closed).

---

### Exported API — locked at SDD-26

Sub-package path: `github.com/mrz1836/hush/internal/supervise/validators`

SDD-26 ships the pre-flight credential `Validator` interface plus the
five built-in providers locked by SDD-18's TOML allow-list (anthropic,
anthropic-oauth, openai, google-ai, github). Each validator answers
"is this credential currently accepted by the upstream provider?" via
a single read-only HTTP probe and returns one of three typed sentinel
errors. The credential is consumed exclusively via
`securebytes.Use(fn)`; the single `string(...)` site documented at
research reference R-008 is `req.Header.Set(<header>, string(buf))`
inside the Use-scoped builder (variable named `buf`, not `secret`).
No credential value, `*http.Request`, or `*http.Response` is ever
passed to a logger, error formatter, or other byte sink. Coverage:
98.1% statement coverage with 103 named tests (18 shared +
17 × 5 per-provider), race-clean.

```go
// Validator answers "is this credential currently accepted by the
// upstream provider?" via a single read-only HTTP probe.
type Validator interface {
    Validate(ctx context.Context, secret *securebytes.SecureBytes) error
}

// Registry is the read-only lookup mapping each FR-010 name to its
// concrete Validator. Concurrent Get calls are race-safe.
type Registry struct { /* opaque */ }

// NewRegistry builds a Registry pre-populated with the five built-in
// validators. Passing nil yields a default *http.Client with the
// FR-012 5-second timeout and redirect-follow disabled per FR-021.
func NewRegistry(httpClient *http.Client) *Registry

// Get returns (registered Validator, true) for any of the five fixed
// FR-010 lowercase names; (nil, false) for everything else.
func (r *Registry) Get(name string) (Validator, bool)

// Per-provider constructors. Each returns the Validator interface and
// pins the endpoint URL per research.md R-003a..R-003e.
func NewAnthropic(httpClient *http.Client) Validator       // x-api-key + anthropic-version
func NewAnthropicOAuth(httpClient *http.Client) Validator  // Authorization: Bearer + anthropic-version
func NewOpenAI(httpClient *http.Client) Validator          // Authorization: Bearer
func NewGoogleAI(httpClient *http.Client) Validator        // x-goog-api-key (header only — never ?key=)
func NewGitHub(httpClient *http.Client) Validator          // Authorization: token + Accept

// Sentinel errors — Constitution IX sentinel-class read-only globals.
var (
    ErrStaleCredential  = errors.New("validators: credential rejected by provider")
    ErrValidatorTimeout = errors.New("validators: probe timeout")
    ErrValidatorNetwork = errors.New("validators: probe network failure")
)
```

Files: `internal/supervise/validators/validators.go` (shared
machinery: interface, Registry, three sentinels, `doRequest`,
`classifyTransportError`, `emitWarnAndWrap`, name + endpoint
constants), `anthropic.go`, `anthropic_oauth.go`, `openai.go`,
`google_ai.go`, `github.go` (one per provider — unexported
`<provider>Validator` struct + `New<Provider>` + `set<Provider>Auth`
builder), `validators_test.go` (18 shared tests + per-provider
behaviour helpers), `anthropic_test.go`, `anthropic_oauth_test.go`,
`openai_test.go`, `google_ai_test.go`, `github_test.go` (17 named
tests per provider), and `export_test.go` (test-only
`SetLoggerForTest` seam — R-014).

Constitution principles in scope: V (every failure outcome emits one
WARN slog record via the single `emitWarnAndWrap` site; success path
is DEBUG-only; zero silent suppression); VIII (TDD-first, 103 named
tests, race-clean, ≥90% statement coverage, sentinel-leak fuzz-style
assertion per provider for FR-015/SC-006); IX (single-method
`Validator` interface; three sentinel-class globals exempt per
Constitution IX; zero `init()`; zero mutable package-level state;
five `<provider>Name` + four `<provider>Endpoint` + four `outcome*` +
`anthropicVersionHeader` + `defaultTimeout` are compile-time `const`
declarations; zero goroutines spawned by the package; ctx-first
`Validate(ctx, secret)`); X (`*SecureBytes` is the only credential
surface; `Use(fn)`-scoped builder consumption with fresh `[]byte` +
zero-loop; log-attribute allow-list = `{validator, outcome, status}`;
`*http.Request` / `*http.Header` never threaded to logger / error /
byte sink); XI (stdlib-only — `context`, `errors`, `fmt`, `io`,
`log/slog`, `net`, `net/http`, `time` — plus `internal/vault/securebytes`).

Anti-API (deliberately NOT exported): per-provider concrete struct
types (kept unexported so callers consume only via the `Validator`
interface); a sixth registry name (SC-007 closed set; runtime
extension is post-v0.1.0 per `docs/DAEMONS.md` §5); any setter for
the `*http.Client` after construction (clients are passed at
constructor time only); silent suppression of any failure mode;
mutable package-level state; an `init()` function.

---

## `tests/integration/` — Exported API — locked at SDD-25

**Purpose.** Lifecycle integration test suite owning AC-10 (15 named
scenarios from `docs/LIFECYCLE-SCENARIOS.md`, with Scenarios 9 and 11
each split into two test functions → 17 total `Test_Scenario_NN_<slug>`
symbols locked in spec FR-002). The suite composes the real
`internal/supervise.Lifecycle` (SDD-24) plus the real `internal/server`,
`internal/audit`, `internal/token`, `internal/vault`,
`internal/transport/ecies`, `internal/transport/sign`,
`internal/keys`, and `internal/cli` packages end-to-end; **only four
boundaries are mocked** — Discord (`testutil.DiscordStub`), the five
provider validator HTTP upstreams (loopback `httptest.Server`s per
provider), the wall clock (`harness.FakeClock`), and the Tailscale
reachability probe (`Deps.TailscaleProbe` stub).

**Build tag.** Every file under `tests/integration/` carries
`//go:build integration`. Default `go test ./...` compiles zero files
in this tree. Suite invocation: `magex test:race -tags=integration
./tests/integration/...`.

**File inventory.** Locked at 6 files in `tests/integration/harness/`
plus `tests/integration/lifecycle_test.go` (TestMain +
integration-child-mode dispatcher + RoundTripper allow-list) and
`tests/integration/scenarios_test.go` (17 locked test symbols). The
harness package contains:

- `harness/log_capture.go` — `slog` sink + cross-stream
  `AssertSentinelAbsent`
- `harness/vault.go` — `*TestVault` wraps `testutil.NewTestVault`
  with `RegisterClient` / `Rotate` / `AuditPath` accessors
- `harness/discord.go` — `*TestDiscord` wraps `testutil.DiscordStub`
  with `SetConnected` / alert recorder / `AsSuperviseAlerts` adapter
- `harness/child.go` — `*TestChild` re-invokes the test binary via
  `os.Executable()` in scripted-child mode (`--integration-child-mode
  --exit-code=N --lifetime=D --emit-stderr-pattern=P`)
- `harness/server.go` — placeholder for the in-process
  `internal/server` composition + per-provider `httptest.Server`
  validator mocks (full wiring lands in subsequent SDD-25 chunks)
- `harness/supervisor.go` — placeholder for the
  `*supervise.Lifecycle` composition + `FakeClock` + status-socket
  reader + audit subsequence helper

**Harness types are intentionally NOT signature-frozen** per the
SDD-25 chunk-doc entry contract — they evolve as new scenarios surface
needs. The behavioural contract (Builder properties, four-contract
scenario assertion shape, six-stream `AssertSentinelAbsent` coverage)
is locked in `specs/025-lifecycle-harness/contracts/harness-api.md` and
`specs/025-lifecycle-harness/contracts/scenario-assertions.md`.

**Import discipline.** A `depguard` rule in `.golangci.json`
(`no-integration-harness-in-production`) forbids any non-test file
outside `tests/integration/` from importing
`github.com/mrz1836/hush/tests/integration/harness`.

**Chunk-1 delivery (this PR)**: harness scaffolding + Scenario 14
(`Test_Scenario_14_DuplicateStart`) green under `-race` with 5/5
flake-free runs. The remaining 16 scenarios surface as `t.Fatalf`
"harness wiring not yet complete" failures on every invocation per spec
FR-001 (no `t.Skip` permitted) — those failures are the load-bearing
operator signal that AC-10 is still partially unmet.

---

## `deploy/` — Exported API — locked at SDD-29

**Purpose.** Operator-facing deploy artefacts for v0.1.0: the four
files an operator copies to a fresh macOS or Linux host to turn a
built `hush` binary into a runnable daemon. No exported Go symbols —
see `deploy/install.sh --help` (or the script header) for installation
usage. The Go integration test suite lives at `tests/deploy/`
(`//go:build integration`) and asserts the operator-facing contract.

**File inventory (locked).**

- `deploy/hush.plist` — launchd job for the hush vault server on
  macOS. `Label=com.hush.server` (product identifier, not
  operator-specific); `ProgramArguments=["/usr/local/bin/hush",
  "serve", "--config", "/usr/local/etc/hush/config.toml"]`;
  `UserName=_hush` (non-root, per macOS system-user convention);
  `RunAtLoad=true`; `KeepAlive=true`; standard log paths under
  `/usr/local/var/log/`. Mode `0644`. install.sh sed-substitutes
  `<string>_hush</string>` only if `HUSH_USER` overrides the default.
- `deploy/hush.service` — systemd unit for the hush vault server on
  Linux. Sections `[Unit]`/`[Service]`/`[Install]`;
  `User=@HUSH_USER@` (install.sh substitutes to resolved
  `${HUSH_USER}`, default `hush`);
  `ExecStart=/usr/local/bin/hush serve --config
  /etc/hush/config.toml`; `Restart=on-failure`; hardening directives
  `NoNewPrivileges`/`ProtectSystem=strict`/`ProtectHome=true`/
  `PrivateTmp=true`. Mode `0644`.
- `deploy/install.sh` — idempotent installer (bash 3.2+ compatible;
  `#!/usr/bin/env bash` + `set -euo pipefail`). Reads 5 env vars
  (`PREFIX`, `HUSH_USER`, `HUSH_STATE_DIR`, `HUSH_INSTALL_ROOT`,
  `HUSH_SOURCE_BIN`) plus a `HUSH_FORCE_OS` test-only escape hatch.
  Executes a 7-step flow: (1) idempotent system-user creation
  (`dscl`/`useradd`); (2) `install -d -m 0700 -o ${HUSH_USER}
  ${STATE_DIR}` (Constitution X); (3) macOS-only `tmutil
  addexclusion` (Constitution XI non-negotiable; hard-fails exit-4
  if `tmutil` missing; deduped via in-state-dir marker so re-runs
  invoke at most once); (4) `install -m 0755` of the binary;
  (5) `install -m 0644` of the service file with `@HUSH_USER@`
  substitution on Linux + optional `<string>_hush</string>`
  substitution on macOS; (6) Linux-only `systemctl daemon-reload`;
  (7) byte-identical-across-reruns next-steps banner. **Creates
  ZERO Keychain entries** (FR-003 absolute lock); the banner prints
  the operator-runnable `security add-generic-password -T
  "/usr/local/bin/hush" ...` invocation. Mode `0755`. Exit codes
  `0=success/no-op` / `1=generic` / `2=bad-input` / `3=privilege` /
  `4=missing-tool`; every stderr message follows
  `install.sh: <stage>: <reason>` format.
- `deploy/supervise-launch.sh.template` — generic per-daemon
  launcher operators copy and customise. Single-line core
  `exec /usr/local/bin/hush supervise --config '<CONFIG_PATH>'`.
  Three placeholders (`<NAME>`, `<KEYCHAIN_ITEM>`, `<CONFIG_PATH>`)
  with documenting header comment block + load-bearing DO-NOT
  warning against `hush request --exec` (which would re-prompt on
  every restart and defeat Constitution IV's TTL discipline).
  Pre-flight grep guard exits `78` (`EX_CONFIG`) on any
  unsubstituted placeholder. `hush request --exec` appears EXACTLY
  ONCE inside the DO-NOT warning comment (filtered out by the
  non-comment-line grep used by `TestDeploy_LauncherTemplateExecsSupervise`).
  Mode `0644` (template, not directly executable). install.sh does
  NOT install this file — the operator copies it per daemon.

**CI runner prerequisites.** `bash -n` is the absolute floor (FR-024
+ SC-008) and runs on every runner without setup. `shellcheck` is
RECOMMENDED but optional — runners without it skip the shellcheck
step with a logged note. To make the shellcheck gate load-bearing,
add `apt-get install -y shellcheck` (Ubuntu) or
`brew install shellcheck` (macOS) to the runner bootstrap. The
developer workstation that authored SDD-29 lacked shellcheck;
follow-up CI hardening should install it.

**Verification harness.** Go integration tests under `tests/deploy/`
gated by `//go:build integration`:
`TestDeploy_InstallIdempotent` / `TestDeploy_InstallRefusesUnsupportedOS` /
`TestDeploy_InstallRefusesMissingBinary` /
`TestDeploy_InstallRefusesMissingTmutil` (darwin-guarded) /
`TestDeploy_InstallBannerByteIdentical` / `TestDeploy_PlistParsesAsXML` /
`TestDeploy_ServiceParsesAsINI` / `TestDeploy_LauncherTemplateExecsSupervise` /
`TestDeploy_NoOperatorSpecificNames` / `TestDeploy_AllShellFilesParse`.
Runner: `magex test:race -tags=integration -run TestDeploy_
./tests/deploy/...`. Fixtures `tests/deploy/testdata/tmutil_stub.sh`
(recording shim placed first on PATH for macOS runs) and
`tests/deploy/testdata/fake-hush` (zero-byte stand-in for
`HUSH_SOURCE_BIN`).

**Anti-API (deliberately NOT in scope).** install.sh creates ZERO
Keychain entries / reads ZERO secret material (FR-003 lock). No
file under `deploy/` hard-codes any operator-specific daemon name,
hostname, Tailscale tag, or Discord ID — the
`TestDeploy_NoOperatorSpecificNames` denylist grep
(`openclaw|hermes|mrz|100.90.|tag:trusted`) returns zero matches
across the four committed files. The launcher template uses `hush
supervise` exclusively; any active `hush request --exec` line is a
contract violation. `${HUSH_STATE_DIR}` is never backed up
(Constitution XI). The `deploy/examples/` subtree (per-operator
supervisor TOMLs) is SDD-30 territory and out of SDD-29's
operator-agnostic scope.

### Exported API — locked at SDD-30

- `deploy/examples/supervisors/example-daemon.toml` — the canonical
  operator-facing supervisor template. Fully commented, fully generic;
  every field documented in
  [`docs/CONFIG-SCHEMA.md`](CONFIG-SCHEMA.md) §Supervisor-config
  appears as an active example value with an inline
  `# <purpose>. Required.` or `# <purpose>. Default: <loader-default>.`
  comment. Top-of-file comment block links to
  [`docs/TAILSCALE-ACLS.md`](TAILSCALE-ACLS.md) (Constitution VI —
  network-layer hardening) and
  [`docs/CLEAN-MACHINE.md`](CLEAN-MACHINE.md) (Constitution I —
  agent-host hygiene), plus the per-binary Keychain ACL contract
  (AC-6) and the `[child].command[0]`-as-ACL-bound-path callout.
  Placeholder taxonomy: human-readable slugs (`example-daemon`,
  `your-daemon-binary`, `Example Daemon`), scoped secret names
  (`EXAMPLE_API_KEY_1`, `EXAMPLE_API_KEY_2`), and a single
  `REPLACE_ME` marker for `[discord].alert_channel_id`.
  `server_url = "http://100.64.0.1:7743/h/example"` — first usable
  CGNAT address inside `100.64.0.0/10`, canonical hush port. Validated
  by `TestExamples_GenericTOMLValidates` (loader round-trip) +
  `TestExamples_NoOperatorSpecificNames` (FR-007 grep gate, seed list
  empty at SDD-30 author time), both in
  `internal/supervise/config/example_test.go`. SDD-30 also re-verified
  [`docs/TAILSCALE-ACLS.md`](TAILSCALE-ACLS.md) and
  [`docs/CLEAN-MACHINE.md`](CLEAN-MACHINE.md) against the current
  spec/config/install.sh and applied patch-level alignment (R-002,
  R-003).

---

## `internal/logging/`

Purpose:
- centralize structured logging and redaction
- prevent accidental secret leakage into logs

Expected responsibilities:
- logger creation/config
- field redaction helpers
- audit log append helpers
- log format selection for TTY vs JSON if needed

Likely files:
- `logger.go`
- `redact.go`
- `audit_writer.go`

Must not contain:
- business decisions about approval or auth policy

### Exported API — locked at SDD-05

Path: `github.com/mrz1836/hush/internal/logging`

```go
// Format selects the log output format.
type Format int

const (
    FormatAuto Format = iota // auto-detect: text on TTY, JSON otherwise (zero value)
    FormatText               // force human-readable text
    FormatJSON               // force JSON
)

// Options configures a logger constructed by New.
type Options struct {
    Level  slog.Level // minimum emit level; zero value == slog.LevelInfo
    Format Format     // format selector; zero value == FormatAuto
    Out    io.Writer  // destination writer; nil == os.Stderr
}

// New constructs a *slog.Logger with the package's redaction handler chain
// installed. FormatAuto chooses text for a TTY *os.File, JSON otherwise.
// ERROR records in JSON include source location; all other combinations do not.
// The returned logger is safe for concurrent use. Never mutates slog.Default.
func New(opts Options) *slog.Logger

// RedactString scans s against every pattern in RedactPatterns and replaces
// each match with "[redacted]". Returns s byte-identical when no pattern
// matches. Safe for concurrent use; idempotent.
func RedactString(s string) string

// RedactPatterns is the compiled set of credential-class regexes (one per
// class from docs/SECURITY.md §1.1). Read-only after first use via sync.Once.
// Currently four entries: Anthropic sk-ant-, OpenAI sk-proj-, GitHub ghp_,
// AWS AKIA[0-9A-Z]{16}.
var RedactPatterns []*regexp.Regexp
```

---

## Dependency rules

Allowed dependency direction:

- `cmd/hush` → `internal/cli`
- `internal/cli` → all domain packages as orchestration only
- `internal/server` → `config`, `vault`, `token`, `transport`, `discord`, `logging`
- `internal/supervise` → `config`, `token`, `transport`, `logging` and client-facing fetch helpers
- `internal/discord` should not import `internal/server`
- `internal/vault` should not import `internal/server` or `internal/discord`
- `internal/keys` should stay low-level and reusable

If two packages want each other, the boundary is wrong.

---

## Ownership by feature

- vault encryption at rest → `internal/vault`, `internal/keys`
- JWT issuance and policy → `internal/token`
- request authenticity and response confidentiality → `internal/transport`
- approval UX → `internal/discord`
- HTTP API surface → `internal/server`
- long-running daemon behavior → `internal/supervise`
- human/agent entrypoints → `internal/cli`

---

## Phase 0 completion check

This file is sufficient when an implementation agent can answer all of these without guessing:

- where does JWT logic live?
- where does ECIES transport logic live?
- where does the daemon state machine live?
- where do config schemas and validation live?
- where does Discord approval rendering live?
- where does vault reload and zeroization live?

If any of those answers is fuzzy, Phase 0 is still incomplete.

---

## `.github/workflows/` — Exported API — locked at SDD-31

The three workflows below own AC-9 (release gates). Their step lists,
required check names, and matrix shape are fixed by SDD-31 contracts —
do not rename a job without coordinating a branch-protection update.

- **`.github/workflows/ci.yml`** — per-PR matrix gates (FR-004…FR-019).
  Runs on `pull_request → main`, `push → main`, and `workflow_dispatch`
  across macos-arm64 + linux-amd64 on the Go toolchain pinned in
  `go.mod`. Eleven gates per leg: no-vendor (FR-017), no-CGO (FR-018),
  format-check (FR-004), lint (FR-005), pre-commit (FR-007), test:race
  with coverprofile (FR-006), govulncheck filtered through
  `.govulncheck-allow.yml` (FR-008), gitleaks (FR-009), 30 s smoke
  across the six canonical fuzz targets (FR-010), coverage artefact
  upload (FR-011, linux leg). Two downstream jobs: `coverage-threshold`
  (linux-only — runs `.github/scripts/coverage-threshold/...` against
  the artefact, FR-012/013/014/015/016) and `coverage-upload`
  (codecov/codecov-action@v4 with `fail_ci_if_error: true`, FR-011).
  Required branch-protection checks: `ci / gates (macos-arm64)`,
  `ci / gates (linux-amd64)`, `ci / coverage-threshold`,
  `ci / coverage-upload`.
- **`.github/workflows/fuzz-cron.yml`** — nightly deep-fuzz cron
  (FR-020/021/022). Schedule `0 7 * * *` UTC plus `workflow_dispatch`
  with `seconds_per_target` input (default `300`). Linux-amd64
  matrix-by-target (six legs in parallel — wall-clock ~ matrix-budget
  not 6 × budget) across the same six canonical targets ci.yml smokes
  (FR-029 lockstep — never edit one list without the other). Failing
  legs preserve `testdata/fuzz/<Target>/` as a 30-day `corpus-<Target>`
  artefact for local repro.
- **`.github/workflows/release.yml`** — tag-driven GoReleaser + cosign
  keyless via Sigstore Fulcio OIDC (FR-023…FR-027). Triggers on
  `push.tags: ['v*']` + `workflow_dispatch`. Permissions include the
  load-bearing `id-token: write` so cosign can mint a Fulcio cert
  whose Subject Alternative Name binds to the release tag ref. Inherits
  `CGO_ENABLED=0` at the job env and via `.goreleaser.yml`'s top-level
  env (FR-023 belt-and-braces). Produces four binaries
  (darwin/linux × amd64/arm64) plus a SHA-256 checksums manifest plus
  the manifest's `.sig` + `.pem` (manifest-only signing per FR-025 —
  never per-binary).

Supporting Go tooling under `.github/scripts/`:

- **`.github/scripts/coverage-threshold/`** — the FR-016 byte-equality
  enforcer. `main.go` (≤ 40 lines: flag parse + exit codes 0/1/2/3),
  `compute.go` (pure-fn parser + threshold checker, stdlib-only,
  sentinel errors `ErrMalformedCoverOut` / `ErrCoverageBelowThreshold`
  / `ErrConstitutionMismatch`), `compute_test.go` (table-driven; one
  case asserts byte-equality with the security-critical fenced block in
  `.specify/memory/constitution.md`). Invoked by ci.yml's
  `coverage-threshold` job.
- **`.github/scripts/govulncheck-filter/`** — FR-008 waiver authority.
  Reads `.govulncheck-allow.yml` (single source of truth — PR
  descriptions are non-authoritative), filters out findings whose OSV
  ID has an unexpired waiver, exits non-zero on any remainder. Invoked
  by ci.yml's `govulncheck` step.

---

## Symbol manifest (drift-detection anchor)

This section is the machine-readable list of every exported symbol in
every `internal/*` package, regenerated from `go doc -short -all` by
`scripts/check-package-map-vs-code.sh` (FR-013 / SC-002 / R-001). The
script computes the current symbol set, compares it against the fenced
block below, and exits non-zero on drift.

The manifest is the load-bearing source of truth for "what is exported
from each `internal/*` package as of the most recent commit." The
prose-and-table sections above remain authoritative for **why** each
symbol exists and **how** it composes into the system; the manifest is
authoritative for **what** is currently exported. Discrepancy between
the two is itself a finding: prose may reorganise without re-locking,
but the manifest may not lag behind code.

### Update procedure

1. Modify package code (add / remove / rename exported symbols).
2. Run `scripts/check-package-map-vs-code.sh` from the repo root —
   exit code 1 with `+ doc-only` / `- code-only` lines names every
   drifting symbol.
3. Edit the fenced block below to match the script's reported state
   (remove any `+ doc-only` line, add any `- code-only` line) until
   the script exits 0.
4. Update the relevant `## \`internal/<pkg>\`` prose section above so
   the **why** stays in sync with the **what** — the script does not
   enforce this; reviewers do.

The block is sorted lexicographically (`<package> <symbol>`) so diffs
stay readable.

<!-- symbol-manifest: BEGIN (drift-detection anchor — regenerate via scripts/check-package-map-vs-code.sh) -->

```
internal/audit ActionAuditMirrorFailed
internal/audit ActionClientRefreshInvoked
internal/audit ActionDiscordDisconnected
internal/audit ActionDiscordReconnected
internal/audit ActionFilePermCheckFailed
internal/audit ActionRevokeBadRequest
internal/audit ActionRevokeBadSignature
internal/audit ActionRevokeIdempotentAlreadyRevoked
internal/audit ActionRevokeNonceReplay
internal/audit ActionRevokeStaleTimestamp
internal/audit ActionRevokeSucceeded
internal/audit ActionSecretBadRequest
internal/audit ActionSecretBadToken
internal/audit ActionSecretInternalError
internal/audit ActionSecretMissing
internal/audit ActionSecretOutOfScope
internal/audit ActionSecretRetrieved
internal/audit ActionSecretTokenExpired
internal/audit ActionServerStart
internal/audit ActionServerStop
internal/audit ActionSupervisorAwaitingApproval
internal/audit ActionSupervisorBootTimeout
internal/audit ActionSupervisorChildCleanExit
internal/audit ActionSupervisorChildExit78
internal/audit ActionSupervisorChildExitCrash
internal/audit ActionSupervisorGraceEntered
internal/audit ActionSupervisorGraceExited
internal/audit ActionSupervisorSessionClaimed
internal/audit ActionSupervisorSessionRefreshed
internal/audit ActionSupervisorSilentRefill
internal/audit ActionSupervisorStaleAlert
internal/audit ActionVaultReloaded
internal/audit ChainError
internal/audit DiscordMirror
internal/audit ErrAlreadyRun
internal/audit ErrAuditChainBroken
internal/audit ErrChainLocked
internal/audit ErrChainTailUnreadable
internal/audit ErrEmptyAction
internal/audit ErrInvalidKey
internal/audit ErrInvalidLogger
internal/audit ErrInvalidPath
internal/audit ErrShutdown
internal/audit Event
internal/audit MirrorSession
internal/audit NewDiscordMirror
internal/audit NewWriter
internal/audit ReasonHashMismatch
internal/audit ReasonPrevHashMismatch
internal/audit ReasonSeqGap
internal/audit ReasonSignatureInvalid
internal/audit Verify
internal/audit Writer
internal/cli Commit
internal/cli Date
internal/cli Execute
internal/cli ExitAuth
internal/cli ExitConfigStale
internal/cli ExitErr
internal/cli ExitInputErr
internal/cli ExitNotFound
internal/cli ExitOK
internal/cli ExitPerm
internal/cli Stream
internal/cli Version
internal/config CryptoSection
internal/config DefaultAllowedCIDRs
internal/config DefaultArgonMemoryMB
internal/config DefaultArgonThreads
internal/config DefaultArgonTime
internal/config DefaultAuditLog
internal/config DefaultClaimApprovalTimeout
internal/config DefaultClientRegistry
internal/config DefaultClockSkew
internal/config DefaultJWTTTL
internal/config DefaultListenPort
internal/config DefaultMaxClockDrift
internal/config DefaultMaxInteractiveTTL
internal/config DefaultMaxSupervisorTTL
internal/config DefaultMaxUses
internal/config DefaultNonceTTL
internal/config DefaultRequireFileModeChecks
internal/config DefaultRequireKeychainACL
internal/config DefaultRequireNTPSync
internal/config DefaultRequireTailscale
internal/config DefaultStateDir
internal/config DefaultSupervisorTTLMax
internal/config DiscordSection
internal/config ErrArgonMemoryTooHigh
internal/config ErrArgonMemoryTooLow
internal/config ErrArgonThreadsTooHigh
internal/config ErrArgonThreadsTooLow
internal/config ErrArgonTimeTooHigh
internal/config ErrArgonTimeTooLow
internal/config ErrAuditLogEscape
internal/config ErrAuditLogParentUnsafe
internal/config ErrClaimApprovalTimeoutOutOfRange
internal/config ErrConfigFileMode
internal/config ErrInvalidDuration
internal/config ErrListenLoopback
internal/config ErrListenMalformed
internal/config ErrListenPublic
internal/config ErrListenUnspecified
internal/config ErrMissingRequiredField
internal/config ErrPathPrefixInvalid
internal/config ErrStateDirNotFound
internal/config ErrStateDirUnsafe
internal/config ErrSupervisorTTLOutOfRange
internal/config ErrTOMLDecode
internal/config ErrTailscaleBindRequired
internal/config ErrTailscaleRequired
internal/config ErrUnknownField
internal/config LoadServer
internal/config MaxArgonMemoryMB
internal/config MaxArgonThreads
internal/config MaxArgonTime
internal/config MaxClaimApprovalTimeout
internal/config MaxPathPrefixLen
internal/config MinArgonMemoryMB
internal/config MinArgonThreads
internal/config MinArgonTime
internal/config MinClaimApprovalTimeout
internal/config MinPathPrefixLen
internal/config NetworkSection
internal/config SecuritySection
internal/config Server
internal/config ServerSection
internal/config TailscaleCGNAT
internal/discord ApprovalRequest
internal/discord Approver
internal/discord BotApprover
internal/discord BotConfig
internal/discord Decision
internal/discord DefaultDMRateLimit
internal/discord ErrApprovalDenied
internal/discord ErrApprovalTimeout
internal/discord ErrDiscordUnavailable
internal/discord ErrMissingAppID
internal/discord ErrMissingLogger
internal/discord ErrMissingOwnerID
internal/discord ErrMissingToken
internal/discord ErrRateLimited
internal/discord NewBotApprover
internal/discord/alerts Alert
internal/discord/alerts AlertClass
internal/discord/alerts AlertClassApprovalRequest
internal/discord/alerts AlertClassChildExit78StaleFailure
internal/discord/alerts AlertClassDaemonRefreshRequest
internal/discord/alerts AlertClassDiscordDisconnected
internal/discord/alerts AlertClassDiscordReconnected
internal/discord/alerts AlertClassLogPatternStaleWarning
internal/discord/alerts AlertClassValidatorStaleFailure
internal/discord/alerts AlertClassVaultUnreachableAtBootTimeout
internal/discord/alerts DefaultBucketWindow
internal/discord/alerts ErrAlertRateLimited
internal/discord/alerts ErrAlertTransport
internal/discord/alerts ErrUnknownAlertClass
internal/discord/alerts NewRouter
internal/discord/alerts Router
internal/discord/alerts Sender
internal/discord/alerts Tier
internal/discord/alerts TierCritical
internal/discord/alerts TierInfo
internal/discord/alerts TierWarning
internal/keychain ErrKeychainItemExists
internal/keychain ErrKeychainItemNotFound
internal/keychain ErrKeychainPermissionDenied
internal/keychain ErrKeychainUnsupportedPlatform
internal/keychain FakeKeychain
internal/keychain Keychain
internal/keychain New
internal/keychain NewFake
internal/keychain PerBinaryACLSupported
internal/keys DeriveAuditSigningKey
internal/keys DeriveClientKey
internal/keys DeriveJWTSigningKey
internal/keys DeriveMasterSeed
internal/keys DeriveVaultEncKey
internal/keys ErrPassphraseTooShort
internal/keys ErrSaltMissing
internal/keys PublicKeyFingerprint
internal/logging Format
internal/logging FormatAuto
internal/logging FormatJSON
internal/logging FormatText
internal/logging New
internal/logging Options
internal/logging RedactPatterns
internal/logging RedactString
internal/server ApprovalRequest
internal/server Approver
internal/server AuditAuthFailedNotAllowed
internal/server AuditClaimOutcome
internal/server AuditEvent
internal/server AuditEventType
internal/server AuditFilePermCheckFailed
internal/server AuditPanicCaptured
internal/server AuditServerStart
internal/server AuditServerStop
internal/server AuditVaultReloaded
internal/server AuditWriter
internal/server ClientKeyResolver
internal/server Decision
internal/server DefaultClockSyncTimeout
internal/server DefaultIdleTimeout
internal/server DefaultReadHeaderTimeout
internal/server DefaultReadTimeout
internal/server DefaultReloadDrainWindow
internal/server DefaultShutdownTimeout
internal/server DefaultWriteTimeout
internal/server Deps
internal/server ErrAlreadyRun
internal/server ErrApproverDenied
internal/server ErrApproverRateLimited
internal/server ErrApproverTimeout
internal/server ErrApproverUnavailable
internal/server ErrBindNotOnTailscale
internal/server ErrClientUnknown
internal/server ErrClockProbeUnexpectedOutput
internal/server ErrClockUnsynchronised
internal/server ErrFileModeLoose
internal/server ErrMissingApprover
internal/server ErrMissingAuditWriter
internal/server ErrMissingConfig
internal/server ErrMissingLogger
internal/server ErrMissingTokenIssuer
internal/server ErrMissingTokenStore
internal/server ErrMissingVaultPtr
internal/server ErrMountBadPath
internal/server ErrMountNilHandler
internal/server ErrMountUnsupported
internal/server ErrReloadDecryptFailed
internal/server ErrReloadFileMissing
internal/server ErrReloadInProgress
internal/server ErrReloadInternalNil
internal/server ErrReloadInvalid
internal/server ErrSecretMissing
internal/server ErrShuttingDown
internal/server ErrStateDirUnsafe
internal/server MaxRequestBodyBytes
internal/server New
internal/server NewChassisAuditAdapter
internal/server RequestID
internal/server Server
internal/server SessionInteractive
internal/server SessionSupervisor
internal/server SessionType
internal/server TokenIssuer
internal/supervise AcquirePidFile
internal/supervise AlertClass
internal/supervise AlertClassBootTimeout
internal/supervise AlertClassDiscordUnavailableOnClaim
internal/supervise AlertClassExit78
internal/supervise AlertClassGraceEntered
internal/supervise AlertClassLogPatternMatch
internal/supervise AlertClassRefillFailed
internal/supervise AlertClassRefreshDenied
internal/supervise AlertClassRefreshTimeout
internal/supervise AlertClassValidatorFailure
internal/supervise AlertClassVaultRejectedJWT
internal/supervise AlertPayload
internal/supervise Alerts
internal/supervise Child
internal/supervise ChildConfig
internal/supervise Clock
internal/supervise Deps
internal/supervise EnumerateSupervisorSockets
internal/supervise ErrAlreadyRunning
internal/supervise ErrBootTimeout
internal/supervise ErrChildNotStarted
internal/supervise ErrClaimDenied
internal/supervise ErrCommandEmpty
internal/supervise ErrCommandPathRelative
internal/supervise ErrInvalidTransition
internal/supervise ErrJTIUnknown
internal/supervise ErrLifecycleAlreadyRan
internal/supervise ErrPidLocked
internal/supervise ErrRefillFailedPostRunning
internal/supervise ErrSocketPermsLoose
internal/supervise ErrValidatorFailed
internal/supervise Event
internal/supervise EventApprovalGranted
internal/supervise EventBootRetryExhausted
internal/supervise EventChildExit78Stale
internal/supervise EventChildExitClean
internal/supervise EventChildExitCrash
internal/supervise EventClaimDenied
internal/supervise EventClaimUnavailable
internal/supervise EventFetchAuthRequired
internal/supervise EventFetchOK
internal/supervise EventGraceExpired
internal/supervise EventGraceRestartOK
internal/supervise EventGraceRestartTriggered
internal/supervise EventRefreshRequested
internal/supervise EventStopRequested
internal/supervise EventValidatorFailed
internal/supervise Exit78
internal/supervise Grace
internal/supervise Lifecycle
internal/supervise NewChild
internal/supervise NewGrace
internal/supervise NewLifecycle
internal/supervise NewRefiller
internal/supervise NewRefresher
internal/supervise NewStatusServer
internal/supervise NewStore
internal/supervise PidFile
internal/supervise Refiller
internal/supervise Refresher
internal/supervise Snapshot
internal/supervise SocketPathForSupervisor
internal/supervise State
internal/supervise StateAwaitingApproval
internal/supervise StateFetching
internal/supervise StateGraceRestart
internal/supervise StateRunning
internal/supervise StateStopped
internal/supervise StatusInputs
internal/supervise StatusServer
internal/supervise Store
internal/supervise Validator
internal/supervise Watchdog
internal/supervise/config Child
internal/supervise/config DefaultBootRetryTimeout
internal/supervise/config DefaultCacheSecretsForRestart
internal/supervise/config DefaultDMRateLimit
internal/supervise/config DefaultGraceWindow
internal/supervise/config DefaultLogLevel
internal/supervise/config DefaultRefreshNudgeBefore
internal/supervise/config DefaultRefreshWindow
internal/supervise/config DefaultRequestedTTL
internal/supervise/config DefaultRestartOnCleanExit
internal/supervise/config DefaultRestartOnExit78
internal/supervise/config DefaultWatchdogEnabled
internal/supervise/config DefaultWatchdogMaxAlertsPerHour
internal/supervise/config DefaultWatchdogPatterns
internal/supervise/config DiscordRouting
internal/supervise/config ErrBootRetryTimeoutTooLong
internal/supervise/config ErrCommandEmpty
internal/supervise/config ErrCommandPathRelative
internal/supervise/config ErrGraceTTLWithoutCache
internal/supervise/config ErrGraceWindowTooLong
internal/supervise/config ErrInvalidDuration
internal/supervise/config ErrLogLevelInvalid
internal/supervise/config ErrMissingRequiredField
internal/supervise/config ErrPathNotClean
internal/supervise/config ErrRefreshNudgeBeforeTooLong
internal/supervise/config ErrRefreshWindowFormat
internal/supervise/config ErrRefreshWindowOrder
internal/supervise/config ErrRequestedTTLOutOfRange
internal/supervise/config ErrScopeEmpty
internal/supervise/config ErrServerURLInvalid
internal/supervise/config ErrSessionTypeInvalid
internal/supervise/config ErrTOMLDecode
internal/supervise/config ErrUnknownField
internal/supervise/config ErrUnknownValidator
internal/supervise/config ErrWatchdogRateInvalid
internal/supervise/config Load
internal/supervise/config MaxBootRetryTimeout
internal/supervise/config MaxGraceWindow
internal/supervise/config MaxRefreshNudgeBefore
internal/supervise/config MaxRequestedTTL
internal/supervise/config Supervisor
internal/supervise/config Validator
internal/supervise/config Watchdog
internal/supervise/validators ErrStaleCredential
internal/supervise/validators ErrValidatorNetwork
internal/supervise/validators ErrValidatorTimeout
internal/supervise/validators NewAnthropic
internal/supervise/validators NewAnthropicOAuth
internal/supervise/validators NewGitHub
internal/supervise/validators NewGoogleAI
internal/supervise/validators NewOpenAI
internal/supervise/validators NewRegistry
internal/supervise/validators Registry
internal/supervise/validators Validator
internal/supervise/watchdog ErrAlreadyRan
internal/supervise/watchdog ErrDuplicatePatternName
internal/supervise/watchdog ErrEmptyPatternName
internal/supervise/watchdog ErrNilAlertsChannel
internal/supervise/watchdog ErrNilLogger
internal/supervise/watchdog ErrNilPatternRegex
internal/supervise/watchdog ErrNonPositiveRateLimit
internal/supervise/watchdog Event
internal/supervise/watchdog NewWatchdog
internal/supervise/watchdog Pattern
internal/supervise/watchdog Watchdog
internal/testutil ApprovalCall
internal/testutil ApprovalRequest
internal/testutil Approver
internal/testutil AssertSentinelAbsent
internal/testutil Decision
internal/testutil DecisionApprove
internal/testutil DecisionApproveMute
internal/testutil DecisionDeny
internal/testutil DiscordStub
internal/testutil ErrUnexpectedCall
internal/testutil FakeClock
internal/testutil NewCapturingLogger
internal/testutil NewDiscordStub
internal/testutil NewFakeClock
internal/testutil NewSilentLogger
internal/testutil NewTestKeys
internal/testutil NewTestVault
internal/testutil NewTestVaultDetailed
internal/testutil SentinelSecret
internal/testutil ShortTempDir
internal/testutil VaultEntry
internal/token Claims
internal/token ErrAlgorithmUnsupported
internal/token ErrIPMismatch
internal/token ErrInvalidIssueParams
internal/token ErrInvalidIssuer
internal/token ErrJTIGeneration
internal/token ErrScopeViolation
internal/token ErrSignatureInvalid
internal/token ErrSigningFailed
internal/token ErrTokenExhausted
internal/token ErrTokenExpired
internal/token ErrTokenMalformed
internal/token ErrTokenRevoked
internal/token ErrUnknownSessionType
internal/token Issue
internal/token IssueParams
internal/token NewStore
internal/token NewStoreWithTick
internal/token Register
internal/token SessionInteractive
internal/token SessionSupervisor
internal/token SessionType
internal/token Store
internal/token Token
internal/token Validate
internal/token ValidateOpt
internal/token WithClockSkew
internal/transport/ecies Decrypt
internal/transport/ecies Encrypt
internal/transport/ecies ErrECIESDecryptFailed
internal/transport/ecies ErrECIESEmptyPlaintext
internal/transport/ecies ErrECIESEnvelopeTooShort
internal/transport/ecies ErrECIESInvalidRecipientKey
internal/transport/sign CanonicalJSON
internal/transport/sign ErrCanonicalUnsupported
internal/transport/sign ErrNonceEncoding
internal/transport/sign ErrNonceReplay
internal/transport/sign ErrNonceTTLInvalid
internal/transport/sign ErrSignatureInvalid
internal/transport/sign ErrTimestampStale
internal/transport/sign IsFreshTimestamp
internal/transport/sign NewNonceCache
internal/transport/sign NonceCache
internal/transport/sign RawMessage
internal/transport/sign Sign
internal/transport/sign Verify
internal/vault ErrAuthFailed
internal/vault ErrBadMagic
internal/vault ErrBadVersion
internal/vault ErrDuplicateName
internal/vault ErrFilePermsLoose
internal/vault ErrFileTooLarge
internal/vault ErrInvalidName
internal/vault ErrSecretNotFound
internal/vault ErrShortHeader
internal/vault ErrStoreDestroyed
internal/vault Load
internal/vault LoadSecrets
internal/vault Save
internal/vault Secret
internal/vault Store
internal/vault/securebytes ErrDestroyed
internal/vault/securebytes New
internal/vault/securebytes SecureBytes
```

<!-- symbol-manifest: END -->
