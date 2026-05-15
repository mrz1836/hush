# Contract: `hush client status` and `hush client refresh`

**Branch**: `023-cli-supervise-and-client` | **Date**: 2026-05-12
**Spec section**: FR-023-15 … FR-023-24
**Source files (planned)**: `internal/cli/client.go`, `internal/cli/client_test.go`

---

## 1. Parent command

`hush client` is a parent cobra command with no own RunE — it exists to
namespace `status` and `refresh`. Invoking `hush client` alone prints usage
and exits `ExitOK` (cobra default).

---

## 2. `hush client status`

### 2.1 Synopsis

```text
hush client status [--socket <path>] [--supervisor <name>] [--json]
                   [-v|-q] [--no-color] [-c <global-config>]
```

### 2.2 Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--socket` | `string` | `""` | Absolute path to the supervisor's status socket. When supplied, wins over all other resolution. |
| `--supervisor` | `string` | `""` | Supervisor name (config `name` field). When supplied, derives the socket path via `supervise.SocketPathForSupervisor(name)`. |
| `--json` | `bool` | `false` | Force JSON output regardless of stdout TTY-ness (FR-023-17a). |

### 2.3 Socket-path resolution precedence

Per FR-023-15 (clarified 2026-05-12 Q1):

1. If `--socket <abs-path>` is non-empty → use verbatim.
2. else if `--supervisor NAME` is non-empty →
   `supervise.SocketPathForSupervisor(name)`.
3. else → `supervise.EnumerateSupervisorSockets()`:
   - Exactly 1 socket found → use it.
   - 0 sockets found → `errSocketAmbiguous` wrapping the message
     `no supervisor sockets found in <runtime-dir>` → `ExitInputErr`.
   - > 1 sockets found → `errSocketAmbiguous` wrapping the message
     `multiple supervisor sockets found in <runtime-dir>: <comma-separated names>` →
     `ExitInputErr`.

### 2.4 Round-trip protocol

1. `ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)` (FR-023-19).
2. `(&net.Dialer{}).DialContext(ctx, "unix", socketPath)`. Failure →
   `errSocketUnreachable` → `ExitErr`.
3. `conn.SetDeadline(time.Now().Add(remaining))` where `remaining` is the
   time left until ctx fires.
4. `conn.Write([]byte("status\n"))`.
5. Read until EOF or deadline.
6. Close conn.

### 2.5 Output

| stdout target | Output |
|---|---|
| TTY (`term.IsTerminal(stdout.Fd())==true`) and `--json` not supplied | Human-readable summary. |
| pipe/redirect (TTY false) or `--json` supplied | Raw bytes from the socket, verbatim, terminated by exactly one `\n`. |

Human-summary format (locked; the formatting test asserts label presence):

```text
Supervisor: <name>
State:      <state>
Child PID:  <pid or "no child">
Child up:   <duration>
Session expires: <RFC3339 timestamp>
Next refresh:    <RFC3339 timestamp>
Healthy scopes:  <comma-separated list, or "(none)">
Stale scopes:    <comma-separated list, or "(none)">
Discord:    <connected | disconnected>
Last auth fail:  <RFC3339 timestamp or "never">
```

No ANSI colour by default (operator can pipe through `--no-color` for
forced plain output; the human format is plain by default — no colours
introduced here).

### 2.6 Exit codes

| Code | Condition |
|---|---|
| 0 | Status retrieved and rendered successfully. |
| 1 | Socket unreachable, dial timeout, read timeout, JSON parse failure on TTY path. |
| 2 | Invalid `--socket` value (not absolute), invalid `--supervisor` value (bad slug), socket-ambiguous error. |
| 5 | Socket file mode rejection (when extending readiness to check perms — defer to actual implementation). |

### 2.7 Anti-contracts

- MUST NOT contain `runtime.GOOS` or per-OS branches.
- MUST NOT contain `net.Dial("tcp",...)`.
- MUST NOT log the response bytes (Constitution X — even though status JSON
  doesn't carry secrets in v0.1.0, the file MUST NOT establish a pattern of
  logging socket responses).
- MUST NOT re-marshal the JSON payload on the pipe/`--json` path (R-5
  decision: emit verbatim bytes).

---

## 3. `hush client refresh`

### 3.1 Synopsis

```text
hush client refresh [--socket <path>] [--supervisor <name>]
                    [-v|-q] [--no-color] [-c <global-config>]
```

### 3.2 Flags

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--socket` | `string` | `""` | Absolute path to the supervisor's status socket. |
| `--supervisor` | `string` | `""` | Supervisor name. |

No `--json` flag (FR-023-17a explicit). Cobra rejects unknown flags.

### 3.3 Socket-path resolution

Identical precedence rule to `hush client status` §2.3.

### 3.4 Round-trip protocol

1. `ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)` (FR-023-24).
2. Dial — failure → `errSocketUnreachable` → `ExitErr`.
3. `conn.Write([]byte("refresh\n"))`.
4. Read until EOF or deadline. Expected payload: one JSON line + `\n`.
5. Unmarshal into `refreshAck{OK bool; Error string}`.
6. Close conn.

### 3.5 Acknowledgement mapping

| Response | Mapping |
|---|---|
| `{"ok":true}` | `ExitOK`; stdout empty; stderr empty. |
| `{"ok":false,"error":"<msg>"}` | `errSupervisorRefused` wrapping `<msg>` → `ExitErr`; stderr `hush: client refresh: supervisor refused: <msg>`. |
| Read deadline exceeded | `errSocketUnreachable` → `ExitErr`; stderr `hush: client refresh: timed out after 90s`. |
| Conn closed without writing ack | `errSocketUnreachable` → `ExitErr`. |
| Malformed JSON in response | `errSocketUnreachable` (treated as "supervisor produced unexpected bytes") → `ExitErr`. |

The supervisor's coalescing (FR-023-22a) is invisible to the client — when
a second `hush client refresh` is in flight against the same supervisor,
both clients receive the same terminal ack from the single in-flight refill.

### 3.6 Exit codes

| Code | Condition |
|---|---|
| 0 | Refill completed successfully on the supervisor side. |
| 1 | Socket unreachable, dial timeout, read timeout, supervisor returned `ok:false`. |
| 2 | Invalid `--socket` or `--supervisor` value, socket-ambiguous. |

### 3.7 Anti-contracts

Same as `hush client status` §2.7. Additionally:

- MUST NOT retry on timeout (the 90-s ceiling is the budget, not a per-attempt
  timeout — operator can re-invoke if needed).
- MUST NOT print a partial response to stdout on failure (stdout is empty on
  every non-success path).

---

## 4. Test surface (mandated)

### 4.1 Status tests

- `TestClientStatus_TTYHumanSummary`
- `TestClientStatus_PipeJSON`
- `TestClientStatus_JsonFlagOverridesTTY`
- `TestClientStatus_SocketUnreachableExitErr`
- `TestClientStatus_TimeoutExitErr`
- `TestClientStatus_AutoDetectSingleSocket`
- `TestClientStatus_AutoDetectZeroSocketsExitInputErr`
- `TestClientStatus_AutoDetectMultipleSocketsExitInputErr`
- `TestClientStatus_InvalidSocketPathExitInputErr`
- `TestClientStatus_NoSecretInOutput` (sentinel-leak)

### 4.2 Refresh tests

- `TestClientRefresh_AckMapsToExitOK`
- `TestClientRefresh_ErrorMapsToExitErr`
- `TestClientRefresh_SocketUnreachableExitErr`
- `TestClientRefresh_NoFormatFlag` (cobra unknown-flag check)
- `TestClientRefresh_TimeoutExitErr` (timeout shortened via test seam)
- `TestClientRefresh_MalformedJsonResponseExitErr`
- `TestClientRefresh_NoSecretInOutput` (sentinel-leak)

Coverage target: ≥ 85 % on `client.go` (SC-023-10, combined with `supervise.go`).
