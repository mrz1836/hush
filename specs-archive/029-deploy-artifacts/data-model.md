# Phase 1 — Data Model: SDD-29 Deploy Artifacts

**Branch:** `029-deploy-artifacts`
**Spec:** [spec.md](spec.md)
**Research:** [research.md](research.md)

SDD-29 ships operator-facing artefacts, not a Go package. There are no
runtime entities, no schemas, no persisted state. The "data" of this
chunk is the small set of **inputs**, **resolved values**, and
**produced files** that the installer manipulates, plus the placeholder
contract that the launcher template encodes.

---

## 1. Installer input variables (environment)

These are the only env vars `install.sh` reads. They are documented in
the next-steps banner and the script header.

| Variable             | Default (macOS)            | Default (Linux)         | Required? | Purpose                                                                                  |
|----------------------|----------------------------|-------------------------|-----------|------------------------------------------------------------------------------------------|
| `PREFIX`             | `/usr/local`               | `/usr/local`            | No        | Install prefix for the binary. Effective binary path = `${PREFIX}/bin/hush`.             |
| `HUSH_USER`          | `_hush`                    | `hush`                  | No        | System account that runs the hush server.                                                |
| `HUSH_STATE_DIR`     | `/usr/local/var/hush`      | `/var/lib/hush`         | No        | Vault state directory. Mode `0700`. Target of macOS Time Machine exclusion.              |
| `HUSH_INSTALL_ROOT`  | (empty)                    | (empty)                 | No        | Staging prefix for tests (R-002). Operators leave unset.                                 |
| `HUSH_SOURCE_BIN`    | `./hush` (relative to cwd) | `./hush`                | No        | Path to the source hush binary install.sh will copy. Spec FR-007 — missing → hard fail.  |

**Invariants.**
- Every value is space-free (spec Clarification A2). Quoted in bash but
  no shell quoting tricks are required.
- `HUSH_USER` is validated against `^[a-zA-Z_][a-zA-Z0-9_-]*$` to reject
  shell-injection attempts.
- `HUSH_STATE_DIR` must be an absolute path (begins with `/`). install.sh
  rejects relative paths with a clear error.
- `HUSH_INSTALL_ROOT`, if set, must also be an absolute path and must
  exist on the filesystem before install.sh runs (test harness creates
  it via `t.TempDir()`).

---

## 2. Installer resolved values

After argument resolution, `install.sh` computes the following derived
paths. All subsequent file operations use them.

| Name           | Value (computed)                                                                                  |
|----------------|---------------------------------------------------------------------------------------------------|
| `BIN_PATH`     | `${HUSH_INSTALL_ROOT}${PREFIX}/bin/hush`                                                          |
| `STATE_DIR`    | `${HUSH_INSTALL_ROOT}${HUSH_STATE_DIR}`                                                           |
| `SERVICE_DST`  | macOS: `${HUSH_INSTALL_ROOT}/Library/LaunchDaemons/hush.plist`<br>Linux: `${HUSH_INSTALL_ROOT}/etc/systemd/system/hush.service` |
| `SERVICE_SRC`  | `$(dirname "$0")/hush.plist` (macOS) or `$(dirname "$0")/hush.service` (Linux)                    |
| `RESOLVED_BIN_FOR_ACL` | `${PREFIX}/bin/hush`  *(without `HUSH_INSTALL_ROOT` — the banner shows the operator-facing path the Keychain ACL must reference)* |

`RESOLVED_BIN_FOR_ACL` is the value substituted into the `-T` argument
of the Keychain `add-generic-password` line in the printed banner.
**It deliberately omits `HUSH_INSTALL_ROOT`** because the operator's
real Keychain entry must reference the real install path
(`/usr/local/bin/hush`), not the test's staging root.

---

## 3. Produced filesystem state (per OS)

After a successful run on **macOS**:

```
${HUSH_INSTALL_ROOT}${PREFIX}/bin/hush              [-rwxr-xr-x root:wheel]
${HUSH_INSTALL_ROOT}/Library/LaunchDaemons/hush.plist [-rw-r--r-- root:wheel]
${HUSH_INSTALL_ROOT}${HUSH_STATE_DIR}/              [drwx------ ${HUSH_USER}:(system)]
```

Plus side-effects (NOT files):
- The `${HUSH_USER}` account exists in DirectoryServices.
- `tmutil addexclusion ${HUSH_STATE_DIR}` invoked exactly once.
- `dscl ... -create /Users/${HUSH_USER}` invoked at most once across
  any number of install.sh runs.

After a successful run on **Linux**:

```
${HUSH_INSTALL_ROOT}${PREFIX}/bin/hush              [-rwxr-xr-x root:root]
${HUSH_INSTALL_ROOT}/etc/systemd/system/hush.service [-rw-r--r-- root:root]
${HUSH_INSTALL_ROOT}${HUSH_STATE_DIR}/              [drwx------ ${HUSH_USER}:(system)]
```

Plus side-effects:
- The `${HUSH_USER}` account exists in `/etc/passwd`.
- `useradd --system ... ${HUSH_USER}` invoked at most once.
- `systemctl daemon-reload` invoked exactly once on the FIRST run
  (no-op on re-runs because the unit file content is unchanged).

---

## 4. Banner stdout (FR-008 + FR-003)

The banner is the only stdout `install.sh` produces on success. It is
byte-identical across re-runs in the same env. Content is determined
solely by `RESOLVED_BIN_FOR_ACL`, `HUSH_USER`, and the OS.

### macOS banner (canonical form)

```
hush installed:
  binary:      ${RESOLVED_BIN_FOR_ACL}
  service:     /Library/LaunchDaemons/hush.plist
  state dir:   ${HUSH_STATE_DIR}  (0700, owned by ${HUSH_USER}, excluded from Time Machine)
  run-as user: ${HUSH_USER}

Next steps (run these yourself — install.sh creates no Keychain entries):

  1. Store the vault passphrase in the macOS Keychain with the binary-path ACL:
       security add-generic-password \
         -a "${HUSH_USER}" -s "hush-vault-passphrase" \
         -T "${RESOLVED_BIN_FOR_ACL}" \
         -U -w "<YOUR-PASSPHRASE>"

  2. Run `hush init` interactively to create the vault.

  3. Load the daemon:
       sudo launchctl bootstrap system /Library/LaunchDaemons/hush.plist

See docs/CLEAN-MACHINE.md for per-machine client registration.
```

### Linux banner (canonical form)

```
hush installed:
  binary:      ${RESOLVED_BIN_FOR_ACL}
  service:     /etc/systemd/system/hush.service
  state dir:   ${HUSH_STATE_DIR}  (0700, owned by ${HUSH_USER})
  run-as user: ${HUSH_USER}

Next steps:

  1. Provision the vault passphrase via the operator's chosen secret
     mechanism (systemd LoadCredential, vault-aware launcher, etc.).
     See docs/CLEAN-MACHINE.md.

  2. Run `hush init` interactively to create the vault.

  3. Enable and start the service:
       sudo systemctl daemon-reload
       sudo systemctl enable --now hush.service

See docs/CLEAN-MACHINE.md for per-machine client registration.
```

**Verification.** The Go integration test on macOS runs install.sh
twice, captures stdout each time, and asserts:
- Run 1 stdout `==` Run 2 stdout (byte-identical, FR-001 + Acceptance
  Scenario 2 of US1) — asserted by `TestDeploy_InstallIdempotent` +
  the focused `TestDeploy_InstallBannerByteIdentical`.
- Run 1 stdout contains exactly one line matching
  `-T "${RESOLVED_BIN_FOR_ACL}"` (FR-003 + SC-004).
- No line matches `-T "*"` or `-T '*'` or `-A` (no wildcard ACL).

---

## 5. Launcher template placeholders (FR-020)

`supervise-launch.sh.template` declares three operator substitutions.
Each is a literal token the operator replaces (typically via `sed -i`
or a manual editor pass).

| Placeholder        | Operator value         | Used at                            |
|--------------------|------------------------|------------------------------------|
| `<NAME>`           | logical daemon name    | header comment + filename rename   |
| `<KEYCHAIN_ITEM>`  | Keychain item name     | header comment (informational)     |
| `<CONFIG_PATH>`    | absolute TOML path     | executable `exec` line             |

**Pre-flight guard.** Before the `exec` line, the template runs a
self-grep that exits non-zero (with exit code 78) if any of the three
tokens still appears in the running file. This implements spec edge
case "unmodified placeholder strings must cause the script to fail at
startup".

**`<NAME>` is informational at run-time.** It is named in the spec
because operators must know which token represents the daemon name,
but the executable line does not reference it (the daemon name is
encoded in the TOML at `<CONFIG_PATH>`).

**`<KEYCHAIN_ITEM>` is informational at run-time.** `hush supervise`
reads the Keychain itself (via the `-T` ACL); the launcher does not
shell-out to `security find-generic-password`. The placeholder exists
so the operator's customised launcher carries a clear comment about
which Keychain item is in play for that daemon — auditable by reading
the file.

---

## 6. Test fixtures

| Fixture                                          | Purpose                                                                                  |
|--------------------------------------------------|------------------------------------------------------------------------------------------|
| `tests/deploy/testdata/tmutil_stub.sh`           | Records argv to `${TMPDIR}/tmutil.log`, exits 0. Placed first on PATH in macOS test run. |
| `tests/deploy/testdata/fake-hush`                | Empty executable file used as `HUSH_SOURCE_BIN` during the test (a real binary is not needed for layout assertions). |
| `tests/deploy/testdata/install_root/` (transient) | Created by `t.TempDir()`; passed as `HUSH_INSTALL_ROOT`.                                |

No fixtures are committed under `deploy/` itself — fixtures live with
the tests.

---

## 7. Non-entities (explicitly out of scope)

- The vault file (`vault.dat`) — created by `hush init`, not install.sh.
- The config file (`config.toml`) — created by the operator or by
  `hush init`, not install.sh.
- Keychain items — created by the operator running the banner's
  commands, not install.sh (FR-003 lock).
- Tailscale ACL entries — out of SDD-29 scope (SDD-30 territory).
- Per-daemon supervisor TOMLs — out of SDD-29 scope (SDD-30 ships the
  generic example).
- Client registration entries (`~/.hush/clients.json`) — out of scope
  (per-host operator task).

The chunk's data model is deliberately tiny — that's the design.
