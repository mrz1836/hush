# Contract — `supervise-launch.sh.template`

The launcher template is the *only* file in SDD-29 the operator copies
and edits. It is not installed by `install.sh`; the operator picks it
up by hand once per daemon.

---

## Committed file shape

```bash
#!/usr/bin/env bash
#
# supervise-launch.sh.template — per-daemon launcher for `hush supervise`
#
# WHAT THIS FILE IS
#   A template the operator copies once per long-running daemon. The
#   resulting script is registered with launchd (macOS) or systemd
#   (Linux) as the per-daemon entry point.
#
# SUBSTITUTE THE FOLLOWING THREE PLACEHOLDERS BEFORE USE:
#
#   <NAME>          The logical daemon name (e.g. "openclaw"). Used as
#                   the operator-chosen identifier. By convention the
#                   substituted file is renamed <NAME>-hush-launch.sh.
#
#   <KEYCHAIN_ITEM> The macOS Keychain item name holding the per-daemon
#                   client passphrase (e.g. "hush-openclaw"). hush
#                   supervise reads this via its ACL-restricted Keychain
#                   reader; this script does NOT shell out to `security`.
#
#   <CONFIG_PATH>   Absolute path to the supervisor TOML
#                   (e.g. /Users/.../.hush/supervisors/openclaw.toml).
#
# DO NOT use `hush request --exec` for daemons. It re-prompts the
# operator on every restart, which defeats the supervisor TTL discipline
# and trains the operator to auto-approve. Use `hush supervise` always.
#
# WHERE TO PUT THIS FILE
#   macOS: invoke via /Library/LaunchDaemons/<NAME>.plist
#   Linux: invoke via /etc/systemd/system/<NAME>.service
#
set -euo pipefail

# Pre-flight: refuse to run with unsubstituted placeholders.
# (The tokens below are concatenated so this guard line does not match itself.)
if grep -qE '<''NAME>|<''KEYCHAIN_ITEM>|<''CONFIG_PATH>' "${BASH_SOURCE[0]}"; then
  echo "supervise-launch: placeholders not substituted; refusing to run" >&2
  exit 78
fi

exec /usr/local/bin/hush supervise --config <CONFIG_PATH>
```

---

## Asserted contracts

`TestDeploy_LauncherTemplateExecsSupervise` enforces:

| Assertion                                                                      | Source            |
|--------------------------------------------------------------------------------|-------------------|
| `bash -n` parses the file with no error.                                       | FR-023 / FR-024   |
| File contains the literal string `hush supervise`.                             | FR-018 / SC-006   |
| File contains zero **active** `hush request --exec` lines.                     | FR-019 / SC-006   |
| File contains placeholders `<NAME>`, `<KEYCHAIN_ITEM>`, `<CONFIG_PATH>`.       | FR-020            |
| File contains a header comment block explaining each placeholder + warning.   | FR-021            |
| File contains zero operator-specific tokens (grep against denylist).           | FR-022 / SC-007   |

### "Active" vs commented `hush request --exec`

The string `hush request --exec` appears once, inside an explicit
**DO NOT use** warning comment. The grep used by the assertion is:

```
grep -nE '^[[:space:]]*[^#]' supervise-launch.sh.template \
  | grep -F 'hush request --exec'
```

(Read only lines that do NOT start with `#`, then search for the
forbidden string.) Zero matches is the pass condition.

### Pre-flight guard semantics

The guard line uses string concatenation (`'<''NAME>'`) so the line
itself does not match the grep. After substitution, all three tokens
are gone and the guard returns 1 (no match) → script continues to
exec.

If an operator forgets to substitute any token:
- The guard matches → exits 78 (`EX_CONFIG`).
- launchd / systemd records the exit code in its log; the operator sees
  it via `launchctl print` / `journalctl -u <name>.service`.
- No partial state, no `hush supervise` invocation against an unset
  config path.

---

## What this file is NOT

- NOT installed by `install.sh`. The operator copies it themselves.
- NOT operator-specific. It contains no real daemon name, no hostname,
  no Tailscale tag — only the three placeholders.
- NOT a config file. The supervisor TOML at `<CONFIG_PATH>` is the
  config; this script's only job is to exec the supervisor against it.
- NOT a keychain-read shim. `hush supervise` reads the Keychain via its
  own ACL-restricted reader. The `<KEYCHAIN_ITEM>` placeholder exists
  only so the operator's customised copy carries a clear comment
  identifying which entry the supervisor will use.
