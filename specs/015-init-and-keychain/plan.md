# Implementation Plan: hush init — server + client bootstrap with OS-keychain ACL

**Branch**: `015-init-and-keychain` | **Date**: 2026-05-03 | **Spec**: [spec.md](./spec.md)
**Input**: Feature specification from `/specs/015-init-and-keychain/spec.md`
**Chunk contract**: [docs/sdd/SDD-15.md](../../docs/sdd/SDD-15.md)

## Summary

`hush init` is the one-time bootstrap for a fresh installation, with two
mutually-exclusive modes:

- **`hush init server`** generates a 16-byte cryptographically random salt,
  prompts the operator for a passphrase (with confirmation, ≥ 12 chars,
  TTY-only), derives the master seed via `keys.DeriveMasterSeed`, opens an
  empty `*vault.Store` at `<state_dir>/secrets.vault` via `vault.Save`,
  prompts for the Discord bot token, stores **both** the bot token and the
  vault passphrase in the OS keychain with a per-binary ACL, and atomically
  writes `<state_dir>/config.toml` at mode `0600` populated with **every**
  default declared in `docs/CONFIG-SCHEMA.md`.
- **`hush init client --machine-index N`** prompts for the same passphrase
  (with confirmation, TTY-only), derives the per-machine client signing
  keypair via `keys.DeriveClientKey(seed, N)`, marshals the secp256k1
  private key into a portable byte form, stores it in the OS keychain under
  `(service="hush-client", account="machine-<N>")` with the same per-binary
  ACL, and prints **one** `SHA256:<base64-no-padding>` fingerprint line to
  stdout.

The implementation introduces a new `internal/keychain` package that wraps
the platform-native keychain operations behind a single `Keychain`
interface. The Darwin implementation shells out to `/usr/bin/security`
with the `-T` flag set to the absolute path of the running `hush` binary
(resolved via `os.Executable()`); the Linux implementation wraps
`github.com/zalando/go-keyring` and is **provided for cross-platform
compilation only** — `hush init` itself refuses to run on Linux today
because the Linux Secret Service does not expose a per-binary ACL
mechanism equivalent to macOS `-T` (FR-020a).

A test-injectable in-process `FakeKeychain` covers all platform-agnostic
unit tests; the Darwin command-construction is verified by a build-tagged
test (`//go:build darwin`) that intercepts the `exec.Cmd` argv vector.

## Technical Context

**Language/Version**: Go 1.26.1 (module `github.com/mrz1836/hush`,
`go.mod` declares `go 1.26.1`).
**Primary Dependencies**:
- `github.com/spf13/cobra` (already direct, mounted via `internal/cli`)
- `github.com/zalando/go-keyring` (**NEW direct dep** — see research §1)
- `github.com/pelletier/go-toml/v2` (already direct, used to write
  `config.toml` with stable, documented field names)
- `golang.org/x/term` (already direct, no-echo TTY reads)
- Standard library: `context`, `crypto/rand`, `crypto/sha256`,
  `encoding/base64`, `log/slog`, `os`, `os/exec`, `path/filepath`,
  `runtime`.
**Storage**:
- `~/.hush/secrets.vault` (mode `0600`, owner only) — created by
  `vault.Save` (SDD-03) with a fresh 16-byte salt.
- `~/.hush/config.toml` (mode `0600`, owner only) — atomic write
  (`config.toml.tmp` → `fsync` → `rename`).
- OS keychain items, all created with hush-binary-only ACL:
  - server-mode: `(service=cfg.Discord.BotTokenKeychainItem,
    account="hush-server")` for the Discord bot token;
    `(service="hush-vault-passphrase", account="hush-server")` for the
    vault passphrase
  - client-mode: `(service="hush-client",
    account=fmt.Sprintf("machine-%d", N))` for the per-machine private
    key
**Testing**: standard library `testing` + table-driven cases per
`.github/tech-conventions/testing-standards.md`. Integration tests gated
by `//go:build integration`. `internal/testutil.SentinelSecret(15)` is
the canonical sentinel for FR-022 sentinel-leak assertions. Darwin
command-construction tests use `//go:build darwin` and a `looker`
(`exec.LookPath`) seam that records the argv vector instead of running
the real `security`. PTY-driven prompt tests reuse `github.com/creack/pty`
(already a test-only dep from SDD-14).
**Target Platform**: macOS (darwin) production; Linux build target
exists but `hush init` returns FR-020a's platform-incompatibility error
on Linux until a per-binary ACL mechanism (e.g. flatpak portals,
polkit, or AppArmor profile gating) is established for hush.
**Project Type**: CLI subcommand on top of an existing single-binary Go
CLI (cobra root in `internal/cli`).
**Performance Goals**: not applicable — bootstrap is interactive and
one-shot. Argon2id derivation respects the locked Constitution III
parameters (time=4, memory=256 MiB, threads=4); a single derivation
must complete within the operator's tolerance for a one-time prompt
(SC-001 ≤ 3 minutes including human typing).
**Constraints**:
- Passphrase ≥ 12 characters; rejection happens BEFORE any KDF
  invocation, file write, or keychain call (FR-003, SC-010).
- `config.toml` and the vault file are written at mode `0600`.
- All keychain items are created with a per-binary ACL; no wildcard ACL
  is acceptable (FR-019, FR-020).
- The passphrase is read **only** from the controlling TTY with echo
  off; it is never read from `os.Getenv` and never from a positional
  CLI argument or flag value (FR-001, FR-005, FR-005a, SC-007).
- The init command never generates a passphrase on the operator's
  behalf (FR-002).
- No output stream (stdout, stderr, slog operational log) carries the
  passphrase, the bot token, the master seed, the derived subkeys, or
  the per-machine private key in any form (FR-022, SC-006).
- On Linux, init refuses to run with a clear platform-incompatibility
  message (FR-020a); no vault file, no `config.toml`, no keychain item
  is written.
- Existing vault file at the target path → refuse and exit non-zero
  (FR-012); existing keychain item under the same `(service, account)`
  pair → refuse and exit non-zero (Clarification 2026-05-03 Q1).
**Scale/Scope**: ~600 LOC across the new files (init.go ≈ 350,
keychain.go ≈ 60, keychain_darwin.go ≈ 110, keychain_linux.go ≈ 80, plus
tests). Coverage target: 85% on `internal/cli` (init portion) and 85%
on `internal/keychain`; passphrase-resolution and sentinel-leak code
paths reach 100% (SDD-15 contract).

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1
design.* Principles in scope: **I, III, VII, IX, Security Requirements**.
Principles X (redaction), XI (deps), VIII (testing) also evaluated
because of the new dependency and crypto-adjacent surface.

| Principle / Requirement | Gate | Verdict |
|---|---|---|
| **I. Zero files at rest on agents** — client-mode init MUST NOT leave a key file on the agent's disk. | Client-mode stores only into the OS keychain; no fallback to a `~/.hush/clients/<N>.key` file is added. The agent's `~/.hush/` directory is not created by client-mode init. | PASS |
| **III. Defense in depth** — keys are derived at runtime, not stored as files; `crypto/rand` for salt; Argon2id parameters frozen. | Salt: 16 B `crypto/rand`. Master seed: `keys.DeriveMasterSeed` (already audited, Argon2id time=4, memory=256 MiB, threads=4). Subkeys via existing `keys.Derive*` (already SDD-01-locked). No new crypto code beyond a SHA-256 + base64 fingerprint helper. | PASS |
| **VII. CLI design standards** — cobra subcommands, locked exit codes, TTY/JSON output. | `init` is mounted as `hush init server` and `hush init client` via the existing `internal/cli` cobra root (SDD-14). Errors map to the seven locked exit codes (`ExitOK`, `ExitErr`, `ExitInputErr`, `ExitAuth`, `ExitNotFound`, `ExitPerm`, `ExitConfigStale`). Output: text on TTY (the only supported channel — init is TTY-only); on a non-TTY stdout the success line is the fingerprint only (still a single line, suitable for redirection). | PASS |
| **IX. Idiomatic Go** — `context.Context` first, no `init()`, no globals, errors wrapped with `%w`, sentinels exported, accept-interface-return-concrete. | `Keychain` is a single-method-per-operation interface defined in the **consumer** (init.go imports it from `internal/keychain`); the producer returns the concrete `*Keychain` only via `New(logger) (Keychain, error)` because the platform impl is selected at construction time — accept-interface-return-concrete is satisfied at the call sites. No `init()` in either new package. No mutable package-level state. | PASS |
| **Security Requirements — passphrase ≥ 12, never env, no plist** | Validated up-front (length check before KDF). Read only from TTY; `os.Getenv` is forbidden in init.go. Sentinel-leak tests assert the no-env contract by setting `HUSH_PASSPHRASE`/`PASSPHRASE` to `SECRET_SHOULD_NEVER_APPEAR_15` and proving the resulting vault opens with the TTY-supplied value, never the env value (SC-007). | PASS |
| **Security Requirements — Keychain ACLs (macOS)** — per-binary, no wildcards | Darwin impl invokes `security add-generic-password -s <service> -a <account> -T <abs-path-to-hush>` with the absolute path resolved via `os.Executable()`. No `-A` (wildcard allow-all) flag is ever passed. Linux refuses up-front (FR-020a). | PASS |
| **VIII. Testing** — security-critical = 100%, AC mapping | AC-1 (`hush serve` startup) and AC-6 (per-machine client keys + Keychain ACL) get unit + integration tests. Mandatory test list from SDD-15 + `tasks` prompt (`TestInitServer_RefusesShortPassphrase`, `TestInitServer_CreatesVaultWith0600`, `TestInitServer_CreatesConfigWithAllDefaults`, `TestInitServer_NeverReadsPassphraseFromEnv`, `TestInitClient_RequiresMachineIndex`, `TestInitClient_StoresInKeychainWithACL` (`//go:build darwin`), `TestInitClient_PrintsFingerprintOneLine`, `TestInitClient_ConflictsWithServerMode`, `TestKeychain_StoreRetrieveRoundTrip` (fake), `TestKeychain_DeleteRemoves`, `TestKeychainDarwin_ConstructedSecurityCommand`, `TestKeychainLinux_RefusedByInit`). 100% coverage on the passphrase-resolution and sentinel-leak code paths; 85% overall on the two packages. | PASS |
| **X. Observability & redaction** — `*securebytes.SecureBytes` for any secret; no secret in error messages | Passphrase, bot token, and per-machine private key all flow through `*securebytes.SecureBytes`. The fingerprint helper takes the **public** key and returns a string — no secret crosses the boundary. Errors are sentinel-class with static messages. | PASS |
| **XI. Native-first / minimal deps** — every NEW direct dep needs written justification | `github.com/zalando/go-keyring` is the **only** new direct dependency. Justification (research §1): no Go stdlib library wraps the freedesktop.org Secret Service D-Bus API; the alternative is hand-rolling a D-Bus client (≈ 800 LOC of custom protocol code) or shelling out to `secret-tool` from the libsecret package (operational dependency on a system-installed tool, no compile-time check). zalando/go-keyring is ≈ 1.4 K stars, MIT-licensed, transitively pulls only `github.com/godbus/godbus/v5` (the canonical D-Bus binding) — both stable, low-churn, no transitive crypto. The Linux backend is a build target only; Linux init refuses today, so the dep is exercised only in tests until a per-binary ACL gate exists. | PASS — with documented justification |

**Verdict**: All gates PASS. No Complexity Tracking entries required.

## Project Structure

### Documentation (this feature)

```text
specs/015-init-and-keychain/
├── plan.md              # This file
├── spec.md              # Feature spec (already written)
├── research.md          # Phase 0 output (this command)
├── data-model.md        # Phase 1 output (this command)
├── quickstart.md        # Phase 1 output (this command)
├── contracts/
│   ├── keychain-api.md  # internal/keychain locked surface
│   └── cli-init.md      # `hush init server` / `hush init client` CLI contract
└── tasks.md             # Phase 2 output (/speckit-tasks command — NOT created here)
```

### Source Code (repository root)

The feature touches **two** packages: an existing one (`internal/cli`)
and a new one (`internal/keychain`). No other internal package is
modified.

```text
internal/cli/
├── init.go                        # NEW — cobra `init` parent + `server` + `client` subcommands
├── init_test.go                   # NEW — unit tests (fake Keychain, fake TTY, fake vault dir)
├── init_integration_test.go       # NEW — //go:build integration — full init dance in t.TempDir
├── root.go                        # MODIFIED — mount newInitCmd() under root
├── exit_codes.go                  # MODIFIED — add init-specific sentinel errors mapped to existing codes
└── (existing files unchanged: serve.go, health.go, version.go, revoke.go, ...)

internal/keychain/                 # NEW PACKAGE
├── doc.go                         # package overview
├── keychain.go                    # interface, sentinel errors, New() factory, FakeKeychain
├── keychain_darwin.go             # //go:build darwin — security-CLI implementation
├── keychain_linux.go              # //go:build linux — zalando/go-keyring implementation
├── keychain_test.go               # cross-platform: FakeKeychain round-trip tests
├── keychain_darwin_test.go        # //go:build darwin — argv-vector verification
└── keychain_linux_test.go         # //go:build linux — backend interaction + init-refusal contract

cmd/hush/main.go                   # NO CHANGE — still 2-line `os.Exit(cli.Execute(ctx))`
```

**Structure Decision**: The chunk slots cleanly into the existing
single-binary CLI layout. No `cmd/` reshuffling, no new top-level
directories, no cross-package imports beyond what is already permitted
by `docs/PACKAGE-MAP.md` (`internal/cli` orchestrates domain packages;
`internal/keychain` is a low-level domain package on the same tier as
`internal/keys` / `internal/vault`). The new `internal/keychain` is
imported by `internal/cli`; nothing imports it back.

## Complexity Tracking

> No Constitution Check violations — this section is intentionally empty.

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|--------------------------------------|
| _none_    | _n/a_      | _n/a_                                |
