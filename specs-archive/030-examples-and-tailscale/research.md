# Phase 0 — Research: SDD-30 Example Supervisor TOML + Doc Verification

**Branch**: `030-examples-and-tailscale` | **Date**: 2026-05-14

This document resolves every unknown the plan needed before Phase 1
design. Six items. Each follows the Decision / Rationale / Alternatives
Considered format.

---

## R-001 — Schema authority: SDD-18 loader vs. the prompt's "locked HOW" snippet

### Context

The chunk-doc Prompt 3 (and the user prompt that invoked
`/speckit-plan`) includes a "locked HOW" snippet that proposes this
template structure:

```toml
name = "example-daemon"
[child]
  command = ["/usr/local/bin/your-daemon", "--flag"]
  env = ["EXAMPLE_API_KEY_1", "EXAMPLE_API_KEY_2"]
[discord]
  audit_channel_id = "REPLACE_ME"
[validators.example_api_key_1]
  type = "anthropic"
[watchdog]
  patterns = [
    { name = "rate-limit", regex = "(?i)rate.limit", rate_limit = "5m" },
  ]
[grace]
  window = "30m"
  cache_secrets_for_restart = true
refresh_window = "03:00-05:00"
boot_retry_timeout = "10m"
dm_rate_limit = "5m"
```

The actual SDD-18 loader at `internal/supervise/config/config.go` uses
`toml.NewDecoder(...).DisallowUnknownFields()` and decodes into a
strict struct (`supervisorDecoded`) whose shape is mirrored in
`docs/CONFIG-SCHEMA.md` §Supervisor-config. Concretely, the loader's
canonical maximal example lives at
`internal/supervise/config/testdata/valid_maximal.toml` and looks
nothing like the prompt's snippet.

### Divergences (each one is a hard loader rejection)

| Prompt-snippet form | What the loader accepts | Loader sentinel that fires on the prompt form |
|---------------------|-------------------------|-----------------------------------------------|
| `[child].env = [...]` | `[child].env_passthrough = [...]` | `ErrUnknownField` ("env" not in childDecoded) |
| `[discord].audit_channel_id = ...` | `[discord].alert_channel_id = ...` | `ErrUnknownField` |
| `[validators.example_api_key_1].type = "anthropic"` (sub-table form) | `[validators]\nEXAMPLE_API_KEY_1 = "anthropic"` (inline map) | `ErrUnknownField` (the validators decoder is `map[string]string`, not a sub-table-per-secret) |
| `[watchdog].patterns = [{name=,regex=,rate_limit=}]` (struct form) | `[watchdog].patterns = ["literal-string", ...]` (string array) | `ErrTOMLDecode` (type mismatch — `[]string` cannot decode a struct array) |
| `[grace]\nwindow = ..., cache_secrets_for_restart = ...` | Root-level `cache_secrets_for_restart` (bool) + root-level `cache_grace_ttl` (duration) | `ErrUnknownField` (no `[grace]` section in the schema) |
| `dm_rate_limit = "5m"` | Not in schema | `ErrUnknownField` (and the loader's `DefaultDMRateLimit` constant is package-internal — it parametrises code, not config) |
| Missing `reason`, `server_url`, `client_machine_index`, `session_type`, `status_socket`, `pid_file`, `scope` | All seven are required per the loader's `requiredFieldGate` | `ErrMissingRequiredField` (errors.Join — operator sees the full punch list) |

If we shipped the prompt's snippet literally, the
`TestExamples_GenericTOMLValidates` test (FR-005) would fail with at
least seven different errors. That directly contradicts FR-005, which
says the template "MUST validate cleanly against the SDD-18
supervisor-config loader as-shipped".

### Decision

**The SDD-18 loader + `docs/CONFIG-SCHEMA.md` §Supervisor-config are
the authoritative source for the template's shape.** The prompt's
"locked HOW" snippet is treated as a stale draft that predates the
SDD-18 loader's implementation and is **superseded** for the purposes
of this chunk.

The template's structure follows `testdata/valid_maximal.toml`
verbatim for field names, types, ordering, and section grouping;
diverges from it only in (a) substituting placeholder values for the
fixture-realistic values (`100.96.10.4` → `100.64.0.1`,
`/usr/local/bin/your-daemon-binary` → kept identical, etc.), (b)
adding per-field inline comments per FR-003, and (c) adding the
top-of-file comment block per FR-006.

### Rationale

1. **Spec authority.** spec.md §Assumptions explicitly names
   `docs/CONFIG-SCHEMA.md` (Phase 0) as "the source of truth for which
   fields the template must populate", and the SDD-18 loader at
   `internal/supervise/config` as "the authoritative validator for the
   template". Both override an out-of-date prompt snippet.
2. **FR-005 is testable.** If the template doesn't validate against
   the loader, the chunk fails its own gate
   (`TestExamples_GenericTOMLValidates`). The plan must produce a
   template that the test actually accepts.
3. **Constitution VIII.** Sentinel-error specificity (loader returns
   `ErrUnknownField` / `ErrMissingRequiredField`) means the failure
   modes the prompt snippet would trigger are not subtle — they would
   immediately surface in CI.
4. **Loader stability.** The loader's strict-decode behaviour is itself
   under test by 30+ entries in
   `internal/supervise/config/config_test.go` (incl.
   `TestSuperviseConfig_RejectsUnknownField`). Bending the loader to
   the snippet would re-open SDD-18 — out of scope for this chunk.

### Alternatives considered

- **Edit the loader to accept the prompt's snippet form.** Rejected:
  out of scope (would re-open SDD-18), would invalidate
  `testdata/valid_maximal.toml`, and would break ≥10 existing tests.
- **Edit CONFIG-SCHEMA.md to match the snippet.** Rejected: same
  reason, plus FR-009/FR-010-style consistency would then re-fire
  across every consumer of the schema.
- **Ship two templates** (one schema-faithful, one snippet-faithful).
  Rejected: violates FR-001 ("the canonical operator-facing template")
  and confuses the public release narrative.
- **Flag the divergence to the user before proceeding.** Considered;
  rejected because the spec's Assumptions section already resolves
  the precedence, and the user prompt does say
  "/speckit-plan runs a Constitution Check — if it fires, fix the
  plan, do NOT bypass". This research entry IS the documented fix.

---

## R-002 — Tailscale tag-naming divergence

### Context

Three documents currently disagree on the canonical Tailscale tag pair
that gates port 7743:

| Source | Tag pair used | Status |
|--------|---------------|--------|
| `.specify/memory/constitution.md` Principle VI (constitutional non-negotiable) | `tag:trusted → tag:sandbox:7743` | Authoritative — cannot be edited in this chunk per scope |
| `docs/SECURITY.md §2.3 Network` | `tag:trusted → tag:sandbox` | Authoritative — matches constitution |
| `docs/TAILSCALE-ACLS.md` (whole document) | `tag:hush-agent → tag:hush-vault:7743` | Operator-facing — in scope for this chunk's verify-and-polish |
| `docs/SPEC.md` §FR-8 | `tag:trusted → tag:sandbox:7743` | Authoritative — matches constitution |

This is exactly the kind of cross-document inconsistency FR-009 was
written to catch: TAILSCALE-ACLS.md has drifted from
SECURITY.md / SPEC.md / constitution-VI on the specific names.

### Decision

**Patch-edit `docs/TAILSCALE-ACLS.md` to align with
constitution-VI / SECURITY.md / SPEC.md.** The constitution's tag pair
(`tag:trusted` / `tag:sandbox`) is the *floor* the document presents;
the more descriptive `tag:hush-agent` / `tag:hush-vault` pair is
documented as a recommended operator alternative in the existing
section on tag-name substitution.

Concretely, the verify-and-polish pass:

1. **Updates the opening of "The pattern" section** to state the
   constitutional pair first: "The constitution names the canonical
   pair as `tag:trusted → tag:sandbox:7743`. Many operators prefer
   more descriptive tags such as `tag:hush-agent → tag:hush-vault`;
   the *pattern* (one source tag, one destination tag, one port grant)
   is the load-bearing part — the specific names are operator choice."
2. **Updates the example ACL JSON block** to show both pairs side by
   side (or use the constitutional pair as the primary example with a
   short comment noting the descriptive alternative).
3. **Leaves the "tightening further" section untouched** — its
   per-agent and time-of-day refinements are orthogonal to the tag
   pair.
4. **Adds a verification line** that an operator who deploys with
   either pair satisfies constitution-VI as long as the source-to-dest
   port-7743-only grant pattern holds.

The constitution itself is **NOT** edited (out of scope; would also
require a constitutional amendment per `.specify/memory/constitution.md`
§Governance).

### Rationale

1. **FR-009 explicit clause:** "Any divergence MUST be resolved by
   editing the document, not by editing the schema or security model."
   The schema (CONFIG-SCHEMA.md `[network]`) and security model
   (SECURITY.md §2.3) are not edited; TAILSCALE-ACLS.md is.
2. **Operator-agnostic intent (Principle I).** TAILSCALE-ACLS.md's
   existing prose already says operators may substitute their own
   names. The realignment preserves that operator agency while
   anchoring the canonical pair to the constitution.
3. **Backward compatibility.** Operators who already deployed using
   `tag:hush-agent` / `tag:hush-vault` (the doc's previous
   recommendation) continue to satisfy the pattern — no breaking
   change for early adopters.

### Alternatives considered

- **Constitution amendment to use `tag:hush-agent` / `tag:hush-vault`
  in Principle VI.** Rejected: out of scope (chunk-doc says
  "Constitutional principles in scope: I, VI" — VI is referenced, not
  amended), and would require a separate constitutional-amendment PR
  per the constitution's Governance section.
- **Leave TAILSCALE-ACLS.md unchanged.** Rejected: directly violates
  FR-009 (the verification pass MUST resolve divergences).
- **Replace `tag:hush-agent`/`tag:hush-vault` entirely with
  `tag:trusted`/`tag:sandbox`.** Rejected as over-correction — the
  descriptive names are already in deployed documentation and removing
  them would confuse existing adopters.

---

## R-003 — Keychain-item naming divergence

### Context

`docs/CLEAN-MACHINE.md` §8 (macOS Keychain) currently states:

> hush-managed entries — keep these. They are ACL-restricted to
> `/usr/local/bin/hush` and used to derive client keys at runtime.

And then references the documented names "`hush`, `hush-discord`,
`hush-client`" elsewhere in the doc and in `.specify/memory/constitution.md`
Security Requirements ("Keychain ACLs (macOS): The passphrase entry MUST
restrict access to the `hush` binary path only").

The actual installer at `deploy/install.sh` (SDD-29) prints, in its
post-install banner:

```
security add-generic-password \
  -a "${HUSH_USER}" -s "hush-vault-passphrase" \
  -T "${RESOLVED_BIN_FOR_ACL}" \
  -U -w "<YOUR-PASSPHRASE>"
```

So the canonical Keychain item names — established by SDD-29 in the
artefact operators actually copy/paste — are:

| Entry | Purpose | Host | Created by |
|-------|---------|------|-----------|
| `hush-vault-passphrase` | Vault passphrase, ACL-bound to `/usr/local/bin/hush` | Vault host | Operator manually post-install (install.sh prints the command) |
| `hush-discord` | Discord bot token | Vault host | Operator manually (server config references `[discord].bot_token_keychain_item = "hush-discord"`) |
| `hush-client` | Per-machine client-key passphrase | Agent host | `hush init --client` |

CLEAN-MACHINE.md §8 references the older name `hush` for the
vault-passphrase entry; SDD-29's installer uses
`hush-vault-passphrase`. This is exactly the kind of doc-vs-installer
divergence FR-010 was written to catch.

### Decision

**Patch-edit `docs/CLEAN-MACHINE.md` §8 to name the three canonical
entries in the order/spelling the installer uses.** Specifically:

- Replace "hush-managed entries — keep these. They are ACL-restricted
  to `/usr/local/bin/hush` and used to derive client keys at runtime."
  with a short bulleted enumeration of the three entries (purpose +
  host) that match SDD-29's install.sh banner output exactly.

The installer (`deploy/install.sh`) is **NOT** edited (out of scope
per FR-010: "Any divergence MUST be resolved by editing the document,
not by editing the installer").

The constitution Security Requirements row that says "The passphrase
entry MUST restrict access to the `hush` binary path only" is **NOT**
edited — the rule is about the ACL constraint, not the item name; the
constitutional sentence is correct as written.

### Rationale

1. **FR-010 explicit clause:** "Any divergence MUST be resolved by
   editing the document, not by editing the installer."
2. **SDD-29 is the authoritative installer.** The chunk-doc names
   SDD-29 as the source of truth and SDD-30 as the consumer that
   verifies CLEAN-MACHINE.md against it.
3. **Operator-agnostic intent.** All three entry names are generic
   (`hush-*`); no operator-specific identifier appears. FR-011
   (zero operator-specific identifiers in CLEAN-MACHINE.md) is
   preserved.

### Alternatives considered

- **Edit the installer to use the simpler `hush` name.** Rejected:
  out of scope (SDD-30 ships only docs + the example TOML, never
  edits the installer per FR-010).
- **Edit the constitution Security Requirements row.** Rejected: the
  row is about the ACL constraint, not the item name — no
  constitutional change required.

---

## R-004 — CGNAT placeholder choice for `server_url`

### Context

FR-008 requires `server_url` in the template to be a concrete CGNAT
literal inside `100.64.0.0/10` such that the SDD-18 loader's
URL-parse step (`validateServerURL` in `validate.go`) accepts the
literal as-shipped, with an inline comment marking it as a placeholder
the operator MUST replace with their vault's real Tailscale IP before
first boot. Clarification 3 in spec.md uses
`https://100.64.0.1:7743/h/example` as the worked example.

The loader's `validateServerURL` does NOT itself enforce the CGNAT
range — that's a runtime concern enforced at server-side
`[network] allowed_cidrs` startup check (see CONFIG-SCHEMA.md
`[network] allowed_cidrs = ["100.64.0.0/10"]`). The loader only
requires: non-empty value, parseable URL, non-empty host, scheme is
`http` or `https`. So any well-formed URL inside `100.64.0.0/10`
satisfies the loader; FR-008 imposes the CGNAT discipline on the
*template* for cross-doc consistency.

### Decision

`server_url = "http://100.64.0.1:7743/h/example"`

- **`http://`** scheme (not `https://`) — matches `valid_maximal.toml`
  and matches CONFIG-SCHEMA.md (which says "must point to
  `http://<tailscale-ip>:7743/h/<prefix>` in v0.1.0"; Layer 6 of
  SECURITY.md / Principle III explicitly defers TLS-within-Tailscale
  to a future release).
- **`100.64.0.1`** — first usable address in `100.64.0.0/10`, so any
  operator who chooses a different vault host IP will substitute it.
- **`:7743`** — canonical hush port (constitution VI, SECURITY.md,
  CONFIG-SCHEMA.md all agree).
- **`/h/example`** — path-prefix `example` is URL-safe (6-32
  characters per CONFIG-SCHEMA.md `path_prefix`) and explicitly marked
  as a placeholder by an inline comment.

Inline comment grammar: `# Tailscale CGNAT vault IP + canonical hush
port + URL prefix. REPLACE the IP+prefix with values from your vault
host's hush init output before first boot.`

### Rationale

1. **Clarification 3 in spec.md** uses this exact form as the worked
   example.
2. **Loader-clean** — `validateServerURL` accepts http+host+scheme;
   no other check trips.
3. **Visibility** — `100.64.0.1` is the most obviously-placeholder
   address inside the range (first allocatable); any operator who
   sees it in production output would notice it was left
   un-substituted. Reinforces FR-008's "fails fast" semantics for
   the edge-case from spec.md.

### Alternatives considered

- `https://100.64.0.1:7743/h/example`. Rejected: spec.md clarification
  3 uses `https://` *as an example of form*, but the project ships
  HTTP-only over Tailscale in v0.1.0 (Principle III Layer 6 defers
  TLS); shipping `https` in the template would conflict with the
  loader's runtime-equivalent (which is HTTP) and mislead operators
  into expecting v0.1.0 to terminate TLS. The minimal-loader-clean
  choice is `http`, matching `testdata/valid_maximal.toml`.
- `100.96.10.4` (the test fixture's literal). Rejected: it's outside
  the `100.64.0.0/10` range as documented (it IS inside the broader
  Tailscale CGNAT block, but `100.64.0.1` is the first-allocatable
  literal and reads more obviously as a placeholder).
- A non-routable RFC 5737 sample (`192.0.2.1`). Rejected: violates
  FR-008 (must be inside `100.64.0.0/10`) and would fail the runtime
  `[network] allowed_cidrs` check.

---

## R-005 — Test location and helper pattern

### Context

The chunk-doc says the validation test goes "added to
`internal/supervise/config` tests OR a new `deploy/examples/`-level
test file". The spec's "Coverage target: N/A" and clarification 5
make clear this is a single-purpose, single-assertion test.

Existing tests in the package (`config_test.go`,
`validate_test.go`, `config_fuzz_test.go`) all sit in the `config`
package and use relative paths for fixtures (e.g.,
`testdata/valid_maximal.toml`). The template lives at
`deploy/examples/supervisors/example-daemon.toml` — relative path
from `internal/supervise/config/example_test.go` is
`../../../../deploy/examples/supervisors/example-daemon.toml` (four
`..` levels — verified by directory tree).

### Decision

Create a new file:
**`internal/supervise/config/example_test.go`** (package `config`,
no build tag).

```go
//go:build !nointegration
// (build tag optional — chunk-doc says "or just under the normal test pkg".
// Use the normal package — no build tag — because the test is a
// pure file-read + Load() round-trip, no I/O, no network.)
```

```go
package config

func TestExamples_GenericTOMLValidates(t *testing.T) {
    t.Parallel()
    path := filepath.Join("..", "..", "..", "deploy", "examples", "supervisors", "example-daemon.toml")
    sup, err := Load(context.Background(), path)
    require.NoError(t, err)
    require.NotNil(t, sup)
    assert.Equal(t, "example-daemon", sup.Name)
    assert.Equal(t, "supervisor", sup.SessionType)
}
```

The path traversal is `internal/supervise/config/ → ../../../deploy/` —
**three** `..` levels (config → supervise → internal → repo-root,
then `/deploy/...`).
[contracts/template-field-census.md](./contracts/template-field-census.md)
records the exact relative path the test will use.

`TestExamples_NoOperatorSpecificNames` (FR-007, added in
/speckit-tasks per Prompt 4) lives in the same file and uses the
same path. Seed forbidden list per FR-007 / clarification 1 is a
package-private Go slice literal in that file.

### Rationale

1. **Co-location.** The test exercises the SDD-18 loader's public
   `Load` function; placing it next to the loader keeps the test
   discoverable and pinned.
2. **No new test directory.** Avoids creating a parallel
   `tests/deploy-examples/` tree just for one test.
3. **Existing pattern.** `config_test.go` already loads relative-path
   fixtures (`testdata/valid_*.toml`); the new test follows the same
   shape, just with a longer relative path.
4. **No build tag needed.** The test does only file-read + parse — no
   network, no time-sensitive behaviour, no integration concerns.

### Alternatives considered

- `tests/deploy-examples/example_validates_test.go` with
  `//go:build integration`. Rejected: chunk-doc's coverage target is
  N/A and the test isn't an integration test (no external
  dependencies). Adding a new test directory introduces friction.
- Inline test inside `config_test.go`. Rejected: harder to locate;
  splitting into a dedicated `example_test.go` makes the
  examples-specific gate visible at a glance.

---

## R-006 — Optional-field commenting convention

### Context

Clarification 2 (spec.md): "Every field (required and optional)
appears as an active example value, with an inline comment naming the
loader default so operators see every knob at once and delete what
they don't need."

FR-003: "Every field in the template MUST carry an inline comment
that states the field's purpose in one short sentence." Plus
clarification 5: "A single explicit pointer at the top of the file
(e.g., 'Every field below is documented at
`docs/CONFIG-SCHEMA.md#supervisor-config`'); per-field inline
comments are purpose-only and do not repeat the doc link."

### Decision

**Two grammars: required fields and optional fields.**

```toml
# Required field grammar:
<key> = <value>                      # <one-sentence purpose>. Required.

# Optional field grammar:
<key> = <value>                      # <one-sentence purpose>. Default: <loader-default>.

# Bool-as-toggle grammar (optional):
<key> = <value>                      # <one-sentence purpose>. Default: <loader-default>; set <other-value> to <effect>.
```

The single CONFIG-SCHEMA.md anchor lives in the top-of-file comment
block (FR-003 / clarification 5):

```toml
# This file is the canonical operator-facing template for hush's
# per-daemon supervisor config. Every field below is documented at:
#   docs/CONFIG-SCHEMA.md#supervisor-config
# Companion docs:
#   docs/TAILSCALE-ACLS.md   — network-layer hardening (Constitution VI)
#   docs/CLEAN-MACHINE.md    — agent-host hygiene (Constitution I)
# Per-binary Keychain ACL contract (AC-6):
#   [child].command[0] is the binary path the operator ACL-binds in
#   Keychain. See docs/CLEAN-MACHINE.md §8 (macOS Keychain).
# Operator workflow:
#   1. Copy this file to ~/.hush/supervisors/<your-daemon>.toml
#   2. Find/replace every "REPLACE_ME" and "EXAMPLE_*" placeholder
#   3. Run: hush supervise --config ~/.hush/supervisors/<your-daemon>.toml
```

### Rationale

1. **Operator scannability.** A single grammar means an operator can
   skim the file in 30 seconds and know what's required versus
   defaulted.
2. **Avoids per-field doc-link repetition** (clarification 5).
3. **Surfaces every loader default in the file itself** — operators
   don't need to flip to CONFIG-SCHEMA.md for the common case.

### Alternatives considered

- **Inline doc-links per field.** Rejected per clarification 5.
- **Defaults listed only in CONFIG-SCHEMA.md.** Rejected per
  clarification 2 ("operators see every knob at once").
- **Block comments per section.** Rejected: section headers
  (`[child]`, `[discord]`, etc.) already group fields semantically;
  block comments would duplicate per-field comments.

---

## Cross-references

- `.specify/memory/constitution.md` Principle I (operator-agnostic),
  Principle VI (Tailscale-only), Security Requirements
  (Keychain ACLs).
- `docs/SPEC.md` FR-8 (Tailscale-only), FR-11 (supervise lifecycle),
  AC-6, AC-8, AC-10.
- `docs/CONFIG-SCHEMA.md` §Supervisor config (authoritative field
  set, defaults, validation rules).
- `docs/SECURITY.md` §2.3 Network (the Layer-0-equivalent network
  perimeter — TAILSCALE-ACLS.md verification cross-reference).
- `internal/supervise/config/{config,defaults,validate,errors}.go`
  (the SDD-18 loader implementation).
- `internal/supervise/config/testdata/valid_maximal.toml` (the
  canonical maximal fixture the template mirrors).
- `deploy/install.sh` (the SDD-29 installer the CLEAN-MACHINE.md
  verification cross-references).
- `docs/TAILSCALE-ACLS.md`, `docs/CLEAN-MACHINE.md` (verify-and-polish
  targets).
- `docs/DAEMONS.md` (operator workflow context).
