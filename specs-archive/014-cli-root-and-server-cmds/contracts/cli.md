# Public CLI Contract — SDD-14

**Branch**: `014-cli-root-and-server-cmds` | **Date**: 2026-05-01

This document is the **public contract** of the four operator-facing
subcommands. Operator scripts depend on this surface. Once published,
changes to it require a constitutional amendment (FR-006).

The contract has five sections: command grammar, global flags,
exit-code map, per-subcommand IO contract, and stable JSON shapes.

---

## 1. Command grammar

```
hush [global flags] <subcommand> [subcommand flags] [positional args]
```

**Subcommands defined by this chunk**:

```
hush serve
hush health     [--server <url>]
hush version
hush revoke     --server <url> --jti <uuid>
```

Subcommands NOT defined by this chunk (delivered by SDD-15..23):

```
hush init
hush request    --scope <name> --reason <text> [--ttl <duration>] [--exec <cmd>] [--format eval]
hush secret     <add|list|rotate|remove>
hush supervise  --config <path>
hush client     <status|refresh>
```

Each future subcommand inherits the same global flags and exit-code
contract — SDD-14's responsibility ends at delivering the root +
the four covered above.

---

## 2. Global flags (persistent, inherited by every subcommand)

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--config` | `-c` | string | `~/.hush/config.toml` | Path to the configuration file. Read by `serve` (and by future `secret`, `supervise`); ignored by `health`, `version`, `revoke` in this chunk. |
| `--verbose` | `-v` | bool | `false` | Add a stderr trace of resolved configuration values and step transitions. Mutually exclusive with `--quiet`. |
| `--quiet` | `-q` | bool | `false` | Suppress all non-error stderr output; trim stdout to the essential machine-parseable result. Mutually exclusive with `--verbose`. |
| `--no-color` | (none) | bool | `false` | Force no ANSI styling sequences in any output stream regardless of terminal detection. |

**Global validation**:
- `--verbose` AND `--quiet` simultaneously → `ExitInputErr` (2) with
  message `"--verbose and --quiet are mutually exclusive"`.
- `--config <path>` unreadable → `ExitInputErr` with message naming
  the file and the OS error class (e.g., `"could not read config file
  /home/op/.hush/config.toml: permission denied"`).
- `--config` flag without value → cobra's standard "flag needs an
  argument" message → `ExitInputErr`.

---

## 3. Exit-code map (the operator-script contract)

| Code | Constant | Mapped from | Subcommand emitters (this chunk) |
|------|----------|-------------|----------------------------------|
| `0` | `ExitOK` | clean completion | `serve` (clean shutdown), `health` (all dimensions healthy), `version` (always when build OK), `revoke` (HTTP 200) |
| `1` | `ExitErr` | network failure, server 5xx, partial-health (`health`), panic recovery, unknown errors | all four |
| `2` | `ExitInputErr` | missing flag, conflicting flags, malformed `--jti`, unreadable config, no passphrase source | all four |
| `3` | `ExitAuth` | bad passphrase (vault decrypt fails — `vault.ErrAuthFailed`), `revoke` HTTP 401/403 (signature rejected) | `serve`, `revoke` |
| `4` | `ExitNotFound` | `--config` file missing, `revoke` HTTP 404 (server distinguishes unknown jti) | `serve`, `revoke` |
| `5` | `ExitPerm` | `os.ErrPermission`, `server.ErrFileModeLoose`, bind permission denied | `serve` |
| `78` | `ExitConfigStale` | **NOT raised by this chunk** (reserved for the supervisor↔child contract delivered by SDD-15/SDD-23) | none |

**Contract guarantees (SC-002)**:
- 100% of subcommand terminal outcomes in this chunk map to one of
  the seven values above.
- An outcome NEVER exits with an out-of-contract code (e.g., 6, 77,
  127). Asserted by `TestExitCodes_AllSentinelsCovered` and by
  `TestExitCodes_NoStaleConfigInThisChunk`.

---

## 4. Per-subcommand IO contract

### 4.1 `hush serve`

**Synopsis**: `hush serve [global flags]`

**Stdin**:
- If a pipe (`!IsTerminal(stdin)`): all bytes are read as the vault
  passphrase. Exactly one trailing `\n` or one trailing `\r\n` is
  stripped; all other bytes are preserved verbatim. Zero bytes piped
  → fall through to TTY (if attached) or fail.
- If a TTY (`IsTerminal(stdin)`): a no-echo prompt `"Vault passphrase: "`
  is written to stderr; bytes typed are read via `term.ReadPassword`.
- If neither: terminal failure with `ExitInputErr` and message
  `"no passphrase source: stdin is not a pipe and is not a terminal"`.

**Stdout**: Empty during normal operation. The chassis's structured
log records go to stderr via `log/slog`.

**Stderr**:
- The no-echo passphrase prompt (TTY mode only).
- Server lifecycle log records (info-level: startup, ready, shutdown).
- Errors during startup (decrypt failure, bind failure, etc.).
- Verbose mode: additional trace of config-file path, stdin-mode
  detection, key-derivation completion, audit writer started.

**Signals**:
- `SIGINT` / `SIGTERM` → graceful shutdown via the chassis's existing
  `Run` cancellation path. In-flight requests complete; new requests
  refused; process exits with `ExitOK` (clean) or `ExitErr` (chassis
  reported error during shutdown).
- `SIGHUP` → atomic vault reload via the chassis's existing `ReloadVault`
  path (registered by the chassis itself; `serve` does not register
  a separate handler).

**Exit codes**: 0 (clean shutdown), 1 (generic), 2 (input — no passphrase
source / unreadable config), 3 (auth — vault decrypt failure), 4
(not-found — config file missing), 5 (perm — bind/permissions).

### 4.2 `hush health`

**Synopsis**: `hush health [--server <url>]`

If `--server` is omitted, the address is read from the loaded config's
`Server.ListenAddr` field (with the random `path_prefix` joined to form
the full URL `http://<addr>/h/<prefix>/hz`).

**Stdin**: Not consumed.

**Stdout**:
- TTY: a human-readable table of the eight dimensions in the locked
  order (`status, uptime, secrets_count, active_tokens,
  discord_connected, config_valid, vault_loaded, clock_in_sync`).
  Each row prints the dimension name and its value; healthy rows in
  green checkmarks (suppressed by `--no-color`), unhealthy rows in
  red crosses.
- Non-TTY: the raw `HealthSnapshot` JSON (no transformation, no
  added wrapper). Mirrors the server's `/hz` response body byte-for-
  byte (Edge Case "Health JSON shape stability").

**Stderr**:
- Empty on success.
- On connection refused: literal text `"could not connect to hush
  server at <addr>: connection refused"` (FR-014; the literal-text
  contract is implementation-locked by SDD-14 Prompt 3).
- On timeout: `"could not connect to hush server at <addr>: timeout
  after 5s"` (FR-015a clarification — the 5-second value is hard-coded;
  no `--timeout` flag in this chunk).
- On other transport failures: `"could not connect to hush server at
  <addr>: <classifier>"` where `<classifier>` is one of `no route`,
  `name resolution failed`, `EOF`, etc.
- Verbose mode: the URL hit, the HTTP status code received, the
  response-body byte count.

**Exit codes**: 0 (all dimensions healthy), 1 (unreachable, partial-
health, server 5xx), 2 (malformed `--server` URL).

### 4.3 `hush version`

**Synopsis**: `hush version`

**Stdin**: Not consumed.

**Stdout**:
- TTY: human-formatted lines:

  ```
  hush version v0.1.0
  commit:  fb3e402
  built:   2026-05-01T12:34:56Z
  ```

  Development build:

  ```
  hush version dev
  commit:  unknown
  built:   unknown
  ```

- Non-TTY: locked JSON shape (FR-019a):

  ```json
  {"version":"v0.1.0","commit":"fb3e402","date":"2026-05-01T12:34:56Z"}
  ```

  Development build:

  ```json
  {"version":"dev","commit":"unknown","date":"unknown"}
  ```

  All three keys ALWAYS present; the placeholder values for missing
  metadata are the literal strings `"dev"`, `"unknown"`, `"unknown"`.

**Stderr**: Empty.

**Exit codes**: 0 (always; `version` has no failure mode in this
chunk).

### 4.4 `hush revoke`

**Synopsis**: `hush revoke --server <url> --jti <uuid>`

Both flags are required. Either omitted → `ExitInputErr` with message
`"missing required flag: --server"` or `"missing required flag: --jti"`.

**Stdin**: Consumed for the per-machine client signing key — same
passphrase resolution path as `serve` (pipe → TTY → fail). The signing
key is `keys.DeriveClientKey(masterSeed, machineIndex)` where
`machineIndex` is read from the loaded config's
`Server.MachineIndex` field.

**Stdout**:
- TTY: `"revoked jti=<id>"` on success.
- Non-TTY: JSON `{"revoked":"<id>"}` on success.

**Stderr**:
- Empty on success.
- On HTTP 401/403: `"server rejected revocation: signature invalid
  (or jti unknown — server treats both alike)"` → `ExitAuth`.
- On HTTP 404: `"server reported jti not found: <id>"` → `ExitNotFound`.
- On HTTP 5xx: `"server returned <status>: <body excerpt>"` (the body
  excerpt is the first 256 bytes of the response, with control chars
  replaced by `?`; NEVER includes the raw signed-request payload) →
  `ExitErr`.
- On connection refused / timeout: same shape as `health`'s
  failure messages → `ExitErr`.
- Verbose mode: the canonical JSON bytes that were signed (with the
  signature itself NOT printed), the URL hit, the HTTP status received.

**Exit codes**: 0 (HTTP 200), 1 (network/5xx), 2 (missing flag,
malformed `--jti`/`--server`), 3 (HTTP 401/403), 4 (HTTP 404), 5
(passphrase source unavailable for key derivation — same constraint
as `serve`).

---

## 5. Stable JSON shapes (machine consumers)

### 5.1 `version` (locked at FR-019a)

```json
{"version":"<string>","commit":"<string>","date":"<string>"}
```

Three keys, always present, always strings. Adding a key is a
breaking change.

### 5.2 `health` (mirrors server's `/hz`)

```json
{
  "status":"ok",
  "uptime":"1h23m45s",
  "secrets_count":12,
  "active_tokens":3,
  "discord_connected":true,
  "config_valid":true,
  "vault_loaded":true,
  "clock_in_sync":true
}
```

Eight keys, in this exact order, always present. The CLI emits the
server's response body verbatim — no re-marshaling, no field
suppression.

### 5.3 `revoke` (success)

```json
{"revoked":"<uuid>"}
```

One key. The supplied `--jti` is echoed back so a script can confirm
the operation.

### 5.4 `serve` (no JSON — text-only stderr lifecycle)

`serve` does not emit a structured success/failure JSON document
because the success state is "the process is still running". Lifecycle
events go through the chassis's `log/slog` JSON output (FormatJSON when
stderr is not a TTY) which is governed by the chassis's locked surface,
not this chunk.

---

## 6. Error message wording (locked verbatim)

Operators script against substrings in error messages. The following
strings are part of the contract:

| Subcommand | Condition | Literal text |
|------------|-----------|--------------|
| `serve` | no passphrase source | `no passphrase source: stdin is not a pipe and is not a terminal` |
| `health` | connection refused | `could not connect to hush server at <addr>: connection refused` |
| `health` | timeout | `could not connect to hush server at <addr>: timeout after 5s` |
| `revoke` | connection refused | `could not connect to hush server at <addr>: connection refused` |
| `revoke` | missing `--server` | `missing required flag: --server` |
| `revoke` | missing `--jti` | `missing required flag: --jti` |
| `revoke` | malformed `--jti` | `invalid --jti: must be a UUID` |
| any | `--verbose` + `--quiet` | `--verbose and --quiet are mutually exclusive` |

Other failure messages are free-form; only the strings above are
contract-locked.

---

## 7. Forward-compatibility commitments

- Adding a new subcommand to a future SDD chunk does NOT change the
  exit-code semantics of the four subcommands here.
- Adding a new GLOBAL flag in a future chunk MAY introduce new
  failure modes but MUST NOT remove or remap existing exit codes.
- The seven exit-code constants are public, named symbols
  (`internal/cli.ExitOK`, etc.). Their numeric values are stable
  across releases.
- The locked JSON shapes above remain shape-stable; new keys are a
  breaking change requiring a major-version bump per Constitution
  governance.
