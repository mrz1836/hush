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

### Exported API — locked

> Filled by SDD-06 (server config) and SDD-18 (supervisor config). Until
> then, this section is a placeholder.

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

### Exported API — locked

> Filled by SDD-01 once `internal/keys` is implemented. Until then, this
> section is a placeholder.

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

### Exported API — locked

> Filled by SDD-02 (`internal/vault/securebytes`) and SDD-03
> (`internal/vault`). Until then, this section is a placeholder.

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

### Exported API — locked

> Filled by SDD-07. Until then, this section is a placeholder.

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

> Filled by SDD-08 (`internal/transport/sign`) and SDD-09
> (`internal/transport/ecies`). Until then, this section is a placeholder.

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

### Exported API — locked

> Filled by SDD-11 (Approver interface + bot + monitor) and SDD-28 (alert
> classes + tiered routing). Until then, this section is a placeholder.

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

### Exported API — locked

> Filled by SDD-05. Until then, this section is a placeholder.

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
