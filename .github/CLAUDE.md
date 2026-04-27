# CLAUDE.md — hush agent guide

> **Read first:** [AGENTS.md](AGENTS.md) is the root authority for coding,
> commit, PR, release, and CI rules. [tech-conventions/](tech-conventions/)
> has the deep guides. [../.specify/memory/constitution.md](../.specify/memory/constitution.md)
> defines the 11 non-negotiable principles. If anything below conflicts with
> those, **they win.**

## What this project is

`hush` is a Discord-gated secrets broker for AI agents. Single static Go
binary (`hush serve` / `hush request` / `hush supervise`). Encrypted vault
on a single trusted host; ES256K JWT sessions over Tailscale; ECIES-encrypted
delivery; Discord DM approval; nothing on disk on agent machines.

- **Module:** `github.com/mrz1836/hush` — Go 1.26.1, CGO=0, no `/vendor`.
- **Layout:** [../cmd/hush/](../cmd/hush/) entrypoint; [../internal/](../internal/)
  packages per [../docs/PACKAGE-MAP.md](../docs/PACKAGE-MAP.md).
- **State:** greenfield. Phase 0 docs locked; SDD-01..32 implementation
  pending — track in [../docs/SDD-PLAYBOOK.md](../docs/SDD-PLAYBOOK.md).

## Build, lint, test — always via `magex`

This repo uses MAGE-X, not raw `go` invocations. Full reference:
[tech-conventions/mage-x.md](tech-conventions/mage-x.md). Daily commands:

| Command                           | When to use                                                |
| --------------------------------- | ---------------------------------------------------------- |
| `magex format:fix`                | Before every commit                                        |
| `magex lint`                      | All 60+ linters (`magex lint:fix` to auto-fix)             |
| `magex vet` / `magex staticcheck` | Static analysis                                            |
| `magex test`                      | Fast unit tests (default development loop)                 |
| `magex test:race`                 | Race detector — required for goroutine-touching SDDs       |
| `magex test:coverrace`            | Full CI suite + coverage — gate before PR                  |
| `magex test:fuzz time=30s`        | Fuzz targets — required where the SDD lists one            |
| `magex deps:audit`                | govulncheck + scanners — required for security gates       |
| `magex build`                     | Build the `hush` binary                                    |

Coverage targets: ≥ 90% project-wide; **100% on security-critical packages**
(`internal/keys`, `internal/vault`, `internal/token`, `internal/transport`).

## How to run an SDD chunk

Implementation is broken into ~31 chunks (SDD-01..SDD-32, SDD-24 reserved)
across 8 phases. **Run one chunk per fresh Claude session** — keeps each
session under the compaction threshold and each PR atomic.

1. **Pick the next chunk.** Open [../docs/SDD-PLAYBOOK.md](../docs/SDD-PLAYBOOK.md);
   choose the lowest-numbered chunk with status `pending` whose `Blocked by`
   chunks are all `done`.
2. **Read the chunk's full entry** in [../docs/SDD-CATALOG.md](../docs/SDD-CATALOG.md).
   Note its package, files, blockers, behaviour contracts, anti-contracts,
   test list, and coverage target.
3. **Pre-read** [../.specify/memory/constitution.md](../.specify/memory/constitution.md),
   [../docs/SPEC.md](../docs/SPEC.md), [../docs/PACKAGE-MAP.md](../docs/PACKAGE-MAP.md),
   and the chunk's rows in [../docs/AC-MATRIX.md](../docs/AC-MATRIX.md).
4. **Run the speckit pipeline** with the chunk's agent prompt as input:
   `/speckit-specify` → `/speckit-clarify` (only if under-specified) →
   `/speckit-plan` → `/speckit-tasks` → `/speckit-analyze` → `/speckit-implement`.
5. **TDD.** Tests first, including the fuzz target if listed. Use real
   dependencies in integration tests (no mocks for crypto, vault, transport).
6. **Gates before PR:** `magex format:fix && magex lint && magex test:coverrace`
   must all pass. `magex deps:audit` for security-critical chunks.
7. **Update tracking docs manually** (speckit doesn't):
   [../docs/SDD-PLAYBOOK.md](../docs/SDD-PLAYBOOK.md) status → `done`,
   [../docs/AC-MATRIX.md](../docs/AC-MATRIX.md) row → green with test path.
8. **Branch & commit per** [tech-conventions/commit-branch-conventions.md](tech-conventions/commit-branch-conventions.md).
   PR per [tech-conventions/pull-request-guidelines.md](tech-conventions/pull-request-guidelines.md).

If a chunk feels too large or you find yourself needing to compact, **stop and
split** before continuing — chunk granularity is supposed to fit one session.

## Don't

- Don't run raw `go test` / `go build` / `golangci-lint` directly. Use `magex`.
- Don't tag releases. CI does it from `magex version:bump bump=… push`.
- Don't write key material to disk. Ever. (Constitution principle.)
- Don't use `--no-verify` on commits.
- Don't skip the speckit pipeline for "small" chunks — the artifacts are the audit trail.
- Don't author `tech-conventions/` content into this file. Link to it.
