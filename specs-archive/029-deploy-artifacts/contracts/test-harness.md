# Contract — Go integration test harness

This document fixes the Go integration test structure that gates
SDD-29 acceptance. All tests are tagged `//go:build integration` and run
via `magex test:race -tags=integration ./tests/deploy/...`.

---

## Files

| Path                                          | Purpose                                                       |
|-----------------------------------------------|---------------------------------------------------------------|
| `tests/deploy/install_test.go`                | Idempotency + banner assertions (FR-001, FR-002, FR-025).     |
| `tests/deploy/smoke_test.go`                  | Static-file assertions on plist, unit, launcher template.     |
| `tests/deploy/testdata/tmutil_stub.sh`        | Recording shim for `tmutil` on macOS test runs.               |
| `tests/deploy/testdata/fake-hush`             | Zero-byte executable used as `HUSH_SOURCE_BIN`.               |

---

## `install_test.go` — required test functions

### `TestDeploy_InstallIdempotent`

Body outline:
```
1. tmp := t.TempDir()
2. logFile := tmp + "/tmutil.log"
3. stubDir := tmp + "/stub-bin"; place tmutil_stub.sh as ${stubDir}/tmutil; chmod +x
4. env := augmented PATH with stubDir first, HUSH_INSTALL_ROOT=tmp, HUSH_SOURCE_BIN=fake-hush, TMUTIL_LOG=logFile
5. Run install.sh once  → assert exit 0; capture stdout1
6. Snapshot tree (paths + modes + content hashes) → snap1
7. Run install.sh again → assert exit 0; capture stdout2
8. Snapshot again → snap2
9. Assert bytes.Equal(stdout1, stdout2)             [FR-001]
10. Assert snap1 == snap2                            [FR-001]
11. On macOS-flavour run, assert exactly one `addexclusion` line in logFile against the resolved STATE_DIR  [FR-002 / FR-025]
12. On macOS-flavour run, assert stdout1 contains `-T "${RESOLVED_BIN_FOR_ACL}"` exactly once  [FR-003 / SC-004]
13. Assert stdout1 contains no `-T "*"` and no `-A` token             [FR-003 anti-contract]
```

### `TestDeploy_InstallRefusesUnsupportedOS`

Run install.sh with `HUSH_FORCE_OS=plan9` (a test-only escape hatch
documented in the script header). Assert exit code 2 and a clear
stderr message (FR-005).

### `TestDeploy_InstallRefusesMissingBinary`

Run install.sh with `HUSH_SOURCE_BIN=/nonexistent`. Assert exit code
2 and a clear stderr message (FR-007).

### `TestDeploy_InstallRefusesMissingTmutil`

macOS-flavour run with PATH stripped of any `tmutil` binary. Assert
exit code 4 and a clear stderr message (FR-002 hard-fail / spec edge
case).

### `TestDeploy_InstallBannerByteIdentical`

A focused re-run check that captures the FR-001 byte-identical-stdout
sub-contract on its own (a duplicate of step 9 above, kept separate so
a banner-regression failure is visible by test name).

---

## `smoke_test.go` — required test functions

### `TestDeploy_PlistParsesAsXML`

Open `deploy/hush.plist`. Decode with `encoding/xml`. Assert: parses
clean, `<key>UserName</key>` value is not `root` and not `0`, first
`<string>` of `<key>ProgramArguments</key>` is `/usr/local/bin/hush`.

### `TestDeploy_ServiceParsesAsINI`

Open `deploy/hush.service`. Parse line-by-line. Assert: `[Unit]`,
`[Service]`, `[Install]` sections present; `User=` value in `[Service]`
is the literal `@HUSH_USER@`; `ExecStart=` begins with
`/usr/local/bin/hush`.

### `TestDeploy_LauncherTemplateExecsSupervise`

Open `deploy/supervise-launch.sh.template`. Assert: `bash -n` succeeds
(invoked via `exec.Command`), file contains `hush supervise`, file
contains zero non-comment lines matching `hush request --exec`, file
contains all three placeholder tokens `<NAME>`, `<KEYCHAIN_ITEM>`,
`<CONFIG_PATH>`.

### `TestDeploy_NoOperatorSpecificNames`

Walk all four committed files in `deploy/`. Run a grep against a
denylist of operator-personal patterns:
```
openclaw   hermes   mrz   100.90.   tag:trusted
```
Assert zero matches across all files (FR-009 / FR-013 / FR-017 /
FR-022 / SC-007).

### `TestDeploy_AllShellFilesParse`

Walk `deploy/`. For every `.sh` and `.template` file, run
`exec.Command("bash", "-n", path)`. Assert exit code 0 (FR-024 / SC-008).

---

## Test invocation

CI command (from SDD-29 Prompt 5 step 4):
```
magex test:race -tags=integration -run TestDeploy_ ./tests/deploy/...
```

Local developer command:
```
go test -tags=integration -race -run TestDeploy_ ./tests/deploy/...
```

Both invocations are platform-portable: macOS-only assertions are
guarded by `if runtime.GOOS != "darwin" { t.Skip(...) }`, Linux-only
assertions by the equivalent `linux` guard. The CI matrix runs both
macOS-13 and ubuntu-latest jobs (existing matrix; SDD-29 inherits it).

---

## Coverage / gate

This chunk has **no Go production code** to cover. The gates are:

| Gate                                    | Source                  |
|-----------------------------------------|-------------------------|
| `magex format:fix && magex lint`        | SDD-29 Prompt 5 step 1  |
| `bash -n` on every committed shell file | FR-024 / SC-008         |
| `shellcheck` if available               | SDD-29 chunk-doc        |
| `TestDeploy_InstallIdempotent` green    | FR-001 / FR-025         |
| `TestDeploy_*` smoke tests green        | FR-012 / FR-016 / FR-026|
