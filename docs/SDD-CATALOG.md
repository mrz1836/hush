# SDD-CATALOG — chunk index + cross-cutting requirements

> Index of every SDD chunk that builds hush v0.1.0. Each chunk lives
> in its own file under [`docs/sdd/`](sdd/) and contains the full
> chunk contract plus **5 self-contained prompts** that drive the
> spec-kit cycle in 5 fresh Claude Code sessions.
>
> Companion files:
> - [`docs/SDD-PLAYBOOK.md`](SDD-PLAYBOOK.md) — at-a-glance status dashboard
> - [`docs/AC-MATRIX.md`](AC-MATRIX.md) — AC ↔ chunk ↔ test path mapping
> - [`docs/IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md) — phase rationale + dependency direction
> - [`.specify/memory/constitution.md`](../.specify/memory/constitution.md) — non-negotiable principles

---

## How to use this catalog

Each chunk is run as **5 separate Claude Code sessions**, one per
spec-kit phase. The artifacts (`spec.md`, `plan.md`, `tasks.md`)
persist to disk in the per-feature directory under `specs/`, and
`.specify/feature.json` records the directory so subsequent sessions
locate it without depending on conversation memory.

Workflow per chunk:

1. Pick a chunk from [`docs/SDD-PLAYBOOK.md`](SDD-PLAYBOOK.md) whose
   blockers are all `done`.
2. Open the chunk's file at `docs/sdd/SDD-NN.md`.
3. Open a fresh Claude Code session in `/Users/mrz/projects/hush/`
   and paste **Prompt 1 (Specify)** from the chunk file. Let the
   session finish and close it.
4. Open ANOTHER fresh Claude Code session and paste **Prompt 2
   (Clarify)**. Close it.
5. Repeat for **Prompt 3 (Plan)**, **Prompt 4 (Tasks)**, **Prompt 5
   (Implement)** — one fresh session each.
6. Prompt 5 updates `docs/AC-MATRIX.md` and `docs/SDD-PLAYBOOK.md`,
   then makes one combined commit. Open a PR; reviewer verifies
   AC-MATRIX rows + gates.

Why 5 sessions instead of 1: each spec-kit phase produces a
substantial artifact. Chaining them in one session guarantees
context compaction on larger chunks (SDD-13, SDD-20, SDD-25, SDD-33),
and post-compaction code reliably drifts from the chunk's locked
contracts. Speckit persists every artifact to disk; fresh sessions
reload context from disk without losing fidelity. See
[docs/sdd/SDD-01.md](sdd/SDD-01.md) for the canonical example.

---

## Cross-cutting requirements (apply to every chunk)

- **Phase-1/2 chunks freeze public API:** Every chunk in Phase 1
  (SDD-01..06) and Phase 2 (SDD-07..09) ends with appending an
  `Exported API — locked at SDD-NN` section to
  [`docs/PACKAGE-MAP.md`](PACKAGE-MAP.md). Consumers in Phase 3+
  only reference the locked section.
- **Sentinel-leak tests:** Wherever a chunk handles a secret value,
  it MUST inject `SECRET_SHOULD_NEVER_APPEAR_<chunk_id>` and assert
  that the sentinel is absent from logs, errors, and any HTTP
  response. Use the helper from `internal/testutil` (SDD-04).
- **Constitutional principles:** Every chunk audits itself against
  the principles in `.specify/memory/constitution.md` it touches,
  listed explicitly in each chunk's file.
- **TDD:** Tests are written first per `tasks.md` from
  `/speckit-tasks`. Note: `/speckit-tasks` defaults to NO test tasks
  unless explicitly told to use TDD; every chunk's Prompt 4 passes
  the TDD signal as the command argument.
- **Gates:** Every chunk's gate before merge: `magex format:fix &&
  magex lint && magex test:race`. Fuzz chunks add `go test
  -fuzz=Fuzz<Name> -fuzztime=60s ./...`. Sentinel-leak chunks
  assert the sentinel is absent.
- **Auto-commit hooks:** `.specify/extensions.yml` wires
  `before_specify` (creates the feature branch) and `after_*`
  (offers to auto-commit each artifact). Every chunk's prompts
  give explicit accept/decline guidance per phase.

---

## 5-prompt template (the shape every chunk file follows)

Each `docs/sdd/SDD-NN.md` follows this layout:

```
# SDD-NN — <Title>

**Phase / Package / Files / Branch / Blocked-by / Blocks /
 Primary AC / Coverage target**
**Behaviour contracts (MUST)**
**Anti-contracts (MUST NOT)**
**Tests required**
**Constitutional principles in scope**
**Exported API to lock in PACKAGE-MAP.md**

## How to run this chunk
[brief preamble: 5 sessions, hook accept/decline guidance]

## Prompt 1 — Specify  (fresh session)
[VERBOSE — locks WHAT-level acceptance criteria; specify drift
 propagates everywhere downstream]

## Prompt 2 — Clarify  (fresh session)
[LEAN — pointer to chunk doc + run command + accept-if-changed]

## Prompt 3 — Plan  (fresh session)
[VERBOSE — locks HOW: scope, files, exported API, deps allow-list,
 anti-imports; Constitution Check gate runs here]

## Prompt 4 — Tasks  (fresh session)
[LEAN — explicit TDD argument to /speckit-tasks (its default is
 no tests), chunk-specific test names + gate commands]

## Prompt 5 — Implement  (fresh session)
[LEAN — chunk doc pointer + /speckit-implement + post-impl
 workflow (gates, fuzz, PACKAGE-MAP/AC-MATRIX/PLAYBOOK updates,
 ONE combined commit; declines after_implement auto-commit)]
```

Specify and Plan are verbose because they lock the WHAT and HOW
respectively, and downstream phases inherit any drift. Clarify,
Tasks, and Implement are lean because they read the already-locked
artifacts off disk.

---

## Chunk index

> **Reading the tables:** rows are listed in **execution order** (earliest-
> unblocked first), NOT numeric ID order. The ordering rule is: sort
> by the chunk's latest blocker (ascending), tiebreak by ID. The
> `Blocked by` column is the source of truth — if a row's blockers
> aren't all `done` in [`docs/SDD-PLAYBOOK.md`](SDD-PLAYBOOK.md), don't
> start it. Within the same row of blockers, multiple chunks can run
> in parallel.

### Phase 1 — Cryptographic and storage core

| ID | Title | Package | Blocked by | Chunk file |
|----|-------|---------|------------|------------|
| SDD-01 | Argon2id + BIP32 HD derivation | `internal/keys` | — | [docs/sdd/SDD-01.md](sdd/SDD-01.md) |
| SDD-02 | mlocked secure memory + redaction | `internal/vault/securebytes` | — (parallel-safe with SDD-01) | [docs/sdd/SDD-02.md](sdd/SDD-02.md) |
| SDD-03 | HUSH file format + AES-256-GCM + atomic write | `internal/vault` | SDD-01, SDD-02 | [docs/sdd/SDD-03.md](sdd/SDD-03.md) |
| SDD-05 | slog setup + redaction enforcement | `internal/logging` | SDD-02 | [docs/sdd/SDD-05.md](sdd/SDD-05.md) |
| SDD-04 | Test fixtures + sentinel helpers + harness primitives | `internal/testutil` | SDD-01, SDD-02, SDD-03 | [docs/sdd/SDD-04.md](sdd/SDD-04.md) |
| SDD-06 | Server TOML schema + validation | `internal/config` | SDD-05 | [docs/sdd/SDD-06.md](sdd/SDD-06.md) |

### Phase 2 — Session and transport core

| ID | Title | Package | Blocked by | Chunk file |
|----|-------|---------|------------|------------|
| SDD-08 | ECDSA canonical-JSON request signing + nonce + timestamp | `internal/transport/sign` | SDD-01 | [docs/sdd/SDD-08.md](sdd/SDD-08.md) |
| SDD-09 | ECIES encrypt/decrypt for secret responses | `internal/transport/ecies` | SDD-02 | [docs/sdd/SDD-09.md](sdd/SDD-09.md) |
| SDD-07 | ES256K JWT issue/validate + claims + store | `internal/token` | SDD-01, SDD-02, SDD-06 | [docs/sdd/SDD-07.md](sdd/SDD-07.md) |

### Phase 3 — Server control plane

| ID | Title | Package | Blocked by | Chunk file |
|----|-------|---------|------------|------------|
| SDD-10 | HTTP server + middleware + startup checks + SIGHUP reload | `internal/server` | SDD-03, SDD-05, SDD-06, SDD-07, SDD-08, SDD-09 | [docs/sdd/SDD-10.md](sdd/SDD-10.md) |
| SDD-11 | Approver interface + Discord bot + disconnect monitor | `internal/discord` | SDD-05, SDD-06, SDD-10 | [docs/sdd/SDD-11.md](sdd/SDD-11.md) |
| SDD-12 | `/claim` handler (signed verify + Discord approval + JWT issue) | `internal/server` | SDD-07, SDD-08, SDD-10, SDD-11 | [docs/sdd/SDD-12.md](sdd/SDD-12.md) |
| SDD-13 | `/s/<name>`, `/revoke/<jti>`, `/hz` handlers + audit log | `internal/server` + `internal/audit` | SDD-09, SDD-12 | [docs/sdd/SDD-13.md](sdd/SDD-13.md) |
| SDD-14 | cmd/hush + cli root + serve/health/version/revoke | `cmd/hush` + `internal/cli` | SDD-10, SDD-11, SDD-12, SDD-13 | [docs/sdd/SDD-14.md](sdd/SDD-14.md) |

### Phase 4 — Interactive CLI path

| ID | Title | Package | Blocked by | Chunk file |
|----|-------|---------|------------|------------|
| SDD-15 | `hush init` (server + client modes; Keychain ACL) | `internal/cli` + `internal/keychain` | SDD-01, SDD-03, SDD-14 | [docs/sdd/SDD-15.md](sdd/SDD-15.md) — see [internal/cli/init.go](../internal/cli/init.go) and [internal/keychain/keychain.go](../internal/keychain/keychain.go) |
| SDD-16 | `hush request` (interactive; ECIES decrypt; --exec injection) | `internal/cli` | SDD-08, SDD-09, SDD-13, SDD-15 | [docs/sdd/SDD-16.md](sdd/SDD-16.md) — see [internal/cli/request.go](../internal/cli/request.go) and [internal/cli/exec.go](../internal/cli/exec.go) |
| SDD-17 | `hush secret` add/remove/list/rotate (TTY-only) | `internal/cli` | SDD-03, SDD-15 | [docs/sdd/SDD-17.md](sdd/SDD-17.md) |

### Phase 5 — Supervisor lifecycle

| ID | Title | Package | Blocked by | Chunk file |
|----|-------|---------|------------|------------|
| ✅ SDD-18 | Supervisor TOML schema + validation | `internal/supervise/config` | SDD-06 | [docs/sdd/SDD-18.md](sdd/SDD-18.md) |
| ✅ SDD-19 | Supervisor state machine + transitions + store | `internal/supervise` | SDD-07, SDD-18 | [docs/sdd/SDD-19.md](sdd/SDD-19.md) |
| ✅ SDD-20 | Child fork/exec + signals + exit-78 + process-group death-watch | `internal/supervise` | SDD-19 | [docs/sdd/SDD-20.md](sdd/SDD-20.md) |
| ✅ SDD-21 | Refill + refresh + grace cache | `internal/supervise` | SDD-09, SDD-13, SDD-19 | [docs/sdd/SDD-21.md](sdd/SDD-21.md) |
| ✅ SDD-22 | PID file + flock + Unix status socket | `internal/supervise` | SDD-19 | [docs/sdd/SDD-22.md](sdd/SDD-22.md) |
| ✅ SDD-23 | `hush supervise` + `hush client status` + `hush client refresh` | `internal/cli` | SDD-14, SDD-18, SDD-19, SDD-20, SDD-21, SDD-22 | [docs/sdd/SDD-23.md](sdd/SDD-23.md) |
| SDD-25 | Lifecycle integration harness (15 scenarios — explicit AC-10 owner) | `tests/integration/` | ALL of SDD-01..SDD-23 | [docs/sdd/SDD-25.md](sdd/SDD-25.md) |
| ✅ SDD-24 | Reserved orchestration glue (default skipped) | — | (only activated if SDD-25 surfaces a gap) | [docs/sdd/SDD-24.md](sdd/SDD-24.md) |

### Phase 6 — Validators + alerts

| ID | Title | Package | Blocked by | Chunk file |
|----|-------|---------|------------|------------|
| ✅ SDD-27 | Log-pattern watchdog (alert-only) | `internal/supervise/watchdog` | SDD-20 | [docs/sdd/SDD-27.md](sdd/SDD-27.md) |
| SDD-26 | 5 builtin validators (anthropic, anthropic-oauth, openai, google-ai, github) | `internal/supervise/validators` | SDD-21 | [docs/sdd/SDD-26.md](sdd/SDD-26.md) |
| SDD-28 | 8 alert classes + tiered routing + DM rate limit | `internal/discord/alerts` | SDD-11, SDD-27 | [docs/sdd/SDD-28.md](sdd/SDD-28.md) |

### Phase 7 — Deployment

| ID | Title | Files | Blocked by | Chunk file |
|----|-------|-------|------------|------------|
| SDD-29 | Deploy artifacts (launchd plist, systemd unit, install.sh, generic launcher template) | `deploy/*` | SDD-15, SDD-23 | [docs/sdd/SDD-29.md](sdd/SDD-29.md) |
| SDD-30 | Generic example supervisor TOML + Tailscale ACL spec + clean-machine checklist | `deploy/examples/*` + `docs/TAILSCALE-ACLS.md` + `docs/CLEAN-MACHINE.md` | SDD-18, SDD-29 | [docs/sdd/SDD-30.md](sdd/SDD-30.md) |

### Phase 8 — Release

| ID | Title | Files | Blocked by | Chunk file |
|----|-------|-------|------------|------------|
| SDD-31 | Release gates (coverage + 6 fuzz + magex + go-pre-commit + govulncheck + gitleaks + CGO=0 + no /vendor) | `.github/workflows/*` | SDD-25 + every prior chunk | [docs/sdd/SDD-31.md](sdd/SDD-31.md) |
| SDD-33 | Final repo + docs overhaul (drift reconciliation, dead-code sweep, README rewrite) | repo-wide | SDD-25, SDD-31 | [docs/sdd/SDD-33.md](sdd/SDD-33.md) |
| SDD-32 | Open-source release: README + DAEMONS + repo-level OSS files + docs polish + GoReleaser + v0.1.0 tag | `README.md` + repo root + `docs/*` + `.goreleaser.yml` | SDD-31, SDD-33 | [docs/sdd/SDD-32.md](sdd/SDD-32.md) |

> **Cross-phase parallelism:** the phases are conceptual buckets, not
> hard execution barriers. The dependency graph is what matters. For
> example, SDD-18 (Phase 5) is only blocked by SDD-06 — it can start
> as soon as Phase 1 finishes, in parallel with all of Phase 2 / 3.
> Likewise SDD-08 and SDD-09 (Phase 2) can start the moment SDD-01
> and SDD-02 land. Use the `Blocked by` column to find the next
> launchable chunk; ignore phase boundaries when picking what to
> work on next.

---

## End of catalog

For dependency direction visualised, see
[`docs/IMPLEMENTATION-PLAN.md`](IMPLEMENTATION-PLAN.md). For the
current status of each chunk, see
[`docs/SDD-PLAYBOOK.md`](SDD-PLAYBOOK.md).
