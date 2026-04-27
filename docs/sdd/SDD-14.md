# SDD-14 — `internal/cli` root + global flags + (`serve`, `health`, `version`, `revoke`)

**Phase:** 3
**Package:** `internal/cli` + `cmd/hush`
**Files:** `cmd/hush/main.go`; `internal/cli/{root.go, serve.go, health.go, version.go, revoke.go, output.go, flags.go, exit_codes.go, *_test.go}`
**Branch:** `014-cli-root-and-server-cmds` (created by the `before_specify` git hook)
**Blocked by:** SDD-10, SDD-11, SDD-12, SDD-13
**Blocks:** SDD-15, SDD-16, SDD-17, SDD-23
**Primary AC:** AC-1
**Coverage target:** 85%

**Behaviour contracts (MUST):**
- `cobra` root command name `hush`; global flags `--config`, `--verbose`, `--quiet`, `--no-color`
- Output: TTY → text; non-TTY → JSON; `--no-color` forces no ANSI
- `ExitCode` constants: `ExitOK=0, ExitErr=1, ExitInputErr=2, ExitAuth=3, ExitNotFound=4, ExitPerm=5, ExitConfigStale=78`
- `hush serve` passphrase resolution per `docs/SPEC.md` FR-16: stdin pipe → TTY prompt → fail
- `hush health` handles connection refused with explicit message + `ExitErr`
- `hush revoke` requires `--server` and `--jti`; signs request via SDD-08

**Anti-contracts (MUST NOT):**
- Use `viper` (Constitution VII)
- Read passphrase from env var
- Print secret values

**Tests required:**
- Unit: flag wiring, output formatter, exit code mapping, passphrase resolution order
- Integration: `TestServe_StartAndShutdown` — start in goroutine, SIGTERM, expect clean exit

**Constitutional principles in scope:** VII (cobra-only, no viper), IX (idiomatic Go), X (no secret values printed)

**Exported API to lock in PACKAGE-MAP.md (this chunk):**
- `cmd/hush/main.go`: `func main()` only
- `internal/cli`:
  - `func Execute() int`
  - `var ExitOK, ExitErr, ExitInputErr, ExitAuth, ExitNotFound, ExitPerm, ExitConfigStale int`
  - Subcommand registration entry points (unexported except Execute)

---

## How to run this chunk

Run **5 separate Claude Code sessions**, one per prompt below. The
`extensions.yml` hooks auto-commit each artifact (accept in Prompts 1,
3, 4; conditionally in Prompt 2; **decline** in Prompt 5).

---

## Prompt 1 — Specify  (fresh session)

```
You are running the SPECIFY phase of SDD-14 (internal/cli root +
global flags + serve/health/version/revoke subcommands) of the
hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (Principles VII, IX — cobra-only, idiomatic)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-1, FR-16, FR-17, FR-21, AC-1)
- /Users/mrz/projects/hush/docs/API.md  (GET /hz — what hush health hits)
- /Users/mrz/projects/hush/docs/AC-MATRIX.md  (current AC-1 row state)
- /Users/mrz/projects/hush/docs/sdd/SDD-14.md  (the full chunk contract)

About this chunk (one-paragraph intent, for the spec's overview):
This chunk delivers the hush CLI binary (cmd/hush) and the
internal/cli root: global flags, output formatting, exit-code
contract, and the four operator-facing server subcommands —
serve, health, version, revoke. Subsequent CLI chunks (SDD-15
init, SDD-16 request, SDD-17 secret, SDD-23 supervise/client)
mount on top of this skeleton.

The spec MUST encode these acceptance-level (WHAT) requirements.
Override any /speckit-specify "informed guess" that would soften
them:

- The CLI's output is TTY-aware: human text on a TTY, JSON when
  piped or redirected. --no-color forces no ANSI codes.
- The exit-code contract is fixed: 0=ok, 1=generic error,
  2=input error, 3=auth error, 4=not-found, 5=permission error,
  78=stale config (sysexits convention). These codes are part
  of the public CLI contract — operators script against them.
- hush serve resolves the vault passphrase in this priority
  order: stdin pipe → TTY prompt → fail with ExitInputErr.
  NEVER from an environment variable.
- hush health calls the server's /hz; connection-refused is
  reported with a clear human message and ExitErr.
- hush revoke takes --server and --jti flags; the request is
  signed with the local client key and POSTed to /revoke.
- Secret values are never printed by any subcommand in this
  chunk.

The spec MUST NOT encode HOW (no library names, no specific cobra
patterns). Those are plan-phase.

Acceptance criterion: AC-1 (server CLI surface).

Action — run exactly one command:
  /speckit-specify "cmd/hush + internal/cli root: cobra-based CLI with TTY-aware output (text on TTY, JSON otherwise), fixed sysexits-style exit codes, and four subcommands — serve (passphrase from stdin pipe → TTY prompt → fail, NEVER env), health (calls /hz, clear connection-refused message), version (build version), revoke (signed POST to /revoke); no secret value ever printed"

The before_specify hook will create branch 014-cli-root-and-server-cmds.

If /speckit-specify produces [NEEDS CLARIFICATION] markers, check
each against the chunk contract / constitution. Otherwise leave
the marker — /speckit-clarify will handle it next session.

When the after_specify hook offers to auto-commit spec.md, accept.
```

---

## Prompt 2 — Clarify  (fresh session)

```
You are running the CLARIFY phase of SDD-14 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-14.md.

Run: /speckit-clarify

Accept the after_clarify auto-commit only if spec.md actually changed.
```

---

## Prompt 3 — Plan  (fresh session)

```
You are running the PLAN phase of SDD-14 (internal/cli root +
serve/health/version/revoke) of the hush project.

Read first (in order):
- /Users/mrz/projects/hush/.specify/memory/constitution.md  (full file — /speckit-plan runs a Constitution Check; VII non-negotiable: cobra-only, no viper)
- /Users/mrz/projects/hush/docs/SPEC.md  (FR-1, FR-16, FR-17, FR-21)
- /Users/mrz/projects/hush/docs/API.md  (GET /hz, POST /revoke shapes)
- /Users/mrz/projects/hush/docs/PACKAGE-MAP.md  (cmd/hush + internal/cli)
- /Users/mrz/projects/hush/docs/sdd/SDD-14.md  (the full chunk contract)

The plan MUST honour every item below. /speckit-plan runs a
Constitution Check — if it fires, fix the plan, do NOT bypass.

Scope:
- cmd/hush/main.go (calls cli.Execute(); os.Exit(rc))
- internal/cli/{root.go, serve.go, health.go, version.go,
  revoke.go, output.go, flags.go, exit_codes.go,
  root_test.go, serve_test.go, health_test.go, version_test.go,
  revoke_test.go, output_test.go}
- Subcommands implemented in this chunk: serve, health, version,
  revoke. Other subcommands (init, request, secret, supervise,
  client) wired in by SDD-15..23.
- Exported API:
    cmd/hush:  func main()
    internal/cli:
      func Execute() int
      var ExitOK = 0, ExitErr = 1, ExitInputErr = 2, ExitAuth = 3,
          ExitNotFound = 4, ExitPerm = 5, ExitConfigStale = 78

Implementation contract (HOW — locked):
- CLI lib: github.com/spf13/cobra. Constitution VII: NO viper —
  config is loaded by internal/config (SDD-06), not viper.
- Build version injected via -ldflags by GoReleaser
  (placeholder var Version = "dev" in version.go).
- output.go: detects TTY via golang.org/x/term; --no-color
  short-circuits ANSI; JSON output uses encoding/json with
  indent for TTY, no-indent for pipe.
- serve passphrase resolution flow (in order, first hit wins):
    1. stdin is a pipe (not TTY): read all stdin as passphrase
    2. stdin is a TTY: term.ReadPassword without echo
    3. neither: ExitInputErr with explicit message
  NEVER consult os.Getenv for any passphrase variable.
- health uses net/http to GET <server>/hz; connection-refused
  is reported with the literal text "could not connect to
  hush server at <addr>: ..." and ExitErr.
- revoke uses internal/transport/sign (SDD-08) to canonicalise
  + sign {jti, timestamp, nonce}; POSTs to /revoke; maps
  HTTP status to exit code.
- All subcommands accept the global flags --config, --verbose,
  --quiet, --no-color; root.go wires them.
- Tests use cobra's command-execution helpers; passphrase
  resolution tests use t.TempFile and pseudo-TTY libraries
  (github.com/creack/pty if needed — research note required).

Coverage target: 85%.
Constitutional principles in scope: VII, IX, X, XI.

Run: /speckit-plan

Accept the after_plan auto-commit.
```

---

## Prompt 4 — Tasks  (fresh session)

```
You are running the TASKS phase of SDD-14 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-14.md.

Run:
  /speckit-tasks "TDD-mandatory per Constitution VIII: include a test-writing task for every behaviour contract BEFORE the implementation task. Coverage target: 85%. Tests required: TestExecute_VersionPrintsBuildVersion, TestRoot_GlobalFlagsWired, TestOutput_TTYPicksText, TestOutput_NonTTYPicksJSON, TestOutput_NoColorStripsANSI, TestServe_PassphraseFromStdinPipe, TestServe_PassphraseFromTTYPrompt, TestServe_NoStdinNoTTY_ExitInputErr, TestServe_NeverReadsEnv (assert os.Getenv not called for any passphrase var), TestHealth_HappyPath, TestHealth_ConnectionRefusedExplicitMessage, TestRevoke_SignedRequestPosted, TestRevoke_BadStatusMapsToExitCode, TestExitCodes_ConstantValues. Integration test TestServe_StartAndShutdown starts serve in goroutine, sends SIGTERM, expects clean exit. Final phase MUST include magex format:fix, magex lint, magex test:race, and magex test:race -tags=integration."

Accept the after_tasks auto-commit.
```

---

## Prompt 5 — Implement  (fresh session)

```
You are running the IMPLEMENT phase of SDD-14 of the hush project.

Read /Users/mrz/projects/hush/docs/sdd/SDD-14.md.

Run: /speckit-implement

After /speckit-implement completes, do these steps from repo root:

1. Gates (all must pass clean):
     magex format:fix && magex lint && magex test:race
2. Integration tests:
     magex test:race -tags=integration
3. Verify coverage ≥ 85% on internal/cli:
     go test -cover ./internal/cli/
4. Smoke each subcommand help renders:
     ./hush --help
     ./hush serve --help
     ./hush health --help
     ./hush version --help
     ./hush revoke --help
5. Confirm build version injection works (./hush version prints
   non-"dev" when built with -ldflags).
6. Confirm os.Getenv was not called for any passphrase variable
   (grep internal/cli for Getenv).
7. Append "Exported API — locked at SDD-14" section to
   docs/PACKAGE-MAP.md under cmd/hush + internal/cli listing
   the locked API from the chunk doc.
8. Update docs/AC-MATRIX.md AC-1 row with the new test file paths.
9. Mark SDD-14 status `done` in docs/SDD-PLAYBOOK.md.

DECLINE the after_implement auto-commit. Make one combined commit
instead:
  git add cmd/hush/ internal/cli/ docs/PACKAGE-MAP.md \
          docs/AC-MATRIX.md docs/SDD-PLAYBOOK.md \
          specs/<feature-dir>/tasks.md
  git commit -m "feat(cli): root + serve/health/version/revoke subcommands (SDD-14)"

Final message: confirm gates passed, race-clean, coverage ≥ 85%,
all four subcommands runnable via help, build version injection
verified, no env-var passphrase reads, AC-1 row updated,
SDD-PLAYBOOK updated, and the combined commit created.
```
