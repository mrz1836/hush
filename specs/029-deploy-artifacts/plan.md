# Implementation Plan: SDD-29 Deploy Artifacts

**Branch:** `029-deploy-artifacts` | **Date:** 2026-05-14 | **Spec:** [spec.md](spec.md)

**Input:** Feature specification from
[`specs/029-deploy-artifacts/spec.md`](spec.md) and chunk contract at
[`docs/sdd/SDD-29.md`](../../docs/sdd/SDD-29.md).

---

## Summary

SDD-29 delivers the four operator-facing deploy artefacts that turn a
freshly-built hush binary into a runnable daemon on either macOS or
Linux:

1. `deploy/hush.plist` — launchd job for the hush vault server (macOS).
2. `deploy/hush.service` — systemd unit for the hush vault server (Linux).
3. `deploy/install.sh` — idempotent installer that lays down the
   binary, places the platform service file, creates the vault state
   directory with `0700` ownership, adds the macOS Time Machine
   exclusion, and prints a banner whose `security
   add-generic-password -T <binary-path> ...` invocation the operator
   runs separately (install.sh creates zero Keychain entries).
4. `deploy/supervise-launch.sh.template` — generic per-daemon launcher
   the operator copies, fills in three clearly-marked placeholders
   (`<NAME>`, `<KEYCHAIN_ITEM>`, `<CONFIG_PATH>`), and registers with
   launchd or systemd. The template execs `hush supervise` and refuses
   to run with unsubstituted placeholders.

Plus the test harness at `tests/deploy/install_test.go` and
`tests/deploy/smoke_test.go` (`//go:build integration`) that asserts
FR-001 idempotency, FR-002 tmutil-on-macOS, FR-003 banner-`-T`-ACL,
FR-019/026 no-active-request-exec, FR-024 bash-n-parses, and FR-009/013/017/022
no-operator-specific-names.

Approach is locked by the chunk-doc HOW contract
([`docs/sdd/SDD-29.md`](../../docs/sdd/SDD-29.md) lines 157–185). All
plan-time decisions live in [research.md](research.md); none of them
extend the chunk-doc API.

---

## Technical Context

**Language/Version:** bash 3.2+ (matches macOS system bash and modern Linux distros) for installer + launcher template; Go 1.22+ (matches repo `go.mod`) for the integration test.

**Primary Dependencies:** stdlib only — bash, `install(1)`, `uname`, `dscl` (macOS), `useradd` (Linux), `tmutil` (macOS, hard-fail if missing). Go tests use stdlib (`testing`, `os`, `os/exec`, `path/filepath`, `runtime`, `strings`, `bytes`, `bufio`, `encoding/xml`) — zero new direct Go dependencies (Constitution XI).

**Storage:** N/A. The "data" of this chunk is the produced filesystem layout (binary, service file, state directory). No databases, no persisted runtime state.

**Testing:** Go integration tests under `tests/deploy/` gated by `//go:build integration`. Runner: `magex test:race -tags=integration -run TestDeploy_ ./tests/deploy/...`. Plus `bash -n` + `shellcheck` (if available) on every `.sh` / `.template` file in CI.

**Target Platform:** macOS 13+ (Apple Silicon and Intel) and Linux x86_64 / arm64 with systemd. Other OSes refuse to install (FR-005). CI matrix runs both macOS-13 and ubuntu-latest jobs.

**Project Type:** Operator-facing deploy artefacts — not a Go package. `deploy/` is a new top-level repo directory; no entry in PACKAGE-MAP.md yet (added by Prompt 5 step 9).

**Performance Goals:** N/A. `install.sh` runs once per host; performance is not a quality attribute. Idempotent re-run completes in <2s on commodity hardware.

**Constraints:**
- install.sh MUST be bash 3.2-compatible (no associative arrays, no `${var,,}`, no `mapfile`).
- All produced file modes match [research.md §R-012](research.md): binary `0755`, service file `0644`, state directory `0700`.
- Banner stdout MUST be byte-identical across re-runs in the same env (FR-001 + US3 acceptance scenario 2).
- Zero new Go dependencies (Constitution XI).

**Scale/Scope:** 4 committed deploy artefacts + 2 Go test files + 2 testdata fixtures. No production Go code.

---

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-evaluated after Phase 1 design.*

Constitutional principles in scope (per SDD-29 chunk doc + plan prompt):
**I** (operator-agnostic), **IV** (daemons don't re-prompt), **V** (loud
failure), **IX** (idiomatic Go discipline — applies to the integration
test only), **X** (observability & redaction — install.sh handles zero
secret material), **XI** (native-first, minimal dependencies, ephemeral
vault — tmutil exclusion + non-root).

| Principle | Gate | Verdict | Evidence |
|-----------|------|---------|----------|
| **I — Operator-agnostic** | Zero operator-specific names (daemons, hostnames, Tailscale tags, Discord IDs) committed under `deploy/`. | **PASS** | Plist `Label` is `com.hush.server` (product, not operator — [research.md §R-004](research.md)). Unit `Description` references `hush` (product). Launcher template uses `<NAME>`/`<KEYCHAIN_ITEM>`/`<CONFIG_PATH>` placeholders — no real names. `TestDeploy_NoOperatorSpecificNames` greps a denylist (`openclaw`, `hermes`, `mrz`, `100.90.`, `tag:trusted`) against all four files; zero matches required ([contracts/test-harness.md](contracts/test-harness.md)). |
| **IV — Supervisor for daemons** | Launcher template execs `hush supervise`; `hush request --exec` appears only inside an explicit DO-NOT warning comment. | **PASS** | [contracts/launcher-template.md](contracts/launcher-template.md): single `exec /usr/local/bin/hush supervise --config <CONFIG_PATH>` line; `hush request --exec` confined to header comment; `TestDeploy_LauncherTemplateExecsSupervise` greps non-comment lines and asserts zero matches for the forbidden string. |
| **V — Loud failure** | Missing source binary, unsupported OS, missing `tmutil`, and unsubstituted launcher placeholders all fail loudly with non-zero exit and stderr message. | **PASS** | install.sh exit codes 1–4 documented in [contracts/install-cli.md](contracts/install-cli.md); `TestDeploy_InstallRefusesUnsupportedOS`, `TestDeploy_InstallRefusesMissingBinary`, `TestDeploy_InstallRefusesMissingTmutil` enforce three of those. Launcher pre-flight guard exits 78 (`EX_CONFIG`) on unsubstituted placeholders ([contracts/launcher-template.md](contracts/launcher-template.md)). |
| **IX — Idiomatic Go** | The Go integration test uses stdlib only, `context` where I/O occurs, table-driven sub-tests, no `init()`, no globals. | **PASS** | [contracts/test-harness.md](contracts/test-harness.md): named test functions only, `t.TempDir()` per test, no package-level state, no globals. Bash files are out of Principle IX scope (the principle reads "every line of *Go* in this repo"). |
| **X — Observability & redaction** | install.sh handles zero secret material. State directory mode `0700`. | **PASS** | FR-003 lock asserted by `TestDeploy_InstallIdempotent` (stdout grep for `-T "<path>"`); install.sh never reads passphrases. Mode `0700` enforced by `install -d -m 0700` ([data-model.md §3](data-model.md)). |
| **XI — Native-first + ephemeral vault** | Zero new Go dependencies. macOS `tmutil addexclusion` mandatory (no silent skip). Vault state dir non-root, `0700`. | **PASS** | Test imports stdlib only ([research.md §R-014](research.md)). `tmutil` invocation unconditional on macOS; missing `tmutil` → exit 4, not a skip ([research.md §R-006](research.md)). State dir owned by `${HUSH_USER}` (`_hush` / `hush`), mode `0700`. |

**Gate verdict: PASS with no violations.**

The plan introduces one env knob — `HUSH_INSTALL_ROOT` — that is NOT
in the spec FR list. This is a test-only staging prefix, documented in
[research.md §R-002](research.md). It is operator-facing (any operator
could in theory set it) but encodes no operator-specific value, so
Constitution I is satisfied. It is not a Constitution-Check violation
and does not require a Complexity Tracking row.

**Re-evaluation after Phase 1.** All four Phase 1 artefacts —
[research.md](research.md), [data-model.md](data-model.md), the four
files in [contracts/](contracts/), and [quickstart.md](quickstart.md) —
reflect the same gate verdicts. No design choice in Phase 1 weakens any
principle. Gate remains **PASS**.

---

## Project Structure

### Documentation (this feature)

```text
specs/029-deploy-artifacts/
├── plan.md                            # This file
├── spec.md                            # Feature specification + clarifications
├── research.md                        # Phase 0 — decision log (R-001…R-015)
├── data-model.md                      # Phase 1 — env vars, resolved paths, banner shape
├── quickstart.md                      # Phase 1 — operator-facing walkthrough
└── contracts/
    ├── install-cli.md                 # install.sh CLI / env / exit-code contract
    ├── service-files.md               # plist + systemd unit committed content
    ├── launcher-template.md           # supervise-launch.sh.template contract
    └── test-harness.md                # Go integration test required functions
```

### Source artefacts (repository root)

```text
deploy/                                # NEW top-level directory
├── hush.plist                         # launchd job (macOS), Label=com.hush.server
├── hush.service                       # systemd unit (Linux), User=@HUSH_USER@
├── install.sh                         # idempotent installer (bash 3.2+)
└── supervise-launch.sh.template       # operator copies + fills 3 placeholders

tests/deploy/                          # NEW test directory
├── install_test.go                    # //go:build integration — FR-001/002/025
├── smoke_test.go                      # //go:build integration — FR-012/016/019/026
└── testdata/
    ├── tmutil_stub.sh                 # recording shim used during macOS test run
    └── fake-hush                      # zero-byte exec, used as HUSH_SOURCE_BIN
```

**Structure Decision.** Two new top-level directories. `deploy/` already
exists (housing `deploy/examples/supervisors/`); SDD-29 adds the four
new files at its root. `tests/deploy/` is new — chosen over
`internal/deploy/` because there is no Go package to test (the
artefacts are bash + XML + INI); `tests/deploy/` reflects "integration
test of repository files" without misleading anyone into looking for
a non-existent Go package. Build-tag isolation (`//go:build
integration`) keeps the tests out of `go test ./...` default runs and
aligns with existing repo CI conventions.

PACKAGE-MAP.md gains a new `deploy/` entry on Prompt 5 step 9
(implement phase) with the heading **"Exported API — locked at
SDD-29"** that describes the four files and notes "no exported Go
symbols".

---

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified.**

The Constitution Check passes with no violations. No rows required.

The two design choices that could *look* like extensions:
1. `HUSH_INSTALL_ROOT` env knob — test-only staging prefix
   ([research.md §R-002](research.md)). Not a spec extension because
   the spec does not address test mechanics; not a Constitution
   violation because the knob encodes no operator-specific value.
2. Pre-flight placeholder guard in the launcher template
   ([research.md §R-010](research.md)). Not a spec extension because
   spec edge cases explicitly require "unmodified placeholder strings
   must cause the script to fail at startup"; the guard is the
   minimum-viable implementation of that requirement.

Both choices are referenced in the plan body above; neither is a
chunk-doc-API extension (no new files, no new env vars in the public
contract beyond what FR-002/004/006 already named) and neither
relaxes a Constitution principle. No Complexity Tracking entries
needed.
