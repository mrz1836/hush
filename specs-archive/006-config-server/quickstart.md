# Quickstart: `internal/config` (Server)

**Audience**: SDD-10 (server skeleton), SDD-15 (init wizard), and any future agent reading a `~/.hush/config.toml`.
**Last updated**: 2026-04-28 (Phase 1 of SDD-06)

This is the operational cheat-sheet for loading the server-side config. The contract and rationale live in [api.md](./contracts/api.md), [data-model.md](./data-model.md), and [research.md](./research.md); this file shows you how to wire the loader into a startup path and how to react to each documented failure.

---

## 1. Load the config

```go
import (
    "context"
    "fmt"

    "github.com/mrz1836/hush/internal/config"
)

func loadServerConfig(ctx context.Context, path string) (*config.Server, error) {
    s, err := config.LoadServer(ctx, path)
    if err != nil {
        return nil, fmt.Errorf("load server config: %w", err)
    }
    return s, nil
}
```

Every absent optional field is populated from the [defaults catalogue](./contracts/api.md#default-constants); every duration is parsed; every path is `~`-expanded and absolute. The returned `*Server` is read-only — pass it by pointer; do not mutate it.

---

## 2. React to each documented failure

Every error from `LoadServer` is matchable via `errors.Is`. The recommended startup path checks for the operator-actionable categories first (so the operator gets the most useful message) and falls back to a generic `unknown error` print.

```go
s, err := config.LoadServer(ctx, path)
if err == nil {
    return s, nil
}

switch {
case errors.Is(err, config.ErrUnknownField):
    fmt.Fprintln(os.Stderr, "config has an unknown field — did you misspell something?")
case errors.Is(err, config.ErrMissingRequiredField):
    fmt.Fprintln(os.Stderr, "config is missing a required field")
case errors.Is(err, config.ErrTailscaleBindRequired):
    // Matches ErrListenLoopback, ErrListenUnspecified, ErrListenPublic.
    fmt.Fprintln(os.Stderr, "listen_addr must be a Tailscale CGNAT address (100.64.0.0/10)")
case errors.Is(err, config.ErrTailscaleRequired):
    fmt.Fprintln(os.Stderr, "[network] require_tailscale must be true in v0.1.0")
case errors.Is(err, config.ErrPathPrefixInvalid):
    fmt.Fprintln(os.Stderr, "path_prefix must be 6-32 URL-safe chars")
case errors.Is(err, config.ErrAuditLogEscape):
    fmt.Fprintln(os.Stderr, "audit_log must resolve under state_dir")
case errors.Is(err, config.ErrStateDirNotFound):
    fmt.Fprintln(os.Stderr, "state_dir does not exist — run `hush init` first")
case errors.Is(err, config.ErrArgonMemoryTooLow),
     errors.Is(err, config.ErrArgonTimeTooLow),
     errors.Is(err, config.ErrArgonThreadsTooLow):
    fmt.Fprintln(os.Stderr, "Argon2id parameters below the constitutional floor (time≥4, memory≥256, threads≥4)")
case errors.Is(err, config.ErrSupervisorTTLOutOfRange):
    fmt.Fprintln(os.Stderr, "max_supervisor_ttl must be > jwt_default_ttl and ≤ 24h")
default:
    fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
}
return nil, err
```

The full sentinel catalogue is in [contracts/api.md](./contracts/api.md#sentinel-error-catalogue). Multi-violation reports are `errors.Join`-style: every sentinel matchable individually.

---

## 3. Reach into the loaded config

The struct is plain data. Typical SDD-10 wiring:

```go
// Bind the listen address.
listener, err := net.Listen("tcp", s.Server.ListenAddr.String())
if err != nil { ... }

// Choose the audit-log path.
auditLog, err := os.OpenFile(s.Server.AuditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
if err != nil { ... }

// Look up the Discord bot token from the Keychain (SDD-10's job — not SDD-06's).
token, err := keychain.Get(s.Discord.BotTokenKeychainItem)
if err != nil { ... }

// Configure crypto parameters.
sessionKey, err := keys.DeriveJWTSigningKey(seed) // uses Argon2id params from s.Crypto
if err != nil { ... }
sessionTTL := s.Crypto.JWTDefaultTTL // already a parsed time.Duration
```

The `*config.Server` value carries only non-secret data. Secrets (Discord bot token, vault passphrase) are fetched from the Keychain at startup using the names the config provides.

---

## 4. Test redaction in your own package

Downstream packages MUST NOT add fields to the `Server` struct that hold secret values. The schema is fixed at SDD-06 and frozen. To verify your downstream code does not put a secret into an unrelated config field, write a test that loads a valid config and asserts the secret is not present:

```go
func TestServerConfig_HasNoSecretFields(t *testing.T) {
    cfg, err := config.LoadServer(t.Context(), "testdata/valid/full-default.toml")
    require.NoError(t, err)

    // The Discord bot token name is non-secret (e.g. "hush-discord").
    require.Equal(t, "hush-discord", cfg.Discord.BotTokenKeychainItem)

    // Sanity: assert no field holds a string that looks like a token.
    // (Test is illustrative; the schema is fixed and the audit is at SDD-06 review time.)
}
```

---

## 5. Author a `~/.hush/config.toml` (operator workflow)

The schema documented in `docs/CONFIG-SCHEMA.md` is authoritative. Minimum-viable operator config:

```toml
[server]
listen_addr = "100.96.10.4:7743"     # your Tailscale IP, port 7743
path_prefix = "a8k2f9"                # generated by hush init; treat as obscurity layer only
discord_owner_id = "123456789012345678"
client_registry = "~/.hush/clients.json"
state_dir = "~/.hush"
audit_log = "~/.hush/audit.jsonl"

[discord]
bot_token_keychain_item = "hush-discord"
application_id = "345678901234567890"
```

Every other section ([crypto], [network], [security]) gets defaulted automatically. `hush init` (SDD-15) will generate a fully-populated TOML file with comments; the loader accepts both that and the minimum-viable form above.

---

## 6. What you MUST NOT do

- **Do not mutate the returned `*Server`**. The locked contract is "read-only after `LoadServer` returns". Mutation is undefined behaviour.
- **Do not add a "secret" field to the `Server` struct in a future chunk**. The struct is frozen by Constitution X. If you need to thread a new piece of secret material into the runtime, fetch it via the Keychain at startup and store it in a separate runtime struct (not in the loaded config).
- **Do not call `LoadServer` from a hot path**. It is a once-at-startup function; calling it on every request would re-read the file from disk and re-stat `state_dir` — wasteful and racy. Cache the `*Server` for the lifetime of the process.
- **Do not assume `LoadServer` watches the file**. There is no inotify, no fsnotify, no SIGHUP rebind. SIGHUP-driven vault reload (SDD-10) is for the vault file, not the config file. Config changes require a process restart.
- **Do not pass operator-supplied env-var overrides into the loader**. There is no `HUSH_LISTEN_ADDR` env var, no `HUSH_PATH_PREFIX` env var. The TOML file is the only source. (See FR-007: env-var-driven secret-field reads are constitutionally forbidden.)
- **Do not use this loader for supervisor config**. Supervisor config has its own loader (SDD-18 — `LoadSupervisor`) in the same package. Don't try to load a supervisor TOML through `LoadServer` — the schemas are different.
- **Do not bypass `Validate`**. If you construct a `*Server` programmatically (rare), call `Validate` before threading it into any consumer. The loader runs `Validate` for you; programmatic construction skips it.

---

## 7. Deferring to the constitution

If a question is not answered above, the answers in priority order are:
1. `.specify/memory/constitution.md` Principles III, VI, VIII, IX, X, XI.
2. `docs/CONFIG-SCHEMA.md` (the authoritative schema).
3. `docs/SPEC.md` FR-8, FR-15, AC-8.
4. `docs/sdd/SDD-06.md` (the chunk contract).
5. The contract document at [contracts/api.md](./contracts/api.md).

All of the above are checked-in source-of-truth — when they conflict, the constitution wins, and any drift between the others is an issue to file before writing code.
