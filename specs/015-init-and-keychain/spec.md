# Feature Specification: hush init — server + client bootstrap with OS-keychain ACL

**Feature Branch**: `015-init-and-keychain`
**Created**: 2026-05-03
**Status**: Draft
**Input**: User description: "hush init: server mode (creates vault + config + keychain entries with hush-binary-only ACL) and client mode (--machine-index, derives client key, stores in keychain, prints fingerprint); rejects passphrase < 12 chars; never reads passphrase from env or arg; never generates a passphrase for the operator"

## Overview

`hush init` is the one-time bootstrap an operator runs on a fresh installation. It has two distinct modes that never overlap:

- **Server mode** runs on the trusted vault host. It creates the encrypted vault, writes the server configuration file with every documented default, and stores the Discord bot token plus the vault passphrase in the OS keychain — each item locked to the `hush` binary by an access control restriction so no other process on the machine can read them.
- **Client mode** runs on every agent machine. It derives a per-machine client key from the operator's passphrase plus an explicit machine index, stores the key in the OS keychain under the same hush-binary-only restriction, and prints the matching public-key fingerprint to standard output as a single copy-pasteable line so the operator can paste it into the server's allow list.

The command is interactive by design: passphrase entropy comes from the operator, never from the program, and never from an environment variable or command-line argument. A passphrase shorter than twelve characters is rejected with a distinct, actionable error before any key material is derived. The mutually exclusive mode flags prevent an operator from accidentally bootstrapping a server on an agent machine.

## Clarifications

### Session 2026-05-03

- Q: When client-mode init is rerun on a host that already has a keychain item for the same machine index, should the command refuse-and-exit, prompt to overwrite, or require an explicit `--force` flag? → A: Refuse-and-exit non-zero with a clear message naming the conflict; the operator must delete the existing keychain item by hand to re-enroll (mirrors FR-012 server-mode behavior).
- Q: On a platform whose keychain lacks a per-binary access control mechanism, should init refuse, proceed with a weaker restriction and warn, require an opt-in flag, or only support per-binary-ACL platforms? → A: Refuse-to-run with a clear platform-incompatibility error naming the missing per-binary ACL mechanism; init writes nothing. The per-binary ACL is a constitutional Security Requirement and the command never downgrades silently.
- Q: What format does client-mode init use when printing the public-key fingerprint to standard output? → A: `SHA256:<base64-no-padding-of-sha256-of-public-key>` — OpenSSH-style single token, exactly one line, no surrounding whitespace, no decoration.
- Q: Should client-mode init also require passphrase confirmation (double entry), or prompt only once? → A: Require confirmation in both modes; mirrors FR-004 server-mode behavior. A mismatch exits non-zero without writing anything; this prevents a typoed passphrase from producing an unusable fingerprint that the operator would otherwise paste into the allow list before discovering the mistake.
- Q: Should init accept the passphrase from a piped standard-input stream (mirroring `hush serve`), or be strictly TTY-only? → A: Strictly TTY-only for all interactive prompts — passphrase, passphrase confirmation, and Discord bot token. If standard input is not connected to an interactive terminal, init exits non-zero with an input-error message. Tests drive prompts via PTY emulation. Init diverges from serve here because (1) it is a one-time bootstrap where operator presence is intentional, (2) confirmation and bot-token prompts make multi-secret pipe input error-prone, and (3) FR-001's literal wording already requires this.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Operator bootstraps the vault host (Priority: P1)

The operator has just installed hush on the trusted vault host. They run `hush init` in server mode in an interactive terminal. The command prompts them for a passphrase (twice, to confirm), prompts them for the Discord bot token, generates a fresh random salt, derives the master keys, creates the encrypted vault, writes the server configuration file populated with every default value documented in the configuration schema, and stores both the Discord bot token and the vault passphrase in the OS keychain with an access control restriction that names the hush binary as the only client allowed to read those items. After completion, the operator can immediately start the vault server with their passphrase and the server reads the bot token from the keychain.

**Why this priority**: Without server mode, no vault exists; nothing else in the product can run. This is the AC-1 entry point — a fresh install must reach a running `hush serve` via this command.

**Independent Test**: Run `hush init` server mode in a temporary directory with a piped script that drives the interactive prompts, then assert (a) a vault file exists with mode 0600, (b) a configuration file exists with mode 0600 and contains every documented default field, (c) two keychain items exist (vault passphrase, Discord bot token) and each is restricted to the hush binary, (d) re-running `hush serve` with the same passphrase opens the vault successfully.

**Acceptance Scenarios**:

1. **Given** a fresh machine with no prior hush state, **When** the operator runs server-mode init in an interactive terminal and supplies a 16-character passphrase plus a Discord bot token, **Then** the command creates the vault file at mode 0600, writes a configuration file at mode 0600 containing every documented default, and stores the passphrase and bot token in the OS keychain with a hush-binary-only access control restriction.
2. **Given** an interactive server-mode session, **When** the operator types a passphrase shorter than twelve characters, **Then** the command rejects it with a distinct error that names the minimum length, derives no key material, writes no files, creates no keychain entries, and exits non-zero.
3. **Given** an interactive server-mode session, **When** the operator's confirmation passphrase does not match the first entry, **Then** the command reports the mismatch and exits non-zero without writing any artifact.
4. **Given** a fresh machine, **When** server-mode init completes, **Then** the salt embedded in the vault is a fresh 16-byte value drawn from a cryptographically secure random source for this run.
5. **Given** a host where a vault file already exists at the target path, **When** server-mode init is run again, **Then** the command refuses to overwrite the existing vault and exits non-zero with a clear message; the operator must explicitly remove or relocate the existing vault to re-bootstrap.

---

### User Story 2 — Operator enrolls a new agent machine (Priority: P1)

The operator wants to enroll a fresh agent machine — say, machine number 3 in their fleet — so it can request secrets from the vault server. They run `hush init` in client mode with an explicit machine index of 3. The command prompts them interactively for the same vault passphrase they used on the server host, derives the client key for index 3 from that passphrase, stores the resulting private key in the OS keychain with a hush-binary-only access control restriction, and prints the matching public-key fingerprint to standard output as a single line. The operator copies that fingerprint and adds it to the server's registered-clients list. From that moment on, this machine is the only one that can sign requests with this key.

**Why this priority**: Without client mode no agent machine can be enrolled; the server has no clients to authorize. This is the AC-6 entry point.

**Independent Test**: Run `hush init` client mode with `--machine-index 3` and a piped passphrase, then assert (a) a keychain item exists for machine index 3 and is restricted to the hush binary, (b) the standard-output line containing the fingerprint is a single line and contains nothing else, (c) running the same command again with the same passphrase and same machine index produces the same fingerprint, (d) running the same command with the same passphrase and a different machine index produces a different fingerprint, and (e) running the same command with a different passphrase produces a different fingerprint even at the same machine index.

**Acceptance Scenarios**:

1. **Given** a fresh agent machine and the operator's vault passphrase, **When** the operator runs client-mode init with `--machine-index 0`, **Then** the command derives the per-machine client key for index 0, stores it in the OS keychain with a hush-binary-only access control restriction, and prints the public-key fingerprint as a single copy-pasteable line on standard output.
2. **Given** the operator runs client-mode init twice with the same passphrase and the same `--machine-index N`, **Then** both runs produce the same public-key fingerprint.
3. **Given** the operator runs client-mode init with the same passphrase and two different machine indices, **Then** the two runs produce different fingerprints.
4. **Given** the operator runs client-mode init with two different passphrases at the same machine index, **Then** the two runs produce different fingerprints.
5. **Given** the operator omits `--machine-index` in client mode, **When** the command runs, **Then** it exits non-zero with a clear message naming the missing flag and writes nothing to disk or the keychain.
6. **Given** the operator combines server-mode and client-mode flags in a single invocation, **When** the command runs, **Then** it exits non-zero with a clear message that the modes are mutually exclusive and writes nothing to disk or the keychain.

---

### User Story 3 — Operator's passphrase stays out of process arguments and environment (Priority: P1)

The operator wants to be confident that their vault passphrase is never visible to other processes on the host. They look at process listings (`ps`), they look at the environment block of the hush process (`/proc/PID/environ` on Linux, `ps eww` on macOS), they grep their shell history. The passphrase appears in none of those places — `hush init` reads it only from the controlling terminal with input echo suppressed.

**Why this priority**: This is a constitutional non-negotiable (Security Requirements: "Passphrase from … stdin pipe (never env var or plist)"). A regression here defeats the entire passphrase-as-root-of-trust model.

**Independent Test**: Construct an environment containing a sentinel value in a plausibly-named variable (for example a value placed in an environment variable named in a way that an unrelated implementation might read) and a command-line argument carrying the same sentinel; run `hush init` server mode driven by a piped script that supplies a *different* passphrase via the documented input channel; assert that (a) the resulting vault opens with the passphrase that was supplied via the documented input channel, (b) the vault does NOT open with the sentinel value, (c) no error message, log line, or output stream contains the sentinel.

**Acceptance Scenarios**:

1. **Given** any environment variable is set on the host, **When** `hush init` runs in either mode, **Then** the command never reads the passphrase from any environment variable.
2. **Given** any command-line argument or flag value is supplied to `hush init`, **When** the command runs, **Then** the command never interprets any argument or flag value as the passphrase.
3. **Given** standard input is not connected to an interactive terminal, **When** server-mode init needs a passphrase, **Then** the command exits non-zero with a clear input-error message rather than silently using a default, an environment value, an empty string, or a value from a piped standard-input stream.

---

### User Story 4 — Operator's machine has no `hush`-readable secrets after enrollment (Priority: P2)

After the operator enrolls a new agent machine via client-mode init, they want assurance that any unrelated process on that machine — a malicious npm package, a curious script, an LLM-generated tool — cannot read the stored client key from the OS keychain without an explicit OS-level prompt.

**Why this priority**: This is the practical payoff of AC-6 — the keychain ACL is what makes "no key files on disk" meaningful. Without it the keychain becomes a slightly-better dotfile.

**Independent Test**: On a host that supports the OS keychain ACL mechanism, run client-mode init, then attempt to read the stored client-key item from a process that is NOT the hush binary (for example, the OS keychain CLI invoked directly, or a small test program). Assert that the read attempt either fails outright or triggers an OS-level user-prompt dialog rather than returning the secret silently.

**Acceptance Scenarios**:

1. **Given** client-mode init has just stored the client key, **When** any process other than the hush binary attempts to read that keychain item, **Then** the OS does not return the secret silently — it either denies the read, requires an explicit OS-level user authorization prompt, or both, per the host platform's keychain access control mechanism.
2. **Given** server-mode init has just stored the vault passphrase and Discord bot token, **When** any process other than the hush binary attempts to read either keychain item, **Then** the OS does not return the secret silently — same access control restriction applies as in scenario 1.
3. **Given** the hush binary itself attempts to read its own keychain items in normal operation, **When** the read happens, **Then** it succeeds without any user prompt because the access control restriction names the hush binary as authorized.

---

### Edge Cases

- **Passphrase exactly twelve characters**: accepted (the rejection rule is "shorter than twelve").
- **Passphrase contains whitespace, Unicode, or control characters**: accepted as long as it meets the minimum length; the operator chose this entropy intentionally.
- **Operator interrupts (Ctrl-C) the passphrase prompt**: the command exits non-zero having written nothing.
- **Standard input is closed mid-passphrase**: the command exits non-zero with a clear input-error message; it does not treat the partial input as a valid passphrase.
- **Machine index is non-numeric, negative, or absent**: client mode rejects with a clear input-error message and writes nothing.
- **Server-mode rerun on a host that already has a vault file**: the command refuses to overwrite and exits non-zero with a clear message.
- **Client-mode rerun on a host that already has a keychain item for the same machine index**: the command refuses and exits non-zero with a clear message naming the conflicting keychain item; the operator must delete the existing item by hand to re-enroll. The command never silently overwrites.
- **Operator runs init on a platform whose keychain lacks a per-binary access control mechanism**: the command refuses to run with a clear platform-incompatibility error that names the missing per-binary ACL capability. Init writes nothing — no vault, no configuration file, no keychain item. The per-binary ACL is a constitutional Security Requirement and is never silently downgraded to a session-scoped or wildcard restriction.
- **Operator tries to start the server while the keychain is locked**: out of scope for this command; addressed by `hush serve`.

## Requirements *(mandatory)*

### Functional Requirements — passphrase handling

- **FR-001**: The init command MUST prompt the operator for the vault passphrase only via the controlling interactive terminal with input echo suppressed; it MUST NOT read the passphrase from any environment variable; it MUST NOT read the passphrase from any command-line argument or flag value.
- **FR-002**: The init command MUST NEVER generate the vault passphrase on behalf of the operator; passphrase entropy is the operator's responsibility by design.
- **FR-003**: The init command MUST reject any passphrase shorter than twelve characters with a distinct error that names the minimum length; on rejection it MUST NOT derive any key material, write any file, or create any keychain entry.
- **FR-004**: In both server and client modes the init command MUST require the operator to confirm the passphrase by entering it a second time; if the two entries differ, the command MUST exit non-zero without writing any artifact (no vault file, no configuration file, no keychain item).
- **FR-005**: If standard input is not connected to an interactive terminal, the init command MUST exit non-zero with a clear input-error message rather than fall back to any default. Init is strictly TTY-driven; unlike `hush serve`, it does not accept a passphrase from a piped standard-input stream.
- **FR-005a**: The Discord bot token prompt (server mode) and the passphrase confirmation prompt (both modes) MUST also be read only from the controlling interactive terminal with input echo suppressed; init MUST NOT read either value from a piped standard-input stream, an environment variable, or a command-line argument.

### Functional Requirements — server mode

- **FR-006**: When invoked in server mode the init command MUST generate a fresh sixteen-byte salt from a cryptographically secure random source for this installation.
- **FR-007**: When invoked in server mode the init command MUST create the encrypted vault file with file mode 0600 (owner read/write only, no group or other access).
- **FR-008**: When invoked in server mode the init command MUST write the server configuration file with file mode 0600.
- **FR-009**: The generated server configuration file MUST contain every required field defined by the documented configuration schema, populated with the schema's documented default value for that field.
- **FR-010**: When invoked in server mode the init command MUST prompt the operator for the Discord bot token via the same interactive-terminal mechanism used for the passphrase, and store the supplied bot token in the OS keychain.
- **FR-011**: When invoked in server mode the init command MUST store the vault passphrase in the OS keychain.
- **FR-012**: When invoked in server mode the init command MUST refuse to overwrite an existing vault file and MUST exit non-zero with a clear message identifying the conflicting path.

### Functional Requirements — client mode

- **FR-013**: The init command MUST accept a `--machine-index` flag that takes a non-negative integer identifying the per-machine client key.
- **FR-014**: When invoked in client mode the init command MUST require the `--machine-index` flag; if it is absent the command MUST exit non-zero with a clear input-error message naming the missing flag.
- **FR-015**: When invoked in client mode the init command MUST derive the per-machine client key from the operator's passphrase combined with the supplied machine index in a way that is deterministic for a given passphrase + index pair and that produces a different key for any change to either input.
- **FR-016**: When invoked in client mode the init command MUST store the derived per-machine private key in the OS keychain.
- **FR-017**: When invoked in client mode the init command MUST print the public-key fingerprint that corresponds to the stored private key to standard output as a single line containing the fingerprint and nothing else, in a form the operator can copy and paste directly into the server's registered-clients list. The fingerprint format MUST be `SHA256:<base64-no-padding>` (OpenSSH-style), where the base64 payload encodes the SHA-256 hash of the public key. The line MUST NOT contain a trailing space, decorative prefix, or any other text; it terminates with a single newline.

### Functional Requirements — mode exclusivity

- **FR-018**: The init command MUST treat server mode and client mode as mutually exclusive; if both are requested in the same invocation the command MUST exit non-zero with a clear message naming the conflict and MUST NOT write to disk or the keychain.

### Functional Requirements — keychain access control

- **FR-019**: Every keychain item the init command creates (vault passphrase, Discord bot token, per-machine client key) MUST be created with an OS-level access control restriction that names the hush binary as the sole authorized reader, so that any other process attempting to read the item is denied silently or required to obtain an explicit OS-level user authorization.
- **FR-020**: On the macOS platform the access control restriction in FR-019 MUST be the platform's per-binary keychain authorization mechanism; a wildcard or unrestricted ACL on any item created by this command is forbidden.
- **FR-020a**: If the host platform's keychain service does not provide a per-binary access control mechanism equivalent to FR-019, the init command MUST refuse to run with a clear platform-incompatibility error that names the missing capability. The command MUST NOT write the vault file, the configuration file, or any keychain item; it MUST NOT silently downgrade to a session-scoped, user-scoped, or wildcard restriction.

### Functional Requirements — surface and reporting

- **FR-021**: The init command MUST report success only after every artifact for the chosen mode has been created; on any failure mid-way through the command MUST report the failure with a non-zero exit, name the failed step, and MUST NOT leave a partially-initialized state that would silently succeed on retry.
- **FR-022**: No output stream produced by the init command (standard output, standard error, log records) MUST contain the operator's passphrase, the Discord bot token, the derived master seed, the derived signing keys, the derived encryption keys, or the per-machine private key in any form.

### Key Entities

- **Vault file**: the encrypted blob holding the operator's secrets at the trusted host. Created by server mode at mode 0600. Contains the salt used for key derivation. Re-creating it requires the same passphrase plus that salt.
- **Server configuration file**: human-readable file describing the server's network bindings, cryptographic parameters, file paths, Discord owner identity, and security toggles. Created by server mode at mode 0600 with every documented default populated.
- **Vault passphrase keychain item**: stored by server mode in the OS keychain with hush-binary-only access control. Read by `hush serve` at runtime.
- **Discord bot token keychain item**: stored by server mode in the OS keychain with hush-binary-only access control. Read by `hush serve` at runtime to authenticate the Discord bot.
- **Per-machine client key**: an asymmetric keypair derived from the operator's passphrase and an explicit machine index. The private half is stored in the agent's OS keychain with hush-binary-only access control by client mode. The public half's fingerprint is printed once for the operator to paste into the server's allow list.
- **Machine index**: an explicit non-negative integer the operator supplies in client mode. Identifies which agent machine in the operator's fleet this enrollment is for. Different indices under the same passphrase produce different keys.
- **Public-key fingerprint**: the operator-pasteable identifier corresponding to the per-machine client key's public half, printed once on enrollment. Encoded as `SHA256:<base64-no-padding>` over the public key, OpenSSH-style.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An operator with no prior hush state on a fresh trusted host can complete server-mode init in a single interactive session, including supplying the passphrase twice and the Discord bot token once, in under three minutes.
- **SC-002**: An operator can enroll a new agent machine via client-mode init in a single interactive session, including pasting the resulting fingerprint into the server's allow list, in under two minutes.
- **SC-003**: After server-mode init succeeds, the operator can immediately start the vault server with the same passphrase and the server opens the vault on the first attempt 100% of the time across repeated installs in clean environments.
- **SC-004**: After client-mode init succeeds for the same passphrase and the same machine index, repeated runs produce the same public-key fingerprint 100% of the time.
- **SC-005**: After client-mode init succeeds for the same passphrase but different machine indices, the two runs produce different fingerprints 100% of the time; the same property holds for two different passphrases at a fixed machine index.
- **SC-006**: 0% of init invocations leak the passphrase, bot token, derived seed, or private key in any output stream, log line, or error message — measured by sentinel-leak tests that drive init with a known-unique sentinel value and grep all captured output for that value.
- **SC-007**: 0% of init invocations read the passphrase from an environment variable or command-line argument — measured by tests that place a sentinel in plausibly-named environment variables and command-line positions and prove the resulting vault opens only with the passphrase supplied via the documented input channel.
- **SC-008**: 100% of keychain items created by init on a platform whose keychain supports per-binary access control carry an access control restriction that names the hush binary; any other process reading the item is denied silently or prompted by the OS — verified by a host-level read attempt from a non-hush process.
- **SC-009**: Every required field documented in the configuration schema appears in the server-mode-generated configuration file with the schema's documented default value — verified by a test that loads the documented schema and asserts every field is present in the generated file.
- **SC-010**: A passphrase shorter than twelve characters is rejected on 100% of attempts before any key material is derived or any artifact is written — verified by a test that asserts no vault file, no configuration file, and no keychain item exists after the rejected attempt.

## Assumptions

- The operator is the single configured human approver per the project's product model — there is no service account or shared bootstrap workflow to support.
- The operator runs init on a host where the OS keychain service is available and unlocked at the moment of invocation; if it is not, the underlying keychain operation surfaces an error that init reports verbatim. Recovering from a locked keychain is the operator's responsibility, not init's.
- The operator runs init in an interactive terminal on the same host they are bootstrapping — there is no remote, scripted, or unattended init mode in this scope.
- The only passphrase input channel for `hush init` is the controlling terminal with input echo suppressed. Init does NOT accept the passphrase from a piped standard-input stream; this is an intentional divergence from `hush serve` because init is a one-time bootstrap with operator-present semantics and additional prompts (confirmation, bot token) that make multi-secret pipe input error-prone. Tests drive these prompts via PTY emulation.
- "OS keychain" means: macOS Keychain on Darwin hosts; the Secret Service / equivalent platform service on Linux hosts. The exact platform mapping for each is the plan-phase concern; this spec only requires that whichever platform-native service is used, an item-level access control restriction limiting reads to the hush binary is applied.
- The vault file path, the configuration file path, and the keychain item names are the values established by the existing project documentation and not redefined here.
- "Configuration schema documented defaults" refers to every field documented as having a default value in the project's configuration schema document; fields documented without a default are out of scope for the auto-generated configuration file.
- Re-running init on a host that already has hush state is treated as a deliberate operator action that requires the operator to first remove the conflicting state by hand; init does not attempt to merge, repair, or migrate prior state in this scope.
