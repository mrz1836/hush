# Feature Specification: Deploy Artifacts (launchd plist + systemd unit + install.sh + supervisor launcher template)

**Feature Branch**: `029-deploy-artifacts`

**Created**: 2026-05-14

**Status**: Implemented (2026-05-14; commit `22d5fdd` on `029-deploy-artifacts`)

**Input**: User description: "deploy/: launchd plist + systemd unit + install.sh (idempotent, adds macOS tmutil exclusion, sets up Keychain entries with -T binary-path ACL, non-root) + supervise-launch.sh.template (execs hush supervise, NOT hush request --exec, with clearly-marked <NAME>/<KEYCHAIN_ITEM> placeholders); zero operator-specific names hard-coded"

## Clarifications

### Session 2026-05-14

- Q: FR-006 either-or for the non-root user account — create or refuse? → A: Auto-create the account if missing (OS-conventional system-user creation: `dscl` on macOS, `useradd --system` on Linux). The account name is read from `$HUSH_USER`, defaulting to an OS-appropriate value. The user-creation command is invoked at most once across repeated runs (idempotency contract).
- Q: What absolute path should install.sh use for the vault state directory (target of the macOS tmutil exclusion)? → A: `/usr/local/var/hush` on macOS, `/var/lib/hush` on Linux. Both overridable via `$HUSH_STATE_DIR`. Defaults are space-free to keep shell quoting and test assertions clean.
- Q: How does install.sh handle Keychain entry creation on macOS (where does the passphrase come from)? → A: install.sh creates **zero** Keychain entries itself. The next-steps banner prints copy-pasteable `security add-generic-password -T <resolved-binary-path> ...` invocations that the operator runs separately. FR-003 governs the printed commands (the `-T` ACL discipline); FR-025 asserts the banner content via stdout capture. install.sh never handles secret material, idempotency is trivial, and the test needs no Keychain stub.
- Q: Which absolute install paths should install.sh use for the platform service files? → A: macOS = `/Library/LaunchDaemons/hush.plist`; Linux = `/etc/systemd/system/hush.service`. System-wide, operator-installed, boot-time loaded regardless of user login. No env-var overrides in v0.1.0 (avoids a third override pair).
- Q: Should install.sh create the vault state directory, or assume it already exists? → A: install.sh creates `$HUSH_STATE_DIR` if missing via `mkdir -p`, then `chown $HUSH_USER` and `chmod 0700` (Constitution X — vault state is sensitive), then issues the macOS `tmutil addexclusion`. All four steps are idempotent on repeated runs.

## Overview

This chunk delivers the four files an operator needs to deploy hush on macOS or
Linux, shipped as committed artifacts under `deploy/`:

1. A launchd plist that runs the hush vault server on macOS as a non-root user.
2. A systemd unit that runs the hush vault server on Linux as a non-root user.
3. An idempotent installer script that lays down the binary, places the
   service file at the platform's expected location, creates the vault state
   directory with non-root ownership and `0700` mode, adds a Time Machine
   exclusion for that directory on macOS, and prints a next-steps banner
   containing the exact `security add-generic-password -T <binary-path> ...`
   commands the operator runs separately to populate the Keychain (install.sh
   itself creates no Keychain entries and handles no secret material).
4. A generic supervisor launcher template that operators copy and customise
   per daemon. The template execs `hush supervise` (the daemon path) and never
   `hush request --exec` (the interactive path), with clearly-marked
   placeholders that an operator substitutes for their specific daemon.

These artifacts are operator-facing infrastructure files, not Go code. The spec
defines WHAT they must guarantee for the operator and what they MUST NOT do.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - First-time install on a fresh trusted host (Priority: P1)

An operator who has just built or downloaded the hush binary needs a single
command that lays everything down: the binary in a standard location, the
service definition where the host's init system will pick it up, the vault
state directory with the right ownership and permissions, a printed banner
showing the exact `security add-generic-password` commands the operator runs
afterward to populate the Keychain with the correct binary-path ACL, and on
macOS an exclusion that ensures the vault state directory is never copied to
a Time Machine backup.

**Why this priority**: Without an installer, every operator improvises a
different layout, the Keychain ACL discipline drifts, and the vault state
directory gets backed up by default — directly violating Constitution XI. This
is the load-bearing artifact of the chunk.

**Independent Test**: Run `install.sh` in a clean temporary root with the
hush binary present and a stub `tmutil` in PATH. Assert: the binary appears at
the expected install path; the platform service file appears at the expected
system location; on macOS the stub `tmutil` records that an `addexclusion`
call was made with the configured vault state directory; on macOS the
captured stdout contains a `security add-generic-password -T <binary-path>
...` invocation in which `<binary-path>` is the resolved hush binary path
(install.sh itself creates zero Keychain entries — see FR-003); on Linux the
systemd unit file declares a non-root `User=`; the operator sees a
next-steps banner.

**Acceptance Scenarios**:

1. **Given** a clean install root with the hush binary in the source path and
   a stub `tmutil` on PATH, **When** the operator runs `install.sh` once,
   **Then** the binary, the platform service file, and (on macOS) the
   `tmutil addexclusion` invocation all materialise; install.sh prints a
   next-steps banner containing the exact `security add-generic-password -T
   <binary-path> ...` command the operator runs separately to populate the
   Keychain; and the script exits 0.
2. **Given** a clean install root with the hush binary present, **When** the
   operator runs `install.sh` twice in succession, **Then** the second run
   exits 0 with no destructive changes, no second user-creation invocation
   (the existing `$HUSH_USER` account is left untouched), no second
   `tmutil addexclusion` against the same state directory (or a no-op
   recognised by `tmutil`), no re-prompted installer questions, byte-
   identical printed banner, and a filesystem state identical to the
   post-first-run state.
3. **Given** the install completes on macOS, **When** the operator inspects
   install.sh's printed next-steps banner, **Then** the banner contains a
   `security add-generic-password -T <binary-path> ...` invocation in which
   `<binary-path>` is exactly the absolute path of the installed hush binary
   and contains no wildcard or other binary path.

---

### User Story 2 - Operator deploys a long-running daemon under a supervisor (Priority: P1)

An operator with one or more long-running daemons (each is a workload — an
agent runtime, a gateway service — that needs secrets at startup) needs a
launcher template they can copy per daemon, fill in two clearly-marked
placeholders, and register with launchd or systemd. The template MUST exec
`hush supervise`, because the supervisor lifecycle is the only way a daemon
survives crashes and restarts within a single Discord approval. The template
MUST NOT exec `hush request --exec`, because that path re-prompts the
operator on every restart and trains them to auto-approve.

**Why this priority**: The whole point of `hush supervise` is defeated if
operators copy a launcher that uses the interactive code path. Shipping the
correct template — with explicit warning comments and clearly-marked
placeholders — is how the project enforces Constitution IV in operator
practice. Same priority as User Story 1 because both define the v0.1.0
operator-facing surface; without either one the daemon path is unusable.

**Independent Test**: Read the template file. Assert it contains the literal
string `hush supervise`, contains zero un-warned occurrences of `hush request
--exec`, contains both placeholder tokens (one for the daemon name, one for
the Keychain item name) in clearly-marked form, and contains a header comment
explaining how to substitute them and warning against using the interactive
path.

**Acceptance Scenarios**:

1. **Given** an operator forking the template for a new daemon, **When** they
   read the file top-to-bottom, **Then** the header comments tell them
   exactly which two tokens to substitute, what each one means, where the
   substituted file should live, and warn against using the interactive
   request path.
2. **Given** an operator searches the committed template for an active
   `hush request --exec` invocation, **When** the search runs, **Then** it
   returns zero matches.
3. **Given** an operator searches the committed template for `hush
   supervise`, **When** the search runs, **Then** it returns at least one
   match — the line that execs the supervisor.
4. **Given** an operator substitutes both placeholders for their daemon and
   registers the resulting script with launchd or systemd, **When** the host
   boots, **Then** the init system invokes the supervisor (not an
   interactive request) and the daemon enters the bootstrap path documented
   in the daemon lifecycle scenarios.

---

### User Story 3 - Operator runs install.sh again after upgrading the binary (Priority: P2)

An operator who has installed hush once and later builds a newer binary needs
to re-run the installer to lay the new binary down. Re-running MUST be safe:
it MUST NOT delete or rotate the operator's existing Keychain items, MUST NOT
re-add a duplicate Time Machine exclusion, MUST NOT change file ownership in
ways that break the running service, and SHOULD leave the system in the same
state as a clean first install of the new binary.

**Why this priority**: Idempotency is the difference between an installer
that operators trust to re-run and one they avoid. Avoidance leads to manual
divergence between hosts. Lower than P1 because the first-install path
already exercises every step; this story is the discipline that the same
steps remain safe on re-run.

**Independent Test**: Run `install.sh` once in a clean temporary root; record
the resulting filesystem layout, file modes, and the calls made to the
stubbed `tmutil`. Run `install.sh` again in the same root. Assert: exit code
0; no second `tmutil addexclusion` invocation against the same path (or the
second invocation is a no-op recognised by `tmutil`); install.sh's captured
stdout from the second run is byte-identical to the first run's banner (per
FR-003 install.sh never mutates the Keychain); the service-file content and
mode are unchanged.

**Acceptance Scenarios**:

1. **Given** the install completed on macOS, **When** the operator re-runs
   `install.sh`, **Then** `tmutil addexclusion` is invoked at most once for
   the vault state directory across both runs (or the second invocation is
   a no-op recognised by `tmutil`).
2. **Given** the operator has populated Keychain entries by running the
   commands from install.sh's first-run next-steps banner, **When** the
   operator re-runs `install.sh`, **Then** the script does not touch the
   Keychain at all (install.sh creates zero entries on every run per FR-003)
   and does not prompt for any passphrase; the printed banner on the second
   run is byte-identical to the first run's banner.

---

### Edge Cases

- The host already has an older copy of hush at the install path — the
  installer must replace it without leaving a half-overwritten file at any
  point that the running service might exec.
- The `tmutil` binary is missing on a macOS host (rare — typically a
  stripped-down image) — the installer must fail loudly with an actionable
  error rather than silently skip the exclusion, because the exclusion is a
  Constitution XI non-negotiable.
- The operator runs `install.sh` on an unsupported OS (neither macOS nor
  Linux) — the installer must refuse with a clear error rather than partially
  install.
- The operator runs `install.sh` from a build tree where the source binary
  is not present — the installer must fail before any partial state is
  written.
- The operator copies the launcher template, registers the unmodified file
  (forgets to substitute placeholders), and the init system tries to run it
  — the resulting failure must be visible to the operator (the unmodified
  placeholder strings must cause the script to fail at startup, not silently
  exec something unintended).

## Requirements *(mandatory)*

### Functional Requirements

#### Installer (install.sh)

- **FR-001**: The installer MUST be idempotent. Running it twice in
  succession MUST leave the system in the same observable state as a single
  run, with the second run exiting 0.
- **FR-002**: On macOS, the installer MUST add a Time Machine exclusion for
  the vault state directory used by the hush server. The vault state
  directory path is read from `$HUSH_STATE_DIR`, defaulting to
  `/usr/local/var/hush` on macOS and `/var/lib/hush` on Linux. The
  defaults are deliberately space-free to keep shell quoting and stubbed
  `tmutil` assertions unambiguous. Skipping the exclusion step is a
  Constitution XI non-negotiable.
  - **FR-002a**: As a prerequisite to FR-002 (and so the daemon can start
    after install on a fresh host), the installer MUST ensure
    `$HUSH_STATE_DIR` exists with the correct ownership and mode before
    invoking `tmutil`: `mkdir -p` the directory, `chown` it to
    `$HUSH_USER`, and `chmod 0700`. All three steps MUST be idempotent on
    re-run. Mode `0700` aligns with Constitution X (vault state is
    sensitive); group-readable modes MUST NOT be used.
- **FR-003**: On macOS, install.sh MUST NOT create any Keychain entries
  itself; it MUST NOT prompt for, read, or otherwise handle a passphrase.
  Instead, the next-steps banner printed by install.sh (FR-008) MUST
  include copy-pasteable `security add-generic-password` invocations that
  the operator runs separately. Every such invocation in the printed
  banner MUST include `-T <resolved-binary-path>`, where
  `<resolved-binary-path>` is the absolute path at which install.sh placed
  the hush binary (FR-004). Wildcard ACLs MUST NOT appear in the printed
  commands.
- **FR-004**: The installer MUST place the hush binary at a well-known
  filesystem location on the host (defaulting to a system path operators
  expect — `${PREFIX:-/usr/local}/bin/hush`). The installer MUST place the
  platform's service file at the following operator-installed,
  boot-time-loaded location: `/Library/LaunchDaemons/hush.plist` on macOS,
  `/etc/systemd/system/hush.service` on Linux. These paths are NOT
  operator-overridable in v0.1.0.
- **FR-005**: The installer MUST detect macOS vs Linux at runtime and
  install only the artifact appropriate for the host. On any other OS it
  MUST refuse to proceed with a clear error message.
- **FR-006**: The installer MUST NOT install hush to run as root. The
  service it lays down MUST run as a non-root user. The account name is
  read from `$HUSH_USER`, defaulting to an OS-appropriate system-user name
  when the env var is unset. If the named account does not yet exist on
  the host, the installer MUST create it using the OS-conventional
  system-user creation mechanism (e.g. `dscl` on macOS, `useradd --system`
  on Linux). The creation step MUST be idempotent: when the named account
  already exists, the installer MUST NOT attempt to recreate it or alter
  its uid/gid/shell/home.
- **FR-007**: The installer MUST refuse to proceed with a clear error if a
  required input (the source hush binary) is missing, rather than producing
  partial state.
- **FR-008**: The installer MUST print a next-steps banner on success
  pointing the operator to the documentation that explains the remaining
  configuration (init the vault, register clients, deploy supervisors).
- **FR-009**: The installer MUST NOT hard-code any operator-specific value:
  no specific daemon names, no specific hostnames, no specific Tailscale
  tags, no specific Discord IDs.

#### launchd plist (macOS server)

- **FR-010**: The launchd plist MUST declare a non-root user as the run-as
  account for the hush server.
- **FR-011**: The launchd plist MUST point to the same absolute binary path
  the installer uses, so the Keychain ACL (FR-003) and the launchd execution
  path agree.
- **FR-012**: The launchd plist MUST be parseable as valid XML / a valid
  launchd plist by the host's launchd loader.
- **FR-013**: The launchd plist MUST NOT hard-code any operator-specific
  value (FR-009 applies here too).

#### systemd unit (Linux server)

- **FR-014**: The systemd unit MUST declare a non-root user as the run-as
  account for the hush server.
- **FR-015**: The systemd unit MUST point to the same absolute binary path
  the installer uses (parallel to FR-011).
- **FR-016**: The systemd unit MUST be parseable as a valid systemd unit
  file (loadable INI-style structure with the expected sections).
- **FR-017**: The systemd unit MUST NOT hard-code any operator-specific
  value (FR-009 applies here too).

#### Supervisor launcher template (supervise-launch.sh.template)

- **FR-018**: The launcher template MUST exec `hush supervise` to start the
  daemon under the supervisor lifecycle.
- **FR-019**: The launcher template MUST NOT exec `hush request --exec` for
  any code path. The literal string `hush request --exec` MUST NOT appear
  in any executable line of the template (it MAY appear inside a clearly
  marked "DO NOT use this" warning comment that an automated grep can
  distinguish from an active invocation).
- **FR-020**: The launcher template MUST contain clearly-marked
  placeholders for at least the daemon's logical name and the daemon's
  Keychain item name. Placeholders MUST be visually distinct from
  surrounding code (e.g. wrapped in angle brackets or all-caps with a
  conventional sigil) so that an operator searching the file for
  unsubstituted markers cannot miss them.
- **FR-021**: The launcher template MUST contain a header comment block
  that (a) explains every placeholder, (b) tells the operator where the
  customised copy belongs in the per-daemon directory layout, and (c) warns
  explicitly that `hush request --exec` is not a substitute and re-prompts
  on every restart.
- **FR-022**: The launcher template MUST NOT hard-code any operator-specific
  daemon name, hostname, or Tailscale tag (FR-009 applies here too).
- **FR-023**: The launcher template MUST be parseable by `bash -n` (no
  syntax errors).

#### Verification

- **FR-024**: Every committed shell file in `deploy/` (the installer and the
  launcher template) MUST pass `bash -n` parsing without error.
- **FR-025**: A repeatable automated test MUST exist that runs `install.sh`
  twice in a temporary root and asserts: (a) the idempotency contract from
  FR-001 (zero observable filesystem differences between the post-first-run
  and post-second-run states); (b) the macOS tmutil behaviour from FR-002
  via a stubbed `tmutil` on PATH that records its arguments, with the
  assertion that `addexclusion` was invoked at most once across both runs
  against the configured vault state directory; and (c) the FR-003 banner
  contract by capturing install.sh's stdout and asserting it contains a
  `security add-generic-password -T <binary-path>` invocation in which
  `<binary-path>` is the absolute path where install.sh placed the binary.
  The test MUST NOT mutate the host's real Keychain or backup
  configuration.
- **FR-026**: A repeatable automated check MUST verify that the launcher
  template contains the `hush supervise` exec line and contains no active
  `hush request --exec` invocation outside an explicit warning comment.

### Key Entities *(include if feature involves data)*

- **Installer (install.sh)**: An operator-runnable shell script. Its inputs
  are the source binary and the host OS. Its outputs are the laid-down
  binary, the platform service file, (on macOS) the Time Machine exclusion,
  and a printed next-steps banner containing the exact `security
  add-generic-password -T <binary-path> ...` commands the operator runs
  separately to populate the Keychain (per FR-003, install.sh itself
  creates zero Keychain entries). It is the only artifact in the chunk
  that takes destructive actions on the host's filesystem.
- **launchd plist (hush.plist)**: A static XML artifact that tells launchd
  how to run the hush server on macOS. Read-only from the operator's
  perspective; the installer copies it into place.
- **systemd unit (hush.service)**: A static INI-style artifact that tells
  systemd how to run the hush server on Linux. Read-only from the
  operator's perspective; the installer copies it into place.
- **Supervisor launcher template (supervise-launch.sh.template)**: A
  generic shell-script template, NOT installed by the installer. The
  operator copies it once per daemon, substitutes the marked placeholders,
  and registers the resulting per-daemon script with launchd or systemd
  separately. The template's value is the discipline its placeholders and
  warnings encode.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A first-time operator can install hush on a fresh macOS or
  Linux host in a single `install.sh` invocation and reach a state where
  the host's init system can load the hush server, with no manual
  filesystem manipulation between the binary build step and the install
  step.
- **SC-002**: Re-running `install.sh` in a temporary install root produces
  zero observable filesystem differences vs. the post-first-run state, and
  exits 0. (Verified by the FR-025 automated test.)
- **SC-003**: On every macOS install, the vault state directory is excluded
  from Time Machine. (Verified by the FR-025 test asserting the stubbed
  `tmutil` recorded an `addexclusion` call against the vault state
  directory path.)
- **SC-004**: On every macOS install, install.sh's printed next-steps banner
  contains a `security add-generic-password -T <binary-path> ...`
  invocation whose `-T` argument is exactly the installed hush binary path
  (no wildcard, no other binary path). install.sh itself creates zero
  Keychain entries (FR-003). (Verified by the FR-025 test capturing
  install.sh's stdout and asserting the `-T` argument matches the binary
  path it placed.)
- **SC-005**: Neither the launchd plist nor the systemd unit declares a
  root user as the service's run-as account. (Verified by static inspection
  of the committed plist and unit.)
- **SC-006**: The supervisor launcher template execs `hush supervise` and
  contains zero un-warned occurrences of `hush request --exec`. (Verified
  by an automated grep-style test that fails on any active occurrence.)
- **SC-007**: The four committed deploy artifacts contain zero
  operator-specific daemon names, hostnames, Discord IDs, or Tailscale
  tags. (Verified by reviewer scan during PR review and by an automated
  grep against a denylist of operator-personal patterns.)
- **SC-008**: `bash -n` exits 0 against every committed shell file in
  `deploy/`.

## Assumptions

- The host that runs `install.sh` is either macOS or Linux. Other operating
  systems are out of scope for v0.1.0 and the installer is allowed to refuse
  them.
- The hush binary has already been built or downloaded before the operator
  runs `install.sh`. The installer is not responsible for the build or
  download step.
- The operator has the privilege to install a system service and create
  Keychain entries on the target host (typically `sudo` on Linux, an
  authorised account on macOS). The installer does not attempt to elevate
  privilege beyond what `sudo`-style invocation provides.
- The operator's filesystem layout follows Unix-y conventions for
  system-wide install paths. The installer is allowed to use those paths as
  defaults; advanced operators who need a different prefix may set an
  environment variable that the installer respects (the specific knob is a
  plan-phase decision, not a spec requirement).
- The Time Machine exclusion mechanism (`tmutil addexclusion`) is the
  authoritative way to mark a directory as backup-excluded on macOS. We
  rely on it. If Apple ever changes this mechanism, the chunk will be
  re-specced.
- The supervisor launcher template is shipped as a `.template` file
  precisely so the installer does NOT copy it into a system location; the
  operator copies it manually per daemon. This separation keeps the chunk's
  installer simple (no per-daemon logic) and keeps the template's
  placeholders visible to the operator instead of being silently
  substituted by the installer.
- The four files live under a top-level `deploy/` directory at the repo
  root, alongside the existing project structure. The directory has no
  prior committed contents; this chunk creates them.
- Neither the plist nor the unit is expected to encode every possible
  hardening directive in v0.1.0; the spec requires only that they declare
  the non-root user and point to the correct binary. Plan-phase decides
  any further hardening directives.
