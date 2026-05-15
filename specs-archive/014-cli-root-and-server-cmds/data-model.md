# Phase 1 Data Model — SDD-14

**Branch**: `014-cli-root-and-server-cmds` | **Date**: 2026-05-01

This file enumerates the entities the CLI surface introduces. Each
entity is described by **Fields**, **Validation rules**, and
(where applicable) **State transitions**. Cobra-internal types
(`*cobra.Command`, `*pflag.FlagSet`) are not modelled here — they are
implementation tools, not domain entities.

---

## 1. ExitCode

The public, contract-locked numeric outcome that every subcommand
emits exactly once. Operators script against these values.

**Fields**:
- `int` — the value itself.

**Symbolic form** (`internal/cli/exit_codes.go`):

| Constant | Value | Meaning | Raised by (this chunk) |
|----------|-------|---------|------------------------|
| `ExitOK` | `0` | Subcommand completed successfully. | All four subcommands. |
| `ExitErr` | `1` | Generic error: server unreachable, server 5xx, panic recovery, partial-health on `health`, anything not categorised below. | All four subcommands. |
| `ExitInputErr` | `2` | Operator-input error: missing flag, conflicting flags (`--verbose` + `--quiet`), unreadable config, no passphrase source on `serve`. | All four subcommands. |
| `ExitAuth` | `3` | Authentication failure: bad passphrase (vault decrypt fails), signature rejected (`revoke` — server returned 401/403 with auth-failure shape), JWT rejected (n/a — this chunk does not present JWTs). | `serve`, `revoke`. |
| `ExitNotFound` | `4` | Server reported the named entity does not exist: unknown `--jti` (when the server distinguishes the case), config file not found. | `revoke`, `serve`. |
| `ExitPerm` | `5` | OS-level permission denied: cannot bind the configured port, cannot read the vault file due to mode. | `serve`. |
| `ExitConfigStale` | `78` | **Reserved** (`EX_CONFIG`). The supervisor↔child contract for stale credentials. **NEVER raised by a subcommand in this chunk** — declared so future SDD-15 / SDD-23 chunks have a stable public symbol. | none (in this chunk). |

**Validation rules**:
- The seven values are the **only** values any subcommand may emit.
  No subcommand may emit a value not in this table (FR-005, SC-002).
- `ExitConfigStale` MUST NOT be raised by `serve`/`health`/`version`/`revoke`
  — guarded by `mapErr` (no error sentinel maps to 78) and asserted by
  `TestExitCodes_NoStaleConfigInThisChunk`.
- The mapping is stable across releases (FR-006). Adding a new code
  requires a constitutional amendment.

**Construction**: `mapErr(err error) int` (unexported) walks
`errors.Is` against the locked sentinel sets enumerated in
[`research.md`](./research.md) §9. Default for unrecognised errors is
`ExitErr`. `nil` error returns `ExitOK`.

---

## 2. OutputContext

The per-stream determination of "human or machine consumer".
Constructed once per `cli.Execute(ctx)` call, stored in the cobra
context, and consulted by every output write.

**Fields**:
- `Stdout *Stream` — the stream pointing at `os.Stdout`.
- `Stderr *Stream` — the stream pointing at `os.Stderr`.

Where `Stream` (unexported in `internal/cli/output.go`) holds:
- `w io.Writer` — the destination.
- `isTTY bool` — true when `term.IsTerminal(fd)` returns true at construction time.
- `noColor bool` — true when `--no-color` is set OR the underlying
  stream is not a TTY (no point emitting ANSI to a pipe regardless).

**Validation rules**:
- Determination is per-stream: stdout-TTY-only, stderr-TTY-only, both,
  or neither — all four combinations are valid runtime states (Edge
  Case "Output context detection").
- ANSI sequences are NEVER written when `noColor` is true (FR-004,
  SC-006).
- JSON output uses `encoding/json.MarshalIndent("", "  ")` when
  `isTTY` is true (rare but supported), `encoding/json.Marshal`
  otherwise; either way a single trailing `\n` follows.

**State transitions**: None — `OutputContext` is immutable for the
lifetime of one `Execute` call.

---

## 3. PassphraseSource

The logical channel by which `hush serve` obtains the vault passphrase.
Exactly three valid runtime states; **no environment-variable channel
exists** (FR-009).

**Symbolic form** (unexported in `internal/cli/serve.go`):

```go
type passphraseSourceKind int

const (
    sourceUnknown passphraseSourceKind = iota
    sourcePipe                         // stdin is a pipe (or regular file)
    sourceTTY                          // stdin is a terminal
    sourceMissing                      // neither — terminal failure
)
```

**Fields** (post-resolution):
- `kind passphraseSourceKind` — which channel produced the value.
- `value *securebytes.SecureBytes` — the resolved passphrase, wrapped
  immediately on read so no plaintext byte is reachable from any
  subsequent log/print site.

**Validation rules**:
- Resolution order is **fixed and not configurable**: pipe first, TTY
  second, fail third (FR-008).
- Pipe path: read all of stdin; strip exactly one trailing `\n` or one
  trailing `\r\n` (FR-008a, [`research.md`](./research.md) §7); preserve
  every other byte verbatim.
- Pipe path with zero bytes returned: treated as "no passphrase
  available on stdin"; falls through to TTY only if a TTY is
  separately attached (Edge Case "Passphrase source ambiguity").
- TTY path: `term.ReadPassword(int(stdin.Fd()))` — never echoes the
  bytes.
- Missing path: returns `errNoPassphraseSource` → `ExitInputErr` with
  the literal message `"no passphrase source: stdin is not a pipe and is not a terminal"`.
- **The resolution path NEVER calls `os.Getenv`** — verified by
  `TestServe_NeverReadsEnv`.

**State transitions**: One-shot — resolved once at the top of
`runServe`; the resulting `*securebytes.SecureBytes` is consumed
immediately by `keys.DeriveMasterSeed` and then `Destroy()`'d after
all subkeys are derived.

---

## 4. RevocationRequest

The operator-originated, locally-signed message identifying a token
to invalidate. Wire shape governed by the `internal/transport/sign`
contract (locked at SDD-08); modelled here for the CLI-side
construction path only.

**Fields** (unexported struct in `internal/cli/revoke.go`):
- `JTI string` — the token id supplied by `--jti`. Validated as
  RFC 4122 UUID format (the same regex as `internal/server`'s
  `getRequestIDRe`).
- `Nonce string` — 32 random bytes hex-encoded; generated by
  `crypto/rand.Read` at the top of `runRevoke`.
- `Timestamp time.Time` — `time.Now().UTC()`.
- `Signature []byte` — the result of `sign.Sign(ctx, signKey,
  sign.CanonicalJSON({jti, nonce, timestamp}))`.
- `ClientKeyFingerprint string` — derived via
  `keys.PublicKeyFingerprint(signKey.PublicKey)` at construction.

**Validation rules**:
- `--jti` MUST be supplied; missing → `ExitInputErr` with message
  `"missing required flag: --jti"`.
- `--server` MUST be supplied; missing → `ExitInputErr`.
- `--jti` MUST match the UUID regex; malformed → `ExitInputErr` with
  message `"invalid --jti: must be a UUID"`.
- The signing key is the same per-machine client key derived from the
  configured passphrase + machine index (`keys.DeriveClientKey`).
  **For this chunk, `revoke` reads the signing key by re-deriving it
  through the same passphrase resolution path as `serve`** — pipe
  first, TTY second, fail third. (Same `passphraseSource` seam,
  reused.) Rationale: the CLI does not store keys on disk
  (Constitution III "zero key files exist on disk").

**State transitions**: Construct → sign → POST → unmap response →
exit. No retries (idempotency is on the server side per SDD-13's
`/revoke` contract).

---

## 5. BuildIdentification

The package-level metadata `version` prints. Injected at link time by
GoReleaser; defaulted to development placeholders otherwise.

**Symbolic form** (`internal/cli/version.go`):

```go
var (
    Version = "dev"      // -ldflags injects e.g. "v0.1.0"
    Commit  = "unknown"  // -ldflags injects e.g. "fb3e402"
    Date    = "unknown"  // -ldflags injects e.g. "2026-05-01T12:34:56Z"
)
```

**Fields**:
- `Version string` — semantic version (or `"dev"`).
- `Commit string` — short commit identifier (or `"unknown"`).
- `Date string` — RFC 3339 build date (or `"unknown"`).

**Validation rules**:
- All three keys are ALWAYS present in JSON output (FR-019a clarification).
- The locked JSON object key order is `{"version", "commit", "date"}`
  (FR-019a clarification — Go's `encoding/json` preserves struct field
  order on marshal, so a `struct{ Version, Commit, Date string }` with
  the right tags emits in that order deterministically).
- Adding a new key is a breaking change to the public CLI contract
  (FR-019a final paragraph).

**State transitions**: None — read-only at runtime.

---

## 6. HealthSnapshot

The structured shape `health` consumes from the server's `GET /hz`
response and renders to the user. Mirrors the locked SDD-13 shape so
downstream tooling parses one structure regardless of which side
produced the bytes.

**Symbolic form** (mirrors `internal/server/health_handler.go`):

```go
type HealthSnapshot struct {
    Status           string  `json:"status"`            // "ok" | "degraded"
    Uptime           string  `json:"uptime"`            // duration string
    SecretsCount     int     `json:"secrets_count"`
    ActiveTokens     int     `json:"active_tokens"`
    DiscordConnected bool    `json:"discord_connected"`
    ConfigValid      bool    `json:"config_valid"`
    VaultLoaded      bool    `json:"vault_loaded"`
    ClockInSync      bool    `json:"clock_in_sync"`
}
```

**Fields**: As shown above; types match the server's emitted JSON
verbatim (Edge Case "Health JSON shape stability").

**Validation rules**:
- Healthy when ALL of: `Status == "ok"`, `DiscordConnected`,
  `ConfigValid`, `VaultLoaded`, `ClockInSync` (FR-017a).
  `SecretsCount` and `ActiveTokens` are informational only — not
  health gates.
- Partial-health renders the full per-dimension summary AND exits
  `ExitErr` (FR-017a — clarification 2026-05-01).
- The TEXT-mode rendering is a fixed two-column table (dimension →
  status) in stable order matching the JSON key order. The JSON-mode
  output is the raw struct, no transformation.

**State transitions**: None — fetched, rendered, exited.

---

## 7. ServeWiring (the chassis composition)

The transient, in-memory wiring that `runServe` constructs to call
`(*server.Server).Run`. Modelled here for design-traceability — not
exported.

**Fields** (unexported in `internal/cli/serve.go`):
- `cfg *config.Server` — loaded via `config.LoadServer`.
- `passphrase *securebytes.SecureBytes` — resolved per §3.
- `masterSeed []byte` — wrapped in `*securebytes.SecureBytes` after
  derivation; destroyed after subkey derivation completes.
- `vaultKey *securebytes.SecureBytes` — for the chassis's `Deps.VaultKey`.
- `jwtSignKey *ecdsa.PrivateKey` — for `token.Issue` (server-side; held
  in chassis Deps).
- `auditSignKey *ecdsa.PrivateKey` — for `audit.NewWriter`.
- `tokenStore token.Store` — `token.NewStore()`.
- `auditWriter audit.Writer` — `audit.NewWriter(...)`.
- `approver discord.Approver` — `discord.NewBotApprover(...)` in
  production; a `testutil.DiscordStub`-class instance in integration
  tests.
- `logger *slog.Logger` — `logging.New(opts)`.

**Validation rules**:
- All required `server.Deps` fields are populated before
  `server.New(deps)` is called; `server.New` returns matching `ErrMissing*`
  sentinels otherwise — caller maps to `ExitErr` (chassis-config bug,
  not operator-input bug).
- The `passphrase` and `masterSeed` `SecureBytes` are `Destroy()`'d
  before `serve` returns (whether by clean exit or error) — verified
  by `TestServe_DestroysPassphraseAndSeedOnExit`.
- The `vaultKey` is owned by the chassis after `New`; the CLI does
  not destroy it directly.

**State transitions**:
1. `cfg` loaded → `passphrase` resolved → `masterSeed` derived.
2. Subkeys (`vaultKey`, `jwtSignKey`, `auditSignKey`) derived.
3. `passphrase.Destroy()` and `masterSeed.Destroy()` called.
4. `auditWriter` constructed and `go auditWriter.Run(ctx)` spawned.
5. `approver` constructed.
6. `tokenStore = token.NewStore()` constructed.
7. `srv = server.New(Deps{...})`.
8. `srv.RegisterHandlers()`.
9. `signalCtx, _ = signal.NotifyContext(ctx, SIGINT, SIGTERM)`.
10. `srv.Run(signalCtx)` — blocking call.
11. On return (clean or error), audit writer ctx is cancelled; the
    audit goroutine drains and exits.

---

## 8. CrossEntityInvariants

Properties that span multiple entities and are asserted by tests:

- **Sentinel invariance**: Every field of every entity above either
  is or wraps `*securebytes.SecureBytes` for any byte sequence that
  could be a secret value, key byte, or signature byte. Plain `string`
  / `[]byte` is permitted only for non-secret fields (e.g.,
  `RevocationRequest.JTI`, `BuildIdentification.Version`).
  Asserted by `TestServe_OutputNoSentinel`,
  `TestRevoke_OutputNoSentinel`, `TestHealth_OutputNoSentinel`.

- **Exit-code monomorphism**: Every entity-level failure maps to
  exactly one `ExitCode` constant. Asserted by
  `TestExitCodes_AllSentinelsCovered` (which iterates the locked
  sentinel sets enumerated in `research.md` §9 and confirms `mapErr`
  emits a non-default code for each).

- **Output context discipline**: No entity writes both text AND JSON
  to the same stream within one subcommand call. Asserted implicitly
  by the per-stream `OutputContext` accessor — there is no public API
  that emits both shapes from one call site.
