# Contract: `hush supervise`

**Branch**: `023-cli-supervise-and-client` | **Date**: 2026-05-12
**Spec section**: FR-023-1 … FR-023-14
**Source files (planned)**: `internal/cli/supervise.go`, `internal/cli/supervise_test.go`,
`internal/cli/supervise_integration_test.go`

This contract is the binding shape of the `hush supervise` subcommand. The
implementation MUST match every clause; deviations require a SPEC amendment.

---

## 1. Synopsis

```text
hush supervise <config-path> [--dry-run] [--grace-window <duration>] [--no-cache]
               [-v|-q] [--no-color] [-c <global-config>]
```

- Exactly **one** positional argument: the path to a supervisor TOML file
  conforming to `docs/CONFIG-SCHEMA.md §"Supervisor config"`.
- Subcommand-specific flags are listed below in §2. Global flags
  (`--config/-c`, `--verbose/-v`, `--quiet/-q`, `--no-color`) inherited from
  the SDD-14 root via `addPersistentFlags`.

---

## 2. Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--dry-run` | `bool` | `false` | Render canonical `/claim` payload to stdout and exit 0; no PID file, no socket, no Discord, no vault contact. |
| `--grace-window` | `time.Duration` | `0s` (sentinel "use config value") | Override `cfg.CacheGraceTTL` for this run. Must be `> 0 && ≤ 4h` if explicit. |
| `--no-cache` | `bool` | `false` | Force `cfg.CacheSecretsForRestart = false` for this run, regardless of config or `--grace-window`. |

Cobra rejects unknown flags via its standard "unknown flag" error (mapped to
`ExitInputErr`).

---

## 3. Positional argument validation

| Condition | Outcome |
|---|---|
| Missing positional argument | Cobra's `cobra.ExactArgs(1)` rejects → stderr `Error: requires exactly 1 arg(s)` → `ExitInputErr`. |
| Argument is a path to a non-existent file | `config.Load` returns wrapped `fs.ErrNotExist` → `mapErr` → `ExitNotFound`. |
| Argument is a path to an unreadable file (mode/perm) | `config.Load` returns wrapped `fs.ErrPermission` → `mapErr` → `ExitPerm`. |
| Argument is a path to a malformed TOML file | `config.Load` returns wrapped `ErrTOMLDecode` → `mapErr` → `ExitInputErr`. |
| Argument is a path to a TOML file with unknown fields | `config.Load` returns wrapped `ErrUnknownField` → `mapErr` → `ExitInputErr`. |
| Argument is a path to a TOML file violating a validator (TTL out of range, etc.) | the specific sentinel maps via `mapErr` to `ExitInputErr`. |

---

## 4. Exit codes

| Code | Symbol | Condition |
|---|---|---|
| 0 | `ExitOK` | Clean run; either dry-run completed, or supervisor exited cleanly on SIGTERM/SIGINT after child cleanup. |
| 1 | `ExitErr` | Generic operational failure: refill exhausted boot retries, PID file lock held by a live owner (duplicate supervisor — FR-023-6), vault unreachable past `boot_retry_timeout`, child failed to start, supervisor lost ctx for unrecoverable reasons. |
| 2 | `ExitInputErr` | Operator input error: missing/unparseable config, invalid `--grace-window` value, unknown flag, config TOML validation failure. |
| 3 | `ExitAuth` | Supervisor JWT rejected with non-`unknown_jti` 401 from vault (the `unknown_jti` case transitions to `awaiting-approval` and does NOT exit). |
| 4 | `ExitNotFound` | Config file path does not exist. |
| 5 | `ExitPerm` | Config file mode rejection, PID-file or socket parent-dir mode laxer than 0700 (`supervise.ErrSocketPermsLoose`). |
| 78 | `ExitConfigStale` | Reserved — only raised when the supervisor surfaces a child's verbatim exit-78 to its parent service manager AND the operator has configured `restart_on_exit_78 = false` (so the supervisor itself terminates instead of restarting). The supervisor's own state-machine handles exit-78 internally via `EventChildExit78Stale`; this exit code is the legacy contract to the service manager only. |

---

## 5. Dry-run output

When `--dry-run` is supplied:

- **Stdout**: the canonical JSON payload that would be sent to the vault
  server's claim endpoint, followed by a single `\n`. Shape:

  ```json
  {"machine_index":2,"name":"example-daemon","reason":"Example long-running daemon","requested_ttl":"20h0m0s","scope":["ANTHROPIC_API_KEY","GITHUB_TOKEN"],"session_type":"supervisor"}
  ```

  - Keys in alphabetical order (locked by `sign.CanonicalJSON`).
  - Compact spacing (no spaces between tokens).
  - `requested_ttl` rendered via `time.Duration.String()`.
  - `scope` order preserved from the config (the canonicaliser sorts
    keys, not array values — config order IS the wire order).
  - `machine_index` carries `cfg.ClientMachineIndex` as an integer.

- **Stderr**: empty on success.
- **Exit**: `ExitOK` (0).
- **Side effects (NONE — FR-023-9)**:
  - No `AcquirePidFile`.
  - No `NewStatusServer.Run` (no socket binding).
  - No `NewChild.Start` (no child process).
  - No HTTP request to the vault server.
  - No Discord call.

Config validation runs **before** the dry-run branch (FR-023-10) — invalid
config → `ExitInputErr` with empty stdout.

---

## 6. Normal startup sequence

In order:

1. `config.Load(ctx, configPath)` — input-error exits land here.
2. Apply flag overrides: `effectiveGraceTTL`, `effectiveCacheEnabled`.
3. `signal.NotifyContext(cmd.Context(), SIGTERM, SIGINT)` → derived `rootCtx`.
4. `AcquirePidFile(cfg.PIDFile)` — `ErrPidLocked` → wrap as
   `errDuplicateSupervisor` with the message `hush: supervise: another supervisor
   is already running for this configuration (pidfile=%s)`. `ErrSocketPermsLoose`
   → `ExitPerm`. Defer `pidfile.Release()`.
5. Build `*Store`, `*Grace`, `*Refiller`, `*Refresher`, `*StatusServer`,
   `*orchestratorInputs`, `*refreshCoalescer`.
6. Wire: `statusServer.attach(inputs)` and
   `statusServer.attachRefreshHandler(coalescer.Handle)`.
7. `wg.Add(2)`; spawn:
   - `go func() { defer wg.Done(); _ = statusServer.Run(rootCtx) }()` —
     owner: this goroutine; cancellation: `rootCtx.Done()`; termination:
     `Run` returns when its watch goroutine sees ctx fire; top-frame
     `recover()` logged.
   - `go func() { defer wg.Done(); _ = refresher.Run(rootCtx) }()` — same
     owner / cancellation / termination shape.
8. Initial `refiller.Refill(rootCtx, cfg.Scope)` — bounded by
   `cfg.BootRetryTimeout` via the existing SDD-21 retry helper. On exhausted
   retries, `ErrBootTimeout` → `ExitErr` with stderr message.
9. Build `child = NewChild(buildChildConfig(...))` with env populated from
   the grace cache.
10. `child.Start(rootCtx)`.
11. Enter the wait loop (Phase 0 R-8).
12. On `rootCtx.Done()`: `child.Forward(SIGTERM)`; `child.Wait()`;
    `wg.Wait()`; return nil (pidfile released via defer).

---

## 7. Signal contract

| Signal | Effect |
|---|---|
| `SIGTERM` | Initiate graceful shutdown. Forwarded to child via `(*Child).Forward(SIGTERM)`. Supervisor waits for child exit, joins all goroutines, releases pidfile, exits 0. |
| `SIGINT` | Same as `SIGTERM` (CLI-interactive operator convenience). |
| `SIGHUP` | **Reserved**. v0.1.0 does not handle SIGHUP in the supervisor. Future amendment may add config-reload semantics. The default Go behaviour (terminate) applies if sent. |
| `SIGUSR1`, `SIGUSR2`, etc. | Forwarded to the child verbatim via `(*Child).Forward(sig)`. Operators may use these to trigger child-specific behaviour (e.g. log rotation in the supervised daemon). |

Documented timing budget for clean SIGTERM shutdown: ≤ 5 s under normal
conditions (SC-023-8).

---

## 8. Error message shapes (FR-023-5, FR-023-6, FR-023-28)

All stderr messages produced by this subcommand follow the locked
`hush: supervise: <one-line message>` prefix. Identifiable cases:

| Condition | Stderr message |
|---|---|
| Missing positional arg | (cobra default) `Error: requires exactly 1 arg(s), only received 0` |
| Config file not found | `hush: supervise: config file not found: <path>` |
| Config file unreadable | `hush: supervise: config file unreadable: <path>: <wrapped fs error>` |
| Config validation failure | `hush: supervise: config invalid: <wrapped validator error>` |
| Duplicate supervisor (FR-023-6) | `hush: supervise: another supervisor is already running for this configuration (pidfile=<path>)` |
| Parent-dir mode loose | `hush: supervise: socket parent directory <path> mode <mode> laxer than 0700` |
| `--grace-window` out of range | `hush: supervise: --grace-window must be >0 and ≤4h, got <value>` |
| Boot retry exhausted | `hush: supervise: boot retry timeout exhausted after <timeout>` |
| Refill auth required (after AwaitingApproval transition) | informational; printed but does not exit |

No error message embeds a secret value or partial thereof (FR-023-27/28,
Constitution X).

---

## 9. Anti-contracts (MUST NOT)

The grep test `TestSupervise_OrchestrationDelegatesToInternalSupervise`
enforces these by static substring matching against the source bytes of
`supervise.go`:

- MUST NOT contain `runtime.GOOS` or any per-OS conditional.
- MUST NOT contain `switch state` or `case StateRunning` / `case StateFetching` etc.
  (state-table reasoning belongs to SDD-19).
- MUST NOT contain hard-coded exit-code arithmetic (e.g. `os.Exit(N)`, `return N`).
  All exit codes come from `mapErr`.
- MUST NOT contain `net.Listen("tcp"`, `net.Listen("tcp4"`, `127.0.0.1:`,
  `http.Server`, `http.ListenAndServe`, or `"Bearer"` literal (Constitution V).
- MUST NOT contain `string(decryptedBytes)` or any conversion of a
  `*securebytes.SecureBytes` payload to a Go `string`.
- MUST NOT contain `init()` or package-level mutable `var`.
- MUST NOT spawn a goroutine without owner/ctx/termination/top-frame `recover()`.

---

## 10. Test surface (mandated)

Per Constitution VIII (TDD-mandatory) and SDD-23 §Prompt 4. Every test
listed below MUST be authored BEFORE the production code it covers.

- Unit: `TestSupervise_DryRunPrintsCanonicalPayload`,
  `TestSupervise_DryRunExitsZero`,
  `TestSupervise_DryRunValidatesConfigFirst`,
  `TestSupervise_GraceWindowOverrideTakesPrecedence`,
  `TestSupervise_GraceWindowExceedsCapRejected`,
  `TestSupervise_NoCacheForcesStrict`,
  `TestSupervise_NoCacheBeatsGraceWindow`,
  `TestSupervise_OrchestrationDelegatesToInternalSupervise` (grep-based),
  `TestSupervise_DuplicateStartRefused`,
  `TestSupervise_SigtermReleasesPidfileAndSocket`,
  `TestSupervise_ConfigNotFoundExitNotFound`,
  `TestSupervise_NoSecretInErrorMessages` (sentinel-leak).
- Integration (`//go:build integration`):
  `TestSuperviseIntegration_DryRunWithDiscordStub` —
  full dry-run preview against a fake supervisor config + DiscordStub,
  asserts no Discord call, parses payload, asserts canonical-form invariants.

Coverage target: ≥ 85 % on `supervise.go` (SC-023-10).
