<!--
SYNC IMPACT REPORT
==================
Version Change: 0.0.0 → 1.0.0 (initial ratification)
Modified Principles: N/A (initial)
Added Sections: All sections (initial constitution)
Removed Sections: N/A
Templates Requiring Updates:
  - ✅ docs/SPEC.md (created in alignment)
  - ✅ docs/MVP.md (created in alignment)
  - ✅ docs/SECURITY.md (created in alignment)
  - ✅ docs/ARCHITECTURE.md (created in alignment)
  - ✅ docs/API.md (aligned)
  - ✅ docs/OPERATIONS.md (aligned)
  - ✅ docs/CONFIG-SCHEMA.md (aligned)
  - ✅ docs/PACKAGE-MAP.md (aligned)
  - ✅ docs/LIFECYCLE-SCENARIOS.md (aligned)
  - ✅ docs/IMPLEMENTATION-PLAN.md (aligned)
  - ✅ docs/TESTING-STRATEGY.md (aligned)
  - ✅ docs/SDD-GUIDE.md (created in alignment)
Follow-up TODOs:
  - Create in-repo `tasks.yaml` when implementation execution starts
-->

# hush Constitution

## Mission

**hush is a Discord-gated secrets broker for AI agents.**

Every API key, OAuth token, and credential lives encrypted in a single vault file on a
trusted host. Agents fetch secrets only after Z approves the request from his phone via
Discord. Approved secrets are delivered over Tailscale, injected into process memory, and
never written to disk on agent machines.

The threat we are eliminating: **commodity malware that scans dotfiles for secrets.**
`~/.zshrc`, `~/.config/gh/hosts.yml`, `~/.aws/credentials`, `.env` files — these are the
first targets when untrusted code runs on a developer machine. With AI agents executing
LLM-generated scripts and npm/pip packages from across the internet, this attack surface
is unacceptable. hush makes it disappear.

## Core Principles

### I. Zero Files at Rest on Agent Machines

Agent machines MUST have zero secrets on disk. No dotfiles. No `.env`. No keychains.
No tool-specific credential stores (`gh auth login`, `aws configure`, etc.). The only
acceptable form of a secret on an agent machine is an environment variable in a
process started by hush.

**Non-negotiables:**
- The clean machine setup checklist MUST remove all known secret stores from agents
- `gh` CLI MUST work via `GITHUB_TOKEN` env var only — no `gh auth login` on agents
- Tool config files (`~/.aws/credentials`, etc.) MUST NOT exist on agent machines
- Vault host is the ONLY place where any encrypted secret material persists

**Rationale:** This is the entire reason hush exists. If we leave any file-based
secret on an agent machine, commodity malware wins the moment it lands.

### II. Approval is Human, Approval is Phone

Every fresh secret request MUST require explicit approval from Z via Discord DM with
interactive buttons. There is no auto-approve mode. There is no "trusted host" exception.
There is no service account that bypasses approval.

**Non-negotiables:**
- Discord DM MUST display: requester host, requested scopes, session type, TTL/use limit
- Approval MUST be an interactive button click (Approve / Deny / Approve & Mute)
- A denied request MUST be logged and MUST NOT be retried automatically
- Discord bot unavailability MUST return HTTP 503 to the client; the server MUST NOT
  fall back to auto-approve under any circumstances
- Supervisor sessions get ONE approval that covers crashes, updates, and restarts within
  the session TTL — but the original Discord approval is still required

**Rationale:** A human in the loop is the only defense against a compromised agent
silently exfiltrating tokens. The phone is the safest interface — segregated from the
attacked machine, biometric-locked, with rich UI for context.

### III. Defense in Depth Through Crypto Layering

hush stacks seven independent security layers. The compromise of any single layer MUST
NOT enable secret extraction.

**Required layers:**
1. **BIP32 HD key hierarchy** — all signing/encryption keys derived at runtime from a
   passphrase + salt. NO key files on disk.
2. **ES256K JWT signing** — asymmetric session tokens via secp256k1 (go-bitcoin)
3. **ECIES transport encryption** — secret values encrypted end-to-end client→server
4. **ECDSA client request signing** — every client request MUST be signed with a
   registered client key
5. **mlocked secure memory** — sensitive material held in `SecureBytes` (mlock + zero
   on free), heap-copy hazards documented and avoided
6. **Signed audit chain** — append-only audit log with hash-chained ECDSA signatures
7. **Obscurity layers** — random API path prefix, custom vault file format, no advertised
   endpoints — additive only, never load-bearing

**Non-negotiables:**
- No new layer added until existing layers have ≥95% test coverage and fuzz tests
- A future layer MAY be deferred but MAY NOT be silently weakened
- Cryptographic operations MUST use `crypto/rand` for entropy — never `math/rand`
- The vault file format is not a standard — its security depends on Argon2id + AES-GCM,
  not on the file layout being secret

**Rationale:** Bitcoin keys protect billions of dollars by stacking layers. We use the
same primitives for the same reason: any single mistake in our code MUST NOT be enough
to leak a secret.

### IV. Supervisor for Daemons, Wrap-Shell for Humans

Two access patterns, one binary:

**Daemons (OpenClaw, Hermes, future agents):** `hush supervise <name>` runs a state
machine that holds a JWT + ephemeral ECIES key across child crashes/restarts within
a single Discord approval. Daily refresh anchored to waking hours (default 09:00–10:00
local). A 3am crash MUST NOT page Z.

**Humans (Z's interactive sessions):** `hush request --exec "zsh"` wraps the SHELL,
not the app. One approval per day; Claude/cursor/etc. crashes do not trigger re-approval.

**Non-negotiables:**
- Supervisor JWTs MUST carry `session_type: "supervisor"` claim
- Supervisor TTL MUST be capped at `max_supervisor_session_ttl` (default 20h)
- Supervisor sessions MUST NOT be use-count-limited (TTL-only)
- Interactive sessions MUST be use-count-limited (default 50)
- A child exit MUST NOT cause the supervisor to exit; the supervisor MUST hold state
  across the child's lifecycle within the session TTL
- The supervisor MUST zero secret material from its memory after handoff to the child,
  EXCEPT during the optional grace-window cache for restart resilience

**Rationale:** The wrong access pattern is worse than no access pattern. Daemons crashing
at 3am and waking Z is a self-inflicted DoS. Humans being forced to re-approve every
Claude restart trains them to auto-approve, defeating the whole point.

### V. Staleness is Visible, Failure is Loud

Stale credentials MUST be detectable by the validator (before the child sees them),
by the child (exit code 78 = `EX_CONFIG`), and by Z (Discord alerts via watchdog).
Silent stale-credential failures are unacceptable.

**Non-negotiables:**
- Pluggable client-side validators MUST run on the supervisor (not the vault server,
  to keep the vault isolated from outbound internet)
- Validators MUST exist for: anthropic, anthropic-oauth, openai, google-ai, github
- Exit code 78 MUST be the contract between child and supervisor for "my creds are stale"
- A local Unix status socket at `$XDG_RUNTIME_DIR/hush-supervise-{name}.sock` MUST expose
  freshness state to `hush client status`
- Log-pattern auth-failure tailing is alert-only — it MUST NOT control supervisor state
- Vault server unreachability, Discord unavailability, and validator failures MUST all
  produce distinct, actionable alerts in Discord

**Rationale:** The Mini-Zai 2026-04-04 incident — 114 MB of logs in hours from a stale
token — is the canonical failure mode we are designing against. Silent failure is the
worst failure.

### VI. Tailscale-Only, Never Public

The vault server MUST NOT be reachable outside the Tailscale mesh. Ever. There is no
"localhost-only" fallback, no "trusted IP" allowlist, no public TLS endpoint.

**Non-negotiables:**
- Vault server MUST bind to the Tailscale interface only
- Tailscale ACLs MUST restrict port 7743 to `tag:trusted → tag:sandbox` grants
- Startup validation MUST verify the bind address resolves to a Tailscale interface
- TLS within Tailscale is OUT OF SCOPE for v0.1.0 — Tailscale provides transport security
- A future TLS layer MAY be added but MUST NOT relax the Tailscale-only constraint

**Rationale:** Tailscale is our perimeter. A public vault server is an attractive target
that defeats the entire model. Closing this door at the network layer is non-negotiable.

### VII. CLI Design Standards

Commands follow the noun-verb pattern: `hush <noun> <verb> [args] [flags]`.
The binary is small, single-file, with cobra subcommands.

**Subcommands (v0.1.0):**
- `serve` — start the vault server
- `request` — interactive client request (use --exec to wrap shell or app)
- `supervise` — run a child process under supervisor lifecycle
- `init` — interactive bootstrap (passphrase, vault, keychain)
- `secret` — manage secrets in the vault (add/remove/list/rotate)
- `client` — client-side helpers (status, refresh)
- `health` — server health check
- `revoke` — revoke an active token by jti
- `version` — print version + build info

**Non-negotiables:**
- Global flags: `--config/-c`, `--verbose/-v`, `--quiet/-q`, `--no-color`
- Output: text for TTY, JSON for pipes/redirects (auto-detect)
- Exit codes: 0 success, 1 error, 2 input error, 3 auth error, 4 not found, 5 permission,
  78 (`EX_CONFIG`) stale credentials (child→supervisor contract)
- `--format eval` MUST require explicit flag and emit a stderr warning (it prints export
  statements that bypass process injection)

**Rationale:** Predictable CLI design enables scripting, integrates with launchd/systemd,
and reduces the chance of misuse via wrong invocation.

### VIII. Testing Discipline

Security-critical code requires 100% test coverage. Fuzz testing is mandatory for all
parsers and crypto entry points.

**Non-negotiables:**
- Table-driven unit tests for all core functions
- Fuzz tests for: vault decrypt, ECIES decrypt, JWT validate, request signature verify,
  TOML config parse
- Integration tests gated by `//go:build integration`
- Pre-commit MUST run `golangci-lint` + `go test -race`
- 90%+ overall coverage required before v0.1.0 tag

**Test Priority:**
| Priority | Scope | Coverage |
|----------|-------|----------|
| Critical | Vault crypto, key derivation, JWT, ECIES, request signing | 100% |
| High | Server handlers, supervisor state machine, validators | 95% |
| Medium | Discord bot logic, CLI flags, config parsing | 85% |
| Low | Help text, log formatting | 70% |

**Rationale:** Bugs in a secrets broker leak secrets. Testing is not optional.

## Security Requirements

These constraints apply to ALL code in the repository:

| Requirement | Implementation |
|-------------|----------------|
| Encrypted at rest | Argon2id (time=4, memory=256MB, threads=4) + AES-256-GCM |
| Memory protection | mlock for sensitive data, explicit zeroing, `[]byte`-only for keys |
| Input validation | All external input validated before use; nonce + timestamp on signed requests |
| No hardcoded secrets | Passphrase from macOS Keychain via stdin pipe (never env var or plist) |
| Secure defaults | Fail closed; explicit flags for `--format eval` and similar dangerous modes |
| Replay protection | Nonce + timestamp on every signed request; nonce cache server-side |
| Token revocation | `/revoke` endpoint; jti tracked in active session map |
| Audit log | Append-only, hash-chained, ECDSA-signed; rotation strategy documented |
| File permissions | Vault: 0600. Supervisor sockets: 0600. Configs: 0640. Dirs: 0750. |
| Clock sync | Startup check against NTP; refuse to start if drift >60s |

**Keychain ACLs (macOS):** The passphrase entry MUST restrict access to the `hush`
binary path only. Wildcard ACLs are forbidden.

**Reload semantics:** SIGHUP triggers atomic vault reload via `atomic.Pointer[Vault]`.
In-flight requests complete with the old vault; new requests use the new vault.

## Development Workflow

### Code Quality Gates

All code MUST pass before merge:

1. **Linting:** `magex lint` (golangci-lint with project config)
2. **Format:** `magex format:fix` (gofmt + goimports + go-broadcast formatting)
3. **Tests:** `magex test:race` (race detector enabled)
4. **Pre-commit:** `go-pre-commit` (gitleaks must be zero-finding)
5. **Build:** Clean build via `magex build`

### Commit Standards

- Commits MUST be atomic (one logical change per commit)
- Commit messages follow conventional commits: `type(scope): description`
- Types: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `security`
- Security-sensitive changes MUST be tagged `security`

### Review Requirements

- All cryptographic code requires explicit security-focused review
- Changes to key derivation, signing, encryption, or session handling require
  security-aware review
- New dependencies require justification and basic supply-chain audit
- The supervisor state machine and Discord bot interaction logic require integration
  test coverage of all 11 lifecycle scenarios documented in `docs/SPEC.md` AC-10

## Governance

This constitution supersedes all other development practices for the hush project.
Amendments require:

1. Written proposal with rationale (PR description)
2. Impact analysis on existing code and downstream daemons (OpenClaw, Hermes)
3. Version increment per semantic versioning:
   - **MAJOR:** Principle removal or incompatible redefinition
   - **MINOR:** New principle or materially expanded guidance
   - **PATCH:** Clarifications, wording, non-semantic refinements
4. Update to all dependent documentation (SPEC.md, MVP.md, SECURITY.md, ARCHITECTURE.md)

**Compliance:** All PRs and reviews MUST verify adherence to these principles.
Deviations MUST be explicitly justified in the Complexity Tracking section of
implementation plans.

**Public release:** The repository starts PRIVATE. Z transitions it to public only
after:
- All v0.1.0 acceptance criteria pass
- 90%+ test coverage achieved
- `magex format:fix && magex lint && magex test:race` all green
- `go-pre-commit` zero gitleaks findings
- README, ARCHITECTURE, SECURITY docs polished

**Version:** 1.0.0 | **Ratified:** 2026-04-26 | **Last Amended:** 2026-04-26
