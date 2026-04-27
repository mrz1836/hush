# SDD-16 — `hush request` (interactive; ECIES decrypt; --exec injection)

**Phase:** 4
**Package:** `internal/cli`
**Files:** `internal/cli/request.go`, `internal/cli/exec.go`, `*_test.go`
**Branch:** `016-cli-request` (created by the `before_specify` git hook)
**Blocked by:** SDD-08, SDD-09, SDD-13, SDD-15
**Blocks:** SDD-25
**Primary AC:** AC-5, AC-6
**Coverage target:** 90%

**Behaviour contracts (MUST):**
- Flags: `--server`, `--scope` (csv), `--reason`, `--ttl`, `--exec`, `--format` (eval only), `--max-uses`
- Read client signing key from Keychain (SDD-15) via `internal/keychain`
- Build canonical-JSON claim (SDD-08), sign, POST `/claim`, await response
- On approval: for each scope name fetch `/s/<name>`, ECIES-decrypt (SDD-09), wrap in `SecureBytes`
- `--exec` path: build child env from `SecureBytes` (use `SecureBytes.Use(fn)` to copy bytes into child env at exec syscall time), `exec.Cmd.Run`, propagate exit code
- `--format eval`: print `export NAME='%s'` for each secret (single quotes, escape `'` inside the value); also emit a stderr WARNING per Constitution VII
- Neither flag: error + `ExitInputErr`

**Anti-contracts (MUST NOT):**
- Write secret values to disk (no cache files, no temp files)
- Print secret values to stdout unless `--format eval` is explicit
- Cache JWT to disk

**Tests required:**
- Unit: `TestRequest_RequiresExecOrFormat`, `TestRequest_FormatEvalEmitsStderrWarning`, `TestRequest_ExecInjectsEnvVars`, `TestRequest_PostExecZeroesEphemeralKey`, `TestRequest_FormatEvalEscapesSingleQuote`
- Integration: full flow with `DiscordStub.ApproveAll`
- Sentinel-leak: `TestRequest_ExecOnlyChildHasSecret` — child process echoes env, parent's logs assert sentinel absent

**Constitutional principles in scope:** I (operator-agnostic), IV (TTL-bound JWT), VII (cobra-only, eval-format warning), X (no values in parent logs)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- internal/cli: subcommand `request` (registered via package side-effect in cli.Execute). No new package-level exported symbols beyond the subcommand registration.

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-16 (hush request:
interactive; ECIES decrypt; --exec injection) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles I, IV, VII)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-1 (request), FR-22, AC-5, AC-6)
- /Users/mrz/projects/hush/docs/SECURITY.md  (--format eval warning rationale — operator must understand the shell-history risk)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenario 1 — full happy path)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-5/6 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-16.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
hush request is the operator's interactive secret-fetch tool: it
signs and submits a /claim request, waits for the operator's own
Discord approval, ECIES-decrypts each requested secret, and then
either execs a child process with the secrets in its env (--exec)
or emits a shell-evalable export block (--format eval). The two
modes are mutually exclusive; the eval mode prints a WARNING to
stderr per Constitution VII (shell history risk).

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The command requires EITHER --exec OR --format eval; neither
  results in an input error (no implicit default that would
  print secrets).
- --exec runs the named child program with the requested
  secrets injected as environment variables; the parent's
  ephemeral key is zeroed after the child has consumed the env.
- --format eval prints `export NAME='value'` for each secret
  (single-quoted, with embedded single-quotes properly escaped)
  AND emits a stderr WARNING explaining the shell-history risk.
- Secret values MUST NEVER be written to disk (no cache files,
  no temp files). The JWT MUST NEVER be cached to disk.
- Secret values MUST NEVER appear in the parent process's logs.
  In --exec mode, only the child has access to the secrets;
  the parent's stdout/stderr/log MUST contain no value.
- The client signing key is loaded from the OS keychain via
  internal/keychain, never from a file the operator could
  accidentally check in.

The spec MUST NOT encode HOW (no library names, no specific
exec/syscall details). Those are plan-phase.

Acceptance criteria: AC-5 (--exec env injection works), AC-6
(secrets do not leak via --format eval if used carefully).

Action — run exactly one command:
  /speckit-specify "hush request: signs + submits /claim with --scope/--reason/--ttl/--max-uses; awaits Discord approval; ECIES-decrypts each secret; either --exec injects secrets into a child env (parent zeroes ephemeral key after) or --format eval prints shell-evalable exports with a stderr WARNING about shell history risk; mutually exclusive; never writes any secret value or JWT to disk"

The before_specify hook will create branch 016-cli-request.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-16 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-16.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-16 (hush request) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; I/IV/VII/X load-bearing)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-1, FR-22, AC-5, AC-6)
- /Users/mrz/projects/hush/docs/SECURITY.md  (--format eval warning text — must match the documented wording)
- /Users/mrz/projects/hush/docs/LIFECYCLE-SCENARIOS.md  (Scenario 1)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (internal/cli)
- /Users/mrz/projects/hush/docs/sdd/SDD-16.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- internal/cli/request.go (request subcommand)
- internal/cli/exec.go (child env construction + exec wrapper)
- internal/cli/request_test.go, exec_test.go
- No new exported package-level symbols.

Implementation contract (HOW — locked):
- Subcommand flags exactly: --server, --scope (csv → []string),
  --reason, --ttl (time.Duration), --exec (string),
  --format (string, eval only), --max-uses (int).
- Validation: --exec XOR --format must be set; otherwise
  ExitInputErr with the explicit message "must specify --exec or
  --format eval".
- Client signing key: internal/keychain.Retrieve under
  (hush-client, machine-<index>) where machine-index comes from
  the existing config or a global flag (decide in plan; document
  the choice).
- Ephemeral key per request: generate fresh ECDSA keypair via
  crypto/ecdsa P-256K (or matching go-bitcoin call); the public
  key goes into the canonical claim payload as ephemeral_pub_key;
  the private key stays in memory only.
- /claim flow: build claim {server, scope, reason, ttl,
  ephemeral_pub_key, max_uses, request_id} → CanonicalJSON
  (SDD-08) → Sign with client key (SDD-08) → POST.
  Response: {jwt, expires_at, jti}.
- /s flow: for each scope name, GET /s/<name> with Bearer JWT;
  ECIES.Decrypt (SDD-09) using the ephemeral private key →
  *securebytes.SecureBytes.
- --exec path:
    1. Build child env: copy the parent's env, then for each
       secret name use SecureBytes.Use(fn) to write the value
       into a freshly-allocated []byte appended to env as
       NAME=VALUE. The []byte's lifetime spans only the
       Use(fn) callback PLUS the time until exec syscall returns
       — there is no easy way to zero env after exec, but the
       parent's ephemeral key + JWT are zeroed/Destroyed
       immediately after Run returns.
    2. exec.Cmd with cmd[0] absolute (NOT shell parsing).
    3. Propagate child exit code.
- --format eval path:
    1. For each secret: SecureBytes.Use(fn) to read bytes into
       a local string (necessary evil — eval mode is operator-
       acknowledged), escape any `'` as `'\''`, print
       `export NAME='value'\n` to stdout.
    2. Print to stderr the EXACT warning text from
       docs/SECURITY.md (eval-format warning rationale).
- After either mode completes: ephemeral private key + JWT
  Destroyed before process exit.

Coverage target: 90%.
Constitutional principles in scope: I, IV, VII, X.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-16 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-16.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 90%. Tests required: TestRequest_RequiresExecOrFormat, TestRequest_FormatEvalEmitsStderrWarning (assert exact wording from docs/SECURITY.md), TestRequest_FormatEvalEscapesSingleQuote, TestRequest_ExecInjectsEnvVars (child receives the secret), TestRequest_PostExecZeroesEphemeralKey (assert key bytes are zero after Run), TestRequest_NeverWritesJWTToDisk (test scans tempdir after run for any leaked value), TestRequest_ClientKeyFromKeychainNotEnv. Integration test: full flow with DiscordStub.ApproveAll. Sentinel-leak: TestRequest_ExecOnlyChildHasSecret — child echoes env containing SECRET_SHOULD_NEVER_APPEAR_16, parent's captured logs and stdout assert sentinel absent. Final phase MUST include magex format:fix, magex lint, magex test:race, and magex test:race -tags=integration."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-16 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-16.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Integration tests:
     magex test:race -tags=integration
3. Verify coverage ≥ 90% on internal/cli (request portion):
     go test -cover ./internal/cli/ -run Request
4. Confirm TestRequest_ExecOnlyChildHasSecret passed —
   SECRET_SHOULD_NEVER_APPEAR_16 in child env, absent from
   parent stdout / logs.
5. Confirm TestRequest_FormatEvalEmitsStderrWarning matches the
   exact warning wording from docs/SECURITY.md.
6. Append "Exported API — locked at SDD-16" section to
   docs/PACKAGE-MAP.md under internal/cli noting the request
   subcommand registration.
7. Update docs/AC-MATRIX.md AC-5, AC-6 rows with the new test
   file paths.
8. Mark SDD-16 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add internal/cli/ docs/PACKAGE-MAP.md docs/AC-MATRIX.md \
          docs/SDD-PLAYBOOK.md specs/<feature-dir>/tasks.md
  git commit -m "feat(cli): hush request (interactive; ECIES; --exec | --format eval) (SDD-16)"

Final message: confirm gates passed (unit + integration), race-
clean, coverage ≥ 90%, --format eval warning verified in stderr,
sentinel present in child env but absent in parent, ECIES round-
trip integration test green, AC-5/6 rows updated, SDD-PLAYBOOK
updated, and the combined commit created.
```
