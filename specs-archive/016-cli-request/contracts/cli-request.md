# Contract: `hush request` CLI

**Feature**: SDD-16
**Branch**: `016-cli-request`
**Date**: 2026-05-03

This document is the byte-and-behaviour contract for the `hush request`
subcommand. Every literal string in fenced blocks is asserted by a
test; downstream chunks (SDD-25 supervisor in particular) build on this
shape and any change requires a new SDD.

---

## §1 — Synopsis

```text
hush request --server <url> \
             --scope <CSV> \
             --reason <string> \
             --ttl <duration> \
             --max-uses <int> \
             --machine-index <uint32> \
             ( --exec <program> [-- ARGS...] | --format eval )
```

All flags are required. `--exec` and `--format eval` are mutually
exclusive; exactly one must be supplied. Trailing positional argv
after `--` becomes the child's `argv[1:]` verbatim in `--exec` mode and
is ignored in `--format eval` mode.

## §2 — Flag table

| Flag | Type | Required | Default | Allowed values |
|------|------|----------|---------|----------------|
| `--server` | string | yes | (none) | non-empty URL |
| `--scope` | csv string | yes | (none) | comma-separated `[A-Z][A-Z0-9_]{0,63}` names |
| `--reason` | string | yes | (none) | 1–256 bytes |
| `--ttl` | duration | yes | (none) | positive `time.Duration` |
| `--max-uses` | int | yes | (none) | ≥ `len(scope)`, ≥ 1 |
| `--machine-index` | uint32 | yes | (none) | parses as uint32 |
| `--exec` | string | one of {exec, format} | (none) | program path; resolved via `exec.LookPath` |
| `--format` | string | one of {exec, format} | (none) | only literal `eval` |

There are no defaults for any flag. Cobra usage shows `(required)` in
the help text for every required flag.

## §3 — Stdout / stderr contract

### `--exec` mode

- Parent's **stdout**: empty for the duration of parent-controlled
  execution (the child writes its own stdout, which is wired to
  `os.Stdout`).
- Parent's **stderr**: empty on success; on failure, exactly one line
  per the §6 mapping below.
- Child's **stdin/stdout/stderr**: wired to `os.Stdin/Stdout/Stderr`.

### `--format eval` mode

- **stdout**: one line per scope name, in the order the names appear in
  `--scope`. Each line:

  ```text
  export NAME='value'\n
  ```

  where `NAME` is the scope name verbatim and `value` is the secret
  bytes with every `'` byte replaced by the four-byte sequence
  `'\''` (close-quote, backslash, quote, open-quote).

- **stderr**: exactly one line — the locked WARNING:

  ```text
  WARNING: --format eval prints secret values to stdout. They may be captured by terminal scrollback, tmux, or script. Use --exec whenever possible.
  ```

  This string is asserted byte-for-byte by
  `TestRequest_FormatEvalEmitsStderrWarning`.

## §4 — Validation messages (locked, byte-equal)

| Sentinel | stderr message |
|----------|----------------|
| `errMissingExecOrFormat` | `hush: request: must specify --exec or --format eval` |
| `errExecAndFormatBothSet` | `hush: request: --exec and --format eval are mutually exclusive` |
| `errFormatNotEval` | `hush: request: --format only accepts the literal value "eval"` |
| `errMaxUsesTooLow` | `hush: request: --max-uses must be ≥ number of scopes` |
| `errMissingFlag(--server)` | `hush: request: missing required flag: --server` |
| `errMissingFlag(--scope)` | `hush: request: missing required flag: --scope` |
| `errMissingFlag(--reason)` | `hush: request: missing required flag: --reason` |
| `errMissingFlag(--ttl)` | `hush: request: missing required flag: --ttl` |
| `errMissingFlag(--max-uses)` | `hush: request: missing required flag: --max-uses` |
| `errMissingFlag(--machine-index)` | `hush: request: missing required flag: --machine-index` |
| `errInvalidScopeName` | `hush: request: invalid scope name: %q` (single-line) |

## §5 — Wire shape (matches `internal/server/claim_handler.go`)

### Request body

JSON body POSTed to `<server>/claim` (where `<server>` already includes
`/h/<prefix>` per SDD-10's mount). Twelve keys:

```json
{
  "client_key_fingerprint": "<16 hex chars>",
  "ephemeral_pubkey":       "<66 hex chars, lowercase>",
  "machine_name":           "<sanitized hostname, ≤64 chars>",
  "nonce":                  "<43 base64url chars>",
  "reason":                 "<1-256 bytes>",
  "request_id":             "<32 base64url chars>",
  "scope":                  ["<scope name>", ...],
  "session_type":           "interactive",
  "signature":              "<base64-std signature bytes>",
  "timestamp":              "<RFC3339Nano UTC>",
  "ttl":                    "<duration string, e.g. 8h>"
}
```

Content-Type: `application/json`.

### Signed canonical payload

Computed via `sign.CanonicalJSON` over a struct with the nine
non-signature fields (alphabetical):

```json
{
  "ephemeral_pubkey": "...",
  "machine_name":     "...",
  "nonce":            "...",
  "reason":           "...",
  "request_id":       "...",
  "scope":            ["..."],
  "session_type":     "interactive",
  "timestamp":        "...",
  "ttl":              "..."
}
```

Bytes are signed with `sign.Sign(ctx, clientKey, canonical)`. The
resulting raw signature is base64-std encoded into the wire envelope's
`signature` field.

### Success response

```json
{ "jwt": "...", "expires_at": "...", "jti": "..." }
```

Mirrors [internal/server/claim_handler.go::claimResponse](../../internal/server/claim_handler.go).

### Failure response

```json
{ "error": "<code>", "request_id": "<id>" }
```

Codes per §6 below.

## §6 — Exit code mapping

### `--exec` mode

| Outcome | Exit code | Source |
|---------|-----------|--------|
| Child exited with status N (any) | N | `(*exec.ExitError).ExitCode()` propagated through `mapErr` via the `errChildExitCode` sentinel |
| Flag-layer failure | `ExitInputErr` (2) | `errMissingExecOrFormat` etc. |
| Keychain not found | `ExitErr` (1) | `keychain.ErrKeychainItemNotFound` (locked stderr: "client key not found in keychain — run `hush init client --machine-index <N>` first") |
| Keychain permission denied | `ExitPerm` (5) | `keychain.ErrKeychainPermissionDenied` |
| Transport / connection failure | `ExitErr` (1) | reuses `classifyTransportErr` from `health.go`/`revoke.go` |
| `/claim` 400 `bad_request` | `ExitInputErr` (2) | server validated shape; client sent malformed payload |
| `/claim` 403 `denied` | `ExitAuth` (3) | operator denied on Discord |
| `/claim` 403 `bad_signature` | `ExitAuth` (3) | client key not registered server-side |
| `/claim` 408 `approval_timeout` | `ExitErr` (1) | server-side deadline elapsed |
| `/claim` 429 `rate_limited` | `ExitErr` (1) | DM-bucket rate limited |
| `/claim` 503 `discord_unavailable` | `ExitErr` (1) | Discord transport down (FR-021a) |
| `/s/<name>` 401 token rejected | `ExitAuth` (3) | maps via `token.ErrSignatureInvalid` family |
| `/s/<name>` 403 out_of_scope | `ExitAuth` (3) | claim's scope didn't include the name |
| `/s/<name>` 404 not_found | `ExitNotFound` (4) | vault doesn't hold the secret (FR-018: child NOT started) |
| Context cancelled (SIGINT/SIGTERM) | `ExitErr` (1) | `errors.Is(err, context.Canceled)` |
| Context deadline (--ttl elapsed) | `ExitErr` (1) | locked stderr: "approval wait exceeded --ttl" |

### `--format eval` mode

Same mapping as above, with the exception that the success path exits
`ExitOK` (0).

## §7 — Lifecycle invariants (asserted by tests)

| Invariant | Test |
|-----------|------|
| Both flags unset → no `/claim` POST | `TestRequest_RequiresExecOrFormat` |
| Both flags set → no `/claim` POST | `TestRequest_ExecOrFormatMutuallyExclusive` |
| `--format json` rejected | `TestRequest_FormatRejectsNonEval` |
| Stderr WARNING byte-equal in eval mode | `TestRequest_FormatEvalEmitsStderrWarning` |
| Single-quote bytes in secret survive eval round-trip | `TestRequest_FormatEvalEscapesSingleQuote` |
| Child receives every secret as env var | `TestRequest_ExecInjectsEnvVars` |
| Sentinel in child env, ABSENT from parent stdout/stderr/log | `TestRequest_ExecOnlyChildHasSecret` |
| Ephemeral key D zeroed after Run | `TestRequest_PostExecZeroesEphemeralKey` |
| JWT not written to any file under tempdir/cwd | `TestRequest_NeverWritesJWTToDisk` |
| Client signing key sourced only from keychain | `TestRequest_ClientKeyFromKeychainNotEnv` |
| `session_type` claim equals "interactive" | `TestRequest_ClaimSessionTypeIsInteractive` |
| Partial `/s` failure → child not started | `TestRequest_PartialFetchFailureAbortsBeforeChild` |
| Child exit code propagated | `TestRequest_PropagatesChildExitCode` |
| Operator denial → ExitAuth, no decrypt | `TestRequest_DeniedOnDiscordExitsAuth` |
| Errors carry no secret bytes | `TestRequest_ErrorsDoNotLeakSecretBytes` |

## §8 — Failure-message wording (locked)

| Class | stderr line |
|-------|-------------|
| Keychain miss | `hush: request: client key not found in keychain — run \`hush init client --machine-index <N>\` first` |
| Keychain perm | `hush: request: keychain access denied (per-binary ACL refused)` |
| Transport down | `hush: request: could not connect to hush server at <url>: <classified-error>` (matches health.go classifier) |
| Approval denied | `hush: request: approval denied on Discord` |
| Approval timed out (server) | `hush: request: server reported approval timeout` |
| Approval timed out (client deadline) | `hush: request: approval wait exceeded --ttl` |
| Discord unavailable | `hush: request: Discord bot unavailable; vault server returned 503` |
| Partial fetch | `hush: request: secret %q not present in vault; aborting before child start` |
| Interrupted (SIGINT/SIGTERM) | `hush: request: interrupted; pending request will expire server-side at --ttl` |

## §9 — Forbidden behaviours (asserted by negative tests)

- The subcommand MUST NOT call `os.Getenv` to resolve any flag value or
  the signing key. (Lint + grep test.)
- The subcommand MUST NOT write any file under `t.TempDir()`,
  `os.TempDir()`, or `~/.hush/` for the duration of the run.
- The subcommand MUST NOT pass any decrypted secret value to a
  `*slog.Logger` call. The only allowed log fields are identifiers
  (`scope`, `jti`, `request_id`, `client_ip`, error class).
- The subcommand MUST NOT shell-parse the `--exec` argument. The only
  allowed splitting is between `--exec` (program path) and the
  positional argv after `--`.
- The subcommand MUST NOT emit a default delivery mode that prints
  secrets to stdout. Both flags unset → input error before any I/O.

---

## §10 — Versioning note

The wire shape is locked at SDD-12 (server-side) and mirrored at
SDD-16. Future SDDs (e.g. supervisor) MAY add fields to the wire
envelope, but only via the locked-extension pattern: append fields with
the appropriate JSON tag; never rename or remove an existing field. The
client's wire encoder uses tagged structs, so adding a field here MUST
be coordinated with a server change first (the server's
`DisallowUnknownFields` would otherwise reject the extra field).
