# Quickstart — `hush supervise` + `hush client status` + `hush client refresh`

**Branch**: `023-cli-supervise-and-client` | **Date**: 2026-05-12

Walk-through for an operator wiring a daemon under hush for the first time
using SDD-23's three subcommands. Assumes the vault host is already running
(`hush init server` + `hush serve`) and the agent host is enrolled
(`hush init client --machine-index N`).

---

## 1. Author a supervisor config

Create `~/.hush/supervisors/example-daemon.toml` per
`docs/CONFIG-SCHEMA.md §"Supervisor config"`:

```toml
name = "example-daemon"
reason = "Example long-running daemon"
server_url = "http://100.96.10.4:7743/h/a8k2f9"
client_machine_index = 2
session_type = "supervisor"
requested_ttl = "20h"
refresh_window = "09:00-10:00"
cache_secrets_for_restart = true
cache_grace_ttl = "60m"
status_socket = "~/Library/Caches/hush/supervise-example-daemon.sock"
pid_file = "~/Library/Caches/hush/supervise-example-daemon.pid"

scope = ["ANTHROPIC_API_KEY", "OPENAI_API_KEY"]

[child]
command = ["/usr/local/bin/your-daemon-binary", "start"]
working_dir = "~"
env_passthrough = ["PATH", "HOME"]

[validators]
ANTHROPIC_API_KEY = "anthropic"
OPENAI_API_KEY = "openai"
```

---

## 2. Preview the claim payload (no Discord prompt)

Verify the wiring without burning a Discord approval:

```bash
hush supervise ~/.hush/supervisors/example-daemon.toml --dry-run
```

Expected stdout (one canonical JSON line + newline):

```json
{"machine_index":2,"name":"example-daemon","reason":"Example long-running daemon","requested_ttl":"20h0m0s","scope":["ANTHROPIC_API_KEY","OPENAI_API_KEY"],"session_type":"supervisor"}
```

Expected exit code: `0`. No Discord call, no vault contact, no PID file,
no socket binding.

Pipe through `jq` for a sanity check:

```bash
hush supervise ~/.hush/supervisors/example-daemon.toml --dry-run | jq
```

---

## 3. Start the supervisor for real

Foreground (the OS service manager — launchd / systemd — is what
backgrounds it in production):

```bash
hush supervise ~/.hush/supervisors/example-daemon.toml
```

The supervisor:

1. Acquires the PID-file flock at the path specified by `pid_file`.
2. Binds the status socket at `status_socket` with mode `0600`.
3. Starts a StatusServer goroutine and a Refresher goroutine.
4. Performs the initial vault claim (Discord approval required — phone
   notification fires on the configured approver).
5. Fetches and validates every scope.
6. Spawns the child with secrets injected as environment variables.

The supervisor stays in the foreground and tails the child's stdout / stderr
through its bounded ring buffer (see SDD-20). `Ctrl-C` (SIGINT) or
`launchctl stop` / `systemctl stop` (SIGTERM) triggers graceful shutdown:
the child is signalled, the supervisor waits for it to exit, releases the
PID file, and removes the socket.

---

## 4. Query daemon freshness

In a separate terminal, with the supervisor still running:

```bash
hush client status --supervisor example-daemon
```

Or by explicit path:

```bash
hush client status --socket ~/Library/Caches/hush/supervise-example-daemon.sock
```

On a TTY, you see a human summary:

```text
Supervisor: example-daemon
State:      running
Child PID:  51234
Child up:   8h12m0s
Session expires: 2026-04-15T06:12:00-07:00
Next refresh:    2026-04-15T09:00:00-07:00
Healthy scopes:  ANTHROPIC_API_KEY, OPENAI_API_KEY
Stale scopes:    (none)
Discord:    connected
Last auth fail:  never
```

When piped or with `--json`, you get the raw status JSON document:

```bash
hush client status --supervisor example-daemon --json | jq
```

---

## 5. Pre-task gate (the agent-visible freshness API)

The canonical script pattern documented in `docs/DAEMONS.md §7`:

```bash
if hush client status --supervisor example-daemon --json \
   | jq -e '.scope_stale | length == 0' >/dev/null; then
  ./run-long-task.sh
else
  echo "ERROR: required scopes are stale; refusing to run" >&2
  exit 1
fi
```

This closes the "the agent has no way to know its credentials are bad" gap.

---

## 6. Force an immediate refresh

After `hush secret rotate ANTHROPIC_API_KEY` on the vault host, the running
supervisor's cached secret is stale until either the next refresh window
fires or the operator forces it:

```bash
hush client refresh --supervisor example-daemon
```

The command:

1. Dials the status socket.
2. Sends the `refresh\n` verb.
3. Waits up to 90 s for the supervisor's terminal ack (the refresh path
   includes Refill, validator runs, and child restart — see FR-023-20).
4. Maps the ack:
   - `{"ok":true}\n` → exit `0`.
   - `{"ok":false,"error":"<msg>"}\n` → exit `1` with `<msg>` on stderr.

If two operators (or scripts) invoke `hush client refresh` against the same
supervisor concurrently, both receive the same terminal ack — the
supervisor coalesces (FR-023-22a). Neither caller sees a "busy" exit.

---

## 7. Strict mode for one run

To force `cache_secrets_for_restart = false` without editing the config:

```bash
hush supervise ~/.hush/supervisors/example-daemon.toml --no-cache
```

To run with a tighter grace window (e.g. 30 minutes instead of the
configured 60):

```bash
hush supervise ~/.hush/supervisors/example-daemon.toml --grace-window 30m
```

Both flags together: `--no-cache` wins, the `--grace-window` value is
silently ignored (FR-023-14):

```bash
hush supervise ~/.hush/supervisors/example-daemon.toml --grace-window 30m --no-cache
# Equivalent to --no-cache alone.
```

---

## 8. Duplicate-supervisor protection

Attempting to start a second `hush supervise` against the same config on the
same host produces:

```text
hush: supervise: another supervisor is already running for this configuration (pidfile=/Users/op/Library/Caches/hush/supervise-example-daemon.pid)
```

Exit code `1`. The PID file's flock is the authoritative signal — the
textual PID inside the file is advisory only.

---

## 9. Verifying success criteria

Quick scripts to confirm SDD-23 is wired correctly:

```bash
# SC-023-2: dry-run is fast and JSON-parseable.
time hush supervise ~/.hush/supervisors/example-daemon.toml --dry-run | jq -e '.name' >/dev/null
# Expected: real <0.5s, exit 0.

# SC-023-3: status round-trip <1s.
time hush client status --supervisor example-daemon --json >/dev/null
# Expected: real <1s, exit 0.

# SC-023-7: duplicate refused <1s.
hush supervise ~/.hush/supervisors/example-daemon.toml &
sleep 0.5
time hush supervise ~/.hush/supervisors/example-daemon.toml
# Expected: exit 1 within 1s.
kill %1
wait

# SC-023-8: SIGTERM clean shutdown.
hush supervise ~/.hush/supervisors/example-daemon.toml &
SUPER_PID=$!
sleep 2
kill $SUPER_PID
wait $SUPER_PID
test ! -e ~/Library/Caches/hush/supervise-example-daemon.pid && echo OK
test ! -e ~/Library/Caches/hush/supervise-example-daemon.sock && echo OK
```

---

## 10. Common failure modes and recovery

| Symptom | Likely cause | Fix |
|---|---|---|
| `config invalid: scope is empty` | `scope = []` in TOML | Add at least one scope name. |
| `--grace-window must be >0 and ≤4h` | Out-of-range flag value | Use `30m`, `1h`, `4h` etc. |
| `another supervisor is already running` | Live process holds the pidfile | Stop the running supervisor (`launchctl stop` / `kill <pid>`), then retry. Stale pidfiles (process dead) clear automatically. |
| `socket parent directory ... mode 755 laxer than 0700` | Cache directory has wrong perms | `chmod 700 ~/Library/Caches/hush` (or platform equivalent). |
| `no supervisor sockets found` from `client status` | No `hush supervise` running, or socket path mismatch | Start the supervisor, or pass `--socket` / `--supervisor` explicitly. |
| `multiple supervisor sockets found` | Multiple supervisors on this host | Disambiguate with `--supervisor NAME` or `--socket <path>`. |
| `timed out after 90s` from `client refresh` | Supervisor stuck on Discord approval | Inspect the Discord channel; approve or deny. Re-invoke `client refresh` afterward if needed. |
