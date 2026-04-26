# Config Schema

This file defines the exact configuration surface Phase 0 commits to.

If a field is documented here, implementation must support it.
If a field is not documented here, implementation agents should not invent it casually.

---

## Design rules

- server and supervisor config are separate concerns
- secure defaults win over convenience
- invalid config must fail closed at startup
- secret values do not belong in config files on agent machines
- path, mode, and network safety checks are part of config validation, not optional extras

---

## Server config

Primary file:
- `~/.hush/config.toml`

Purpose:
- configure the vault server running on the vault host

Example shape:

```toml
[server]
listen_addr = "100.96.10.4:7743"
path_prefix = "a8k2f9"
state_dir = "/Users/z/.hush"
audit_log = "/Users/z/.hush/audit.jsonl"
discord_owner_id = "123456789012345678"
discord_audit_channel_id = "234567890123456789"
client_registry = "/Users/z/.hush/clients.json"

[discord]
bot_token_keychain_item = "hush-discord"
application_id = "345678901234567890"

[crypto]
argon_time = 4
argon_memory_mb = 256
argon_threads = 4
jwt_default_ttl = "8h"
max_interactive_ttl = "12h"
max_supervisor_ttl = "20h"
default_max_uses = 50
nonce_ttl = "60s"
clock_skew = "30s"

[network]
require_tailscale = true
allowed_cidrs = ["100.64.0.0/10"]
health_bind = "100.96.10.4:7743"

[security]
require_file_mode_checks = true
require_keychain_acl = true
require_ntp_sync = true
max_clock_drift = "60s"
```

### `[server]`

Required fields:

- `listen_addr`
  - type: string
  - example: `100.96.10.4:7743`
  - rules:
    - must include host and port
    - host must resolve to a Tailscale interface address
    - must not be `0.0.0.0`, `127.0.0.1`, empty host, or a public IP

- `path_prefix`
  - type: string
  - example: `a8k2f9`
  - rules:
    - random opaque URL prefix under `/h/<prefix>/...`
    - 6-32 URL-safe characters
    - stable after `hush init`

- `state_dir`
  - type: string
  - example: `~/.hush`
  - rules:
    - must exist or be creatable with mode `0700`

- `audit_log`
  - type: string
  - example: `~/.hush/audit.jsonl`
  - rules:
    - parent dir must be under `state_dir`
    - file mode enforced to `0600`

- `discord_owner_id`
  - type: string
  - rules:
    - required for approval routing
    - must be a Discord snowflake string

- `client_registry`
  - type: string
  - purpose:
    - path to registered client metadata keyed by machine index or fingerprint

Optional fields:

- `discord_audit_channel_id`
  - type: string
  - purpose:
    - if set, mirror signed audit events to a Discord channel

### `[discord]`

Required fields:

- `bot_token_keychain_item`
  - type: string
  - example: `hush-discord`
  - rules:
    - keychain entry name only, not the token itself
    - implementation loads the token from Keychain at runtime

- `application_id`
  - type: string
  - purpose:
    - Discord application/bot identity used for interactions

### `[crypto]`

Required fields:

- `argon_time`
  - type: int
  - default: `4`

- `argon_memory_mb`
  - type: int
  - default: `256`

- `argon_threads`
  - type: int
  - default: `4`

- `jwt_default_ttl`
  - type: duration string
  - default: `8h`

- `max_interactive_ttl`
  - type: duration string
  - default: `12h`

- `max_supervisor_ttl`
  - type: duration string
  - default: `20h`
  - rules:
    - must be greater than `jwt_default_ttl`
    - must not exceed 24h in v0.1.0

- `default_max_uses`
  - type: int
  - default: `50`
  - applies only to interactive sessions

- `nonce_ttl`
  - type: duration string
  - default: `60s`

- `clock_skew`
  - type: duration string
  - default: `30s`

### `[network]`

Required fields:

- `require_tailscale`
  - type: bool
  - default: `true`
  - must remain true in v0.1.0

- `allowed_cidrs`
  - type: string array
  - default: `["100.64.0.0/10"]`
  - purpose:
    - additional startup assertion that bound/request IPs are inside expected Tailscale ranges

Optional fields:

- `health_bind`
  - type: string
  - default:
    - same as `listen_addr`
  - purpose:
    - explicit address for health endpoint if implementation splits listeners later

### `[security]`

Required fields:

- `require_file_mode_checks`
  - type: bool
  - default: `true`

- `require_keychain_acl`
  - type: bool
  - default: `true` on macOS

- `require_ntp_sync`
  - type: bool
  - default: `true`

- `max_clock_drift`
  - type: duration string
  - default: `60s`

---

## Supervisor config

Primary location:
- `~/.hush/supervisors/<name>.toml`

Purpose:
- define how one long-running daemon is approved, refreshed, validated, started, and observed

Example shape:

```toml
name = "openclaw"
reason = "OpenClaw gateway daemon"
server_url = "http://100.96.10.4:7743/h/a8k2f9"
client_machine_index = 2
session_type = "supervisor"
requested_ttl = "20h"
refresh_window = "09:00-10:00"
refresh_nudge_before = "30m"
boot_retry_timeout = "10m"
cache_secrets_for_restart = true
cache_grace_ttl = "60m"
status_socket = "/Users/z/Library/Caches/hush/supervise-openclaw.sock"
pid_file = "/Users/z/Library/Caches/hush/supervise-openclaw.pid"
log_level = "info"

scope = [
  "ANTHROPIC_API_KEY",
  "OPENAI_API_KEY",
  "GITHUB_TOKEN"
]

[child]
command = ["/usr/local/bin/openclaw", "gateway", "start"]
working_dir = "/Users/z/projects/zai"
env_passthrough = ["PATH", "HOME", "SHELL"]
restart_on_clean_exit = true
restart_on_exit_78 = false

[discord]
daemon_label = "OpenClaw"
alert_channel_id = "234567890123456789"

[validators]
ANTHROPIC_API_KEY = "anthropic"
OPENAI_API_KEY = "openai"
GITHUB_TOKEN = "github"

[watchdog]
enabled = true
patterns = [
  "401 Unauthorized",
  "No API key found",
  "invalid x-api-key"
]
max_alerts_per_hour = 6
```

### Root fields

Required:

- `name`
  - type: string
  - rules:
    - unique per host
    - URL/path-safe slug

- `reason`
  - type: string
  - purpose:
    - human-facing explanation shown in Discord approvals

- `server_url`
  - type: string
  - rules:
    - must point to `http://<tailscale-ip>:7743/h/<prefix>` in v0.1.0
    - must not be public internet host

- `client_machine_index`
  - type: int
  - rules:
    - required
    - maps to BIP32 client key path

- `session_type`
  - type: string
  - allowed values: `supervisor`
  - notes:
    - fixed for this config type; documented explicitly to keep intent obvious

- `requested_ttl`
  - type: duration string
  - default: `20h`
  - rules:
    - capped by server `max_supervisor_ttl`

- `refresh_window`
  - type: string
  - example: `09:00-10:00`
  - rules:
    - local time window
    - start must be before end

- `refresh_nudge_before`
  - type: duration string
  - default: `30m`

- `boot_retry_timeout`
  - type: duration string
  - default: `10m`

- `cache_secrets_for_restart`
  - type: bool
  - default: `false`

- `cache_grace_ttl`
  - type: duration string
  - default: `60m`
  - rules:
    - valid only when `cache_secrets_for_restart = true`
    - cap: `4h`

- `status_socket`
  - type: string
  - rules:
    - file mode `0600`
    - parent dir `0700`

- `pid_file`
  - type: string
  - purpose:
    - split-brain guard with flock

Optional:

- `log_level`
  - type: string
  - allowed: `debug`, `info`, `warn`, `error`

- `scope`
  - type: string array
  - required in practice
  - rules:
    - non-empty
    - each item is an exact secret name approved for this daemon

### `[child]`

Required fields:

- `command`
  - type: string array
  - example: `["/usr/local/bin/openclaw", "gateway", "start"]`
  - rules:
    - first element absolute path preferred
    - no shell parsing implied

- `working_dir`
  - type: string

- `env_passthrough`
  - type: string array
  - purpose:
    - non-secret env inherited by the child

- `restart_on_clean_exit`
  - type: bool
  - default: `true`

- `restart_on_exit_78`
  - type: bool
  - default: `false`
  - rules:
    - should remain false; exit 78 means await approval / refresh

### `[discord]`

Optional but recommended:

- `daemon_label`
  - type: string
  - purpose:
    - nicer label in DMs and alerts

- `alert_channel_id`
  - type: string
  - purpose:
    - send non-DM operational alerts to a dedicated channel if desired

### `[validators]`

Type:
- map of secret name → validator type

Allowed validator values in v0.1.0:
- `anthropic`
- `anthropic-oauth`
- `openai`
- `google-ai`
- `github`

Rules:
- every listed validator runs on the supervisor host
- unknown validator names are startup errors
- validators must only perform read-only, cheapest-possible auth checks

### `[watchdog]`

Optional fields:

- `enabled`
  - type: bool
  - default: `true`

- `patterns`
  - type: string array
  - purpose:
    - known auth-failure log fragments

- `max_alerts_per_hour`
  - type: int
  - default: `6`

Rules:
- watchdog matches are alert-only
- watchdog patterns must not drive restart policy directly

---

## Client status output schema

`hush client status --json` should align to this shape:

```json
{
  "supervisor": "openclaw",
  "state": "running",
  "session_expires_at": "2026-04-15T06:12:00-07:00",
  "refresh_window_next": "2026-04-15T09:00:00-07:00",
  "scope_healthy": ["ANTHROPIC_API_KEY"],
  "scope_stale": [],
  "last_auth_failure": null,
  "child_pid": 51234,
  "child_uptime": "8h12m",
  "discord_connected": true
}
```

Required fields:
- `supervisor`
- `state`
- `session_expires_at`
- `refresh_window_next`
- `scope_healthy`
- `scope_stale`
- `last_auth_failure`
- `child_pid`
- `child_uptime`
- `discord_connected`

---

## Validation rules summary

Startup must fail if any of these are true:

- listen address is not Tailscale-scoped
- path prefix missing or malformed
- state dir/file modes are too loose
- Discord owner ID missing
- keychain item names missing where required
- NTP is unsynced or drift exceeds max
- supervisor command is empty
- supervisor scope is empty
- unknown validator is declared
- refresh window is malformed
- grace cache TTL exceeds v0.1.0 cap
- status socket or pid file path is unsafe/unwritable

---

## Phase 0 completion check

This file is sufficient when an implementation agent can answer these without guessing:

- what config files exist?
- what exact fields are required?
- what defaults are expected?
- what validations are startup-fatal?
- what is server-only vs supervisor-only config?

If those answers still require invention, Phase 0 is not done.
