# Phase 0 Research — SDD-14

**Branch**: `014-cli-root-and-server-cmds` | **Date**: 2026-05-01

This document resolves every Technical-Context question raised by `plan.md`
before Phase 1 design work begins. Each section follows the
**Decision / Rationale / Alternatives considered** structure. No
`NEEDS CLARIFICATION` markers remain after this file is read top-to-bottom.

---

## 1. CLI library choice

**Decision**: `github.com/spf13/cobra` v1.8+ as a NEW direct dependency. No
`viper`. Subcommand handlers are constructed inside `cli.Execute(ctx)` so
no package-level mutable state is introduced.

**Rationale**:
- Constitution VII non-negotiable: "The binary is small, single-file, with
  cobra subcommands."
- Cobra provides persistent flags inherited by every subcommand
  (`--config`, `--verbose`, `--quiet`, `--no-color`), generated `--help`
  text, and the `RunE` error-returning pattern that maps cleanly to
  `internal/cli/exit_codes.go::mapErr`.
- The trusted-sources hierarchy (Constitution XI):
  - **stdlib**: `flag` does not provide subcommands or persistent flags.
  - **sigil baseline**: no CLI-framework equivalent.
  - **bsv-blockchain org**: no CLI-framework equivalent.
  - **wider ecosystem**: cobra is the de-facto Go CLI standard
    (kubectl, hugo, terraform, gh, all use it). Stable since 2014;
    weekly Dependabot review will catch CVEs.
- Pinned at v1.8+ (most recent minor as of 2026-05-01); transitive footprint
  is dominated by `spf13/pflag` (already a cobra dep, not added separately).

**Alternatives considered**:
- **`urfave/cli`**: smaller dep tree but less idiomatic for a noun-verb
  pattern; no `RunE`-style error contract; help text generation is
  sparser.
- **`alecthomas/kingpin`**: archived/maintenance-only since 2020.
- **stdlib `flag` + hand-rolled subcommand dispatch**: rejected by
  Constitution VII ("with cobra subcommands"); also reinventing
  persistent-flag inheritance is a known footgun.
- **`viper`**: explicitly forbidden by SDD-14 chunk contract and by
  Constitution XI's "minimal direct dependencies" rule. Configuration is
  already loaded by `internal/config.LoadServer` (locked at SDD-06);
  `viper` would duplicate that logic with a parallel surface.

---

## 2. TTY detection (per stream)

**Decision**: Use `golang.org/x/term.IsTerminal(fd)` (already on the
dependency tree as v0.42.0) for each output stream independently.
`internal/cli/output.go` exposes
`type Stream struct { w io.Writer; isTTY bool; noColor bool }` and one
factory each for stdout and stderr that captures the file descriptor at
construction time. The TTY check happens once per `cli.Execute(ctx)` call,
before any subcommand runs, and is passed through cobra's `Context`.

**Rationale**:
- FR-003 + Edge Case "Output context detection" mandate per-stream
  detection — diagnostics on stderr must not bleed onto stdout when
  stdout is piped (the JSON consumer must parse stdout cleanly even if
  stderr is going to a terminal).
- `IsTerminal` is the canonical Go idiom; it's a one-syscall check on
  the underlying file descriptor (`isatty(2)`).
- Constitution IX "no globals carrying mutable runtime state" — the
  `Stream` is created per `Execute` call, threaded through the
  `*cobra.Command` via `cmd.SetContext`, and accessed via
  `cmd.Context().Value(streamKey)`. Tests substitute their own writers
  without touching package state.

**Alternatives considered**:
- **Detect via env vars (`TERM`, `NO_COLOR`)**: misses redirected file
  output; gives false negatives in launchd `StandardOutPath` cases. Used
  by `--no-color`'s sibling concept but not as a primary signal.
- **`mattn/go-isatty`**: a third-party wrapper around the same syscalls;
  adding a new dep when `golang.org/x/term` already provides
  `IsTerminal` violates Constitution XI (no duplication of stdlib-or-equivalent).
- **One-shot detect on stdout, infer stderr**: rejected — fails the
  Edge Case "stdout is a terminal but stderr is not (or vice versa)".

---

## 3. Pseudo-TTY library for `term.ReadPassword` no-echo testing

**Decision**: `github.com/creack/pty` v1.1.21+ as a NEW **test-only**
dependency (imported only from `internal/cli/serve_test.go`). Used solely
for `TestServe_PassphraseFromTTYPrompt` to assert that
`term.ReadPassword` is called against an actual PTY and that the typed
bytes do not appear in any captured terminal output.

**Rationale**:
- The no-echo property is security-critical: a regression that quietly
  echoes the passphrase to the controlling terminal would silently
  destroy operator trust. The only faithful proof is to drive a real
  PTY and capture both ends.
- Mocking `golang.org/x/term.ReadPassword` at the function level (e.g.,
  via a global `var readPassword = term.ReadPassword`) leaves the
  no-echo property untested at the syscall layer — a future change to
  the call site (e.g., switching to a custom `tcsetattr` dance) would
  ship a regression.
- `creack/pty` is pure Go, depends only on `golang.org/x/sys` (already
  on the tree), is unix-only (acceptable: the CLI targets macOS + Linux
  per spec Assumption 1), and has been the canonical Go PTY library for
  ten years (used by docker, kubectl exec, dlv).
- Test-only import means it never appears in the release binary's
  dependency closure (`go.sum` records it; the linker excludes it).

**Alternatives considered**:
- **Skip the no-echo unit test entirely**: rejected by Constitution
  VIII (security-critical paths reach 100% coverage).
- **Use `os.Pipe` instead of a PTY**: a pipe is not a TTY, so
  `term.IsTerminal(fd)` returns false, so `serve` falls through to
  the pipe-read path — defeats the test premise. Confirmed by
  reading the `golang.org/x/term` source.
- **Build a minimal `tcgetattr` wrapper in-tree**: reinvents `creack/pty`,
  adds maintenance burden, fails Constitution XI's "smallest dep
  surface is the strongest dep surface" guidance — for a test-only
  consumer, the ratio of cost to benefit lands the other way.

---

## 4. HTTP client construction (`hush health`, `hush revoke`)

**Decision**: One `*http.Client` per subcommand call, constructed inside
`runHealth` / `runRevoke` with `Timeout: 5 * time.Second` (FR-015a) and
no other transport tuning. The transport is an `*http.Transport` with
`MaxIdleConnsPerHost: 1`, `DisableKeepAlives: true`, and otherwise the
zero value. The client is not stored in a struct; it is a local variable.

**Rationale**:
- FR-015a (clarification 2026-05-01): single fixed 5-second total-request
  timeout covering connect + write + read + close, applied identically
  to `health` and `revoke`. `http.Client.Timeout` is the textbook
  total-request bound.
- Constitution IX: no globals carrying mutable runtime state; per-call
  construction is cheap (the Go runtime pools transports below the
  Client level when needed, but for one-shot CLI commands keep-alive
  reuse provides no benefit).
- `DisableKeepAlives` ensures the client closes the TCP connection
  after the response — operationally important for a CLI that exits
  immediately afterward; avoids a "TIME_WAIT pile-up" smell in CI logs.
- Errors from the transport are wrapped via `fmt.Errorf("...: %w", err)`
  and matched by `errors.Is(err, context.DeadlineExceeded)` (timeout)
  vs `errors.Is(err, syscall.ECONNREFUSED)` (refused) for the
  message-classification logic in FR-014/FR-015.

**Alternatives considered**:
- **Use `http.DefaultClient`**: rejected — has no timeout by default
  (would wait indefinitely on a hung server); shared global state is
  unsuitable for one-shot CLIs.
- **Long-lived `*http.Client` cached at root level**: no benefit, since
  `health` / `revoke` exit after one call. Adds package-level state
  (Constitution IX violation).
- **Per-phase timeouts (connect 1 s, write 1 s, read 3 s)**: rejected
  by FR-015a — operators get a single number to reason about.

---

## 5. Output formatter implementation

**Decision**: `internal/cli/output.go` exposes:
- `type Stream` (unexported fields: `w io.Writer`, `isTTY bool`, `noColor bool`).
- `func NewStream(w io.Writer, isTTY, noColor bool) *Stream` (test-friendly).
- `func StreamFor(f *os.File, noColor bool) *Stream` (production: `term.IsTerminal(f.Fd())`).
- `(*Stream).WriteText(format string, args ...any) error` (skips ANSI when `noColor`).
- `(*Stream).WriteJSON(v any) error` (uses `encoding/json`; `MarshalIndent("", "  ")` when `isTTY`, `Marshal` when `!isTTY`; trailing `\n` always).
- `(*Stream).Auto(text string, jsonV any) error` — text on TTY, JSON otherwise; the workhorse used by `version`, `health`, `revoke`.

ANSI suppression: a tiny in-package regexp `\x1b\[[0-9;]*m` strips
escape sequences when `noColor` is true. The simpler approach — only
ever emit ANSI from a single helper that respects `noColor` — is also
followed; the regexp is a defense-in-depth catch for any third-party
text passed through.

**Rationale**:
- FR-003 (TTY-aware output), FR-004 (no-color overrides terminal
  detection), FR-019a (locked JSON shape on non-TTY) all converge on
  one formatter.
- `encoding/json` is stdlib (Constitution XI); indent-on-TTY makes
  human inspection of JSON-piped output (rare but used) readable while
  pipe output stays compact.
- The `Auto` method centralises the "pick text or JSON" decision so no
  subcommand reimplements it.

**Alternatives considered**:
- **`fatih/color` library**: third-party, adds dep, and the project
  emits very little ANSI (only the optional success-checkmark + warn
  marker). Rolling our own under 30 LOC is cheaper.
- **Templates via `text/template`**: overkill for the four subcommands'
  output shapes; YAGNI.

---

## 6. Passphrase resolution mechanism

**Decision**: Inside `internal/cli/serve.go`, define an unexported
testable seam **as a field of `serveDeps`**, not as a package-level var
(Constitution IX — no mutable package-level state):

```go
type passphraseSource func(ctx context.Context, in *os.File, prompt io.Writer) (*securebytes.SecureBytes, error)

type serveDeps struct {
    configPath       string
    passphraseSource passphraseSource     // defaults to resolvePassphrase in RunE
    approverFactory  approverFactory      // defaults to newProductionBotApprover
    auditMirror      *audit.DiscordMirror // defaults to nil
    listener         net.Listener         // defaults to nil → chassis binds itself
}

func resolvePassphrase(ctx context.Context, in *os.File, prompt io.Writer) (*securebytes.SecureBytes, error) {
    // 1. stdin is a pipe (not TTY): io.ReadAll, strip exactly one trailing \n or \r\n, wrap in *securebytes.SecureBytes.
    // 2. stdin is a TTY: write prompt to `prompt`, term.ReadPassword(int(in.Fd())), wrap in *securebytes.SecureBytes.
    // 3. neither: return (nil, errNoPassphraseSource) — caller maps to ExitInputErr.
}
```

The default `serveCmd.RunE` constructs `serveDeps{passphraseSource:
resolvePassphrase, approverFactory: newProductionBotApprover, ...}` and
calls `runServe(ctx, deps)`. The integration test constructs its own
`serveDeps` with stub fields. Constitution IX preserved: `serveDeps` is
a transient stack-allocated value; `resolvePassphrase` is an
unexported function (not a var).

The pipe-vs-TTY discrimination uses the **same** `golang.org/x/term.IsTerminal(in.Fd())`
function as the output detection; if false, a `Stat()` check
(`(stat.Mode() & os.ModeCharDevice) == 0` AND `stat.Size() ≥ 0`)
confirms the source is a pipe or a regular file. The combined check
matches the spec's "Passphrase source ambiguity" Edge Case
(zero-byte pipe → no-passphrase-on-stdin → fall through to TTY only
if a TTY is attached, otherwise `ExitInputErr`).

**Rationale**:
- FR-008 + FR-008a + FR-009 + clarification 2026-05-01 ("strip exactly
  one trailing `\n` or `\r\n`, preserve all other bytes verbatim").
- Constitution IX: no globals — `defaultPassphraseSource` is a
  package-level `var` only as a test seam; the production code path
  takes it as a parameter to `runServe`.
- Constitution X: passphrase wrapped in `*securebytes.SecureBytes`
  immediately after the read, before any `slog` or `fmt` could log it.
- **No `os.Getenv` call anywhere in the resolution path** — FR-009
  explicit. Verified by `TestServe_NeverReadsEnv`: a static AST scan
  over `internal/cli/*.go` that fails on any `os.Getenv` reference.

**Alternatives considered**:
- **A separate library wrapper around stdin** (e.g., `huh`, `survey`):
  unwarranted dep; the stdlib + `golang.org/x/term` cover every
  required behaviour.
- **Heuristic: try stdin pipe, on EOF fall through to TTY**: fails the
  "Passphrase source ambiguity" edge case (zero-byte pipe must NOT
  fall through to TTY when stdin is the pipe — that would let an
  upstream tool's silent-failure look like an interactive prompt).

---

## 7. POSIX-line trailing whitespace stripping

**Decision**: Strip exactly one trailing `\r\n` if the read ends with
those two bytes; else strip exactly one trailing `\n` if the read ends
with `\n`; else preserve all bytes. **No other transformation** —
leading whitespace, internal whitespace, other trailing whitespace
(extra `\n`, trailing space, tabs) is preserved verbatim. Implementation
is a 6-line helper:

```go
func stripPOSIXLineEnd(b []byte) []byte {
    n := len(b)
    if n >= 2 && b[n-2] == '\r' && b[n-1] == '\n' {
        return b[:n-2]
    }
    if n >= 1 && b[n-1] == '\n' {
        return b[:n-1]
    }
    return b
}
```

**Rationale**: FR-008a clarification 2026-05-01. Test
`TestServe_PassphraseFromStdinPipe` covers all four edge cases: bare
`\n`, bare `\r\n`, two trailing `\n`s (only one stripped), and
leading-whitespace preservation.

**Alternatives considered**:
- **`strings.TrimRight(s, "\r\n")`**: rejected — strips multiple
  trailing newlines, violating "preserve all other bytes verbatim".
- **`strings.TrimSpace`**: rejected — strips leading whitespace and
  internal-around-edges whitespace, violating the clarification.

---

## 8. Build version injection

**Decision**: `internal/cli/version.go` declares three package-level
`var`s:

```go
var (
    Version = "dev"
    Commit  = "unknown"
    Date    = "unknown"
)
```

GoReleaser injects production values via `-ldflags`:

```
-X github.com/mrz1836/hush/internal/cli.Version={{.Version}}
-X github.com/mrz1836/hush/internal/cli.Commit={{.ShortCommit}}
-X github.com/mrz1836/hush/internal/cli.Date={{.Date}}
```

(The `.goreleaser.yml` change is recorded in `tasks.md` and `quickstart.md`
but is NOT applied by this chunk's source patch — release wiring is a
release-engineering concern outside the SDD-14 file list. The Implement
phase asserts the placeholders work; the GoReleaser config update lands
when v0.1.0 is tagged.)

The `version` subcommand prints the locked JSON shape on non-TTY (FR-019a):

```json
{"version":"<string>","commit":"<string>","date":"<string>"}
```

**Rationale**: This is the standard Go pattern for build-time injection;
GoReleaser supports it natively. The three keys, in the locked order,
match the spec clarification 2026-05-01.

**Alternatives considered**:
- **`debug/buildinfo.ReadBuildInfo()`**: provides Go module path + main
  version but requires a tagged release for `Version` to populate;
  insufficient on dev builds where the operator wants to know the
  commit. Rejected.
- **Single string constant set at build**: loses commit + date data,
  fails FR-019a's three-key shape.

---

## 9. Exit-code mapping (the heart of the contract)

**Decision**: `internal/cli/exit_codes.go` declares the seven constants
plus an unexported `mapErr(err error) int` helper. The mapper walks
`errors.Is` against the locked sentinel sets from upstream packages,
falling back to the default for the subcommand:

| Sentinel set | Source package | Mapped exit code |
|--------------|---------------|------------------|
| `ErrFlagConflict`, `ErrMissingFlag`, `ErrConfigUnreadable`, config-package decode/validate sentinels | `internal/cli`, `internal/config` | `ExitInputErr` (2) |
| `token.ErrSignatureInvalid`, `token.ErrTokenExpired`, `token.ErrTokenRevoked`, `token.ErrTokenExhausted`, `token.ErrIPMismatch`, `sign.ErrSignatureInvalid` | `internal/token`, `internal/transport/sign` | `ExitAuth` (3) |
| `vault.ErrSecretNotFound`, `server.ErrSecretMissing`, server's "unknown jti" mapped to 404 | `internal/vault`, `internal/server` | `ExitNotFound` (4) |
| `os.ErrPermission`, `server.ErrFileModeLoose` | `os`, `internal/server` | `ExitPerm` (5) |
| **everything else (network failures, server 5xx, panics)** | — | `ExitErr` (1) |
| **never raised by this chunk** | — | `ExitConfigStale` (78) |

The `mapErr` helper is unit-tested with a table of (input error, expected
code) covering each sentinel.

**Rationale**: Gives every terminal outcome exactly one mapping
(SC-002); operators can `script $code` against any subcommand's exit
without parsing stderr.

**Alternatives considered**:
- **Per-subcommand mappers**: rejected — duplication, drift risk.
- **`switch err.(type)`**: rejected — fails when errors are
  fmt.Errorf-wrapped; `errors.Is` is the idiomatic match.

---

## 10. `serve` chassis composition

**Decision**: `runServe(ctx, deps serveDeps) error` performs the
following sequence (each step's failure maps to a documented exit code):

1. Load config: `internal/config.LoadServer(ctx, deps.configPath)` →
   error → `ExitInputErr`.
2. Resolve passphrase: `deps.passphraseSource(ctx, ...)` →
   `errNoPassphraseSource` → `ExitInputErr`; any other error →
   `ExitErr`.
3. Derive master seed: `internal/keys.DeriveMasterSeed(ctx,
   passphrase, salt)` where `salt` is read from
   `cfg.Server.StateDir + "/secrets.vault"` header (the same file the
   chassis later loads — read once, header-only). Error → `ExitErr` (or
   `ExitAuth` if `vault.ErrAuthFailed` surfaces from the salt-extraction
   path; the salt-only header read has its own error for not-found
   that maps to `ExitNotFound`).
4. Derive subkeys via `internal/keys.DeriveJWTSigningKey`,
   `DeriveVaultEncKey`, `DeriveAuditSigningKey`. Each returns
   `*ecdsa.PrivateKey` or `[]byte`; both are immediately wrapped in
   `*securebytes.SecureBytes` where applicable.
5. Construct the audit writer: `audit.NewWriter(ctx, cfg.Server.AuditLog,
   auditSignKey, deps.auditMirror, logger)`. Run it via
   `go writer.Run(ctx)` owned by a sub-context that cancels with
   `serve`'s parent ctx.
6. Construct the Discord approver: `deps.approverFactory(ctx, cfg, logger)`
   — production path reads the bot token from the OS keychain via the
   helper recorded in §11; integration-test path returns a
   `testutil.DiscordStub`-class approver. **`approverFactory` errors
   on missing/empty token** but does NOT error on transport-down
   (FR-013a — the chassis surfaces the bot's connect state via `/hz`,
   it does not refuse to start).
7. Construct the chassis: `srv, err := server.New(server.Deps{ ... })`.
   Validation errors map to `ExitInputErr` (missing dep) or
   `ExitConfigStale`-adjacent (`ExitErr`) for unexpected.
8. Mount the four locked routes: `srv.RegisterHandlers()`.
9. Bind context to OS signals via `signal.NotifyContext(ctx,
   syscall.SIGINT, syscall.SIGTERM)`.
10. `if err := srv.Run(signalCtx); err != nil { return err }` — the
    chassis's own graceful-shutdown path runs; clean exit returns
    `nil`. The caller maps `nil` → `ExitOK` and any chassis error
    through `mapErr`.

The keychain bot-token loader (§11) is invoked from inside
`approverFactory`, not at the top level, so the integration test can
substitute the entire factory and never touch the keychain.

**Rationale**:
- Sequence matches the chassis's own `Run` ordering (startup checks
  internal to the chassis run after `New` validates Deps; this is
  intentional — the CLI only orchestrates config + passphrase, the
  chassis owns the rest per `docs/PACKAGE-MAP.md` "must not contain
  HTTP handler logic, vault parsing").
- Constitution IX goroutine ownership: the only goroutine `serve`
  owns directly is the audit `Run`, and it is parented to the same
  ctx the chassis uses, so cancellation is unified.

**Alternatives considered**:
- **Hoist all wiring into `cmd/hush/main.go`**: rejected by
  `docs/PACKAGE-MAP.md` ("No business logic belongs in `cmd/hush/`").
- **Lazy-construct the approver inside the chassis**: rejected — the
  chassis's `Deps` already requires `Approver` at `New` time, by design.

---

## 11. Discord bot token retrieval (last-mile concern)

**Decision**: A small unexported helper inside `serve.go`,
`loadBotToken(ctx context.Context, item string) (*securebytes.SecureBytes, error)`,
shells out to `security find-generic-password -s <item> -w` on Darwin
and to `secret-tool lookup service hush attribute <item>` on Linux. The
helper:

- Validates the item-name token (`^[a-zA-Z0-9._-]{1,128}$`) before
  invoking the subprocess so the operator-supplied config field cannot
  inject a flag (no command injection; `exec.CommandContext` with a
  fixed argv vector closes the door regardless, but defense in depth).
- Caps the subprocess's stdout read at 1 KiB; bot tokens are ≤ 100 B.
- Wraps the read bytes in `*securebytes.SecureBytes` immediately and
  zeroes the intermediate buffer.
- Returns `errBotTokenMissing` (mapped to `ExitInputErr`) when the
  subprocess exits non-zero with a "no such item" stderr; returns
  `errBotTokenSubprocess` (mapped to `ExitErr`) for any other failure.

**Rationale**:
- AC-1 (the chunk's primary AC) requires `hush serve` to start a
  responsive server. The chassis requires a non-nil `Approver` at
  construction; the production approver requires a non-empty bot
  token. The keychain helper is the smallest production-correct path
  that does not pull in a new package or new dependency.
- macOS `security` and Linux `secret-tool` are pre-installed on every
  supported operator host. The helper has no Go dependencies beyond
  `os/exec` (stdlib).
- The helper is < 60 LOC; hoisting it into a new package
  (`internal/keychain`) is premature — `init` (SDD-15) is the right
  chunk to extract a shared helper if/when keychain reads happen in
  multiple places. For now, the only consumer is `serve`.

**Alternatives considered**:
- **Defer to a future chunk and use a stub approver in production**:
  rejected — silently shipping a stub approver in the production binary
  would violate Constitution II's "no service account that bypasses
  approval".
- **`zalando/go-keyring` Go library**: a new direct dep that wraps the
  same OS-level commands; adds maintenance cost for no functional gain
  over a 60-LOC `os/exec` helper.
- **Read the token from a config-file field**: rejected by Security
  Requirements ("Passphrase from macOS Keychain via stdin pipe") —
  ditto for the bot token, which the threat model treats as a secret.
- **Split keychain into `internal/keychain/`**: a real option, but
  introducing a package solely for one consumer this chunk delays
  delivery; SDD-15's `init` will write the keychain entry, at which
  point a shared package becomes warranted.

---

## 12. Integration test architecture (`TestServe_StartAndShutdown`)

**Decision**: The integration test (`//go:build integration`) lives in
`internal/cli/serve_test.go` and uses the `runServe(ctx, deps)` seam:

```go
//go:build integration

func TestServe_StartAndShutdown(t *testing.T) {
    // 1. Build a t.TempDir() vault via testutil.NewTestVault.
    // 2. Build a config.Server with that vault path + a free-port
    //    listener address obtained from net.Listen("tcp", "127.0.0.1:0").
    //    Override RequireTailscale = false for the test only (the chassis
    //    accepts a config-driven override; the production CLI never sets
    //    it).  Note: this is the chassis's locked tunable, not a CLI escape
    //    hatch.
    // 3. Build serveDeps with:
    //      - passphraseSource returning the test passphrase.
    //      - approverFactory returning testutil.NewDiscordStub().
    //      - auditMirror = nil (no Discord mirror in tests).
    //      - configPath pointing at the temp config file.
    // 4. ctx, cancel := context.WithCancel(context.Background())
    //    errCh := make(chan error, 1)
    //    go func() { errCh <- runServe(ctx, deps) }()
    // 5. Poll GET /hz until 200 OK or 2s timeout.
    // 6. cancel(); assert errCh receives nil within 5s; assert
    //    testutil.AssertSentinelAbsent on captured stderr.
}
```

**Rationale**:
- Constitution VIII integration-test requirement for AC-1.
- Race-clean by virtue of using ctx-cancellation as the only shutdown
  signal (no `os.Process.Signal` in the test process).
- Uses already-locked `internal/testutil` primitives (`NewTestVault`,
  `NewDiscordStub`, `AssertSentinelAbsent`) per SDD-04.

**Alternatives considered**:
- **`exec.Command` the actual binary in a subprocess**: heavier;
  loses code-coverage instrumentation on the spawned binary; would
  require building the binary as a test prerequisite.
- **Send `SIGTERM` to `os.Getpid()` from the test**: works on unix
  but races against Go's signal handler, and the test would need to
  reset `signal.Notify` afterward to avoid leaking handlers into
  subsequent tests in the same package.

---

## 13. Static assertion: no `os.Getenv` in `internal/cli`

**Decision**: `internal/cli/serve_test.go::TestServe_NeverReadsEnv`
performs a build-time static check by reading every `*.go` file under
`internal/cli/` (excluding `*_test.go`) and asserting the literal
substring `os.Getenv` does not appear. The check uses
`go/parser.ParseFile` for AST-level reliability — looking for any
`*ast.SelectorExpr{X: "os", Sel: "Getenv"}` reference.

**Rationale**:
- FR-009 mandates the prohibition is verifiable.
- AST-level scan is robust to identifier-renaming via dot imports
  (which Constitution IX would forbid anyway, but defense in depth).
- A grep-only approach in CI is also available (SDD-14 Prompt 5 step 6),
  but a unit test fails fast and runs on every PR without a separate
  CI job.

**Alternatives considered**:
- **`golangci-lint` custom rule**: heavier setup; the AST check is
  ~20 LOC and lives next to its assertion.
- **Trust the linter's `gochecknoglobals`**: doesn't catch function
  calls, only variable declarations.

---

## 14. Cobra version pinning + `go get` order

**Decision**: `go get github.com/spf13/cobra@v1.8.1` (or current latest
v1.8.x as of `tasks.md` execution) is run before any source file is
written; `go get github.com/creack/pty@v1.1.21` runs alongside.
`go.sum` is committed as part of the chunk's combined commit.
`govulncheck ./...` runs in CI on every PR (NFR-3) to catch
post-merge advisories.

**Rationale**: Following the existing project convention (every prior
chunk that introduced a dep ran `go get` as the first task). Pinning
to a minor (`v1.8.x`) accepts patch-level Dependabot updates; pinning
to a patch would generate weekly low-value PRs.

**Alternatives considered**:
- **Pin to a major+minor only (`v1.8`)**: accepts breaking changes
  within v1.8 series — there are none, but the discipline is to lock
  the exact patch in `go.sum` and let Dependabot bump it.

---

## 15. Documentation updates deferred to Implement phase

**Decision**: Per SDD-14 Prompt 5 steps 7–9, the following docs are
edited only at the end of the Implement phase:

- `docs/PACKAGE-MAP.md` — append "Exported API — locked at SDD-14"
  block under `cmd/hush/` and `internal/cli/`.
- `docs/AC-MATRIX.md` — update the AC-1 row with the new test paths.
- `docs/SDD-PLAYBOOK.md` — flip SDD-14 status to `done`.

**Rationale**: These touches require the actual exports + test paths
to exist, which only happens after `/speckit-implement` has produced
the source. Putting them earlier risks drift between the locked-API
text and the code.

---

## Open questions remaining

**None.** Every Technical Context entry in `plan.md` resolves to a
documented decision above. Phase 1 may proceed.
