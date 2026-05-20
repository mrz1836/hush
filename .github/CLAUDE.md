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
  packages — see [../docs/ARCHITECTURE.md](../docs/ARCHITECTURE.md).
- **State:** working toward the v0.1.0 release tag.

## Build, lint, test — always via `magex`

This repo uses MAGE-X, not raw `go` invocations. Full reference:
[tech-conventions/mage-x.md](tech-conventions/mage-x.md). Daily commands:

| Command                           | When to use                                                |
| --------------------------------- | ---------------------------------------------------------- |
| `magex format:fix`                | Before every commit                                        |
| `magex lint`                      | All 60+ linters (`magex lint:fix` to auto-fix)             |
| `magex vet` / `magex staticcheck` | Static analysis                                            |
| `magex test`                      | Fast unit tests (default development loop)                 |
| `magex test:race`                 | Race detector — required for goroutine-touching changes    |
| `magex test:coverrace`            | Full CI suite + coverage — gate before PR                  |
| `magex test:fuzz time=30s`        | Fuzz targets — required where a parser/crypto entry changes |
| `magex deps:audit`                | govulncheck + scanners — required for security gates       |
| `magex build`                     | Build the `hush` binary                                    |

Coverage targets: ≥ 90% project-wide; **100% on security-critical packages**
(`internal/keys`, `internal/vault`, `internal/token`, `internal/transport`).

## Don't

- Don't run raw `go test` / `go build` / `golangci-lint` directly. Use `magex`.
- Don't tag releases. CI does it from `magex version:bump bump=… push`.
- Don't write key material to disk. Ever. (Constitution principle.)
- Don't use `--no-verify` on commits.
- Don't skip the speckit pipeline for "small" chunks — the artifacts are the audit trail.
- Don't author `tech-conventions/` content into this file. Link to it.
