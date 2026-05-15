# Phase 1 — Data Model: hush request

**Feature**: SDD-16 `hush request`
**Branch**: `016-cli-request`
**Date**: 2026-05-03

This file enumerates every type the subcommand introduces, the
validation rules that fence each field, and the state transitions of
one request from flag parse to defer-chain close. All types are
**unexported**; the chunk contract forbids new exported package-level
symbols in `internal/cli`.

---

## §1 — Flag layer (`requestFlags`)

```go
type requestFlags struct {
    server        string        // --server, e.g. "https://100.97.178.13:7743/h/abc123def"
    scope         []string      // --scope, csv-split into a slice
    reason        string        // --reason
    ttl           time.Duration // --ttl
    maxUses       int           // --max-uses
    machineIndex  uint32        // --machine-index
    execProgram   string        // --exec (program path; argv tail comes from childArgs)
    formatMode    string        // --format (only "eval" accepted; "" means unset)
    childArgs     []string      // positional argv after `--`, becomes child's argv[1:]
}
```

### Validation rules

| Field | Rule | Failure → |
|-------|------|-----------|
| `server` | non-empty after `strings.TrimSpace` | `errMissingFlag` (`--server`) → `ExitInputErr` |
| `scope` | non-empty after csv split; each name matches `^[A-Z][A-Z0-9_]{0,63}$` | `errMissingFlag` (`--scope`) or `errInvalidScopeName` → `ExitInputErr` |
| `reason` | 1–256 bytes | `errMissingFlag` (`--reason`) → `ExitInputErr` |
| `ttl` | parses as positive `time.Duration` | `errMissingFlag` (`--ttl`) → `ExitInputErr` |
| `maxUses` | ≥ `len(scope)` and ≥ 1 | `errMaxUsesTooLow` → `ExitInputErr` |
| `machineIndex` | parses as non-negative uint32 | `errMissingFlag` (`--machine-index`) → `ExitInputErr` |
| `execProgram` xor `formatMode == "eval"` | exactly one set | `errMissingExecOrFormat` (neither) or `errExecAndFormatBothSet` (both) → `ExitInputErr` |
| `formatMode` | when set, must equal literal `"eval"` | `errFormatNotEval` → `ExitInputErr` |

The mutual-exclusion check runs first; downstream validation only runs
when exactly one delivery mode is configured.

### Locked stderr messages

| Sentinel | Message |
|----------|---------|
| `errMissingExecOrFormat` | `hush: request: must specify --exec or --format eval` |
| `errExecAndFormatBothSet` | `hush: request: --exec and --format eval are mutually exclusive` |
| `errFormatNotEval` | `hush: request: --format only accepts the literal value "eval"` |
| `errMaxUsesTooLow` | `hush: request: --max-uses must be ≥ number of scopes` |

`errMissingFlag` (existing in [exit_codes.go](../../internal/cli/exit_codes.go))
covers `--server`, `--scope`, `--reason`, `--ttl`, `--machine-index`
omissions; the per-flag message is rendered as
`hush: request: missing required flag: --<name>`.

---

## §2 — Wire envelope (`claimWireRequest`)

```go
type claimWireRequest struct {
    Scope                []string `json:"scope"`
    Reason               string   `json:"reason"`
    TTL                  string   `json:"ttl"`
    SessionType          string   `json:"session_type"`           // always "interactive"
    EphemeralPubKey      string   `json:"ephemeral_pubkey"`       // 66-char lowercase hex
    Nonce                string   `json:"nonce"`                  // 43-char base64url
    Timestamp            string   `json:"timestamp"`              // RFC3339Nano
    Signature            string   `json:"signature"`              // base64-std (matches revoke)
    RequestID            string   `json:"request_id"`             // 32-char base64url
    MachineName          string   `json:"machine_name"`
    ClientKeyFingerprint string   `json:"client_key_fingerprint"` // 16-char hex
}
```

This struct **mirrors**
[internal/server/claim_handler.go::claimRequest](../../internal/server/claim_handler.go)
exactly — same field names, same JSON tags. The only deliberate diff
is field order in the Go source (cosmetic; does not affect the wire
shape because `encoding/json` honours tag names regardless of source
order).

The signed canonical bytes are derived from a smaller struct that
mirrors the server's `signedPayload`:

```go
type claimSignedPayload struct {
    EphemeralPubKey string   `json:"ephemeral_pubkey"`
    MachineName     string   `json:"machine_name"`
    Nonce           string   `json:"nonce"`
    Reason          string   `json:"reason"`
    RequestID       string   `json:"request_id"`
    Scope           []string `json:"scope"`
    SessionType     string   `json:"session_type"`
    Timestamp       string   `json:"timestamp"`
    TTL             string   `json:"ttl"`
}
```

Nine fields, alphabetical, matches
[internal/server/claim_handler.go::signedPayload](../../internal/server/claim_handler.go).
`sign.CanonicalJSON` is reflective and field-tag-driven, so the byte
sequence on both sides is identical.

---

## §3 — Wire response (`claimWireResponse`)

```go
type claimWireResponse struct {
    JWT       string `json:"jwt"`
    ExpiresAt string `json:"expires_at"`
    JTI       string `json:"jti"`
}
```

Three fields. Mirrors
[internal/server/claim_handler.go::claimResponse](../../internal/server/claim_handler.go).

---

## §4 — Failure response (`claimWireError`)

```go
type claimWireError struct {
    Error     string `json:"error"`
    RequestID string `json:"request_id"`
}
```

The `Error` value is one of the server's locked codes:
`bad_request`, `bad_signature`, `nonce_replay`, `stale_timestamp`,
`ip_not_allowed`, `denied`, `approval_timeout`, `rate_limited`,
`discord_unavailable`, `unknown_outcome`. Each maps to a stderr
message + exit code; see [contracts/cli-request.md §6](./contracts/cli-request.md).

---

## §5 — Dependencies bundle (`requestDeps`)

```go
type requestDeps struct {
    keychain     keychain.Keychain
    httpClient   *http.Client
    nowFn        func() time.Time
    randReader   io.Reader
    hostnameFn   func() (string, error)
    ephemeralKey func(io.Reader) (*ecdsa.PrivateKey, error)
    looker       func(string) (string, error)        // exec.LookPath seam
    runner       func(*exec.Cmd) error               // *Cmd.Run seam (for tests)
    signalCtx    func(parent context.Context, sigs ...os.Signal) (context.Context, context.CancelFunc)
}
```

Production wiring (`productionRequestDeps()`) returns:

```go
{
    keychain:     keychain.New(slog.Default()),
    httpClient:   &http.Client{Transport: &http.Transport{DisableKeepAlives: true, MaxIdleConnsPerHost: 1}},
    nowFn:        time.Now,
    randReader:   rand.Reader,
    hostnameFn:   os.Hostname,
    ephemeralKey: generateEphemeralKey,        // package-internal helper
    looker:       exec.LookPath,
    runner:       func(cmd *exec.Cmd) error { return cmd.Run() },
    signalCtx:    signal.NotifyContext,
}
```

Tests substitute deterministic seams (e.g. a `*httptest.Server` URL,
an in-process `keychain.FakeKeychain`, a fixed-time clock).

---

## §6 — Per-secret material lifetime

Each fetched secret lives in a `*securebytes.SecureBytes`. The slice
of secrets, in scope order, is the only handle:

```go
secrets := make([]*securebytes.SecureBytes, len(flags.scope))
defer func() {
    for _, sb := range secrets {
        if sb != nil {
            _ = sb.Destroy()
        }
    }
}()
```

`Destroy()` is idempotent (per
[internal/vault/securebytes/securebytes.go](../../internal/vault/securebytes/securebytes.go)).
The deferred loop covers happy-path AND every failure path — even
context-cancellation during the fetch loop runs the loop on the slice
populated so far.

The JWT lives in its own `*SecureBytes` for symmetry with the secrets:

```go
jwtSB, _ := securebytes.New([]byte(resp.JWT))
defer func() { _ = jwtSB.Destroy() }()
```

The ephemeral private key's `D` field is zeroed once the request
completes:

```go
defer func() {
    if ephPriv != nil && ephPriv.D != nil {
        ephPriv.D.SetBytes(make([]byte, 32))
    }
}()
```

The reconstituted client signing key receives the same treatment.

---

## §7 — State transitions (one request)

```text
START
  │
  ▼  parseAndValidateFlags(flags)
  │  ─── err: missing/conflicting/malformed → ExitInputErr (no I/O performed)
  │
  ▼  retrieveClientKey(ctx, deps.keychain, machineIndex)
  │  ─── err: ErrKeychainItemNotFound       → ExitErr (locked stderr)
  │  ─── err: ErrKeychainPermissionDenied   → ExitPerm
  │
  ▼  generateEphemeralKey(deps.randReader)
  │  ─── err: rng failure                   → ExitErr
  │
  ▼  buildAndSignClaim(clientKey, ephemeralPub, flags)
  │  ─── err: sign failure                  → ExitErr
  │
  ▼  signal.NotifyContext(ctx, SIGINT, SIGTERM)
  │  ─── ttlCtx, cancel := context.WithDeadline(sigCtx, now+ttl)
  │
  ▼  POST /claim → claimWireResponse
  │  ─── http err:                          → ExitErr (transport-classified)
  │  ─── 200 OK:                            → continue
  │  ─── 403 denied:                        → ExitAuth
  │  ─── 408 approval_timeout:              → ExitErr (locked msg)
  │  ─── 503 discord_unavailable:           → ExitErr
  │  ─── 4xx other:                         → ExitInputErr | ExitAuth (per code)
  │
  ▼  for each scope name N:
  │     GET /s/<N> with Bearer <jwt>
  │     ─── 401 token rejected              → ExitAuth (abort loop)
  │     ─── 404 not found                   → ExitNotFound (abort, no child started)
  │     ─── 200: ecies.Decrypt → SecureBytes
  │     ─── err during loop: abort, defer chain destroys partial secrets
  │
  ▼  switch flags.modeOf() {
  │     case "exec":   runChildWithEnv(ctx, deps, flags, secrets)
  │     case "eval":   writeEvalExports(stdout, stderr, flags, secrets)
  │  }
  │
  ▼  defer chain (LIFO):
  │     1. Destroy each *SecureBytes secret
  │     2. Destroy JWT SecureBytes
  │     3. Zero ephemeral private key D
  │     4. Zero reconstituted client key D
  │     5. cancel() on the signal context
END
```

---

## §8 — Mode-specific data flow

### `--exec` mode

```go
// Inside SecureBytes.Use(fn):
//   - Build env entries one secret at a time:
//       envSlot := append([]byte(nil), name...)
//       envSlot = append(envSlot, '=')
//       envSlot = append(envSlot, secretBytes...)
//       env = append(env, string(envSlot))   // <-- the one place a secret crosses to string
//
// After all secrets are appended, exec the child. The string allocations
// are referenced only by the env slice, which is owned by exec.Cmd.Env.
// The exec syscall hands them to the child kernel-side; the parent's
// strings become unreachable as soon as cmd.Run returns and the
// referencing local variables fall out of scope.
```

Documented residual risk: `string` in Go is immutable; the parent
cannot zero the env-string memory before exec. SECURITY.md §6 lists
"ECIES protects transit, not at-rest in process memory" as the same
trade-off — readable via `/proc/{pid}/environ` (Linux) or `ps eww`
(macOS) by same-user processes. AC-5 accepts this.

### `--format eval` mode

```go
for i, name := range flags.scope {
    var line string
    _ = secrets[i].Use(func(b []byte) {
        line = renderEvalLine(name, b)   // returns "export NAME='value'\n"
    })
    _, _ = io.WriteString(stdout.w, line)
}
_ = stderr.WriteText(formatEvalWarning)   // locked WARNING string
```

`renderEvalLine` does the `'\''` escape inline; the local `line`
string is the brief plaintext crossing per SECURITY.md §6.

---

## §9 — Cross-references

- Server-side wire shape: [internal/server/claim_handler.go::claimRequest / signedPayload / claimResponse](../../internal/server/claim_handler.go).
- ECIES envelope shape: [internal/transport/ecies/ecies.go](../../internal/transport/ecies/ecies.go).
- Canonical-JSON contract: [internal/transport/sign/canonical.go](../../internal/transport/sign/canonical.go).
- SecureBytes contract: [internal/vault/securebytes/securebytes.go](../../internal/vault/securebytes/securebytes.go).
- Keychain contract: [internal/keychain/keychain.go](../../internal/keychain/keychain.go).
- Public-key fingerprint: [internal/keys/](../../internal/keys/).
- Exit-code mapping: [internal/cli/exit_codes.go](../../internal/cli/exit_codes.go).
