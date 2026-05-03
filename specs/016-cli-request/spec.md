# Feature Specification: hush request — interactive secret fetch with --exec or --format eval

**Feature Branch**: `016-cli-request`
**Created**: 2026-05-03
**Status**: Draft
**Input**: User description: "hush request: signs + submits /claim with --scope/--reason/--ttl/--max-uses; awaits Discord approval; ECIES-decrypts each secret; either --exec injects secrets into a child env (parent zeroes ephemeral key after) or --format eval prints shell-evalable exports with a stderr WARNING about shell history risk; mutually exclusive; never writes any secret value or JWT to disk"

## Overview

`hush request` is the operator's interactive secret-fetch tool. The operator runs it on an agent machine to obtain one or more named secrets — but the secrets are never delivered until the same operator personally approves the request from their phone via a Discord direct message. Once approved, the requested secrets are pulled down end-to-end-encrypted, decrypted only inside the calling process, and then either:

1. Injected as environment variables into a child program the operator names with `--exec`, OR
2. Rendered as a shell-evalable `export …` block on standard output with `--format eval`, accompanied by a loud warning on standard error that the values may end up in shell history.

The two delivery modes are mutually exclusive. The command refuses to run if neither is supplied, because the only remaining option — a default that prints secrets — is unsafe and would surprise the operator. No secret value, and no session token, is ever written to disk.

## Clarifications

### Session 2026-05-03

- Q: How long does `hush request` wait for the operator's Discord decision before giving up? → A: Wait at most `--ttl` (reuse the same flag); if no decision by then, exit with timeout status.
- Q: How does the operator pass arguments to the child program named by `--exec`? → A: `--exec` takes only the program path; argv tail comes from trailing positional arguments after `--` (e.g., `hush request --scope X --exec myprog -- --flag value`).
- Q: What does `--max-uses` count, and how is it reconciled with `len(--scope)`? → A: `--max-uses` caps the total number of `/s/<name>` fetches the issued session token will authorize across its lifetime; the operator-supplied value is sent verbatim in the claim. `hush request` performs an early input-validation check that `--max-uses >= len(--scope)` and exits with the input-error status if not (so the Discord prompt is never sent for a request that cannot possibly succeed).
- Q: Is `--server` required on every invocation, or resolved from a client-side default? → A: `--server` is required on every `hush request` invocation; missing → input error before any network call. SDD-16 does NOT introduce a client-side config file or env-var fallback; the operator supplies the vault server address on each call.
- Q: What happens when the operator sends SIGINT/SIGTERM during the approval wait? → A: The command zeroes any in-memory ephemeral key material, exits non-zero with the documented "interrupted" status, and lets the pending request expire naturally on the server at `--ttl`. SDD-16 introduces no `/claim/cancel` endpoint or other server-side cancellation path.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Wrap a child program safely with `--exec` (Priority: P1)

The operator wants to launch a tool (a shell, an agent, a one-shot script) that needs one or more API credentials. They run `hush request --scope ANTHROPIC_API_KEY,GITHUB_TOKEN --reason "starting work session" --ttl 8h --exec "zsh"`, approve the resulting Discord prompt on their phone, and the child program starts with the requested values present as environment variables. The operator's terminal, scrollback, and operational logs contain no trace of any secret value.

**Why this priority**: This is the canonical safe path and is required by AC-5. It is the workflow that lets the operator run AI agents and developer tools without putting credentials in dotfiles, `.env` files, or any other persistent store. Without this story, the feature does not deliver the threat-model promise of the product.

**Independent Test**: Run the command pointing `--exec` at a small program that prints its environment to its own stdout. Confirm the program receives every named secret as an environment variable. Confirm the parent's stdout, stderr, and structured log contain no occurrence of any secret value. Confirm the child's exit code becomes the parent's exit code.

**Acceptance Scenarios**:

1. **Given** a registered operator on a configured agent machine and a vault holding a secret named `ANTHROPIC_API_KEY`, **When** they run `hush request --scope ANTHROPIC_API_KEY --reason "claude session" --ttl 1h --exec "<env-printer>"` and approve the Discord prompt, **Then** the child program's environment contains `ANTHROPIC_API_KEY` set to the vault value, and the parent's stdout/stderr/log contain no occurrence of that value.
2. **Given** the same setup, **When** the child program exits with a non-zero status, **Then** `hush request` exits with the same status so the calling shell can branch on it.
3. **Given** an active request, **When** the call to the child program returns, **Then** the per-request ephemeral private key held in the parent's memory is overwritten with zero bytes before the parent process exits.
4. **Given** the operator denies the Discord prompt, **Then** `hush request` exits with the documented authentication-failure status and no child program is started.

---

### User Story 2 — Emit shell-evalable exports with `--format eval` (Priority: P2)

The operator wants to load secrets into the current interactive shell (for example, to debug an issue without re-launching the shell, or to run a one-line `gh ...` command). They run `hush request --scope GITHUB_TOKEN --reason "ad-hoc gh call" --ttl 15m --format eval`, approve on their phone, and `hush request` prints a block of `export NAME='value'` lines on standard output that the operator pipes into `eval`. A WARNING line is printed on standard error explaining that the printed values may be captured by shell history, terminal scrollback, `tmux`, or `script`, and that `--exec` is the safer choice.

**Why this priority**: AC-6 requires that even the operator-acknowledged shell-history mode does not silently leak — the warning must be loud and the escaping must be correct. This story exists to make that contract testable.

**Independent Test**: Run the command with `--format eval`. Confirm the standard-output stream is exactly the set of `export NAME='value'` lines, one per requested scope, and that `eval` of that output sets each named variable to the correct vault value (including any secret value that contains single quotes). Confirm the standard-error stream contains the documented WARNING text. Confirm the warning goes to standard error, never to standard output.

**Acceptance Scenarios**:

1. **Given** a request for two secrets, **When** the operator approves and selects `--format eval`, **Then** standard output contains exactly two `export NAME='value'` lines and standard error contains a WARNING about shell history risk.
2. **Given** a secret value that itself contains a single-quote character, **When** rendered into the export line, **Then** the line is encoded so that a POSIX shell `eval` of the line restores the original byte sequence exactly.
3. **Given** the operator pipes standard output to a file or to `eval`, **Then** the WARNING still reaches a human-visible stream because it is on standard error.

---

### User Story 3 — Refuse to run when neither delivery mode is chosen (Priority: P3)

The operator runs `hush request --scope X` with no `--exec` and no `--format eval`. The command refuses to make any network call and refuses to print any secret. It exits with the documented input-error status and prints a message that names both flags and explains they are mutually exclusive.

**Why this priority**: A default that prints secrets to standard output would silently undo the safe-by-construction property of the other two stories. Closing this door at the input-validation layer means the operator cannot accidentally produce secret-on-stdout behaviour.

**Independent Test**: Run `hush request --scope X` with no other delivery flag. Confirm the exit status is the input-error code, no Discord prompt is sent, no secret value is printed anywhere, and the error message names the two valid modes.

**Acceptance Scenarios**:

1. **Given** neither `--exec` nor `--format eval`, **When** the operator invokes the command, **Then** it exits with the input-error status and writes a message naming the two valid modes; no `/claim` request is sent.
2. **Given** both `--exec "zsh"` and `--format eval` together, **When** the operator invokes the command, **Then** it exits with the input-error status and the message states the two modes are mutually exclusive; no `/claim` request is sent.
3. **Given** `--format` set to anything other than the literal string `eval`, **When** the operator invokes the command, **Then** it exits with the input-error status and no secret request is sent.

---

### Edge Cases

- **Embedded single quote in a secret value (`--format eval` mode)**: the rendered `export` line MUST encode the value such that a POSIX shell `eval` of the line restores the original bytes verbatim.
- **Operator denies the Discord prompt**: `hush request` exits with the documented authentication-failure status; no child program is started; no secret bytes are decrypted.
- **Discord bot unavailable while the request is pending**: the server signals the unavailability; `hush request` exits with the documented failure status without printing or injecting any secret.
- **Vault server unreachable on the configured Tailscale address**: `hush request` exits with a clear connection-failure message; no secret bytes are decrypted.
- **A scope name is approved but the vault no longer holds that secret**: `hush request` exits with the documented not-found status; no partial environment is handed to a child.
- **Multiple secrets in a single request**: every approved scope must be delivered before any child is launched, otherwise the child must not be launched at all (no half-populated environment).
- **`--exec`'d child crashes before reading its environment**: parent still zeroes the ephemeral private key on shutdown; no secret bytes leak to a core dump produced by the parent.
- **Approval times out**: if no decision arrives within `--ttl` from request submission, `hush request` exits with the documented timeout status; no secret bytes are decrypted. The `--ttl` value bounds both the eventual session-token lifetime AND the maximum wait window — there is no separate wait-timeout flag.
- **Operator interrupts during approval wait (Ctrl-C / SIGINT / SIGTERM)**: the command zeroes any in-memory ephemeral key material, exits non-zero with the documented "interrupted" status, and abandons the pending request — the request expires naturally on the server at `--ttl`. There is no client-initiated cancel call to the server.
- **Standard output is redirected to a file in `--format eval` mode**: the WARNING still reaches the operator because it is on standard error.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The command MUST accept the flags `--scope` (one or more secret names), `--reason` (free-form string), `--ttl` (duration), `--max-uses` (positive integer), and `--server` (vault server address). Approval requests MUST carry every one of these to the approver so the operator can make an informed decision on their phone. `--max-uses` defines the total number of single-secret fetches the eventual session token will authorize across its lifetime, and the operator-supplied value MUST be transmitted verbatim in the claim payload (no silent rewriting). The command MUST reject — at the input-validation layer, before any network call — any invocation where `--max-uses < len(--scope)`, on the grounds that such a request cannot possibly succeed and would burn a Discord prompt for nothing.
- **FR-002**: The command MUST require exactly one of two delivery modes: `--exec <program>` OR `--format eval`. Supplying neither MUST cause an input error before any network call is made. Supplying both MUST cause an input error before any network call is made.
- **FR-003**: When `--format` is supplied, its only accepted value is the literal string `eval`. Any other value MUST cause an input error.
- **FR-004**: The command MUST sign the approval request with the operator's per-machine client signing key. That signing key MUST be loaded from the operating system's keychain via the project's keychain abstraction, never from a file on disk. There MUST be no flag, environment variable, or configuration option that lets the signing key come from a file path.
- **FR-005**: The command MUST submit the signed request to the configured vault server and wait for the approver's Discord decision. The wait window MUST be bounded by the `--ttl` value supplied at the call site: if no decision arrives within `--ttl` from request submission, the command MUST exit with the documented timeout status without decrypting any secret. There MUST be no auto-approve mode, no fallback that skips the Discord wait, and no separate "wait timeout" flag.
- **FR-006**: On approval, the command MUST fetch every requested secret over an end-to-end-encrypted channel and decrypt each value into in-process memory only.
- **FR-007**: In `--exec` mode, the command MUST start the named child program with each requested secret available as an environment variable whose name is the scope name and whose value is the decrypted secret value. The child MUST inherit the parent's other environment variables. Existing entries with the same name MUST be overridden by the request's value.
- **FR-008**: In `--exec` mode, the value of `--exec` MUST be treated as the child program path only (looked up on `PATH` when not absolute) — it MUST NOT be split on whitespace, parsed by a shell, or interpreted as a multi-word command. Arguments to be passed to the child program MUST come from the trailing positional arguments after the literal `--` separator (e.g., `hush request --scope X --exec myprog -- --flag value arg2`); those positional arguments form the child's `argv[1:]` verbatim, in order, without further parsing. When `--` is absent, the child receives an empty argv tail.
- **FR-009**: In `--exec` mode, after the child program returns, the parent MUST zero the per-request ephemeral private key in its own memory before exiting.
- **FR-010**: In `--exec` mode, the command MUST exit with the same numeric exit status as the child program.
- **FR-011**: In `--format eval` mode, for each requested secret the command MUST write a single line to standard output of the form `export NAME='VALUE'`, where `NAME` is the scope name and `VALUE` is the decrypted secret value rendered such that a POSIX shell `eval` of the line restores the original bytes (single-quote characters in the value MUST be properly escaped for that quoting style).
- **FR-012**: In `--format eval` mode, the command MUST also write a WARNING line to standard error that explains the shell-history risk and recommends `--exec` whenever possible. The warning MUST go to standard error so it remains visible when standard output is piped to `eval` or to a file.
- **FR-013**: The command MUST NOT write any secret value to any file at any point — not as a cache, not as a temporary file, not as a debug artifact, not as a crash dump.
- **FR-014**: The command MUST NOT write the issued session token to any file at any point.
- **FR-015**: The command MUST NOT print any secret value to its own standard output unless `--format eval` is the explicit delivery mode. In `--exec` mode, neither standard output nor standard error of the parent MUST contain any secret value.
- **FR-016**: The command MUST NOT include any secret value in any operational log line, structured logging field, error message, or audit-style breadcrumb the parent itself emits.
- **FR-017**: When the approver denies the request, when the request times out, when the Discord delivery channel is unavailable, or when the vault server cannot be reached, the command MUST exit with a non-zero status and MUST NOT print or inject any secret value.
- **FR-018**: When any one requested scope cannot be fetched after approval (for example, because the vault no longer holds it), the command MUST exit with a non-zero status and MUST NOT start a child program with a partial environment and MUST NOT print a partial export block.
- **FR-019**: The command MUST be the sole interactive surface of this feature: no other subcommand or library entry point provides the same delivery semantics. (This guards against a second, weaker code path being added later that bypasses the warning, the keychain requirement, or the no-disk rules.)
- **FR-020**: `--server` MUST be required on every invocation of `hush request`. A missing or empty value MUST cause an input error before any keychain access, any network call, or any Discord prompt. SDD-16 MUST NOT introduce a client-side config file, environment-variable fallback, or auto-discovery path for the server address; the operator MUST supply the vault server address explicitly on each call.
- **FR-021**: When the process receives SIGINT or SIGTERM during the approval wait window, the command MUST zero any in-memory ephemeral private-key material before returning from the signal-handling path, MUST exit with the documented "interrupted" status, and MUST NOT issue any client-initiated cancellation call to the server (the pending request is allowed to expire server-side at `--ttl`). The same key-zeroing guarantee MUST hold for SIGINT/SIGTERM received at any other point in the request lifecycle prior to `--exec` of the child.

### Key Entities

- **Approval Request**: the structured ask submitted to the vault server containing the requested scope list, the operator's stated reason, the requested time-to-live, the requested use-count limit, and a per-request public key used to deliver the eventual response in encrypted form. It is signed by the operator's machine-bound signing key. It is never persisted to disk.
- **Session Token**: the short-lived, scope-bound, IP-bound credential issued on approval. It lives only in the calling process's memory and is zeroed before the process exits. It is never written to disk.
- **Secret Value**: the decrypted bytes for a named scope. Lives only in memory, is handed off to a child process's environment in `--exec` mode or rendered into a shell `export` line in `--format eval` mode, and is never persisted by `hush request`.
- **Ephemeral Decryption Key**: a per-request key pair generated by the calling process. Only the public half travels in the request payload; the private half is held in process memory and is overwritten with zeroes before the parent exits.
- **Child Process Environment**: the environment block handed to the program named by `--exec`. Contains every requested secret keyed by scope name, plus the inherited environment of the parent.
- **Eval Output Block**: the standard-output stream produced in `--format eval` mode. Contains exactly one `export NAME='value'` line per requested scope.
- **Shell-History Warning**: the standard-error message emitted in `--format eval` mode, naming the shell-history / scrollback / multiplexer-buffer risk and recommending `--exec` instead.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: After a successful `--exec` invocation, a search of every file under the operator's home directory and every file under the system temporary directory for any of the requested secret values returns zero matches.
- **SC-002**: After a successful `--exec` invocation, the parent process's combined standard output, standard error, and structured log stream contain zero occurrences of any requested secret value.
- **SC-003**: A child program launched via `--exec` receives every requested secret as an environment variable whose name equals the scope name and whose value equals the vault value, on 100% of successful runs.
- **SC-004**: After a successful run in either mode, no file written by `hush request` contains either the issued session token or any decrypted secret value (verified by scanning every file timestamped during the run).
- **SC-005**: Running `hush request --scope X` without `--exec` and without `--format eval` exits non-zero, prints a message naming the two valid modes, and produces zero network traffic to the vault server (verifiable by packet capture on the loopback or Tailscale interface).
- **SC-006**: In `--format eval` mode, piping standard output through `eval` correctly sets every named variable, including for secret values that contain single-quote bytes, on 100% of test cases.
- **SC-007**: In `--format eval` mode, the standard-error WARNING is emitted on 100% of successful runs, regardless of whether standard output is a terminal, a pipe, or a redirected file.
- **SC-008**: A run that the operator denies on Discord results in a non-zero exit, no decrypted secret in memory after exit, and a parent-side scrollback and log that contain zero secret values.
- **SC-009**: Replacing the operator's keychain with a stub that returns no entry causes `hush request` to fail before any network call is made; replacing the keychain with a stub that returns the correct key allows the request to proceed. There is no other code path that produces a usable signing key.
- **SC-010**: When two or more scopes are requested and any one of them cannot be delivered after approval, no child program is started and no partial export block is printed, on 100% of test cases.

## Assumptions

- The operator has previously run the project's per-machine client bootstrap (delivered by SDD-15) so that a signing key and machine identity exist in the OS keychain.
- The vault server is reachable on the configured Tailscale address; reachability problems are surfaced as connection errors rather than hidden behind retries.
- The Discord bot is online and the configured operator's Discord identity matches the approver enrolled on the server. Bot-down conditions surface as documented unavailability errors, not as auto-approval.
- The operator runs the command from an interactive terminal session; non-interactive use (scripted runs without a controlling terminal) is acceptable as long as the operator is still personally available to approve on Discord — the command itself does not require a TTY.
- The named child program in `--exec` mode is a real executable on `PATH` or an absolute path; it is not a shell expression that needs metacharacter expansion.
- Scope names are well-formed environment-variable names that are safe to set in a child process and to render in shell-export lines (uppercase letters, digits, and underscores, not starting with a digit). Validation of scope-name shape happens at vault-write time, not at request time.
- The exact WARNING text for `--format eval` mode is fixed during implementation to match the wording recorded in the project's security documentation; this specification mandates that a warning is emitted, names what risk it must cover, and pins the destination stream.
- The exit-status code points used by this command (success, generic failure, input error, authentication failure, not found) are the project-wide values defined in the constitution; this specification names them as "documented" rather than re-defining them.
- No client-side configuration file exists today. SDD-15 stored only the per-machine signing key in the OS keychain; the vault server address is therefore supplied explicitly via `--server` on every invocation. A future SDD may introduce a client-side config file, but introducing one is out of scope for SDD-16.
