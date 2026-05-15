---
description: "Task list for SDD-29 Deploy Artifacts"
---

# Tasks: SDD-29 Deploy Artifacts (launchd plist + systemd unit + install.sh + supervisor launcher template)

**Input**: Design documents from [`specs/029-deploy-artifacts/`](.)

**Prerequisites**: [plan.md](plan.md), [spec.md](spec.md), [research.md](research.md), [data-model.md](data-model.md), [contracts/](contracts/), [quickstart.md](quickstart.md)

**Tests**: REQUIRED per the user's explicit task-phase instruction and per FR-025 / FR-026. These are deploy artifacts, not Go production code — there is no TDD-on-business-logic. The "tests" here are smoke checks (idempotency, XML parse, INI parse, supervisor-line grep, bash-n parse, no-operator-specific-names grep) that MUST be written BEFORE the artifacts they validate. Every test starts red (no artefact yet → fail) and turns green when the corresponding artefact is committed.

**Organization**: Tasks are grouped by the three user stories from [spec.md](spec.md). US1 and US2 are both P1 (independently delivering the v0.1.0 operator-facing surface). US3 is P2 (idempotency re-run discipline).

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: Which user story this task belongs to (US1, US2, US3)
- Every task lists exact file path(s) so it is immediately executable

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Lay the two new top-level directories the chunk introduces. No build-system changes, no new Go dependencies (Constitution XI lock).

- [X] T001 [P] Create `deploy/` directory at repo root (already exists if `deploy/examples/supervisors/` is present — verify with `ls deploy/`; otherwise `mkdir -p deploy/`)
- [X] T002 [P] Create `tests/deploy/` directory and `tests/deploy/testdata/` subdirectory at repo root (`mkdir -p tests/deploy/testdata`)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Test fixtures that every later test depends on. These are stubs and zero-byte files — they encode no business logic but the integration tests cannot run without them.

**⚠️ CRITICAL**: All US1 / US2 / US3 test tasks read these fixtures. Complete Phase 2 before any test task in Phase 3+.

- [X] T003 [P] Create [tests/deploy/testdata/tmutil_stub.sh](../../tests/deploy/testdata/tmutil_stub.sh) — recording shim per [research.md §R-006](research.md) + [data-model.md §6](data-model.md). Body: `#!/usr/bin/env bash` + `set -euo pipefail` + `printf '%s\n' "$*" >> "${TMUTIL_LOG:-/tmp/tmutil.log}"` + `exit 0`. Mode 0755. The Go test prepends this file's parent directory to PATH so `tmutil` resolves here.
- [X] T004 [P] Create [tests/deploy/testdata/fake-hush](../../tests/deploy/testdata/fake-hush) — zero-byte file used as `HUSH_SOURCE_BIN` in `TestDeploy_InstallIdempotent`. Mode 0755 (must be executable so `install -m 0755` does not balk).

**Checkpoint**: Fixtures present — US1/US2/US3 test tasks can now be authored and will fail in the expected pre-implementation way.

---

## Phase 3: User Story 1 - First-time install on a fresh trusted host (Priority: P1) 🎯 MVP

**Goal**: Deliver `install.sh` + `hush.plist` + `hush.service` so a fresh operator runs one command to lay down the binary, the service file, the state directory (with `0700` ownership), the macOS Time Machine exclusion, and the next-steps banner — and `install.sh` creates ZERO Keychain entries (FR-003 absolute lock).

**Independent Test**: `magex test:race -tags=integration -run TestDeploy_InstallIdempotent ./tests/deploy/...` green. Plus `TestDeploy_InstallRefusesUnsupportedOS`, `TestDeploy_InstallRefusesMissingBinary`, `TestDeploy_InstallRefusesMissingTmutil`, `TestDeploy_InstallBannerByteIdentical`, `TestDeploy_PlistParsesAsXML`, `TestDeploy_ServiceParsesAsINI` all green.

### Tests for User Story 1 (write FIRST — these MUST exist before any artefact is committed)

> Per [contracts/test-harness.md](contracts/test-harness.md) and the user's explicit task-phase instruction. The Go test file may host multiple `TestDeploy_*` functions; T005 creates the file with the idempotency test; T006–T009 add sibling test functions in the same file (sequential, same file → no `[P]`). T010 and T011 live in a different file (`smoke_test.go`) so they can run in parallel with each other.

- [X] T005 [US1] Create [tests/deploy/install_test.go](../../tests/deploy/install_test.go) with `//go:build integration` header and `TestDeploy_InstallIdempotent` per [contracts/test-harness.md §install_test.go](contracts/test-harness.md). Imports: stdlib only (`testing`, `os`, `os/exec`, `path/filepath`, `runtime`, `strings`, `bytes`, `bufio`). Body: `t.TempDir()` → stage `tmutil_stub.sh` as `${stubDir}/tmutil` → run `deploy/install.sh` twice with augmented PATH + `HUSH_INSTALL_ROOT=${tmp}` + `HUSH_SOURCE_BIN=tests/deploy/testdata/fake-hush` + `TMUTIL_LOG=${tmp}/tmutil.log` → assert both runs exit 0, `bytes.Equal(stdout1, stdout2)`, recursive tree+modes+content-hash snapshot equal across runs, macOS-flavour run asserts exactly one `addexclusion` line in `tmutil.log` against the resolved STATE_DIR (per [data-model.md §2](data-model.md) RESOLVED_BIN_FOR_ACL discipline), macOS-flavour run asserts stdout1 contains `-T "${RESOLVED_BIN_FOR_ACL}"` exactly once, stdout1 contains no `-T "*"` and no `-A` token (FR-003 anti-contract). Linux-flavour run skips the macOS-specific asserts via `runtime.GOOS != "darwin"` guards.
- [X] T006 [US1] Append `TestDeploy_InstallRefusesUnsupportedOS` to [tests/deploy/install_test.go](../../tests/deploy/install_test.go) per [contracts/test-harness.md](contracts/test-harness.md). Run install.sh with `HUSH_FORCE_OS=plan9`. Assert exit code 2 and stderr matches `install.sh: <stage>: <reason>` format (FR-005).
- [X] T007 [US1] Append `TestDeploy_InstallRefusesMissingBinary` to [tests/deploy/install_test.go](../../tests/deploy/install_test.go). Run install.sh with `HUSH_SOURCE_BIN=/nonexistent`. Assert exit code 2 and a clear stderr message (FR-007).
- [X] T008 [US1] Append `TestDeploy_InstallRefusesMissingTmutil` to [tests/deploy/install_test.go](../../tests/deploy/install_test.go) guarded by `runtime.GOOS == "darwin"`. Run install.sh with PATH stripped of any `tmutil` binary (do NOT stage `tmutil_stub.sh`). Assert exit code 4 and a clear stderr message (FR-002 hard-fail per Constitution XI non-negotiable + [research.md §R-006](research.md)).
- [X] T009 [US1] Append `TestDeploy_InstallBannerByteIdentical` to [tests/deploy/install_test.go](../../tests/deploy/install_test.go) — a focused re-run that captures the FR-001 byte-identical-stdout sub-contract on its own so a banner-regression failure is visible by test name (duplicate of T005 step 9 by design per [contracts/test-harness.md](contracts/test-harness.md)).
- [X] T010 [P] [US1] Create [tests/deploy/smoke_test.go](../../tests/deploy/smoke_test.go) with `//go:build integration` header and `TestDeploy_PlistParsesAsXML` per [contracts/test-harness.md §smoke_test.go](contracts/test-harness.md). Imports stdlib only including `encoding/xml`. Open `deploy/hush.plist`, decode with `encoding/xml`, assert: parses clean, `<key>UserName</key>` value is not `root` and not `0`, first `<string>` of `<key>ProgramArguments</key>` is `/usr/local/bin/hush` (FR-010 + FR-011 + FR-012 + SC-005).
- [X] T011 [P] [US1] Append `TestDeploy_ServiceParsesAsINI` to [tests/deploy/smoke_test.go](../../tests/deploy/smoke_test.go). Open `deploy/hush.service`, parse line-by-line with `bufio.Scanner`, assert: `[Unit]`, `[Service]`, `[Install]` sections present; `User=` value in `[Service]` is the literal `@HUSH_USER@` (committed-file token before install.sh substitution); `ExecStart=` begins with `/usr/local/bin/hush` (FR-014 + FR-015 + FR-016 + SC-005).

### Implementation for User Story 1

> Per [contracts/install-cli.md](contracts/install-cli.md), [contracts/service-files.md](contracts/service-files.md), [research.md §R-003 / §R-006 / §R-007 / §R-008 / §R-009 / §R-011](research.md), [data-model.md §1–§4](data-model.md). Tests T005–T011 are red until these files exist.

- [X] T012 [US1] Write [deploy/install.sh](../../deploy/install.sh) — bash 3.2+ compatible (`#!/usr/bin/env bash` + `set -euo pipefail` per [research.md §R-009](research.md)). Read 5 env vars per [data-model.md §1](data-model.md): `PREFIX` (default `/usr/local`), `HUSH_USER` (default `_hush` macOS / `hush` Linux per [research.md §R-003](research.md)), `HUSH_STATE_DIR` (default `/usr/local/var/hush` macOS / `/var/lib/hush` Linux per spec Clarification A2), `HUSH_INSTALL_ROOT` (default empty, test staging prefix per [research.md §R-002](research.md)), `HUSH_SOURCE_BIN` (default `./hush`). Validate every value per [contracts/install-cli.md §Environment](contracts/install-cli.md) (regex `^[a-zA-Z_][a-zA-Z0-9_-]*$` on HUSH_USER; absolute-path check on PREFIX / HUSH_STATE_DIR / HUSH_INSTALL_ROOT). Honour `HUSH_FORCE_OS` test escape hatch per [contracts/test-harness.md](contracts/test-harness.md). Execute the 7-step flow per [data-model.md §3](data-model.md): (1) create `${HUSH_USER}` via `dscl . -create` (macOS) / `useradd --system` (Linux) idempotently — wrap in `dscl . -read /Users/${HUSH_USER}` / `getent passwd ${HUSH_USER}` existence check per [research.md §R-003](research.md); (2) `install -d -m 0700 -o ${HUSH_USER} ${STATE_DIR}` per FR-002a + Constitution X; (3) macOS-only `tmutil addexclusion ${STATE_DIR}` per FR-002 + Constitution XI — hard-fail with exit 4 if `command -v tmutil` returns non-zero per [research.md §R-006](research.md); (4) `install -m 0755 ${HUSH_SOURCE_BIN} ${BIN_PATH}`; (5) `install -m 0644` service file to `/Library/LaunchDaemons/hush.plist` (macOS) or `/etc/systemd/system/hush.service` (Linux) with sed substitution `@HUSH_USER@` → `${HUSH_USER}` on Linux + optional `<string>_hush</string>` → `<string>${HUSH_USER}</string>` on macOS only if `HUSH_USER` differs from default per [research.md §R-011](research.md); (6) Linux-only `systemctl daemon-reload` when `HUSH_INSTALL_ROOT` is empty; (7) print byte-identical-across-reruns banner per [data-model.md §4](data-model.md) using `printf` (no timestamps, no hostnames, no random values) — banner's `security add-generic-password` line contains `-T "${RESOLVED_BIN_FOR_ACL}"` exactly once + no `-T "*"` + no `-A`. Document 5 exit codes 0/1/2/3/4 in script header per [contracts/install-cli.md §Exit codes](contracts/install-cli.md). Every stderr message uses format `install.sh: <stage>: <reason>` per [contracts/install-cli.md §stderr](contracts/install-cli.md). Mode 0755 on commit.
- [X] T013 [P] [US1] Write [deploy/hush.plist](../../deploy/hush.plist) — committed content per [contracts/service-files.md](contracts/service-files.md) + [research.md §R-011](research.md). `Label=com.hush.server`, `ProgramArguments=["/usr/local/bin/hush","serve","--config","/usr/local/etc/hush/config.toml"]`, `UserName=_hush`, `RunAtLoad=true`, `KeepAlive=true`, `StandardOutPath=/usr/local/var/log/hush.out.log`, `StandardErrorPath=/usr/local/var/log/hush.err.log`. Valid XML with the Apple PLIST 1.0 DOCTYPE. Mode 0644. Zero operator-specific tokens.
- [X] T014 [P] [US1] Write [deploy/hush.service](../../deploy/hush.service) — committed content per [contracts/service-files.md](contracts/service-files.md) + [research.md §R-011](research.md). `[Unit] Description=hush — Discord-gated secrets broker / After=network-online.target / Wants=network-online.target`; `[Service] Type=simple / User=@HUSH_USER@ / ExecStart=/usr/local/bin/hush serve --config /etc/hush/config.toml / Restart=on-failure / RestartSec=5s / NoNewPrivileges=true / ProtectSystem=strict / ProtectHome=true / PrivateTmp=true`; `[Install] WantedBy=multi-user.target`. Mode 0644. Zero operator-specific tokens.

**Checkpoint**: T005–T011 turn green. `magex test:race -tags=integration -run TestDeploy_Install ./tests/deploy/...` and `magex test:race -tags=integration -run TestDeploy_PlistParsesAsXML ./tests/deploy/...` and `magex test:race -tags=integration -run TestDeploy_ServiceParsesAsINI ./tests/deploy/...` all green. US1 is independently shippable as the first-time-install MVP.

---

## Phase 4: User Story 2 - Operator deploys a long-running daemon under a supervisor (Priority: P1)

**Goal**: Ship the generic supervisor launcher template that operators copy per daemon. The template execs `hush supervise` (NOT `hush request --exec` — defeats Constitution IV TTL discipline), refuses to run with unsubstituted placeholders (exit 78 / `EX_CONFIG`), and contains clearly-marked `<NAME>`, `<KEYCHAIN_ITEM>`, `<CONFIG_PATH>` placeholders per [contracts/launcher-template.md](contracts/launcher-template.md).

**Independent Test**: `magex test:race -tags=integration -run TestDeploy_LauncherTemplateExecsSupervise ./tests/deploy/...` green. Plus manual `bash -n deploy/supervise-launch.sh.template` exits 0.

### Tests for User Story 2 (write FIRST)

- [X] T015 [US2] Append `TestDeploy_LauncherTemplateExecsSupervise` to [tests/deploy/smoke_test.go](../../tests/deploy/smoke_test.go) per [contracts/test-harness.md §smoke_test.go](contracts/test-harness.md). Open `deploy/supervise-launch.sh.template`. Assertions: (a) `exec.Command("bash", "-n", path)` exits 0 (FR-023 + FR-024); (b) file contains the literal string `hush supervise` (FR-018 + SC-006); (c) file contains zero non-comment lines matching `hush request --exec` — implement with line-by-line scan, skip lines whose first non-whitespace char is `#`, on remaining lines run `strings.Contains(line, "hush request --exec")`, assert zero matches (FR-019 + SC-006 per [contracts/launcher-template.md §Active-vs-commented](contracts/launcher-template.md)); (d) file contains all three placeholder tokens `<NAME>`, `<KEYCHAIN_ITEM>`, `<CONFIG_PATH>` (FR-020); (e) file's first ~40 lines contain a header comment block explaining each placeholder + the `hush request --exec` warning (FR-021 — assert the literal substrings "SUBSTITUTE" and "DO NOT" appear in comment lines).

### Implementation for User Story 2

- [X] T016 [US2] Write [deploy/supervise-launch.sh.template](../../deploy/supervise-launch.sh.template) — committed content per [contracts/launcher-template.md](contracts/launcher-template.md) + [research.md §R-010](research.md). Three sections: (1) Header comment block listing the 3 placeholders + their meaning + where the substituted file belongs + DO-NOT warning against `hush request --exec`; (2) `set -euo pipefail` then a pre-flight grep guard using string concatenation `'<''NAME>'` etc. so the guard line doesn't match itself — exits 78 (`EX_CONFIG`) with `supervise-launch: placeholders not substituted; refusing to run` to stderr if any placeholder remains; (3) single-line core `exec /usr/local/bin/hush supervise --config <CONFIG_PATH>`. `hush request --exec` appears EXACTLY ONCE inside the DO-NOT warning comment (will be filtered out by the test's non-comment grep). Mode 0644 (template, not directly executable). Zero operator-specific tokens.

**Checkpoint**: T015 turns green. `magex test:race -tags=integration -run TestDeploy_LauncherTemplateExecsSupervise ./tests/deploy/...` green. US2 is independently shippable — operators can fork the template and register per-daemon launchers with launchd / systemd (the per-daemon plist / unit is SDD-30 territory).

---

## Phase 5: User Story 3 - Operator re-runs install.sh after upgrading the binary (Priority: P2)

**Goal**: Confirm `install.sh` re-run discipline. No new files — the contract is asserted by the same `TestDeploy_InstallIdempotent` + `TestDeploy_InstallBannerByteIdentical` that already covered US1, plus a focused readback against [data-model.md §3 side-effects](data-model.md) (`tmutil` invoked at most once, `dscl`/`useradd` invoked at most once, banner byte-identical).

**Independent Test**: Two consecutive runs of `magex test:race -tags=integration -run TestDeploy_InstallIdempotent ./tests/deploy/...` in the same CI job both green (CI already runs each test once; the test itself runs install.sh twice inside `t.TempDir()`).

### Tests for User Story 3

> US3 is covered by tests authored in US1 (T005 already runs install.sh twice and asserts every US3 acceptance bullet). No new test code. The independent-test discipline is satisfied because `TestDeploy_InstallIdempotent` is a self-contained two-run check.

- [X] T017 [US3] Validation-only task — no code changes. Confirm that the `TestDeploy_InstallIdempotent` body in [tests/deploy/install_test.go](../../tests/deploy/install_test.go) asserts every US3 Acceptance Scenario bullet from [spec.md §User Story 3](spec.md): (a) exit 0 on second run; (b) `tmutil addexclusion` invocation count == 1 in `tmutil.log` across both runs; (c) banner stdout from run 2 is byte-identical to run 1 (`bytes.Equal(stdout1, stdout2)`); (d) service-file content hash identical before/after run 2 (covered by the recursive tree snapshot). If any bullet is missing, expand the test body (do NOT create a separate test file — keep the assertion centralised so a regression has exactly one failing test name).

### Implementation for User Story 3

> No new artefacts. The install.sh idempotency primitives (`install -d` / `install -m` + existence-guarded `dscl`/`useradd` per [research.md §R-008](research.md)) committed in T012 deliver the US3 behaviour.

**Checkpoint**: T017 confirms US3 acceptance is fully encoded in the existing test. No additional code needed.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Cross-cutting smoke checks, gate runs, documentation updates, and the single combined commit per SDD-29 Prompt 5.

### Cross-cutting tests (no story label — these gate the whole chunk)

- [X] T018 [P] Append `TestDeploy_NoOperatorSpecificNames` to [tests/deploy/smoke_test.go](../../tests/deploy/smoke_test.go) per [contracts/test-harness.md §smoke_test.go](contracts/test-harness.md). Walk all 4 committed files in `deploy/` (`hush.plist`, `hush.service`, `install.sh`, `supervise-launch.sh.template`). For each file, run a regex match against denylist `openclaw|hermes|mrz|100\.90\.|tag:trusted`. Assert zero matches across all files (FR-009 + FR-013 + FR-017 + FR-022 + SC-007).
- [X] T019 [P] Append `TestDeploy_AllShellFilesParse` to [tests/deploy/smoke_test.go](../../tests/deploy/smoke_test.go). Walk `deploy/`. For every `.sh` and `.template` file, run `exec.Command("bash", "-n", path)`. Assert exit code 0 (FR-024 + SC-008).

### Gates (run in this order — every gate MUST pass clean before commit)

- [X] T020 Run `magex format:fix && magex lint` from repo root per [SDD-29 Prompt 5 step 1](../../docs/sdd/SDD-29.md) (Go formatting + lint pass on the new Go test files).
- [X] T021 Run `bash -n deploy/install.sh` and `bash -n deploy/supervise-launch.sh.template` per [SDD-29 Prompt 5 step 2](../../docs/sdd/SDD-29.md). Both must exit 0 (FR-024 + SC-008).
- [X] T022 If `command -v shellcheck` returns 0, run `shellcheck deploy/install.sh deploy/supervise-launch.sh.template` with default severity. Any finding fails the chunk per [SDD-29 Prompt 5 step 3](../../docs/sdd/SDD-29.md) + [research.md §R-013](research.md). If shellcheck is not installed, log "shellcheck not installed — document in CI prerequisites" and continue.
- [X] T023 Run `magex test:race -tags=integration -run TestDeploy_ ./tests/deploy/...` per [SDD-29 Prompt 5 step 4](../../docs/sdd/SDD-29.md). All `TestDeploy_*` functions green: `TestDeploy_InstallIdempotent`, `TestDeploy_InstallRefusesUnsupportedOS`, `TestDeploy_InstallRefusesMissingBinary`, `TestDeploy_InstallRefusesMissingTmutil`, `TestDeploy_InstallBannerByteIdentical`, `TestDeploy_PlistParsesAsXML`, `TestDeploy_ServiceParsesAsINI`, `TestDeploy_LauncherTemplateExecsSupervise`, `TestDeploy_NoOperatorSpecificNames`, `TestDeploy_AllShellFilesParse`.
- [X] T024 Static spot-checks per [SDD-29 Prompt 5 steps 5–8](../../docs/sdd/SDD-29.md): (a) `grep "tmutil addexclusion" deploy/install.sh` returns at least one match (Constitution XI presence check); (b) `grep "hush supervise" deploy/supervise-launch.sh.template` returns at least one match (FR-018); (c) `grep -E "^[[:space:]]*[^#]" deploy/supervise-launch.sh.template | grep -F "hush request --exec"` returns zero matches (FR-019 active-line check); (d) `grep -F "<NAME>" deploy/supervise-launch.sh.template && grep -F "<KEYCHAIN_ITEM>" deploy/supervise-launch.sh.template && grep -F "<CONFIG_PATH>" deploy/supervise-launch.sh.template` all match (FR-020); (e) `grep -REiE "openclaw|hermes|mrz|100\.90\.|tag:trusted" deploy/` returns zero matches (FR-009 / SC-007).

### Documentation updates

- [X] T025 [P] Append a new `deploy/` entry to [docs/PACKAGE-MAP.md](../../docs/PACKAGE-MAP.md) titled `**Exported API — locked at SDD-29**` per [SDD-29 Prompt 5 step 9](../../docs/sdd/SDD-29.md). Describe the 4 files (`hush.plist`, `hush.service`, `install.sh`, `supervise-launch.sh.template`), their operator-facing contract, and note `no exported Go symbols — see install.sh --help for installation usage`.
- [X] T026 [P] Update [docs/AC-MATRIX.md](../../docs/AC-MATRIX.md) AC-1 (CLI surface), AC-6 (Keychain ACL), AC-10 (daemon lifecycle) rows per [SDD-29 Prompt 5 step 10](../../docs/sdd/SDD-29.md). Add references to `deploy/install.sh` (AC-1 + AC-6 banner), `deploy/hush.plist` + `deploy/hush.service` (AC-10), `deploy/supervise-launch.sh.template` (AC-10).
- [X] T027 [P] Mark SDD-29 status `done` in [docs/SDD-PLAYBOOK.md](../../docs/SDD-PLAYBOOK.md) per [SDD-29 Prompt 5 step 11](../../docs/sdd/SDD-29.md).

### Single combined commit (per SDD-29 deferred-commit discipline)

- [X] T028 From repo root: `git add deploy/ tests/deploy/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md specs/029-deploy-artifacts/tasks.md` then `git commit -m "feat(deploy): launchd plist + systemd unit + install.sh + launcher template (SDD-29)"`. Per [SDD-29 chunk doc lines 41–42](../../docs/sdd/SDD-29.md), all SDD-29 commits are deferred to this single combined commit — do NOT commit between phases.

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: No dependencies — `mkdir` only.
- **Foundational (Phase 2)**: Depends on Phase 1. Blocks all test tasks in Phase 3+ (every test reads `testdata/`).
- **User Story 1 (Phase 3)**: Depends on Phase 2. Tests T005–T011 must be written before artefacts T012–T014. T012 (install.sh) does not depend on T013/T014 only because the integration test reads the service files from the source tree (`deploy/hush.plist`, `deploy/hush.service`) — running install.sh in test mode without committed service files would fail at step 5 of [contracts/install-cli.md §Side effects](contracts/install-cli.md). Practical order: write all tests → commit nothing → write install.sh + plist + unit → run tests green. Within T013/T014, files are independent and `[P]`-marked.
- **User Story 2 (Phase 4)**: Depends on Phase 2. Independent of US1 — the launcher template is not installed by install.sh, so US2 can be implemented in parallel with US1 if staffed.
- **User Story 3 (Phase 5)**: Depends on US1 only. T017 is a validation-only readback of T005's test body.
- **Polish (Phase 6)**: Depends on US1 + US2 complete. Gates T020–T024 must pass before T028 (the combined commit).

### User Story Independence

- **US1 (P1)**: Self-contained. Delivers `install.sh` + `hush.plist` + `hush.service`. Operator can install on a fresh host.
- **US2 (P1)**: Self-contained. Delivers `supervise-launch.sh.template`. Operator can fork it per daemon.
- **US3 (P2)**: A discipline on US1's install.sh, not a separate artefact. Asserted by US1's existing test.

### Within Each User Story

- Tests are written BEFORE artefacts (user's explicit instruction; deploy artefacts are not Go code so this is not classical TDD but the same write-fail-pass discipline applies).
- Within US1's implementation tasks: T013 (plist) and T014 (unit) are independent of T012 (install.sh) and of each other — all three can be authored in parallel once T005–T011 exist.

### Parallel Opportunities

- T001, T002 in parallel (different directories).
- T003, T004 in parallel (different files).
- T010, T011 are appended to the same file — sequential (no `[P]` between them within the same file). Both gated by T002 (`tests/deploy/` exists).
- T013, T014 in parallel (different files, both depend only on tests being written).
- T018, T019 in parallel (different test functions, same file — `[P]` is OK because each test is appended as a new top-level function with no source-line conflict with the other).
- T025, T026, T027 in parallel (different docs files).

---

## Parallel Example: Foundational Phase

```bash
# After Phase 1 directories exist, launch both fixture-creation tasks together:
Task: "Create tests/deploy/testdata/tmutil_stub.sh recording shim (T003)"
Task: "Create tests/deploy/testdata/fake-hush zero-byte executable (T004)"
```

## Parallel Example: User Story 1 Artefacts

```bash
# After T005–T011 exist (and are red), launch all three artefacts together:
Task: "Write deploy/install.sh per contracts/install-cli.md + research.md §R-002–§R-011 (T012)"
Task: "Write deploy/hush.plist committed content per contracts/service-files.md (T013)"
Task: "Write deploy/hush.service committed content per contracts/service-files.md (T014)"
```

## Parallel Example: Polish Documentation

```bash
# After all gates (T020–T024) pass:
Task: "Append deploy/ entry to docs/PACKAGE-MAP.md (T025)"
Task: "Update AC-1/AC-6/AC-10 rows in docs/AC-MATRIX.md (T026)"
Task: "Mark SDD-29 status done in docs/SDD-PLAYBOOK.md (T027)"
```

---

## Implementation Strategy

### MVP First (User Story 1 Only)

1. Complete Phase 1 (T001, T002): directories.
2. Complete Phase 2 (T003, T004): fixtures.
3. Complete Phase 3: tests T005–T011 first (all red), then artefacts T012–T014 (all green).
4. **STOP and VALIDATE**: `magex test:race -tags=integration -run TestDeploy_Install ./tests/deploy/...` green. First-time install works.
5. Demo / ship the install.sh path on its own.

### Incremental Delivery

1. Phase 1 + Phase 2 → foundation ready.
2. Add US1 (Phase 3) → install.sh + plist + unit ship. Operator can install hush on a fresh host. MVP!
3. Add US2 (Phase 4) → launcher template ships. Operator can deploy daemons under supervise.
4. US3 (Phase 5) → already covered by US1's test; no new code.
5. Phase 6 → gates + docs + single combined commit.

### Parallel Team Strategy

With two developers:

1. Both: Phase 1 + Phase 2 together (10 minutes).
2. Once Phase 2 done:
   - Developer A: US1 tests (T005–T011) → US1 artefacts (T012–T014).
   - Developer B: US2 test (T015) → US2 artefact (T016).
3. Both: Phase 6 gates, docs, and combined commit (T028) together.

---

## Notes

- This chunk produces ZERO Go production code. The Go integration tests are the only `.go` files committed.
- Tests-before-artefacts is the user's explicit instruction; SDD-29's "Tests required" lines 25–28 + FR-024 + FR-025 + FR-026 + SC-002 + SC-003 + SC-004 + SC-005 + SC-006 + SC-007 + SC-008 codify the same discipline.
- Constitution XI is non-negotiable: `tmutil addexclusion` on macOS must hard-fail (exit 4) if `tmutil` is missing — `TestDeploy_InstallRefusesMissingTmutil` (T008) is the enforcement.
- `install.sh` creates ZERO Keychain entries (FR-003 absolute lock); the banner prints `security add-generic-password -T <binary-path>` invocations the operator runs separately. T005's macOS-flavour stdout grep enforces this.
- The single combined commit (T028) is per SDD-29's deferred-commit discipline (chunk-doc lines 41–42). Do NOT commit between phases.
- Constitution Check passes clean per [plan.md §Constitution Check](plan.md) — no Complexity Tracking rows needed; `HUSH_INSTALL_ROOT` test-knob and launcher pre-flight grep guard are documented design choices, not chunk-doc-API extensions.
