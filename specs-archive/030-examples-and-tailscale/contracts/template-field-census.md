# Contract: Template Field Census

**Branch**: `030-examples-and-tailscale` | **Date**: 2026-05-14

This contract enumerates every field documented in
`docs/CONFIG-SCHEMA.md` §Supervisor-config and maps it to the
corresponding line in `deploy/examples/supervisors/example-daemon.toml`,
the inline comment grammar that field will carry, and the
loader-validation rule the value satisfies. The /speckit-tasks T2 task
builds the file by walking this table top to bottom.

The table is the single source of truth for FR-002 / FR-003 /
SC-002. The post-/speckit-implement gate is: every row's
"Template line" cell is non-empty AND
`TestExamples_GenericTOMLValidates` is GREEN.

---

## Field census table

Legend:
- **Block**: 1 = root scalars; 2 = `scope` array; 3 = `[child]`; 4 = `[discord]`; 5 = `[validators]`; 6 = `[watchdog]`.
- **R/O**: Required / Optional (per `validate.go:requiredFieldGate`).
- **Loader default** (Optional only): from `defaults.go`.
- **Template value**: the literal that will appear in the file.
- **Inline comment** (purpose + Required-or-Default): the comment grammar from data-model.md §4.

### Block 1 — Root-level scalar fields

| # | Field | R/O | Loader default | Template value | Inline comment |
|---|-------|-----|----------------|---------------|----------------|
| 1 | `name` | R | — | `"example-daemon"` | `# Unique daemon slug — URL/path-safe; appears in DM labels, status socket path, and pid file. Required.` |
| 2 | `reason` | R | — | `"Example long-running daemon"` | `# One-line human reason shown in Discord approval DMs. Required.` |
| 3 | `server_url` | R | — | `"http://100.64.0.1:7743/h/example"` | `# Tailscale CGNAT vault IP + canonical hush port + URL prefix — REPLACE the IP and prefix with values from your vault host's hush init output before first boot. Required.` |
| 4 | `client_machine_index` | R | — | `2` | `# BIP32 client-key path index — match the index used at hush init --client on this host. Required.` |
| 5 | `session_type` | R | — | `"supervisor"` | `# Fixed for supervisor configs — the loader refuses any other value. Required.` |
| 6 | `requested_ttl` | O | `"20h"` | `"20h"` | `# Discord-approved session lifetime — capped server-side at max_supervisor_ttl. Default: 20h.` |
| 7 | `refresh_window` | O | `"09:00-10:00"` | `"09:00-10:00"` | `# Local-time window in which the supervisor DMs you the daily refresh prompt. Default: 09:00-10:00.` |
| 8 | `refresh_nudge_before` | O | `"30m"` | `"30m"` | `# How long before refresh_window the supervisor sends its T-N reminder DM. Default: 30m.` |
| 9 | `boot_retry_timeout` | O | `"10m"` | `"10m"` | `# Cap on supervisor's exponential-backoff retries when vault or Tailscale is unreachable at boot. Default: 10m.` |
| 10 | `cache_secrets_for_restart` | O | `false` | `true` | `# Hold the last good secrets in mlocked memory during the grace window so a crash within cache_grace_ttl doesn't re-prompt. Default: false; set true to opt into grace cache.` |
| 11 | `cache_grace_ttl` | O | `"60m"` (only when cache_secrets_for_restart=true) | `"60m"` | `# Lifetime of the grace cache after first arming — capped at 4h. Default: 60m.` |
| 12 | `status_socket` | R | — | `"/tmp/hush/supervise-example-daemon.sock"` | `# Unix-socket path hush client status reads — file mode 0600, parent dir 0700. Required.` |
| 13 | `pid_file` | R | — | `"/tmp/hush/supervise-example-daemon.pid"` | `# Pid-file path used for flock split-brain guard; audit_log defaults to <dirname>/<name>-audit.jsonl. Required.` |
| 14 | `log_level` | O | `"info"` | `"info"` | `# Supervisor log verbosity — one of debug, info, warn, error. Default: info.` |

Field omitted from template by design:
- `audit_log` — loader auto-defaults to
  `<dirname(pid_file)>/<name>-audit.jsonl`; including a redundant
  literal would force the operator to invent a path. See
  data-model.md §6 for rationale.

### Block 2 — `scope` array

| # | Field | R/O | Loader default | Template value | Inline comment |
|---|-------|-----|----------------|---------------|----------------|
| 15 | `scope` (array, multi-line form) | R | — | `["EXAMPLE_API_KEY_1", "EXAMPLE_API_KEY_2"]` | `# Exact secret names this daemon may fetch — substitute your real secret names (e.g., ANTHROPIC_API_KEY). Required, non-empty.` |

### Block 3 — `[child]` table

| # | Field | R/O | Loader default | Template value | Inline comment |
|---|-------|-----|----------------|---------------|----------------|
| 16 | `command` | R | — | `["/usr/local/bin/your-daemon-binary", "start"]` | `# Daemon process invocation — element [0] must be an absolute path (ACL-bound binary per AC-6); no shell parsing. Required.` |
| 17 | `working_dir` | O | (no default in defaults.go; field optional with empty meaning "current") | `"/tmp"` | `# Working directory the child runs in — operator's choice; expands ~/. Default: process default if absent.` |
| 18 | `env_passthrough` | O | (empty list) | `["PATH", "HOME", "SHELL"]` | `# Non-secret env vars to inherit into the child — NEVER list secret names here, those flow through scope above. Default: [].` |
| 19 | `restart_on_clean_exit` | O | `true` | `true` | `# Whether to restart the child after a clean exit (code 0). Default: true.` |
| 20 | `restart_on_exit_78` | O | `false` | `false` | `# Whether to restart on exit code 78 (EX_CONFIG = stale credentials) — keep false; flipping causes silent stale-cred loops. Default: false.` |

### Block 4 — `[discord]` table

| # | Field | R/O | Loader default | Template value | Inline comment |
|---|-------|-----|----------------|---------------|----------------|
| 21 | `daemon_label` | O | (empty) | `"Example Daemon"` | `# Nicer label shown in Discord DMs and alerts — replace with whatever you want to see in your phone. Default: empty (falls back to name).` |
| 22 | `alert_channel_id` | O | (empty) | `"REPLACE_ME"` | `# Discord channel snowflake for non-DM operational alerts — substitute your alerts channel ID before first boot. Default: empty (alerts go to DM only).` |

### Block 5 — `[validators]` table

The validators map is REQUIRED (loader treats a nil map as missing).
Each entry maps a secret name to a validator-allow-listed string. The
template carries two entries to demonstrate the map form.

| # | Field | R/O | Loader default | Template value | Inline comment |
|---|-------|-----|----------------|---------------|----------------|
| 23 | `[validators]` table opening | R (presence) | — | (table header) | `# Per-secret pre-injection validator names — allow-list: anthropic, anthropic-oauth, openai, google-ai, github. Required (map must be present, but entries may be empty if you accept the staleness contract via exit 78 only).` |
| 24 | `EXAMPLE_API_KEY_1 = "anthropic"` | (map entry) | — | `EXAMPLE_API_KEY_1 = "anthropic"` | `# Validator type for EXAMPLE_API_KEY_1 — replace key with your secret name.` |
| 25 | `EXAMPLE_API_KEY_2 = "openai"` | (map entry) | — | `EXAMPLE_API_KEY_2 = "openai"` | `# Validator type for EXAMPLE_API_KEY_2 — replace key with your secret name (or delete this entry).` |

### Block 6 — `[watchdog]` table

Optional section; the loader applies all defaults if the table is
absent. The template includes the section explicitly with the
defaults so operators see every knob (clarification 2).

| # | Field | R/O | Loader default | Template value | Inline comment |
|---|-------|-----|----------------|---------------|----------------|
| 26 | `enabled` | O | `true` | `true` | `# Whether the log-pattern watchdog is on — alert-only; never drives restart policy. Default: true.` |
| 27 | `patterns` | O | `[]` (empty) | `["401 Unauthorized", "No API key found", "invalid x-api-key"]` | `# Literal log-fragment strings to grep for; matches trigger a Discord alert, not a child restart. Default: [].` |
| 28 | `max_alerts_per_hour` | O | `6` | `6` | `# Cap on watchdog Discord alerts per hour — operator typo guard. Default: 6.` |

---

## Total field count

- Block 1: 14 fields (one omitted by design — `audit_log`)
- Block 2: 1 field (`scope` array)
- Block 3: 5 fields (`[child]`)
- Block 4: 2 fields (`[discord]`)
- Block 5: 1 table + 2 map entries (`[validators]`)
- Block 6: 3 fields (`[watchdog]`)

**Total: 27 distinct documented fields** + 2 example map entries — every
CONFIG-SCHEMA.md §Supervisor-config field except the deliberately-omitted
`audit_log`. SC-002 ("Every field documented in CONFIG-SCHEMA.md
appears in the template") is satisfied; the inline comment on
`pid_file` (row 13) documents the `audit_log` default to preserve
operator discoverability.

---

## Validation test contract

```go
// example_test.go (package config; no build tag)
package config

import (
    "context"
    "path/filepath"
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

// TestExamples_GenericTOMLValidates feeds the canonical operator-facing
// supervisor TOML template through the SDD-18 loader and asserts zero
// validation error. The path traversal is three levels up from this
// test file (config -> supervise -> internal -> repo-root) plus
// /deploy/examples/supervisors/example-daemon.toml.
func TestExamples_GenericTOMLValidates(t *testing.T) {
    t.Parallel()

    path := filepath.Join(
        "..", "..", "..",
        "deploy", "examples", "supervisors", "example-daemon.toml",
    )

    sup, err := Load(context.Background(), path)
    require.NoError(t, err, "the canonical operator-facing template MUST validate against the SDD-18 loader as-shipped (FR-005)")
    require.NotNil(t, sup)

    // Spot-check the headline placeholder slug — guards against an
    // accidental find-replace of "example-daemon" to a real name.
    assert.Equal(t, "example-daemon", sup.Name)
    assert.Equal(t, "supervisor", sup.SessionType)
}
```

The test makes no assertions about field values beyond the slug
spot-check — its only job is the loader round-trip. The forbidden-name
gate (`TestExamples_NoOperatorSpecificNames`) is added by
/speckit-tasks per FR-007.

---

## Acceptance criteria mapping

| Spec ID | Where this contract satisfies it |
|---------|--------------------------------|
| FR-001 | Template path is named in row 1 of the data-model state-transitions table; this contract confirms it as the canonical file. |
| FR-002 | All 27 documented fields are accounted for in the census table; `audit_log` is documented-by-comment on `pid_file` row 13. |
| FR-003 | Every census row carries an "Inline comment" cell that matches the data-model §4 grammar (purpose + Required/Default suffix). |
| FR-004 | Rows 1, 2, 4, 14, 21, 22, 23, 24, 25 use placeholder slugs / scoped secret names / `REPLACE_ME` markers per the taxonomy in data-model §5. |
| FR-005 | Validation test contract is captured in this file's §Validation test contract block. |
| FR-006 | Top-of-file comment block per data-model §3 — references TAILSCALE-ACLS.md, CLEAN-MACHINE.md, the Keychain-ACL contract (AC-6), and the `[child].command[0]` callout. |
| FR-008 | Row 3 — `server_url` literal inside `100.64.0.0/10`; comment marks it as placeholder. |
| SC-001 | The validation test contract is the operationalisation of SC-001. |
| SC-002 | The census table is the side-by-side field census SC-002 names. |
| SC-004 | Top-of-file comment block per data-model §3 contains the three required references. |
| AC-6 | `[child].command[0]` comment in row 16 + top-of-file block per FR-006. |
| AC-8 | Row 3 — `server_url` is a Tailscale CGNAT literal consistent with `[network] allowed_cidrs = ["100.64.0.0/10"]`. |
| AC-10 | All 27 fields populated — covers the full supervisor-config schema, which is what AC-10 exercises through its 15 scenarios. |
