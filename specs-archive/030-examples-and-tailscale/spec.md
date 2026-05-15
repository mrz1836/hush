# Feature Specification: Generic Supervisor Example + Verified Operator Docs (SDD-30)

**Feature Branch**: `030-examples-and-tailscale`

**Created**: 2026-05-14

**Status**: Draft

**Input**: User description: "deploy/examples/supervisors/example-daemon.toml: fully commented, fully generic supervisor TOML using placeholder secret names; validates against SDD-18 loader; references docs/TAILSCALE-ACLS.md and docs/CLEAN-MACHINE.md in comments; both pre-existing docs re-verified for accuracy; zero operator-specific names anywhere"

## Overview

This chunk delivers the canonical operator-facing supervisor TOML template
(`deploy/examples/supervisors/example-daemon.toml`) AND re-validates two
pre-existing operator-agnostic documents (`docs/TAILSCALE-ACLS.md` and
`docs/CLEAN-MACHINE.md`) against the current spec state. The template
uses placeholder secret names (`EXAMPLE_API_KEY_1` etc.) so any operator
can copy it, find/replace to fit their daemon, and obtain a working
supervisor config without leaking any other operator's daemon identity
into their copy.

The feature is intentionally narrow: one new template file, two doc
verifications, and a loader-validation test. The chunk produces no new
Go runtime code; the only Go added is a test
(`TestExamples_GenericTOMLValidates`) that feeds the template through
the SDD-18 loader and asserts no validation error.

Constitutional principles in scope: **I** (operator-agnostic — zero
private daemon names, hostnames, or Tailscale tags anywhere in any
file delivered by this chunk) and **VI** (Tailscale-only network
boundary — the ACL guide referenced in the template is the operator's
network-layer reference).

Primary acceptance criteria: **AC-6** (Keychain ACL — the template's
client-key handling references the per-binary ACL contract), **AC-8**
(server hardening — the template's `server_url` points at a Tailscale
CGNAT address consistent with the bind validation), and **AC-10**
(supervisor lifecycle — the template exercises the full
`docs/CONFIG-SCHEMA.md` supervisor section so an operator can drive the
15 lifecycle scenarios with it).

## Clarifications

### Session 2026-05-14

- Q: What seed list of forbidden operator-specific identifiers ships with `TestExamples_NoOperatorSpecificNames`? → A: A small explicit list of historically-leaked terms — the original author's deployment-specific daemon names, hostnames, and project codenames — maintained as a one-line-per-addition Go slice in the test source. New forbidden identifiers are added one at a time as discoveries surface; the test source is the canonical list.
- Q: How are optional `docs/CONFIG-SCHEMA.md` Supervisor Config fields represented in the template? → A: Every field (required and optional) appears as an active example value, with an inline comment naming the loader default so operators see every knob at once and delete what they don't need.
- Q: What literal form does `server_url` take in the template? → A: A concrete CGNAT sample inside `100.64.0.0/10` (e.g., `https://100.64.0.1:7743/h/example`) so the SDD-18 loader's CIDR validation passes as-shipped; an inline comment marks it as a placeholder the operator MUST replace with their vault's real Tailscale IP before first boot.
- Q: How does the supervisor TOML template surface AC-6 (Keychain ACL — per-binary contract)? → A: A top-of-file comment block explicitly references the per-binary Keychain ACL contract, links to the relevant section of `docs/CLEAN-MACHINE.md` (or the Keychain-ACL doc), and calls out that `[child].command[0]` is the ACL-bound binary path. The template documents the linkage; it does not configure the ACL itself.
- Q: At what frequency does the template link to `docs/CONFIG-SCHEMA.md`? → A: A single explicit pointer at the top of the file (e.g., "Every field below is documented at `docs/CONFIG-SCHEMA.md#supervisor-config`"); per-field inline comments are purpose-only (one short sentence per field) and do not repeat the doc link.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Operator adopts hush for a new daemon (Priority: P1)

A new operator (anyone deploying hush against a daemon of their own) has
just finished `hush init` on the vault host and `hush init --client` on
the agent host. They want to add their first long-running daemon. They
open the public `hush` repository, find the canonical example, copy
`deploy/examples/supervisors/example-daemon.toml` to
`~/.hush/supervisors/<their-daemon>.toml`, find/replace the placeholders,
and launch `hush supervise --config <path>` without further reference
material. Every field in the file has an inline comment that explains
its purpose and points to the authoritative schema.

**Why this priority**: This is the *only* user story the chunk delivers
end-to-end. Without it, the open-source release cannot demonstrate the
documented supervisor model to operators who are not the original author.

**Independent Test**: A reviewer copies the template to a temporary
file, substitutes their own placeholder strings (no real secrets, no
real hostnames), runs the template through the SDD-18 loader, and
observes zero validation errors. The reviewer also confirms each field
in the template has an inline comment that links back to
`docs/CONFIG-SCHEMA.md` for the full spec.

**Acceptance Scenarios**:

1. **Given** the canonical template at
   `deploy/examples/supervisors/example-daemon.toml`, **when** an
   operator copies it verbatim and feeds it to the SDD-18 supervisor-
   config loader, **then** the loader returns no validation error.
2. **Given** the canonical template, **when** an operator opens it in
   an editor, **then** every field documented in
   `docs/CONFIG-SCHEMA.md` under the Supervisor Config section is
   present in the file, each annotated with an inline comment that
   states its purpose.
3. **Given** the canonical template, **when** an operator reads the
   top-of-file comment block, **then** the block links to both
   `docs/TAILSCALE-ACLS.md` and `docs/CLEAN-MACHINE.md` so the related
   network-layer and host-hygiene guidance is one click away.
4. **Given** the canonical template, **when** the entire repository
   is grepped for known operator-specific identifiers (any name from
   the original author's deployment, any private project codename),
   **then** zero matches appear in the template.

---

### User Story 2 - Operator verifies Tailscale ACL guidance is still current (Priority: P2)

An operator about to apply Tailscale ACLs reads
`docs/TAILSCALE-ACLS.md`. They want the document to (a) describe the
ACL pattern in operator-agnostic terms, (b) match the current
`docs/CONFIG-SCHEMA.md` `[network]` constraints, and (c) match the
current `docs/SECURITY.md` Layer 0 (network perimeter) statements.
Divergences would mean an operator applies an ACL the runtime no
longer expects.

**Why this priority**: AC-8 (server hardening) lists the Tailscale ACL
as a perimeter precondition. If the doc has drifted from
`docs/CONFIG-SCHEMA.md` or `docs/SECURITY.md`, the operator may
configure a perimeter that the runtime rejects (or worse — silently
relaxes). The chunk only ships green if the doc and schema agree.

**Independent Test**: A reviewer reads `docs/TAILSCALE-ACLS.md`
side-by-side with `docs/CONFIG-SCHEMA.md` `[network]` and
`docs/SECURITY.md` Layer 0, and confirms zero contradictions.
Divergences are recorded as patch-level edits to the doc and re-checked.

**Acceptance Scenarios**:

1. **Given** the current `docs/TAILSCALE-ACLS.md`, **when** a reviewer
   compares it against the current `[network] require_tailscale` /
   `allowed_cidrs` constraints in `docs/CONFIG-SCHEMA.md`, **then** the
   document's ACL pattern is consistent with the schema's bind rules
   (Tailscale-only, port 7743, default-deny on the perimeter).
2. **Given** the current `docs/TAILSCALE-ACLS.md`, **when** a reviewer
   compares its tag-naming examples against the current Tailscale
   conventions, **then** the document uses only generic placeholder tag
   names (`tag:hush-agent`, `tag:hush-vault`) with an explicit note
   that operators substitute their own names.
3. **Given** the current `docs/TAILSCALE-ACLS.md`, **when** the entire
   document is grepped for operator-specific identifiers, **then** zero
   matches appear.

---

### User Story 3 - Operator verifies clean-machine checklist is still current (Priority: P2)

An operator about to deploy a hush agent on a new machine reads
`docs/CLEAN-MACHINE.md` to remove pre-existing on-disk secrets. They
want the checklist's install-time steps to match `deploy/install.sh`
(delivered by SDD-29) and the dotfile/keychain hygiene steps to remain
operator-agnostic. Divergences would mean the checklist tells the
operator to remove a file the installer just put back, or vice versa.

**Why this priority**: Constitution Principle I is enforced by this
checklist. If the checklist drifts from the installer, Principle I is
unenforceable in practice. Same rationale as Story 2: ship-blocking if
the doc no longer matches the runtime/installer.

**Independent Test**: A reviewer reads `docs/CLEAN-MACHINE.md`
side-by-side with `deploy/install.sh` (SDD-29 output) and confirms
every install-time step the checklist describes matches the installer's
actual behaviour. Divergences are recorded as patch-level edits to the
doc and re-checked.

**Acceptance Scenarios**:

1. **Given** the current `docs/CLEAN-MACHINE.md`, **when** a reviewer
   walks through each section, **then** every step is operator-agnostic
   (no specific operator's dotfile contents, no specific keychain entry
   names beyond the documented `hush`, `hush-discord`, `hush-client`).
2. **Given** the current `docs/CLEAN-MACHINE.md`, **when** a reviewer
   compares its install-time guidance with `deploy/install.sh`
   (SDD-29), **then** the install-time steps in the checklist match
   the installer's behaviour with zero contradictions.
3. **Given** the current `docs/CLEAN-MACHINE.md`, **when** the entire
   document is grepped for operator-specific identifiers, **then**
   zero matches appear.

---

### Edge Cases

- **Operator copies the template, leaves a placeholder in place, and
  starts hush.** The SDD-18 loader rejects the placeholder (e.g.,
  `REPLACE_ME` is not a valid Discord snowflake; `EXAMPLE_API_KEY_1`
  is not in the vault) with a clear startup error. The template's
  comments must call out which placeholders MUST be substituted before
  first boot so this fails fast.
- **Operator copies the template into a public fork.** Because the
  template contains zero operator-specific identifiers, no follow-on
  leakage occurs. The operator's substitutions land in *their*
  fork, not in the public template.
- **Future Tailscale tag-naming convention change.** The doc uses the
  current `tag:hush-agent` / `tag:hush-vault` pattern and explicitly
  notes that operators substitute their own tag names — the *pattern*
  is the load-bearing part, not the specific names. A future tag
  convention change requires a doc-only PATCH; this chunk locks the
  current convention.
- **Future installer change (SDD-29) drifts from the checklist.** Any
  later PR that edits `deploy/install.sh` MUST update
  `docs/CLEAN-MACHINE.md` in the same PR, or the post-merge
  consistency audit will fail. This chunk does not add a CI gate for
  that — it only re-verifies the current state.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The repository MUST contain a canonical operator-facing
  supervisor TOML template at the exact path
  `deploy/examples/supervisors/example-daemon.toml`.
- **FR-002**: The template MUST populate every field documented in
  `docs/CONFIG-SCHEMA.md` under the "Supervisor config" section
  (required and optional fields alike), so a copying operator does not
  need to consult other sources to enumerate available knobs. Every
  field — required and optional alike — MUST appear as an active
  example value (not commented-out). Optional fields MUST also carry
  an inline comment that names the loader's default value so an
  operator can see at a glance what they would be overriding.
- **FR-003**: Every field in the template MUST carry an inline
  comment that states the field's purpose in one short sentence. The
  pointer to `docs/CONFIG-SCHEMA.md` for the authoritative schema
  MUST appear exactly once, in the top-of-file comment block (e.g.,
  "Every field below is documented at
  `docs/CONFIG-SCHEMA.md#supervisor-config`"); per-field comments
  are purpose-only and do not repeat the schema link.
- **FR-004**: The template MUST use generic placeholder strings —
  including but not limited to `EXAMPLE_API_KEY_1`, `EXAMPLE_API_KEY_2`,
  `example-daemon`, `your-daemon-binary`, and a clearly-marked
  `REPLACE_ME` pattern for fields that have no safe default (e.g.,
  Discord IDs).
- **FR-005**: The template MUST validate cleanly against the SDD-18
  supervisor-config loader as-shipped. A test
  (`TestExamples_GenericTOMLValidates`) MUST exercise this validation
  and assert zero error.
- **FR-006**: The template MUST contain a top-of-file comment block
  that links to `docs/TAILSCALE-ACLS.md` (for network-layer
  hardening) and `docs/CLEAN-MACHINE.md` (for agent-host hygiene), so
  the related operator-facing guidance is discoverable from the
  template itself. The same top-of-file comment block MUST also
  explicitly reference the per-binary Keychain ACL contract (AC-6),
  link to the Keychain-ACL section of `docs/CLEAN-MACHINE.md` (or
  the dedicated Keychain-ACL doc if one exists), and call out that
  `[child].command[0]` is the ACL-bound binary path. The template
  documents this linkage; it does not configure the ACL itself.
- **FR-007**: The template MUST NOT contain any operator-specific
  identifier — no private daemon name, no private hostname, no
  private Tailscale tag, no private project codename. A separate
  test (`TestExamples_NoOperatorSpecificNames`, delivered in the
  tasks phase) MUST grep the file for known forbidden patterns and
  assert zero matches. The test ships with a small explicit seed
  list of historically-leaked terms (the original author's
  deployment-specific daemon names, hostnames, and project
  codenames), maintained as a one-line-per-addition Go slice
  literal in the test source; new forbidden identifiers are added
  one at a time as discoveries surface, and the test source is the
  canonical list (see Assumptions).
- **FR-008**: The `server_url` value in the template MUST point at a
  documented Tailscale CGNAT address (within `100.64.0.0/10`) and use
  the canonical `/h/<prefix>` path shape, so the example is consistent
  with the AC-8 bind validation. The shipped value MUST be a concrete
  CGNAT literal inside `100.64.0.0/10` (e.g.,
  `https://100.64.0.1:7743/h/example`) so the SDD-18 loader's CIDR
  validation passes as-shipped without operator edits; an inline
  comment MUST flag the literal as a placeholder the operator
  replaces with their vault's real Tailscale IP before first boot.
- **FR-009**: `docs/TAILSCALE-ACLS.md` (already present in the
  repository) MUST be re-verified against the current
  `docs/CONFIG-SCHEMA.md` `[network]` section and the current
  `docs/SECURITY.md` Layer 0 statements. Any divergence MUST be
  resolved by editing the document, not by editing the schema or
  security model.
- **FR-010**: `docs/CLEAN-MACHINE.md` (already present in the
  repository) MUST be re-verified against the current
  `deploy/install.sh` (SDD-29 output). Any divergence MUST be resolved
  by editing the document, not by editing the installer.
- **FR-011**: Neither `docs/TAILSCALE-ACLS.md` nor
  `docs/CLEAN-MACHINE.md` may contain any operator-specific identifier
  after the verification pass.

### Key Entities *(include if feature involves data)*

- **Supervisor TOML template**: A static `.toml` file at
  `deploy/examples/supervisors/example-daemon.toml`. It is documentation
  written in TOML syntax — every field is an example, every comment is
  guidance, the file is not loaded by any runtime path. It is shipped
  as part of the repository, not as a runtime artefact.
- **Operator-agnostic placeholder**: Any identifier in the template
  whose only purpose is to be replaced by the operator. Placeholders
  fall in three categories: (a) human-readable example slugs
  (`example-daemon`, `your-daemon-binary`), (b) scoped secret names
  (`EXAMPLE_API_KEY_1`, `EXAMPLE_API_KEY_2`), and (c) `REPLACE_ME`
  markers for fields with no safe default (e.g., Discord snowflakes).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A reviewer who copies
  `deploy/examples/supervisors/example-daemon.toml` to a fresh path
  and feeds it to the SDD-18 supervisor-config loader observes zero
  validation errors on first attempt.
- **SC-002**: Every field documented in `docs/CONFIG-SCHEMA.md` under
  the "Supervisor config" section appears at least once in the
  template (verified by side-by-side field census).
- **SC-003**: A grep of the template for known operator-specific
  identifiers returns zero matches; the
  `TestExamples_NoOperatorSpecificNames` test passes.
- **SC-004**: The top-of-file comment block in the template contains
  at least one explicit link to each of
  `docs/TAILSCALE-ACLS.md` and `docs/CLEAN-MACHINE.md`, plus an
  explicit reference to the per-binary Keychain ACL contract (AC-6)
  and a note that `[child].command[0]` is the ACL-bound binary path.
- **SC-005**: A reviewer cross-checking `docs/TAILSCALE-ACLS.md`
  against `docs/CONFIG-SCHEMA.md` `[network]` and `docs/SECURITY.md`
  Layer 0 records zero contradictions.
- **SC-006**: A reviewer cross-checking `docs/CLEAN-MACHINE.md`
  against `deploy/install.sh` records zero contradictions.
- **SC-007**: A grep of `docs/TAILSCALE-ACLS.md` and
  `docs/CLEAN-MACHINE.md` for known operator-specific identifiers
  returns zero matches in either file.
- **SC-008**: `magex format:fix && magex lint && magex test:race` all
  pass clean on the branch containing the new template and any doc
  edits.

## Assumptions

- The SDD-18 supervisor-config loader at
  `internal/supervise/config` is the authoritative validator for the
  template; the test harness uses its public `Load` function.
- `docs/CONFIG-SCHEMA.md` (Phase 0) is the source of truth for which
  fields the template must populate. If the schema changes after this
  chunk lands, the template is updated by the chunk that changes the
  schema, not retro-actively.
- `deploy/install.sh` (SDD-29) is the source of truth for install-time
  behaviour. `docs/CLEAN-MACHINE.md` is updated to match the installer
  if they diverge, not the reverse.
- Both `docs/TAILSCALE-ACLS.md` and `docs/CLEAN-MACHINE.md` already
  exist in the repository. This chunk only re-verifies them; it does
  not rewrite them from scratch.
- The placeholder convention used by the template
  (`EXAMPLE_API_KEY_1`, `REPLACE_ME`, etc.) is shared by future
  operator-facing examples. A separate chunk MAY introduce additional
  example files; this chunk locks the *first* such file.
- The list of "known operator-specific identifiers" used by
  `TestExamples_NoOperatorSpecificNames` is a small, well-defined
  list maintained alongside the test (the test source is the
  source of truth for the list). Adding a new forbidden identifier
  is a one-line test edit.
- The chunk produces no new runtime code paths and therefore has no
  coverage target. AC-9 (coverage + fuzz) is unaffected by this chunk.
