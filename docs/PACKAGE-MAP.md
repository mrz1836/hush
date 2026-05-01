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

### Exported API — locked

> Filled by SDD-14 once cmd/hush is implemented. Until then, this section is
> a placeholder. Consumers (none — this is `package main`) MUST NOT depend
> on internal exports beyond the locked sections of `internal/cli`.

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

### Exported API — locked

> Filled by SDD-14, SDD-15, SDD-16, SDD-17, SDD-23 as each `internal/cli/*.go`
> file is implemented. Until then, this section is a placeholder.

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
