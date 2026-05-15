# Phase 0 — Research: hush request

**Feature**: SDD-16 `hush request`
**Branch**: `016-cli-request`
**Date**: 2026-05-03

This document resolves every "how do we know X?" before Phase 1 design
begins. Each section is **Decision / Rationale / Alternatives
considered**. Decisions made here are reflected in the locked plan and
in `contracts/cli-request.md`.

---

## §1 — Where does `--machine-index` come from?

### Decision

`--machine-index N` (uint32) is a **required** flag on every `hush
request` invocation. There is no client-side configuration file, no
environment-variable fallback, and no auto-discovery path. Missing or
malformed → `ExitInputErr` with the locked message
`"hush: request: --machine-index is required"`. The same operator-supplied
integer that was passed to `hush init client --machine-index N`
(SDD-15) is the value that selects the keychain account
`machine-<N>`.

### Rationale

- Spec FR-020 already pins this for `--server`: SDD-16 introduces no
  client-side config. Extending the same posture to `--machine-index`
  keeps the chunk's surface tight and avoids creating a hidden state
  file the operator has to remember to back up or delete.
- The operator typed `--machine-index N` to enroll the machine; making
  them type the same number to use it is symmetric and surprise-free.
- Auto-discovery (e.g. enumerating keychain entries) would require
  invoking `security find-generic-password` once per candidate value.
  That is both slower and noisier (each call may prompt the keychain
  ACL dialog). The operator already knows the value.

### Alternatives considered

- **Read `--machine-index` from `~/.hush/client.toml`**: rejected.
  Introduces a client-side state file SDD-16 explicitly disclaims
  (FR-020, spec assumption "No client-side configuration file exists
  today"). The future SDD that adds one will retro-fit a default; until
  then the explicit flag is the contract.
- **Fall back to `HUSH_MACHINE_INDEX`**: rejected. Constitution III §1
  bans key material from env vars; while the index is not key
  material, allowing env-var-driven defaults for a flag that selects
  key material is a slippery slope. Constitution IX (no globals)
  reinforces the "explicit > implicit" stance.
- **Iterate `machine-0`, `machine-1`, … and pick the first one that
  signs the canonical bytes verifiable by a fingerprint match**:
  rejected. Triggers N keychain ACL prompts; provides no usability
  win over making the operator type the number once.

---

## §2 — What signs the claim, and how is the key reconstituted?

### Decision

The per-machine secp256k1 client signing key is retrieved from the OS
keychain by calling
`keychain.Retrieve(ctx, "hush-client", fmt.Sprintf("machine-%d", N))`.
The returned `*securebytes.SecureBytes` holds the 32-byte raw scalar
exactly as written by `hush init client` (matches
[internal/cli/init.go::serializeECPrivKey](../../internal/cli/init.go)).
Inside a single `Use(fn)` callback the scalar is fed to
`secp256k1.PrivKeyFromBytes(scalar).ToECDSA()`, returning an
`*ecdsa.PrivateKey` whose `D` field holds the same scalar in a
`*big.Int`. The `*SecureBytes` is `Destroy()`-ed inside the same
callback's `defer`. Before process exit (defer chain) the
`*ecdsa.PrivateKey.D` is zeroed with `D.SetBytes(make([]byte, 32))`.

### Rationale

- This matches the storage shape already locked by
  `internal/cli/init.go::serializeECPrivKey` (32-byte big-endian scalar
  in `*SecureBytes`).
- `secp256k1.PrivKeyFromBytes` is the curve's canonical reconstitution
  function, mirrors the use in
  [internal/cli/revoke_helpers.go::ephemeralRevokeKey](../../internal/cli/revoke_helpers.go).
- Constitution III layer 4 + Constitution X both require that the
  signing key never appear as a Go `string` and that intermediate
  buffers are explicitly zeroed.

### Alternatives considered

- **Pass the `*SecureBytes` directly to a reflexive
  `sign.SignFromSecureBytes`**: rejected — `sign.Sign` already takes
  `*ecdsa.PrivateKey`, and refactoring the signing API would expand
  the chunk's blast radius into SDD-08 with no security benefit. The
  scalar still has to materialize into the `*big.Int` for the curve
  math; wrapping the API doesn't avoid that.
- **Sign inside a `Use(fn)` callback that opens a fresh
  `*ecdsa.PrivateKey` each time**: rejected — every claim has exactly
  one signature, so the single-shot reconstitute → sign → zero pattern
  is the simplest path that still respects the redaction contract.

---

## §3 — What is the wire shape? Match the server.

### Decision

The wire envelope mirrors
[internal/server/claim_handler.go::claimRequest](../../internal/server/claim_handler.go)
exactly. Twelve top-level keys, alphabetical when serialised:

```text
client_key_fingerprint, ephemeral_pubkey, machine_name, nonce, reason,
request_id, scope, session_type, signature, timestamp, ttl
```

The signed canonical payload mirrors `server/claim_handler.go::signedPayload`
— same nine fields **excluding** `signature` and
`client_key_fingerprint`:

```text
ephemeral_pubkey, machine_name, nonce, reason, request_id, scope,
session_type, timestamp, ttl
```

Both client and server canonicalise via `sign.CanonicalJSON`, which
sorts struct fields alphabetically by JSON tag name. Bytes match.

### Rationale

- The server's regex set
  ([internal/server/claim_handler.go:122-152](../../internal/server/claim_handler.go))
  pins the format of every field. The client just has to emit values
  that match, then sign. Anything else risks `bad_request` rejection
  on a perfectly approvable claim.
- `sign.CanonicalJSON` is the contract — its reflective walker drops
  `MarshalJSON` hooks, so struct-tag alphabetisation is the only sort
  mechanism. Re-ordering fields in the Go source has no effect; only
  tag names matter.

### Alternatives considered

- **Roll a smaller payload (omit `request_id` and `machine_name`)**:
  rejected. The server validates both are well-formed; an absent or
  malformed value lands on `bad_request` before signature
  verification.
- **Encode the signature inside the canonical payload**: rejected
  — circular dependency: signing requires bytes that don't yet have
  a signature. The server's `signedPayload` shape excludes the
  signature for the same reason.

---

## §4 — How long does the client wait?

### Decision

The HTTP request context is created with deadline
`time.Now().Add(--ttl)`. SIGINT/SIGTERM cancellation is layered via
`signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)` so the
operator's Ctrl-C cancels the in-flight `client.Do(req)` immediately
and runs the same defer chain (FR-021).

### Rationale

- Spec clarification 2026-05-03: "Wait at most --ttl (reuse the same
  flag); if no decision by then, exit with timeout status."
- The server-side approval timeout is governed by
  `Crypto.ClaimApprovalTimeout` (default 60s, range [1s, 10min]). When
  `--ttl > server-side approval timeout`, the server fires first and
  returns 408 `approval_timeout`. When `--ttl < server-side approval
  timeout`, the client's context fires first and the request errors
  out with `context.DeadlineExceeded`. Both paths land on
  `ExitErr` with a documented stderr message.
- SIGINT during the wait is FR-021 — must zero key material before
  exiting. `signal.NotifyContext` returns a context whose `Done()`
  fires on signal receipt; the existing defer chain closes everything,
  no separate signal handler needed.

### Alternatives considered

- **Hardcoded 5-minute wait (mirror `health` and `revoke`)**: rejected.
  Spec clarification explicitly ties the wait to `--ttl`. A 5-minute
  cap would surprise operators who set `--ttl 8h` for a long-running
  shell — they'd get cut off at 5 minutes even though the server is
  still waiting.
- **Two flags (`--ttl` for the JWT, `--wait-timeout` for the
  approval)**: rejected by spec clarification: "There MUST be no
  separate 'wait timeout' flag." (FR-005.)
- **Run a goroutine that cancels at deadline + watches a signal
  channel**: rejected — `signal.NotifyContext` already does both jobs
  and respects Constitution IX's "no fire-and-forget goroutine" rule
  (the context owner is the calling function; cancel runs at function
  return).

---

## §5 — Single-quote escaping in `--format eval`

### Decision

Render each export as `export NAME='value'\n` where every literal
`'` byte inside `value` is replaced with `'\''` — the standard POSIX
trick (close-quote, escaped-quote, open-quote). No other byte is
escaped; the single-quoted shell literal preserves every byte verbatim,
including newlines, backslashes, dollar signs, and high-bit bytes.

### Rationale

- POSIX shell single-quoted strings have only one special character:
  `'` itself. Closing the quote, emitting an escaped `'`, and
  re-opening the quote restores the literal byte. Every other byte is
  passed through.
- This is the same algorithm used by `printf '%q'` for single-quote
  output and by the `shlex.quote()` family in other languages.

### Alternatives considered

- **Double-quote everything and escape `$`, `` ` ``, `\`, `"`**:
  rejected. Five escape rules vs. one, and `$VARIABLE` interpolation
  inside double quotes makes any future change risky (a stray `$`
  becomes a shell variable lookup).
- **Base64-encode the value and emit `export NAME=$(echo ... | base64
  -d)`**: rejected. Adds a subprocess to every export; loses the
  "run-on-any-shell" property; `base64 -d` flag varies (`-D` on macOS).

---

## §6 — `request_id`, `nonce`, `machine_name`, `client_key_fingerprint`

### Decision

| Field | Format | Source |
|-------|--------|--------|
| `request_id` | 32 chars `[A-Za-z0-9_-]` | `base64.RawURLEncoding(crypto/rand 24 bytes)` |
| `nonce` | 43 chars `[A-Za-z0-9_-]` | `base64.RawURLEncoding(crypto/rand 32 bytes)` |
| `machine_name` | ≤ 64 chars matching `[A-Za-z0-9._-]{1,64}` | `os.Hostname()`, truncated and sanitised |
| `client_key_fingerprint` | 16 chars `[0-9a-f]` | `keys.PublicKeyFingerprint(&clientKey.PublicKey)` (SDD-01 locked) |
| `ephemeral_pubkey` | 66 chars `[0-9a-fA-F]` | SEC1-compressed public point, hex-lowercase |
| `timestamp` | RFC3339Nano | `time.Now().UTC().Format(time.RFC3339Nano)` |
| `ttl` | duration string | `--ttl.String()` |

### Rationale

- Each format matches the server's compiled regex
  ([internal/server/claim_handler.go:123-152](../../internal/server/claim_handler.go))
  byte-for-byte.
- `keys.PublicKeyFingerprint` is the SDD-01-locked hex-truncated
  SHA-256 of the SEC1-compressed pubkey — the exact value the server's
  `clientKeyResolver` uses to look up the registered pub.
- `os.Hostname()` may return a name with characters outside the
  server's regex (especially on edge-case Windows builds). The
  sanitiser maps disallowed bytes to `_` and truncates to 64 chars.
  This is purely a label for the Discord prompt; uniqueness is provided
  by `client_key_fingerprint`.

### Alternatives considered

- **UUID for `request_id`**: rejected — base64 of 24 bytes gives 144
  bits of entropy in 32 chars vs. 122 bits in a 36-char UUID. The
  server accepts 16-64 chars `[A-Za-z0-9_-]`; UUID would require
  additional special-case handling for `-`.
- **Constant `machine_name = "interactive"`**: rejected — the
  operator's Discord prompt benefits from showing a real hostname so a
  laptop and a workstation can be distinguished at a glance.

---

## §7 — Ephemeral key generation

### Decision

`secp256k1.GeneratePrivateKey()` produces the per-request keypair. The
returned `*secp256k1.PrivateKey` is converted to `*ecdsa.PrivateKey` via
`.ToECDSA()` for use by `internal/transport/ecies.Decrypt`. The 33-byte
SEC1-compressed public point is rendered as 66-char lowercase hex via
the same compression routine used by
[internal/cli/init.go::sec1Compress](../../internal/cli/init.go) (and
by `internal/transport/ecies::compressedPubKey`).

After `Run()` returns (or the request errors out), the deferred
zeroing runs: `priv.D.SetBytes(make([]byte, 32))`. This matches the
existing zeroing pattern in
[internal/transport/ecies/ecies.go::secureZeroBigInt](../../internal/transport/ecies/ecies.go)
(though the helper itself is package-internal).

### Rationale

- `secp256k1.GeneratePrivateKey()` calls into the curve's own RNG
  consumer (which reads from `crypto/rand`) — same source the server
  uses for its ephemeral encryption-side keys. Constitution III §3
  approves it.
- `D.SetBytes(zero)` is the canonical Go idiom for clearing a
  `*big.Int`'s magnitude; the original allocation is GC'd whenever
  the key falls out of scope.

### Alternatives considered

- **`ecdsa.GenerateKey(secp256k1.S256(), rand.Reader)`**: rejected
  — the `crypto/ecdsa` package historically has had subtle constant-time
  issues for non-stdlib curves; the `secp256k1.GeneratePrivateKey()`
  path is the project's chosen vendor and matches existing code.
- **Reuse the client's signing key as the ephemeral encryption key**:
  rejected. Constitution III §3 (layer 5) and the threat model both
  require ephemeral, per-request encryption keys to bound damage from
  any single leaked private key.

---

## §8 — `os/exec` invocation shape

### Decision

```go
cmd := exec.CommandContext(ctx, flags.exec, flags.childArgs...)
cmd.Path, _ = exec.LookPath(flags.exec)        // honours absolute paths and PATH
cmd.Env = childEnv                              // built inside SecureBytes.Use(fn)
cmd.Stdin = os.Stdin
cmd.Stdout = os.Stdout
cmd.Stderr = os.Stderr
err := cmd.Run()
```

The exit code is propagated:

```go
if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
    os.Exit(exitErr.ExitCode())                 // rendered through cli.Execute
}
```

In the `cli.Execute` flow, the request RunE function returns a sentinel
that `mapErr` translates into the child's exit code (see
`request.go::errChildExitCode` — wraps the int).

### Rationale

- `exec.CommandContext` ties the child's lifecycle to the request
  context. SIGINT on the parent → `ctx.Done()` → `cmd.Cancel` (default
  `os.Process.Kill`) → child exits → defer chain runs.
- `exec.LookPath` is the canonical PATH-aware resolver. The spec
  forbids shell parsing; `exec.LookPath` does not invoke a shell.
- Setting `cmd.Path` explicitly after `exec.CommandContext` (which
  also calls LookPath internally) is defensive: a future upstream
  change in the cobra wrapper could substitute its own command
  builder, and the explicit assignment makes the property an invariant
  of this package, not a property of the cobra version.

### Alternatives considered

- **`syscall.Exec`** (replace the parent process image): rejected —
  the parent must run its defer chain to zero the ephemeral private
  key. `syscall.Exec` skips deferred functions and does not zero the
  parent's heap before swapping address spaces.
- **`os/exec` with `cmd.SysProcAttr.Setpgid = true`**: deferred to
  SDD-23 (supervisor). For interactive `request`, the child inherits
  the parent's process group so Ctrl-C in the controlling terminal
  hits both — matching operator intuition.

---

## §9 — Sentinel-leak test design

### Decision

`TestRequest_ExecOnlyChildHasSecret` (mandatory per SDD-16):

1. Build an in-process `*server.Server` (SDD-13) seeded with a single
   secret named `SENTINEL_SCOPE` whose value is
   `internal/testutil.SentinelSecret(16)` (=
   `"SECRET_SHOULD_NEVER_APPEAR_16"`).
2. Build a tiny test program (`internal/cli/testdata/echoenv/main.go`)
   that prints its environment to its own stdout. Compile it inside
   `t.TempDir()` via `go build`.
3. Capture the parent's stdout, stderr, and slog output via
   `bytes.Buffer`.
4. Run `hush request ... --exec <test-prog>`.
5. Assertions:
   - The child's captured stdout contains `SENTINEL_SCOPE=` followed
     by the sentinel.
   - `internal/testutil.AssertSentinelAbsent(t, sentinel,
     parentStdout)` passes.
   - Same assertion against parentStderr and the slog handler's
     captured output.
   - A walk over `t.TempDir()` finds no file containing the sentinel.

### Rationale

- The sentinel idiom (`SECRET_SHOULD_NEVER_APPEAR_<n>`) is the project
  standard; `internal/testutil.AssertSentinelAbsent` already provides
  the byte-offset-and-context error format.
- An in-process `*server.Server` exercises the real ECIES round-trip
  AND the real `sign.Sign` / `sign.Verify` path — full coverage of
  the constitutional boundary.
- A real subprocess (rather than a mock `Cmd.Run`) is required because
  AC-5 specifies environment-variable injection — only a real exec can
  prove the child receives the env entry.

### Alternatives considered

- **Use a builtin `cmd.Env` capture**: rejected — would not exercise
  the actual exec syscall, so it would not validate the constitutional
  requirement that the env crossing happens at exec time.
- **Run the test program as `/bin/sh -c 'echo $VAR'`**: rejected — that
  is exactly the shell parsing the spec forbids. The test must
  exercise the same `os/exec` path as production.
