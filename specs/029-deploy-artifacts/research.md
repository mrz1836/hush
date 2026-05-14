# Phase 0 — Research: SDD-29 Deploy Artifacts

**Branch:** `029-deploy-artifacts`
**Spec:** [spec.md](spec.md)
**Chunk doc:** [`docs/sdd/SDD-29.md`](../../docs/sdd/SDD-29.md)

This file resolves every plan-time decision left open by the spec and the
chunk-doc HOW contract so the Phase-1 contracts and the Phase-2 tasks can
proceed with zero NEEDS CLARIFICATION markers.

---

## R-001 — Test harness: Go integration test, not bash

**Decision.** The idempotency test is a Go integration test at
`tests/deploy/install_test.go` with the `//go:build integration` tag, run
by `magex test:race -tags=integration -run TestDeploy_InstallIdempotent`.
A separate Go file at `tests/deploy/smoke_test.go` (same build tag)
carries the three sibling smoke tests called out in SDD-29 Prompt 4:
`TestDeploy_PlistParsesAsXML`, `TestDeploy_ServiceParsesAsINI`,
`TestDeploy_LauncherTemplateExecsSupervise`.

**Rationale.** Three forces converge:

1. The repo's CI gate is `magex test:race -tags=integration` (Constitution
   VIII; Prompt 5 step 4 explicitly invokes this command). A standalone
   `install_test.sh` would not be picked up by the existing gate without
   adding a second harness path.
2. Go's `t.TempDir()` already gives us per-test isolation, automatic
   cleanup, and a recordable error trail through `*testing.T`. A bash
   harness would need to roll equivalents by hand.
3. The four sibling Go assertions (XML parse, INI parse, grep
   `hush supervise`, grep absence of active `hush request --exec`) belong
   in the same harness — splitting them across bash + Go would duplicate
   build-tag wiring without benefit.

**Alternatives considered.**
- `deploy/install_test.sh` (bash). Rejected: a second test runner the CI
  gate would have to learn about; harder cross-platform assertions
  (the test must run identically on macOS and Linux CI runners).
- `internal/deploy/install_test.go`. Rejected: nothing under `internal/`
  is Go *code* for this chunk — the artefacts are not a Go package. The
  test lives at `tests/deploy/` so the directory layout reflects that
  these are integration-only assertions about repository files, not
  package-internal unit tests.

**Files produced.** `tests/deploy/install_test.go` (idempotency, FR-025
acceptance), `tests/deploy/smoke_test.go` (FR-012/016/019/026 smoke
asserts), `tests/deploy/testdata/tmutil_stub.sh` (executable shim placed
on PATH during the macOS-flavour test invocation).

---

## R-002 — `HUSH_INSTALL_ROOT` staging prefix (the chroot-style env knob)

**Decision.** `install.sh` honours a `HUSH_INSTALL_ROOT` environment
variable (default empty string). All destination paths are prefixed with
its value. Concretely:

| Resource                    | Effective path                                                           |
|-----------------------------|--------------------------------------------------------------------------|
| Binary (both OSes)          | `${HUSH_INSTALL_ROOT}${PREFIX}/bin/hush`                                 |
| State dir (macOS default)   | `${HUSH_INSTALL_ROOT}${HUSH_STATE_DIR}`  → `…/usr/local/var/hush`        |
| State dir (Linux default)   | `${HUSH_INSTALL_ROOT}${HUSH_STATE_DIR}`  → `…/var/lib/hush`              |
| Service file (macOS)        | `${HUSH_INSTALL_ROOT}/Library/LaunchDaemons/hush.plist`                  |
| Service file (Linux)        | `${HUSH_INSTALL_ROOT}/etc/systemd/system/hush.service`                   |

`HUSH_INSTALL_ROOT` is **not** mentioned in the spec — spec FR-004 fixes
the *resolved* paths, but it does not forbid a staging knob. The knob is
solely for the Go integration test to run install.sh inside
`t.TempDir()` without root privilege or polluting `/Library/`. Operators
should not set it.

**Rationale.** Without this knob, the test cannot exercise install.sh's
real codepath — only a fake. With the knob, the test runs the same
script that ships to operators, just rooted at `$TMPDIR/install-root-X`.

**Alternatives considered.**
- `chroot` / Linux user namespaces. Rejected: requires CAP_SYS_ADMIN on
  the runner and is not available in standard `go test` invocations.
- `sed`-rewriting install.sh paths in the test. Rejected: tests a
  rewritten copy, not the artefact that ships.
- Per-step `--prefix` flags. Rejected: an extra surface area to maintain
  and a footgun if operators pass the wrong combination.

**Constitution check.** `HUSH_INSTALL_ROOT` is operator-facing (any
operator could in theory set it), but it does not encode any
operator-specific value — Constitution I is satisfied. The next-steps
banner does **not** print `HUSH_INSTALL_ROOT`-aware paths (the banner
always shows the resolved system paths the operator will actually use,
because real operators run with `HUSH_INSTALL_ROOT=""`).

---

## R-003 — `HUSH_USER` defaults per platform

**Decision.** `HUSH_USER` defaults to `_hush` on macOS and `hush` on
Linux, matching each OS's convention for system-user names.

**Rationale.**
- macOS reserves the `_` prefix for system service users (e.g. `_postgres`,
  `_jabber`, `_unbound`). `_hush` reads as a system service to any
  operator inspecting `dscl . list /Users` or the plist's `<UserName>`.
- Linux historically uses bare names for `useradd --system` accounts
  (e.g. `postgres`, `redis`, `nginx`). `hush` is consistent.
- Both defaults are space-free and contain no operator-specific
  identifiers, satisfying spec Clarification A2 (space-free) and
  Constitution I.

**Alternatives considered.**
- `hush` on both OSes. Rejected: violates the macOS convention; an
  experienced macOS operator would be surprised.
- Force the operator to supply `HUSH_USER`. Rejected: spec FR-006
  requires a default ("defaulting to an OS-appropriate system-user name
  when the env var is unset").

**Idempotent creation.** `install.sh` invokes:
- macOS: `dscl . -read "/Users/${HUSH_USER}" >/dev/null 2>&1 || dscl . -create "/Users/${HUSH_USER}" UserShell /usr/bin/false`, then sets a non-conflicting `UniqueID`, `PrimaryGroupID`, `NFSHomeDirectory`, and `RealName`.
- Linux: `getent passwd "${HUSH_USER}" >/dev/null || useradd --system --shell /usr/sbin/nologin --home-dir /nonexistent --no-create-home "${HUSH_USER}"`.

Both invocations are skipped if the account already exists (spec FR-006
idempotency contract).

---

## R-004 — launchd `Label` is `com.hush.server` (fixed, not substituted)

**Decision.** The plist's `<key>Label</key>` value is the literal string
`com.hush.server`. install.sh does NOT substitute it. The label
identifies the hush vault server daemon on every host — it is not
operator-specific.

**Rationale.**
- The label is the `com.hush.server` *product* identifier, not an
  operator's daemon name. Constitution I forbids operator-specific
  identifiers; product identifiers are fine.
- A fixed label simplifies operator runbooks: `launchctl unload
  /Library/LaunchDaemons/hush.plist` and `launchctl list | grep
  com.hush.server` are stable across hosts.
- Per-daemon supervisor labels (for OpenClaw, Hermes, etc.) are the
  operator's responsibility via `supervise-launch.sh.template` — those
  belong to per-operator overlays, not to this chunk.

**Alternatives considered.**
- Operator-customisable label via install.sh substitution. Rejected:
  install.sh would need an env knob (`HUSH_LABEL`?), the plist would
  need a tokeniser, and no spec or chunk-doc requirement demands it.
  Lock and move on.

---

## R-005 — Config-file paths the service file points to

**Decision.** The plist sets `ProgramArguments` to
`["/usr/local/bin/hush", "serve", "--config", "/usr/local/etc/hush/config.toml"]`.
The systemd unit sets
`ExecStart=/usr/local/bin/hush serve --config /etc/hush/config.toml`.
install.sh does **not** create the config file — the operator generates
it via `hush init` separately. SDD-29 is explicitly out of scope for
config-file authoring.

**Rationale.** Locked by SDD-29 chunk-doc "Implementation contract (HOW
— locked)" lines 161–169. Both paths follow each OS's `${PREFIX}/etc`
vs `/etc` convention.

**Constitution X.** The config file (when later created by the
operator) belongs to `${HUSH_USER}` at mode `0640`. install.sh does not
chown it because install.sh does not write it.

---

## R-006 — `tmutil` interaction model & idempotency

**Decision.** install.sh invokes `tmutil addexclusion "${HUSH_STATE_DIR}"`
unconditionally on macOS. `tmutil addexclusion` is idempotent by Apple's
documented contract: a second call against an already-excluded path is a
recognised no-op (`tmutil` returns 0 and prints nothing new). install.sh
therefore does NOT need a pre-check via `tmutil isexcluded` — it relies
on `tmutil`'s native idempotency.

**Test contract.** The Go integration test replaces `tmutil` on PATH
with a recording stub. The stub appends its argv to
`$TMPDIR/tmutil.log` and exits 0. The test asserts that exactly **one**
`addexclusion` invocation appears in the log across two install.sh
runs against the same `HUSH_STATE_DIR` (because the stub is dumb — it
does not implement Apple's no-op behavior; the assertion catches a
regression where install.sh *would* invoke tmutil twice on real macOS).

**Hard-fail on missing `tmutil`.** Spec edge case: a macOS host with no
`tmutil` (rare, but possible on stripped-down images). install.sh runs
`command -v tmutil >/dev/null || die "tmutil not found; Constitution XI
non-negotiable"` and exits non-zero with an actionable error.

**Rationale.** Constitution XI: "vault state is sensitive … No Time
Machine inclusion." Silently skipping the exclusion because the binary
is missing would violate the principle.

---

## R-007 — Keychain banner content (FR-003 / SC-004)

**Decision.** install.sh prints a fixed banner on success. The macOS
banner contains a `security add-generic-password` invocation with the
literal resolved binary path under `-T`. Example post-substitution
output for the default install:

```
Next steps:
  1. Generate or recover your vault passphrase, then store it:
       security add-generic-password \
         -a "${HUSH_USER}" -s "hush-vault-passphrase" -T "/usr/local/bin/hush" \
         -U -w "<PASSPHRASE>"
  2. Run `hush init` interactively to create the vault.
  3. See docs/CLEAN-MACHINE.md for the per-machine client registration steps.
```

install.sh does NOT pipe the passphrase. It does NOT read the
passphrase. The operator runs the command separately after install
exits — install.sh creates zero Keychain entries (FR-003).

**Byte-identical re-run (FR-001 + spec Acceptance Scenario 3 of US3).**
The banner is constructed with `printf` of fixed format strings and
substituted env-resolved paths only. No timestamps, no hostnames, no
random values. Two runs in the same env produce byte-identical stdout.

**Linux banner.** No Keychain block; instead a line pointing to the
clean-machine doc for systemd-credential or env-var passphrase delivery.
The Linux passphrase mechanism is out of SDD-29 scope.

**Rationale.** The banner is the verification target for SC-004. The
`-T` arg's value (`/usr/local/bin/hush` for default install) is exactly
the path install.sh placed the binary at — the test captures install.sh
stdout and string-matches `-T "<that path>"`.

**Anti-contract.** No `-T` wildcard. No second `-T` arg. No
`security add-generic-password -A` (allow-all-apps). Test grep asserts
none of those forms appear.

---

## R-008 — Idempotency primitives in install.sh

**Decision.** install.sh uses three patterns, in this order of
preference:

1. `install -d -m 0700 -o "${HUSH_USER}" "${dir}"` for directories. The
   `install(1)` utility silently no-ops when the directory exists at the
   correct mode/owner; otherwise it adjusts. macOS and GNU coreutils
   both ship a compatible `install` binary.
2. `install -m <mode> "${src}" "${dst}"` for the binary and service
   file. Same semantics. The mode is asserted on every run; ownership
   is set via a follow-up `chown` only when the destination's owner
   differs from `${HUSH_USER}` (avoids touching the inode on every
   run — important because some launchd configurations re-verify
   file owners).
3. Explicit `if ! …; then …; fi` guards for non-`install(1)` actions:
   user creation (R-003) and `tmutil addexclusion` (R-006).

**Byte-identical filesystem.** After both install.sh runs:
- The binary's mtime may change if `install -m 0755` saw a newer mtime
  on the source. The integration test fixes the source binary's mtime
  before each run so the assertion is filesystem-state-identical
  (size, mode, owner, content hash), not mtime-identical. Per spec
  Acceptance Scenario 2 of US1 ("filesystem state identical to the
  post-first-run state") — interpreted as observable state, not raw
  inode timestamps.

**Alternatives considered.** `cp` with manual `stat`-then-chmod
sequences. Rejected: more shell lines for the same outcome and a
known footgun where `cp` can fail to update owner without an explicit
`-p` (which interacts poorly with stripped-down OSes).

---

## R-009 — Bash dialect, `#!` line, `set -euo pipefail`

**Decision.** install.sh and `supervise-launch.sh.template` both start
with `#!/usr/bin/env bash` and `set -euo pipefail`. Neither file uses
bash 4-only constructs (associative arrays, `${var,,}`); they target
bash 3.2 (macOS's pre-installed version) so the system bash works
without homebrew.

**Rationale.**
- `set -e` (exit on error) is mandatory for an installer: partial state
  is the failure mode we are designing against.
- `set -u` (unset-var trap) catches mistyped env var names early.
- `set -o pipefail` propagates pipeline failures (e.g.
  `command | tee >(grep …)` patterns) — install.sh uses no pipes today,
  but enabling it now is cheap and prevents regressions.
- `#!/usr/bin/env bash` finds bash via PATH, working on hosts with
  homebrew-bash at `/opt/homebrew/bin/bash` as well as system bash at
  `/bin/bash`.

**Bash 3.2 compatibility.** Validated by `bash -n` and `bash --posix`
parse pass on macOS's `/bin/bash`. No `mapfile`, no `readarray`, no
`${var,,}` lowercase substitution.

---

## R-010 — Supervise-launch template structure

**Decision.** The template is a 30-ish-line file with three sections:

1. **Header block** (comments only): a numbered substitution guide
   listing each placeholder, its meaning, and where the customised
   copy belongs in the per-daemon directory layout. The block contains
   a load-bearing **DO NOT** warning against `hush request --exec`.
2. **Placeholder guard** (executable): a `grep`-based self-check that
   exits non-zero if any unsubstituted placeholder remains in the
   currently-running script.
3. **Exec line** (single line): `exec /usr/local/bin/hush supervise --config <CONFIG_PATH>`.

**Three placeholders.** `<NAME>`, `<KEYCHAIN_ITEM>`, `<CONFIG_PATH>`.
The spec (FR-020) only requires the first two, but a CONFIG_PATH
substitution is mechanically necessary because the exec line references
the per-daemon TOML; otherwise the operator would have to edit the
exec line itself, which the chunk-doc locked as a single-line core.

**Placeholder guard rationale.** Spec edge case: "the unmodified
placeholder strings must cause the script to fail at startup, not
silently exec something unintended." A trivial unsubstituted exec line
would either parse OK (bash treats `<X>` as a redirection) or fail with
a confusing redirect-target message. The explicit `grep` guard fails
loudly with an actionable message and exit code 78 (`EX_CONFIG` — the
hush stale-creds contract; the init system gets a familiar signal).

**Grep guard implementation.** Stores the placeholder tokens with
indirection so the guard line itself does NOT match (otherwise the
guard would always fire):

```bash
__hush_placeholders='<NAME> <KEYCHAIN_ITEM> <CONFIG'_'PATH>'
if grep -qE 'NAME|KEYCHAIN_ITEM|CONFIG_PATH' "${BASH_SOURCE[0]}" \
     | grep -F '<'; then
  echo "ERROR: substitute placeholders before running" >&2
  exit 78
fi
```

The exact form is finalised in implementation; the contract is
"unsubstituted-placeholder run-time exits non-zero with a message" and
"`hush request --exec` does NOT appear in any executable line".

**FR-019 compliance.** The string `hush request --exec` appears
exactly once — inside the header comment, inside an explicit
"DO NOT USE THIS" warning. The grep-based FR-026 check parses for
`hush request --exec` only on lines that do NOT start with `#`, so the
warning comment passes.

---

## R-011 — Non-shell artefacts: plist and unit file structure

**Decision.**

`deploy/hush.plist` (canonical content; no install.sh substitution):

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
                       "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>            <string>com.hush.server</string>
  <key>ProgramArguments</key> <array>
    <string>/usr/local/bin/hush</string>
    <string>serve</string>
    <string>--config</string>
    <string>/usr/local/etc/hush/config.toml</string>
  </array>
  <key>UserName</key>         <string>_hush</string>
  <key>RunAtLoad</key>        <true/>
  <key>KeepAlive</key>        <true/>
  <key>StandardOutPath</key>  <string>/usr/local/var/log/hush.out.log</string>
  <key>StandardErrorPath</key><string>/usr/local/var/log/hush.err.log</string>
</dict>
</plist>
```

`deploy/hush.service` (canonical content; install.sh **does** substitute
`@HUSH_USER@` → resolved user at copy time):

```ini
[Unit]
Description=hush — Discord-gated secrets broker
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=@HUSH_USER@
ExecStart=/usr/local/bin/hush serve --config /etc/hush/config.toml
Restart=on-failure
RestartSec=5s
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

**Why the asymmetry (plist literal, unit templated)?** macOS's default
`_hush` matches the literal plist; users wanting a different macOS
account override `HUSH_USER` and `install.sh` does a one-line sed of
`<string>_hush</string>` → `<string>${HUSH_USER}</string>` during the
copy. The systemd unit uses the more explicit `@HUSH_USER@` token to
make the substitution obvious. Both approaches preserve FR-013/FR-017
(no operator-specific values committed) because the substituted value
is a *system-user name*, not an operator-specific name like a daemon or
Tailscale tag.

**Hardening directives.** `NoNewPrivileges`, `ProtectSystem=strict`,
`ProtectHome=true`, `PrivateTmp=true` are added to the systemd unit
because Constitution V/XI favor loud-and-locked-down defaults. The
spec (Assumptions) explicitly notes "plan-phase decides any further
hardening directives". launchd has fewer equivalent directives;
StandardOut/Err redirection is the only addition.

**Anti-contract.** Neither file contains a Tailscale tag, Discord
channel ID, hostname, or daemon name. The plist's `Label` and the
unit's `Description` reference `hush` itself (the product), which is
allowed.

---

## R-012 — Mode bits and ownership

**Decision.** All file modes and ownership choices:

| Artifact                                              | Mode  | Owner          | Group   |
|-------------------------------------------------------|-------|----------------|---------|
| `${PREFIX}/bin/hush`                                  | 0755  | root           | wheel/root |
| `/Library/LaunchDaemons/hush.plist`                   | 0644  | root           | wheel   |
| `/etc/systemd/system/hush.service`                    | 0644  | root           | root    |
| `${HUSH_STATE_DIR}` (vault state)                     | 0700  | ${HUSH_USER}   | (system)|
| `${HUSH_STATE_DIR}/*` (created later by hush)         | 0600  | ${HUSH_USER}   | (system)|

**0700 on state dir** is Constitution X non-negotiable (vault state is
sensitive). FR-002a fixes this.

**0644 on the service file** matches every other system service file
on each OS; root-owned so unprivileged tampering is rejected by the
init system at load.

---

## R-013 — `bash -n` and shellcheck in CI

**Decision.** A CI step runs `bash -n` against every committed `.sh`
and `.template` file under `deploy/`. Where `shellcheck` is available
on the runner, a second step runs it with `--severity=warning` and
fails the build on any finding. The "where available" wording is
inherited from SDD-29's "shellcheck if available" line; CI runners that
lack shellcheck must document the gap in their setup notes.

**bash -n is the absolute floor (FR-024 + SC-008).** It runs in <1ms
on each file and rejects syntax errors immediately. No skipping under
any condition.

**Where these run.**
- Local: pre-commit hook (already configured in
  `.github/tech-conventions/pre-commit.md`) picks up shell files.
- CI: a new step in the existing GH Actions workflow runs both checks
  on every push to `029-deploy-artifacts` and on every PR.

The exact CI YAML edit is a Phase-2 (tasks) item, not Phase-1.

---

## R-014 — No new Go dependencies; Constitution XI

The Go integration test imports only stdlib (`testing`, `os`,
`os/exec`, `path/filepath`, `runtime`, `strings`, `bytes`, `bufio`,
`encoding/xml` for plist parse, `gopkg.in/ini.v1`?). The plist
verification can use `encoding/xml` (a generic XML decode that the
plist already validates against — strict-mode XML rejects invalid
plists). The systemd `.service` file is INI-style; the test parses it
with a small ad-hoc reader (no `gopkg.in/ini.v1` dependency).

**Why not `plutil`?** `plutil -lint` is the macOS-native plist checker,
but it is not available on Linux CI runners. A pure-Go `encoding/xml`
parse covers FR-012 (parseable as valid XML) on every platform. For
deeper plist-schema validation, a follow-up chunk can introduce
`howett.net/plist` after a Constitution-XI justification — out of
SDD-29 scope.

**Constitution XI satisfied.** Zero new direct Go dependencies.

---

## R-015 — Cross-references between SDD-29 and adjacent chunks

| Adjacent chunk | Interaction                                                             |
|----------------|--------------------------------------------------------------------------|
| SDD-15 (keychain pkg) | Provides the runtime Keychain reader used by `hush supervise` / `hush serve`. install.sh prints the `-T` invocation that adds the entry; the *reader* is SDD-15's job. SDD-29 does NOT call into `internal/keychain`. |
| SDD-23 (CLI surface)  | Provides `hush serve` and `hush supervise` subcommands. The plist and unit reference `hush serve` only; the launcher template references `hush supervise`. Both subcommand names are locked by SDD-23 (and Constitution VII). |
| SDD-30 (examples)     | Ships `deploy/examples/supervisors/example-daemon.toml`. The `<CONFIG_PATH>` placeholder in `supervise-launch.sh.template` is exactly the kind of path an operator points at the SDD-30 example. SDD-30 is blocked by SDD-29; the launcher template existing is what unblocks it. |
| SDD-32 (release tag)  | The four files are part of the v0.1.0 ship. v0.1.0 release notes will reference `deploy/install.sh` as the install entry point. |

No SDD-29 file imports any internal Go package — the artefacts are
shell/XML/INI, and the test is pure stdlib Go.

---

## Resolved status

All NEEDS CLARIFICATION markers from the spec's Clarifications section
were resolved in the spec itself (Session 2026-05-14). No additional
clarifications were introduced by the plan-phase research. The plan
proceeds with **zero open questions**.
