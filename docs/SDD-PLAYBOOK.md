# SDD-PLAYBOOK — chunk index and progress dashboard

> One-line per chunk. Concise overview of the 32 SDD chunks that make up the
> v0.1.0 implementation. The full agent prompts live in [`docs/SDD-CATALOG.md`](SDD-CATALOG.md).
> The constitutional acceptance criteria (AC-1..AC-10) for each chunk live in
> [`docs/AC-MATRIX.md`](AC-MATRIX.md).

---

## How to use this file

- This is the at-a-glance progress dashboard.
- For the full contract + the 5-prompt session set of each chunk,
  open `docs/sdd/SDD-NN.md` (linked from
  [`docs/SDD-CATALOG.md`](SDD-CATALOG.md)).
- For the AC ↔ chunk ↔ test mapping, open `docs/AC-MATRIX.md`.
- Status column values: `pending`, `in-progress`, `done`, `skipped`. Update
  this file when a chunk's status changes; commit the change with the chunk's
  PR.

---

> Tables are in **execution order** (earliest-unblocked first), not numeric ID order.
> See [`docs/SDD-CATALOG.md`](SDD-CATALOG.md) for the `Blocked by` column.

## Phase 1 — Cryptographic and storage core

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-01 | Argon2id + BIP32 HD derivation | `internal/keys` | done | AC-7 |
| SDD-02 | mlocked secure memory + redaction | `internal/vault/securebytes` | done | AC-7 |
| SDD-03 | HUSH file format + AES-256-GCM + atomic write | `internal/vault` | done | AC-2 |
| SDD-05 | slog setup + redaction enforcement | `internal/logging` | done | (Principle X) |
| SDD-04 | Test fixtures + sentinel helpers + harness primitives | `internal/testutil` | done | (AC-9 support) |
| SDD-06 | Server TOML schema + validation | `internal/config` | done | AC-1, AC-8 |

## Phase 2 — Session and transport core

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-08 | ECDSA canonical-JSON request signing + nonce + timestamp | `internal/transport/sign` | done | AC-7 |
| SDD-09 | ECIES encrypt/decrypt for secret responses | `internal/transport/ecies` | done | AC-7 |
| SDD-07 | ES256K JWT issue/validate + claims + store | `internal/token` | done | AC-4 |

## Phase 3 — Server control plane

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-10 | HTTP server + middleware + startup checks + SIGHUP reload | `internal/server` | done | AC-1, AC-2, AC-8 |
| SDD-11 | Approver interface + Discord bot + disconnect monitor | `internal/discord` | done | AC-3 |
| SDD-12 | `/claim` handler (signed verify + Discord approval + JWT issue) | `internal/server` | done | AC-1, AC-3, AC-4 |
| SDD-13 | `/s/<name>`, `/revoke/<jti>`, `/hz` handlers + audit log | `internal/server` + `internal/audit` | done | AC-1, AC-2, AC-4, AC-7 |
| SDD-14 | cmd/hush + cli root + serve/health/version/revoke | `cmd/hush` + `internal/cli` | done | AC-1 |

## Phase 4 — Interactive CLI path

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-15 | `hush init` (server + client modes; Keychain ACL) | `internal/cli` + `internal/keychain` | done | AC-1, AC-6 |
| SDD-16 | `hush request` (interactive; ECIES decrypt; --exec injection) | `internal/cli` | done | AC-5, AC-6 |
| SDD-17 | `hush secret` add/remove/list/rotate (TTY-only) | `internal/cli` | done | AC-1, AC-2 |

## Phase 5 — Supervisor lifecycle

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-18 | Supervisor TOML schema + validation | `internal/supervise/config` | done | AC-10 |
| SDD-19 | Supervisor state machine + transitions + store | `internal/supervise` | done | AC-10 |
| SDD-20 | Child fork/exec + signals + exit-78 + process-group death-watch | `internal/supervise` | done | AC-10 |
| SDD-21 | Refill + refresh + grace cache | `internal/supervise` | done | AC-10 |
| SDD-22 | PID file + flock + Unix status socket | `internal/supervise` | done | AC-10 |
| SDD-23 | `hush supervise` + `hush client status` + `hush client refresh` | `internal/cli` | done | AC-10 |
| SDD-24 | Supervisor orchestration glue (activated 2026-05-12 by SDD-25 — see `docs/sdd/SDD-24.md`) | `internal/supervise/` (`lifecycle.go` + 4 siblings — Plan Option C) | done | AC-10 |
| SDD-25 | Lifecycle integration harness (15 scenarios — explicit AC-10 owner) | `tests/integration/` | in-progress (chunk 1: harness scaffolding + Scenario 14 green under `-race`, 5/5 flake-free; remaining 16 scenarios fail loudly per FR-001 until upstream wiring lands in subsequent chunks) | AC-9, AC-10 |

## Phase 6 — Validators + alerts

| ID | Title | Package | Status | AC |
|----|-------|---------|--------|-----|
| SDD-27 | Log-pattern watchdog (alert-only) | `internal/supervise/watchdog` | done | AC-10 |
| SDD-26 | 5 builtin validators (anthropic, anthropic-oauth, openai, google-ai, github) | `internal/supervise/validators` | done | AC-10 |
| SDD-28 | 8 alert classes + tiered routing + DM rate limit | `internal/discord/alerts` | done | AC-3, AC-10 |

## Phase 7 — Deployment

| ID | Title | Files | Status | AC |
|----|-------|-------|--------|-----|
| SDD-29 | Deploy artifacts (launchd plist, systemd unit, install.sh, generic launcher template) | `deploy/*` | done | AC-1, AC-6, AC-10 |
| SDD-30 | Generic example supervisor TOML + Tailscale ACL spec + clean-machine checklist | `deploy/examples/*` + `docs/TAILSCALE-ACLS.md` + `docs/CLEAN-MACHINE.md` | partial (docs done; example TOML pending SDD-18) | AC-6, AC-8, AC-10 |

## Phase 8 — Release

> Phase 8 ordering: SDD-31 (CI gates) → SDD-33 (final overhaul) → SDD-32 (release tag).
> SDD-32 is explicitly blocked on SDD-33 so the v0.1.0 tag captures the overhauled state.

| ID | Title | Files | Status | AC |
|----|-------|-------|--------|-----|
| SDD-31 | Release gates (coverage + 6 fuzz + magex + go-pre-commit + govulncheck + gitleaks + CGO=0 + no /vendor) | `.github/workflows/*` | code-complete (gates green locally; PR #38 awaits Actions-enable + green checks) | AC-9 |
| SDD-33 | Final repo + docs overhaul (drift reconciliation, dead-code sweep, README rewrite) | repo-wide | pending | AC-1 (+ tightens every other AC) |
| SDD-32 | OSS-grade README + DAEMONS.md + repo-level OSS files + docs polish + GoReleaser + v0.1.0 tag | `README.md` + repo root + `docs/*` + `.goreleaser.yml` | partial (DAEMONS.md done; README + supporting files done; tag pending SDD-31 + SDD-33) | AC-1 |

---

## Workflow

For each chunk, in dependency order:

1. Open `docs/sdd/SDD-NN.md`.
2. Open a fresh Claude Code session in `/Users/mrz/projects/hush/`.
   Paste **Prompt 1 (Specify)** verbatim. Let the session finish and
   close it.
3. Open ANOTHER fresh Claude Code session. Paste **Prompt 2
   (Clarify)**. Close it.
4. Repeat for **Prompt 3 (Plan)**, **Prompt 4 (Tasks)**, **Prompt 5
   (Implement)** — one fresh session each.
5. Prompt 5 updates this file (mark the chunk `done`), updates
   `docs/AC-MATRIX.md`, and makes one combined commit covering code
   + doc updates.
6. Open a PR. Reviewer verifies the AC-MATRIX rows + gates.

**Why 5 sessions instead of 1:** each speckit phase produces a
substantial artifact (`spec.md`, `plan.md`, `tasks.md`). Chaining
them in one session guarantees compaction on larger chunks (SDD-13,
SDD-20, SDD-25, SDD-33), and post-compaction code reliably drifts
from the chunk's locked contracts. Speckit persists every artifact
to disk; fresh sessions reload context from disk without losing
fidelity.

The `extensions.yml` git hooks auto-commit each artifact. Accept
those in Prompts 1, 3, 4. In Prompt 2 accept only if `spec.md`
changed. **Decline** the `after_implement` auto-commit in Prompt 5
— that prompt makes one combined commit covering code + doc updates.

See [`docs/sdd/SDD-01.md`](sdd/SDD-01.md) for the canonical
template each chunk file follows.

---

## Cross-references

- Chunk index + cross-cutting requirements: [`docs/SDD-CATALOG.md`](SDD-CATALOG.md)
- Per-chunk contracts + 5-prompt sets: [`docs/sdd/`](sdd/)
- AC ↔ chunk ↔ test mapping: [`docs/AC-MATRIX.md`](AC-MATRIX.md)
- Phase rationale: [`docs/IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md)
- Package responsibilities: [`docs/PACKAGE-MAP.md`](PACKAGE-MAP.md)
- Constitutional principles: [`.specify/memory/constitution.md`](../.specify/memory/constitution.md)
