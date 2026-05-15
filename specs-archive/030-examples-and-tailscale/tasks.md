# Tasks: Generic Supervisor Example + Verified Operator Docs (SDD-30)

**Input**: Design documents from `/specs/030-examples-and-tailscale/`

**Prerequisites**: plan.md (loaded), spec.md (loaded), research.md (loaded), data-model.md (loaded), contracts/ (loaded — template-field-census.md, tailscale-acls-verification.md, clean-machine-verification.md), quickstart.md (loaded)

**Tests**: Tests are REQUIRED for this chunk. The chunk-doc, spec FR-005/FR-007, and the /speckit-tasks invocation all mandate two tests: `TestExamples_GenericTOMLValidates` (test-first, RED before the template exists, GREEN after) and `TestExamples_NoOperatorSpecificNames` (Polish-phase grep gate). Both live in `internal/supervise/config/example_test.go`.

**Organization**: Three user stories from spec.md drive three implementation phases. US1 (P1) is the MVP — the template + its validation test. US2 (P2) verifies docs/TAILSCALE-ACLS.md. US3 (P2) verifies docs/CLEAN-MACHINE.md. US2 and US3 are independent of US1 and of each other (different files).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- Every task description includes the exact file path

## Path Conventions

- Single Go module + repo-level documentation tree (per plan.md §Project Structure).
- Template ships at `deploy/examples/supervisors/example-daemon.toml`.
- Validation test is package-co-located at `internal/supervise/config/example_test.go`.
- Doc edits at `docs/TAILSCALE-ACLS.md` and `docs/CLEAN-MACHINE.md`.
- Deferred doc updates at `docs/PACKAGE-MAP.md`, `docs/AC-MATRIX.md`, `docs/SDD-PLAYBOOK.md` (per chunk-doc Prompt 5 steps 6–8).

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Confirm the repo state matches plan.md assumptions before any test or file is written.

- [X] T001 Ensure the target directory `deploy/examples/supervisors/` exists (create if missing) so [example_test.go](internal/supervise/config/example_test.go) and [example-daemon.toml](deploy/examples/supervisors/example-daemon.toml) can land at the planned paths.

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: None for this chunk. This is a config + docs chunk with no shared runtime infrastructure between user stories. US1, US2, and US3 are independently implementable after Setup completes.

**Checkpoint**: Foundation trivially ready — user story implementation can begin.

---

## Phase 3: User Story 1 — Operator adopts hush via canonical template (Priority: P1) 🎯 MVP

**Goal**: Ship `deploy/examples/supervisors/example-daemon.toml` as the canonical operator-facing supervisor template, guarded by the SDD-18 loader round-trip test `TestExamples_GenericTOMLValidates`.

**Independent Test**: Run `go test ./internal/supervise/config/ -run TestExamples_GenericTOMLValidates` — it must pass. Open the template and confirm every CONFIG-SCHEMA.md §Supervisor-config field is present with an inline comment per [contracts/template-field-census.md](specs/030-examples-and-tailscale/contracts/template-field-census.md).

### Tests for User Story 1 ⚠️ (write FIRST — must FAIL before T003)

> **NOTE**: Write T002 against a not-yet-existing template file. Confirm RED (the test fails because the path doesn't resolve). Then T003 writes the template; T004 confirms the test goes GREEN.

- [X] T002 [US1] Create [internal/supervise/config/example_test.go](internal/supervise/config/example_test.go) with `TestExamples_GenericTOMLValidates`. Test contract per [contracts/template-field-census.md](specs/030-examples-and-tailscale/contracts/template-field-census.md) §Validation test contract: package `config`, no build tag, `t.Parallel()`, path is `filepath.Join("..","..","..","deploy","examples","supervisors","example-daemon.toml")` (three `..` levels: config → supervise → internal → repo-root), calls `Load(context.Background(), path)`, asserts `require.NoError`, `require.NotNil(sup)`, `assert.Equal("example-daemon", sup.Name)`, `assert.Equal("supervisor", sup.SessionType)`. Run `go test ./internal/supervise/config/ -run TestExamples_GenericTOMLValidates` and confirm it FAILS (the file doesn't exist yet — RED).

### Implementation for User Story 1

- [X] T003 [US1] Write [deploy/examples/supervisors/example-daemon.toml](deploy/examples/supervisors/example-daemon.toml) per the field census in [contracts/template-field-census.md](specs/030-examples-and-tailscale/contracts/template-field-census.md) and the structure described in [data-model.md](specs/030-examples-and-tailscale/data-model.md) §§2–5. File structure: (a) top-of-file comment block per data-model §3 (5 sub-blocks: what-this-is, CONFIG-SCHEMA.md anchor, companion-docs TAILSCALE-ACLS.md + CLEAN-MACHINE.md, Keychain-ACL contract AC-6 + `[child].command[0]` callout, three-step operator workflow); (b) Block 1 — 14 root scalars in census order (rows 1–14, EXCLUDING `audit_log`); (c) Block 2 — `scope = ["EXAMPLE_API_KEY_1", "EXAMPLE_API_KEY_2"]`; (d) Block 3 — `[child]` with `command = ["/usr/local/bin/your-daemon-binary", "start"]`, `working_dir = "/tmp"`, `env_passthrough = ["PATH","HOME","SHELL"]`, `restart_on_clean_exit = true`, `restart_on_exit_78 = false`; (e) Block 4 — `[discord]` with `daemon_label = "Example Daemon"`, `alert_channel_id = "REPLACE_ME"`; (f) Block 5 — `[validators]` with `EXAMPLE_API_KEY_1 = "anthropic"`, `EXAMPLE_API_KEY_2 = "openai"`; (g) Block 6 — `[watchdog]` with `enabled = true`, `patterns = ["401 Unauthorized", "No API key found", "invalid x-api-key"]`, `max_alerts_per_hour = 6`. `server_url = "http://100.64.0.1:7743/h/example"` per [research.md](specs/030-examples-and-tailscale/research.md) R-004. Every field carries an inline comment per data-model §4 grammar (Required → `# <purpose>. Required.`; Optional → `# <purpose>. Default: <loader-default>.`). UTF-8, LF line endings, no BOM, mode 0644.
- [X] T004 [US1] Re-run `go test ./internal/supervise/config/ -run TestExamples_GenericTOMLValidates` and confirm it PASSES (GREEN). If it fails, the failure mode points at T003's value choices, not the test — re-check against [contracts/template-field-census.md](specs/030-examples-and-tailscale/contracts/template-field-census.md).

**Checkpoint**: At this point, User Story 1 is fully functional and testable independently. The MVP — operator can copy the template, find/replace placeholders, and the SDD-18 loader accepts it.

---

## Phase 4: User Story 2 — Operator verifies Tailscale ACL guidance is still current (Priority: P2)

**Goal**: Apply the five patches in [contracts/tailscale-acls-verification.md](specs/030-examples-and-tailscale/contracts/tailscale-acls-verification.md) §Patch specification (R-002) to `docs/TAILSCALE-ACLS.md` so its tag-pair examples align with constitution-VI / SECURITY.md §2.3 / SPEC.md FR-8. After patching, every row in the audit table reads `OK`.

**Independent Test**: After patches apply, manually compare `docs/TAILSCALE-ACLS.md` against `docs/SECURITY.md` §2.3 + `docs/CONFIG-SCHEMA.md` `[network]` + `docs/SPEC.md` FR-8. Confirm zero contradictions per [contracts/tailscale-acls-verification.md](specs/030-examples-and-tailscale/contracts/tailscale-acls-verification.md) §Final verification gate. SC-005 + SC-007 must both pass.

### Implementation for User Story 2

> **NOTE**: All five patches edit the same file (`docs/TAILSCALE-ACLS.md`) — they are SEQUENTIAL, not parallel. Apply in order.

- [X] T005 [US2] Apply Patch 1 to [docs/TAILSCALE-ACLS.md](docs/TAILSCALE-ACLS.md) §"The pattern" opening per [contracts/tailscale-acls-verification.md](specs/030-examples-and-tailscale/contracts/tailscale-acls-verification.md) §Patch 1. Rewrite the two-tag preamble + grant line to lead with the canonical `tag:trusted → tag:sandbox:7743` pair and present `tag:hush-agent → tag:hush-vault` as a descriptive operator alternative; close the paragraph with "Either tag-pair satisfies Constitution Principle VI as long as the grant is scoped to port 7743 and the source-tagged set is exactly the set of authorised agents."
- [X] T006 [US2] Apply Patch 2 to [docs/TAILSCALE-ACLS.md](docs/TAILSCALE-ACLS.md) §"Example ACL JSON" per [contracts/tailscale-acls-verification.md](specs/030-examples-and-tailscale/contracts/tailscale-acls-verification.md) §Patch 2. Update the hujson example block: `tagOwners` keys become `tag:trusted` + `tag:sandbox` (primary) with the descriptive alternative shown as a comment; `acls` rule's `src` uses `["tag:trusted"]` and `dst` uses `["tag:sandbox:7743"]`.
- [X] T007 [US2] Apply Patch 3 to [docs/TAILSCALE-ACLS.md](docs/TAILSCALE-ACLS.md) §"Before / after diff" hunks per [contracts/tailscale-acls-verification.md](specs/030-examples-and-tailscale/contracts/tailscale-acls-verification.md) §Patch 3. Replace `tag:hush-agent` / `tag:hush-vault` with `tag:trusted` / `tag:sandbox` in BOTH diff hunks (the default-allow and the default-deny examples).
- [X] T008 [US2] Apply Patch 4 to [docs/TAILSCALE-ACLS.md](docs/TAILSCALE-ACLS.md) §"Applying the tags" per [contracts/tailscale-acls-verification.md](specs/030-examples-and-tailscale/contracts/tailscale-acls-verification.md) §Patch 4. Replace the two tag bullets with the canonical-plus-alternative phrasing: "The vault host: tag `sandbox` (canonical) or `hush-vault` (descriptive alternative)." / "Each agent machine ...: tag `trusted` (canonical) or `hush-agent` (descriptive alternative)."
- [X] T009 [US2] Apply Patch 5 to [docs/TAILSCALE-ACLS.md](docs/TAILSCALE-ACLS.md) §"Tightening further" → "Per-agent restriction" per [contracts/tailscale-acls-verification.md](specs/030-examples-and-tailscale/contracts/tailscale-acls-verification.md) §Patch 5. Replace "Replace `tag:hush-agent` with one tag per agent machine (`tag:hush-agent-<machine-name>`)" with "Replace the canonical source tag with one tag per agent machine (e.g., `tag:trusted-<machine-name>` or `tag:hush-agent-<machine-name>`)".
- [X] T010 [US2] Manual cross-check on [docs/TAILSCALE-ACLS.md](docs/TAILSCALE-ACLS.md) post-patch: confirm SECURITY.md §2.3 tag pair (`tag:trusted` / `tag:sandbox`) appears as primary; confirm port 7743 + CIDR `100.64.0.0/10` claims match CONFIG-SCHEMA.md `[network]`; grep TAILSCALE-ACLS.md for operator-specific identifiers (FR-011) and confirm zero matches. Report any residual divergence in the task's completion note. Satisfies SC-005 + SC-007.

**Checkpoint**: User Story 2 complete. TAILSCALE-ACLS.md aligned with constitution-VI / SECURITY.md / CONFIG-SCHEMA.md / SPEC.md. AC-8 perimeter doc is internally consistent.

---

## Phase 5: User Story 3 — Operator verifies clean-machine checklist is still current (Priority: P2)

**Goal**: Apply the one patch in [contracts/clean-machine-verification.md](specs/030-examples-and-tailscale/contracts/clean-machine-verification.md) §Patch specification (R-003) to `docs/CLEAN-MACHINE.md` §8 (macOS Keychain) so its hush-managed-entries bullet enumerates the three canonical entries (`hush-vault-passphrase`, `hush-discord`, `hush-client`) matching `deploy/install.sh`'s banner output verbatim.

**Independent Test**: After patch applies, manually compare `docs/CLEAN-MACHINE.md` against `deploy/install.sh` (SDD-29). Confirm zero contradictions per [contracts/clean-machine-verification.md](specs/030-examples-and-tailscale/contracts/clean-machine-verification.md) §Final verification gate. SC-006 + SC-007 must both pass.

### Implementation for User Story 3

- [X] T011 [P] [US3] Apply Patch 1 to [docs/CLEAN-MACHINE.md](docs/CLEAN-MACHINE.md) §8 "macOS Keychain" "hush-managed entries" bullet per [contracts/clean-machine-verification.md](specs/030-examples-and-tailscale/contracts/clean-machine-verification.md) §Patch 1. Replace the single-bullet description with a three-sub-bullet enumeration: `hush-vault-passphrase` (vault passphrase, vault host, created via the security command in install.sh's banner), `hush-discord` (Discord bot token, vault host, referenced by `[discord].bot_token_keychain_item`), `hush-client` (per-machine client-key marker, each agent host, created by `hush init --client --machine-index N`). Reference `deploy/install.sh`'s next-steps banner for the exact `security add-generic-password` invocation. Keep the "Tool-specific entries" bullet unchanged. (Note: parallel-safe with US2 because the edit targets a different file.)
- [X] T012 [US3] Manual cross-check on [docs/CLEAN-MACHINE.md](docs/CLEAN-MACHINE.md) post-patch: confirm the three entry names match `deploy/install.sh` banner output exactly (`hush-vault-passphrase`, `hush-discord`, `hush-client`); confirm the constitution Security Requirements "Keychain ACLs (macOS)" row remains correct as written (no constitution edit per scope); grep CLEAN-MACHINE.md for operator-specific identifiers (FR-011) and confirm zero matches. Report any residual divergence in the task's completion note. Satisfies SC-006 + SC-007.

**Checkpoint**: User Story 3 complete. CLEAN-MACHINE.md §8 aligned with the SDD-29 installer.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Add the FR-007 grep gate test, the deferred doc cross-references (PACKAGE-MAP / AC-MATRIX / SDD-PLAYBOOK per chunk-doc Prompt 5 steps 6–8), and run the mandatory final gates.

- [X] T013 Add `TestExamples_NoOperatorSpecificNames` and the package-private `operatorSpecificForbidden` slice to [internal/supervise/config/example_test.go](internal/supervise/config/example_test.go) per FR-007 + clarification 1. Test: `t.Parallel()`, reads the template via the same relative path as T002, asserts that NO entry in `operatorSpecificForbidden` appears anywhere in the template's bytes. Seed list: a one-line-per-addition Go slice literal in the test source (the canonical list) — start empty if no historically-leaked identifiers are known to the planner; new forbidden identifiers are added one at a time as discoveries surface. Run `go test ./internal/supervise/config/ -run TestExamples_NoOperatorSpecificNames` and confirm it PASSES. Satisfies SC-003.
- [X] T014 [P] Extend the `deploy/` entry in [docs/PACKAGE-MAP.md](docs/PACKAGE-MAP.md) with an "Exported API — locked at SDD-30" note that names `deploy/examples/supervisors/example-daemon.toml` as the canonical operator-facing supervisor template, cross-references CONFIG-SCHEMA.md §Supervisor-config, and points to docs/TAILSCALE-ACLS.md + docs/CLEAN-MACHINE.md as companion docs. (Chunk-doc Prompt 5 step 6.)
- [X] T015 [P] Update [docs/AC-MATRIX.md](docs/AC-MATRIX.md) AC-6, AC-8, AC-10 rows to reference the new file path `deploy/examples/supervisors/example-daemon.toml` (AC-6: top-of-file Keychain-ACL block + `[child].command[0]` callout; AC-8: CGNAT `server_url` literal; AC-10: full-schema population enables 15-scenario exercise). (Chunk-doc Prompt 5 step 7.)
- [X] T016 [P] Mark SDD-30 status `done` in [docs/SDD-PLAYBOOK.md](docs/SDD-PLAYBOOK.md). (Chunk-doc Prompt 5 step 8.)
- [ ] T017 Gate: run `magex format:fix` from the repo root and confirm clean (no diff after running). (Chunk-doc Prompt 5 step 1, part 1; SC-008.)
- [ ] T018 Gate: run `magex lint` from the repo root and confirm clean (zero lint findings). (Chunk-doc Prompt 5 step 1, part 2; SC-008.)
- [ ] T019 Gate: run `magex test:race` from the repo root and confirm all tests pass (specifically including `TestExamples_GenericTOMLValidates` and `TestExamples_NoOperatorSpecificNames`). (Chunk-doc Prompt 5 step 1, part 3; SC-008.)

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — can start immediately.
- **Foundational (Phase 2)**: Trivially complete (no foundational work in this chunk).
- **US1 (Phase 3)**: Depends on Phase 1.
- **US2 (Phase 4)**: Depends on Phase 1 — independent of US1 (different file: `docs/TAILSCALE-ACLS.md`).
- **US3 (Phase 5)**: Depends on Phase 1 — independent of US1 and US2 (different file: `docs/CLEAN-MACHINE.md`).
- **Polish (Phase 6)**: T013 depends on US1 (T002 creates the test file T013 extends). T014, T015, T016 depend on all of US1+US2+US3 (they reference outputs of all three). T017–T019 (gates) depend on ALL prior tasks.

### User Story Dependencies

- US1 (P1): Self-contained — owns the template and its validation test.
- US2 (P2): Self-contained — doc-only edits to TAILSCALE-ACLS.md.
- US3 (P2): Self-contained — doc-only edit to CLEAN-MACHINE.md.

### Within User Story 1

- T002 (test, RED) MUST be written and verified failing BEFORE T003 (template).
- T004 (test, GREEN) MUST be verified passing AFTER T003.

### Within User Story 2

- T005 → T006 → T007 → T008 → T009 are SEQUENTIAL (same file: `docs/TAILSCALE-ACLS.md`).
- T010 (cross-check) runs after T005–T009.

### Within User Story 3

- T011 (the single patch) and T012 (cross-check) are sequential.

### Parallel Opportunities

- US2 (T005–T010) and US3 (T011–T012) can run in parallel — different files (`docs/TAILSCALE-ACLS.md` vs. `docs/CLEAN-MACHINE.md`). T011 is marked `[P]` because it is parallel-safe with US2's patches.
- T014, T015, T016 in Polish can run in parallel — different files (`docs/PACKAGE-MAP.md`, `docs/AC-MATRIX.md`, `docs/SDD-PLAYBOOK.md`).
- T017–T019 (gates) must run sequentially in the listed order (format → lint → test), though `format:fix` is idempotent enough that re-running it is cheap.

---

## Parallel Example: US2 + US3 (after US1 completes)

```bash
# Launch US2 and US3 doc-edits in parallel:
Task: "Apply Patch 1 to docs/TAILSCALE-ACLS.md §The pattern (T005)"
Task: "Apply Patch 1 to docs/CLEAN-MACHINE.md §8 hush-managed entries (T011)"
```

```bash
# Launch Polish doc cross-references in parallel:
Task: "Extend deploy/ entry in docs/PACKAGE-MAP.md (T014)"
Task: "Update AC-6/8/10 rows in docs/AC-MATRIX.md (T015)"
Task: "Mark SDD-30 done in docs/SDD-PLAYBOOK.md (T016)"
```

---

## Implementation Strategy

### MVP First (User Story 1 only)

1. Complete Phase 1: Setup (T001).
2. Phase 2: Foundational — trivially complete.
3. Complete Phase 3: User Story 1 (T002 RED → T003 → T004 GREEN).
4. **STOP and VALIDATE**: Run `go test ./internal/supervise/config/ -run TestExamples_GenericTOMLValidates` and confirm GREEN. The canonical operator-facing template now exists and validates.

### Incremental Delivery

1. Setup + US1 → MVP demo (template + validation test).
2. Add US2 → TAILSCALE-ACLS.md verified against constitution-VI / SECURITY.md / CONFIG-SCHEMA.md / SPEC.md.
3. Add US3 → CLEAN-MACHINE.md verified against deploy/install.sh.
4. Polish phase → grep gate, doc cross-references, final gates.
5. Single combined commit at the end of /speckit-implement per chunk-doc Prompt 5.

### Parallel Team Strategy

After Setup:
- Developer A: US1 (template + validation test).
- Developer B: US2 (TAILSCALE-ACLS.md patches).
- Developer C: US3 (CLEAN-MACHINE.md patch).

All three converge in Polish — T013 (grep gate) requires US1's example_test.go; T014–T016 (deferred doc cross-refs) reference all three; T017–T019 (gates) require everything.

---

## Notes

- [P] tasks = different files, no dependencies.
- [Story] label maps task to specific user story (US1, US2, US3) for traceability.
- Each user story is independently completable and testable.
- Verify T002 fails (RED) before writing T003; verify T004 passes (GREEN) after T003.
- No commits between phases per chunk-doc — single combined commit at the end of /speckit-implement.
- Avoid: editing the SDD-18 loader (would re-open SDD-18), editing the constitution (out of scope per research.md R-002), editing `deploy/install.sh` (out of scope per FR-010 / research.md R-003), shipping the user-prompt "locked HOW" snippet's field forms (would break FR-005 per research.md R-001).
- The seed list of forbidden identifiers in T013 may start empty per FR-007 / clarification 1 — entries are added one at a time as discoveries surface.
- `audit_log` is intentionally absent from the template per data-model §6 — the loader auto-defaults it relative to `pid_file` dirname; the `pid_file` inline comment documents the auto-default for operator discoverability.
- `magex` is the project's task runner; `format:fix`, `lint`, and `test:race` are pre-existing targets (no setup work for the gates).
