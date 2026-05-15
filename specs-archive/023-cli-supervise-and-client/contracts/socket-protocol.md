# Contract: Status socket verb protocol (extension to SDD-22)

**Branch**: `023-cli-supervise-and-client` | **Date**: 2026-05-12
**Spec section**: FR-023-20 / FR-023-22a (extends FR-12 + SDD-22)
**Source files (planned)**: minimal modifications to
`internal/supervise/socket.go`, `internal/supervise/socket_darwin.go`,
`internal/supervise/socket_linux.go`, `internal/supervise/socket_test.go`.

This contract documents the verb-dispatch extension to SDD-22's status socket.
The extension is the only behavioural change `internal/supervise` undergoes
in this chunk; it is justified in `plan.md §Complexity Tracking` and detailed
in `research.md §R-1`.

---

## 1. Transport invariants (unchanged from SDD-22)

- Unix domain socket. Mode `0600`. Parent directory mode `0700`.
- One TCP-free, HTTP-free, auth-free dispatch — filesystem permissions ARE
  the authentication (Constitution V).
- Per-connection: one read of one line, then one response, then close.
- No persistent client connections; no streaming responses.

---

## 2. Verb dispatch (NEW in SDD-23)

After accepting a connection and reading the first line via
`bufio.NewReader(conn).ReadString('\n')`, the per-connection handler trims
the trailing `\n` (and any leading/trailing whitespace) and matches the
result against the recognised verb set:

| Verb (after trim) | Dispatch |
|---|---|
| `status` (or empty, or any unrecognised non-empty value) | Existing SDD-22 `renderStatus(snapshot)` path. |
| `refresh` | New refresh path described in §3 below. |

The "unrecognised → status" default preserves SDD-22 §2.5's "request payload
is advisory in v0.1.0" backward-compatibility note. Existing clients that
send no payload (or non-verb payload) continue to receive a status document.

---

## 3. Refresh path

### 3.1 Handler hook

A new package-private method on `*StatusServer`:

```go
// attachRefreshHandler wires the orchestrator's refresh callback. Mirrors
// (*StatusServer).attach(StatusInputs) and (*Refiller).attach. Called once
// post-construction from internal/cli/supervise.go. Until called, the
// refresh path returns a stable "refresh not wired" error instead of
// panicking.
func (s *StatusServer) attachRefreshHandler(handler func(ctx context.Context) error)
```

When unwired (handler == nil at request time), the refresh handler writes
`{"ok":false,"error":"refresh handler not wired"}\n` and returns. This is
defensive only — the orchestrator wires the handler before starting `Run`.

### 3.2 Response shape

A single JSON line + `\n`:

| Outcome | Response |
|---|---|
| `handler(ctx)` returned `nil` | `{"ok":true}\n` |
| `handler(ctx)` returned non-nil | `{"ok":false,"error":"<msg>"}\n` where `<msg>` is the error's `.Error()` output with all newlines replaced by spaces (so the entire response is one line). |

### 3.3 ctx propagation

The handler is invoked with the per-connection context derived from the
StatusServer's `Run` ctx (via the existing accept-loop ctx chain). When the
StatusServer is cancelled (supervisor SIGTERM), the handler's ctx fires,
and the orchestrator's refresh callback returns `ctx.Err()` immediately.

### 3.4 Concurrency

The handler MAY be invoked from multiple per-connection goroutines
concurrently. The orchestrator's `refreshCoalescer` (data-model.md §2.4)
enforces single-flight on the supervisor side: concurrent invocations
share the same in-flight refill result (FR-023-22a).

### 3.5 No state-machine reasoning here

`socket.go`'s verb dispatch does NOT consult the supervisor's state or
the snapshot — it ONLY routes by verb. All refill / state-transition /
child-restart logic lives in the orchestrator's `refreshCoalescer.perform`
closure (data-model.md §2.4), which calls `Refiller.Refill` (SDD-21) and
`Child.Forward`/`Wait`/`Start` (SDD-20). The socket layer remains
state-table-ignorant.

---

## 4. Path-derivation helpers (NEW in SDD-23)

Added to `socket_darwin.go` + `socket_linux.go`. These are production
functions (NOT test-fixture-only, unlike the existing `defaultRuntimeDir`).

### 4.1 `SocketPathForSupervisor(name string) string`

Returns the absolute path the supervisor would bind for a given supervisor
name. Pure path-derivation; no syscalls. Per-platform scheme:

- **Darwin**: `<UserCacheDir>/hush/supervise-<name>.sock`
- **Linux**: `<XDG_RUNTIME_DIR>/hush-supervise-<name>.sock`
  (fallback to `<TempDir>/hush-supervise-<name>.sock` if XDG is unset)

`name` is validated against `^[a-zA-Z0-9_-]+$` (path-safe slug per
CONFIG-SCHEMA.md). Invalid name → panic (programmer error; the CLI layer
validates the cobra flag value before this call).

### 4.2 `EnumerateSupervisorSockets() ([]string, error)`

Returns the sorted, absolute paths of every file in the platform runtime
directory matching the supervise-socket naming scheme. Implementation:

- **Darwin**: scan `<UserCacheDir>/hush/`, match `supervise-*.sock`.
- **Linux**: scan `$XDG_RUNTIME_DIR/` (or `<TempDir>` fallback), match
  `hush-supervise-*.sock`.

The function does NOT verify mode, ownership, or liveness — it returns
every name matching the pattern, and the caller (`internal/cli/client.go`)
attempts to connect, with failures surfaced as `errSocketUnreachable`.

When the runtime directory does not exist, returns `([]string{}, nil)` —
no sockets found is a normal state, not an error.

---

## 5. Anti-contracts (extension MUST NOT)

- MUST NOT add HTTP semantics (no `/status` or `/refresh` URL routing — the
  dispatch is a one-byte-line verb match, not URL parsing).
- MUST NOT add TLS (Constitution V — Tailscale is the perimeter; local
  Unix sockets carry no transport encryption).
- MUST NOT add a bearer-token, HMAC, or signed-cookie check (FS perms ARE
  the auth — Constitution V).
- MUST NOT add a streaming response (one connection = one read + one write +
  close).
- MUST NOT panic on unwired refreshHandler — return a stable error response
  instead (so the operator's CLI receives a readable diagnostic).
- MUST NOT log the refresh response body (Constitution X — even though the
  body is non-secret in v0.1.0, this establishes the safe pattern).
- MUST NOT add backwards-incompatible behaviour to the `status` path —
  existing clients that connect with no payload, or any non-verb payload,
  continue to receive a status document byte-for-byte identical to SDD-22.

---

## 6. Test surface (mandated)

Added to `internal/supervise/socket_test.go`:

- `TestSocket_VerbStatusReturnsStatusDocument` — explicit `status\n` request.
- `TestSocket_VerbRefreshInvokesHandler` — register a refresh handler that
  returns nil; assert response `{"ok":true}\n`.
- `TestSocket_VerbRefreshErrorIsSerialised` — handler returns
  `errors.New("vault unreachable")`; assert
  `{"ok":false,"error":"vault unreachable"}\n`.
- `TestSocket_VerbRefreshErrorMultilineSerialisedAsOneLine` — handler
  returns an error with `\n` in the message; assert the response is still
  one line (newlines replaced by spaces).
- `TestSocket_VerbRefreshHandlerUnwiredReturnsStableError` — no handler
  attached; assert `{"ok":false,"error":"refresh handler not wired"}\n`.
- `TestSocket_VerbUnrecognisedFallsBackToStatus` — send `garbage\n`,
  expect the status document (backward-compat).
- `TestSocket_VerbStatusEmptyPayloadReturnsStatusDocument` — send only
  `\n` (no verb); expect the status document.
- `TestSocket_RefreshHandlerCtxFiresOnServerCancel` — start server, queue a
  refresh that blocks on its ctx, cancel the server's `Run` ctx, assert the
  handler's ctx fires within 100 ms.
- `TestSocket_AttachRefreshHandlerCalledTwicePanicsOrLastWinsLockedBehaviour` —
  documents whether `attachRefreshHandler` is single-shot or last-wins.
  Recommended: single-shot panic on second call, matching SDD-22's
  one-shot `Run` semantics. (Implementation may revisit; the test fixes
  the contract either way.)

Added to `internal/supervise/socket_darwin_test.go` and `_linux_test.go`:

- `TestSocketPathForSupervisor_DerivesPlatformPath` — assert the documented
  per-platform path scheme is produced for several `name` inputs.
- `TestEnumerateSupervisorSockets_ListsMatchingFiles` — populate a temp
  runtime directory with one matching + one non-matching file; assert
  exactly the matching file is returned.
- `TestEnumerateSupervisorSockets_EmptyDirReturnsEmptySlice` — runtime
  directory exists but contains no `.sock` files.
- `TestEnumerateSupervisorSockets_MissingDirReturnsEmptySlice` — runtime
  directory does not exist; assert `([]string{}, nil)`.

Coverage on `socket.go` MUST remain ≥ 95 % (existing SDD-22 bar; the
extension does not relax it).
