# Contract — Startup Checks

The chassis runs a fixed sequence of startup checks before binding
any socket. Each failure short-circuits with a distinct sentinel
error and a non-zero exit. This contract locks the sequence,
the sentinel names, and the dependency-injection points.

---

## Sequence (FR-001, FR-003, locked)

```text
1. clock_sync       → ErrClockUnsynchronised
2. file_modes       → ErrFileModeLoose
3. tailscale_bind   → ErrBindNotOnTailscale
4. state_dir        → ErrStateDirUnsafe
```

The order is observable from outside — SC-002 asserts that on a
host with multiple misconfigurations, the *first* failing check's
error is returned and the later checks are not executed.

---

## Check details

### 1. `clock_sync` → `ErrClockUnsynchronised` (FR-004)

**Asserts:**

- The host clock is NTP-synchronised.
- Absolute drift is `≤ Cfg.Security.MaxClockDrift` (default 60 s).
- The probe completes within a bounded timeout (5 s — internal
  constant).

**Probe injection:**

```go
type ClockSyncProbe func(ctx context.Context) (synced bool, drift time.Duration, err error)
```

`Deps.ClockSyncProbe` defaults to a platform helper:

- darwin: parses `systemsetup -getusingnetworktime`.
- linux: parses `timedatectl show --property=NTPSynchronized`.

A `Deps.ClockSyncProbe` of `nil` falls back to the default. Tests
inject a deterministic probe.

**Skip condition:** if `Cfg.Security.RequireNTPSync == false`, the
check is skipped entirely. Default is `true` (FR-17).

### 2. `file_modes` → `ErrFileModeLoose` (FR-005)

**Asserts:**

- Every regular file under `Cfg.Server.StateDir` has
  `Mode().Perm() ≤ 0600`.
- The state directory itself has `Mode().Perm() ≤ 0700`.
- Every nested directory has `Mode().Perm() ≤ 0700`.

**Implementation:** `filepath.WalkDir(stateDir, ...)`. Symlinks
are not followed. The first offending entry returns the error.

**Error contents:** the wrapped error includes the offending
path and a category ("regular file" or "directory"); it does
**not** read or include any byte of the file.

**Skip condition:** if `Cfg.Security.RequireFileModeChecks ==
false`, the check is skipped. Default is `true`.

### 3. `tailscale_bind` → `ErrBindNotOnTailscale` (FR-006)

**Asserts:**

1. `Cfg.Server.ListenAddr.Addr()` is inside `100.64.0.0/10`
   (Tailscale CGNAT range). This is a re-check of what
   `internal/config.validateTailscaleAddrPort` already validated
   at TOML time.
2. The configured address belongs to at least one local
   interface (`net.InterfaceAddrs()`).

**Failure modes captured:**

- `0.0.0.0` → CGNAT-range check fails.
- empty host → CGNAT-range check fails.
- loopback `127.0.0.1` → CGNAT-range check fails.
- public address → CGNAT-range check fails.
- valid CGNAT IP not bound to any local interface → interface-
  table check fails (the host moved between Tailscale identities).

### 4. `state_dir` → `ErrStateDirUnsafe` (FR-007)

**Asserts:**

1. `os.Lstat(stateDir)` succeeds and the entry is not a symlink.
2. The entry is a regular directory (`info.IsDir()`).
3. The entry is owned by the running user
   (`info.Sys().(*syscall.Stat_t).Uid == os.Getuid()`).

**Failure modes captured:**

- missing directory.
- not a directory (regular file, named pipe, etc.).
- owned by another user (`root`, `nobody`, etc.).
- symlink at the state-dir root (refused — could be moved
  between Lstat and WalkDir).

---

## Sentinel errors

```go
package server

import "errors"

var (
    ErrClockUnsynchronised = errors.New("server: startup: clock unsynchronised")
    ErrFileModeLoose       = errors.New("server: startup: file mode laxer than 0600/0700")
    ErrBindNotOnTailscale  = errors.New("server: startup: listen address not on Tailscale CGNAT")
    ErrStateDirUnsafe      = errors.New("server: startup: state directory missing or unsafe")
)
```

Callers match via `errors.Is(err, ErrClockUnsynchronised)`. Each
check returns a wrapped error: `fmt.Errorf("server: clock_sync: drift %v: %w", drift, ErrClockUnsynchronised)`.

---

## Refuse-to-start semantics (FR-002)

`Run` invokes the checks before calling `httpServer.ListenAndServe`.
If any check returns non-nil:

1. The error is logged at ERROR with a structured field naming
   the failed check.
2. An `AuditServerStart` event is emitted with `Detail["status"]
   = "refused"` and `Detail["check"] = <check name>`.
3. `Run` returns the error.
4. **No listener is opened.** SDD-14 (`cmd/hush serve`) sees the
   error and exits non-zero — the chassis does not call `os.Exit`
   itself (Constitution IX: panics/exits in `main` only).

---

## Test fakes & injection

Each check is unit-testable with a fake host:

- **clock_sync**: fake `ClockSyncProbe` that returns scripted
  `(synced, drift, err)` triples.
- **file_modes**: a `t.TempDir()` populated with files of the
  required modes; `Cfg.Server.StateDir` points to the temp dir.
- **tailscale_bind**: `Cfg.Server.ListenAddr` is overridden;
  the interface-table check is the only platform-dependent
  part — the test runs on a host that has at least one local
  interface in 100.64.0.0/10 (Tailscale-on-test-host) **or**
  the integration test injects a fake `InterfaceLister` (see
  below).
- **state_dir**: `t.TempDir()` with chmod variations.

For the interface-table check, the chassis exposes a single
unexported injection point:

```go
type interfaceLister func() ([]net.Addr, error)

// default: net.InterfaceAddrs
```

Tests in `startup_checks_test.go` inject a fake `interfaceLister`
to keep unit tests independent of the host's Tailscale state.
Integration tests (`integration_test.go`) use the real lister.

---

## Required tests (SC-001, SC-002)

| Test name | What it asserts |
|-----------|-----------------|
| `TestStartupChecks_RefusesUnsyncedClock` | `synced=false` → `ErrClockUnsynchronised`; no listener bound |
| `TestStartupChecks_RefusesClockDriftOver60s` | `drift=61s` → `ErrClockUnsynchronised` |
| `TestStartupChecks_RefusesLooseFileMode` | a 0644 file under StateDir → `ErrFileModeLoose` |
| `TestStartupChecks_RefusesLooseDirMode` | StateDir at 0755 → `ErrFileModeLoose` |
| `TestStartupChecks_RefusesPublicBind` | `ListenAddr=0.0.0.0:7743` → `ErrBindNotOnTailscale` |
| `TestStartupChecks_RefusesLoopbackBind` | `ListenAddr=127.0.0.1:7743` → `ErrBindNotOnTailscale` |
| `TestStartupChecks_RefusesUnsafeStateDir` | StateDir owned by another user → `ErrStateDirUnsafe` |
| `TestStartupChecks_RefusesMissingStateDir` | StateDir does not exist → `ErrStateDirUnsafe` |
| `TestStartupChecks_OrderedExecution` | host with all four misconfigurations → `ErrClockUnsynchronised` (first in order); a probe-counting test verifies `file_modes`, `tailscale_bind`, `state_dir` were not invoked |
| `TestStartupChecks_HappyPath` (integration) | a correctly-configured host → all checks pass and `Run` proceeds to bind |
