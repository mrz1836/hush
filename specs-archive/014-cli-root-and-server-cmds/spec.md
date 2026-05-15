# Feature Specification: CLI Root and Server-Facing Subcommands

**Feature Branch**: `014-cli-root-and-server-cmds`
**Created**: 2026-05-01
**Status**: Draft
**Input**: User description: "cmd/hush + internal/cli root: cobra-based CLI with TTY-aware output (text on TTY, JSON otherwise), fixed sysexits-style exit codes, and four subcommands — serve (passphrase from stdin pipe → TTY prompt → fail, NEVER env), health (calls /hz, clear connection-refused message), version (build version), revoke (signed POST to /revoke); no secret value ever printed"

## Overview

This chunk delivers the `hush` CLI binary and its root command surface: the global flags shared by every subcommand, the TTY-aware output formatter, the fixed exit-code contract, and the four operator-facing subcommands that target the vault server — `serve`, `health`, `version`, and `revoke`. It does not include client-side workflows (`init`, `request`, `secret`, `supervise`, `client`); those are layered on by later chunks and depend on the skeleton this chunk locks in.

The CLI is the operator's primary control surface for the vault host. The contracts established here — exit codes, output formats, passphrase handling, and command vocabulary — are part of the **public CLI contract**: operators write launchd/systemd units and shell scripts that depend on them, so once published they cannot be silently changed.

## Clarifications

### Session 2026-05-01

- Q: When `hush health` reaches the server but the server's `/hz` response reports one or more unhealthy dimensions (e.g. `clock_in_sync=false`, `vault_loaded=false`, `discord_connected=false`, `config_valid=false`), what exit code should `hush health` return? → A: Exit `ExitErr` (`1`) if any reported dimension is unhealthy; exit `0` only when all dimensions are green. The rendered summary still includes the per-dimension detail in either case.
- Q: When `hush serve` reads the passphrase from a stdin pipe, how should trailing whitespace (e.g. the `\n` appended by `echo "pw" | hush serve`) be handled? → A: Strip exactly one trailing `\n` or one trailing `\r\n` if present; preserve all other bytes verbatim (including leading spaces, internal whitespace, and any other trailing whitespace). POSIX-line convention.
- Q: What HTTP request timeout should `hush health` and `hush revoke` use when calling the server? → A: A single fixed 5-second total-request timeout (covers connect + write + read + close), applied identically to both subcommands. No `--timeout` flag in this chunk.
- Q: What JSON shape should `hush version` emit when stdout is not a TTY? → A: A stable three-key object `{"version": "<string>", "commit": "<string>", "date": "<string>"}` — all three keys always present; missing metadata is reported as `"dev"` for `version` and `"unknown"` for `commit` and `date`. Downstream parsers never have to branch on key existence.
- Q: What are the observable semantics of the `--verbose` and `--quiet` global flags in this chunk? → A: Minimal uniform semantics applied identically to all four subcommands. By default, the primary outcome goes to stdout and informational notes go to stderr. `--verbose` adds a stderr trace of resolved configuration values and step transitions (e.g. "config loaded from X", "stdin pipe detected", "POST /revoke status 200"). `--quiet` suppresses all non-error stderr output and trims stdout to the essential machine-parseable result. No numeric verbosity levels, no debug flag, no per-subcommand variation.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Operator starts the vault server (Priority: P1)

The operator runs `hush serve` on the vault host to bring the encrypted vault online behind the trusted network. The command obtains the vault passphrase **without ever consulting an environment variable**: if standard input is piped (e.g. from a launchd `StandardInPath` or a keychain helper), the piped bytes are the passphrase; otherwise, if standard input is a terminal, the operator is prompted for the passphrase with no echo; if neither input mode is available, the command fails fast with the input-error exit code and a clear message. Once the passphrase unlocks the vault, the server begins listening and remains running until it receives a termination signal, at which point it shuts down cleanly without losing in-flight requests.

**Why this priority**: This is the entire reason the binary exists on the vault host. Acceptance criterion AC-1 (a fresh `hush init` followed by `hush serve` produces a running server reachable on `/hz`) is the v0.1.0 release gate. Without it, no other workflow — Discord approval, secret fetch, supervisor lifecycle — can be tested. It also establishes the root-command scaffolding (global flags, output formatter, exit codes) that every other subcommand consumes.

**Independent Test**: Run `hush serve` with the passphrase delivered via stdin pipe in one process; in another process on the same trusted network, hit the server's health endpoint and observe a successful response. Send a termination signal and verify the process exits with the success exit code without dropping requests that were already mid-flight.

**Acceptance Scenarios**:

1. **Given** a valid configured vault file and the correct passphrase available on stdin via a pipe, **When** the operator runs `hush serve`, **Then** the server starts listening on the configured trusted-network address within a few seconds and stays running until signaled to stop.
2. **Given** an interactive terminal (no piped stdin) and a valid configured vault file, **When** the operator runs `hush serve` and types the correct passphrase at the prompt, **Then** the server starts listening; the typed passphrase is **not** echoed to the terminal and never appears in any log line.
3. **Given** neither piped stdin nor an interactive terminal (e.g. detached background launch with no input source), **When** the operator runs `hush serve`, **Then** the command exits with the input-error exit code and prints a clear message explaining that no passphrase source is available.
4. **Given** any environment variable that might plausibly hold a passphrase (e.g. `HUSH_PASSPHRASE`, `VAULT_PASSPHRASE`, or any name containing the word "pass"), **When** the operator runs `hush serve`, **Then** the command **does not consult that variable** and uses only the stdin/TTY resolution path described above.
5. **Given** the server is running, **When** it receives a graceful-termination signal, **Then** it stops accepting new connections, allows in-flight requests to complete, and exits with the success exit code.
6. **Given** any failure during startup (bad passphrase, missing config, refused bind, etc.), **When** the operator runs `hush serve`, **Then** the command exits with an exit code drawn from the fixed contract (input-error for malformed input, auth-error for bad passphrase, generic-error otherwise) and the failure message **never includes any byte of any secret material**.

---

### User Story 2 — Operator checks server health (Priority: P2)

The operator runs `hush health` from any trusted-network host to confirm the vault server is reachable, has a loaded vault, has a valid configuration, has a synchronized clock, and has its Discord approval bot connected. When the server is reachable, the operator sees a concise human-readable status summary on a terminal or a structured JSON document when the output is captured by another tool. When the server is **not** reachable (connection refused, unroutable, timed out), the operator sees a clear, explicit message that names the address that was tried and the reason the connection failed — not a stack trace, not a generic "error" — and the command exits with the generic-error exit code so scripts can detect the failure.

**Why this priority**: This is the operator's first diagnostic tool when something looks wrong. It is also the smoke test that ships in launchd/systemd readiness probes. It depends on `serve` being implementable but does not gate the v0.1.0 release on its own — `serve` is the gate.

**Independent Test**: Start the server in one process, run `hush health` in another, observe a successful summary. Stop the server, run `hush health` again, observe the explicit connection-refused message and the generic-error exit code.

**Acceptance Scenarios**:

1. **Given** a running, healthy vault server, **When** the operator runs `hush health` from an interactive terminal, **Then** the command prints a human-readable status summary including the dimensions reported by the server's health endpoint and exits with the success exit code.
2. **Given** a running, healthy vault server, **When** `hush health` is invoked in a context where standard output is **not** a terminal (piped to another tool, redirected to a file, captured by a CI step), **Then** the same status information is emitted as a structured JSON document instead of human-formatted text.
3. **Given** no server is listening at the configured address (connection refused), **When** the operator runs `hush health`, **Then** the command prints a clear message that names the address it tried and explains the connection was refused, and exits with the generic-error exit code.
4. **Given** the server is unreachable for a different reason (route timeout, DNS failure, etc.), **When** the operator runs `hush health`, **Then** the command prints a clear message that names the address it tried and explains the failure mode, and exits with the generic-error exit code.
5. **Given** `hush health` runs in any of the conditions above, **When** the output is examined, **Then** **no secret value, vault passphrase byte, or session token byte ever appears in the output** (the health endpoint never returns secret material, and `hush health` never invents any).
6. **Given** the server is reachable but its `/hz` response reports one or more dimensions as unhealthy (e.g. `clock_in_sync=false`, `vault_loaded=false`, `discord_connected=false`, `config_valid=false`), **When** the operator runs `hush health`, **Then** the command renders the full per-dimension summary (text on TTY, JSON otherwise) and exits with the generic-error exit code so a single-shot readiness probe (`hush health && …`) treats partial-health as failure.
7. **Given** the server is reachable and every dimension reported by `/hz` is healthy, **When** the operator runs `hush health`, **Then** the command exits with the success exit code.

---

### User Story 3 — Operator revokes an active session token (Priority: P3)

The operator (or an automated incident-response tool) needs to immediately invalidate a specific session token by its unique identifier — for example, after a suspected client-machine compromise. The operator runs `hush revoke --server <addr> --jti <token-id>`. The command builds a revocation request, signs it locally with the operator's registered client key (so the server can verify the request authentically came from a trusted client), and submits it to the server's revoke endpoint. On success, the operator sees a confirmation and the command exits successfully; on failure, the operator sees a precise reason (token not found, server rejected the signature, network error, etc.) mapped to a meaningful exit code from the fixed contract.

**Why this priority**: Token revocation is the operator's emergency lever. It is required by AC-4 (JWT lifecycle: a token can be revoked via `hush revoke --jti`) and by the threat model (any compromised client must be isolated within seconds, not minutes). It is lower-priority than `serve` and `health` because it is invoked rarely under normal operation, but it must be present and reliable.

**Independent Test**: Issue a session token through the normal claim flow, then run `hush revoke --server <addr> --jti <that-token-id>`. Verify the command exits successfully; verify a subsequent attempt to use the revoked token against the server is refused with an authentication failure.

**Acceptance Scenarios**:

1. **Given** a running server and a valid active token id, **When** the operator runs `hush revoke --server <addr> --jti <id>`, **Then** the command builds a request, signs it with the operator's local client key, submits it to the server, sees an acceptance, and exits with the success exit code.
2. **Given** the operator omits either `--server` or `--jti`, **When** the operator runs `hush revoke`, **Then** the command exits with the input-error exit code and prints a clear message naming the missing flag.
3. **Given** the server rejects the request because the signature does not match a registered client key, **When** `hush revoke` receives the rejection, **Then** the command exits with the auth-error exit code and prints a clear failure message.
4. **Given** the server reports the token id is unknown, **When** `hush revoke` receives that response, **Then** the command exits with the not-found exit code (or, if the server intentionally treats unknown ids as a signature failure for security reasons, with the auth-error exit code) and prints a clear message.
5. **Given** the network call to the server fails (connection refused, timeout), **When** `hush revoke` cannot reach the server, **Then** the command prints a clear message naming the address and reason and exits with the generic-error exit code.
6. **Given** any of the above outcomes, **When** the output is examined, **Then** **no secret value, no signing-key byte, and no fragment of the token-id payload beyond the supplied jti** appears in the output.

---

### User Story 4 — Operator identifies the running binary (Priority: P4)

The operator runs `hush version` to print the build identification of the binary on the host — semantic version, commit identifier, build date, or whatever build metadata was embedded at release time. This is used during incident triage ("which version of hush is on this box?") and during upgrade verification ("did the deploy actually replace the binary?").

**Why this priority**: Small but ubiquitous — every CLI of this class ships a version subcommand, and operators expect it. It is the lowest-priority story because the absence of structured version output does not block any acceptance criterion and the fallback (`hush --help` shows a banner) is acceptable in a pinch. It is included here because it is a trivial addition once the root scaffolding is in place.

**Independent Test**: Run `hush version` on a binary built with non-default version metadata; verify the output reflects that metadata. Run `hush version` on a development build; verify the output reflects the development placeholder.

**Acceptance Scenarios**:

1. **Given** a binary built with version metadata embedded at release time, **When** the operator runs `hush version`, **Then** the command prints the embedded metadata in the appropriate format for the output context (text on a terminal, JSON when captured) and exits with the success exit code.
2. **Given** a development build (no embedded version), **When** the operator runs `hush version`, **Then** the command prints a recognizable development-build placeholder rather than failing.

---

### Edge Cases

- **Output context detection**: When standard output is a terminal but standard error is not (or vice versa), the format choice is made per-stream against the actual terminal-attachment status of that stream. Diagnostics never bleed onto stdout in a way that would corrupt machine-readable output on a pipe.
- **`--no-color` override**: When the operator passes `--no-color`, all ANSI styling is suppressed regardless of terminal detection. Other formatting choices (text vs JSON) are unaffected — `--no-color` only influences color, not structure.
- **Verbose vs quiet conflict**: When both `--verbose` and `--quiet` are passed, the command exits with the input-error exit code and prints a clear message naming the conflict.
- **Config file missing or unreadable**: When the config file specified by `--config` (or the default location) cannot be read, the command exits with the input-error exit code and a clear message naming the file and the failure mode.
- **Stale-config exit code**: The exit code reserved for "stale credentials" (78, the standard `EX_CONFIG`) is **not** raised by any subcommand introduced in this chunk; it is part of the contract because later chunks (`supervise`, `client`) will use it. Subcommands here treat that code as off-limits to avoid colliding with the supervisor↔child contract.
- **Passphrase source ambiguity on `serve`**: When stdin is connected to a pipe that yields zero bytes (the upstream tool produced no input), the command treats this as "no passphrase available on stdin" and falls through to the next resolution step (TTY prompt) only if a TTY is attached; otherwise it exits with the input-error exit code.
- **Long passphrase entry**: Interactive passphrase entry has no artificial length cap below the limit imposed by the underlying terminal driver; the operator is responsible for providing a passphrase the vault layer accepts.
- **Revoke against an HTTP error the operator did not anticipate** (e.g. server returns 5xx unrelated to the request): the command maps the response to the generic-error exit code and prints the server's status line and any safe-to-show response excerpt — never the raw signed-request payload.
- **Health JSON shape stability**: The JSON shape emitted by `hush health` mirrors the server's health response so downstream tooling can parse a single shape regardless of which side produced the bytes.

## Requirements *(mandatory)*

### Functional Requirements

#### Root command and global behavior

- **FR-001**: The system MUST expose a single binary named `hush` whose entry point dispatches to subcommands by name (`hush serve`, `hush health`, `hush version`, `hush revoke`). Subcommands not introduced in this chunk MAY be wired in later but MUST NOT regress the contracts established here.
- **FR-002**: The system MUST expose four global flags accepted by every subcommand: a flag to point at an alternate configuration file, a flag to increase output verbosity, a flag to suppress non-essential output, and a flag to force no color in output. The global flags MUST be parsed identically regardless of which subcommand is invoked.
- **FR-002a**: The verbosity flags MUST follow these uniform semantics across all four subcommands: by default, the primary outcome of the subcommand is written to stdout and informational notes are written to stderr. When the verbose flag is set, the system MUST additionally write to stderr a trace of resolved configuration values and step transitions (for example: which configuration file was loaded, whether stdin was detected as a pipe or a TTY, the HTTP status code returned by `/hz` or `/revoke`). When the quiet flag is set, the system MUST suppress all non-error stderr output and trim stdout to the essential machine-parseable result. The system MUST NOT introduce numeric verbosity levels, a separate debug flag, or per-subcommand variations on these semantics in this chunk.
- **FR-003**: The system MUST detect whether standard output is attached to an interactive terminal. When stdout is a terminal, output MUST be formatted as human-readable text. When stdout is not a terminal (a pipe, a file, a captured stream), output MUST be formatted as a structured JSON document so downstream tooling can parse it without ambiguity.
- **FR-004**: When the no-color flag is set, the system MUST emit no ANSI styling sequences in any output stream regardless of terminal detection.
- **FR-005**: The system MUST exit with one of the following exit codes — and **only** these codes — for every subcommand introduced in this chunk: `0` for success, `1` for a generic error, `2` for an input error (missing flag, malformed argument, conflicting flags, unreadable config), `3` for an authentication failure (bad passphrase, signature rejected, JWT refused), `4` for a not-found condition (e.g. unknown token id, when the server distinguishes that case), `5` for a permission failure (e.g. insufficient privileges to bind the configured port). Code `78` is reserved for the supervisor↔child stale-credentials contract and MUST NOT be raised by any subcommand in this chunk.
- **FR-006**: The exit-code mapping MUST be stable across releases — operators script against these codes. Adding a new code requires a constitutional amendment; reusing or remapping an existing code is forbidden.
- **FR-007**: No subcommand introduced in this chunk MAY emit any byte of any secret value, vault passphrase, signing key, ECIES key, JWT body, or vault ciphertext to any output stream, log destination, or error message under any condition (success, failure, panic recovery). This MUST be verified by sentinel-leak tests in the spirit of the existing chunk-level convention.

#### `hush serve`

- **FR-008**: `hush serve` MUST resolve the vault passphrase using exactly this priority order: first, if standard input is connected to a pipe (not a terminal), read the piped bytes as the passphrase; second, if standard input is connected to an interactive terminal, prompt the operator without echoing keystrokes; third, fail with the input-error exit code and an explicit message.
- **FR-008a**: When the passphrase is read from a stdin pipe, `hush serve` MUST strip exactly one trailing `\n` byte, or one trailing `\r\n` pair, if present at the end of the piped input. All other bytes — leading whitespace, internal whitespace, other trailing whitespace — MUST be preserved verbatim. This matches the POSIX line-tool convention so that `echo "pw" | hush serve`, a keychain helper that writes one line, or a file written by a text editor all deliver the intended passphrase, while passphrases that legitimately contain surrounding spaces are not corrupted.
- **FR-009**: `hush serve` MUST NOT consult any environment variable for any passphrase value. The prohibition applies to all variable names, not a curated denylist — the resolution path simply never reads any environment variable on its way to obtaining the passphrase. This is verifiable by an automated check that fails if a future change introduces such a read.
- **FR-010**: After obtaining the passphrase, `hush serve` MUST hand off to the server-skeleton runtime, which performs all startup validation (clock sync, permissions, bind address, vault decryption). `hush serve` MUST surface the runtime's terminal exit reason as the appropriate exit code from the contract above.
- **FR-011**: `hush serve` MUST translate process termination signals into a graceful shutdown of the running server such that in-flight requests complete and the process exits with the success exit code.
- **FR-012**: `hush serve` MUST not write any byte of the passphrase to any log stream, telemetry sink, error message, or audit event.

#### `hush health`

- **FR-013**: `hush health` MUST issue a request to the configured server's health endpoint over the trusted network and render the response in the appropriate format for the output context (text on a terminal, JSON otherwise).
- **FR-014**: When the server is unreachable because the connection was actively refused, `hush health` MUST print a clear message that explicitly names the address that was tried and identifies the failure as connection-refused (in human, operator-readable terms — not a stack trace, not a transport-library error string verbatim) and exit with the generic-error exit code.
- **FR-015**: When the server is unreachable for any other reason (timeout, no route, DNS failure), `hush health` MUST print a clear message naming the address and the failure mode and exit with the generic-error exit code.
- **FR-015a**: `hush health` and `hush revoke` MUST each apply a fixed 5-second total-request timeout to their HTTP call to the server (covering connect, write, read, and close). When the timeout elapses before the server responds, the command MUST report the failure as a timeout (clear message naming the address and the timeout duration) and exit with the generic-error exit code. The timeout is not operator-configurable in this chunk.
- **FR-016**: `hush health` MUST not require any authentication, signed request, or session token to query the health endpoint — the health endpoint is an unauthenticated readiness probe.
- **FR-017**: `hush health` MUST never include any secret value in its output. The health endpoint never returns secret material; `hush health` never invents any.
- **FR-017a**: When the server is reachable and the health endpoint response reports every dimension (e.g. `vault_loaded`, `config_valid`, `clock_in_sync`, `discord_connected`) as healthy, `hush health` MUST exit with the success exit code. When the server is reachable but **any** reported dimension is unhealthy, `hush health` MUST render the full per-dimension summary in the appropriate output format **and** exit with the generic-error exit code, so that a single-shot operator readiness probe (`hush health && …`) treats partial-health as failure without requiring callers to parse the body.

#### `hush version`

- **FR-018**: `hush version` MUST print the binary's build identification — at minimum the semantic version string set at release time. The output MAY include additional metadata (commit identifier, build date) when available.
- **FR-019**: When the binary was built without explicit version metadata (a development build), `hush version` MUST print a recognizable placeholder (e.g. `dev`) rather than failing.
- **FR-019a**: When stdout is not a terminal, `hush version` MUST emit a stable JSON object with exactly three keys, in this order: `version` (string), `commit` (string), `date` (string). All three keys MUST always be present so downstream parsers never have to branch on key existence. When build metadata was not embedded at release time, the placeholder values MUST be: `"version": "dev"`, `"commit": "unknown"`, `"date": "unknown"`. Adding new keys to this object is a breaking change to the public CLI contract.

#### `hush revoke`

- **FR-020**: `hush revoke` MUST require two flags: one naming the target server's address, and one naming the unique identifier of the token to revoke. When either is missing, the command MUST exit with the input-error exit code and a clear message naming the missing flag.
- **FR-021**: `hush revoke` MUST construct a revocation request containing at minimum the token identifier and replay-protection fields (a timestamp and a single-use nonce as required by the server's request-signing contract), sign the request locally with the operator's registered client key, and submit it to the server's revoke endpoint over the trusted network.
- **FR-022**: When the server accepts the revocation, `hush revoke` MUST exit with the success exit code and print a confirmation appropriate to the output context.
- **FR-023**: When the server rejects the request because the signature did not match a registered client key (or because the server treats unknown token ids as a signature failure for security reasons), `hush revoke` MUST exit with the auth-error exit code and print a clear failure message.
- **FR-024**: When the server reports the token id as unknown **and** chooses to expose that distinction (rather than returning the auth-failure response above), `hush revoke` MUST exit with the not-found exit code.
- **FR-025**: When the network call to the server fails (connection refused, timeout, no route), `hush revoke` MUST behave the same way `hush health` does for the analogous failure: clear message naming the address and reason, generic-error exit code.
- **FR-026**: `hush revoke` MUST never print or log any byte of the local signing key, the signed payload itself beyond the explicitly supplied token id, or any field of the server's response that could leak secret material. The signed payload bytes are an internal artifact, not a user-facing artifact.

### Key Entities *(include if feature involves data)*

- **Exit code**: A small integer in the fixed range above. It is the **public contract** between the binary and any operator script. Each subcommand maps its terminal outcomes onto exactly one exit code.
- **Output context**: A determination of whether the binary is producing output for human consumption (terminal attached, ANSI styling permitted unless suppressed) or for machine consumption (pipe, file redirect, captured stream — JSON expected). The determination is per-stream, made independently for stdout and stderr at the moment of emission.
- **Passphrase source**: One of three logical channels by which `hush serve` obtains the passphrase: a pipe on standard input, an interactive prompt on standard input, or — and **only** — failure. Environment variables are explicitly **not** a passphrase source.
- **Revocation request**: An operator-originated, locally-signed message identifying a token to invalidate. Its on-the-wire shape is governed by the existing request-signing contract; for this spec it is sufficient to know that the operator's local client key is what authenticates the request.
- **Build identification**: A small, human-readable string (or structured object on a pipe) describing which version of the binary is running. Treated as opaque to the rest of the system.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: An operator running `hush serve` from a fresh, correctly initialized vault host can have the server accepting health-endpoint requests within five seconds of running the command (passphrase delivery time excluded). This is the AC-1 timing target.
- **SC-002**: 100% of subcommand terminal outcomes in this chunk are mapped to one of the seven exit codes in the contract (0, 1, 2, 3, 4, 5, 78), and the exit code is reproducible across runs of the same scenario. No outcome ever exits with an out-of-contract code.
- **SC-003**: 100% of `hush serve` runs in any of the documented environment-variable conditions (no env vars set; common-name env vars set; arbitrary env vars set) resolve the passphrase from stdin or TTY only — never from any environment variable. This is verifiable by an automated sentinel test that asserts no environment-variable lookup occurs on the passphrase path.
- **SC-004**: 100% of error and success outputs across all four subcommands contain zero bytes of any secret value. This is verifiable by sentinel-leak tests that plant a known marker in every secret material handled and assert the marker never appears in any captured stream.
- **SC-005**: When standard output is not a terminal, 100% of subcommand outputs are valid, parseable JSON; when standard output is a terminal, 100% are formatted as human-readable text. The two formats carry the same information content.
- **SC-006**: When the operator passes `--no-color`, 100% of output streams are free of ANSI styling sequences regardless of terminal detection.
- **SC-007**: When `hush health` is run against an unreachable server, the printed message names the address that was tried in 100% of cases and the operator can identify the failure mode (refused, timed out, no route) without consulting external diagnostics.
- **SC-008**: An operator who issues a token, then runs `hush revoke --server <addr> --jti <id>`, then attempts to use the same token, observes the second attempt fail with an authentication error in 100% of runs. This is the AC-4(c) revocation scenario.
- **SC-009**: The chunk's package-level test coverage meets or exceeds the 85% target set in the chunk contract; security-critical paths (passphrase resolution, sentinel-leak assertions) reach 100%.

## Assumptions

- The CLI is invoked by a human operator at a terminal or by a trusted launchd/systemd unit that the operator configured. There is no remote CLI invocation model.
- `hush serve` runs only on the configured vault host. The operator is responsible for delivering the passphrase to the chosen input channel (interactive terminal during boot, or a piped helper that reads from the system keychain). The CLI does not arrange that delivery itself.
- `hush health`, `hush revoke`, and any future client-side subcommand always run from inside the trusted-network perimeter (Tailscale mesh in v0.1.0). The CLI does not enforce that perimeter — that is the network layer's job — but operators MUST not invoke it from outside.
- The output formatter's "TTY → text, otherwise JSON" rule is sufficient for the v0.1.0 operator population. There is no plan to support a third format (YAML, table, etc.) in this chunk.
- Build version metadata is injected at release time by the release tooling; the CLI does not derive it at runtime.
- Configuration loading (the `--config` flag's effect, the schema of the file, the validation rules) is owned by a separate config package and is out of scope for this chunk except as the consumer of `--config`.
- Request signing (the canonicalisation, hashing, and signature scheme used by `hush revoke`) is owned by an existing transport-signing package and is consumed as a black-box dependency by this chunk.
- The Discord-disconnect, NTP-drift, and file-permission startup checks invoked by `hush serve` are owned by the server-skeleton runtime and are out of scope for this chunk except as the producer of the exit code that `hush serve` propagates.
- The supervisor-only exit code 78 is reserved by this chunk's contract but is not raised by any subcommand here. It will be raised by the supervisor and child-process subcommands introduced in later chunks.
- The CLI binary is delivered as a single static executable. The packaging, signing, and distribution of that binary are out of scope for this chunk.
