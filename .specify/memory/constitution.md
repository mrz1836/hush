<!--
SYNC IMPACT REPORT
==================
Version Change: 2.0.0 → 3.0.0 (MAJOR amendment — Amendment 1 ratified:
  machine-bound standing supervisor lease carves out Principles II & V)
Ratification: Amendment 1 ratified 2026-07-14 (owner sign-off).
Modified Principles:
  - II Human-in-the-Loop Approval — appended the standing-lease carve-out: an
    opt-in, machine-bound, revocable standing lease MAY reissue an
    already-human-approved supervisor session without a fresh approval; first
    grants stay human, no trusted-host mode, no service-account bypass.
  - V Access Patterns — appended the standing-lease TTL extension: for an
    opted-in standing-lease session only, one establishing approval covers
    reissue until revoked, under the distinguished MaxStandingLeaseTTL ceiling;
    ordinary supervisors keep the 24h ceiling and human-refresh requirement.
Added Principles: N/A
Added Sections: N/A
Removed Sections:
  - "Proposed Amendments (pending ratification)" draft block folded into
    Principles II & V and recorded as ratified in the Amendment Log below.
Templates Requiring Updates:
  - docs/SECURITY.md §4.1/§6, docs/STANDING-LEASE.md, docs/CONFIG-SCHEMA.md,
    docs/DAEMONS.md §6, docs/LIFECYCLE-SCENARIOS.md Scenario 17, docs/API.md —
    all updated in the same body of work.
Follow-up TODOs:
  - Sibling docs (ARCHITECTURE.md, OPERATIONS.md) may cite old principle
    numbers; fix lazily on next touch.

Prior amendment (1.2.0 → 2.0.0, 2026-05-24): 11 principles consolidated to 8;
II renamed "Human-in-the-Loop Approval"; IV Tailscale-Only promoted; V Access
Patterns; VI observability merge; VII Engineering Discipline; VIII Minimal
Dependencies & Ephemeral Vault. Per-principle reference blocks + fuzz-target
lists removed; .claude/commands/hush-audit.md re-aligned to the 8-principle
structure.
-->

# hush Constitution

## Mission

**hush is a Discord-gated secrets broker for AI agents.**

Every API key, OAuth token, and credential lives encrypted in a single vault file on a
trusted host. Agents fetch secrets only after an operator approves the request via
Discord. Approved secrets are delivered over Tailscale, injected into process memory,
and never written to disk on agent machines.

The threat we are eliminating: **commodity malware that scans dotfiles for secrets.**
`~/.zshrc`, `~/.config/gh/hosts.yml`, `~/.aws/credentials`, `.env` files — these are the
first targets when untrusted code runs on a developer machine. With AI agents executing
LLM-generated scripts and packages from across the internet, this attack surface is
unacceptable. hush makes it disappear.

## Core Principles

### I. Zero Files at Rest on Agent Machines

Agent machines MUST have zero secrets on disk. No dotfiles. No `.env`. No keychains.
No tool-specific credential stores. The only acceptable form of a secret on an agent
machine is an environment variable in a process started by hush.

- The clean machine setup checklist MUST remove all known secret stores from agents.
- Tool config files (e.g. `~/.aws/credentials`) MUST NOT exist on agent machines.
- The `gh` CLI MUST work via `GITHUB_TOKEN` env var only — no persistent on-disk auth.
- The vault host is the ONLY place where any encrypted secret material persists.

### II. Human-in-the-Loop Approval

Every fresh secret request MUST require explicit approval from a human via Discord
interactive buttons. There is no auto-approve mode. There is no "trusted host"
exception. There is no service account that bypasses approval.

- The approval DM MUST display: requester host, requested scopes, session type,
  TTL/use limit.
- Approval MUST be an interactive button click (Approve / Deny / Approve & Mute).
- A denied request MUST be logged and MUST NOT be retried automatically.
- Discord bot unavailability MUST return HTTP 503; the server MUST NOT fall back to
  auto-approve under any circumstance.
- Supervisor sessions get ONE approval that covers crashes, updates, and restarts
  within the session TTL — the original Discord approval is still required.

**Standing-lease carve-out (Amendment 1, ratified 2026-07-14).** A supervisor
MAY opt into a machine-bound **standing lease** (`standing_lease = true` with a
required `client_machine_index`) that, after a single human establishing
approval, reissues that same already-approved supervisor session without a
further approval, until it is revoked. This is a narrow, revocable relaxation of
the reissue step only — NOT a blanket auto-approve — and is bounded by ALL of:

- The **establishing / first grant MUST still be a human interactive
  approval.** The `/claim` choke point above is unchanged; a standing lease
  MUST NOT approve any first/fresh request. Only reissue of an already-approved
  session is unattended.
- The lease is **opt-in per supervisor** (default off) and **bound to one
  machine** via `client_machine_index`; the reissue fires only for a claim
  signed by that machine's registered client key from its allow-listed
  Tailscale IP. Any other machine falls back to the human approver — there is
  no trusted-host mode.
- The lease MUST be **scoped** to the supervisor's declared `scope` and MUST
  NOT reach any other secret.
- Every unattended reissue MUST emit a **distinct, hash-chained, ECDSA-signed
  audit event**. A standing lease MUST NOT be silent.
- The lease MUST be **revocable in a single operator action**, after which
  claims return to the full human-approval floor.
- Discord unavailability during an establishing grant MUST still return HTTP
  503 and MUST NOT fall back to approval.

### III. Defense in Depth Through Crypto Layering

hush stacks seven independent security layers. The compromise of any single layer
MUST NOT enable secret extraction.

**Required layers:**
1. **BIP32 HD key hierarchy** — all signing/encryption keys derived at runtime from a
   passphrase + salt. NO key files on disk.
2. **ES256K JWT signing** — asymmetric session tokens via secp256k1.
3. **ECIES transport encryption** — secret values encrypted end-to-end client→server.
4. **ECDSA client request signing** — every client request MUST be signed with a
   registered client key.
5. **mlocked secure memory** — sensitive material held in `SecureBytes` (mlock + zero
   on free); heap-copy hazards documented and avoided.
6. **Signed audit chain** — append-only audit log with hash-chained ECDSA signatures.
7. **Obscurity layers** — random API path prefix, custom vault file format, no
   advertised endpoints — additive only, never load-bearing.

- A future layer MAY be deferred but MAY NOT be silently weakened.
- Cryptographic operations MUST use `crypto/rand` for entropy — never `math/rand`.
- The vault file format is not a standard — its security depends on Argon2id +
  AES-GCM, not on the file layout being secret.

### IV. Tailscale-Only Network Perimeter

The vault server MUST NOT be reachable outside the Tailscale mesh. Ever. There is no
"localhost-only" fallback, no "trusted IP" allowlist, no public TLS endpoint.

- The vault server MUST bind to a Tailscale interface only.
- Tailscale ACLs MUST restrict the vault port to trusted-tag → sandbox-tag grants.
- Startup validation MUST verify the bind address resolves to a Tailscale interface.
- TLS within Tailscale is out of scope — Tailscale provides transport security.
- A future TLS layer MAY be added but MUST NOT relax the Tailscale-only constraint.

### V. Access Patterns: Supervisor for Daemons, Shell-Wrap for Humans

Two access patterns, one binary:

- **Daemons:** `hush supervise <name>` runs a state machine that holds a JWT +
  ephemeral ECIES key across child crashes/restarts within a single Discord approval.
  Daily refresh is anchored to waking hours; a crash in the middle of the night MUST
  NOT page the operator.
- **Humans:** `hush request --exec "zsh"` wraps the SHELL, not the app. One approval
  per day; downstream tool restarts (editors, agents) do not trigger re-approval.

- Supervisor JWTs MUST carry `session_type: "supervisor"` claim.
- Supervisor sessions MUST be TTL-only (not use-count-limited).
- Interactive sessions MUST be use-count-limited.
- A child exit MUST NOT cause the supervisor to exit; the supervisor MUST hold state
  across the child's lifecycle within the session TTL.
- The supervisor MUST zero secret material from its memory after handoff to the child,
  EXCEPT during the optional grace-window cache for restart resilience.
- **Standing-lease TTL extension (Amendment 1, ratified 2026-07-14).** For an
  opted-in standing-lease session only, "one approval covers restarts within the
  session TTL" is extended to "one establishing approval covers reissue of this
  machine-bound, single-scope session until the lease is revoked." A
  distinguished ceiling (`MaxStandingLeaseTTL`) applies **only** to
  standing-lease sessions; ordinary supervisor sessions keep the 24h ceiling and
  the unchanged human-refresh requirement. Standing-lease sessions remain
  TTL-only (never use-count-limited).

### VI. Failure Visibility & Observability

Stale credentials MUST be detectable by the validator, by the child (exit code 78 =
`EX_CONFIG`), and by the operator (Discord alerts via watchdog). Operational logs
MUST NOT leak secret material. Silent failures and logged plaintext are both
unacceptable.

- Pluggable client-side validators MUST run on the supervisor (not the vault server,
  to keep the vault isolated from outbound internet). Validators exist for the
  credential types currently in use.
- Exit code 78 is the contract between child and supervisor for "my creds are stale."
- A per-supervisor local Unix status socket MUST expose freshness state to status
  queries.
- Log-pattern auth-failure tailing is alert-only — it MUST NOT control supervisor
  state.
- Vault unreachability, Discord unavailability, and validator failures MUST produce
  distinct, actionable alerts.
- Structured logging via `log/slog` only; JSON for non-TTY, text for TTYs.
- Secret-bearing types MUST implement `LogValue() slog.Value` returning
  `slog.StringValue("[redacted]")`. Plain `[]byte` carrying secret material MUST be
  wrapped before any code path can log it.
- Error messages return failure mode + identifier (secret name, jti, scope) — never
  the secret value, never a partial of it.
- The hash-chained, ECDSA-signed audit chain is the source of truth for "who fetched
  what, when." Operational logs MUST NOT duplicate audit entries.
- Discord alert tiers:
  - **Critical (page):** vault unreachable, NTP drift over threshold on startup,
    audit-chain signature break, repeated denied requests from a single client.
  - **Warning (channel, no page):** validator failure, single denied request,
    log-pattern watchdog detection, supervisor grace-cache hit.
  - **Info (audit only):** routine approve/deny, JWT issuance, secret rotation.

### VII. Engineering Discipline

hush is a Go project. Every line of Go in this repo MUST follow the patterns encoded
in `.github/tech-conventions/go-essentials.md` and enforced by `.golangci.json`. CLI
design, testing depth, and idiomatic-Go conventions are non-negotiable.

**CLI:**
- Commands follow the noun-verb pattern: `hush <noun> <verb> [args] [flags]`. Small,
  single-file binary with cobra subcommands.
- Global flags: `--config/-c`, `--verbose/-v`, `--quiet/-q`, `--no-color`.
- Output: text for TTY, JSON for pipes/redirects (auto-detect).
- Exit codes: 0 success, 1 error, 2 input error, 3 auth error, 4 not found, 5
  permission, 78 (`EX_CONFIG`) stale credentials.
- `--format eval` MUST require an explicit flag and emit a stderr warning (it prints
  export statements that bypass process injection).

**Testing:**
- Table-driven unit tests for all core functions; `TestFunctionName_Scenario`,
  PascalCase.
- Integration tests gated by `//go:build integration`.
- Pre-commit MUST run `golangci-lint` + `go test -race`.
- Coverage tracked by `codecov.yml`; no PR may regress overall coverage by more than
  2%.
- Release tags require **≥90% overall coverage** AND **100% on security-critical
  packages** (enumerated in the machine-readable block below).
- Fuzz targets cover all parsers and crypto entry points (vault decode, JWT
  validate, ECIES decrypt, request signature, config TOML parsing, key derivation,
  status JSON); each MUST run clean for ≥60s in CI.
- Fuzz goals: no panics, no unbounded memory growth, malformed input returns
  explicit errors, no partial secret exposure in error messages.

**Idiomatic Go:**
- **Context propagation:** `context.Context` is the first parameter of any function
  that does I/O, can be cancelled, or has a timeout. Never store a `Context` in a
  struct field.
- **Error handling:** wrap with `%w`, compare with `errors.Is` / `errors.As`, declare
  sentinel errors as exported package-level `var Err... = errors.New(...)`. Never
  compare error strings.
- **No globals, no `init()`:** mutable package-level state is forbidden;
  side-effectful `init` functions are forbidden. Pass dependencies explicitly.
- **Panic policy:** panics are reserved for `main` startup wiring and unrecoverable
  invariant violations. Library code returns errors. Every spawned goroutine MUST
  `recover()` at its top frame.
- **Goroutine discipline:** every goroutine has a clear owner, an explicit
  cancellation path (context), and a documented termination condition. No
  fire-and-forget goroutines.
- **Interfaces:** accept interfaces, return concrete types. Define interfaces at the
  consumer, not the producer. Prefer single-method interfaces.
- **Package layout:** non-`main` code lives under `internal/`. Public surface area is
  `cmd/hush` only.
- **Modules-only:** Go modules are the single dependency manager. `/vendor` is
  forbidden. `go.mod` and `go.sum` are authoritative.
- **CGO disabled:** all release binaries are pure Go (`CGO_ENABLED=0`). Adding a CGO
  dependency requires a constitutional amendment.

**Security-critical packages** (byte-equality anchor — the
`.github/scripts/coverage-threshold` tool reads the block below verbatim and fails
CI on divergence):

<!-- security-critical-packages: BEGIN (DO NOT EDIT without amending .github/scripts/coverage-threshold/compute.go) -->
internal/keys
internal/vault
internal/vault/securebytes
internal/token
internal/transport/sign
internal/transport/ecies
internal/audit
<!-- security-critical-packages: END -->

### VIII. Minimal Dependencies & Ephemeral Vault

The smallest dependency surface is the strongest dependency surface. The vault file
is treated as ephemeral, not as a backed-up artifact, because the secrets it holds
are rebuildable from upstream sources.

**Minimal dependencies:**
- Prefer the Go standard library. Reach for a third-party package only when the
  stdlib equivalent is missing or materially worse for security.
- Every new direct dependency requires a written justification in the PR covering
  maintainer activity, supply-chain provenance, transitive footprint, and why no
  stdlib option suffices.
- The crypto stack pinned in Principle III is the ONLY cryptographic dependency
  surface — adding another crypto library requires a constitutional amendment.
- `govulncheck` runs in CI on every PR; reported vulnerabilities block merge until
  upgraded, patched, or explicitly waived.
- `gitleaks` runs pre-commit and in CI; zero findings required to merge.

**Ephemeral vault — secrets are rebuildable, not backed up:**
- The vault file is **explicitly NOT backed up.** No off-host copies, no cloud
  snapshot, no Time Machine inclusion (the install path adds an exclusion).
- All secrets in the vault MUST be rebuildable from their upstream source within
  minutes (provider console regenerate, PAT regen, key rotate, OAuth re-consent).
- Loss of the vault file is a recoverable operational event: re-run `hush init`,
  re-add each secret from its upstream source, re-issue client keys. It is NOT a
  disaster.
- The passphrase is held in the macOS Keychain on the trusted host only and is
  regenerable by the operator. There is no escrow, no Shamir split, no recovery
  seed.

## Security Requirements

These constraints apply to ALL code in the repository:

| Requirement | Implementation |
|-------------|----------------|
| Encrypted at rest | Argon2id (time=4, memory=256MB, threads=4) + AES-256-GCM |
| Memory protection | mlock for sensitive data, explicit zeroing, `[]byte`-only for keys |
| Input validation | All external input validated before use; nonce + timestamp on signed requests |
| No hardcoded secrets | Passphrase from OS keystore via stdin pipe (never env var or plist) |
| Secure defaults | Fail closed; explicit flags for `--format eval` and similar dangerous modes |
| Replay protection | Nonce + timestamp on every signed request; nonce cache server-side |
| Token revocation | `/revoke` endpoint; jti tracked in active session map |
| Audit log | Append-only, hash-chained, ECDSA-signed; rotation strategy documented |
| File permissions | Vault: 0600. Supervisor sockets: 0600. Configs: 0640. Dirs: 0750. |
| Clock sync | Startup check against NTP; refuse to start if drift exceeds threshold |
| Supply-chain | `govulncheck` in CI on every PR; `gitleaks` pre-commit + CI with zero-finding requirement; weekly dependency updates |

**Keychain ACLs (macOS):** The passphrase entry MUST restrict access to the `hush`
binary path only. Wildcard ACLs are forbidden.

**Reload semantics:** SIGHUP triggers atomic vault reload via `atomic.Pointer[Vault]`.
In-flight requests complete with the old vault; new requests use the new vault.

## Development Workflow

### Code Quality Gates

All code MUST pass before merge:

1. **Linting:** `magex lint`
2. **Format:** `magex format:fix`
3. **Tests:** `magex test:race`
4. **Pre-commit:** `go-pre-commit` (gitleaks must be zero-finding)
5. **Build:** Clean build via `magex build`

### Commit Standards

- Commits MUST be atomic (one logical change per commit).
- Commit messages follow conventional commits: `type(scope): description`.
- Types: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `security`.
- Security-sensitive changes MUST be tagged `security`.

### Review Requirements

- All cryptographic code requires explicit security-focused review.
- Changes to key derivation, signing, encryption, or session handling require
  security-aware review.
- New dependencies require justification and basic supply-chain audit.
- Supervisor state-machine and Discord-bot interaction changes require integration
  test coverage.

## Governance

This constitution supersedes all other development practices for the hush project.
Amendments require:

1. Written proposal with rationale (PR description).
2. Impact analysis on existing code and any downstream consumers.
3. Version increment per semantic versioning:
   - **MAJOR:** Principle removal or incompatible redefinition.
   - **MINOR:** New principle or materially expanded guidance.
   - **PATCH:** Clarifications, wording, non-semantic refinements.
4. Update to all dependent documentation.

**Compliance:** All PRs and reviews MUST verify adherence to these principles.
Deviations MUST be explicitly justified in the Complexity Tracking section of
implementation plans.

**Version:** 3.0.0 | **Ratified:** 2026-04-26 | **Last Amended:** 2026-07-14

---

## Amendment Log

> Amendment 1 is **RATIFIED** (2026-07-14, owner sign-off) and folded into
> Principles II and V above; the constitution version is bumped `2.0.0 → 3.0.0`.
> The full rationale and guardrails are retained below for the record.

### Amendment 1: Machine-bound standing supervisor lease (RATIFIED 2026-07-14)

**Status:** `RATIFIED 2026-07-14 — owner sign-off; folded into Principles II & V above`
**Classification:** MAJOR amendment (Governance §3 — incompatible redefinition
of Principles II and V). On ratification the constitution version bumped
`2.0.0 → 3.0.0`.
**Design + threat model:** [`docs/STANDING-LEASE.md`](../../docs/STANDING-LEASE.md);
residual risk recorded in [`docs/SECURITY.md`](../../docs/SECURITY.md) §6.

**Rationale.** Some supervised daemons must fire on a fixed schedule around the
clock — an overnight dead-man tripwire, an evening bell, a monthly heartbeat —
with no operator present. Under the ratified model the supervisor TTL is capped
at 24h and the grace cache at 4h, so once the window lapses the next claim (or a
cold restart) falls through to a **recurring human approval**, and the scheduled
action silently does not fire until someone taps Approve. A daemon whose entire
purpose is unattended reliability cannot depend on a periodic phone tap. This
amendment permits a narrow, opt-in, revocable, machine-bound exception so one
such daemon delivers one scoped secret with zero recurring approval, without
weakening the human-approval floor for anything else.

**Carve-out to Principle II (Human-in-the-Loop Approval).** The prohibition on
auto-approve, trusted-host exceptions, and service-account bypass is retained in
full for every **fresh** secret request. A supervisor MAY additionally be
configured with an opt-in **standing lease** that, after a single human
establishing approval, **reissues that same already-approved supervisor
session** without a further approval, subject to ALL of the following
guardrails:

- The lease is **opt-in per supervisor** (`standing_lease = true`, default
  `false`) and **bound to one machine** via a required `client_machine_index`;
  the reissue fires only for a claim signed by that machine's registered client
  key and originating from its allow-listed Tailscale IP.
- The **establishing / first grant MUST still be a human interactive approval.**
  The `/claim` Constitution II choke point is unchanged; a standing lease MUST
  NOT auto-approve any first request. Only reissue of an
  already-human-approved session is unattended.
- The lease MUST be **scoped** to the supervisor's declared `scope` and MUST NOT
  extend to any other secret.
- Every unattended reissue MUST emit a **distinct, hash-chained, ECDSA-signed
  audit event**. A standing lease MUST NOT be silent.
- The lease MUST be **revocable in a single operator action** (revoke the active
  session and/or drop the flag and reload), after which claims return to the
  full human-approval floor.
- Discord unavailability during an establishing grant MUST still return HTTP 503
  and MUST NOT fall back to approval. The lease only affects reissue, never the
  first grant.

**Carve-out to Principle V (Supervisor sessions / one approval covers crashes
within the session TTL).** For an opted-in standing-lease session only, "one
approval covers restarts within the session TTL" is extended to "one
establishing approval covers reissue of this machine-bound, single-scope session
**until the lease is revoked**." A distinguished ceiling (`MaxStandingLeaseTTL`)
applies **only** to standing-lease sessions; ordinary supervisor sessions keep
the 24h ceiling and the unchanged human-refresh requirement. Standing-lease
sessions remain TTL-only (never use-count-limited), and the supervisor MUST
continue to zero secret material after handoff except under the separately
opt-in grace-window cache.

**What this amendment does NOT permit.**

- It does NOT introduce any auto-approve for a first/establishing request.
- It does NOT create a trusted-host mode: presenting the standing config on a
  machine other than the enrolled one falls back to human approval.
- It does NOT widen scope, add a new plaintext cache, or relax any of the seven
  crypto layers or the Tailscale-only perimeter.
- It does NOT change behavior for any supervisor that does not opt in; a config
  without `standing_lease` is byte-for-byte a today's-behavior supervisor.

**Sync-impact applied on ratification (2026-07-14):**

- Version: `2.0.0 → 3.0.0` (MAJOR). ✅
- Principle II: standing-lease carve-out paragraph + guardrails appended. ✅
- Principle V: standing-lease TTL extension + `MaxStandingLeaseTTL` note
  appended. ✅
- `Last Amended` date set to `2026-07-14`. ✅
- Sibling docs updated in the same body of work: `docs/SECURITY.md` (§4.1, §6
  residual-risk row), `docs/STANDING-LEASE.md` (design + threat model),
  `docs/CONFIG-SCHEMA.md` (`standing_lease` field + distinguished ceiling),
  `docs/DAEMONS.md` §6 (standing-lease vs grace cache),
  `docs/LIFECYCLE-SCENARIOS.md` (Scenario 17), `docs/API.md` (`/claim`
  standing-lease reissue). ✅
