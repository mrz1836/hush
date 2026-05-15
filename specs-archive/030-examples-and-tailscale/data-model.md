# Phase 1 — Data Model: SDD-30 Example Supervisor TOML

**Branch**: `030-examples-and-tailscale` | **Date**: 2026-05-14

This document specifies the structure of the static TOML artefact
`deploy/examples/supervisors/example-daemon.toml`. The artefact is
documentation written in TOML syntax; it is shipped in the repository,
loaded only by the `TestExamples_GenericTOMLValidates` test, and never
invoked by any runtime path.

The exact field set is captured in
[contracts/template-field-census.md](./contracts/template-field-census.md);
this document describes the *shape* — three entities, one ordering
contract, one comment grammar, three placeholder categories.

---

## 1. Entities

### 1.1 The template file (single instance)

| Property | Value |
|----------|-------|
| Path | `deploy/examples/supervisors/example-daemon.toml` |
| Owner | The repository (operator-facing documentation) |
| Loaded by | `TestExamples_GenericTOMLValidates` only — no runtime path |
| Modes | File mode `0644` (readable by anyone with repo access; the file holds no secrets and the schema design rule "secret values do not belong in config files on agent machines" applies) |
| Encoding | UTF-8, LF line endings, no BOM |
| Approx. size | 120 lines including comments (~70 lines of TOML + ~50 lines of comments and blank lines) |
| Created in chunk | SDD-30 (new file) |
| Future edits | Any later schema change MUST update this file in the same PR (per spec.md §Assumptions) |

### 1.2 The validation test (single function)

| Property | Value |
|----------|-------|
| Path | `internal/supervise/config/example_test.go` |
| Function | `func TestExamples_GenericTOMLValidates(t *testing.T)` |
| Test package | `package config` (no build tag) |
| External dependencies | `context`, `path/filepath`, `testing`, `github.com/stretchr/testify/require`, `github.com/stretchr/testify/assert` (all already in use) |
| Coverage delta | +0% on `internal/supervise/config` (the package is already at 100% on `Load`; the new test exercises an already-covered path) |

### 1.3 The forbidden-identifier seed list (FR-007, added in /speckit-tasks)

This entity is **not** created by /speckit-plan — it's part of the
/speckit-tasks output. Listed here for completeness because the
template's design must respect it.

| Property | Value |
|----------|-------|
| Path | `internal/supervise/config/example_test.go` (same file as 1.2) |
| Form | Package-private Go slice literal: `var operatorSpecificForbidden = []string{...}` |
| Initial entries | None yet — the seed list is "maintained as a one-line-per-addition Go slice in the test source. New forbidden identifiers are added one at a time as discoveries surface" (FR-007 clarification). The initial entry-set may be empty if no historically-leaked identifiers are known to the planner at the time. |
| Test using it | `TestExamples_NoOperatorSpecificNames` (also added in /speckit-tasks) |

---

## 2. File structure: ordering contract

The template is composed of seven blocks, top-to-bottom. Each block is
mandatory; ordering is fixed (operators expect a stable order across
chunks).

```text
1. Top-of-file comment block        (~15 lines — see §3)
2. Root-level scalar fields         (~14 fields)
3. Blank line + scope = [...] array (3-6 lines)
4. [child] table                    (~5 fields)
5. [discord] table                  (~2 fields)
6. [validators] table               (1-3 inline entries)
7. [watchdog] table                 (~3 fields)
```

Each block ends with a single blank line. No trailing blank line after
block 7.

The ordering mirrors `internal/supervise/config/testdata/valid_maximal.toml`
exactly. Stability with that fixture is intentional: future
schema-evolution chunks update both files in the same PR.

---

## 3. Top-of-file comment block grammar

The block opens the file. It MUST contain (in order):

1. **One sentence stating what the file is.**
   > "This file is the canonical operator-facing template for hush's
   > per-daemon supervisor config."
2. **The single CONFIG-SCHEMA.md anchor link** (FR-003 / clarification 5):
   > "Every field below is documented at:
   >   `docs/CONFIG-SCHEMA.md#supervisor-config`"
3. **The two companion-doc links** (FR-006):
   > "Companion docs:
   >   `docs/TAILSCALE-ACLS.md`  — network-layer hardening (Constitution VI)
   >   `docs/CLEAN-MACHINE.md`   — agent-host hygiene (Constitution I)"
4. **The Keychain-ACL contract reference** (FR-006 / AC-6):
   > "Per-binary Keychain ACL contract (AC-6):
   >   `[child].command[0]` is the binary path the operator ACL-binds
   >   in Keychain. See `docs/CLEAN-MACHINE.md` §8 (macOS Keychain)."
5. **A three-step operator workflow** (FR-001 user story 1):
   > "Operator workflow:
   >   1. Copy this file to `~/.hush/supervisors/<your-daemon>.toml`
   >   2. Find/replace every `REPLACE_ME` and `EXAMPLE_*` placeholder
   >   3. Run: `hush supervise --config ~/.hush/supervisors/<your-daemon>.toml`"

The block uses TOML comment syntax (`#` prefix, one space, then
content). No section labels — the block reads as a single prose
preamble.

---

## 4. Field-comment grammar (per FR-003 + clarification 2 + R-006)

Every field carries an inline comment. Two grammars:

**Required fields:**
```toml
<key> = <value>    # <one-sentence purpose>. Required.
```

**Optional fields (with documented loader default):**
```toml
<key> = <value>    # <one-sentence purpose>. Default: <loader-default>.
```

**Bool-as-toggle optional fields** (where the off-default has a
meaningful "if you flip this..." effect):
```toml
<key> = <value>    # <one-sentence purpose>. Default: <loader-default>; set <other> to <effect>.
```

The "loader default" string is the literal default value from
`internal/supervise/config/defaults.go` (e.g., `"20h"`, `"60m"`,
`true`, `false`, `6`, `"info"`).

The one-sentence purpose is operator-facing, not schema-facing:
explains *what the field does* in concrete operator terms ("which
binary the supervisor exec's", "how often the supervisor DMs at
refresh time"), not what type it is (the value itself shows the
type).

---

## 5. Placeholder taxonomy (per FR-004 + spec.md Key Entities)

Three categories of placeholder appear in the template. Each category
has a single naming convention; each placeholder is annotated by
its inline comment as belonging to one of the three.

### 5.1 Human-readable example slugs

Purpose: identifiers an operator renames to match their daemon.

| Slug | Used in field(s) | Operator action |
|------|------------------|----------------|
| `example-daemon` | `name`, status-socket path, pid-file path | Find/replace with the operator's daemon name (URL/path-safe slug per CONFIG-SCHEMA.md `name` rules) |
| `Example long-running daemon` | `reason` | Replace with a one-line human-readable explanation the operator wants to see in Discord approval DMs |
| `Example Daemon` | `[discord].daemon_label` | Replace with the operator's preferred nicer DM label |
| `your-daemon-binary` | `[child].command[0]` | Replace with the absolute path to the operator's daemon binary (e.g., `/usr/local/bin/my-daemon`) |

### 5.2 Scoped secret names (placeholder API key names)

Purpose: stand-ins for the actual secret-name keys the operator
will populate in `scope`, `[validators]`, and the vault.

| Placeholder | Used in field(s) | Operator action |
|-------------|------------------|----------------|
| `EXAMPLE_API_KEY_1` | `scope[0]`, `[validators]`, `[child].env_passthrough` (no — env_passthrough is for non-secret env only; see note below) | Replace with the operator's first secret name (e.g., `ANTHROPIC_API_KEY`) |
| `EXAMPLE_API_KEY_2` | `scope[1]`, `[validators]` | Replace with the operator's second secret name (or delete the entry) |

**Note on `[child].env_passthrough`** (clarifies a common operator
trap): `env_passthrough` carries *non-secret* env vars (PATH, HOME,
SHELL) into the child process; secrets are injected via the
supervisor's separate code path after JWT-claim + ECIES fetch. The
template's `env_passthrough` MUST NOT list any `EXAMPLE_API_KEY_*`
entry — that would mislead operators into thinking secrets pass
through the env-passthrough mechanism. (The user-prompt "locked HOW"
snippet's `[child].env = ["EXAMPLE_API_KEY_1", "EXAMPLE_API_KEY_2"]`
form does NOT exist in the schema; see research.md R-001.)

### 5.3 `REPLACE_ME` markers (no safe default)

Purpose: fields where any literal value would either look real (and
risk operator confusion) or fail loader validation in a subtle way.

| Marker | Used in field(s) | Operator action |
|--------|------------------|----------------|
| `REPLACE_ME` | `[discord].alert_channel_id` (a Discord snowflake — any literal looks real) | Replace with the operator's Discord alert channel ID |

Optional `server_url`: NOT a `REPLACE_ME` marker — it's the concrete
CGNAT literal `http://100.64.0.1:7743/h/example` per FR-008 and R-004,
which the loader accepts as-shipped. The inline comment flags it as a
placeholder the operator MUST replace before first boot, but the value
itself parses (per FR-008 / SC-001).

---

## 6. Per-field validation rules (echoes from CONFIG-SCHEMA.md / loader)

The template's values are chosen so every loader rule passes on
first attempt. The full rule-by-rule audit is in
[contracts/template-field-census.md](./contracts/template-field-census.md);
the summary here is for the data-model design view:

| Field | Loader rule | Template value compliance |
|-------|-------------|--------------------------|
| `name` | URL/path-safe slug, unique per host | `"example-daemon"` — slug-clean |
| `reason` | Non-empty | `"Example long-running daemon"` — non-empty |
| `server_url` | Parseable URL, http/https, non-empty host | `"http://100.64.0.1:7743/h/example"` — parses, http, host present (R-004) |
| `client_machine_index` | Required, BIP32 path index | `2` — non-negative integer |
| `session_type` | Must equal `"supervisor"` | `"supervisor"` — exact match |
| `requested_ttl` | ≤ 24h | `"20h"` — within cap, matches `DefaultRequestedTTL` |
| `refresh_window` | `HH:MM-HH:MM`, start < end | `"09:00-10:00"` — matches `DefaultRefreshWindow` |
| `refresh_nudge_before` | ≤ 6h | `"30m"` — matches `DefaultRefreshNudgeBefore` |
| `boot_retry_timeout` | ≤ 1h | `"10m"` — matches `DefaultBootRetryTimeout` |
| `cache_secrets_for_restart` | bool | `true` — opt-in grace cache demonstrates the knob |
| `cache_grace_ttl` | ≤ 4h, valid only when cache enabled | `"60m"` — matches `DefaultGraceWindow` |
| `status_socket` | Lexically clean path; expands `~/` | `"/tmp/hush/supervise-example-daemon.sock"` — matches `valid_maximal` fixture (also operator-friendly: writable without sudo) |
| `pid_file` | Lexically clean path; expands `~/` | `"/tmp/hush/supervise-example-daemon.pid"` — matches fixture |
| `log_level` | One of debug/info/warn/error | `"info"` — matches `DefaultLogLevel` |
| `scope` | Non-empty array | `["EXAMPLE_API_KEY_1", "EXAMPLE_API_KEY_2"]` — two entries to show array form |
| `[child].command` | Non-empty array, [0] absolute path | `["/usr/local/bin/your-daemon-binary", "start"]` — absolute path |
| `[child].working_dir` | String (no rule beyond home-expansion) | `"/tmp"` — matches fixture |
| `[child].env_passthrough` | String array (non-secret env names) | `["PATH", "HOME", "SHELL"]` — matches fixture |
| `[child].restart_on_clean_exit` | bool | `true` — matches default |
| `[child].restart_on_exit_78` | bool | `false` — matches default; flipping is dangerous (silent stale-cred loop) |
| `[discord].daemon_label` | String (no rule) | `"Example Daemon"` |
| `[discord].alert_channel_id` | String (snowflake, no format check at load) | `"REPLACE_ME"` — string-typed; loader accepts; operator MUST substitute before first boot |
| `[validators]` | Map of secret-name → validator-allow-listed-name | `EXAMPLE_API_KEY_1 = "anthropic"`, `EXAMPLE_API_KEY_2 = "openai"` |
| `[watchdog].enabled` | bool | `true` — matches default |
| `[watchdog].patterns` | String array (literal log fragments) | `["401 Unauthorized", "No API key found", "invalid x-api-key"]` — matches schema's example list; alert-only |
| `[watchdog].max_alerts_per_hour` | Positive int | `6` — matches default |

**Important**: `audit_log` is intentionally NOT included in the
template. The loader treats absence as "default to
`<dirname(pid_file)>/<name>-audit.jsonl`" (see `materialize` in
`config.go:281-289`). Including it as a placeholder would force the
operator to either invent a path or accept a redundant repetition of
the pid_file dirname; omitting it lets the loader's default do its
job. The inline comment on `pid_file` will mention that
`audit_log` defaults relative to it.

---

## 7. State transitions

The template file is **immutable** in this chunk — it ships as a
static artefact. State transitions in scope:

| Trigger | New state |
|---------|-----------|
| `/speckit-tasks` task T1 runs (write `TestExamples_GenericTOMLValidates`) | The test fails (RED) because the template file doesn't exist yet |
| `/speckit-tasks` task T2 runs (write the template) | The test passes (GREEN); the template is now the canonical operator-facing example |
| Future schema change in any later chunk | That chunk MUST update this template in the same PR (per spec.md §Assumptions); state machine of *this chunk* is unaffected |
| Future "add a forbidden identifier" PR | One-line edit to the `operatorSpecificForbidden` slice in `example_test.go` (FR-007) — template file unchanged |

---

## 8. Relationships

```text
                         ┌──────────────────────────────────────┐
                         │  internal/supervise/config/          │
                         │    config.go         (Load function) │
                         │    defaults.go       (loader defaults) │
                         │    validate.go       (rule engine)  │
                         │    testdata/         (existing fixtures) │
                         └──────────────────────────────────────┘
                                       ▲
                                       │ Load(ctx, path)
                                       │
                         ┌─────────────┴────────────────────────┐
                         │  internal/supervise/config/          │
                         │    example_test.go        NEW        │
                         │      TestExamples_GenericTOMLValidates │
                         │      TestExamples_NoOperatorSpecificNames │
                         └──────────────────────────────────────┘
                                       │ reads (relative path)
                                       ▼
                         ┌──────────────────────────────────────┐
                         │  deploy/examples/supervisors/        │
                         │    example-daemon.toml    NEW        │
                         │      (this chunk's main deliverable) │
                         └──────────────────────────────────────┘
                                       │
                                       │ references via top-of-file comments
                                       ▼
                         ┌──────────────────────────────────────┐
                         │  docs/                                │
                         │    CONFIG-SCHEMA.md    (single link)  │
                         │    TAILSCALE-ACLS.md   (one link)     │
                         │    CLEAN-MACHINE.md    (one link)     │
                         └──────────────────────────────────────┘
                                       ▲
                                       │ verify-and-polish in this chunk
                                       │ per FR-009 / FR-010 / R-002 / R-003
                                       │
                         ┌─────────────┴────────────────────────┐
                         │  .specify/memory/constitution.md     │
                         │  docs/SECURITY.md §2.3               │
                         │  docs/CONFIG-SCHEMA.md [network]     │
                         │  deploy/install.sh                   │
                         └──────────────────────────────────────┘
                              (read-only inputs for the verification pass)
```

The chunk introduces no new package, no new public type, no new
exported symbol. The only behavioural relationship is: the new test
loads the new file via the existing `Load` function.

---

## 9. Open issues

None — every research item (R-001 … R-006) is resolved in
[research.md](./research.md).
