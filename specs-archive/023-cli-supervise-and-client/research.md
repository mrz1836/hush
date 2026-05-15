# Phase 0 — Research: `hush supervise` + `hush client status` + `hush client refresh`

**Branch**: `023-cli-supervise-and-client` | **Date**: 2026-05-12

Every spec [NEEDS CLARIFICATION] from `spec.md` is resolved here. Each note
follows the locked **Decision / Rationale / Alternatives considered** shape.

---

## R-1 — Status-socket verb dispatch (`status` vs `refresh`)

**Decision**: Extend `internal/supervise/socket.go` so the accept-loop's
per-connection handler reads the first line via `bufio.NewReader(conn).ReadString('\n')`,
matches the trimmed payload against the two recognised verbs (`status`, `refresh`),
and dispatches:

- `status` (default for any unrecognised verb, preserving SDD-22 §2.5
  "request payload is advisory" backward-compatibility) → render the existing
  `statusJSON` document via `renderStatus(snapshot)`.
- `refresh` → invoke the orchestrator-supplied `refreshHandler func(ctx context.Context) error`
  callback wired via a new package-private `(*StatusServer).attachRefreshHandler(...)`
  method. The callback's return value is the terminal acknowledgement: `nil` → the
  handler writes the byte literal `{"ok":true}\n` to the conn; non-nil → the handler
  writes `{"ok":false,"error":"<one-line message>"}\n` with the error message stripped
  of any byte that could be misread as a secret marker (FR-023-27/28).

**Rationale**: FR-023-20 mandates a `refresh` verb on the supervisor's existing
status socket; SDD-22's current implementation reads the request line but discards
it. Adding a verb dispatch is the smallest possible change that satisfies
FR-023-20 without (a) opening a second socket, (b) introducing HTTP, or (c) leaking
business logic into `internal/cli`. The `attachRefreshHandler` precedent mirrors
the existing `(*StatusServer).attach(StatusInputs)` and `(*Refiller).attach(...)`
patterns from SDD-21/SDD-22 — no new exported API surface, just a package-private
wiring hook the orchestrator uses post-construction.

**Alternatives considered**:
- **Separate `refresh` socket** — doubles FS-perms surface, contradicts
  CONFIG-SCHEMA.md's `status_socket` (singular) field, adds a second `net.Listen`
  + cleanup lifecycle. Rejected.
- **HTTP-over-Unix-socket with `/status` and `/refresh` paths** — imports
  `net/http`, directly violating Constitution V and SDD-22's locked anti-contract
  (`NEVER add HTTP`). Rejected.
- **Skip refresh, rely on scheduler** — violates FR-023-20 and SC-023-4 outright.
  Rejected.

---

## R-2 — Socket-path resolution precedence

**Decision**: `hush client status` and `hush client refresh` resolve the socket
path with the following deterministic precedence (FR-023-15):

1. `--socket <abs-path>` if supplied → use verbatim. Empty string treated as
   "not supplied" (cobra `StringVar` default).
2. else if `--supervisor NAME` supplied → call
   `supervise.SocketPathForSupervisor(name)` (NEW: pure path-derivation helper
   in `socket_{darwin,linux}.go`; production-callable, not test-fixture-only
   like `defaultRuntimeDir`).
3. else → call `supervise.EnumerateSupervisorSockets()` (NEW: scans the
   platform runtime dir for files matching the
   `hush-supervise-*.sock` (Linux) / `supervise-*.sock` (Darwin) naming scheme
   and returns the absolute paths). Exactly one match → use that path; zero
   matches or > 1 matches → return wrapped `errSocketAmbiguous` (mapped to
   `ExitInputErr` per FR-023-15 (4)).

**Rationale**: Centralising the platform-specific path scheme in
`internal/supervise/socket_{darwin,linux}.go` keeps the CLI files free of
`runtime.GOOS` branches (Constitution VII + SDD-23 anti-contract). The
enumeration helper is the natural place to encode the platform naming scheme
already documented in FR-12 / DAEMONS.md §7. The precedence rule is documented
in the spec; this decision binds the implementation to it.

**Alternatives considered**:
- **CLI scans the FS directly via `os.UserCacheDir` / `XDG_RUNTIME_DIR`** — would
  embed `runtime.GOOS` logic in `client.go`. Rejected.
- **Mandatory `--supervisor`/`--socket` with no auto-detect** — fails the
  single-host common case documented in DAEMONS.md ("auto-select when exactly one
  socket exists"). Rejected — explicit FR-023-15 (3) requirement.

---

## R-3 — Dry-run canonicalisation (no signing)

**Decision**: `hush supervise <path> --dry-run` builds a Go struct mirroring the
canonical `/claim` payload shape, marshals it via
`sign.CanonicalJSON(payload)`, and writes the bytes to stdout followed by a
single `\n`. **No** `sign.Sign(...)` call is made on the dry-run path
(FR-023-9 prohibits any Discord-adjacent or vault-adjacent action). The
payload struct is a private local type in `supervise.go`, populated from the
loaded `*supervise/config.Supervisor`:

```go
type claimPreview struct {
    Name          string   `json:"name"`
    Reason        string   `json:"reason"`
    Scope         []string `json:"scope"`
    SessionType   string   `json:"session_type"`
    RequestedTTL  string   `json:"requested_ttl"`
    MachineIndex  uint32   `json:"machine_index"`
}
```

`requested_ttl` is rendered as a duration string (`time.Duration.String()`) to
match the format the real signing path would send. Config validation runs
BEFORE the dry-run branch (FR-023-10): an invalid config produces
`ExitInputErr` with no stdout output.

**Rationale**: The dry-run path is a preview affordance, not a transmission
path. The operator's contract is "see exactly the canonical bytes that would
have been signed". `CanonicalJSON` is the locked deterministic encoder
(SDD-08); reusing it guarantees the preview matches what the real signing
path would produce, byte-for-byte. Skipping `Sign` honours FR-023-9 and avoids
the keychain access that production sign would trigger.

**Alternatives considered**:
- **Render via `encoding/json.Marshal` with map keys hand-sorted** — duplicates
  SDD-08's canonicaliser and risks divergence. Rejected.
- **Render JSON-with-indent for human readability** — violates the
  "canonical-form" requirement (FR-023-7 specifies alphabetical key order,
  compact spacing). Rejected.

---

## R-4 — `--grace-window` and `--no-cache` flag handling

**Decision**: Both flags are parsed by cobra (`--grace-window` is
`Duration`-typed via `pflag.DurationVar`, default zero; `--no-cache` is `Bool`,
default false). After config load and BEFORE any orchestrator wiring:

```go
// FR-023-12: validate --grace-window range (>0, ≤ 4h) before any side effect.
if graceWindow > 0 {
    if graceWindow > 4*time.Hour {
        return fmtError(errInvalidGraceWindow, "must be ≤ 4h")
    }
    cfg.CacheGraceTTL = graceWindow
}
// FR-023-14: --no-cache wins.
if noCache {
    cfg.CacheSecretsForRestart = false
    // graceWindow value (if any) is silently ignored — explicit per FR-023-14.
}
```

Negative or zero `--grace-window` values are rejected at flag-parse time by
`pflag.DurationVar`'s built-in handling (zero defaults are valid; an explicit
zero `--grace-window 0s` is treated as "use config value", consistent with
"default = absent" semantics).

**Rationale**: The flag precedence rules are spec-locked (FR-023-11/12/13/14);
this note binds them to a deterministic implementation. Validating
`--grace-window` BEFORE any state.Store / pidfile / socket side effect keeps
the input-error path side-effect-free (FR-023-5).

**Alternatives considered**:
- **Treat `--no-cache --grace-window 30m` as a conflict error** — would surprise
  operators using both for tooling reasons; the spec explicitly says
  "`--no-cache` wins, the `--grace-window` value is ignored without error".
  Rejected on spec grounds.
- **Validate the cap inside `supervise/config`** — config validates the
  config-file value; a flag-override is logically a CLI concern. Validating
  here keeps responsibilities clean. Confirmed.

---

## R-5 — TTY detection and `--json` override for `hush client status`

**Decision**: TTY detection follows the project-wide convention (NFR-10):
`golang.org/x/term.IsTerminal(int(os.Stdout.Fd()))` is the sole signal.
A `--json` boolean flag is added to `hush client status` only (FR-023-17a);
when true, JSON is emitted regardless of TTY-ness. `hush client refresh` has
NO format flag. The decision tree:

```text
if --json → emit JSON
else if term.IsTerminal(stdout) → emit human summary
else → emit JSON
```

The human-summary writer (`writeHumanStatus(w io.Writer, doc statusDoc)`) is
a pure function that takes the unmarshalled status doc and produces the
labelled text format. The JSON path writes the raw bytes received from the
socket verbatim (no re-marshal — preserves the supervisor's exact byte
output, which is the schema-authoritative representation).

**Rationale**: The project-wide TTY convention (NFR-10) is the authoritative
rule. `--json` is the spec-required override (FR-023-17a). The "JSON path
writes raw bytes" decision is a defence-in-depth measure: re-marshalling
through a Go struct could re-order fields or alter formatting; passing the
supervisor's bytes verbatim eliminates that risk and makes the test surface
trivial (compare bytes-in-bytes-out).

**Alternatives considered**:
- **Single template renderer for both paths** — couples human formatting to
  JSON shape, complicates the schema-conformance test. Rejected.
- **Add a `--human` flag for symmetry** — explicitly rejected in the spec
  (FR-023-17a "there is no symmetric `--human` flag"). Honour the spec.

---

## R-6 — Timeout discipline

**Decision**:
- `hush client status`: total deadline **2 s** from cobra `RunE` entry to last
  byte read, enforced via a `ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)`
  pair and `(*net.Dialer).DialContext(ctx, "unix", path)`. The read side uses
  `conn.SetReadDeadline(time.Now().Add(remaining))` where `remaining` is the
  time left until the parent ctx fires.
- `hush client refresh`: total deadline **90 s** via the same pattern
  (FR-023-24). The longer ceiling covers a Discord-prompted refill that
  hits the refresh-window path under SDD-21.
- `hush supervise`: no upstream-imposed deadline. The wait loop blocks until
  either (a) child exits, (b) SIGTERM/SIGINT, (c) Refresher fires, (d) refresh
  callback fires. Each blocking call takes a ctx that's a child of the
  signal-derived `signal.NotifyContext` root.

**Rationale**: The CLI-side timeouts are spec-locked (FR-023-19, FR-023-24).
Mapping timeout to a connection-failure exit (`ExitErr`) is the spec's
explicit choice (both FR-023-18 and FR-023-23 say "same non-zero exit code
as an unreachable socket").

**Alternatives considered**:
- **Per-call deadlines (dial 500 ms, read 1.5 s for status)** — finer-grained
  but operationally identical: the operator only sees pass/fail. The
  single-deadline form is simpler and matches the spec phrasing
  ("total wait (connect + read) at 2 s"). Confirmed simple form.
- **Configurable timeouts via flags** — out of scope for v0.1.0; the spec
  fixes the values. Defer to future amendment if needed.

---

## R-7 — Refresh coalescing (FR-023-22a)

**Decision**: Use a single-flight pattern inside the orchestrator's
`refreshHandler`. State held in `internal/cli/supervise.go`:

```go
type refreshCoalescer struct {
    mu       sync.Mutex
    inflight *refreshFlight        // nil when no refill in flight
}
type refreshFlight struct {
    done chan struct{}
    err  error                     // terminal result; written before done close
}
```

The handler:
1. Takes `mu`. If `inflight != nil`, captures the pointer and releases `mu`,
   then `<-inflight.done` and returns `inflight.err`.
2. Otherwise constructs a fresh `*refreshFlight`, assigns it to `inflight`,
   releases `mu`, runs the refill+restart, then under `mu` again sets
   `inflight.err`, closes `inflight.done`, and clears `inflight` so the next
   request starts a new flight.

The refill+restart is sequential: `Refiller.Refill(ctx, scopes)` (SDD-21)
→ if non-nil `ErrJTIUnknown`, surface it; → if other error, surface it; →
otherwise `Child.Forward(SIGTERM)` (SDD-20) → `Child.Wait()` → `NewChild`
+ `Start` with refreshed env (SDD-20).

**Rationale**: FR-023-22a's coalescing requirement is a single-flight
problem in the classic sense. A 6-line mutex-guarded struct is clearer than
importing `golang.org/x/sync/singleflight` (which would be a new direct
dependency — Constitution XI). The state is small and the contract is
trivial: every caller observes the same terminal result.

**Alternatives considered**:
- **`golang.org/x/sync/singleflight`** — adds a direct dependency for a
  6-line pattern. Rejected per Constitution XI.
- **Queue every refresh request** — would violate FR-023-22a's "coalesce"
  requirement (queued requests would each trigger a separate refill).
  Rejected.

---

## R-8 — Child-exit wait loop

**Decision**: The supervisor's main loop in `supervise.go` is the canonical
shape (delegating every behavioural rule to `internal/supervise`):

```go
for {
    ec, sig, err := child.Wait()                          // SDD-20
    if errors.Is(err, supervise.ErrChildNotStarted) { break }

    switch {
    case ec == supervise.Exit78:
        _ = store.Transition(ctx, supervise.EventChildExit78Stale)
        // Re-fetch + re-validate via SDD-21 Refill.
        rerr := refiller.Refill(ctx, scopes)
        if errors.Is(rerr, supervise.ErrJTIUnknown) {
            _ = store.Transition(ctx, supervise.EventFetchAuthRequired)
            // Wait for operator approval surfacing — out of this loop's scope; an external
            // refresh callback (or the morning refresh window) re-drives Refill.
            // Try grace cache as a last resort BEFORE awaiting external approval.
            if grace.Enabled() {
                _ = store.Transition(ctx, supervise.EventGraceRestartTriggered)
                // ... restart child with cached secrets, then transition GraceRestartOK
            }
            continue
        }
        // Other refill errors: emit, continue loop (transient).

    case sig != 0:
        // child was killed by signal — log and continue per cfg.Child.RestartOnCleanExit
    case ec == 0:
        _ = store.Transition(ctx, supervise.EventChildExitClean)
        // silent refill → respawn child
    default:
        _ = store.Transition(ctx, supervise.EventChildExitCrash)
        // silent refill → respawn child
    }

    if ctx.Err() != nil { break }                          // SIGTERM-aware

    // Spawn next child with env populated from grace cache / fresh refill.
    child = supervise.NewChild(buildChildConfig(cfg, env))
    if err := child.Start(ctx); err != nil { break }
}
```

The loop never reasons about the state-table: it dispatches Events and lets
SDD-19 reject illegal transitions (which the orchestrator then logs and
surfaces — but never "corrects"). All restart-policy knobs
(`RestartOnCleanExit`, `RestartOnExit78`) are honoured by reading them
from `cfg.Child` and short-circuiting the loop when the operator has
configured a non-restart policy.

**Rationale**: Constitution IV makes the supervisor state machine the
authority; this orchestrator's wait loop is pure dispatch. The grep test
`TestSupervise_OrchestrationDelegatesToInternalSupervise` (SDD-23 §Prompt 4)
asserts that `supervise.go` contains no state-table syntax (e.g., no `if state ==
StateRunning && event ==` patterns) and no exit-code arithmetic.

**Alternatives considered**:
- **Inline state-table reasoning** — duplicates SDD-19 and violates the
  "orchestration only" anti-contract. Rejected.
- **Channel-based event loop with `select`** — would require introducing
  new channel plumbing on top of SDD-19/20/21's existing ctx-driven model.
  Marginal complexity gain. Rejected.

---

## R-9 — Signal handling and shutdown

**Decision**: The supervise subcommand's `RunE` derives its root context via
`signal.NotifyContext(cmd.Context(), syscall.SIGTERM, syscall.SIGINT)`.
On signal:

1. `cancel()` runs implicitly via `defer` — `ctx.Done()` closes.
2. The main loop (R-8) breaks on `ctx.Err() != nil`.
3. `child.Forward(syscall.SIGTERM)` is called explicitly to surface the signal
   to the child immediately (the ctx propagation alone is not guaranteed to
   reach the child's process group on macOS prior to the death-watch kqueue
   firing).
4. `child.Wait()` joins the child's exit; the existing SDD-20 Wait closes the
   ring buffers and joins all drain/forward goroutines.
5. `statusServer.Run(ctx)` returns when ctx fires (already handled by SDD-22's
   `watch` goroutine).
6. `refresher.Run(ctx)` returns `ctx.Err()` (already handled by SDD-21).
7. All goroutines join via the `sync.WaitGroup` owned by the orchestrator
   (one WaitGroup tracks: StatusServer goroutine, Refresher goroutine, no
   others spawned by this chunk).
8. `pidfile.Release()` runs in a defer at the very end of `RunE`,
   AFTER the WaitGroup join. Best-effort error logged to stderr but not
   propagated (the supervisor is exiting anyway).

The signal-context-derived root ctx is the ONLY new context derivation
introduced by this chunk; every downstream package call uses derived
children with their own deadlines where applicable (R-6).

**Rationale**: This is the spec-required SIGTERM/SIGINT behaviour
(FR-023-4, AC-10 Scenario 3). The "explicit `Forward` + Wait" pattern is
necessary because `exec.CommandContext`'s ctx-cancel semantics differ
subtly across platforms; the explicit signal is the contract-locked path.

**Alternatives considered**:
- **Rely solely on `exec.CommandContext` ctx cancellation** — observed
  flakiness on macOS in `child_darwin_test.go`. Use the explicit
  `Forward` path documented by SDD-20. Confirmed.

---

## R-10 — Test approach

**Decision**:
- **Unit tests** (every behaviour contract has a test BEFORE the
  implementation, per Constitution VIII and SDD-23 §Prompt 4):
  - `supervise_test.go`:
    - `TestSupervise_DryRunPrintsCanonicalPayload` — golden-file compare via
      `sign.CanonicalJSON` on a fixture supervisor config.
    - `TestSupervise_DryRunExitsZero` — assert `ExitOK` and no side effect
      (no pidfile, no socket bound — checked via FS state pre/post).
    - `TestSupervise_GraceWindowOverrideTakesPrecedence` — flag-only test;
      patches `cfg.CacheGraceTTL` via the override path; asserts final value.
    - `TestSupervise_GraceWindowExceedsCapRejected` — `--grace-window 5h` →
      `ExitInputErr`.
    - `TestSupervise_NoCacheForcesStrict` — `--no-cache` flips
      `CacheSecretsForRestart` to false even when config says true.
    - `TestSupervise_NoCacheBeatsGraceWindow` — both flags together; assert
      `--no-cache` wins (FR-023-14).
    - `TestSupervise_OrchestrationDelegatesToInternalSupervise` — grep the
      production file's source bytes for forbidden substrings (`case StateRunning`,
      `case Exit78`, raw `78`, raw state-string literals); fails the test if any
      appear outside of dispatcher dispatch sites (one-time exception for
      `supervise.Exit78` constant reference and the EventChildExit78Stale
      dispatch, both whitelisted by exact-byte match).
    - `TestSupervise_DuplicateStartRefused` — acquire pidfile in test setup;
      start supervise → assert wrapped `ErrPidLocked` + `ExitErr` distinguishing
      message (AC-10 Scenario 14).
    - `TestSupervise_SigtermReleasesPidfileAndSocket` — start supervise with a
      no-op child, send SIGTERM, assert pidfile + socket file removed within 5 s.
  - `client_test.go`:
    - `TestClientStatus_TTYHumanSummary` — feed a fake-supervisor socket a
      canned `statusJSON` response; force `IsTerminal=true` via a `tty` seam;
      assert human-readable labels present (supervisor name, state, child PID,
      session expiry, refresh window, healthy/stale scopes) and no JSON
      delimiters.
    - `TestClientStatus_PipeJSON` — same fake-supervisor; force
      `IsTerminal=false`; assert raw byte equality against the supervisor's
      response (no re-marshal).
    - `TestClientStatus_JsonFlagOverridesTTY` — force `IsTerminal=true`,
      pass `--json`; assert JSON path taken.
    - `TestClientStatus_SocketUnreachableExitErr` — point `--socket` at a
      non-existent path; assert `ExitErr` + readable stderr message
      identifying the socket path.
    - `TestClientStatus_TimeoutExitErr` — fake supervisor that accepts conns
      then hangs without writing; assert 2-s deadline trips and maps to
      `ExitErr`.
    - `TestClientStatus_AutoDetectSingleSocket` — temp-dir with exactly one
      `*.sock` file; no `--socket` or `--supervisor` flag; assert it auto-
      selects.
    - `TestClientStatus_AutoDetectZeroSocketsExitInputErr` — empty temp-dir;
      `ExitInputErr` with "no supervisors found" message.
    - `TestClientStatus_AutoDetectMultipleSocketsExitInputErr` — two sockets;
      `ExitInputErr` with "multiple supervisors" message naming both candidates.
    - `TestClientRefresh_AckMapsToExitOK` — fake supervisor writes
      `{"ok":true}\n`; assert `ExitOK`.
    - `TestClientRefresh_ErrorMapsToExitErr` — fake supervisor writes
      `{"ok":false,"error":"vault unreachable"}\n`; assert `ExitErr` and the
      stderr surfaces the supervisor's reason.
    - `TestClientRefresh_SocketUnreachableExitErr` — non-existent path;
      `ExitErr`.
    - `TestClientRefresh_NoFormatFlag` — invoking `hush client refresh --json`
      fails cobra unknown-flag check (defensive; flag is structurally absent).
    - `TestClientRefresh_TimeoutExitErr` — fake supervisor hangs; 90-s deadline
      trips (use a `--timeout-override` test seam that shortcuts to 1 s in
      tests).
- **Integration tests** (`//go:build integration`):
  - `supervise_integration_test.go` — full dry-run with a fake supervisor
    config + DiscordStub (matches the `request_integration_test.go` pattern):
    runs `hush supervise <fixture-config> --dry-run`, captures stdout, asserts
    JSON-parseable with the expected canonical-payload fields, asserts no
    Discord call was issued, asserts no pidfile / socket binding occurred.
- **Sentinel-leak tests** (FR-023-27): existing `test_sentinels_test.go`
  pattern grows three rows for `supervise.go` / `client.go` — assert no
  output contains the dummy-secret marker bytes used by the existing test
  chassis.

**Rationale**: Constitution VIII mandates TDD; SDD-23 §Prompt 4 enumerates
the required tests. The grep-based `OrchestrationDelegatesToInternalSupervise`
test is the structural enforcement of the "no business logic in this file"
anti-contract.

**Alternatives considered**:
- **Behavioural assertions instead of grep for the delegation test** — would
  pass even if the orchestrator implements business logic that happens to
  reach the same outcome. Grep is the structural guard the anti-contract
  requires. Confirmed.

---

## Open items resolved

| Spec section | NEEDS CLARIFICATION? | Resolution |
|---|---|---|
| FR-023-15 socket precedence | Resolved at spec-clarify (Session 2026-05-12 Q1) | R-2 |
| FR-023-20 ack semantics | Resolved at spec-clarify (Q2) | R-1 + R-7 |
| FR-023-22a coalescing | Resolved at spec-clarify (Q3) | R-7 |
| FR-023-19 / FR-023-24 timeouts | Resolved at spec-clarify (Q4) | R-6 |
| FR-023-17a `--json` flag | Resolved at spec-clarify (Q5) | R-5 |

No outstanding NEEDS CLARIFICATION markers remain. Phase 1 design proceeds.
