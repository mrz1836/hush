# SDD-PLAYBOOK — chunk index and progress dashboard

> One-line per chunk. Concise overview of the 32 SDD chunks that make up the
> v0.1.0 implementation. The full agent prompts live in [`docs/SDD-CATALOG.md`](SDD-CATALOG.md).
> The constitutional acceptance criteria (AC-1..AC-10) for each chunk live in
> [`docs/AC-MATRIX.md`](AC-MATRIX.md).

---

## How to use this file

- This is the at-a-glance progress dashboard.
- For the full prompt and contract of each chunk, open `docs/SDD-CATALOG.md`
  and search for `### SDD-NN`.
- For the AC ↔ chunk ↔ test mapping, open `docs/AC-MATRIX.md`.
- Status column values: `pending`, `in-progress`, `done`, `skipped`. Update
  this file when a chunk's status changes; commit the change with the chunk's
  PR.

---

## Phase 1 — Cryptographic and storage core

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-01 | Argon2id + BIP32 HD derivation | `internal/keys` | pending | AC-7 |
| SDD-02 | mlocked secure memory + redaction | `internal/vault/securebytes` | pending | AC-7 |
| SDD-03 | HUSH file format + AES-256-GCM + atomic write | `internal/vault` | pending | AC-2 |
| SDD-04 | Test fixtures + sentinel helpers + harness primitives | `internal/testutil` | pending | (AC-9 support) |
| SDD-05 | slog setup + redaction enforcement | `internal/logging` | pending | (Principle X) |
| SDD-06 | Server TOML schema + validation | `internal/config` | pending | AC-1, AC-8 |

## Phase 2 — Session and transport core

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-07 | ES256K JWT issue/validate + claims + store | `internal/token` | pending | AC-4 |
| SDD-08 | ECDSA canonical-JSON request signing + nonce + timestamp | `internal/transport/sign` | pending | AC-7 |
| SDD-09 | ECIES encrypt/decrypt for secret responses | `internal/transport/ecies` | pending | AC-7 |

## Phase 3 — Server control plane

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-10 | HTTP server + middleware + startup checks + SIGHUP reload | `internal/server` | pending | AC-1, AC-2, AC-8 |
| SDD-11 | Approver interface + Discord bot + disconnect monitor | `internal/discord` | pending | AC-3 |
| SDD-12 | `/claim` handler (signed verify + Discord approval + JWT issue) | `internal/server` | pending | AC-1, AC-3, AC-4 |
| SDD-13 | `/s/<name>`, `/revoke/<jti>`, `/hz` handlers + audit log | `internal/server` + `internal/audit` | pending | AC-1, AC-2, AC-4, AC-7 |
| SDD-14 | cmd/hush + cli root + serve/health/version/revoke | `cmd/hush` + `internal/cli` | pending | AC-1 |

## Phase 4 — Interactive CLI path

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-15 | `hush init` (server + client modes; Keychain ACL) | `internal/cli` + `internal/keychain` | pending | AC-1, AC-6 |
| SDD-16 | `hush request` (interactive; ECIES decrypt; --exec injection) | `internal/cli` | pending | AC-5, AC-6 |
| SDD-17 | `hush secret` add/remove/list/rotate (TTY-only) | `internal/cli` | pending | AC-1, AC-2 |

## Phase 5 — Supervisor lifecycle

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-18 | Supervisor TOML schema + validation | `internal/supervise/config` | pending | AC-10 |
| SDD-19 | Supervisor state machine + transitions + store | `internal/supervise` | pending | AC-10 |
| SDD-20 | Child fork/exec + signals + exit-78 + process-group death-watch | `internal/supervise` | pending | AC-10 |
| SDD-21 | Refill + refresh + grace cache | `internal/supervise` | pending | AC-10 |
| SDD-22 | PID file + flock + Unix status socket | `internal/supervise` | pending | AC-10 |
| SDD-23 | `hush supervise` + `hush client status` + `hush client refresh` | `internal/cli` | pending | AC-10 |
| SDD-24 | (reserved — orchestration glue if SDD-25 surfaces gaps; default skipped) | — | skipped | — |
| SDD-25 | Lifecycle integration harness (15 scenarios — explicit AC-10 owner) | `tests/integration/` | pending | AC-9, AC-10 |

## Phase 6 — Validators + alerts

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-26 | 5 builtin validators (anthropic, anthropic-oauth, openai, google-ai, github) | `internal/supervise/validators` | pending | AC-10 |
| SDD-27 | Log-pattern watchdog (alert-only) | `internal/supervise` | pending | AC-10 |
| SDD-28 | 8 alert classes + tiered routing + DM rate limit | `internal/discord/alerts` | pending | AC-3, AC-10 |

## Phase 7 — Deployment

| ID | Title | Files | Status | AC |
|----|-------|-------|--------|-----|
| SDD-29 | Deploy artifacts (launchd plist, systemd unit, install.sh, generic launcher template) | `deploy/*` | pending | AC-1, AC-6, AC-10 |
| SDD-30 | Generic example supervisor TOML + Tailscale ACL spec + clean-machine checklist | `deploy/examples/*` + `docs/TAILSCALE-ACLS.md` + `docs/CLEAN-MACHINE.md` | partial (docs done; example TOML pending SDD-18) | AC-6, AC-8, AC-10 |

## Phase 8 — Release

| ID | Title | Files | Status | AC |
|----|-------|-------|--------|-----|
| SDD-31 | Release gates (coverage + 6 fuzz + magex + go-pre-commit + govulncheck + gitleaks + CGO=0 + no /vendor) | `.github/workflows/*` | pending | AC-9 |
| SDD-32 | OSS-grade README + DAEMONS.md + repo-level OSS files + docs polish + GoReleaser + v0.1.0 tag | `README.md` + repo root + `docs/*` + `.goreleaser.yml` | partial (DAEMONS.md done; README + supporting files done; tag pending SDD-31) | AC-1 |

---

## Workflow

For each chunk, in dependency order from `docs/SDD-CATALOG.md`:

1. Open `docs/SDD-CATALOG.md`, find `### SDD-NN`.
2. Copy the **Agent Prompt** block at the bottom of that chunk.
3. Open a fresh Claude Code session in `/Users/mrz/projects/hush/`.
4. Paste the prompt verbatim.
5. The agent runs `/speckit-specify` → `/speckit-plan` → `/speckit-tasks`,
   then implements TDD-style, then runs gates (`magex format:fix`,
   `magex lint`, `magex test:race`, fuzz where applicable).
6. The agent updates this file (`SDD-PLAYBOOK.md`) — mark the chunk done.
7. The agent updates `docs/AC-MATRIX.md` with the test paths it produced.
8. Open a PR. Reviewer verifies the AC-MATRIX rows + gates.

---

## Cross-references

- Full chunk prompts & contracts: [`docs/SDD-CATALOG.md`](SDD-CATALOG.md)
- AC ↔ chunk ↔ test mapping: [`docs/AC-MATRIX.md`](AC-MATRIX.md)
- Phase rationale: [`docs/IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md)
- Package responsibilities: [`docs/PACKAGE-MAP.md`](PACKAGE-MAP.md)
- Constitutional principles: [`.specify/memory/constitution.md`](../.specify/memory/constitution.md)
