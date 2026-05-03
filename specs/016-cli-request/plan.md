# Implementation Plan: hush request — interactive secret fetch (`--exec` | `--format eval`)

**Branch**: `016-cli-request` | **Date**: 2026-05-03 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/016-cli-request/spec.md`
**Chunk contract**: [docs/sdd/SDD-16.md](../../docs/sdd/SDD-16.md)

## Summary

`hush request` is the operator's interactive secret-fetch subcommand. It
mounts on the SDD-14 cobra root (`root.AddCommand(newRequestCmd())` in
`Execute`) and adds **no new exported package-level symbols** to
`internal/cli`. Two new files plus their tests:

- [internal/cli/request.go](../../internal/cli/request.go) — flag layer,
  validation, claim build/sign/POST, secret fetch + ECIES decrypt, mode
  dispatch, lifecycle key/JWT zeroing.
- [internal/cli/exec.go](../../internal/cli/exec.go) — child-env
  construction inside `SecureBytes.Use`, `os/exec.Cmd` wrapper with
  absolute-path lookup, exit-code propagation.

Behavioural shape (locked):

1. **Flag layer** — `--server` (string, required), `--scope` (csv
   string → `[]string`, required), `--reason` (string, required), `--ttl`
   (`time.Duration`, required), `--max-uses` (int, required ≥
   `len(scope)`), `--machine-index` (uint32, required), `--exec` (string),
   `--format` (string, only literal `eval` accepted). Trailing positional
   argv after `--` becomes the child's `argv[1:]` verbatim in `--exec`
   mode.
2. **Pre-network validation** — exactly one of `--exec` xor `--format
   eval` must be set; failure → `errMissingExecOrFormat` →
   `ExitInputErr` with the locked stderr message `"hush: request: must
   specify --exec or --format eval"`. `--max-uses < len(--scope)` →
   `ExitInputErr` ("max-uses must be ≥ number of scopes"). All required
   flags validated before any keychain or network call.
3. **Client signing key** — retrieved from `internal/keychain` under
   (`hush-client`, `machine-<--machine-index>`); the 32-byte scalar is
   reconstituted into an `*ecdsa.PrivateKey` via
   `secp256k1.PrivKeyFromBytes(scalar).ToECDSA()`. The `*SecureBytes`
   handle is `Destroy()`-ed immediately after the scalar is copied into
   the ephemeral big.Int; the private key is zeroed before the process
   exits.
4. **Ephemeral key per request** — fresh secp256k1 keypair generated via
   `secp256k1.GeneratePrivateKey()`; the 33-byte SEC1-compressed public
   key (lowercase hex, 66 chars) is placed into the canonical claim
   payload as `ephemeral_pubkey`; the private half stays in the parent
   process and is zeroed before exit (matching the existing
   `secureZeroBigInt` pattern from `internal/transport/ecies`).
5. **`/claim` flow** — assembles the `signedPayload` shape locked in
   [internal/server/claim_handler.go](../../internal/server/claim_handler.go):
   `{ephemeral_pubkey, machine_name, nonce, reason, request_id, scope,
   session_type, timestamp, ttl}` (alphabetical, matches server
   `signedPayload` struct exactly), runs it through
   `sign.CanonicalJSON`, signs via `sign.Sign(ctx, clientKey, canonical)`,
   wraps as the wire envelope `{payload, signature, client_key_fingerprint,
   nonce, timestamp, ephemeral_pubkey, scope, reason, ttl, session_type,
   request_id, machine_name}` (server-side `claimRequest` shape), and
   POSTs to `<server>/claim`. The HTTP client's request context is
   bounded by `--ttl` (matches the spec's clarification — wait at most
   `--ttl`); successful 200 unmarshals to `{jwt, expires_at, jti}`.
6. **`/s/<name>` fetch loop** — for each scope name, `GET
   <server>/s/<name>` with `Authorization: Bearer <jwt>`; the
   octet-stream body is fed to `ecies.Decrypt(ctx, ephemeralPriv, body)`
   → `*securebytes.SecureBytes` owned by the request hot path. All
   secrets are fetched into a slice of `*SecureBytes` BEFORE either
   delivery mode runs — so a partial fetch failure aborts the run with
   no child started and no partial export block printed (FR-018, SC-010).
7. **`--exec` path** — `cli/exec.go` builds the child env using
   `SecureBytes.Use(fn)`: each secret name is appended to a
   `[]string` env slice as `NAME=<value>` inside the callback; once the
   slice is fully populated `os/exec.Cmd.Run()` is invoked with
   `cmd.Path` resolved via `exec.LookPath` (NOT shell-parsed); the
   trailing positional argv (after `--`) becomes `cmd.Args[1:]`. Stdin /
   stdout / stderr are wired to the parent's; exit code is propagated
   via `(*exec.ExitError).ExitCode()`. After `Run()` returns, the
   ephemeral secp256k1 private key (`big.Int` D field) is zeroed and the
   JWT *SecureBytes (held in the request hot path) is `Destroy()`-ed.
   The child env `[]string` itself cannot be zeroed after the exec
   syscall returns — that is documented in SECURITY.md §6 as a known
   limitation; the parent's hold on plaintext is the only window we
   control, and it is closed within ≤ 1 ms of the child syscall.
8. **`--format eval` path** — for each secret: `SecureBytes.Use(fn)` to
   read bytes into a local `string`, escape any `'` as `'\''`, write
   `export NAME='value'\n` to stdout. Once every line is rendered, write
   the locked stderr WARNING (see §0 below). The eval mode is
   operator-acknowledged per SECURITY.md §6 — the brief plaintext
   crossing through a Go `string` is documented as a residual risk and
   is the price of the eval contract.
9. **Lifecycle close** — both delivery paths converge on a `defer`
   block that zeros the ephemeral private key (`big.Int.SetBytes(make(...))`),
   `Destroy()`s every `*SecureBytes` (JWT, secrets, client-key scalar
   wrapper), and explicitly clears any local `[]byte` buffers used in
   intermediate canonicalisation. SIGINT/SIGTERM during the approval
   wait is handled via `signal.NotifyContext` — the cancel propagates
   through the HTTP client and the same `defer` chain runs (FR-021).

The locked stderr WARNING text (used by the eval path AND asserted
byte-for-byte by `TestRequest_FormatEvalEmitsStderrWarning`) is:

```text
WARNING: --format eval prints secret values to stdout. They may be captured by terminal scrollback, tmux, or script. Use --exec whenever possible.
```

This wording matches docs/SECURITY.md §6 row "`--format eval` stdout
leakage" and Constitution VII's stated rationale ("export statements that
bypass process injection"). It is fixed at this plan and any change is
a contract change requiring a new plan.

## Technical Context

**Language/Version**: Go 1.26.1 (module `github.com/mrz1836/hush`,
`go.mod` declares `go 1.26.1`). No language-version bump.

**Primary Dependencies** (all already direct — no new direct deps):
- `github.com/spf13/cobra` (existing) — subcommand + flag wiring.
- `github.com/decred/dcrd/dcrec/secp256k1/v4` (existing) — ephemeral
  key generation + recipient-private reconstruction; matches the curve
  used by `internal/transport/ecies` and `internal/keys`.
- `github.com/mrz1836/hush/internal/keychain` (SDD-15) — client-key
  retrieval.
- `github.com/mrz1836/hush/internal/transport/sign` (SDD-08) —
  `CanonicalJSON` + `Sign`.
- `github.com/mrz1836/hush/internal/transport/ecies` (SDD-09) —
  `Decrypt` returns `*securebytes.SecureBytes`.
- `github.com/mrz1836/hush/internal/vault/securebytes` (SDD-02) — secret
  + JWT memory containers.
- `github.com/mrz1836/hush/internal/keys` (SDD-01) —
  `PublicKeyFingerprint` for the wire envelope's
  `client_key_fingerprint` field.
- Standard library: `context`, `crypto/rand`, `encoding/base64`,
  `encoding/hex`, `encoding/json`, `errors`, `fmt`, `io`, `log/slog`,
  `net/http`, `os`, `os/exec`, `os/signal`, `strings`, `syscall`,
  `time`.

**Storage**: none. The whole point of the chunk is "no disk writes."
The only filesystem touches are read-only `os.Executable()` (during
keychain ACL-gated retrieval inside the Darwin keychain shell-out — not
this chunk's code) and child-process `argv[0]` lookup via
`exec.LookPath`. No cache files, no temp files, no JWT-on-disk, ever.

**Testing**: `go test -race`; integration tests gated by `//go:build
integration`. Sentinel-leak assertions reuse
`internal/testutil.SentinelSecret(16)` →
`SECRET_SHOULD_NEVER_APPEAR_16`. Approval flow is driven by
`internal/testutil.DiscordStub.ApproveAll` running an in-process
`*server.Server` from SDD-13.

**Target Platform**: macOS (darwin) production; the linux build target
is sound (the keychain wrapper handles the linux-Secret-Service path)
but `internal/keychain.PerBinaryACLSupported()` returns false on Linux
and `request` will surface that refusal up-front before any network
call when the platform doesn't honour ACLs (matches SDD-15's posture).

**Project Type**: CLI subcommand on top of the SDD-14 cobra root.

**Performance Goals**: 
- Latency from `--exec` path resolution to child-process `Run()`: bounded by
  the operator's Discord approval round-trip + `len(scope) × ECIES
  decrypt time` (~ms each for typical secret sizes). No tight inner loop.
- Approval wait window: bounded by `--ttl`. Default supervisor-side
  approval timeout is 60s (server-side `Crypto.ClaimApprovalTimeout`).
  The client's own context deadline is `--ttl`, ensuring the client
  doesn't outlive the server's wait window; whichever fires first
  wins.

**Constraints**:
- **No secret on disk, no JWT on disk** — sentinel-leak test scans the
  process tempdir + cwd after each test for the sentinel and fails on
  any hit.
- **No secret value in any log line** — the `*slog.Logger` rendered for
  this subcommand emits only failure modes + identifiers (scope name,
  jti, request_id). Asserted by the sentinel-leak test capturing the
  parent's stdout/stderr/log.
- **No `os.Getenv`-derived signing key** — the only signing-key source
  is `internal/keychain.Retrieve`. A `golangci-lint forbidigo` rule
  already covers `os.Getenv` repo-wide; the test
  `TestRequest_ClientKeyFromKeychainNotEnv` asserts the call path.
- **Absolute-program-path exec** — `exec.LookPath` is the only
  resolution path; if the program path contains spaces or shell
  metacharacters, the resulting `cmd.Path` is whatever LookPath returns
  with no shell interpretation (FR-008).
- **Mutual exclusion at flag layer** — `--exec` xor `--format eval`
  enforced by the validation function before any I/O. Both unset →
  `ExitInputErr`. Both set → `ExitInputErr`.

**Scale/Scope**: one operator, one approval, ≤ 16 secrets per claim
(matches `len(scope)` cap in claim_handler shape validation).

## Constitution Check

*Gates evaluated against
[`/Users/mrz/projects/hush/.specify/memory/constitution.md`](../../.specify/memory/constitution.md)
v1.1.0. Re-checked after Phase 1 design. No violations identified — no
Complexity Tracking entries required.*

### Principle I — Zero Files at Rest on Agent Machines
- **Compliance**: The whole subcommand is the canonical example of this
  principle. No JWT cache file, no secret cache, no temp file. The
  client signing key is read from the OS keychain at runtime;
  `--machine-index` selects the keychain account.
- **Test guard**:
  - `TestRequest_NeverWritesJWTToDisk` — scans the tempdir + cwd after a
    full happy-path run; asserts no file contains the JWT body.
  - `TestRequest_ExecOnlyChildHasSecret` — sentinel
    `SECRET_SHOULD_NEVER_APPEAR_16` appears in the child's env (asserted
    via env-printing helper child) and is absent from the parent's
    stdout, stderr, slog output, and any file the parent wrote.

### Principle IV — Supervisor for Daemons, Wrap-Shell for Humans
- **Compliance**: This subcommand is the **wrap-shell-for-humans** half
  of Principle IV. The claim payload sets `session_type =
  "interactive"`, the issued JWT is interactive (TTL + `max_uses`
  bounded). Supervisor-mode is out of scope (SDD-23).
- **Test guard**: `TestRequest_ClaimSessionTypeIsInteractive` — captured
  HTTP request body to a fake `/claim` is decoded and the
  `session_type` field asserts equal to `"interactive"`.

### Principle VII — CLI Design Standards
- **Compliance**:
  - Single binary; mounted via `root.AddCommand(newRequestCmd())` in
    `Execute`; reuses the locked `Exit*` constants
    ([internal/cli/exit_codes.go](../../internal/cli/exit_codes.go)).
  - `--format eval` is explicit and off by default; emits the locked
    stderr WARNING.
  - Output: `--exec` mode is structurally silent on parent stdout/stderr
    (the child writes its own); `--format eval` writes export lines to
    stdout regardless of TTY/pipe.
  - Exit codes: `ExitOK` on success, `ExitInputErr` on flag-layer
    failure, `ExitAuth` on approval-deny / signature-rejected,
    `ExitErr` on transport / approval-timeout / partial-fetch /
    interrupted, `ExitNotFound` on a scope name not in the vault. (Plus
    the child's exit code in `--exec` mode — propagated as the parent's
    exit code, can be any value.)
- **Test guards**:
  - `TestRequest_RequiresExecOrFormat` — both flags unset → exit code
    `ExitInputErr`, no network call.
  - `TestRequest_ExecOrFormatMutuallyExclusive` — both flags set →
    `ExitInputErr`, no network call.
  - `TestRequest_FormatRejectsNonEval` — `--format json` →
    `ExitInputErr`.
  - `TestRequest_FormatEvalEmitsStderrWarning` — eval-mode happy path
    asserts byte-equal stderr WARNING line.
  - `TestRequest_PropagatesChildExitCode` — `--exec` of a program that
    exits 7 → parent exits 7.

### Principle X — Observability & Redaction
- **Compliance**:
  - Logger is `slog.Default()`; the subcommand never writes secret bytes
    to it. Every log line uses identifiers (`scope`, `jti`,
    `request_id`) only.
  - All secret-bearing values flow through `*SecureBytes`; the type's
    `LogValue()` returns `[redacted]` so an accidental
    `slog.Any("secret", sb)` would still redact.
  - Errors carry sentinel + identifier only (no secret bytes, no JWT
    bytes, no signature bytes — matches the `internal/server` and
    `internal/token` patterns).
- **Test guards**:
  - `TestRequest_LogsNeverContainSecretValue` — captured slog handler
    output is scanned for the sentinel and the JWT body; both must be
    absent.
  - `TestRequest_ErrorsDoNotLeakSecretBytes` — every documented failure
    path is exercised; the resulting `error.Error()` is scanned for the
    sentinel.

### Other principles (not in scope, but verified non-regressing)
- **Principle III** — uses BIP32-derived client key (via keychain),
  signs via `sign.Sign`, decrypts via `ecies.Decrypt`. No new layer
  added; no existing layer weakened.
- **Principle V** — partial fetch refuses to start the child (FR-018,
  SC-010); a Discord-unavailable response surfaces `ExitErr` with a
  documented message rather than auto-approving.
- **Principle VI** — Tailscale-only is enforced server-side; the client
  trusts the operator's `--server` value (the operator typed it; the
  Tailscale ACL is the perimeter, per SECURITY.md §1.4).
- **Principle VIII** — coverage target 90%; tests listed below cover
  every behavioural contract. No fuzz target added (no parser entry
  point in this chunk).
- **Principle IX** — `context.Context` first parameter on every
  function that does I/O; sentinel errors compared with `errors.Is`; no
  globals; no `init()`; no goroutines spawned in this chunk
  (`signal.NotifyContext` returns a context, not a fire-and-forget
  goroutine that we own).
- **Principle XI** — no new direct dependency; reuses the locked crypto
  stack.

**Result**: ✅ Constitution Check passes. Re-evaluated post-Phase-1 below.

## Project Structure

### Documentation (this feature)

```text
specs/016-cli-request/
├── plan.md              # this file
├── spec.md              # /speckit-specify output (read-only here)
├── research.md          # Phase 0 output
├── data-model.md        # Phase 1 output
├── quickstart.md        # Phase 1 output
├── contracts/
│   └── cli-request.md   # Phase 1 output — locked CLI contract for `hush request`
└── tasks.md             # Phase 2 output (created by /speckit-tasks; not by /speckit-plan)
```

### Source Code (repository root)

```text
internal/cli/
├── request.go               # NEW — subcommand wiring + claim/decrypt orchestration
├── exec.go                  # NEW — child env construction + os/exec wrapper
├── request_test.go          # NEW — unit tests for request.go
├── exec_test.go             # NEW — unit tests for exec.go
├── request_integration_test.go  # NEW (build tag `integration`) — full flow with DiscordStub
├── root.go                  # EDITED — root.AddCommand(newRequestCmd())
└── exit_codes.go            # EDITED — three new sentinels added (errMissingExecOrFormat,
                             #          errExecAndFormatBothSet, errMaxUsesTooLow); locked
                             #          messages registered with mapErr → ExitInputErr.
```

No other production files are modified. No new exported symbols are
added to `internal/cli` (only the subcommand registration on the cobra
tree, per the chunk contract).

**Structure Decision**: The subcommand follows the existing
`internal/cli` pattern locked by SDD-14 / SDD-15 (`serve.go`,
`init.go`, `revoke.go`, `health.go`, `version.go`). All command
functions take `(ctx, stdout, stderr, *requestDeps, ...)` so tests can
inject deterministic seams (HTTP client, keychain, signal source) per
the same `*Deps` pattern used by [internal/cli/init.go](../../internal/cli/init.go)
and [internal/cli/revoke.go](../../internal/cli/revoke.go).

## Phase 0 — Outline & Research

Output: [research.md](./research.md). Key resolved questions:

1. **Where does `--machine-index` come from?** Spec FR-020 already
   pins this: SDD-16 does NOT introduce a client-side config file or
   environment-variable fallback. The operator supplies the index on
   each call via `--machine-index N`. (The same value the operator used
   when running `hush init client --machine-index N`.) No global
   default; missing → `ExitInputErr`.
2. **What signs the claim?** The per-machine secp256k1 client signing
   key, retrieved from the OS keychain at
   `(hush-client, machine-<--machine-index>)` and reconstituted via
   `secp256k1.PrivKeyFromBytes`. No file path, no env var, no flag value
   accepts the key — locked by FR-004 + Constitution III layer 4 +
   SDD-15.
3. **What is the wire shape?** Mirrors the locked
   `internal/server/claim_handler.go` shape: alphabetical canonical
   payload (`ephemeral_pubkey`, `machine_name`, `nonce`, `reason`,
   `request_id`, `scope`, `session_type`, `timestamp`, `ttl`); wire
   envelope adds `signature` + `client_key_fingerprint`. The server
   already canonicalises and verifies; the client matches its sort
   order via `sign.CanonicalJSON` (struct tags do the alphabetisation
   automatically).
4. **What runs the approval wait clock?** The HTTP request context's
   deadline is `time.Now() + --ttl` (per spec clarification — the same
   `--ttl` flag bounds both the eventual JWT lifetime AND the approval
   wait). The server's own approval timeout is 60s by default; the
   first to fire wins. SIGINT during the wait cancels the HTTP context;
   the deferred zeroing chain still runs (FR-021).
5. **How does eval-mode escape single quotes?** `'\''` (close-quote,
   escaped-quote, open-quote) — the standard POSIX trick for embedding
   `'` in a single-quoted shell literal. Asserted by
   `TestRequest_FormatEvalEscapesSingleQuote`.
6. **What's the request_id format?** The server expects 16-64 chars
   matching `[A-Za-z0-9_-]`. Generate 24 bytes from `crypto/rand` and
   render as `base64.RawURLEncoding` → 32 chars.
7. **What's the nonce format?** Same character class, 8-128 chars.
   Generate 32 bytes from `crypto/rand` and render as
   `base64.RawURLEncoding` → 43 chars.
8. **What's the machine_name?** `os.Hostname()` (truncated to 64 chars
   if needed; matches the `[A-Za-z0-9._-]{1,64}` regex on the server).
9. **What's `client_key_fingerprint`?** `keys.PublicKeyFingerprint(pub)`
   returns the SDD-01-locked 16-char lowercase hex fingerprint. The
   server registry indexes registered clients by this fingerprint
   (matches `getFingerprintRe` `^[0-9a-f]{16}$`).

## Phase 1 — Design & Contracts

### Data model — see [data-model.md](./data-model.md)

Core types (none exported beyond the package):

- `requestFlags` — flag-layer state (server, scope, reason, ttl,
  maxUses, machineIndex, exec, format, childArgs).
- `claimWireRequest` — JSON-encodable wire envelope mirroring
  `server/claim_handler.go::claimRequest`.
- `claimWireResponse` — JSON-decodable success body
  `{jwt, expires_at, jti}`.
- `requestDeps` — testable seam (HTTP client, keychain, ephemeral-key
  generator, hostname resolver, now func, rand reader, signal context
  factory, executor seam for `os/exec`).

State transitions per request:

1. **flag-validation** → `errMissingExecOrFormat` |
   `errExecAndFormatBothSet` | `errMaxUsesTooLow` | OK
2. **keychain-retrieve** → reconstituted `*ecdsa.PrivateKey` | OK
3. **ephemeral-keygen** → fresh `*ecdsa.PrivateKey` (D field zeroed in
   defer chain)
4. **canonicalise + sign** → wire envelope bytes
5. **POST /claim** → `{jwt, expires_at, jti}` | mapped error
6. **N × GET /s/<name>** → `[]*securebytes.SecureBytes` (length =
   `len(scope)` on success; partial → abort)
7. **mode dispatch** → `--exec` runs child; `--format eval` writes
   stdout
8. **defer chain** → zero ephemeral key + Destroy JWT + Destroy each
   secret SecureBytes

### Contracts — see [contracts/cli-request.md](./contracts/cli-request.md)

Locks:

- The exact flag set + their default values (none have defaults; every
  flag is operator-supplied).
- The exact stderr WARNING string (byte-equal asserted by
  `TestRequest_FormatEvalEmitsStderrWarning`).
- The exact mutual-exclusion error message ("must specify --exec or
  --format eval").
- The wire envelope shape (matches
  `internal/server/claim_handler.go::claimRequest`).
- The decrypted-secret lifetime: in scope only inside
  `SecureBytes.Use(fn)` callbacks; never assigned to a top-level
  variable that outlives a `Destroy()` call.
- Exit-code mapping for every error class.

### Quickstart — see [quickstart.md](./quickstart.md)

Operator-facing TL;DR:

```bash
# Wrap a shell with two secrets
hush request \
  --server https://100.97.178.13:7743/h/abc123def \
  --scope ANTHROPIC_API_KEY,GITHUB_TOKEN \
  --reason "starting work session" \
  --ttl 8h \
  --max-uses 50 \
  --machine-index 0 \
  --exec /bin/zsh

# Emit shell-evalable exports (operator-acknowledged risk)
eval "$(hush request \
  --server https://100.97.178.13:7743/h/abc123def \
  --scope GITHUB_TOKEN \
  --reason "ad-hoc gh call" \
  --ttl 15m \
  --max-uses 1 \
  --machine-index 0 \
  --format eval)"
# WARNING printed to stderr — visible even when stdout is piped to eval.
```

### Agent context update

Update the `<!-- SPECKIT START -->`…`<!-- SPECKIT END -->` block in
`CLAUDE.md` (project root) to reference this plan file:
`specs/016-cli-request/plan.md`. The CLAUDE.md edit is the only file
this plan touches outside `specs/016-cli-request/`.

## Re-evaluation: Constitution Check (post-Phase 1)

Re-checked after writing data-model + contracts + quickstart. The
following are unchanged:

- **Principle I** — still zero files at rest. The contract locks "no
  cache, no temp, no JWT-on-disk."
- **Principle IV** — claim sets `session_type=interactive`; the
  TTL+max-uses combo is on the wire.
- **Principle VII** — exit codes, output destinations, and
  `--format eval` warning are locked in `contracts/cli-request.md`.
- **Principle X** — `*SecureBytes` is the only secret container; logs
  carry identifiers only.

No new violations surfaced. No Complexity Tracking entries needed.

## Complexity Tracking

> Not required — Constitution Check passed without violations.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| (none) | — | — |
