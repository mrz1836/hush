# Contract — Harness API surface (SDD-25)

This document is the *contract* a scenario consumer depends on. The Go signatures listed below are illustrative and **MAY evolve** as new scenarios surface needs (per the SDD-25 PACKAGE-MAP entry: harness types are listed without freezing signatures). What is **locked** is the behavioural contract: what each helper guarantees, what it never does, and what the consumer can rely on.

## Package

`github.com/mrz1836/hush/tests/integration/harness`

- Build-tagged `//go:build integration`. Default `go test ./...` compiles zero files.
- Importers: only test files under `tests/integration/`. Production code MUST NOT import this package (enforced by golangci-lint `depguard` rule added in tasks phase).

## Builder contract

Every harness builder satisfies these four properties:

1. **`t.Cleanup`-registered teardown.** The builder registers exactly one cleanup that releases every resource the builder allocates (file descriptors, goroutines, listening sockets, temp files, pidfiles). No resource outlives the test function.
2. **No-shared-state isolation.** Two scenarios constructing the same builder produce two fully isolated environments. State directories, sockets, pidfiles, and ports are distinct.
3. **Fail-loud on misuse.** Any builder called with a nil dependency or a context already cancelled returns an error (or `t.Fatal`s) immediately — never silently produces a degraded harness.
4. **No external-network egress.** Any code path that would resolve a non-loopback hostname returns an error (FR-025-13). The harness’s `http.Client` is wired with a `RoundTripper` that rejects any host outside `127.0.0.1`/`::1`/the registered httptest endpoints.

## Exported builders (illustrative signatures)

```go
package harness

// NewVault builds the temp-dir vault, registers the cleanup, and pre-loads
// secrets. Sentinel values MUST be supplied via testutil.SentinelSecret(N).
func NewVault(t *testing.T, secrets map[string]string) *TestVault

// NewServer brings up the real internal/server in-process via httptest, wires
// the DiscordStub adapter, registers cleanup. Returns once the server is ready
// to accept /claim. Per-validator-upstream httptest servers are constructed
// lazily on the first MockValidator call.
func NewServer(t *testing.T, opts ServerOpts) *TestServer

// NewDiscord constructs a DiscordStub wrapped in the connectivity/rate-limit
// state machine, registers cleanup. Approver adapter is built lazily when the
// caller wires it into TestServer.
func NewDiscord(t *testing.T) *TestDiscord

// NewSupervisor composes the SDD-19..22 primitives. The Clock seam is a real
// supervise.Clock implementation backed by a FakeClock the caller drives via
// Clock().Advance.
func NewSupervisor(t *testing.T, opts SupervisorOpts) *TestSupervisor

// NewChild builds a programmable child that re-invokes the test binary in
// integration-child mode. The exit code, lifetime, and stderr pattern are
// scriptable.
func NewChild(t *testing.T, opts ChildOpts) *TestChild

// NewLogCapture installs a slog handler chain capturing every record to a
// sync-safe buffer. Returns a *slog.Logger to hand to consumers and a Bytes()
// accessor for the sentinel-absence assertion.
func NewLogCapture(t *testing.T) *LogCapture
```

## Exported assertion helpers (illustrative signatures)

```go
// AssertSentinelAbsent runs testutil.AssertSentinelAbsent over every supplied
// byte stream. The canonical caller passes 6 streams per the data-model.md §4.6
// coverage list.
func AssertSentinelAbsent(t *testing.T, sentinel string, streams ...[]byte)

// AssertAuditSubsequence walks recorded against documented; passes if every
// documented event appears in recorded in the documented order (intervening
// events are tolerated per spec Clarification 1).
func AssertAuditSubsequence(t *testing.T, recorded []audit.Event, documented []string)

// AssertAuditChainContinuity calls audit.Verify on the on-disk path. Wraps
// the underlying error with the scenario name on failure.
func AssertAuditChainContinuity(t *testing.T, auditPath string, verifyKey *ecdsa.PublicKey)

// AssertStatusShape unmarshals the status-socket bytes into a strictly typed
// DTO matching SPEC §FR-12 + SDD-22's locked statusJSON shape. Asserts every
// FR-12 field is present (no omitempty tolerated per spec Assumptions).
func AssertStatusShape(t *testing.T, raw []byte) StatusDoc
```

## Behavioural guarantees (locked)

| Guarantee | Source | Enforcement |
|-----------|--------|-------------|
| Every builder calls `t.Cleanup` with a function that returns only after all goroutines exit. | Constitution IX | `runtime.NumGoroutine` snapshot in `TestSupervisor.AssertNoGoroutineLeak` |
| Every builder registers its temp-dir cleanup under `t.TempDir()`. | spec FR-025-22 | rely on `testing.T.TempDir` (Go stdlib already cleans up) |
| No builder calls `time.Sleep` to drive a documented transition. | spec FR-025-16 | manual code-review during implement phase; static check via grep test in scenarios_test.go |
| No builder makes a TCP/UDP/HTTPS connection to a host outside `127.0.0.1`/`::1`/registered httptest endpoints. | spec FR-025-13 | `http.Client.Transport` is a `RoundTripper` that returns an error on any other host |
| Every `slog` record the harness emits flows through `LogCapture.Logger()`. | spec FR-025-26 | `TestServer.NewServer` and `TestSupervisor.NewSupervisor` use the `LogCapture` logger as their `Logger` Dep |
| No builder reads or writes `~/.hush/` or any path outside `t.TempDir()`. | spec FR-025-22 | code review + leak test (no path-traversal in harness paths) |
| The `DiscordStub` adapter never returns `Decision{Approved: true}` except on a scripted Approve. | Constitution II | adapter mirrors `internal/server/claim_handler_integration_test.go::stubAsApprover` pattern |

## Anti-API (NOT exported, locked off)

- `harness.Reset()` / `harness.NewGlobal*()` — no global state in the harness; every builder is per-test.
- `harness.Sleep(d)` / `harness.MustSleep(d)` — `time.Sleep` is forbidden for documented transitions; bounded `runtime.Gosched` polls live inside private helpers, not exported.
- `harness.SkipScenario(t, reason)` — every scenario is mandatory (FR-025-3); no skip path.
- `harness.SuppressSentinelLeak(t)` — there is no "ignore the redaction failure for this scenario" path.
- Any builder that returns a `(*Supervisor, error)` tuple — every builder is `t.Fatal`-on-error so scenario bodies stay flat.
- Any exported global `var Logger = …` — no package-level mutable globals (Constitution IX).
